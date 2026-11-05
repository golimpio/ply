package metadata

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// newTVDBCache is a cache whose TVDB images come from the test server, with the
// thumbnail rule of Settings.TVDBImageURL (the server's own address instead of the host
// of the artworks).
func newTVDBCache(t *testing.T, s *imageServer, mod ...func(*ImageOptions)) *ImageCache {
	t.Helper()
	settings := &Settings{o: SettingsOptions{TVDBImageBaseURL: s.srv.URL + "/artworks"}}
	return newCache(t, s, append([]func(*ImageOptions){func(o *ImageOptions) {
		o.TVDBURL = func(_ context.Context, size, file string) (string, bool) {
			if !ValidTVDBImage(size, file) {
				return "", false
			}
			return tvdbImageURLAt(settings.o.TVDBImageBaseURL, size, file), true
		}
	}}, mod...)...)
}

func TestImageCacheTVDBDownloadsOnceUnderItsOwnTree(t *testing.T) {
	srv := newImageServer(t, nil)
	c := newTVDBCache(t, srv)
	ctx := t.Context()

	got, err := c.TVDB(ctx, "w342", "/banners/posters/81189-10.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(c.dir, "tvdb", "w342", "banners", "posters", "81189-10.jpg"); got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	// The small sizes are the thumbnail.
	if want := []string{"/artworks/banners/posters/81189-10_t.jpg"}; !slices.Equal(srv.paths(), want) {
		t.Errorf("fetched %q, want %q", srv.paths(), want)
	}
	// Cached: the second call, and the form without the slash, make no request.
	if again, err := c.TVDB(ctx, "w342", "banners/posters/81189-10.jpg"); err != nil || again != got || srv.hits.Load() != 1 {
		t.Errorf("second call: %q, %v, %d requests", again, err, srv.hits.Load())
	}
	// A large size is the original, cached on its own.
	big, err := c.TVDB(ctx, "w780", "/banners/posters/81189-10.jpg")
	if err != nil || big == got {
		t.Fatalf("w780 = %q, %v", big, err)
	}
	if want := []string{"/artworks/banners/posters/81189-10_t.jpg", "/artworks/banners/posters/81189-10.jpg"}; !slices.Equal(srv.paths(), want) {
		t.Errorf("fetched %q, want %q", srv.paths(), want)
	}
	// Prefetch takes the ref form, and ignores refs of other kinds.
	if err := c.Prefetch(ctx, "original", "tvdb:/banners/fanart/original/1-1.jpg"); err != nil {
		t.Errorf("Prefetch: %v", err)
	}
	n := srv.hits.Load()
	for _, ref := range []string{"local:item:1:poster", "", "tvdb:/banners/../x.jpg", "tvdb:banners/x.jpg"} {
		if err := c.Prefetch(ctx, "w342", ref); err != nil {
			t.Errorf("Prefetch(%q) = %v", ref, err)
		}
	}
	if srv.hits.Load() != n {
		t.Error("a ref that is not a TVDB image was fetched")
	}
}

// A thumbnail that TVDB does not have is made up for by the original; any other failure is
// a failure.
func TestImageCacheTVDBFallsBackToTheOriginalWhenThereIsNoThumbnail(t *testing.T) {
	srv := newImageServer(t, func(w http.ResponseWriter, r *http.Request) {
		if filepath.Base(r.URL.Path) == "no-thumb_t.jpg" {
			http.NotFound(w, r)
			return
		}
		if filepath.Base(r.URL.Path) == "broken_t.jpg" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write(jpegBytes)
	})
	c := newTVDBCache(t, srv)
	ctx := t.Context()
	got, err := c.TVDB(ctx, "w185", "/banners/posters/no-thumb.jpg")
	if err != nil {
		t.Fatalf("the original should make up for the thumbnail: %v", err)
	}
	if want := []string{"/artworks/banners/posters/no-thumb_t.jpg", "/artworks/banners/posters/no-thumb.jpg"}; !slices.Equal(srv.paths(), want) {
		t.Errorf("fetched %q, want %q", srv.paths(), want)
	}
	if fi, err := os.Stat(got); err != nil || fi.Size() == 0 {
		t.Errorf("cached file: %v", err)
	}
	// A 500 on the thumbnail is not "no thumbnail": it is a failure, and not retried at once.
	if _, err := c.TVDB(ctx, "w185", "/banners/posters/broken.jpg"); !errors.Is(err, ErrImageUnavailable) {
		t.Errorf("a server error = %v", err)
	}
	n := srv.hits.Load()
	if _, err := c.TVDB(ctx, "w185", "/banners/posters/broken.jpg"); !errors.Is(err, ErrImageUnavailable) || srv.hits.Load() != n {
		t.Errorf("a failed image was asked for again at once (%d new requests)", srv.hits.Load()-n)
	}
}

// Nothing but a plain TVDB image path reaches the disk or the network.
func TestImageCacheTVDBRefusesWhatIsNotAPlainTVDBPath(t *testing.T) {
	srv := newImageServer(t, nil)
	c := newTVDBCache(t, srv)
	dir := c.dir
	for _, tc := range []struct{ size, file string }{
		{"w342", "/banners/../../etc/passwd"}, {"w342", "/banners/../x.jpg"}, {"w342", "/../x.jpg"}, {"w342", "banners/%2e%2e/x.jpg"},
		{"w342", "/banners//x.jpg"}, {"w342", "/banners/./x.jpg"}, {"w342", "/banners/x.jpg/"}, {"w342", "/banners/x.php"},
		{"w342", "/banners/x"}, {"w342", "/banners/x.svg"}, {"w342", "/banners/a b.jpg"}, {"w342", `/banners\..\x.jpg`},
		{"w342", "https://evil.example/banners/x.jpg"}, {"w342", "//evil.example/x.jpg"}, {"w342", "/banners/x.jpg?a=b"},
		{"w1", "/banners/x.jpg"}, {"", "/banners/x.jpg"}, {"../w342", "/banners/x.jpg"}, {"w342", ""}, {"w342", "/"},
	} {
		if _, err := c.TVDB(t.Context(), tc.size, tc.file); !errors.Is(err, ErrBadImage) {
			t.Errorf("TVDB(%q, %q) = %v, want ErrBadImage", tc.size, tc.file, err)
		}
	}
	if srv.hits.Load() != 0 {
		t.Errorf("%d requests for paths that are not allowed: %q", srv.hits.Load(), srv.paths())
	}
	if files := filesUnder(t, filepath.Dir(dir)); len(files) != 0 {
		t.Errorf("files written: %q", files)
	}
	// Positive control: a plain path in the same cache works.
	if _, err := c.TVDB(t.Context(), "w342", "/banners/posters/1-1.jpg"); err != nil {
		t.Errorf("a plain path: %v", err)
	}
}

// What comes back must look like the picture its name says, and the cache holds nothing
// when the lookups are off.
func TestImageCacheTVDBChecksThePictureAndHonoursThePrivacySwitch(t *testing.T) {
	srv := newImageServer(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>an error page</html>")) })
	c := newTVDBCache(t, srv)
	if _, err := c.TVDB(t.Context(), "w500", "/banners/posters/1-1.jpg"); !errors.Is(err, ErrImageUnavailable) {
		t.Errorf("an error page served as a picture = %v", err)
	}
	if files := filesUnder(t, c.dir); len(files) != 0 {
		t.Errorf("cached: %q", files)
	}

	off := newImageServer(t, nil)
	c2 := newTVDBCache(t, off, func(o *ImageOptions) {
		o.TVDBURL = func(context.Context, string, string) (string, bool) { return "", false }
	})
	if _, err := c2.TVDB(t.Context(), "w342", "/banners/posters/1-1.jpg"); !errors.Is(err, ErrImageUnavailable) || off.hits.Load() != 0 {
		t.Errorf("switched off: %v, %d requests", err, off.hits.Load())
	}
	// An image already cached is still served.
	dest := filepath.Join(c2.dir, "tvdb", "w342", "banners", "posters", "1-1.jpg")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil || os.WriteFile(dest, jpegBytes, 0o644) != nil {
		t.Fatal("cannot seed the cache")
	}
	if got, err := c2.TVDB(t.Context(), "w342", "/banners/posters/1-1.jpg"); err != nil || got != dest {
		t.Errorf("a cached image while switched off = %q, %v", got, err)
	}
	// No TVDB address at all (a cache made for TMDB only).
	c3 := newCache(t, off)
	if _, err := c3.TVDB(t.Context(), "w342", "/banners/posters/9-9.jpg"); !errors.Is(err, ErrImageUnavailable) {
		t.Errorf("no TVDB address = %v", err)
	}
}
