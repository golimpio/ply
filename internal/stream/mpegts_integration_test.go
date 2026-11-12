//go:build integration

package stream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ply/internal/hwaccel"
)

// Exercise the incompatible source shape: HEVC Main10, 60 fps and AAC 5.1 at
// 44.1 kHz. Legacy output must narrow all of those capabilities, including when
// the requested starting segment is far from the beginning.
func TestMPEGTSLegacyEnvelopeResumeAndSeek(t *testing.T) {
	src := generate(t, "legacy-source.mkv",
		"-f", "lavfi", "-i", "testsrc2=size=128x72:rate=60",
		"-f", "lavfi", "-i", "anullsrc=r=44100:cl=5.1",
		"-t", "66.25", "-c:v", "libx265", "-preset", "ultrafast", "-pix_fmt", "yuv420p10le",
		"-x265-params", "keyint=120:min-keyint=120:scenecut=0:open-gop=0:log-level=error",
		"-c:a", "aac", "-b:a", "96k")
	transcodeVariants(t, func(t *testing.T, info hwaccel.Info) {
		spec := info.Plan(hwaccel.Request{Codec: "hevc", BitDepth: 10, Width: 128, Height: 72, VideoIndex: 0, BurnSubtitle: -1, MaxHeight: 1080})
		request := Request{
			Path: src, Container: "mkv", DurationSec: 66.25, VideoIndex: 0, VideoCodec: "hevc",
			AudioIndex: 1, AudioCodec: "aac", AudioCopy: true, MPEGTS: true, StartSec: 44.9,
			Video: &VideoSpec{Codec: "hevc", Encode: spec.Encode, Pre: spec.Pre, Filter: spec.Filter},
		}
		m := newManager(t, func(o *Options) {
			o.Tick, o.WaitTimeout = 5*time.Millisecond, 45*time.Second
			o.ThrottleHigh, o.ThrottleLow, o.Lookahead = 1, 0, 6
		})
		sess, err := m.Create(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
		defer cancel()
		playlist, err := sess.Playlist(ctx, "?token=receiver")
		if err != nil || !strings.Contains(playlist, "#EXT-X-START:TIME-OFFSET=44.900") {
			t.Fatalf("resumed playlist: %v\n%s", err, playlist)
		}
		runs := map[int]bool{}
		var firstRunSegments [][]byte
		for _, idx := range []int{7, 8, 0, 10, 2, 11} {
			media, err := sess.OpenSegment(ctx, idx)
			if err != nil {
				t.Fatalf("segment %d: %v", idx, err)
			}
			data := readAll(t, media)
			sess.mu.Lock()
			runID := sess.run.id
			sess.mu.Unlock()
			runs[runID] = true
			if idx == 7 || idx == 8 {
				firstRunSegments = append(firstRunSegments, data)
			}
			path := filepath.Join(t.TempDir(), fmt.Sprintf("seg%d.ts", idx))
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, ok := checkTSFile(path); !ok {
				t.Fatalf("segment %d is not complete MPEG-TS", idx)
			}
			video, audio := probeSpan(t, nil, data)
			seg := sess.Plan().Segments[idx]
			want := seg.Start + TimelineOffset
			near(t, fmt.Sprintf("segment %d video start", idx), video.first, want, 0.10)
			near(t, fmt.Sprintf("segment %d video end", idx), video.end, want+seg.Duration, 0.10)
			near(t, fmt.Sprintf("segment %d audio start", idx), audio.first, want, 0.15)
			near(t, fmt.Sprintf("segment %d audio end", idx), audio.end, want+seg.Duration, 0.15)
			probeLegacyTS(t, path)
			t.Logf("segment %d run %d: video %.3f..%.3f audio %.3f..%.3f", idx, runID, video.first, video.end, audio.first, audio.end)
		}
		if len(runs) < 3 {
			t.Fatalf("seek test exercised only %d encoder runs", len(runs))
		}
		// Consecutive segments from one run have valid continuity, while fresh
		// runs explicitly flag their counter reset in transport packets.
		checkTSContinuity(t, bytes.Join(firstRunSegments, nil))
		sess.mu.Lock()
		runStart := sess.run.start
		sess.mu.Unlock()
		last, err := sess.OpenSegment(ctx, runStart)
		if err != nil {
			t.Fatal(err)
		}
		data := readAll(t, last)
		for _, pid := range []int{256, 257} {
			found := false
			for at := 0; at < len(data); at += tsPacketSize {
				p := data[at : at+tsPacketSize]
				if int(p[1]&0x1f)<<8|int(p[2]) != pid {
					continue
				}
				found = true
				if (p[3]>>4)&2 == 0 || p[4] == 0 || p[5]&0x80 == 0 {
					t.Errorf("PID %d did not flag its fresh-run discontinuity", pid)
				}
				break
			}
			if !found {
				t.Errorf("PID %d absent from TS", pid)
			}
		}
	})
}

func probeLegacyTS(t *testing.T, path string) {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries",
		"stream=codec_type,codec_name,profile,level,pix_fmt,r_frame_rate,sample_rate,channels",
		"-of", "json", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Streams []struct {
			CodecType  string `json:"codec_type"`
			CodecName  string `json:"codec_name"`
			Profile    string
			Level      int
			PixFmt     string `json:"pix_fmt"`
			Rate       string `json:"r_frame_rate"`
			SampleRate string `json:"sample_rate"`
			Channels   int
		}
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatal(err)
	}
	for _, stream := range result.Streams {
		switch stream.CodecType {
		case "video":
			if stream.CodecName != "h264" || stream.Level != 40 || stream.PixFmt != "yuv420p" || stream.Rate != "30/1" {
				t.Errorf("legacy video envelope: %+v", stream)
			}
		case "audio":
			if stream.CodecName != "aac" || stream.Profile != "LC" || stream.Channels != 2 || stream.SampleRate != "48000" {
				t.Errorf("legacy audio envelope: %+v", stream)
			}
		}
	}
}

func checkTSContinuity(t *testing.T, data []byte) {
	t.Helper()
	last := map[int]int{}
	for at := 0; at < len(data); at += tsPacketSize {
		p := data[at : at+tsPacketSize]
		pid := int(p[1]&0x1f)<<8 | int(p[2])
		control, counter := (p[3]>>4)&3, int(p[3]&15)
		if pid == 0x1fff || control&1 == 0 {
			continue
		}
		discontinuity := control&2 != 0 && p[4] > 0 && p[5]&0x80 != 0
		if previous, ok := last[pid]; ok && !discontinuity && counter != (previous+1)&15 {
			t.Fatalf("PID %d continuity jumped %d -> %d at packet %d", pid, previous, counter, at/tsPacketSize)
		}
		last[pid] = counter
	}
}

func TestMPEGTSComplexGraphKeepsNativeTimelineAndFrameRate(t *testing.T) {
	src := generate(t, "complex.mkv", "-f", "lavfi", "-i", "testsrc2=size=128x72:rate=60",
		"-t", "12.25", "-c:v", "libx264", "-preset", "ultrafast")
	m := newManager(t, nil)
	sess, err := m.Create(t.Context(), Request{
		Path: src, Container: "mkv", DurationSec: 12.25, VideoIndex: 0, VideoCodec: "h264", AudioIndex: -1, MPEGTS: true, StartSec: 6.5,
		Video: &VideoSpec{Encode: []string{"-c:v", "libx264", "-preset", "veryfast"}, Complex: "[0:0]null[v]"},
	})
	if err != nil {
		t.Fatal(err)
	}
	media, err := sess.OpenSegment(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "complex.ts")
	if err := os.WriteFile(path, readAll(t, media), 0o600); err != nil {
		t.Fatal(err)
	}
	probeLegacyTS(t, path)
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=r_frame_rate", "-of", "csv=p=0", path).Output()
	if err != nil || !strings.Contains(string(out), "30/1") {
		t.Fatalf("complex graph did not apply the legacy fps stage: %v %s", err, out)
	}
}
