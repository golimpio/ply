package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ply/internal/config"
	"ply/internal/hwaccel"
	"ply/internal/stream"
)

const (
	safari = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15"
	chrome = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
)

// transcodeBody asks for a transcode of the (4K HEVC) episode: nothing it plays is
// small enough.
const transcodeBody = `{"custom": {"name": "small", "containers": ["mp4"], "videoCodecs": ["h264"], ` +
	`"audioCodecs": ["aac"], "maxHeight": 120}}`

type clock struct{ ns atomic.Int64 }

func newClock() *clock {
	c := &clock{}
	c.ns.Store(time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC).UnixNano())
	return c
}
func (c *clock) Now() time.Time          { return time.Unix(0, c.ns.Load()).UTC() }
func (c *clock) Advance(d time.Duration) { c.ns.Add(int64(d)) }

func (e *env) post(target, body string, hdr map[string]string) *httptest.ResponseRecorder {
	return e.do("POST", target, hdr, body)
}

// start asks to play item id with the profile body, as the client with the user agent.
func (e *env) start(id int64, body, ua string) *httptest.ResponseRecorder {
	return e.do("POST", fmt.Sprintf("/api/items/%d/playback", id), map[string]string{"User-Agent": ua}, body)
}

func (e *env) file(id int64, ua string) *httptest.ResponseRecorder {
	return e.do("GET", fmt.Sprintf("/api/items/%d/file", id), map[string]string{"User-Agent": ua, "Range": "bytes=0-3"}, "")
}

func (e *env) sessions() []PlayingEntry {
	e.t.Helper()
	rec := e.get("/api/sessions")
	wantStatus(e.t, rec, 200)
	return decode[[]PlayingEntry](e.t, rec)
}

func keysOf(m map[string]any) []string {
	var k []string
	for key := range m {
		k = append(k, key)
	}
	sort.Strings(k)
	return k
}

// ---- heartbeat ----

func TestHeartbeatRoute(t *testing.T) {
	e := newEnv(t, "tok")
	e.withStreams(fakeFFmpeg(t, "exec sleep 60"), nil)
	auth := map[string]string{"Authorization": "Bearer tok"}
	pb := decode[Playback](t, e.start(e.mkv, `{}`, safari)) // no token: refused (see below)
	if pb.SessionID != "" {
		t.Fatal("a session was started without the token")
	}
	rec := e.do("POST", fmt.Sprintf("/api/items/%d/playback", e.mkv), auth, `{}`)
	wantStatus(t, rec, 200)
	sid := decode[Playback](t, rec).SessionID
	beat := "/api/sessions/" + sid + "/heartbeat"

	// The player's beat while paused: 204, and the listing follows it.
	rec = e.post(beat, `{"positionSec": 1234.5, "paused": true}`, auth)
	wantStatus(t, rec, 204)
	if rec.Body.Len() != 0 {
		t.Errorf("204 with a body: %q", rec.Body.String())
	}
	list := decode[[]PlayingEntry](t, e.do("GET", "/api/sessions", auth, ""))
	if len(list) != 1 || list[0].PositionSec != 1234.5 || !list[0].Paused || list[0].ID != sid {
		t.Errorf("sessions after a heartbeat = %+v", list)
	}
	// A beat may say less, or nothing: it is still a beat, and what it leaves out stays.
	wantStatus(t, e.post(beat, `{"positionSec": 1300}`, auth), 204)
	wantStatus(t, e.post(beat, ``, auth), 204)
	list = decode[[]PlayingEntry](t, e.do("GET", "/api/sessions", auth, ""))
	if list[0].PositionSec != 1300 || !list[0].Paused {
		t.Errorf("after a partial beat: %+v", list[0])
	}
	wantStatus(t, e.post(beat, `{"paused": false}`, auth), 204)
	if list = decode[[]PlayingEntry](t, e.do("GET", "/api/sessions", auth, "")); list[0].Paused || list[0].PositionSec != 1300 {
		t.Errorf("after a beat that resumed: %+v", list[0])
	}

	wantJSONError(t, e.post(beat, `{not json`, auth), 400)
	wantJSONError(t, e.post(beat, `{"positionSec": -3}`, auth), 400)
	wantJSONError(t, e.post(beat, `{"positionSec": "soon"}`, auth), 400)
	wantJSONError(t, e.post(beat, `{}`, nil), 401) // like everything else under /api

	// A session that is unknown: 404, which tells the player to start another. One
	// that was stopped on purpose is 410 (TestAStoppedSessionAnswers410).
	wantJSONError(t, e.post("/api/sessions/nosuchsession/heartbeat", `{}`, auth), 404)
	wantStatus(t, e.do("DELETE", "/api/sessions/"+sid, auth, ""), 204)
	wantJSONError(t, e.post(beat, `{"positionSec": 5}`, auth), 410)
}

func TestHeartbeatWithoutStreams(t *testing.T) {
	e := newEnv(t, "")
	wantJSONError(t, e.post("/api/sessions/x/heartbeat", `{}`, nil), 404)
	rec := e.get("/api/sessions")
	wantStatus(t, rec, 200)
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("sessions without a stream manager = %s, want []", rec.Body.String())
	}
}

// The point of it all, end to end, on a clock the test moves: a paused player
// that beats keeps its session through three minutes of silence on the wire, and
// one that never beat is stopped after the 60 s of no request.
func TestHeartbeatHoldsAPausedSessionPastTheIdleTimeout(t *testing.T) {
	e := newEnv(t, "")
	clk := newClock()
	m := e.withStreams(fakeFFmpeg(t, "exec sleep 60"), func(o *stream.Options) { o.Clock = clk.Now })
	beaten := e.startSession("", `{}`).SessionID
	silent := e.startSession("", `{}`).SessionID

	for i := 0; i < 18; i++ { // the player beats every 10 s
		clk.Advance(10 * time.Second)
		wantStatus(t, e.post("/api/sessions/"+beaten+"/heartbeat", `{"positionSec": 90, "paused": true}`, nil), 204)
		time.Sleep(60 * time.Millisecond) // the monitors tick every 20 ms
		if _, ok := m.Get(silent); i == 4 && !ok {
			t.Fatal("a session that never beat was stopped 50 s in, within its 60 s")
		}
	}
	if _, ok := m.Get(beaten); !ok {
		t.Fatal("the paused session that kept beating was stopped")
	}
	if _, ok := m.Get(silent); ok {
		t.Error("the session that did not beat was not stopped (the beats of another must not keep it)")
	}
	wantJSONError(t, e.get("/api/sessions/"+silent+"/index.m3u8"), 404)
	wantStatus(t, e.get("/api/sessions/"+beaten+"/index.m3u8"), 200)

	clk.Advance(31 * time.Second) // and when the beats stop, 30 s do it
	waitFor(t, "the session whose beats stopped to be stopped", func() bool { return m.Count() == 0 })
}

// blockingWriter is the other end of a slow client: the first Write waits until
// the test lets it go.
type blockingWriter struct {
	h       http.Header
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingWriter() *blockingWriter {
	return &blockingWriter{h: http.Header{}, started: make(chan struct{}), release: make(chan struct{})}
}

func (b *blockingWriter) Header() http.Header { return b.h }
func (b *blockingWriter) WriteHeader(int)     {}
func (b *blockingWriter) Write(p []byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	<-b.release
	return len(p), nil
}

// A player that reads the whole file over one connection (VLC, Infuse, AVPlayer)
// is a request that lasts as long as the film: it is a play for all of it, not
// for 30 s after it began.
func TestADirectPlayIsCountedWhileTheFileIsBeingServed(t *testing.T) {
	e := newEnv(t, "")
	clk := newClock()
	e.withStreams(fakeFFmpeg(t, "exec sleep 60"), func(o *stream.Options) { o.MaxSessions = 1; o.Clock = clk.Now })

	bw := newBlockingWriter()
	req := httptest.NewRequest("GET", fmt.Sprintf("/api/items/%d/file", e.mp4), nil)
	req.Header.Set("User-Agent", safari)
	req.Header.Set("Range", "bytes=0-")
	served := make(chan struct{})
	go func() { e.srv.ServeHTTP(bw, req); close(served) }()
	t.Cleanup(func() {
		select {
		case <-bw.release:
		default:
			close(bw.release)
		}
		<-served
	})
	<-bw.started

	clk.Advance(2 * time.Hour) // a long film
	list := e.sessions()
	if len(list) != 1 || list[0].Kind != "direct" {
		t.Fatalf("after 2 hours of one request, sessions = %+v, want the direct play", list)
	}
	wantStatus(t, e.file(e.mp4, chrome), 503) // the limit is held by the slow one

	close(bw.release) // the film ends
	<-served
	if n := len(e.sessions()); n != 1 {
		t.Fatalf("%d plays right after the request ended, want it counted for DirectTTL more", n)
	}
	clk.Advance(stream.DirectTTL)
	if n := len(e.sessions()); n != 0 {
		t.Errorf("%d plays DirectTTL after it ended", n)
	}
	wantStatus(t, e.file(e.mp4, chrome), 206)
}

// HEAD asks about the file, it does not play it: not counted, not refused.
func TestHeadOfTheFileIsNotAPlay(t *testing.T) {
	e := newEnv(t, "")
	e.withStreams(fakeFFmpeg(t, "exec sleep 60"), func(o *stream.Options) { o.MaxSessions = 1 })
	head := func(ua string) *httptest.ResponseRecorder {
		return e.do("HEAD", fmt.Sprintf("/api/items/%d/file", e.mp4), map[string]string{"User-Agent": ua}, "")
	}
	wantStatus(t, head(safari), 200)
	if n := len(e.sessions()); n != 0 {
		t.Fatalf("a HEAD counted as a play: %+v", e.sessions())
	}
	wantStatus(t, e.file(e.mp4, safari), 206) // a GET does (positive control), and fills the limit
	if n := len(e.sessions()); n != 1 {
		t.Fatalf("a GET is not counted: %d plays", n)
	}
	wantStatus(t, head(chrome), 200) // at the limit, a HEAD from another client is not refused
	if n := len(e.sessions()); n != 1 {
		t.Errorf("the HEAD of another client counted: %+v", e.sessions())
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ---- what is playing ----

func TestSessionsListsEverythingPlaying(t *testing.T) {
	e := newEnv(t, "")
	e.withStreams(fakeFFmpeg(t, "exec sleep 60"), nil)
	if rec := e.get("/api/sessions"); strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("nothing playing = %s, want []", rec.Body.String())
	}

	remux := decode[Playback](t, e.start(e.mkv, `{"startSec": 300}`, safari))
	transcode := decode[Playback](t, e.start(e.mkv, transcodeBody, chrome))
	if remux.Method != "remux" || transcode.Method != "transcode" {
		t.Fatalf("methods = %q, %q; want remux and transcode", remux.Method, transcode.Method)
	}
	wantStatus(t, e.file(e.mp4, "Infuse/7.0 (iPhone)"), 206)

	rec := e.get("/api/sessions")
	wantStatus(t, rec, 200)
	var raw []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"client", "id", "itemId", "kind", "lastSeenAt", "paused", "positionSec", "sessionId", "startedAt", "title"}
	for i, entry := range raw {
		if got := keysOf(entry); strings.Join(got, ",") != strings.Join(wantKeys, ",") {
			t.Errorf("entry %d has keys %v, want %v", i, got, wantKeys)
		}
	}
	list := decode[[]PlayingEntry](t, rec)
	if len(list) != 3 {
		t.Fatalf("%d entries, want 3 (a remux, a transcode and a direct play): %s", len(list), rec.Body)
	}
	by := map[string]PlayingEntry{}
	for _, p := range list {
		by[p.Kind] = p
	}
	r, tr, d := by["remux"], by["transcode"], by["direct"]
	if r.ID != remux.SessionID || r.SessionID != r.ID || r.ItemID != e.mkv || r.Title != "Cool Show S01E02 Pilot" ||
		r.Client != "192.0.2.1 · Safari" || r.PositionSec != 300 || r.Paused || r.StartedAt.IsZero() || r.LastSeenAt.Before(r.StartedAt) {
		t.Errorf("remux entry = %+v", r)
	}
	if tr.ID != transcode.SessionID || tr.Client != "192.0.2.1 · Chrome" || tr.ItemID != e.mkv {
		t.Errorf("transcode entry = %+v", tr)
	}
	if d.ItemID != e.mp4 || d.Title != "Movie" || d.Client != "192.0.2.1 · Infuse" || !strings.HasPrefix(d.ID, "d-") || d.ID != d.SessionID {
		t.Errorf("direct entry = %+v", d)
	}
	for i := 1; i < len(list); i++ {
		if list[i].StartedAt.Before(list[i-1].StartedAt) {
			t.Errorf("not oldest first: %v", list)
		}
	}
	// The same listing needs the token.
	secured := newEnv(t, "tok")
	wantJSONError(t, secured.get("/api/sessions"), 401)
}

// DELETE stops a session, and for a direct play only drops it from the count.
func TestDeleteStopsASessionAndDropsADirectPlay(t *testing.T) {
	e := newEnv(t, "")
	m := e.withStreams(fakeFFmpeg(t, "exec sleep 60"), nil)
	sid := e.startSession("", `{}`).SessionID
	wantStatus(t, e.file(e.mp4, safari), 206)
	var direct string
	for _, p := range e.sessions() {
		if p.Kind == "direct" {
			direct = p.ID
		}
	}
	if direct == "" || len(e.sessions()) != 2 {
		t.Fatalf("sessions = %+v, want the remux and the direct play", e.sessions())
	}
	wantStatus(t, e.do("DELETE", "/api/sessions/"+direct, nil, ""), 204)
	if list := e.sessions(); len(list) != 1 || list[0].ID != sid {
		t.Errorf("after dropping the direct play: %+v", list)
	}
	wantJSONError(t, e.do("DELETE", "/api/sessions/"+direct, nil, ""), 404)
	wantStatus(t, e.do("DELETE", "/api/sessions/"+sid, nil, ""), 204)
	if m.Count() != 0 || len(e.sessions()) != 0 {
		t.Errorf("still playing: %+v", e.sessions())
	}
}

// ---- the limits ----

func TestLimitResponse(t *testing.T) {
	e := newEnv(t, "")
	e.withStreams(fakeFFmpeg(t, "exec sleep 60"), func(o *stream.Options) { o.MaxSessions = 2; o.MaxTranscodes = 1 })
	a := e.startSession("", `{}`)
	b := decode[Playback](t, e.start(e.mkv, transcodeBody, chrome))
	if b.Method != "transcode" {
		t.Fatalf("second play is a %s", b.Method)
	}

	rec := e.start(e.mkv, `{}`, safari)
	wantStatus(t, rec, 503)
	if ra := rec.Header().Get("Retry-After"); ra != "5" {
		t.Errorf("Retry-After = %q, want 5", ra)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != "2 of 2 plays in use" || body["limit"] != "streams" || body["max"] != 2.0 || body["inUse"] != 2.0 {
		t.Errorf("503 body = %v", body)
	}
	playing, _ := body["playing"].([]any)
	if len(playing) != 2 {
		t.Fatalf("playing = %v, want the two plays", body["playing"])
	}
	ids := map[string]string{}
	for _, p := range playing {
		entry := p.(map[string]any)
		if got := keysOf(entry); strings.Join(got, ",") != "client,id,itemId,kind,lastSeenAt,paused,positionSec,sessionId,startedAt,title" {
			t.Errorf("entry keys = %v", got)
		}
		ids[entry["sessionId"].(string)] = entry["kind"].(string)
	}
	if ids[a.SessionID] != "remux" || ids[b.SessionID] != "transcode" {
		t.Errorf("playing = %v, want %s remux and %s transcode", ids, a.SessionID, b.SessionID)
	}

	// Stopping one makes room, for a play of its kind or the other.
	wantStatus(t, e.do("DELETE", "/api/sessions/"+a.SessionID, nil, ""), 204)
	wantStatus(t, e.start(e.mkv, `{}`, safari), 200)
}

func TestTranscodeLimitResponse(t *testing.T) {
	e := newEnv(t, "")
	e.withStreams(fakeFFmpeg(t, "exec sleep 60"), func(o *stream.Options) { o.MaxSessions = 4; o.MaxTranscodes = 1 })
	remux := e.startSession("", `{}`)
	first := decode[Playback](t, e.start(e.mkv, transcodeBody, chrome))
	rec := e.start(e.mkv, transcodeBody, safari)
	wantStatus(t, rec, 503)
	if rec.Header().Get("Retry-After") != "5" {
		t.Errorf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
	var lr LimitReached
	if err := json.Unmarshal(rec.Body.Bytes(), &lr); err != nil {
		t.Fatal(err)
	}
	if lr.Error != "1 of 1 transcodes in use" || lr.Limit != "transcodes" || lr.Max != 1 || lr.InUse != 1 {
		t.Errorf("503 body = %+v", lr)
	}
	if len(lr.Playing) != 1 || lr.Playing[0].SessionID != first.SessionID || lr.Playing[0].Kind != "transcode" {
		t.Errorf("playing = %+v, want only the transcode (stopping the remux %s would not help)", lr.Playing, remux.SessionID)
	}
	wantStatus(t, e.start(e.mkv, `{}`, safari), 200) // a remux still fits
}

// The player's own stop-then-start at the cap.
func TestStopThenStartAtTheCapOverHTTP(t *testing.T) {
	e := newEnv(t, "")
	e.withStreams(fakeFFmpeg(t, "exec sleep 60"), func(o *stream.Options) { o.MaxSessions = 1 })
	first := e.startSession("", `{}`)
	wantStatus(t, e.start(e.mkv, `{}`, safari), 503) // positive control: it is full
	for i := 0; i < 3; i++ {
		wantStatus(t, e.do("DELETE", "/api/sessions/"+first.SessionID, nil, ""), 204)
		first = e.startSession("", `{"startSec": 30}`)
	}
}

func TestDirectPlaysCountAgainstTheLimit(t *testing.T) {
	e := newEnv(t, "")
	clk := newClock()
	e.withStreams(fakeFFmpeg(t, "exec sleep 60"), func(o *stream.Options) { o.MaxSessions = 2; o.Clock = clk.Now; o.IdleTimeout = time.Hour })

	wantStatus(t, e.file(e.mp4, safari), 206)
	wantStatus(t, e.file(e.mp4, safari), 206) // the same client again: the same play
	if n := len(e.sessions()); n != 1 {
		t.Fatalf("two range requests of one client are %d plays, want 1", n)
	}
	wantStatus(t, e.file(e.mp4, chrome), 206) // another client: a second play, which fills the limit

	// The limit is reached by direct plays alone: everything else is told, with the list.
	rec := e.file(e.mp4, "Infuse/7.0")
	wantStatus(t, rec, 503)
	if rec.Header().Get("Retry-After") != "5" {
		t.Errorf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
	var lr LimitReached
	if err := json.Unmarshal(rec.Body.Bytes(), &lr); err != nil {
		t.Fatal(err)
	}
	if lr.Error != "2 of 2 plays in use" || len(lr.Playing) != 2 || lr.Playing[0].Kind != "direct" || lr.Playing[0].ItemID != e.mp4 {
		t.Errorf("503 body = %s", rec.Body)
	}
	wantStatus(t, e.file(e.mp4, safari), 206) // a play that is counted is never refused
	wantStatus(t, e.start(e.mkv, `{}`, safari), 503)
	rec = e.start(e.mp4, `{}`, "Infuse/7.0") // POST /playback for a direct play says it too, before the file is asked for
	wantStatus(t, rec, 503)
	if !strings.Contains(rec.Body.String(), `"playing"`) {
		t.Errorf("the 503 to a direct play announcement has no list: %s", rec.Body)
	}
	if n := len(e.sessions()); n != 2 {
		t.Errorf("announcing a direct play counted it: %d plays", n)
	}
	wantStatus(t, e.start(e.mp4, `{}`, safari), 200) // this client is playing it already

	// They expire 30 s after the last request.
	clk.Advance(29 * time.Second)
	wantStatus(t, e.file(e.mp4, "Infuse/7.0"), 503)
	wantStatus(t, e.file(e.mp4, safari), 206) // keeps safari's alive
	clk.Advance(2 * time.Second)              // chrome's is 31 s old, safari's 2 s
	wantStatus(t, e.file(e.mp4, "Infuse/7.0"), 206)
	if list := e.sessions(); len(list) != 2 {
		t.Errorf("after chrome's expired: %+v", list)
	}
}

// ---- the settings ----

func TestPlaybackSettingsViewAndPrecedence(t *testing.T) {
	e := newEnv(t, "")
	m := e.withStreams(fakeFFmpeg(t, "exec sleep 60"), nil)
	ctx := context.Background()
	view := func() SettingsView {
		t.Helper()
		rec := e.get("/api/settings")
		wantStatus(t, rec, 200)
		return decode[SettingsView](t, rec)
	}
	limits := func() [2]int { s, tr := m.Limits(); return [2]int{s, tr} }

	// Defaults: 4 plays; 1 transcode with a software encoder.
	e.srv.ApplyLimits(ctx)
	v := view()
	if v.MaxStreams != 4 || v.MaxStreamsSource != "default" || v.MaxTranscodes != 1 || v.MaxTranscodesSource != "default" {
		t.Errorf("defaults = %+v", v)
	}
	raw := decode[map[string]any](t, e.get("/api/settings"))
	for k, want := range map[string]any{"maxStreams": 4.0, "maxTranscodes": 1.0, "maxStreamsSource": "default", "maxTranscodesSource": "default"} {
		if raw[k] != want {
			t.Errorf("settings[%s] = %v, want %v", k, raw[k], want)
		}
	}
	if limits() != [2]int{4, 1} {
		t.Errorf("manager limits = %v", limits())
	}

	// A hardware encoder allows three.
	e.srv.HW = hwaccel.Static(hwaccel.Info{Encoder: hwaccel.Encoder{Kind: hwaccel.VideoToolbox, Name: "h264_videotoolbox"}})
	if v := view(); v.MaxTranscodes != 3 || v.MaxTranscodesSource != "default" {
		t.Errorf("with VideoToolbox: %+v, want 3 transcodes by default", v)
	}
	e.srv.ApplyLimits(ctx)
	if limits() != [2]int{4, 3} {
		t.Errorf("manager limits with a hardware encoder = %v", limits())
	}

	// PATCH stores, and applies at once.
	rec := e.do("PATCH", "/api/settings", nil, `{"maxStreams": 6, "maxTranscodes": 2}`)
	wantStatus(t, rec, 200)
	v = decode[SettingsView](t, rec)
	if v.MaxStreams != 6 || v.MaxStreamsSource != "settings" || v.MaxTranscodes != 2 || v.MaxTranscodesSource != "settings" {
		t.Errorf("after PATCH = %+v", v)
	}
	if limits() != [2]int{6, 2} {
		t.Errorf("manager limits after PATCH = %v, want them applied live", limits())
	}
	if got, ok, _ := e.store.GetSetting(ctx, "playback.max_streams"); !ok || got != "6" {
		t.Errorf("stored max_streams = %q, %v", got, ok)
	}
	if got, ok, _ := e.store.GetSetting(ctx, "playback.max_transcodes"); !ok || got != "2" {
		t.Errorf("stored max_transcodes = %q, %v", got, ok)
	}
	// One at a time, and PUT is the same call.
	v = decode[SettingsView](t, e.do("PUT", "/api/settings", nil, `{"maxStreams": 8}`))
	if v.MaxStreams != 8 || v.MaxTranscodes != 2 || limits() != [2]int{8, 2} {
		t.Errorf("after PUT of one limit: %+v, manager %v", v, limits())
	}

	// A value from the command line is above the settings, and cannot be changed here.
	e.srv.MaxStreamsFlag = 5
	v = view()
	if v.MaxStreams != 5 || v.MaxStreamsSource != "flag" || v.MaxTranscodes != 2 || v.MaxTranscodesSource != "settings" {
		t.Errorf("with -max-streams 5 and 8 in the settings: %+v", v)
	}
	rec = e.do("PATCH", "/api/settings", nil, `{"maxStreams": 3}`)
	wantJSONError(t, rec, 409)
	if !strings.Contains(rec.Body.String(), "-max-streams") {
		t.Errorf("the 409 does not say how it was set: %s", rec.Body)
	}
	if got, _, _ := e.store.GetSetting(ctx, "playback.max_streams"); got != "8" {
		t.Errorf("a refused change was stored: %q", got)
	}
	// ... but the other one still can, even in the same call as a refused one: nothing is half done.
	wantJSONError(t, e.do("PATCH", "/api/settings", nil, `{"maxStreams": 3, "maxTranscodes": 9}`), 409)
	if got, _, _ := e.store.GetSetting(ctx, "playback.max_transcodes"); got != "2" {
		t.Errorf("max_transcodes = %q after a refused call; it must not be half done", got)
	}
	wantStatus(t, e.do("PATCH", "/api/settings", nil, `{"maxTranscodes": 4}`), 200)
	e.srv.ApplyLimits(ctx)
	if limits() != [2]int{5, 4} {
		t.Errorf("manager limits = %v, want the flag's 5 and the setting's 4", limits())
	}
	e.srv.MaxTranscodesFlag = 2
	if v := view(); v.MaxTranscodes != 2 || v.MaxTranscodesSource != "flag" {
		t.Errorf("with both flags: %+v", v)
	}
	wantJSONError(t, e.do("PATCH", "/api/settings", nil, `{"maxTranscodes": 1}`), 409)
}

func TestPlaybackSettingsValidation(t *testing.T) {
	e := newEnv(t, "")
	m := e.withStreams(fakeFFmpeg(t, "exec sleep 60"), nil)
	for _, body := range []string{
		`{"maxStreams": 0}`, `{"maxStreams": -1}`, `{"maxStreams": 33}`, `{"maxStreams": 1.5}`, `{"maxStreams": "4"}`,
		`{"maxTranscodes": 0}`, `{"maxTranscodes": 17}`, `{"maxTranscodes": -2}`,
		`{"maxStreams": 4, "maxTranscodes": 17}`, `{}`, `{"maxStreams": null}`,
	} {
		wantJSONError(t, e.do("PATCH", "/api/settings", nil, body), 400)
	}
	if got, ok, _ := e.store.GetSetting(context.Background(), "playback.max_streams"); ok {
		t.Errorf("a refused change was stored: %q", got)
	}
	if s, tr := m.Limits(); s != 4 || tr != 1 {
		t.Errorf("limits = %d, %d after refused changes", s, tr)
	}
	// The ends are allowed.
	for _, body := range []string{`{"maxStreams": 1, "maxTranscodes": 1}`, `{"maxStreams": 32, "maxTranscodes": 16}`} {
		wantStatus(t, e.do("PATCH", "/api/settings", nil, body), 200)
	}
	if s, tr := m.Limits(); s != 32 || tr != 16 {
		t.Errorf("limits = %d, %d, want 32, 16", s, tr)
	}
}

// A stored value that is not a number in range is ignored, not used.
func TestPlaybackSettingsIgnoreAnUnusableStoredValue(t *testing.T) {
	e := newEnv(t, "")
	for _, v := range []string{"abc", "0", "99", "-3", ""} {
		if err := e.store.SetSetting(context.Background(), "playback.max_streams", v); err != nil {
			t.Fatal(err)
		}
		if got := decode[SettingsView](t, e.get("/api/settings")); got.MaxStreams != 4 || got.MaxStreamsSource != "default" {
			t.Errorf("stored %q: %+v, want the default", v, got)
		}
	}
}

// Everything in a change of settings is checked before anything is stored.
func TestPlaybackSettingsTogetherWithMetadataSettings(t *testing.T) {
	se := newSettingsEnv(t)
	rec := se.do("PATCH", "/api/settings", `{"language": "pt-BR", "maxStreams": 99}`)
	wantStatus(t, rec, 400)
	if got := decode[SettingsView](t, se.get("/api/settings")); got.Language != "en-US" {
		t.Errorf("language = %q: it was changed by a call that was refused", got.Language)
	}
	rec = se.do("PATCH", "/api/settings", `{"language": "pt-BR", "maxStreams": 7}`)
	wantStatus(t, rec, 200)
	if got := decode[SettingsView](t, rec); got.Language != "pt-BR" || got.MaxStreams != 7 || got.MaxStreamsSource != "settings" {
		t.Errorf("after a call with both: %+v", got)
	}
	// A key that TMDB refuses leaves the limits alone.
	wantStatus(t, se.do("PATCH", "/api/settings", `{"tmdbKey": "wrongkey", "maxStreams": 9}`), 400)
	if got := decode[SettingsView](t, se.get("/api/settings")); got.MaxStreams != 7 {
		t.Errorf("maxStreams = %d: a call that failed on the key changed it", got.MaxStreams)
	}
}

// ---- who may see and touch what is playing ----

func TestPrivatePaths(t *testing.T) {
	for path, want := range map[string]bool{
		"/api/folders": true, "/api/folders/3": true, "/api/fs": true, "/api/fs/x": true, "/api/sessions": true,
		"/api/sessions/": true, "/api/sessions/abc": true, "/api/sessions/abc/heartbeat": true,
		"/api/sessions/../folders": true,
		// The media of a session is fetched by Cast and AirPlay receivers from other origins.
		"/api/sessions/abc/index.m3u8": false, "/api/sessions/abc/init.mp4": false, "/api/sessions/abc/seg00001.m4s": false,
		"/api/sessions/abc/heartbeat/x": false, "/api/sessionsx": false, "/api/items": false, "/api/items/1/file": false,
		"/api/settings": false, "/": false,
		// What logs in to a provider with a key the page cannot see.
		"/api/settings/test": true, "/api/settings/test/": true, "/api/settings/../settings/test": true, "/api/settings/testx": false,
	} {
		if got := private(path); got != want {
			t.Errorf("private(%q) = %v, want %v", path, got, want)
		}
	}
}

// What is playing, with the titles and addresses, and the calls that stop or hold a
// session, are for ply's own pages and native clients: a page of another site gets
// no CORS headers, and is refused whatever it sends.
func TestSessionsEndpointsAreForTheServersOwnPagesOnly(t *testing.T) {
	e := newEnv(t, "")
	m := e.withStreams(fakeFFmpeg(t, "exec sleep 60"), nil)
	sid := e.startSession("", `{}`).SessionID
	wantStatus(t, e.file(e.mp4, safari), 206)
	beat := "/api/sessions/" + sid + "/heartbeat"
	cross := map[string]string{"Origin": "https://evil.example"}
	site := map[string]string{"Sec-Fetch-Site": "cross-site"}

	// Positive controls: ply's own page (httptest requests are for example.com), and a
	// native client that sends neither header, get the answer, and no CORS headers.
	for name, hdr := range map[string]map[string]string{
		"native client": nil, "own page": {"Origin": "http://example.com"}, "own page, Fetch metadata": {"Sec-Fetch-Site": "same-origin"},
	} {
		rec := e.do("GET", "/api/sessions", hdr, "")
		wantStatus(t, rec, 200)
		if list := decode[[]PlayingEntry](t, rec); len(list) != 2 {
			t.Errorf("%s: %d entries, want the remux and the direct play", name, len(list))
		}
		if acao := rec.Header().Get("Access-Control-Allow-Origin"); acao != "" {
			t.Errorf("%s: Access-Control-Allow-Origin = %q on a private endpoint", name, acao)
		}
	}
	// ... unlike an ordinary endpoint, which is open to every origin (contrast).
	if rec := e.do("GET", "/api/items", cross, ""); rec.Code != 200 || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("contrast: GET /api/items from another origin = %d, ACAO %q", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}

	for name, hdr := range map[string]map[string]string{"another origin": cross, "Sec-Fetch-Site: cross-site": site} {
		rec := e.do("GET", "/api/sessions", hdr, "")
		wantJSONError(t, rec, 403)
		if strings.Contains(rec.Body.String(), "Cool Show") || strings.Contains(rec.Body.String(), "192.0.2.1") {
			t.Errorf("%s: the refusal shows what is playing: %s", name, rec.Body)
		}
		if rec.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s: refused, but with CORS headers", name)
		}
		// Nor may it hold a session, or stop it.
		withBody := map[string]string{}
		for k, v := range hdr {
			withBody[k] = v
		}
		wantJSONError(t, e.post(beat, `{"positionSec": 99}`, withBody), 403)
		wantJSONError(t, e.do("DELETE", "/api/sessions/"+sid, hdr, ""), 403)
		if _, ok := m.Get(sid); !ok {
			t.Fatalf("%s: the session was stopped by a page of another site", name)
		}
	}
	if p := e.sessions()[0]; p.PositionSec == 99 {
		t.Error("a heartbeat from another site was taken")
	}

	// A preflight is not answered with permission either (contrast: the media of the session is).
	pre := map[string]string{"Origin": "https://evil.example", "Access-Control-Request-Method": "DELETE"}
	for _, p := range []string{"/api/sessions", "/api/sessions/" + sid, beat} {
		if rec := e.do("OPTIONS", p, pre, ""); rec.Header().Get("Access-Control-Allow-Origin") != "" || rec.Header().Get("Access-Control-Allow-Methods") != "" {
			t.Errorf("OPTIONS %s allows another origin: %v", p, rec.Header())
		}
	}
	media := "/api/sessions/" + sid + "/index.m3u8"
	if rec := e.do("OPTIONS", media, pre, ""); rec.Code != 204 || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("contrast: the preflight of the playlist = %d, ACAO %q; Cast needs it open", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}
	if rec := e.do("GET", media, cross, ""); rec.Code != 200 || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("the playlist, from another origin = %d, ACAO %q; it must stay open", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}

	// And the page itself can do all of it.
	own := map[string]string{"Origin": "http://example.com"}
	wantStatus(t, e.post(beat, `{"positionSec": 11}`, own), 204)
	if p := e.sessions()[0]; p.ID != sid || p.PositionSec != 11 { // oldest first: the remux, then the direct play
		t.Errorf("the heartbeat of the page was not taken: %+v", e.sessions())
	}
	wantStatus(t, e.do("DELETE", "/api/sessions/"+sid, own, ""), 204)
	if _, ok := m.Get(sid); ok {
		t.Error("the page could not stop its own session")
	}
}

// The list in the 503 is what GET /api/sessions would show: not to a page of
// another site, which may ask for the file (Cast does).
func TestLimitListIsNotShownToOtherSites(t *testing.T) {
	e := newEnv(t, "")
	e.withStreams(fakeFFmpeg(t, "exec sleep 60"), func(o *stream.Options) { o.MaxSessions = 1 })
	wantStatus(t, e.file(e.mp4, safari), 206)

	ask := func(hdr map[string]string) LimitReached {
		hdr["User-Agent"], hdr["Range"] = chrome, "bytes=0-3"
		rec := e.do("GET", fmt.Sprintf("/api/items/%d/file", e.mp4), hdr, "")
		wantStatus(t, rec, 503)
		if rec.Header().Get("Retry-After") != "5" {
			t.Errorf("Retry-After = %q", rec.Header().Get("Retry-After"))
		}
		return decode[LimitReached](t, rec)
	}
	if lr := ask(map[string]string{"Origin": "http://example.com"}); lr.Error != "1 of 1 plays in use" || len(lr.Playing) != 1 {
		t.Fatalf("own page: %+v, want the list with the one play", lr) // positive control
	}
	for name, hdr := range map[string]map[string]string{"another origin": {"Origin": "https://evil.example"}, "cross-site": {"Sec-Fetch-Site": "cross-site"}} {
		lr := ask(hdr)
		if lr.Error != "1 of 1 plays in use" || lr.Max != 1 || lr.InUse != 1 {
			t.Errorf("%s: %+v, the count is still told", name, lr)
		}
		if lr.Playing == nil || len(lr.Playing) != 0 {
			t.Errorf("%s: playing = %+v, want an empty list", name, lr.Playing)
		}
	}
}

// wantStopped checks what a player is told when its session was stopped on
// purpose: 410, {"error": "stopped", "stoppedBy": "another device"}.
func wantStopped(t *testing.T, rec *httptest.ResponseRecorder, what string) {
	t.Helper()
	if rec.Code != 410 {
		t.Errorf("%s = %d %s, want 410", what, rec.Code, rec.Body)
		return
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body) != 2 || body["error"] != "stopped" || body["stoppedBy"] != "another device" {
		t.Errorf("%s: body %s, want {\"error\":\"stopped\",\"stoppedBy\":\"another device\"}", what, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("%s: Content-Type %q", what, ct)
	}
}

// A session stopped on purpose (DELETE: the Activity page, the cap panel) is told
// so, 410, by its heartbeat and its playlist, init and segments, for two minutes;
// a player restarts on a 404, which is for a session that is unknown or that ply
// ended because nothing was heard from it.
func TestAStoppedSessionAnswers410(t *testing.T) {
	e := newEnv(t, "")
	clk := newClock()
	m := e.withStreams(fakeFFmpeg(t, "exec sleep 60"), func(o *stream.Options) { o.Clock = clk.Now })
	stopped := e.startSession("", `{}`).SessionID
	lost := e.startSession("", `{}`).SessionID
	paths := func(sid string) map[string]string {
		base := "/api/sessions/" + sid
		return map[string]string{"playlist": base + "/index.m3u8", "init": base + "/init.mp4", "segment": base + "/seg00001.m4s"}
	}

	// Positive controls: both are alive and answer.
	for _, sid := range []string{stopped, lost} {
		wantStatus(t, e.get(paths(sid)["playlist"]), 200)
		wantStatus(t, e.post("/api/sessions/"+sid+"/heartbeat", `{}`, nil), 204)
	}

	wantStatus(t, e.do("DELETE", "/api/sessions/"+stopped, nil, ""), 204)
	wantStopped(t, e.post("/api/sessions/"+stopped+"/heartbeat", `{"positionSec": 5}`, nil), "heartbeat of a stopped session")
	for what, p := range paths(stopped) {
		wantStopped(t, e.get(p), what+" of a stopped session")
	}
	wantJSONError(t, e.do("DELETE", "/api/sessions/"+stopped, nil, ""), 404) // there is nothing to stop any more
	wantJSONError(t, e.post("/api/sessions/nosuchsession/heartbeat", `{}`, nil), 404)

	// A session that ply ended for its silence is lost, not stopped: 404, and the
	// player starts another.
	clk.Advance(61 * time.Second) // (the player of `lost` beat once: 30 s is its limit)
	waitFor(t, "the silent session to be ended", func() bool { _, ok := m.Get(lost); return !ok })
	wantJSONError(t, e.post("/api/sessions/"+lost+"/heartbeat", `{}`, nil), 404)
	for _, p := range paths(lost) {
		wantJSONError(t, e.get(p), 404)
	}
	wantStopped(t, e.post("/api/sessions/"+stopped+"/heartbeat", `{}`, nil), "the stopped one, 61 s on")

	// And two minutes after the stop it is a session like any that is gone.
	clk.Advance(stream.StoppedTTL - 61*time.Second - time.Nanosecond)
	wantStopped(t, e.post("/api/sessions/"+stopped+"/heartbeat", `{}`, nil), "a nanosecond before the end of the 2 minutes")
	clk.Advance(time.Nanosecond)
	wantJSONError(t, e.post("/api/sessions/"+stopped+"/heartbeat", `{}`, nil), 404)
	wantJSONError(t, e.get(paths(stopped)["playlist"]), 404)
}

// A direct play dropped by DELETE gets 410 for its next request for the file, and
// for its id; another client is not affected, and a client that announces a play
// again (POST /playback) may have the file again.
func TestADirectPlayStoppedOnPurposeGets410(t *testing.T) {
	e := newEnv(t, "")
	clk := newClock()
	e.withStreams(fakeFFmpeg(t, "exec sleep 60"), func(o *stream.Options) { o.Clock = clk.Now })
	wantStatus(t, e.file(e.mp4, safari), 206)
	var id string
	for _, p := range e.sessions() {
		if p.Kind == "direct" {
			id = p.ID
		}
	}
	if id == "" {
		t.Fatalf("setup: no direct play in %+v", e.sessions())
	}
	wantStatus(t, e.post("/api/sessions/"+id+"/heartbeat", `{"paused": true}`, nil), 204) // positive control: it is beaten

	wantStatus(t, e.do("DELETE", "/api/sessions/"+id, nil, ""), 204)
	wantStopped(t, e.file(e.mp4, safari), "the next request for the file")
	wantStopped(t, e.post("/api/sessions/"+id+"/heartbeat", `{}`, nil), "the heartbeat of the dropped play")
	if n := len(e.sessions()); n != 0 {
		t.Errorf("a refused request counted as a play: %+v", e.sessions())
	}
	wantStatus(t, e.file(e.mp4, chrome), 206) // another client

	// The user plays it again from this client: the page asks for a play first.
	wantStatus(t, e.start(e.mp4, `{}`, safari), 200)
	wantStatus(t, e.file(e.mp4, safari), 206)

	// Or the two minutes pass.
	var again string
	for _, p := range e.sessions() {
		if p.Client == "192.0.2.1 · Safari" {
			again = p.ID
		}
	}
	wantStatus(t, e.do("DELETE", "/api/sessions/"+again, nil, ""), 204)
	wantStopped(t, e.file(e.mp4, safari), "the file after the second stop")
	clk.Advance(stream.StoppedTTL)
	wantStatus(t, e.file(e.mp4, safari), 206)
}

// A hidden tab beats about once a minute; a paused player has 3 minutes from its
// last beat, and the 30 s are back when it plays.
func TestAPausedPlayerIsKeptThreeMinutes(t *testing.T) {
	e := newEnv(t, "")
	clk := newClock()
	m := e.withStreams(fakeFFmpeg(t, "exec sleep 60"), func(o *stream.Options) { o.Clock = clk.Now })
	sid := e.startSession("", `{}`).SessionID
	beat := "/api/sessions/" + sid + "/heartbeat"
	wantStatus(t, e.post(beat, `{"positionSec": 600, "paused": true}`, nil), 204)

	clk.Advance(170 * time.Second) // one beat a minute is enough, a few late
	time.Sleep(100 * time.Millisecond)
	if _, ok := m.Get(sid); !ok {
		t.Fatal("a paused player that beat 170 s ago was stopped")
	}
	wantStatus(t, e.post(beat, `{"paused": false}`, nil), 204) // it plays again
	clk.Advance(31 * time.Second)
	waitFor(t, "the player that plays, silent for 31 s, to be stopped", func() bool { _, ok := m.Get(sid); return !ok })
	wantJSONError(t, e.post(beat, `{}`, nil), 404) // lost, not stopped
}

// A limit given by a flag or an environment variable cannot be changed in the
// settings, and the 409 says which of the two to look at.
func TestTheLimitConflictNamesTheEnvironmentVariable(t *testing.T) {
	for _, c := range []struct {
		env, field, flag string
	}{
		{config.MaxStreamsEnv, "maxStreams", "-max-streams"},
		{config.MaxTranscodesEnv, "maxTranscodes", "-max-transcodes"},
	} {
		t.Run(c.field, func(t *testing.T) {
			e := newEnv(t, "")
			t.Setenv(c.env, "")
			e.srv.MaxStreamsFlag, e.srv.MaxTranscodesFlag = 5, 2
			body := fmt.Sprintf(`{%q: 1}`, c.field)
			rec := e.do("PATCH", "/api/settings", nil, body)
			wantJSONError(t, rec, 409)
			if msg := rec.Body.String(); !strings.Contains(msg, c.flag+" or "+c.env) {
				t.Errorf("the 409 for a flag is %s, want it to name %s and %s", msg, c.flag, c.env)
			}
			t.Setenv(c.env, "3") // the variable is what is in use
			rec = e.do("PATCH", "/api/settings", nil, body)
			wantJSONError(t, rec, 409)
			if msg := rec.Body.String(); !strings.Contains(msg, c.env+" (or "+c.flag+")") {
				t.Errorf("the 409 with %s set is %s, want it to name the variable first", c.env, msg)
			}
		})
	}
}
