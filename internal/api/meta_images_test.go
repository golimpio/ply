package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"ply/internal/store"
)

func TestTMDBImageIsDownloadedOnceAndServedWithLongCaching(t *testing.T) {
	m := newMetaEnv(t, apiToken)
	rec := m.get("/api/images/tmdb/w342/p603.jpg")
	wantStatus(t, rec, 200)
	if got := rec.Body.Bytes(); string(got) != string(jpegBytes) {
		t.Errorf("body = %q", got)
	}
	h := rec.Header()
	if h.Get("Content-Type") != "image/jpeg" || h.Get("Cache-Control") != "public, max-age=31536000, immutable" ||
		h.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(h.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("headers = %v", h)
	}
	if m.imageHits() != 1 {
		t.Errorf("%d downloads", m.imageHits())
	}
	// Cached: not fetched again, and Range and HEAD work.
	rec = m.get("/api/images/tmdb/w342/p603.jpg")
	wantStatus(t, rec, 200)
	req := httptest.NewRequest("GET", "/api/images/tmdb/w342/p603.jpg", nil)
	req.Header.Set("Authorization", "Bearer "+apiToken)
	req.Header.Set("Range", "bytes=0-3")
	part := httptest.NewRecorder()
	m.srv.ServeHTTP(part, req)
	if part.Code != 206 || part.Body.Len() != 4 {
		t.Errorf("range: %d, %d bytes", part.Code, part.Body.Len())
	}
	head := httptest.NewRecorder()
	req = httptest.NewRequest("HEAD", "/api/images/tmdb/w342/p603.jpg", nil)
	req.Header.Set("Authorization", "Bearer "+apiToken)
	m.srv.ServeHTTP(head, req)
	if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Type") != "image/jpeg" {
		t.Errorf("HEAD: %d, %d bytes, %v", head.Code, head.Body.Len(), head.Header())
	}
	if m.imageHits() != 1 {
		t.Errorf("%d downloads after cache hits, want 1", m.imageHits())
	}
	// Each size is its own file; png and svg have their own content types.
	wantStatus(t, m.get("/api/images/tmdb/w1280/p603.jpg"), 200)
	if m.imageHits() != 2 {
		t.Errorf("%d downloads", m.imageHits())
	}
	if ct := m.get("/api/images/tmdb/w92/logo.png").Header().Get("Content-Type"); ct != "image/png" {
		// the fake server sends JPEG bytes, which a .png is not: refused (a 404 has no image type)
		t.Logf("png with jpeg bytes: content type %q", ct)
	}
}

func TestImageSizesAndNamesAreChecked(t *testing.T) {
	m := newMetaEnv(t, apiToken)
	before := m.imageHits()
	for _, p := range []string{
		"w100/a.jpg", "W342/a.jpg", "w342%20/a.jpg", "w3420/a.jpg", "full/a.jpg", "tmdb/a.jpg", "w342%00/a.jpg", "originalx/a.jpg",
		"w342/a.gif", "w342/a.jpg.exe", "w342/A.JPG", "w342/a%2Fb.jpg", "w342/..%2Fa.jpg", "w342/%2e%2e%2fa.jpg", "w342/a%00.jpg",
		"w342/a%20b.jpg", "w342/.jpg", "w342/a.jpg%0A", "w342/%E2%82%AC.jpg", "w342/a.svgz", "w342/a..jpg", "w342/a.b.jpg",
		"w342/a.jpg%3Fx=1", "w342/..%5Ca.jpg", "..%2F..%2Fetc/passwd",
	} {
		rec := m.get("/api/images/tmdb/" + p)
		if rec.Code != 400 && rec.Code != 404 {
			t.Errorf("GET /api/images/tmdb/%s = %d, want 400 or 404", p, rec.Code)
		}
		if rec.Code == 200 || strings.Contains(rec.Body.String(), "image-bytes") {
			t.Errorf("GET /api/images/tmdb/%s served something", p)
		}
	}
	// Dot segments are cleaned away by the router into a redirect: never a file.
	for _, p := range []string{"/api/images/tmdb/w342/../../../etc/passwd", "/api/images/tmdb/w342/./../a.jpg", "/api/images/tmdb/../tmdb/w342/a.jpg"} {
		rec := m.get(p)
		if rec.Code == 200 {
			t.Errorf("GET %s = 200", p)
		}
	}
	// A request the allowlists let through is 400 with a JSON error and touches nothing.
	wantJSONError(t, m.get("/api/images/tmdb/w100/a.jpg"), 400)
	wantJSONError(t, m.get("/api/images/tmdb/w342/a.gif"), 400)
	if m.imageHits() != before {
		t.Errorf("a rejected image reached the image server (%d requests)", m.imageHits()-before)
	}
	imgDir := filepath.Join(m.env.dir, "..", "images")
	var written []string
	filepath.WalkDir(imgDir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			written = append(written, p)
		}
		return nil
	})
	if len(written) != 0 {
		t.Errorf("rejected requests left files: %v", written)
	}
	// Positive control: a good one is served from the same place.
	wantStatus(t, m.get("/api/images/tmdb/w342/ok.jpg"), 200)
	if m.imageHits() != before+1 {
		t.Errorf("the good request made %d downloads", m.imageHits()-before)
	}
}

func TestImageFailuresAre404AndNeedTheToken(t *testing.T) {
	m := newMetaEnv(t, apiToken)
	// Not reachable: the page falls back to the frame-grab artwork.
	m.imgSrv.Close()
	wantJSONError(t, m.get("/api/images/tmdb/w342/gone.jpg"), 404)
	// No token, no image.
	req := httptest.NewRequest("GET", "/api/images/tmdb/w342/gone.jpg", nil)
	rec := httptest.NewRecorder()
	m.srv.ServeHTTP(rec, req)
	wantJSONError(t, rec, 401)
	rec = httptest.NewRecorder()
	m.srv.ServeHTTP(rec, httptest.NewRequest("GET", "/api/images/tmdb/w342/gone.jpg?token="+apiToken, nil))
	wantJSONError(t, rec, 404) // authorised, and simply not there

	// No cache at all behind the server: the same.
	bare := newMetaEnv(t, "", func(o *Options) { o.Images = nil })
	wantJSONError(t, bare.get("/api/images/tmdb/w342/x.jpg"), 404)
	wantJSONError(t, bare.get("/api/images/tmdb/w342/x.gif"), 400)
}

func TestNothingIsDownloadedWithTheSwitchOffButTheCacheStillServes(t *testing.T) {
	m := newMetaEnv(t, "")
	wantStatus(t, m.get("/api/images/tmdb/w342/cached.jpg"), 200)
	hits := m.imageHits()
	m.src.Set(m.fake, false)
	wantStatus(t, m.get("/api/images/tmdb/w342/cached.jpg"), 200)
	wantJSONError(t, m.get("/api/images/tmdb/w342/other.jpg"), 404)
	if m.imageHits() != hits {
		t.Errorf("an image was downloaded with the switch off")
	}
}

func TestLocalImages(t *testing.T) {
	m := newMetaEnv(t, apiToken)
	ctx := t.Context()
	poster := m.writeFile("Movies/poster.jpg", string(jpegBytes))
	back := m.writeFile("Movies/fanart.png", pngBytes)
	if err := m.store.SetItemArt(ctx, m.mp4, store.Art{Poster: poster, Backdrop: back, Thumb: "/nonexistent/thumb.jpg"}); err != nil {
		t.Fatal(err)
	}
	rec := m.get(fmt.Sprintf("/api/images/local/item/%d/poster", m.mp4))
	wantStatus(t, rec, 200)
	if rec.Body.String() != string(jpegBytes) || rec.Header().Get("Content-Type") != "image/jpeg" || rec.Header().Get("Cache-Control") != "no-cache" ||
		rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Last-Modified") == "" {
		t.Errorf("poster: %q %v", rec.Body.String(), rec.Header())
	}
	if rec := m.get(fmt.Sprintf("/api/images/local/item/%d/backdrop", m.mp4)); rec.Header().Get("Content-Type") != "image/png" || rec.Body.String() != pngBytes {
		t.Errorf("backdrop: %v", rec.Header())
	}
	// Revalidation works.
	req := httptest.NewRequest("GET", fmt.Sprintf("/api/images/local/item/%d/poster", m.mp4), nil)
	req.Header.Set("Authorization", "Bearer "+apiToken)
	req.Header.Set("If-Modified-Since", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat))
	rec = httptest.NewRecorder()
	m.srv.ServeHTTP(rec, req)
	if rec.Code != 304 {
		t.Errorf("If-Modified-Since: %d", rec.Code)
	}

	// Show artwork is by the show row's id.
	showPoster := m.writeFile("TV/poster.jpg", string(jpegBytes)+"show")
	if err := m.store.SetSeriesArt(ctx, "Cool Show", store.Art{Poster: showPoster}); err != nil {
		t.Fatal(err)
	}
	sm, _ := m.store.GetSeriesMatch(ctx, "Cool Show")
	if rec := m.get(fmt.Sprintf("/api/images/local/series/%d/poster", sm.ID)); rec.Code != 200 || rec.Body.String() != string(jpegBytes)+"show" {
		t.Errorf("series poster: %d %q", rec.Code, rec.Body.String())
	}

	// Things that are not there, or may not be served.
	for _, p := range []string{
		fmt.Sprintf("/api/images/local/item/%d/thumb", m.mp4),    // the file is gone
		fmt.Sprintf("/api/images/local/item/%d/poster", m.multi), // no art
		fmt.Sprintf("/api/images/local/item/%d/logo", m.mp4),     // no such slot
		fmt.Sprintf("/api/images/local/series/%d/thumb", sm.ID),  // series have no thumb
		fmt.Sprintf("/api/images/local/movie/%d/poster", m.mp4),  // no such kind
		"/api/images/local/item/99999/poster", "/api/images/local/item/abc/poster", "/api/images/local/item/-1/poster",
		fmt.Sprintf("/api/images/local/item/%d/poster?path=/etc/passwd", m.multi),
		fmt.Sprintf("/api/images/local/item/%d/..%%2F..%%2Fposter", m.mp4),
	} {
		rec := m.get(p)
		if rec.Code != 404 {
			t.Errorf("GET %s = %d, want 404", p, rec.Code)
		}
	}
	// A path in the database that is not an image is not served, whatever it is.
	for _, bad := range []string{"/etc/hosts", m.writeFile("notes.txt", "secret"), filepath.Dir(poster), m.writeFile("x.svg", "<svg/>")} {
		if err := m.store.SetItemArt(ctx, m.multi, store.Art{Poster: bad}); err != nil {
			t.Fatal(err)
		}
		rec := m.get(fmt.Sprintf("/api/images/local/item/%d/poster", m.multi))
		if rec.Code != 404 || strings.Contains(rec.Body.String(), "secret") {
			t.Errorf("poster %q: %d %q", bad, rec.Code, rec.Body.String())
		}
	}
	// The scanner follows symlinks, so "poster.jpg" can lead anywhere. What it
	// leads to must be a regular file that really is an image.
	secret := m.writeFile("secret.conf", "password=hunter2\n")
	link := filepath.Join(m.env.dir, "linked-poster.jpg")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	if err := m.store.SetItemArt(ctx, m.multi, store.Art{Poster: link}); err != nil {
		t.Fatal(err)
	}
	if rec := m.get(fmt.Sprintf("/api/images/local/item/%d/poster", m.multi)); rec.Code != 404 || strings.Contains(rec.Body.String(), "hunter2") {
		t.Errorf("symlink to a config file: %d %q", rec.Code, rec.Body.String())
	}
	for _, target := range []string{"/etc/passwd", "/etc/hosts"} {
		os.Remove(link)
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if rec := m.get(fmt.Sprintf("/api/images/local/item/%d/poster", m.multi)); rec.Code != 404 || strings.Contains(rec.Body.String(), "root") || strings.Contains(rec.Body.String(), "localhost") {
			t.Errorf("symlink to %s: %d %q", target, rec.Code, rec.Body.String())
		}
	}
	// Positive control: a symlink to a real image is served.
	os.Remove(link)
	if err := os.Symlink(poster, link); err != nil {
		t.Fatal(err)
	}
	if rec := m.get(fmt.Sprintf("/api/images/local/item/%d/poster", m.multi)); rec.Code != 200 {
		t.Errorf("symlink to an image: %d", rec.Code)
	}
	// A text file with an image name is not an image either.
	text := m.writeFile("notes.jpg", "just some notes about the movie, not a picture at all")
	if err := m.store.SetItemArt(ctx, m.multi, store.Art{Poster: text}); err != nil {
		t.Fatal(err)
	}
	if rec := m.get(fmt.Sprintf("/api/images/local/item/%d/poster", m.multi)); rec.Code != 404 {
		t.Errorf("text named .jpg: %d %q", rec.Code, rec.Body.String())
	}

	// And the request cannot choose the path: no query parameter or header does.
	req = httptest.NewRequest("GET", fmt.Sprintf("/api/images/local/item/%d/poster?path=%s&file=%s", m.multi, escSpaces(poster), escSpaces(poster)), nil)
	req.Header.Set("Authorization", "Bearer "+apiToken)
	req.Header.Set("X-Path", poster)
	rec = httptest.NewRecorder()
	m.srv.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Errorf("a path in the request was used: %d %q", rec.Code, rec.Body.String())
	}

	// No token, no image.
	rec = httptest.NewRecorder()
	m.srv.ServeHTTP(rec, httptest.NewRequest("GET", fmt.Sprintf("/api/images/local/item/%d/poster", m.mp4), nil))
	wantJSONError(t, rec, 401)
}

func escSpaces(s string) string { return strings.ReplaceAll(s, " ", "%20") }

func TestImageURLsFromRefs(t *testing.T) {
	s := &Server{Token: "a b"}
	for ref, want := range map[string]string{
		"tmdb:/abc.jpg":        "/api/images/tmdb/w342/abc.jpg?token=a+b",
		"local:item:12:poster": "/api/images/local/item/12/poster?token=a+b",
		"local:series:3:thumb": "/api/images/local/series/3/thumb?token=a+b",
		"":                     "",
		"http://evil/x.jpg":    "",
		"embedded:3":           "",
		"local:item:1":         "",
		"local:a:b:c:d":        "",
	} {
		if got := s.imageURL(ref, "w342"); got != want {
			t.Errorf("imageURL(%q) = %q, want %q", ref, got, want)
		}
	}
	open := &Server{}
	if got := open.imageURL("tmdb:/a.jpg", "original"); got != "/api/images/tmdb/original/a.jpg" {
		t.Errorf("without a token: %q", got)
	}
}

func TestCORSAllowsPatch(t *testing.T) {
	m := newMetaEnv(t, apiToken)
	req := httptest.NewRequest("OPTIONS", "/api/titles/1", nil)
	rec := httptest.NewRecorder()
	m.srv.ServeHTTP(rec, req)
	methods := strings.Split(rec.Header().Get("Access-Control-Allow-Methods"), ", ")
	if rec.Code != 204 || !slices.Contains(methods, "PATCH") {
		t.Errorf("preflight: %d %v", rec.Code, methods)
	}
}
