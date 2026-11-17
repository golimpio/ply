package trailerdl

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ply/internal/hwaccel"
	"ply/internal/store"
)

// Resolver gives the streams of a video of one site.
type Resolver interface {
	Resolve(ctx context.Context, key string) (Source, error)
}

// Fetcher downloads trailers.
type Fetcher struct {
	YouTube Resolver
	Vimeo   Resolver
	HTTP    *http.Client
	FFmpeg  string
	Encoder func() hwaccel.Encoder // the encoder for a stream that is not H.264
	Chunk   int64                  // the size of a ranged request; 0 = 10 MiB, as yt-dlp asks YouTube
}

// Steps are what Fetch tells its caller as it goes.
type Steps struct {
	// Progress is the bytes downloaded of the bytes known so far, and what Fetch is doing.
	Progress func(done, total int64, note string)
	// Converting is called before a stream is converted to H.264 (the CPU's work): it may
	// wait, and an error stops the fetch.
	Converting func(ctx context.Context) error
}

// What Fetch says it is doing.
const (
	NoteDownloading = "Downloading"
	NoteConverting  = "Converting to H.264"
)

// Fetch downloads the video v into dir, and returns the path of the MP4 it made there:
// H.264 (converted if the site had none at the best height) and the site's audio.
func (f *Fetcher) Fetch(ctx context.Context, v store.Video, dir string, s Steps) (string, error) {
	var r Resolver
	switch v.Site {
	case store.SiteYouTube:
		r = f.YouTube
	case store.SiteVimeo:
		r = f.Vimeo
	}
	if r == nil {
		return "", fmt.Errorf("trailerdl: no downloader for %q", v.Site)
	}
	src, err := r.Resolve(ctx, v.Key)
	if err != nil {
		return "", err
	}
	if src.Wait > 0 { // the site's start ad: asking for the media before it ends gets a 403
		t := time.NewTimer(src.Wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return "", ctx.Err()
		case <-t.C:
		}
	}
	report := func(done, total int64, note string) {
		if s.Progress != nil {
			s.Progress(done, total, note)
		}
	}

	var inputs []string
	h264, aac := true, true
	if src.HLS != "" { // Vimeo's HLS is H.264 with AAC, read by ffmpeg itself
		report(0, 0, NoteDownloading)
		inputs = append(inputs, "-user_agent", src.Header.Get("User-Agent"), "-referer", src.Header.Get("Referer"), "-i", src.HLS)
	} else {
		p := &progress{report: report, total: src.Video.Size + src.Audio.Size}
		video := filepath.Join(dir, "video")
		if err := f.download(ctx, src.Video, src.Header, video, p); err != nil {
			return "", err
		}
		inputs = append(inputs, "-i", video)
		h264 = src.Video.codec("avc1")
		if src.Audio.URL != "" {
			audio := filepath.Join(dir, "audio")
			if err := f.download(ctx, src.Audio, src.Header, audio, p); err != nil {
				return "", err
			}
			inputs = append(inputs, "-i", audio)
			aac = src.Audio.codec("mp4a")
		}
	}

	args := append([]string{"-v", "error", "-y"}, inputs...)
	args = append(args, "-map", "0:v:0")
	if src.Audio.URL != "" {
		args = append(args, "-map", "1:a:0")
	} else {
		args = append(args, "-map", "0:a:0?")
	}
	if h264 {
		args = append(args, "-c:v", "copy")
	} else {
		report(0, 0, NoteConverting)
		if s.Converting != nil {
			if err := s.Converting(ctx); err != nil {
				return "", err
			}
		}
		enc := hwaccel.SoftwareEncoder()
		if f.Encoder != nil {
			enc = f.Encoder()
		}
		rate := int(min(max(2*src.Video.Bitrate, 3_000_000), 10_000_000)) // H.264 needs about twice AV1's or VP9's
		args = append(append(args, hwaccel.EncodeArgs(enc, rate)...),
			"-pix_fmt", "yuv420p", "-force_key_frames", "expr:gte(t,n_forced*2)") // a keyframe every 2 s: seeking
	}
	if aac {
		args = append(args, "-c:a", "copy")
	} else {
		args = append(args, "-c:a", "aac", "-b:a", "192k")
	}
	out := filepath.Join(dir, "trailer.mp4")
	args = append(args, "-movflags", "+faststart", out)
	if res, err := exec.CommandContext(ctx, f.FFmpeg, args...).CombinedOutput(); err != nil {
		os.Remove(out)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(lastLine(res)))
	}
	return out, nil
}

func lastLine(b []byte) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return lines[len(lines)-1]
}

// progress adds up the bytes of the streams a fetch downloads.
type progress struct {
	report      func(done, total int64, note string)
	done, total int64
}

func (p *progress) add(n int64) {
	p.done += n
	p.report(p.done, max(p.total, p.done), NoteDownloading)
}

// download writes the stream to path, in ranged requests of about f.Chunk (95 to 100 % of
// it, as yt-dlp asks: YouTube throttles a single long request).
func (f *Fetcher) download(ctx context.Context, s Stream, h http.Header, path string, p *progress) error {
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()
	chunk := f.Chunk
	if chunk <= 0 {
		chunk = 10 << 20
	}
	c := f.HTTP
	if c == nil {
		c = http.DefaultClient
	}
	total := s.Size
	for from := int64(0); total == 0 || from < total; {
		n := chunk - rand.Int64N(chunk/20+1)
		to := from + n - 1
		if total > 0 {
			to = min(to, total-1)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
		if err != nil {
			return err
		}
		for k, v := range h {
			req.Header[k] = v
		}
		req.Header.Set("Range", "bytes="+strconv.FormatInt(from, 10)+"-"+strconv.FormatInt(to, 10))
		resp, err := c.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("download: %w", err)
		}
		switch resp.StatusCode {
		case http.StatusPartialContent:
			if t := contentRangeTotal(resp.Header.Get("Content-Range")); t > 0 && total == 0 {
				total = t
				p.total += t
			}
		case http.StatusOK: // the whole file: the server ignored the range
			total = -1
		case http.StatusRequestedRangeNotSatisfiable:
			resp.Body.Close()
			return nil // past the end of a stream whose size was not known
		default:
			resp.Body.Close()
			return fmt.Errorf("download: HTTP %d", resp.StatusCode)
		}
		got, err := io.Copy(out, resp.Body)
		resp.Body.Close()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("download: %w", err)
		}
		p.add(got)
		if total < 0 || got == 0 {
			break
		}
		from += got
	}
	return out.Close()
}

// contentRangeTotal is the total of "bytes 0-99/1234" (0: not given).
func contentRangeTotal(v string) int64 {
	_, t, ok := strings.Cut(v, "/")
	if !ok {
		return 0
	}
	n, _ := strconv.ParseInt(t, 10, 64)
	return n
}
