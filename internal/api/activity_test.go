package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"ply/internal/jobs"
	"ply/internal/tier"
)

// newActivityEnv is the test server with a running queue of one kind ("work"),
// paused so that what is enqueued stays queued until a test resumes it.
func newActivityEnv(t *testing.T, gov *tier.Governor) (*env, *jobs.Queue) {
	return newActivityEnvToken(t, gov, "")
}

func newActivityEnvToken(t *testing.T, gov *tier.Governor, token string) (*env, *jobs.Queue) {
	t.Helper()
	e := newEnv(t, token)
	q := jobs.New(jobs.Options{Persist: e.store})
	q.Register(jobs.Spec{Kind: "work", Priority: jobs.PriorityCache, Title: func(json.RawMessage) string { return "Some work" },
		Run: func(ctx context.Context, r *jobs.Run) error { return nil }})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { q.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	<-q.Ready()
	if err := q.SetPaused(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	e.srv = New(Options{Store: e.store, Scanner: e.scanner, UI: testUI, ID: "abc123", Name: "Test Server",
		Version: "9.9.9", Token: token, Jobs: q, Governor: gov})
	return e, q
}

// The feed needs the token like the rest of the API; EventSource cannot send a
// header, so it comes as ?token=.
func TestEventsNeedTheToken(t *testing.T) {
	e, _ := newActivityEnvToken(t, nil, "s3cret")
	ts := httptest.NewServer(e.srv)
	defer ts.Close()
	status := func(query string) int {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/events"+query, nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		return res.StatusCode
	}
	if got := status(""); got != http.StatusUnauthorized {
		t.Fatalf("no token: %d, want 401", got)
	}
	if got := status("?token=wrong"); got != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d, want 401", got)
	}
	if got := status("?token=s3cret"); got != http.StatusOK {
		t.Fatalf("?token=: %d, want 200", got)
	}
}

func TestJobsWithoutAQueue(t *testing.T) {
	e := newEnv(t, "")
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/jobs"}, {"POST", "/api/jobs/1/cancel"}, {"POST", "/api/queue/pause"}, {"GET", "/api/events"},
	} {
		if rec := e.do(c.method, c.path, nil, ""); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %d, want 503", c.method, c.path, rec.Code)
		}
	}
}

func TestJobsSnapshotAndActions(t *testing.T) {
	e, q := newActivityEnv(t, nil)
	ctx := context.Background()
	a, _ := q.Enqueue(ctx, "work", jobs.Opt{})
	b, _ := q.Enqueue(ctx, "work", jobs.Opt{})

	snap := decode[jobs.Snapshot](t, e.get("/api/jobs"))
	if !snap.Paused || len(snap.Queued) != 2 || snap.Queued[0].ID != a.ID || snap.Queued[0].Title != "Some work" {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap.Running == nil || snap.History == nil {
		t.Fatal("empty lists must be [] in JSON, not null")
	}

	post := func(path string) *httptest.ResponseRecorder { return e.do("POST", path, nil, "") }
	if rec := post("/api/jobs/" + strconv.FormatInt(b.ID, 10) + "/bump"); rec.Code != http.StatusNoContent {
		t.Fatalf("bump = %d %s", rec.Code, rec.Body)
	}
	if first := decode[jobs.Snapshot](t, e.get("/api/jobs")).Queued[0]; first.ID != b.ID || !first.User {
		t.Fatalf("after bump the first is %+v", first)
	}
	if rec := post("/api/jobs/" + strconv.FormatInt(a.ID, 10) + "/cancel"); rec.Code != http.StatusNoContent {
		t.Fatalf("cancel = %d %s", rec.Code, rec.Body)
	}
	if rec := post("/api/jobs/" + strconv.FormatInt(a.ID, 10) + "/cancel"); rec.Code != http.StatusConflict {
		t.Fatalf("cancel again = %d, want 409", rec.Code)
	}
	rec := post("/api/jobs/" + strconv.FormatInt(a.ID, 10) + "/retry")
	if rec.Code != http.StatusOK {
		t.Fatalf("retry = %d %s", rec.Code, rec.Body)
	}
	if again := decode[jobs.Job](t, rec); again.ID == a.ID || again.State != jobs.Queued || !again.User {
		t.Fatalf("retried job = %+v", again)
	}
	for _, path := range []string{"/api/jobs/999999/cancel", "/api/jobs/x/cancel", "/api/jobs/1/explode"} {
		if rec := post(path); rec.Code != http.StatusNotFound {
			t.Errorf("POST %s = %d, want 404", path, rec.Code)
		}
	}

	if rec := post("/api/queue/resume"); rec.Code != http.StatusNoContent {
		t.Fatalf("resume = %d", rec.Code)
	}
	deadline := time.Now().Add(5 * time.Second)
	for q.Busy("") {
		if time.Now().After(deadline) {
			t.Fatal("the resumed queue did not run its jobs")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if s := decode[jobs.Snapshot](t, e.get("/api/jobs")); s.Paused || len(s.Queued) != 0 {
		t.Fatalf("after resume: %+v", s)
	}
	if rec := post("/api/queue/pause"); rec.Code != http.StatusNoContent || !q.Snapshot().Paused {
		t.Fatalf("pause = %d, paused=%v", rec.Code, q.Snapshot().Paused)
	}
}

// The queue names the library's folders: another site may not read it or act on it.
func TestJobsArePrivate(t *testing.T) {
	e, q := newActivityEnv(t, nil)
	j, _ := q.Enqueue(context.Background(), "work", jobs.Opt{})
	cancel := "/api/jobs/" + strconv.FormatInt(j.ID, 10) + "/cancel"
	evil := map[string]string{"Origin": "http://evil.example"}
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/jobs"}, {"POST", cancel}, {"POST", "/api/queue/pause"}, {"GET", "/api/events"},
	} {
		if rec := e.do(c.method, c.path, evil, ""); rec.Code != http.StatusForbidden {
			t.Errorf("cross-site %s %s = %d, want 403", c.method, c.path, rec.Code)
		}
	}
	if rec := e.do("GET", "/api/jobs", map[string]string{"Origin": "http://example.com"}, ""); rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("CORS opened /api/jobs")
	}
	// Positive control: the server's own page may.
	if rec := e.do("POST", cancel, map[string]string{"Origin": "http://example.com"}, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("same-origin cancel = %d %s", rec.Code, rec.Body)
	}
}

// readEvent reads one SSE event (id, type, data) from r.
func readEvent(t *testing.T, r *bufio.Reader) (id int64, typ, data string) {
	t.Helper()
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("reading the stream: %v", err)
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case line == "":
			if typ != "" {
				return id, typ, data
			}
		case strings.HasPrefix(line, "id: "):
			id, _ = strconv.ParseInt(line[4:], 10, 64)
		case strings.HasPrefix(line, "event: "):
			typ = line[7:]
		case strings.HasPrefix(line, "data: "):
			data = line[6:]
		}
	}
}

func openEvents(t *testing.T, url string, lastID int64) (*bufio.Reader, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", url+"/api/events", nil)
	if lastID > 0 {
		req.Header.Set("Last-Event-ID", strconv.FormatInt(lastID, 10))
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("events: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	return bufio.NewReader(res.Body), func() { cancel(); res.Body.Close() }
}

func TestEventsStreamSnapshotThenChangesAndReplays(t *testing.T) {
	e, q := newActivityEnv(t, nil)
	ts := httptest.NewServer(e.srv)
	defer ts.Close()

	r, closeA := openEvents(t, ts.URL, 0)
	id, typ, data := readEvent(t, r)
	if typ != "snapshot" {
		t.Fatalf("first event %q, want a snapshot", typ)
	}
	var snap jobs.Snapshot
	if err := json.Unmarshal([]byte(data), &snap); err != nil || !snap.Paused {
		t.Fatalf("snapshot %s: %v", data, err)
	}

	j, _ := q.Enqueue(context.Background(), "work", jobs.Opt{})
	var queuedID int64
	for {
		eid, typ, data := readEvent(t, r)
		if typ == "job.queued" {
			var ev jobs.Event
			_ = json.Unmarshal([]byte(data), &ev)
			if ev.Job == nil || ev.Job.ID != j.ID {
				t.Fatalf("job.queued for %s", data)
			}
			queuedID = eid
			break
		}
	}
	if queuedID <= id {
		t.Fatalf("event ids must grow: snapshot %d, queued %d", id, queuedID)
	}
	closeA()

	// A reconnect with the snapshot's id gets what came after it, not a snapshot.
	r2, closeB := openEvents(t, ts.URL, id)
	defer closeB()
	if _, typ, _ := readEvent(t, r2); typ != "job.queued" {
		t.Fatalf("replay began with %q, want job.queued", typ)
	}
}

func TestBrowsingHoldsTheQueueButPollingDoesNot(t *testing.T) {
	gov := tier.New(tier.Options{BrowseQuiet: time.Hour}) // once browsing, held for the whole test
	e, _ := newActivityEnv(t, gov)
	for _, p := range []string{"/api/jobs", "/api/scan", "/api/info", "/api/metadata/status"} {
		e.get(p)
		if h := gov.Check(tier.Need{}); h.Held {
			t.Fatalf("GET %s counted as browsing", p)
		}
	}
	e.get("/api/items")
	if h := gov.Check(tier.Need{}); !h.Held || !strings.Contains(h.Reason, "browsing") {
		t.Fatalf("GET /api/items did not count as browsing: %+v", h)
	}
}

func TestIsBrowse(t *testing.T) {
	cases := []struct {
		method, path string
		want         bool
	}{
		{"GET", "/api/items", true},
		{"GET", "/api/items/12", true},
		{"GET", "/api/items/12/art/backdrop", true},
		{"GET", "/api/shows/Lost", true},
		{"GET", "/api/images/tmdb/w342/x.jpg", true},
		{"GET", "/api/items/12/file", false},            // playback
		{"GET", "/api/items/12/subtitles/3.vtt", false}, // playback
		{"GET", "/api/sessions/s1/seg00001.m4s", false}, // playback
		{"GET", "/api/sessions", false},                 // polling
		{"GET", "/api/events", false},                   // the feed
		{"GET", "/api/jobs", false},                     // the Activity page
		{"GET", "/api/scan", false},                     // polling
		{"GET", "/api/metadata/status", false},          // polling
		{"POST", "/api/items/12/playback", false},       // playback
		{"PUT", "/api/items/12/progress", false},        // playback
		{"PUT", "/api/items/12/watched", false},         // a write
		{"PUT", "/api/shows/Lost/seasons/1/watched", false},
		{"GET", "/assets/app.js", false},   // the app itself
		{"GET", "/api/../api/jobs", false}, // cleaned first
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, "http://x"+c.path, nil)
		r.URL.Path = c.path
		if got := isBrowse(r); got != c.want {
			t.Errorf("isBrowse(%s %s) = %v, want %v", c.method, c.path, got, c.want)
		}
	}
}
