package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// urlTokenOf is the URL token u carries, failing the test when it carries none, or the shared
// token (docs/M11-CLIENTS.md, "URL tokens": a URL never carries a secret).
func urlTokenOf(t *testing.T, u, shared string) string {
	t.Helper()
	q, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	tok := q.Query().Get("token")
	if !strings.HasPrefix(tok, urlTokenPrefix) {
		t.Errorf("%q carries no URL token", u)
	}
	for _, vs := range q.Query() {
		if slices.Contains(vs, shared) && shared != "" {
			t.Errorf("%q carries the shared token", u)
		}
	}
	return tok
}

// withoutToken is u without its token parameter, for comparing the rest of it.
func withoutToken(u string) string {
	p, q, ok := strings.Cut(u, "?")
	if !ok {
		return u
	}
	v, _ := url.ParseQuery(q)
	v.Del("token")
	if len(v) == 0 {
		return p
	}
	return p + "?" + v.Encode()
}

var tokenSuffix = regexp.MustCompile(`\?token=u1\.[A-Za-z0-9_-]*\.[0-9]+\.[A-Za-z0-9_-]+`)

// A URL token opens what it was made for and nothing else: its path, or a playlist's directory; for
// its device; until it expires, and not with a key made from another shared token.
func TestURLTokens(t *testing.T) {
	now := time.Date(2026, 11, 12, 9, 30, 0, 0, time.UTC)
	u := newURLSigner([]byte("secret"), "shared")
	u.now = func() time.Time { return now }
	ok := func(tok, p, wantDevice string) {
		t.Helper()
		if dev, good := u.verify(tok, p); !good || dev != wantDevice {
			t.Errorf("%s for %s: %q, %v; want good for %q", tok, p, dev, good, wantDevice)
		}
	}
	bad := func(tok, p string) {
		t.Helper()
		if dev, good := u.verify(tok, p); good {
			t.Errorf("%s opened %s (device %q)", tok, p, dev)
		}
	}

	file := u.sign(pathScope("/api/items/7/file"), "phone-0001")
	ok(file, "/api/items/7/file", "phone-0001")
	bad(file, "/api/items/8/file")
	bad(file, "/api/items/7/art/poster") // a sibling: the path alone
	bad(file, "/api/items/7/../8/file")

	playlist := u.sign(dirScope("/api/sessions/abc/index.m3u8"), "")
	ok(playlist, "/api/sessions/abc/index.m3u8", "")
	ok(playlist, "/api/sessions/abc/seg00001.m4s", "")
	ok(playlist, "/api/sessions/abc/subtitles.m3u8", "")
	bad(playlist, "/api/sessions/xyz/seg00001.m4s")
	bad(playlist, "/api/sessions/abc/deeper/seg.m4s")
	bad(strings.Replace(file, "phone-0001", "tv-00000002", 1), "/api/items/7/file") // another device
	bad("u1.."+strconv.FormatInt(u.expires(), 10)+".AAAAAAAAAAAAAAAAAAAAAA", "/api/items/7/file")
	bad("garbage", "/api/items/7/file")

	if again := u.sign(pathScope("/api/items/7/file"), "phone-0001"); again != file {
		t.Errorf("a token made later the same day = %q, want the same %q", again, file)
	}
	if exp := time.Unix(u.expires(), 0).UTC(); exp != time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC) {
		t.Errorf("expires %v, want the start of the day after next", exp)
	}

	// A forged expiry far ahead, with a good MAC (as if the key had been used for it), is refused.
	far := now.Add(10 * 24 * time.Hour).Unix()
	bad(urlTokenPrefix+"."+strconv.FormatInt(far, 10)+"."+u.mac(pathScope("/api/items/7/file"), "", far), "/api/items/7/file")

	other := newURLSigner([]byte("secret"), "a new shared token")
	other.now = u.now
	if _, good := other.verify(file, "/api/items/7/file"); good {
		t.Error("a token made before -token changed still opens")
	}

	now = now.Add(48 * time.Hour)
	bad(file, "/api/items/7/file") // expired
}

// pairDevice pairs device id on e (a server with a token) and returns its token.
func pairDevice(t *testing.T, e *env, owner map[string]string, id string) string {
	t.Helper()
	rec := e.post("/api/pairing", "", owner)
	wantStatus(t, rec, 200)
	code := decode[pairingCodeResponse](t, rec).Code
	rec = e.post("/api/pair", fmt.Sprintf(`{"code":%q,"id":%q,"name":"Kitchen iPad","kind":"ipados"}`, code, id), nil)
	wantStatus(t, rec, 200)
	return decode[map[string]string](t, rec)["token"]
}

// The owner shows a code; a device sends it once and gets a token of its own. A wrong code counts
// against it, an expired or spent one is refused.
func TestPairing(t *testing.T) {
	e := newEnv(t, "s3cret")
	owner := map[string]string{"Authorization": "Bearer s3cret"}
	now := time.Now()
	e.srv.Now = func() time.Time { return now }
	pair := func(code string) int {
		return e.post("/api/pair", fmt.Sprintf(`{"code":%q,"id":"phone-0001"}`, code), nil).Code
	}
	if c := pair("123456"); c != 403 {
		t.Errorf("pairing with no code shown = %d, want 403", c)
	}
	newCode := func() string {
		rec := e.post("/api/pairing", "", owner)
		wantStatus(t, rec, 200)
		c := decode[pairingCodeResponse](t, rec)
		if len(c.Code) != 6 || !c.ExpiresAt.Equal(now.Add(pairingCodeFor).UTC().Truncate(time.Nanosecond)) || c.ExpiresIn != 300 {
			t.Errorf("code = %+v", c)
		}
		return c.Code
	}
	wrong := func(code string) string { return fmt.Sprintf("%06d", (mustAtoi(t, code)+1)%1_000_000) }

	code := newCode()
	for range pairingMaxMisses - 1 {
		if c := pair(wrong(code)); c != 403 {
			t.Fatalf("a wrong code = %d, want 403", c)
		}
	}
	if c := pair(code); c != 200 { // control: four misses still leave the code
		t.Fatalf("the right code after %d misses = %d, want 200", pairingMaxMisses-1, c)
	}
	if c := pair(code); c != 403 {
		t.Errorf("the same code again = %d, want 403: it is spent", c)
	}

	code = newCode()
	for range pairingMaxMisses {
		pair(wrong(code))
	}
	if c := pair(code); c != 403 {
		t.Errorf("the right code after %d misses = %d, want 403: guessing spent it", pairingMaxMisses, c)
	}

	code = newCode()
	now = now.Add(pairingCodeFor)
	if c := pair(code); c != 403 {
		t.Errorf("an expired code = %d, want 403", c)
	}

	// Only the owner shows a code.
	device := pairDevice(t, e, owner, "phone-0002")
	if rec := e.post("/api/pairing", "", map[string]string{"Authorization": "Bearer " + device}); rec.Code != 403 {
		t.Errorf("a device asked for a code: %d, want 403", rec.Code)
	}
	if rec := e.post("/api/pairing", "", nil); rec.Code != 401 {
		t.Errorf("no credential asked for a code: %d, want 401", rec.Code)
	}
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// A paired device's token opens what a viewer does and not what only the owner may do, and its
// requests are its own whatever X-Ply-Device says.
func TestDeviceTokens(t *testing.T) {
	e := newEnv(t, "s3cret")
	e.withStreams(fakeFFmpeg(t, "exec sleep 60"), nil)
	owner := map[string]string{"Authorization": "Bearer s3cret"}
	tok := pairDevice(t, e, owner, "phone-0001")
	device := map[string]string{"Authorization": "Bearer " + tok}

	wantStatus(t, e.do("GET", "/api/items", device, ""), 200)
	if rec := e.get("/api/items?token=" + tok); rec.Code != 401 {
		t.Errorf("a device token in a URL = %d, want 401: Bearer only", rec.Code)
	}
	item := fmt.Sprintf("/api/items/%d", e.mp4)
	for _, req := range []struct{ method, path, body string }{
		{"PUT", "/api/settings", `{}`},
		{"POST", "/api/folders", `{}`},
		{"GET", "/api/fs", ""},
		{"POST", "/api/queue/pause", ""},
		{"DELETE", "/api/devices/phone-0001/token", ""},
		{"PUT", "/api/devices/other-0001", `{"name":"spoof"}`},
		{"POST", "/api/scan", ""},
		{"POST", "/api/metadata/refresh", ""},
		{"PUT", item + "/match", `{}`},
		{"DELETE", item + "/match", ""},
		{"POST", item + "/metadata/refresh", ""},
		{"POST", item + "/subtitles", ""},
		{"DELETE", item + "/subtitles/ext0", ""},
		{"POST", "/api/shows/Cool/metadata/refresh", ""},
		{"PATCH", "/api/titles/1", `{}`},
	} {
		if rec := e.do(req.method, req.path, device, req.body); rec.Code != 403 {
			t.Errorf("a device: %s %s = %d, want 403", req.method, req.path, rec.Code)
		}
	}
	// Control: what is a viewer's own is not refused (whatever the handler then makes of it).
	for _, req := range []struct{ method, path, body string }{
		{"PUT", item + "/progress", `{"positionSec":1}`},
		{"PUT", item + "/watched", `{"watched":true}`},
		{"POST", item + "/subtitles/download", `{}`},
		{"PUT", "/api/titles/1/watched", `{"watched":true}`},
	} {
		if rec := e.do(req.method, req.path, device, req.body); rec.Code == 403 || rec.Code == 401 {
			t.Errorf("a device: %s %s = %d, want it let through", req.method, req.path, rec.Code)
		}
	}
	wantStatus(t, e.do("PUT", "/api/devices/phone-0001", device, `{"name":"Kitchen iPad","version":"2"}`), 204) // itself

	list := decode[[]Device](t, e.do("GET", "/api/devices", owner, ""))
	if len(list) != 1 || list[0].ID != "phone-0001" || !list[0].Paired || list[0].PairedAt == nil || list[0].Version != "2" {
		t.Errorf("devices = %+v, want the iPad, paired", list)
	}

	// The play is the token's device, though the header names another.
	spoof := map[string]string{"Authorization": "Bearer " + tok, DeviceHeader: "spoofed-01"}
	wantStatus(t, e.do("POST", fmt.Sprintf("/api/items/%d/playback", e.mp4), spoof, `{}`), 200)
	playing := decode[[]PlayingEntry](t, e.do("GET", "/api/sessions", owner, ""))
	if len(playing) != 1 || playing[0].Device == nil || playing[0].Device.ID != "phone-0001" {
		t.Errorf("playing = %+v, want the play of phone-0001", playing)
	}
}

// Revoking a device ends its token and every URL made for it at once, and nobody else's.
func TestRevokeEndsTheDevicesURLs(t *testing.T) {
	e := newEnv(t, "s3cret")
	owner := map[string]string{"Authorization": "Bearer s3cret"}
	tok := pairDevice(t, e, owner, "phone-0001")
	device := map[string]string{"Authorization": "Bearer " + tok}

	fileURL := func(hdr map[string]string) string {
		t.Helper()
		rec := e.do("POST", fmt.Sprintf("/api/items/%d/playback", e.mp4), hdr, `{}`)
		wantStatus(t, rec, 200)
		return decode[Playback](t, rec).URL
	}
	deviceURL, ownerURL := fileURL(device), fileURL(owner)
	if !strings.Contains(deviceURL, "token=u1.phone-0001.") {
		t.Errorf("the device's url = %q, want a token made for it", deviceURL)
	}
	wantStatus(t, e.get(deviceURL), 200) // control: it opens, with nothing else
	if rec := e.do("PUT", fmt.Sprintf("/api/items/%d/progress?%s", e.mp4, strings.SplitN(deviceURL, "?", 2)[1]), nil, `{"positionSec":1}`); rec.Code != 401 {
		t.Errorf("a URL token for a write = %d, want 401: GET and HEAD only", rec.Code)
	}
	if rec := e.do("GET", "/api/items", map[string]string{"Authorization": "Bearer " + urlTokenOf(t, deviceURL, "s3cret")}, ""); rec.Code != 401 {
		t.Errorf("a URL token as Bearer = %d, want 401", rec.Code)
	}

	wantStatus(t, e.do("DELETE", "/api/devices/phone-0001/token", owner, ""), http.StatusNoContent)
	if rec := e.get(deviceURL); rec.Code != 401 {
		t.Errorf("the revoked device's url = %d, want 401", rec.Code)
	}
	if rec := e.do("GET", "/api/items", device, ""); rec.Code != 401 {
		t.Errorf("the revoked device's token = %d, want 401", rec.Code)
	}
	wantStatus(t, e.get(ownerURL), 200) // anyone else's is untouched
	if rec := e.do("DELETE", "/api/devices/phone-0001/token", owner, ""); rec.Code != 404 {
		t.Errorf("revoking again = %d, want 404", rec.Code)
	}
	list := decode[[]Device](t, e.do("GET", "/api/devices", owner, ""))
	if len(list) != 1 || list[0].Paired {
		t.Errorf("devices = %+v, want the iPad still listed, unpaired", list)
	}
}

// A URL token reads: a playlist's, good for its directory, does not send that session's heartbeats.
func TestURLTokensOnlyRead(t *testing.T) {
	e := newEnv(t, "s3cret")
	e.withStreams(fakeFFmpeg(t, "exec sleep 60"), nil)
	pb := e.startSession("s3cret", `{}`)
	tok := urlTokenOf(t, pb.URL, "s3cret")
	beat := "/api/sessions/" + pb.SessionID + "/heartbeat"
	if dev, ok := e.srv.urls.verify(tok, beat); !ok || dev != "" { // control: the scope covers it
		t.Fatalf("the playlist's token does not cover %s", beat)
	}
	if rec := e.do("POST", beat+"?token="+tok, nil, `{"positionSec":1}`); rec.Code != 401 {
		t.Errorf("a heartbeat with a URL token = %d, want 401: GET and HEAD only", rec.Code)
	}
	wantStatus(t, e.do("HEAD", withoutToken(pb.URL)+"?token="+tok, nil, ""), 200)
}

// Revoking a device ends what it has open, its feed for one, at once.
func TestRevokeEndsWhatTheDeviceHasOpen(t *testing.T) {
	e := newEnv(t, "s3cret")
	owner := map[string]string{"Authorization": "Bearer s3cret"}
	tok := pairDevice(t, e, owner, "phone-0001")
	opened := make(chan struct{})
	ended := make(chan error, 1)
	chain := e.srv.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(opened)
		select {
		case <-r.Context().Done():
			ended <- nil
		case <-time.After(5 * time.Second):
			ended <- fmt.Errorf("still open 5 s after the revoke")
		}
	}))
	go func() {
		req := httptest.NewRequest("GET", "/api/events", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		chain.ServeHTTP(httptest.NewRecorder(), req)
	}()
	<-opened
	select {
	case err := <-ended:
		t.Fatalf("ended before the revoke: %v", err) // control
	case <-time.After(100 * time.Millisecond):
	}
	wantStatus(t, e.do("DELETE", "/api/devices/phone-0001/token", owner, ""), http.StatusNoContent)
	if err := <-ended; err != nil {
		t.Error(err)
	}
}

// The checks read the path as the router does: an encoded slash stays in its segment. A playlist's
// token for one session does not open another's path that decodes into the first's directory, and a
// device cannot hide an owner's path behind encoded slashes.
func TestChecksReadThePathAsTheRouterDoes(t *testing.T) {
	e := newEnv(t, "s3cret")
	e.withStreams(fakeFFmpeg(t, "exec sleep 60"), nil)
	pb := e.startSession("s3cret", `{}`)
	tok := urlTokenOf(t, pb.URL, "s3cret")
	wantStatus(t, e.get("/api/sessions/"+pb.SessionID+"/index.m3u8?token="+tok), 200) // control: its own directory
	smuggled := "/api/sessions/other/x%2F..%2F..%2F" + pb.SessionID + "%2Findex.m3u8?token=" + tok
	if rec := e.get(smuggled); rec.Code != 401 {
		t.Errorf("GET %s = %d, want 401: the router reads another session", smuggled, rec.Code)
	}

	owner := map[string]string{"Authorization": "Bearer s3cret"}
	device := map[string]string{"Authorization": "Bearer " + pairDevice(t, e, owner, "phone-0001")}
	pairDevice(t, e, owner, "other-0001")
	item := fmt.Sprintf("/api/items/%d", e.mp4)
	for _, req := range []struct{ method, plain, encoded string }{
		{"DELETE", "/api/devices/other-0001/token", "/api/devices/x%2f..%2f..%2fz/token"},
		{"DELETE", "/api/devices/other-0001/token", "/api/devices/other-0001/%74oken"},
		{"PATCH", "/api/settings", "/api/%73ettings"},
		{"POST", "/api/pairing", "/api/p%61iring"},
		{"GET", "/api/fs", "/api/%66s"},
		{"DELETE", item + "/subtitles/ext0", item + "/subtitle%73/ext0"},
	} {
		if rec := e.do(req.method, req.plain, device, `{}`); rec.Code != 403 { // control: plainly asked
			t.Errorf("a device: %s %s = %d, want 403", req.method, req.plain, rec.Code)
		}
		if rec := e.do(req.method, req.encoded, device, `{}`); rec.Code != 403 {
			t.Errorf("a device: %s %s = %d, want 403 as for %s", req.method, req.encoded, rec.Code, req.plain)
		}
	}
	if list := decode[[]Device](t, e.do("GET", "/api/devices", owner, "")); !slices.ContainsFunc(list, func(d Device) bool { return d.ID == "other-0001" && d.Paired }) {
		t.Errorf("devices = %+v, want the other one still paired", list)
	}

	// A dot segment, encoded so that the router keeps it as a literal, is refused: a check that cleaned
	// it would follow it where the router does not. A picture's token for a TMDB image, dotted into a
	// TVDB path, opens nothing.
	picture := urlTokenOf(t, e.srv.pictureURL("/api/images/tmdb/w342/x.jpg"), "s3cret")
	for _, p := range []string{"/api/images/tvdb/w342/%2e%2e/%2e%2e/tmdb/w342/x.jpg?token=" + picture, "/api/sessions/%2e%2e/heartbeat", "/api/items/%2E/file"} {
		if rec := e.do("GET", p, nil, ""); rec.Code != 400 {
			t.Errorf("GET %s = %d, want 400", p, rec.Code)
		}
	}

	// A show named with a slash is one segment: an encoded slash is not refused as such.
	if rec := e.do("GET", "/api/shows/AC%2FDC", device, ""); rec.Code == 403 || rec.Code == 401 || rec.Code == 400 {
		t.Errorf("GET /api/shows/AC%%2FDC = %d, want it let through", rec.Code)
	}
}

// A request of a device that is revoked between the check of its credential and its hold is ended
// at once all the same.
func TestHoldOfARevokedDeviceEndsAtOnce(t *testing.T) {
	var p pairedTokens
	p.loaded, p.byHash, p.byID = true, map[string]string{"h": "phone-0001"}, map[string]string{"phone-0001": "h"}
	ctx, cancel := context.WithCancel(context.Background())
	release := p.hold("phone-0001", cancel)
	if ctx.Err() != nil {
		t.Fatal("control: a paired device's request was ended")
	}
	release()
	p.set("phone-0001", "")
	ctx, cancel = context.WithCancel(context.Background())
	defer p.hold("phone-0001", cancel)()
	if ctx.Err() == nil {
		t.Error("a request held after the revoke is still open")
	}
}

// The lists' ETag changes with the day of their pictures' tokens: a list kept from yesterday is
// not said to be current with tokens that run out.
func TestListETagFollowsTheTokensDay(t *testing.T) {
	e := newEnv(t, "s3cret")
	now := time.Now()
	e.srv.urls.now = func() time.Time { return now }
	today := e.srv.libraryETag("")
	if again := e.srv.libraryETag(""); again != today { // control: the same within the day
		t.Fatalf("etag %s then %s within a day", today, again)
	}
	now = now.Add(24 * time.Hour)
	if tomorrow := e.srv.libraryETag(""); tomorrow == today {
		t.Errorf("etag %s on the next day too", today)
	}
	open := newEnv(t, "")
	if tag := open.srv.libraryETag(""); strings.Contains(tag, ".t") {
		t.Errorf("etag %s without a token, want no token part", tag)
	}
}

// tokenSuffixIn is the "?token=..." a playlist's URIs carry.
func tokenSuffixIn(t *testing.T, playlist string) string {
	t.Helper()
	m := tokenSuffix.FindString(playlist)
	if m == "" {
		t.Fatalf("no URL token in the playlist:\n%s", playlist)
	}
	return m
}
