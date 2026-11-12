package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ply/internal/events"
	"ply/internal/stream"
)

const (
	phoneID = "phone-0001"
	tvID    = "tv-0000002"
)

func (e *env) putDevice(id, body string, hdr map[string]string) *httptest.ResponseRecorder {
	return e.do("PUT", "/api/devices/"+id, hdr, body)
}

func TestDevicesRegisterAndList(t *testing.T) {
	e := newEnv(t, "")
	wantStatus(t, e.putDevice(phoneID, `{"name":"Kitchen iPad","kind":"ipados","app":"ply","version":"1.0"}`, nil), http.StatusNoContent)
	// A browser sends no name: it is named after its User-Agent.
	wantStatus(t, e.putDevice(tvID, `{"kind":"web"}`, map[string]string{"User-Agent": safari}), http.StatusNoContent)
	// A name too long is cut.
	long := strings.Repeat("é", maxDeviceField+20)
	wantStatus(t, e.putDevice("long-name-1", fmt.Sprintf(`{"name":%q}`, long), nil), http.StatusNoContent)

	list := decode[[]Device](t, e.get("/api/devices"))
	byID := map[string]Device{}
	for _, d := range list {
		byID[d.ID] = d
	}
	if d := byID[phoneID]; d.Name != "Kitchen iPad" || d.Kind != "ipados" || d.App != "ply" || d.Version != "1.0" || d.LastSeen.IsZero() {
		t.Errorf("phone = %+v", d)
	}
	if d := byID[tvID]; d.Name != "Safari" || d.Kind != "web" {
		t.Errorf("a device with no name = %+v, want it named Safari", d)
	}
	if d := byID["long-name-1"]; len([]rune(d.Name)) != maxDeviceField {
		t.Errorf("a long name was kept at %d characters, want %d", len([]rune(d.Name)), maxDeviceField)
	}

	for _, id := range []string{"short", "has%20space", strings.Repeat("x", 65)} {
		if rec := e.putDevice(id, `{}`, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("PUT a device %q = %d, want 400", id, rec.Code)
		}
	}
	if rec := e.putDevice(phoneID, `{not json`, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("PUT with a body that is not JSON = %d, want 400", rec.Code)
	}
}

// The list of the household's devices is private: another site may neither read it
// nor add to it.
func TestDevicesArePrivate(t *testing.T) {
	e := newEnv(t, "")
	evil := map[string]string{"Origin": "http://evil.example"}
	if rec := e.putDevice(phoneID, `{"name":"x"}`, evil); rec.Code != http.StatusForbidden {
		t.Errorf("cross-site PUT = %d, want 403", rec.Code)
	}
	if rec := e.do("GET", "/api/devices", evil, ""); rec.Code != http.StatusForbidden {
		t.Errorf("cross-site GET = %d, want 403", rec.Code)
	}
	// Positive control: the server's own page may, and gets no CORS headers for it.
	same := map[string]string{"Origin": "http://example.com"}
	rec := e.putDevice(phoneID, `{"name":"x"}`, same)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("same-origin PUT = %d, CORS %q", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}
}

// A direct play has its id from POST /playback; the file's URL carries it, and a
// request with it counts for that play whoever sends it (an Apple TV): one play, not
// two. Heartbeats keep it, and a stopped play is refused as stopped.
func TestADirectPlayHasAnIDThatItsFileURLCarries(t *testing.T) {
	e := newEnv(t, "")
	e.withStreams(fakeFFmpeg(t, "exec sleep 60"), nil)
	pb := decode[Playback](t, e.start(e.mp4, "{}", safari))
	if pb.Method != "direct" || !strings.HasPrefix(pb.SessionID, "d-") || pb.URL != fmt.Sprintf("/api/items/%d/file?sid=%s", e.mp4, pb.SessionID) {
		t.Fatalf("playback = %+v", pb)
	}
	if got := e.sessions(); len(got) != 1 || got[0].ID != pb.SessionID {
		t.Fatalf("after POST /playback: %+v, want the play counted from now", got)
	}
	tv := e.do("GET", pb.URL, map[string]string{"User-Agent": "AppleCoreMedia", "Range": "bytes=0-3"}, "")
	wantStatus(t, tv, http.StatusPartialContent)
	if got := e.sessions(); len(got) != 1 || got[0].ID != pb.SessionID {
		t.Fatalf("the Apple TV's request made another play: %+v", got)
	}
	// Positive control: the same request without the id is another client's play.
	wantStatus(t, e.file(e.mp4, "AppleCoreMedia"), http.StatusPartialContent)
	if got := e.sessions(); len(got) != 2 {
		t.Fatalf("a request without the id: %d plays, want 2", len(got))
	}

	wantStatus(t, e.post("/api/sessions/"+pb.SessionID+"/heartbeat", `{"positionSec":12,"paused":true}`, nil), http.StatusNoContent)
	wantStatus(t, e.do("DELETE", "/api/sessions/"+pb.SessionID, nil, ""), http.StatusNoContent)
	if rec := e.do("GET", pb.URL, map[string]string{"Range": "bytes=0-3"}, ""); rec.Code != http.StatusGone {
		t.Fatalf("the file of a stopped play = %d, want 410", rec.Code)
	}
}

// Two POST /playback of one device for one item, handled in the other order than they
// were sent: the newer attempt's play stays, and the older is answered as stopped.
func TestTheNewerAttemptOfAPlayWins(t *testing.T) {
	e := newEnv(t, "")
	e.withStreams(fakeFFmpeg(t, "exec sleep 60"), nil)
	post := func(attempt int) *httptest.ResponseRecorder {
		t.Helper()
		return e.do("POST", fmt.Sprintf("/api/items/%d/playback", e.mp4), map[string]string{"User-Agent": safari, DeviceHeader: phoneID},
			fmt.Sprintf(`{"attempt":%d}`, attempt))
	}
	newer := post(2)
	wantStatus(t, newer, http.StatusOK)
	id := decode[Playback](t, newer).SessionID
	if rec := post(1); rec.Code != http.StatusGone || !strings.Contains(rec.Body.String(), `"stoppedBy":"a newer attempt"`) {
		t.Fatalf("the older attempt, handled last = %d %s, want 410 stopped by a newer attempt", rec.Code, rec.Body)
	}
	if got := e.sessions(); len(got) != 1 || got[0].ID != id {
		t.Fatalf("playing = %+v, want the newer attempt's play %s", got, id)
	}
	// Positive control: a newer attempt still replaces it.
	latest := post(3)
	wantStatus(t, latest, http.StatusOK)
	if got, next := e.sessions(), decode[Playback](t, latest).SessionID; next == id || len(got) != 1 || got[0].ID != next {
		t.Fatalf("attempt 3: playing %+v, want its play %s alone", got, next)
	}
	// One no client can hold exactly, or below 0, is refused: it would lock the device out.
	for _, a := range []string{"-1", "9007199254740992", "1.5"} {
		rec := e.do("POST", fmt.Sprintf("/api/items/%d/playback", e.mp4), map[string]string{DeviceHeader: phoneID}, `{"attempt":`+a+`}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("attempt %s = %d, want 400", a, rec.Code)
		}
	}
	// Positive control: the largest taken.
	wantStatus(t, e.do("POST", fmt.Sprintf("/api/items/%d/playback", e.mp4), map[string]string{DeviceHeader: phoneID}, `{"attempt":9007199254740991}`), http.StatusOK)
}

// A client that names its device is one client whatever its address or program: two
// User-Agents of one device are one play. Without the device they are two.
func TestPlaysAreKeyedByDevice(t *testing.T) {
	e := newEnv(t, "")
	e.withStreams(fakeFFmpeg(t, "exec sleep 60"), nil)
	get := func(ua, device string) {
		t.Helper()
		hdr := map[string]string{"User-Agent": ua, "Range": "bytes=0-3"}
		if device != "" {
			hdr[DeviceHeader] = device
		}
		wantStatus(t, e.do("GET", fmt.Sprintf("/api/items/%d/file", e.mp4), hdr, ""), http.StatusPartialContent)
	}
	get(safari, "")
	get(chrome, "")
	if n := len(e.sessions()); n != 2 {
		t.Fatalf("two programs, no device: %d plays, want 2", n)
	}
	e.withStreams(fakeFFmpeg(t, "exec sleep 60"), nil) // a fresh count
	get(safari, phoneID)
	get(chrome, phoneID)
	if n := len(e.sessions()); n != 1 {
		t.Fatalf("two programs of one device: %d plays, want 1", n)
	}
	// An id that is not one is ignored: the client is told apart as before.
	get(safari, "bad id")
	if n := len(e.sessions()); n != 2 {
		t.Fatalf("with a bad device id: %d plays, want 2", n)
	}
}

// What plays names the device that started it, as it registered.
func TestAPlayNamesItsDevice(t *testing.T) {
	e := newEnv(t, "")
	e.withStreams(fakeFFmpeg(t, "exec sleep 60"), nil)
	wantStatus(t, e.putDevice(phoneID, `{"name":"Kitchen iPad","kind":"ipados"}`, nil), http.StatusNoContent)
	hdr := map[string]string{"User-Agent": safari, DeviceHeader: phoneID}
	wantStatus(t, e.do("POST", fmt.Sprintf("/api/items/%d/playback", e.mp4), hdr, "{}"), http.StatusOK)
	got := e.sessions()
	if len(got) != 1 || got[0].Device == nil || *got[0].Device != (DeviceRef{ID: phoneID, Name: "Kitchen iPad", Kind: "ipados"}) {
		t.Fatalf("sessions = %+v", got)
	}
	// One that never registered is named by its User-Agent; one with no device has none.
	wantStatus(t, e.do("POST", fmt.Sprintf("/api/items/%d/playback", e.mkv), map[string]string{"User-Agent": chrome, DeviceHeader: tvID}, "{}"), http.StatusOK)
	wantStatus(t, e.start(e.mp4, "{}", "VLC/3.0"), http.StatusOK)
	byDevice := map[string]*DeviceRef{}
	for _, p := range e.sessions() {
		byDevice[p.Client] = p.Device
	}
	if d := byDevice["192.0.2.1 · Chrome"]; d == nil || d.ID != tvID || d.Name != "Chrome" {
		t.Errorf("an unregistered device = %+v", d)
	}
	if d, ok := byDevice["192.0.2.1 · VLC"]; !ok || d != nil {
		t.Errorf("a play with no device: %+v (listed %v), want no device", d, ok)
	}
}

// The live feed tells what plays: session.started when a play starts, session.ended
// with its reason when it ends (main.go connects the manager to the feed so).
func TestTheFeedTellsWhatPlays(t *testing.T) {
	e, q := newActivityEnv(t, nil)
	m := e.withStreams(fakeFFmpeg(t, "exec sleep 60"), nil)
	m.OnPlayChange(func(c stream.PlayChange) {
		typ, data := SessionEventOf(c)
		q.Events().Publish(events.Sessions, typ, data)
	})
	ts := httptest.NewServer(e.srv)
	defer ts.Close()
	r, closeFeed := openFeed(t, ts.URL+"/api/events?topics=sessions", 0)
	defer closeFeed()
	if _, typ, data := readEvent(t, r); typ != "snapshot" || !strings.Contains(data, `"sessions":[]`) {
		t.Fatalf("first: %s %s, want a snapshot with no sessions", typ, data)
	}

	hdr := map[string]string{"User-Agent": safari, DeviceHeader: phoneID}
	pb := decode[Playback](t, e.do("POST", fmt.Sprintf("/api/items/%d/playback", e.mp4), hdr, "{}"))
	_, typ, data := readEvent(t, r)
	var ev SessionEvent
	if err := json.Unmarshal([]byte(data), &ev); err != nil || typ != "session.started" || ev.Session.ID != pb.SessionID ||
		ev.Session.Device == nil || ev.Session.Device.ID != phoneID {
		t.Fatalf("got %s %s, want session.started for %s from %s", typ, data, pb.SessionID, phoneID)
	}
	wantStatus(t, e.do("DELETE", "/api/sessions/"+pb.SessionID, nil, ""), http.StatusNoContent)
	_, typ, data = readEvent(t, r)
	if err := json.Unmarshal([]byte(data), &ev); err != nil || typ != "session.ended" || ev.Session.ID != pb.SessionID || ev.Reason != "stopped" {
		t.Fatalf("got %s %s, want session.ended, stopped", typ, data)
	}
}
