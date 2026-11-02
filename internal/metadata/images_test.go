package metadata

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var jpegBytes = append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte("jpeg"), 100)...)

// imageServer serves jpegBytes (or what handler says) for any path and counts hits.
type imageServer struct {
	srv  *httptest.Server
	hits atomic.Int32
	mu   sync.Mutex
	got  []string
}

func newImageServer(t *testing.T, handler http.HandlerFunc) *imageServer {
	t.Helper()
	s := &imageServer{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		s.mu.Lock()
		s.got = append(s.got, r.URL.Path)
		s.mu.Unlock()
		if handler != nil {
			handler(w, r)
			return
		}
		w.Write(jpegBytes)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *imageServer) paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.got)
}

func newCache(t *testing.T, s *imageServer, mod ...func(*ImageOptions)) *ImageCache {
	t.Helper()
	o := ImageOptions{
		Dir: filepath.Join(t.TempDir(), "My Data", "images"), // a space on purpose
		URL: func(_ context.Context, size, file string) (string, bool) {
			return s.srv.URL + "/t/p/" + size + "/" + file, true
		},
	}
	for _, m := range mod {
		m(&o)
	}
	c, err := NewImageCache(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

// tree lists every file under dir, relative, so that a test can see that
// nothing was written where it should not be.
func filesUnder(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

func TestImageSizeAllowlist(t *testing.T) {
	srv := newImageServer(t, nil)
	c := newCache(t, srv)
	for _, size := range TMDBImageSizes {
		if got, err := c.TMDB(t.Context(), size, "abc.jpg"); err != nil || !strings.HasSuffix(got, filepath.Join("tmdb", size, "abc.jpg")) {
			t.Errorf("size %s: %q, %v", size, got, err)
		}
	}
	if want := []string{"original", "w1280", "w185", "w342", "w500", "w780", "w92"}; len(TMDBImageSizes) != 7 || !slices.Equal(sorted(TMDBImageSizes), sorted(want)) {
		t.Errorf("allowlist = %v, want exactly %v", TMDBImageSizes, want)
	}
	hits := srv.hits.Load()
	for _, size := range []string{"", "w100", "w342 ", " w342", "W342", "w3420", "w34", "original/", "../w342", "w342/..", "tmdb", "full", "w342\x00", "w342%2f", "orig", "w1281", "h632", "w300"} {
		if got, err := c.TMDB(t.Context(), size, "abc.jpg"); !errors.Is(err, ErrBadImage) || got != "" {
			t.Errorf("size %q: %q, %v; want ErrBadImage", size, got, err)
		}
		if ValidTMDBImage(size, "abc.jpg") {
			t.Errorf("ValidTMDBImage(%q) = true", size)
		}
	}
	if srv.hits.Load() != hits {
		t.Errorf("a bad size reached the image server (%d requests)", srv.hits.Load()-hits)
	}
}

func sorted(s []string) []string { c := slices.Clone(s); slices.Sort(c); return c }

func TestImageFileNameRegex(t *testing.T) {
	srv := newImageServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".png") {
			w.Write([]byte("\x89PNG\r\n\x1a\nxxxx"))
		} else if strings.HasSuffix(r.URL.Path, ".svg") {
			w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`))
		} else {
			w.Write(jpegBytes)
		}
	})
	c := newCache(t, srv)
	for _, file := range []string{"abc.jpg", "ABC_def-123.png", "a.svg", "9Zk2.jpg", "pQrfQ2nkdFr5SGiLgkpHobaxx0t.jpg", "_-_.png"} {
		if _, err := c.TMDB(t.Context(), "w342", file); err != nil {
			t.Errorf("file %q: %v", file, err)
		}
		if !ValidTMDBImage("w342", file) {
			t.Errorf("ValidTMDBImage(%q) = false", file)
		}
	}
	before := srv.hits.Load()
	for _, file := range []string{
		"", ".jpg", "jpg", "abc", "abc.gif", "abc.jpeg", "abc.webp", "abc.JPG", "abc.Png", "abc.jpg.exe", "abc.jpg ", " abc.jpg", "abc.jpg\n",
		"a b.jpg", "a/b.jpg", "/abc.jpg", "../abc.jpg", "..%2fabc.jpg", "a%2fb.jpg", `a\b.jpg`, "..\\abc.jpg", "a..jpg", "a.b.jpg", "abc.jpg/", "abc.jpg?x=1",
		"abc.jpg#x", "ab\x00c.jpg", "é.jpg", "a.jpg/../b.jpg", "./abc.jpg", "~root.jpg", "$HOME.jpg", "a;b.jpg", "a:b.jpg", "a*.jpg", strings.Repeat("a", 5000) + ".gif",
	} {
		if got, err := c.TMDB(t.Context(), "w342", file); !errors.Is(err, ErrBadImage) || got != "" {
			t.Errorf("file %q: %q, %v; want ErrBadImage", file, got, err)
		}
	}
	if srv.hits.Load() != before {
		t.Errorf("a bad file name reached the image server")
	}
	// Nothing outside <dir>/tmdb/w342 was written, and only the good files are in it.
	got := filesUnder(t, c.dir)
	want := []string{"tmdb/w342/9Zk2.jpg", "tmdb/w342/ABC_def-123.png", "tmdb/w342/_-_.png", "tmdb/w342/a.svg", "tmdb/w342/abc.jpg", "tmdb/w342/pQrfQ2nkdFr5SGiLgkpHobaxx0t.jpg"}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("files in the cache:\n got %q\nwant %q", got, want)
	}
	if entries, _ := os.ReadDir(filepath.Dir(c.dir)); len(entries) != 1 {
		t.Errorf("something was written next to the cache dir: %v", entries)
	}
}

func TestTMDBFile(t *testing.T) {
	for in, want := range map[string]string{
		"tmdb:/abc.jpg": "abc.jpg", "tmdb:/x_y-z.png": "x_y-z.png", "tmdb:/a.svg": "a.svg",
		"": "", "abc.jpg": "", "tmdb:abc.jpg": "", "tmdb://abc.jpg": "", "tmdb:/../abc.jpg": "", "tmdb:/a/b.jpg": "",
		"local:item:1:poster": "", "TMDB:/abc.jpg": "", "tmdb:/abc.gif": "",
	} {
		got, ok := TMDBFile(in)
		if got != want || ok != (want != "") {
			t.Errorf("TMDBFile(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

func TestImageIsFetchedOnceAndShared(t *testing.T) {
	gate := make(chan struct{})
	srv := newImageServer(t, func(w http.ResponseWriter, r *http.Request) {
		<-gate
		w.Write(jpegBytes)
	})
	c := newCache(t, srv)

	var wg sync.WaitGroup
	paths := make([]string, 12)
	errs := make([]error, 12)
	for i := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			paths[i], errs[i] = c.TMDB(t.Context(), "w342", "same.jpg")
		}()
	}
	// Everyone is waiting on one download.
	deadline := time.Now().Add(5 * time.Second)
	for srv.hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()
	for i := range paths {
		if errs[i] != nil || paths[i] != paths[0] {
			t.Fatalf("caller %d: %q, %v", i, paths[i], errs[i])
		}
	}
	if srv.hits.Load() != 1 {
		t.Errorf("%d downloads for 12 concurrent callers, want 1", srv.hits.Load())
	}
	b, err := os.ReadFile(paths[0])
	if err != nil || !bytes.Equal(b, jpegBytes) {
		t.Errorf("cached file: %d bytes, %v", len(b), err)
	}

	// Cached: no more requests. Positive control: another image is fetched.
	if _, err := c.TMDB(t.Context(), "w342", "same.jpg"); err != nil || srv.hits.Load() != 1 {
		t.Errorf("second use: %v, %d hits", err, srv.hits.Load())
	}
	if _, err := c.TMDB(t.Context(), "w500", "same.jpg"); err != nil || srv.hits.Load() != 2 {
		t.Errorf("another size: %v, %d hits (each size is its own file)", err, srv.hits.Load())
	}
	if got := srv.paths(); !slices.Equal(got, []string{"/t/p/w342/same.jpg", "/t/p/w500/same.jpg"}) {
		t.Errorf("paths asked: %q", got)
	}
}

func TestImageIsWrittenThroughATempFileAndRenamed(t *testing.T) {
	half := make(chan struct{})
	finish := make(chan struct{})
	srv := newImageServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(jpegBytes[:200])
		w.(http.Flusher).Flush()
		close(half)
		<-finish
		w.Write(jpegBytes[200:])
	})
	c := newCache(t, srv)
	dest := filepath.Join(c.dir, "tmdb", "w342", "slow.jpg")

	done := make(chan error, 1)
	go func() { _, err := c.TMDB(t.Context(), "w342", "slow.jpg"); done <- err }()
	<-half
	deadline := time.Now().Add(5 * time.Second)
	var tmps []string
	for time.Now().Before(deadline) {
		tmps, _ = filepath.Glob(filepath.Join(filepath.Dir(dest), ".dl-*"))
		if len(tmps) == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(tmps) != 1 {
		t.Fatalf("temp files while downloading: %v", tmps)
	}
	if _, err := os.Stat(dest); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the final file exists while the download is half done: %v", err)
	}
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(dest); err != nil || !bytes.Equal(b, jpegBytes) {
		t.Errorf("final file: %d bytes, %v", len(b), err)
	}
	if got := filesUnder(t, c.dir); !slices.Equal(got, []string{"tmdb/w342/slow.jpg"}) {
		t.Errorf("files after the download: %q", got)
	}
}

func TestImageSizeCap(t *testing.T) {
	for name, tc := range map[string]struct {
		body    int
		chunked bool
		ok      bool
	}{
		"under":                {900, false, true},
		"exactly the cap":      {1024, false, true},
		"one byte over":        {1025, false, false},
		"far over":             {10 * 1024, false, false},
		"over, no length sent": {1025, true, false},
		"exactly, no length":   {1024, true, true},
	} {
		srv := newImageServer(t, func(w http.ResponseWriter, r *http.Request) {
			b := append([]byte{0xFF, 0xD8}, bytes.Repeat([]byte("x"), tc.body-2)...)
			if tc.chunked {
				w.(http.Flusher).Flush() // no Content-Length
			}
			w.Write(b)
		})
		c := newCache(t, srv, func(o *ImageOptions) { o.MaxBytes = 1024 })
		got, err := c.TMDB(t.Context(), "w342", "big.jpg")
		if tc.ok != (err == nil) {
			t.Errorf("%s: err = %v, ok wanted %v", name, err, tc.ok)
		}
		files := filesUnder(t, c.dir)
		if tc.ok && (len(files) != 1 || got == "") {
			t.Errorf("%s: files %q", name, files)
		}
		if !tc.ok && (len(files) != 0 || got != "") {
			t.Errorf("%s: a refused image left files behind: %q", name, files)
		}
	}
	if MaxImageBytes != 20<<20 {
		t.Errorf("MaxImageBytes = %d, want 20 MiB", MaxImageBytes)
	}
}

func TestImageFailuresAreNotCachedForLongOrWrittenToDisk(t *testing.T) {
	var status atomic.Int32
	status.Store(404)
	srv := newImageServer(t, func(w http.ResponseWriter, r *http.Request) {
		if s := int(status.Load()); s != 200 {
			http.Error(w, "nope", s)
			return
		}
		w.Write(jpegBytes)
	})
	clock := newFakeClock()
	c := newCache(t, srv, func(o *ImageOptions) { o.Now = clock.now })

	for i := 0; i < 3; i++ {
		if _, err := c.TMDB(t.Context(), "w342", "gone.jpg"); !errors.Is(err, ErrImageUnavailable) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if srv.hits.Load() != 1 {
		t.Errorf("%d requests for 3 attempts, want 1 (a failure is remembered for a while)", srv.hits.Load())
	}
	if files := filesUnder(t, c.dir); len(files) != 0 {
		t.Errorf("files after failures: %q", files)
	}
	// Positive control: after the wait, and once TMDB has it, it is fetched.
	status.Store(200)
	clock.advance(imageFailureTTL + time.Second)
	if _, err := c.TMDB(t.Context(), "w342", "gone.jpg"); err != nil || srv.hits.Load() != 2 {
		t.Errorf("after the wait: %v, %d hits", err, srv.hits.Load())
	}

	for name, h := range map[string]http.HandlerFunc{
		"server error":  func(w http.ResponseWriter, r *http.Request) { http.Error(w, "x", 500) },
		"html as 200":   func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>captive portal</html>")) },
		"empty body":    func(w http.ResponseWriter, r *http.Request) {},
		"png for jpg":   func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("\x89PNG\r\n\x1a\n")) },
		"redirect loop": func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, r.URL.Path, http.StatusFound) },
		"cut off mid-way": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "5000")
			w.Write(jpegBytes[:100])
		},
	} {
		srv := newImageServer(t, h)
		c := newCache(t, srv)
		if got, err := c.TMDB(t.Context(), "w342", "bad.jpg"); !errors.Is(err, ErrImageUnavailable) || got != "" {
			t.Errorf("%s: %q, %v", name, got, err)
		}
		if files := filesUnder(t, c.dir); len(files) != 0 {
			t.Errorf("%s: files left behind: %q", name, files)
		}
	}
}

func TestImageDownloadTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := newImageServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(release) })
	c := newCache(t, srv, func(o *ImageOptions) { o.Timeout = 100 * time.Millisecond })
	start := time.Now()
	if _, err := c.TMDB(t.Context(), "w342", "hang.jpg"); !errors.Is(err, ErrImageUnavailable) {
		t.Errorf("err = %v", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("the timeout took %v", took)
	}
	if files := filesUnder(t, c.dir); len(files) != 0 {
		t.Errorf("files after a timeout: %q", files)
	}
}

func TestImageOfflineAndClosed(t *testing.T) {
	srv := newImageServer(t, nil)
	on := &atomic.Bool{}
	c := newCache(t, srv, func(o *ImageOptions) {
		o.URL = func(_ context.Context, size, file string) (string, bool) { return srv.srv.URL + "/" + file, on.Load() }
	})
	if _, err := c.TMDB(t.Context(), "w342", "off.jpg"); !errors.Is(err, ErrImageUnavailable) || srv.hits.Load() != 0 {
		t.Errorf("switched off: %v, %d hits", err, srv.hits.Load())
	}
	on.Store(true)
	if _, err := c.TMDB(t.Context(), "w342", "off.jpg"); err != nil {
		t.Fatal(err)
	}
	// What is cached is served even when switched off again.
	on.Store(false)
	if got, err := c.TMDB(t.Context(), "w342", "off.jpg"); err != nil || got == "" {
		t.Errorf("cached image while switched off: %q, %v", got, err)
	}

	c2 := newCache(t, srv)
	c2.Close()
	c2.Close() // idempotent
	if _, err := c2.TMDB(t.Context(), "w342", "late.jpg"); !errors.Is(err, ErrImageUnavailable) {
		t.Errorf("after Close: %v", err)
	}

	if _, err := NewImageCache(ImageOptions{}); err == nil {
		t.Error("NewImageCache without a directory succeeded")
	}
	nilURL, _ := NewImageCache(ImageOptions{Dir: t.TempDir()})
	defer nilURL.Close()
	if _, err := nilURL.TMDB(t.Context(), "w342", "x.jpg"); !errors.Is(err, ErrImageUnavailable) {
		t.Errorf("no URL function: %v", err)
	}
}

func TestImageCallerCancelDoesNotAbortTheDownload(t *testing.T) {
	gate := make(chan struct{})
	srv := newImageServer(t, func(w http.ResponseWriter, r *http.Request) {
		<-gate
		w.Write(jpegBytes)
	})
	c := newCache(t, srv)

	ctx, cancel := context.WithCancel(t.Context())
	first := make(chan error, 1)
	go func() { _, err := c.TMDB(ctx, "w342", "shared.jpg"); first <- err }()
	for srv.hits.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled caller: %v", err)
	}
	second := make(chan error, 1)
	go func() { _, err := c.TMDB(t.Context(), "w342", "shared.jpg"); second <- err }()
	time.Sleep(30 * time.Millisecond)
	close(gate)
	if err := <-second; err != nil {
		t.Errorf("second caller: %v", err)
	}
	if srv.hits.Load() != 1 {
		t.Errorf("%d downloads, want the one the first caller started", srv.hits.Load())
	}
}

func TestImageCloseStopsAHungDownload(t *testing.T) {
	block := make(chan struct{})
	srv := newImageServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(block) })
	c := newCache(t, srv)
	errc := make(chan error, 1)
	go func() { _, err := c.TMDB(t.Context(), "w342", "hung.jpg"); errc <- err }()
	for srv.hits.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	done := make(chan struct{})
	go func() { c.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return while a download hung")
	}
	if err := <-errc; err == nil {
		t.Error("the waiting caller got an image")
	}
}

func TestPrefetch(t *testing.T) {
	srv := newImageServer(t, nil)
	c := newCache(t, srv)
	if err := c.Prefetch(t.Context(), "w342", "tmdb:/pre.jpg"); err != nil || !slices.Equal(filesUnder(t, c.dir), []string{"tmdb/w342/pre.jpg"}) {
		t.Errorf("Prefetch: %v, %q", err, filesUnder(t, c.dir))
	}
	for _, ref := range []string{"", "local:item:1:poster", "tmdb:/../x.jpg", "embedded:3"} {
		before := srv.hits.Load()
		if err := c.Prefetch(t.Context(), "w342", ref); err != nil || srv.hits.Load() != before {
			t.Errorf("Prefetch(%q) = %v, %d requests; refs that are not TMDB's are ignored", ref, err, srv.hits.Load()-before)
		}
	}
	if err := c.Prefetch(t.Context(), "w42", "tmdb:/pre.jpg"); !errors.Is(err, ErrBadImage) {
		t.Errorf("Prefetch with a bad size = %v", err)
	}
}

var (
	pngBytes  = append([]byte("\x89PNG\r\n\x1a\n"), []byte("png-data-png-data")...)
	webpBytes = append([]byte("RIFF\x20\x00\x00\x00WEBPVP8 "), []byte("webp-data-webp-data")...)
)

func TestOpenLocalImage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Movies Dir")
	if err := os.MkdirAll(filepath.Join(dir, "folder.jpg"), 0o755); err != nil { // a directory named like an image
		t.Fatal(err)
	}
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for name, tc := range map[string]struct {
		data []byte
		ct   string
	}{"a.jpg": {jpegBytes, "image/jpeg"}, "b.JPEG": {jpegBytes, "image/jpeg"}, "c.png": {pngBytes, "image/png"}, "d.webp": {webpBytes, "image/webp"},
		// What the bytes are decides, not the name: a PNG called .jpg is served as a PNG.
		"e.jpg": {pngBytes, "image/png"}} {
		f, fi, got, err := OpenLocalImage(write(name, tc.data))
		if err != nil || got != tc.ct || fi.Size() != int64(len(tc.data)) {
			t.Errorf("%s: %q, %v", name, got, err)
			continue
		}
		b := make([]byte, 4)
		if n, _ := f.Read(b); n != 4 || b[0] != tc.data[0] {
			t.Errorf("%s: the file was not rewound after sniffing (read %x)", name, b[:n])
		}
		f.Close()
	}

	// Not images, whatever their names: refused without being sent.
	for name, data := range map[string][]byte{
		"passwd.jpg": []byte("root:x:0:0:root:/root:/bin/sh\nsecret\n"), "page.png": []byte("<html><body>hi</body></html>"),
		"empty.jpg": nil, "tiny.jpg": []byte("x"), "svg.webp": []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`),
		"zeros.jpg": make([]byte, 600), "script.jpg": []byte("<script>alert(1)</script>"),
	} {
		if f, _, _, err := OpenLocalImage(write(name, data)); !errors.Is(err, ErrBadImage) {
			if f != nil {
				f.Close()
			}
			t.Errorf("%s: err = %v; want ErrBadImage (the bytes are not an image)", name, err)
		}
	}
	// Not an image extension.
	for _, name := range []string{"e.txt", "f.svg", "g", "h.gif", "i.bmp"} {
		if _, _, _, err := OpenLocalImage(write(name, jpegBytes)); !errors.Is(err, ErrBadImage) {
			t.Errorf("%s: %v; want ErrBadImage (not an image extension)", name, err)
		}
	}
	// A symlink to something that is not an image: the target is what counts.
	secret := write("secret.conf", []byte("password=hunter2\n"))
	link := filepath.Join(dir, "poster.jpg")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := OpenLocalImage(link); !errors.Is(err, ErrBadImage) {
		t.Errorf("symlink to a config file: %v", err)
	}
	// ...and to an image: fine.
	good := filepath.Join(dir, "linked.jpg")
	if err := os.Symlink(filepath.Join(dir, "a.jpg"), good); err != nil {
		t.Fatal(err)
	}
	if f, _, ct, err := OpenLocalImage(good); err != nil || ct != "image/jpeg" {
		t.Errorf("symlink to an image: %q, %v", ct, err)
	} else {
		f.Close()
	}

	if _, _, _, err := OpenLocalImage(filepath.Join(dir, "folder.jpg")); !errors.Is(err, ErrImageUnavailable) {
		t.Errorf("a directory named folder.jpg: %v", err)
	}
	if _, _, _, err := OpenLocalImage(filepath.Join(dir, "missing.jpg")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
}

func TestImageInputValidation(t *testing.T) {
	for _, tc := range []struct {
		size string
		ok   bool
	}{{"w92", true}, {"original", true}, {"w1280", true}, {"", false}, {"w100", false}, {"w342/../../../3/configuration", false}, {"w342?x=1", false},
		{"w342#", false}, {" w342", false}, {"W342", false}} {
		if ValidSize(tc.size) != tc.ok {
			t.Errorf("ValidSize(%q) = %v", tc.size, !tc.ok)
		}
	}
	for _, tc := range []struct {
		path string
		ok   bool
	}{{"/abc.jpg", true}, {"abc.jpg", true}, {"/A_b-9.png", true}, {"/a.svg", true}, {"", false}, {"/", false}, {"/../../x", false}, {"/../x.jpg", false},
		{"/a.jpg?api_key=x#", false}, {"/a.jpg?api_key=x", false}, {"//a.jpg", false}, {"/a/b.jpg", false}, {"/a.gif", false}, {"/a.JPG", false},
		{"/a b.jpg", false}, {"/a.jpg\n", false}, {"/a\x00.jpg", false}, {"/é.jpg", false}, {`/a\b.jpg`, false}, {"/.jpg", false}, {"/a..jpg", false},
		{"http://evil/x.jpg", false}, {"/a.jpg#frag", false}, {"/%2e%2e/a.jpg", false}} {
		if ValidImagePath(tc.path) != tc.ok {
			t.Errorf("ValidImagePath(%q) = %v", tc.path, !tc.ok)
		}
	}

	// A path out of TMDB's mouth never becomes an address or a ref that leaves /t/p.
	c := NewClient(Options{ImageBaseURL: "https://image.tmdb.org/t/p"})
	for _, tc := range []struct{ size, file string }{
		{"w342/../../../3/configuration", "/a.jpg?api_key=x#"}, {"w342", "/a.jpg?api_key=x#"}, {"w342/..", "/a.jpg"}, {"w342", "/../../x"},
		{"w342", ""}, {"", "/a.jpg"}, {"w9", "/a.jpg"}, {"w342", "/a/b.jpg"}, {"w342", "//evil.example/a.jpg"},
	} {
		if got := c.ImageURL(tc.size, tc.file); got != "" {
			t.Errorf("ImageURL(%q, %q) = %q, want \"\"", tc.size, tc.file, got)
		}
		if got := NewTMDB(c).ImageURL(tc.size, tc.file); got != "" {
			t.Errorf("provider ImageURL(%q, %q) = %q", tc.size, tc.file, got)
		}
	}
	if got := c.ImageURL("w342", "/abc.jpg"); got != "https://image.tmdb.org/t/p/w342/abc.jpg" {
		t.Errorf("ImageURL = %q", got)
	}
	for in, want := range map[string]string{
		"/abc.jpg": "tmdb:/abc.jpg", "abc.jpg": "tmdb:/abc.jpg", "/../../x": "", "/a.jpg?api_key=x#": "", "": "", "/a/b.jpg": "", "/a.gif": "",
		"http://x/y.jpg": "", "/x.jpg\n": "",
	} {
		if got := ImageRef(in); got != want {
			t.Errorf("ImageRef(%q) = %q, want %q", in, got, want)
		}
	}
	// And the settings, which hand out the address for downloads.
	st := NewStaticSource(NewTMDB(c))
	for _, tc := range []struct{ size, file string }{{"w342/..", "a.jpg"}, {"w342", "a.jpg?x"}, {"w342", "/../a.jpg"}} {
		if u, ok := st.ImageURL(t.Context(), tc.size, tc.file); ok || u != "" {
			t.Errorf("static source ImageURL(%q, %q) = %q, %v", tc.size, tc.file, u, ok)
		}
	}
	if u, ok := st.ImageURL(t.Context(), "w342", "a.jpg"); !ok || u != "https://image.tmdb.org/t/p/w342/a.jpg" {
		t.Errorf("static source: %q, %v", u, ok)
	}
}
