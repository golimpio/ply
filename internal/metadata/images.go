package metadata

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"ply/internal/probe"
)

// TMDBImageSizes are the sizes of TMDB images that are served and cached.
var TMDBImageSizes = []string{"w92", "w185", "w342", "w500", "w780", "w1280", "original"}

var (
	tmdbImageFile = regexp.MustCompile(`^[A-Za-z0-9_-]+\.(jpg|png|svg)$`)
	imagePath     = regexp.MustCompile(`^/?[A-Za-z0-9_-]+\.(jpg|png|svg)$`)
)

// Limits of the image cache.
const (
	MaxImageBytes   = 20 << 20
	ImageTimeout    = 30 * time.Second
	imageFailureTTL = 30 * time.Second // how long a failed download is not retried
	maxCovers       = 2                // concurrent cover extractions
)

var (
	// ErrBadImage is returned for a size or file name that is not allowed. It
	// is decided before anything touches the disk or the network.
	ErrBadImage = errors.New("metadata: not a valid image reference")
	// ErrNotRegular is returned for a path that is a directory, a FIFO, a device.
	ErrNotRegular = errors.New("metadata: not a regular file")
	// ErrImageUnavailable means the image could not be had now: the download
	// failed, TMDB is switched off, or the file has no such cover.
	ErrImageUnavailable = errors.New("metadata: image unavailable")
)

// ValidSize reports whether size is on the allowlist of TMDB image sizes.
func ValidSize(size string) bool { return slices.Contains(TMDBImageSizes, size) }

// ValidImagePath reports whether p is a TMDB file path as TMDB writes it
// ("/abc.jpg"; the slash is optional): one plain name with a jpg, png or svg
// extension, with nothing in it that could change the address it is put in.
func ValidImagePath(p string) bool { return imagePath.MatchString(p) }

// ValidTMDBImage reports whether size and file may be served: size is on the
// allowlist and file is a plain jpg, png or svg name.
func ValidTMDBImage(size, file string) bool {
	return ValidSize(size) && tmdbImageFile.MatchString(file)
}

// TMDBFile returns the file name of a "tmdb:/abc.jpg" image ref; ok is false for
// any other ref or a name that may not be served.
func TMDBFile(ref string) (file string, ok bool) {
	rest, found := strings.CutPrefix(ref, "tmdb:/")
	if !found || !tmdbImageFile.MatchString(rest) {
		return "", false
	}
	return rest, true
}

// ImageOptions configures NewImageCache.
type ImageOptions struct {
	// Dir is the cache directory (<data>/images), created if missing.
	Dir string
	// URL gives the address of a TMDB image, or false when online lookups are
	// switched off (cached images are still served).
	URL        func(ctx context.Context, size, file string) (string, bool)
	HTTPClient *http.Client
	Timeout    time.Duration // per download; default ImageTimeout
	MaxBytes   int64         // per image; default MaxImageBytes
	// FFmpeg and Niceness run the extraction of covers embedded in media files.
	FFmpeg   string
	Niceness int
	Logger   *slog.Logger
	Now      func() time.Time // replaced in tests
}

// ImageCache keeps TMDB images on disk under Dir/tmdb/<size>/<file>, fetched
// once on first use, and covers extracted from media files under Dir/embedded.
type ImageCache struct {
	dir      string
	url      func(ctx context.Context, size, file string) (string, bool)
	http     *http.Client
	timeout  time.Duration
	maxBytes int64
	ffmpeg   string
	nice     int
	log      *slog.Logger
	now      func() time.Time

	base   context.Context // parent of every download; cancelled by Close
	cancel context.CancelFunc
	wg     sync.WaitGroup
	covers chan struct{}

	mu       sync.Mutex
	closed   bool
	inflight map[string]*imageCall
	failed   map[string]time.Time // key -> when to try again
}

type imageCall struct {
	done chan struct{}
	err  error
}

// NewImageCache creates the cache directory and returns the cache.
func NewImageCache(o ImageOptions) (*ImageCache, error) {
	if o.Dir == "" {
		return nil, errors.New("metadata: image cache needs a directory")
	}
	if err := os.MkdirAll(o.Dir, 0o755); err != nil {
		return nil, err
	}
	c := &ImageCache{
		dir: o.Dir, url: o.URL, http: o.HTTPClient, timeout: o.Timeout, maxBytes: o.MaxBytes,
		ffmpeg: o.FFmpeg, nice: o.Niceness, log: o.Logger, now: o.Now,
		covers:   make(chan struct{}, maxCovers),
		inflight: map[string]*imageCall{},
		failed:   map[string]time.Time{},
	}
	if c.http == nil {
		c.http = &http.Client{}
	}
	if c.timeout <= 0 {
		c.timeout = ImageTimeout
	}
	if c.maxBytes <= 0 {
		c.maxBytes = MaxImageBytes
	}
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	if c.now == nil {
		c.now = time.Now
	}
	c.base, c.cancel = context.WithCancel(context.Background())
	return c, nil
}

// Close stops the downloads and extractions under way and waits for them. The
// cache must not be used afterwards.
func (c *ImageCache) Close() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.cancel()
	c.wg.Wait()
}

// TMDB returns the path of the cached image, downloading it first when it is
// not there. Concurrent callers for one image share one download. A request
// that is cancelled stops waiting; the download goes on for the others.
func (c *ImageCache) TMDB(ctx context.Context, size, file string) (string, error) {
	if !ValidTMDBImage(size, file) {
		return "", ErrBadImage
	}
	dest := filepath.Join(c.dir, "tmdb", size, file)
	if fi, err := os.Stat(dest); err == nil && fi.Mode().IsRegular() && fi.Size() > 0 {
		return dest, nil
	}
	if c.url == nil {
		return "", ErrImageUnavailable
	}
	if _, ok := c.url(ctx, size, file); !ok {
		return "", ErrImageUnavailable // switched off: nothing leaves the machine
	}
	key := "tmdb/" + size + "/" + file
	err := c.once(ctx, key, func(ctx context.Context) error { return c.download(ctx, size, file, dest) })
	if err != nil {
		return "", err
	}
	return dest, nil
}

// Prefetch makes sure the image of a "tmdb:/abc.jpg" ref is cached at size.
// Refs of other kinds are ignored.
func (c *ImageCache) Prefetch(ctx context.Context, size, ref string) error {
	file, ok := TMDBFile(ref)
	if !ok {
		return nil
	}
	_, err := c.TMDB(ctx, size, file)
	return err
}

// once runs fn for key unless a run is already under way, and waits for it.
// fn runs on its own goroutine under the cache's context, so that a caller
// that goes away does not abort the work for the others.
func (c *ImageCache) once(ctx context.Context, key string, fn func(ctx context.Context) error) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrImageUnavailable
	}
	if until, ok := c.failed[key]; ok {
		if c.now().Before(until) {
			c.mu.Unlock()
			return ErrImageUnavailable
		}
		delete(c.failed, key)
	}
	call, running := c.inflight[key]
	if !running {
		call = &imageCall{done: make(chan struct{})}
		c.inflight[key] = call
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			err := fn(c.base)
			c.mu.Lock()
			delete(c.inflight, key)
			if err != nil && c.base.Err() == nil {
				c.failed[key] = c.now().Add(imageFailureTTL)
			}
			c.mu.Unlock()
			call.err = err
			close(call.done)
		}()
	}
	c.mu.Unlock()

	select {
	case <-call.done:
		if call.err != nil {
			return ErrImageUnavailable
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// download fetches one image into dest through a temporary file in the same
// directory, renamed into place only when the whole image has arrived.
func (c *ImageCache) download(ctx context.Context, size, file, dest string) (err error) {
	target, ok := c.url(ctx, size, file)
	if !ok {
		return ErrImageUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "image/*")
	req.Header.Set("User-Agent", "ply")
	resp, err := c.http.Do(req)
	if err != nil {
		return redactURL(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("image %s/%s: %s", size, file, resp.Status)
	}
	if resp.ContentLength > c.maxBytes {
		return fmt.Errorf("image %s/%s: %d bytes is over the limit", size, file, resp.ContentLength)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".dl-*")
	if err != nil {
		return err
	}
	defer func() {
		tmp.Close()
		if err != nil {
			os.Remove(tmp.Name())
		}
	}()
	n, err := io.Copy(tmp, io.LimitReader(resp.Body, c.maxBytes+1))
	if err != nil {
		return redactURL(err)
	}
	if n > c.maxBytes {
		return fmt.Errorf("image %s/%s: over the %d byte limit", size, file, c.maxBytes)
	}
	if n == 0 {
		return fmt.Errorf("image %s/%s: empty", size, file)
	}
	head := make([]byte, 16)
	hn, _ := tmp.ReadAt(head, 0)
	if !looksLike(filepath.Ext(file), head[:hn]) {
		return fmt.Errorf("image %s/%s: not a %s file", size, file, filepath.Ext(file))
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dest)
}

// looksLike checks the first bytes of a download against its extension, so that
// an error page served with status 200 is not cached as a picture.
func looksLike(ext string, head []byte) bool {
	switch ext {
	case ".jpg":
		return bytes.HasPrefix(head, []byte{0xFF, 0xD8})
	case ".png":
		return bytes.HasPrefix(head, []byte("\x89PNG"))
	case ".svg":
		h := bytes.TrimLeft(bytes.TrimPrefix(head, []byte{0xEF, 0xBB, 0xBF}), " \t\r\n")
		return bytes.HasPrefix(h, []byte("<"))
	}
	return false
}

// redactURL drops the URL net/http puts in its errors (an image URL is not
// secret, but there is no reason to log it either).
func redactURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// Embedded returns the path of the cover extracted from the media file: stream
// is the ffprobe index of the attached picture. The image is extracted on first
// use (at reduced priority, a few at a time) and again when the media file is
// newer than the extracted image.
func (c *ImageCache) Embedded(ctx context.Context, itemID int64, media string, stream int) (string, error) {
	dest := filepath.Join(c.dir, "embedded", strconv.FormatInt(itemID, 10)+".jpg")
	mfi, err := os.Stat(media)
	if err != nil || !mfi.Mode().IsRegular() {
		return "", ErrImageUnavailable
	}
	if fi, err := os.Stat(dest); err == nil && fi.Size() > 0 && !fi.ModTime().Before(mfi.ModTime()) {
		return dest, nil
	}
	key := fmt.Sprintf("embedded/%d/%d", itemID, mfi.ModTime().UnixNano())
	err = c.once(ctx, key, func(ctx context.Context) error {
		select {
		case c.covers <- struct{}{}:
			defer func() { <-c.covers }()
		case <-ctx.Done():
			return ctx.Err()
		}
		return probe.ExtractCover(ctx, c.ffmpeg, media, stream, dest, c.nice)
	})
	if err != nil {
		return "", err
	}
	return dest, nil
}

// localExts are the image types served from the user's own files.
var localExts = map[string]string{
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".webp": "image/webp",
}

// OpenLocalImage opens sidecar artwork whose path came from the database. The
// path must have an image extension and lead to a regular file whose first bytes
// really are an image (a symlink "poster.jpg" to /etc/passwd is not served). It
// returns the file, positioned at its start, and the content type it sniffs as.
func OpenLocalImage(path string) (*os.File, os.FileInfo, string, error) {
	if _, ok := localExts[strings.ToLower(filepath.Ext(path))]; !ok {
		return nil, nil, "", ErrBadImage
	}
	f, err := OpenRegular(path)
	if errors.Is(err, ErrNotRegular) {
		return nil, nil, "", ErrImageUnavailable
	}
	if err != nil {
		return nil, nil, "", err
	}
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	ct := http.DetectContentType(head[:n])
	if !strings.HasPrefix(ct, "image/") {
		f.Close()
		return nil, nil, "", ErrBadImage
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, nil, "", err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, "", err
	}
	return f, fi, ct, nil
}
