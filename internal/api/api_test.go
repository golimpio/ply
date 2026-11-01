package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"ply/internal/library"
	"ply/internal/probe"
	"ply/internal/store"
)

const fileContent = "0123456789abcdef" // 16 bytes

type fakeScanner struct {
	mu        sync.Mutex
	running   bool
	triggered int
}

func (f *fakeScanner) Trigger() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.running {
		return false
	}
	f.running = true
	f.triggered++
	return true
}

func (f *fakeScanner) Status(ctx context.Context) library.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Unix(1_700_000_000, 0).UTC()
	return library.Status{Running: f.running, LastStarted: &now, ItemCount: 3}
}

type env struct {
	t       *testing.T
	srv     *Server
	store   *store.Store
	scanner *fakeScanner
	dir     string
	// item ids
	mp4, mkv, multi int64
}

var testUI = fstest.MapFS{
	"index.html":    {Data: []byte("<html>ply app</html>")},
	"assets/app.js": {Data: []byte("console.log('app')")},
	"favicon.svg":   {Data: []byte("<svg/>")},
}

func streamsH264() []probe.Stream {
	return []probe.Stream{
		{Index: 0, Type: "video", Codec: "h264", CodecTag: "avc1", Width: 1920, Height: 1080},
		{Index: 1, Type: "audio", Codec: "aac", Channels: 2, Language: "eng", Default: true},
	}
}

func newEnv(t *testing.T, token string) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "data dir", "ply.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	media := filepath.Join(dir, "media")
	e := &env{t: t, store: st, scanner: &fakeScanner{}, dir: media}

	add := func(name string, it store.Item, streams []probe.Stream) int64 {
		path := filepath.Join(media, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(fileContent), 0o644); err != nil {
			t.Fatal(err)
		}
		fi, _ := os.Stat(path)
		it.Path, it.Root, it.Size, it.MTime = path, media, fi.Size(), fi.ModTime().UnixNano()
		id, err := st.UpsertItem(context.Background(), &it, streams)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	e.mp4 = add("Movie (2020).mp4", store.Item{Kind: store.KindMovie, Title: "Movie", Year: 2020,
		Container: "mp4", DurationSec: 120, Bitrate: 4000}, streamsH264())
	e.mkv = add("Shows/Cool Show/Season 1/Cool Show S01E02 Pilot.mkv", store.Item{Kind: store.KindEpisode,
		Title: "Pilot", Show: "Cool Show", Season: 1, Episode: 2, Container: "mkv", DurationSec: 2400},
		[]probe.Stream{
			{Index: 0, Type: "video", Codec: "hevc", Width: 3840, Height: 2160, PixFmt: "yuv420p10le",
				ColorTransfer: "smpte2084", Default: true},
			{Index: 1, Type: "audio", Codec: "eac3", Channels: 6, Language: "eng", Default: true},
			{Index: 2, Type: "subtitle", Codec: "hdmv_pgs_subtitle", Language: "eng"},
			{Index: 3, Type: "subtitle", Codec: "subrip", Language: "por", Title: "Portuguese"},
		})
	e.multi = add("Multi (2019).mp4", store.Item{Kind: store.KindMovie, Title: "Multi", Year: 2019, Container: "mp4"},
		[]probe.Stream{
			{Index: 0, Type: "video", Codec: "h264", CodecTag: "avc1", Width: 1280, Height: 720},
			{Index: 1, Type: "audio", Codec: "aac", Channels: 2, Default: true},
			{Index: 2, Type: "audio", Codec: "dts", Channels: 6},
			{Index: 3, Type: "subtitle", Codec: "hdmv_pgs_subtitle"},
			{Index: 4, Type: "subtitle", Codec: "subrip"},
		})

	e.srv = New(Options{Store: st, Scanner: e.scanner, UI: testUI, ID: "abc123", Name: "Test Server",
		Version: "9.9.9", Token: token})
	return e
}

func (e *env) do(method, target string, hdr map[string]string, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	return rec
}

func (e *env) get(target string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(http.MethodGet, target, nil, "")
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, want, rec.Body.String())
	}
}

func wantJSONError(t *testing.T, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	wantStatus(t, rec, status)
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["error"] == "" {
		t.Errorf(`body %q is not {"error": "..."}`, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("error Content-Type = %q", ct)
	}
}

func TestInfo(t *testing.T) {
	for _, token := range []string{"", "s3cret"} {
		e := newEnv(t, token)
		rec := e.get("/api/info") // never needs auth
		wantStatus(t, rec, 200)
		got := decode[map[string]any](t, rec)
		want := map[string]any{"id": "abc123", "name": "Test Server", "version": "9.9.9", "authRequired": token != ""}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("token %q: info[%s] = %v, want %v", token, k, got[k], v)
			}
		}
		if len(got) != len(want) {
			t.Errorf("info = %v, want exactly %v", got, want)
		}
	}
}

func TestAuthMatrix(t *testing.T) {
	const token = "s3cret"
	tests := []struct {
		name   string
		token  string // configured on the server
		method string
		path   string
		header string // Authorization header
		want   int
	}{
		{"no token configured: anonymous allowed", "", "GET", "/api/items", "", 200},
		{"no token configured: credentials ignored", "", "GET", "/api/items", "Bearer whatever", 200},
		{"no token configured: file", "", "GET", "/api/items/1/file", "", 200},

		{"missing", token, "GET", "/api/items", "", 401},
		{"bearer", token, "GET", "/api/items", "Bearer " + token, 200},
		{"bearer scheme is case-insensitive", token, "GET", "/api/items", "bearer " + token, 200},
		{"query", token, "GET", "/api/items?token=" + token, "", 200},
		{"wrong bearer", token, "GET", "/api/items", "Bearer nope", 401},
		{"wrong query", token, "GET", "/api/items?token=nope", "", 401},
		{"empty query token", token, "GET", "/api/items?token=", "", 401},
		{"token prefix", token, "GET", "/api/items?token=s3cre", "", 401},
		{"token with suffix", token, "GET", "/api/items?token=s3cret1", "", 401},
		{"wrong scheme", token, "GET", "/api/items", "Basic " + token, 401},
		{"bare token without scheme", token, "GET", "/api/items", token, 401},
		{"empty bearer", token, "GET", "/api/items", "Bearer ", 401},
		{"wrong bearer but right query", token, "GET", "/api/items?token=" + token, "Bearer nope", 200},

		{"info is public", token, "GET", "/api/info", "", 200},
		{"file needs auth", token, "GET", "/api/items/1/file", "", 401},
		{"file with query token", token, "GET", "/api/items/1/file?token=" + token, "", 200},
		{"item detail needs auth", token, "GET", "/api/items/1", "", 401},
		{"playback needs auth", token, "POST", "/api/items/1/playback", "", 401},
		{"scan start needs auth", token, "POST", "/api/scan", "", 401},
		{"scan status needs auth", token, "GET", "/api/scan", "", 401},
		{"shows need auth", token, "GET", "/api/shows", "", 401},
		{"unknown api path is not revealed without auth", token, "GET", "/api/nope", "", 401},
		{"unknown api path with auth", token, "GET", "/api/nope", "Bearer " + token, 404},

		{"SPA index is public", token, "GET", "/", "", 200},
		{"SPA asset is public", token, "GET", "/assets/app.js", "", 200},
		{"SPA route is public", token, "GET", "/shows/Cool", "", 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, tt.token)
			var hdr map[string]string
			if tt.header != "" {
				hdr = map[string]string{"Authorization": tt.header}
			}
			rec := e.do(tt.method, tt.path, hdr, "")
			if tt.want == 401 {
				wantJSONError(t, rec, 401)
			} else {
				wantStatus(t, rec, tt.want) // item id 1 exists in the fixture
			}
		})
	}
}

func TestAuthAcceptsScanPostWithToken(t *testing.T) {
	e := newEnv(t, "s3cret")
	rec := e.do("POST", "/api/scan", map[string]string{"Authorization": "Bearer s3cret"}, "")
	wantStatus(t, rec, 202)
	if e.scanner.triggered != 1 {
		t.Errorf("scan triggered %d times, want 1", e.scanner.triggered)
	}
	// Positive control for the 401 case: an unauthorised POST must not trigger.
	e2 := newEnv(t, "s3cret")
	wantJSONError(t, e2.do("POST", "/api/scan", nil, ""), 401)
	if e2.scanner.triggered != 0 {
		t.Error("unauthorised request triggered a scan")
	}
}

func TestCORS(t *testing.T) {
	for _, token := range []string{"", "s3cret"} {
		e := newEnv(t, token)
		for _, path := range []string{"/api/items/1/file", "/api/items", "/api/info", "/", "/api/nope"} {
			rec := e.do("OPTIONS", path, map[string]string{
				"Origin":                         "http://tv.local",
				"Access-Control-Request-Method":  "GET",
				"Access-Control-Request-Headers": "range, authorization",
			}, "")
			if rec.Code != 204 {
				t.Errorf("token %q OPTIONS %s = %d, want 204 (preflight carries no credentials)", token, path, rec.Code)
			}
			if rec.Body.Len() != 0 {
				t.Errorf("OPTIONS %s has a body: %q", path, rec.Body.String())
			}
			checkCORSHeaders(t, rec, "OPTIONS "+path)
		}
	}
}

func checkCORSHeaders(t *testing.T, rec *httptest.ResponseRecorder, what string) {
	t.Helper()
	want := map[string]string{
		"Access-Control-Allow-Origin":   "*",
		"Access-Control-Allow-Headers":  "Range, Authorization, Content-Type",
		"Access-Control-Allow-Methods":  "GET, POST, PUT, DELETE, OPTIONS",
		"Access-Control-Expose-Headers": "Content-Range, Accept-Ranges, Content-Length, Content-Type",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s: %s = %q, want %q", what, k, got, v)
		}
	}
}

func TestCORSOnEveryKindOfResponse(t *testing.T) {
	e := newEnv(t, "s3cret")
	auth := map[string]string{"Authorization": "Bearer s3cret"}
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"200 json":    e.do("GET", "/api/items", auth, ""),
		"401":         e.do("GET", "/api/items", nil, ""),
		"404 json":    e.do("GET", "/api/items/999", auth, ""),
		"200 file":    e.do("GET", "/api/items/1/file", auth, ""),
		"206 file":    e.do("GET", "/api/items/1/file", map[string]string{"Authorization": "Bearer s3cret", "Range": "bytes=0-1"}, ""),
		"spa":         e.do("GET", "/", nil, ""),
		"spa asset":   e.do("GET", "/assets/app.js", nil, ""),
		"api info":    e.do("GET", "/api/info", nil, ""),
		"unknown api": e.do("GET", "/api/nope", auth, ""),
	} {
		checkCORSHeaders(t, rec, name)
	}
}

func TestFileServing(t *testing.T) {
	e := newEnv(t, "")
	url := fmt.Sprintf("/api/items/%d/file", e.mp4)

	t.Run("full", func(t *testing.T) {
		rec := e.do("GET", url, map[string]string{"Accept-Encoding": "gzip"}, "")
		wantStatus(t, rec, 200)
		if rec.Body.String() != fileContent {
			t.Errorf("body = %q", rec.Body.String())
		}
		h := rec.Header()
		if h.Get("Content-Type") != "video/mp4" {
			t.Errorf("Content-Type = %q", h.Get("Content-Type"))
		}
		if h.Get("Accept-Ranges") != "bytes" {
			t.Errorf("Accept-Ranges = %q", h.Get("Accept-Ranges"))
		}
		if h.Get("Content-Length") != "16" {
			t.Errorf("Content-Length = %q", h.Get("Content-Length"))
		}
		if h.Get("ETag") == "" || h.Get("Last-Modified") == "" {
			t.Errorf("missing validators: ETag=%q Last-Modified=%q", h.Get("ETag"), h.Get("Last-Modified"))
		}
		if h.Get("Content-Encoding") != "" {
			t.Errorf("media must never be compressed, got Content-Encoding %q", h.Get("Content-Encoding"))
		}
	})

	ranges := []struct {
		name, header, body, contentRange string
	}{
		{"first bytes", "bytes=0-3", "0123", "bytes 0-3/16"},
		{"middle", "bytes=4-7", "4567", "bytes 4-7/16"},
		{"open ended", "bytes=10-", "abcdef", "bytes 10-15/16"},
		{"suffix", "bytes=-4", "cdef", "bytes 12-15/16"},
		{"clamped end", "bytes=12-999", "cdef", "bytes 12-15/16"},
		{"single byte", "bytes=0-0", "0", "bytes 0-0/16"},
	}
	for _, tt := range ranges {
		t.Run("206 "+tt.name, func(t *testing.T) {
			rec := e.do("GET", url, map[string]string{"Range": tt.header}, "")
			wantStatus(t, rec, 206)
			if rec.Body.String() != tt.body {
				t.Errorf("body = %q, want %q", rec.Body.String(), tt.body)
			}
			if got := rec.Header().Get("Content-Range"); got != tt.contentRange {
				t.Errorf("Content-Range = %q, want %q", got, tt.contentRange)
			}
			if got, want := rec.Header().Get("Content-Length"), fmt.Sprint(len(tt.body)); got != want {
				t.Errorf("Content-Length = %q, want %q", got, want)
			}
			if rec.Header().Get("Content-Type") != "video/mp4" {
				t.Errorf("Content-Type = %q", rec.Header().Get("Content-Type"))
			}
		})
	}

	t.Run("416 unsatisfiable", func(t *testing.T) {
		rec := e.do("GET", url, map[string]string{"Range": "bytes=100-200"}, "")
		wantStatus(t, rec, 416)
		if got := rec.Header().Get("Content-Range"); got != "bytes */16" {
			t.Errorf("Content-Range = %q", got)
		}
	})

	t.Run("304 on matching ETag", func(t *testing.T) {
		etag := e.get(url).Header().Get("ETag")
		rec := e.do("GET", url, map[string]string{"If-None-Match": etag}, "")
		wantStatus(t, rec, 304)
		if rec.Body.Len() != 0 {
			t.Error("304 has a body")
		}
		// Positive control: a different ETag serves the body.
		wantStatus(t, e.do("GET", url, map[string]string{"If-None-Match": `"other"`}, ""), 200)
	})

	t.Run("If-Range with stale validator returns the whole file", func(t *testing.T) {
		rec := e.do("GET", url, map[string]string{"Range": "bytes=0-3", "If-Range": `"stale"`}, "")
		wantStatus(t, rec, 200)
		if rec.Body.String() != fileContent {
			t.Errorf("body = %q", rec.Body.String())
		}
	})

	t.Run("HEAD", func(t *testing.T) {
		rec := e.do("HEAD", url, nil, "")
		wantStatus(t, rec, 200)
		if rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != "16" {
			t.Errorf("HEAD body=%q Content-Length=%q", rec.Body.String(), rec.Header().Get("Content-Length"))
		}
	})

	t.Run("mkv content type", func(t *testing.T) {
		rec := e.get(fmt.Sprintf("/api/items/%d/file", e.mkv))
		wantStatus(t, rec, 200)
		if got := rec.Header().Get("Content-Type"); got != "video/x-matroska" {
			t.Errorf("Content-Type = %q", got)
		}
	})
}

func TestContentTypes(t *testing.T) {
	for container, want := range map[string]string{
		"mp4": "video/mp4", "mov": "video/quicktime", "m4v": "video/x-m4v", "mkv": "video/x-matroska",
		"webm": "video/webm", "ts": "video/mp2t", "avi": "video/x-msvideo", "": "application/octet-stream",
		"wmv": "application/octet-stream",
	} {
		if got := contentType(container); got != want {
			t.Errorf("contentType(%q) = %q, want %q", container, got, want)
		}
	}
}

func TestFileGoneFromDisk(t *testing.T) {
	e := newEnv(t, "")
	url := fmt.Sprintf("/api/items/%d/file", e.mp4)
	wantStatus(t, e.get(url), 200) // positive control

	item, err := e.store.GetEntry(context.Background(), e.mp4)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(item.Path); err != nil {
		t.Fatal(err)
	}
	wantJSONError(t, e.get(url), 404)
}

func TestNotFound(t *testing.T) {
	e := newEnv(t, "")
	// Positive control: a real id works on the same routes.
	wantStatus(t, e.get(fmt.Sprintf("/api/items/%d", e.mp4)), 200)
	wantStatus(t, e.get(fmt.Sprintf("/api/items/%d/file", e.mp4)), 200)

	for _, tt := range []struct{ method, path string }{
		{"GET", "/api/items/9999"},
		{"GET", "/api/items/9999/file"},
		{"POST", "/api/items/9999/playback"},
		{"GET", "/api/items/abc"},
		{"GET", "/api/items/abc/file"},
		{"GET", "/api/items/-1"},
		{"GET", "/api/items/1.5"},
		{"GET", "/api/nope"},
		{"GET", "/api/items/1/nope"},
		{"DELETE", "/api/items/1"},
		{"GET", "/api/"},
	} {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			wantJSONError(t, e.do(tt.method, tt.path, nil, ""), 404)
		})
	}
}

type listedItem struct {
	ID           int64   `json:"id"`
	Kind         string  `json:"kind"`
	Title        string  `json:"title"`
	Year         *int    `json:"year"`
	Show         string  `json:"show"`
	Season       *int    `json:"season"`
	Episode      *int    `json:"episode"`
	EpisodeTitle string  `json:"episodeTitle"`
	DurationSec  float64 `json:"durationSec"`
	Container    string  `json:"container"`
	VideoCodec   string  `json:"videoCodec"`
	AudioCodec   string  `json:"audioCodec"`
	Width        int     `json:"width"`
	Height       int     `json:"height"`
	HDR          string  `json:"hdr"`
	AddedAt      int64   `json:"addedAt"`
}

func TestItemsList(t *testing.T) {
	e := newEnv(t, "")

	all := decode[[]listedItem](t, e.get("/api/items"))
	if len(all) != 3 {
		t.Fatalf("items = %d, want 3", len(all))
	}
	// Movies by title first, then episodes.
	if all[0].Title != "Movie" || all[1].Title != "Multi" || all[2].Kind != "episode" {
		t.Errorf("order = %q, %q, %q", all[0].Title, all[1].Title, all[2].Title)
	}

	movie := all[0]
	if movie.ID != e.mp4 || movie.Kind != "movie" || movie.Year == nil || *movie.Year != 2020 ||
		movie.Container != "mp4" || movie.VideoCodec != "h264" || movie.AudioCodec != "aac" ||
		movie.Width != 1920 || movie.Height != 1080 || movie.DurationSec != 120 || movie.AddedAt == 0 || movie.HDR != "" {
		t.Errorf("movie summary = %+v", movie)
	}
	if movie.Season != nil || movie.Episode != nil || movie.Show != "" {
		t.Errorf("movie has episode fields: %+v", movie)
	}

	ep := all[2]
	if ep.ID != e.mkv || ep.Show != "Cool Show" || ep.Season == nil || *ep.Season != 1 || ep.Episode == nil ||
		*ep.Episode != 2 || ep.EpisodeTitle != "Pilot" || ep.Title != "Pilot" || ep.HDR != "hdr10" ||
		ep.VideoCodec != "hevc" || ep.AudioCodec != "eac3" || ep.Year != nil {
		t.Errorf("episode summary = %+v", ep)
	}

	// Raw JSON: absent optionals are omitted, not null.
	raw := decode[[]map[string]any](t, e.get("/api/items?kind=movie"))
	for _, k := range []string{"show", "season", "episode", "episodeTitle", "hdr", "progress"} {
		if _, ok := raw[0][k]; ok {
			t.Errorf("movie JSON has %q", k)
		}
	}

	movies := decode[[]listedItem](t, e.get("/api/items?kind=movie"))
	episodes := decode[[]listedItem](t, e.get("/api/items?kind=episode"))
	if len(movies) != 2 || len(episodes) != 1 {
		t.Errorf("kind filter: %d movies, %d episodes", len(movies), len(episodes))
	}
	byShow := decode[[]listedItem](t, e.get("/api/items?show=cool+show"))
	if len(byShow) != 1 || byShow[0].ID != e.mkv {
		t.Errorf("show filter = %+v", byShow)
	}
	if none := decode[[]listedItem](t, e.get("/api/items?show=Nope")); len(none) != 0 {
		t.Errorf("unknown show = %+v", none)
	}
	wantJSONError(t, e.get("/api/items?kind=bogus"), 400)
}

func TestEmptyListsAreArrays(t *testing.T) {
	e := newEnv(t, "")
	if err := e.store.DeleteItems(context.Background(), []int64{e.mp4, e.mkv, e.multi}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/items", "/api/shows"} {
		rec := e.get(path)
		wantStatus(t, rec, 200)
		if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
			t.Errorf("%s = %s, want []", path, got)
		}
	}
}

func TestEpisodeWithoutTitleGetsLabel(t *testing.T) {
	e := newEnv(t, "")
	path := filepath.Join(e.dir, "untitled S03E07.mkv")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := e.store.UpsertItem(context.Background(), &store.Item{Kind: store.KindEpisode, Show: "Zed", Season: 3,
		Episode: 7, Path: path, Root: e.dir, Size: 1, Container: "mkv"}, streamsH264())
	if err != nil {
		t.Fatal(err)
	}
	var found *listedItem
	for _, it := range decode[[]listedItem](t, e.get("/api/items?show=Zed")) {
		if it.ID == id {
			found = &it
		}
	}
	if found == nil || found.Title != "S03E07" || found.EpisodeTitle != "" {
		t.Errorf("untitled episode = %+v", found)
	}
}

func TestShows(t *testing.T) {
	e := newEnv(t, "")
	shows := decode[[]ShowSummary](t, e.get("/api/shows"))
	if len(shows) != 1 || shows[0].Show != "Cool Show" || len(shows[0].Seasons) != 1 || shows[0].Seasons[0] != 1 ||
		shows[0].EpisodeCount != 1 {
		t.Errorf("shows = %+v", shows)
	}
	raw := e.get("/api/shows").Body.String()
	for _, k := range []string{`"show"`, `"seasons"`, `"episodeCount"`} {
		if !strings.Contains(raw, k) {
			t.Errorf("shows JSON %s lacks %s", raw, k)
		}
	}
}

func TestItemDetail(t *testing.T) {
	t.Run("direct", func(t *testing.T) {
		e := newEnv(t, "")
		d := decode[ItemDetail](t, e.get(fmt.Sprintf("/api/items/%d", e.mp4)))
		if d.Path != "Movie (2020).mp4" || strings.Contains(d.Path, "/") {
			t.Errorf("path = %q, want the base name only", d.Path)
		}
		if d.Size != int64(len(fileContent)) || d.Bitrate != 4000 || d.Title != "Movie" {
			t.Errorf("detail = %+v", d)
		}
		if len(d.Streams) != 2 || d.Streams[0].Codec != "h264" || d.Streams[1].Language != "eng" || !d.Streams[1].Default {
			t.Errorf("streams = %+v", d.Streams)
		}
		if d.Subtitles == nil || len(d.Subtitles) != 0 {
			t.Errorf("subtitles = %#v, want empty non-nil", d.Subtitles)
		}
		pb := d.Playback
		if pb.Method != "direct" || pb.VideoAction != "copy" || pb.AudioAction != "copy" || pb.AudioIndex != 1 ||
			pb.URL != fmt.Sprintf("/api/items/%d/file", e.mp4) || len(pb.Reasons) != 0 || pb.StartSec != 0 {
			t.Errorf("playback = %+v", pb)
		}
	})

	t.Run("direct url carries the token", func(t *testing.T) {
		e := newEnv(t, "s3cret")
		rec := e.do("GET", fmt.Sprintf("/api/items/%d", e.mp4), map[string]string{"Authorization": "Bearer s3cret"}, "")
		d := decode[ItemDetail](t, rec)
		if want := fmt.Sprintf("/api/items/%d/file?token=s3cret", e.mp4); d.Playback.URL != want {
			t.Errorf("url = %q, want %q", d.Playback.URL, want)
		}
	})

	t.Run("remux has reasons and no url yet", func(t *testing.T) {
		e := newEnv(t, "s3cret")
		rec := e.do("GET", fmt.Sprintf("/api/items/%d?target=avplayer", e.mkv), map[string]string{"Authorization": "Bearer s3cret"}, "")
		d := decode[ItemDetail](t, rec)
		pb := d.Playback
		if pb.Method != "remux" || pb.VideoAction != "copy" || pb.AudioAction != "copy" || pb.URL != "" {
			t.Errorf("playback = %+v", pb)
		}
		if len(pb.Reasons) != 1 || !strings.Contains(pb.Reasons[0], "container mkv") {
			t.Errorf("reasons = %q", pb.Reasons)
		}
		if len(d.Subtitles) != 2 || d.Subtitles[0].Key != "2" || !d.Subtitles[0].Image || d.Subtitles[0].Format != "hdmv_pgs_subtitle" ||
			d.Subtitles[1].Key != "3" || d.Subtitles[1].Image || d.Subtitles[1].Language != "por" || d.Subtitles[1].Title != "Portuguese" {
			t.Errorf("subtitles = %+v", d.Subtitles)
		}
	})

	t.Run("unknown target", func(t *testing.T) {
		e := newEnv(t, "")
		wantJSONError(t, e.get(fmt.Sprintf("/api/items/%d?target=nope", e.mp4)), 400)
	})

	t.Run("JSON field names", func(t *testing.T) {
		e := newEnv(t, "")
		raw := decode[map[string]any](t, e.get(fmt.Sprintf("/api/items/%d", e.mkv)))
		for _, k := range []string{"id", "kind", "title", "durationSec", "container", "videoCodec", "audioCodec",
			"width", "height", "hdr", "addedAt", "path", "size", "bitrate", "streams", "subtitles", "playback",
			"show", "season", "episode", "episodeTitle"} {
			if _, ok := raw[k]; !ok {
				t.Errorf("detail JSON lacks %q", k)
			}
		}
		pb := raw["playback"].(map[string]any)
		for _, k := range []string{"method", "videoAction", "audioAction", "audioIndex", "reasons", "url", "startSec"} {
			if _, ok := pb[k]; !ok {
				t.Errorf("playback JSON lacks %q", k)
			}
		}
		st := raw["streams"].([]any)[0].(map[string]any)
		for _, k := range []string{"index", "type", "codec", "codecTag", "profile", "width", "height", "pixFmt",
			"colorTransfer", "channels", "language", "title", "default", "forced"} {
			if _, ok := st[k]; !ok {
				t.Errorf("stream JSON lacks %q", k)
			}
		}
	})
}

func TestPlaybackPost(t *testing.T) {
	e := newEnv(t, "")
	post := func(id int64, body string) *httptest.ResponseRecorder {
		return e.do("POST", fmt.Sprintf("/api/items/%d/playback", id), map[string]string{"Content-Type": "application/json"}, body)
	}

	t.Run("empty body uses avplayer", func(t *testing.T) {
		rec := post(e.mp4, "")
		wantStatus(t, rec, 200)
		pb := decode[Playback](t, rec)
		if pb.Method != "direct" || pb.URL == "" {
			t.Errorf("playback = %+v", pb)
		}
	})

	t.Run("named profile", func(t *testing.T) {
		pb := decode[Playback](t, post(e.mkv, `{"profile":"avplayer"}`))
		if pb.Method != "remux" || pb.URL != "" || len(pb.Reasons) == 0 {
			t.Errorf("playback = %+v", pb)
		}
	})

	t.Run("custom profile can make mkv direct", func(t *testing.T) {
		const custom = `{"custom": {"name": "mkvplayer", "containers": ["mkv"], "videoCodecs": ["hevc"], "audioCodecs": ["eac3"]}}`
		pb := decode[Playback](t, post(e.mkv, custom))
		if pb.Method != "direct" || pb.URL != fmt.Sprintf("/api/items/%d/file", e.mkv) {
			t.Errorf("playback = %+v", pb)
		}
	})

	t.Run("custom profile that supports nothing", func(t *testing.T) {
		pb := decode[Playback](t, post(e.mp4, `{"custom": {"containers": ["webm"]}}`))
		if pb.Method != "transcode" || pb.URL != "" {
			t.Errorf("playback = %+v", pb)
		}
		if len(pb.Reasons) == 0 || !strings.Contains(pb.Reasons[0], "custom") {
			t.Errorf("custom profile should be named in reasons: %q", pb.Reasons)
		}
	})

	t.Run("custom beats profile", func(t *testing.T) {
		pb := decode[Playback](t, post(e.mp4, `{"profile": "avplayer", "custom": {"containers": ["webm"]}}`))
		if pb.Method != "transcode" {
			t.Errorf("method = %s, want the custom profile to win", pb.Method)
		}
	})

	t.Run("explicit audio index", func(t *testing.T) {
		pb := decode[Playback](t, post(e.multi, `{"audioIndex": 2}`))
		if pb.Method != "remux" || pb.AudioIndex != 2 || pb.AudioAction != "transcode" || pb.VideoAction != "copy" {
			t.Errorf("playback = %+v", pb)
		}
		def := decode[Playback](t, post(e.multi, `{}`))
		if def.Method != "direct" || def.AudioIndex != 1 {
			t.Errorf("default playback = %+v", def)
		}
	})

	t.Run("image subtitle forces transcode", func(t *testing.T) {
		pb := decode[Playback](t, post(e.multi, `{"subtitleIndex": 3}`))
		if pb.Method != "transcode" || pb.VideoAction != "transcode" || pb.URL != "" {
			t.Errorf("playback = %+v", pb)
		}
		text := decode[Playback](t, post(e.multi, `{"subtitleIndex": 4}`))
		if text.Method != "direct" {
			t.Errorf("text subtitle playback = %+v", text)
		}
	})

	t.Run("startSec", func(t *testing.T) {
		if pb := decode[Playback](t, post(e.mkv, `{"startSec": 30.5}`)); pb.StartSec != 30.5 {
			t.Errorf("remux startSec = %v, want 30.5", pb.StartSec)
		}
		if pb := decode[Playback](t, post(e.mp4, `{"startSec": 30.5}`)); pb.StartSec != 0 {
			t.Errorf("direct startSec = %v, want 0 (the client seeks itself)", pb.StartSec)
		}
	})

	t.Run("token on direct url", func(t *testing.T) {
		te := newEnv(t, "tok")
		rec := te.do("POST", fmt.Sprintf("/api/items/%d/playback?token=tok", te.mp4), nil, "{}")
		if pb := decode[Playback](t, rec); !strings.HasSuffix(pb.URL, "/file?token=tok") {
			t.Errorf("url = %q", pb.URL)
		}
	})

	for name, tt := range map[string]string{
		"invalid JSON":            `{`,
		"not an object":           `[1,2]`,
		"unknown profile":         `{"profile": "nope"}`,
		"negative startSec":       `{"startSec": -1}`,
		"audio index of video":    `{"audioIndex": 0}`,
		"audio index missing":     `{"audioIndex": 42}`,
		"negative audio index":    `{"audioIndex": -1}`,
		"subtitle index of audio": `{"subtitleIndex": 1}`,
		"subtitle index missing":  `{"subtitleIndex": 42}`,
	} {
		t.Run("400 "+name, func(t *testing.T) {
			wantJSONError(t, post(e.multi, tt), 400)
		})
	}
}

func TestScanEndpoints(t *testing.T) {
	e := newEnv(t, "")

	rec := e.do("POST", "/api/scan", nil, "")
	wantStatus(t, rec, 202)
	if got := decode[map[string]bool](t, rec); !got["started"] {
		t.Errorf("body = %s", rec.Body.String())
	}
	wantJSONError(t, e.do("POST", "/api/scan", nil, ""), 409) // still running

	rec = e.get("/api/scan")
	wantStatus(t, rec, 200)
	st := decode[map[string]any](t, rec)
	for _, k := range []string{"running", "lastStarted", "lastFinished", "lastError", "itemCount"} {
		if _, ok := st[k]; !ok {
			t.Errorf("scan status lacks %q: %s", k, rec.Body.String())
		}
	}
	if st["running"] != true || st["itemCount"] != float64(3) || st["lastFinished"] != nil {
		t.Errorf("scan status = %v", st)
	}
}

func TestSPA(t *testing.T) {
	e := newEnv(t, "")

	t.Run("root", func(t *testing.T) {
		rec := e.get("/")
		wantStatus(t, rec, 200)
		if !strings.Contains(rec.Body.String(), "ply app") {
			t.Errorf("body = %q", rec.Body.String())
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("index Cache-Control = %q", got)
		}
	})
	t.Run("unknown paths fall back to index.html", func(t *testing.T) {
		for _, p := range []string{"/shows", "/shows/Cool%20Show", "/item/7", "/does/not/exist.js", "/assets/", "/assets"} {
			rec := e.get(p)
			wantStatus(t, rec, 200)
			if !strings.Contains(rec.Body.String(), "ply app") {
				t.Errorf("%s: body = %q, want index.html", p, rec.Body.String())
			}
		}
	})
	t.Run("existing files are served as themselves", func(t *testing.T) {
		rec := e.get("/assets/app.js")
		wantStatus(t, rec, 200)
		if rec.Body.String() != "console.log('app')" {
			t.Errorf("body = %q", rec.Body.String())
		}
		if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
			t.Errorf("asset Cache-Control = %q", got)
		}
		if body := e.get("/favicon.svg").Body.String(); body != "<svg/>" {
			t.Errorf("favicon = %q", body)
		}
	})
	t.Run("api paths never fall back to the SPA", func(t *testing.T) {
		rec := e.get("/api/nope")
		wantJSONError(t, rec, 404)
		if strings.Contains(rec.Body.String(), "ply app") {
			t.Error("API 404 served index.html")
		}
	})
	t.Run("path traversal stays inside the UI", func(t *testing.T) {
		rec := e.get("/../../etc/passwd")
		if strings.Contains(rec.Body.String(), "root:") {
			t.Error("served a file from outside the UI filesystem")
		}
	})
	t.Run("non-GET is rejected", func(t *testing.T) {
		wantJSONError(t, e.do("POST", "/", nil, ""), 405)
	})
	t.Run("unbuilt UI", func(t *testing.T) {
		for _, ui := range []fstest.MapFS{{".gitkeep": {}}, {}} {
			srv := New(Options{Store: e.store, Scanner: e.scanner, UI: ui})
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, httptest.NewRequest("GET", "/shows", nil))
			if rec.Code != 404 || !strings.Contains(rec.Body.String(), "not built") {
				t.Errorf("unbuilt UI: %d %q", rec.Code, rec.Body.String())
			}
		}
		srv := New(Options{Store: e.store, Scanner: e.scanner})
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		if rec.Code != 404 {
			t.Errorf("nil UI: %d", rec.Code)
		}
	})
}

// plainWriter hides every optional interface of the writer it wraps, like a
// naive logging/metrics middleware would.
type plainWriter struct{ http.ResponseWriter }

func TestMiddlewareKeepsZeroCopyInterfaces(t *testing.T) {
	type seen struct {
		readerFrom, flusher bool
		typeName            string
	}
	describe := func(w http.ResponseWriter) seen {
		_, rf := w.(io.ReaderFrom)
		_, fl := w.(http.Flusher)
		return seen{rf, fl, fmt.Sprintf("%T", w)}
	}
	outerCh, innerCh := make(chan seen, 1), make(chan seen, 1)

	probe := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		innerCh <- describe(w)
		io.WriteString(w, "ok")
	})
	chain := middleware(probe, "tok")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outerCh <- describe(w)
		chain.ServeHTTP(w, r)
	}))
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/anything?token=tok", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	outer, inner := <-outerCh, <-innerCh

	// Positive control: the server's own writer has the interfaces, and a
	// naive wrapper would lose ReaderFrom (so this test can fail).
	if !outer.readerFrom || !outer.flusher {
		t.Fatalf("net/http's writer %s lacks ReaderFrom/Flusher; the check below is meaningless", outer.typeName)
	}
	if _, ok := any(plainWriter{}).(io.ReaderFrom); ok {
		t.Fatal("control wrapper unexpectedly implements io.ReaderFrom")
	}

	if !inner.readerFrom {
		t.Errorf("handler behind CORS+auth sees %s without io.ReaderFrom: ServeContent would lose sendfile", inner.typeName)
	}
	if !inner.flusher {
		t.Errorf("handler behind CORS+auth sees %s without http.Flusher", inner.typeName)
	}
	if inner.typeName != outer.typeName {
		t.Errorf("middleware wrapped the ResponseWriter: %s -> %s", outer.typeName, inner.typeName)
	}
}

func TestServerEndToEnd(t *testing.T) {
	// Real HTTP connection: Range requests through the full stack, as a
	// player would issue them.
	e := newEnv(t, "s3cret")
	ts := httptest.NewServer(e.srv)
	defer ts.Close()

	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/items/%d/file?token=s3cret", ts.URL, e.mp4), nil)
	req.Header.Set("Range", "bytes=2-5")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 206 || string(body) != "2345" || resp.Header.Get("Content-Range") != "bytes 2-5/16" {
		t.Errorf("status=%d body=%q Content-Range=%q", resp.StatusCode, body, resp.Header.Get("Content-Range"))
	}
	if resp.Header.Get("Content-Encoding") != "" {
		t.Errorf("unexpected Content-Encoding %q", resp.Header.Get("Content-Encoding"))
	}

	resp2, err := http.Get(fmt.Sprintf("%s/api/items/%d/file", ts.URL, e.mp4))
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 401 {
		t.Errorf("unauthenticated file request = %d, want 401", resp2.StatusCode)
	}
}
