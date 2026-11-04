package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

// The fake TVDB: an httptest server that logs in, checks the bearer token and
// answers from the fixtures in testdata/tvdb. No test talks to the network, and
// none of the keys, PINs and tokens in them is real.

// tvdbKeys are the query parameters that tell one request from another.
var tvdbKeys = []string{"query", "year", "type", "language", "page", "since", "action", "meta", "short"}

func tvdbCanon(q url.Values) string {
	var kv []string
	for _, k := range tvdbKeys {
		if v := q.Get(k); v != "" {
			kv = append(kv, k, v)
		}
	}
	return params(kv...)
}

// The credentials the fake accepts, and the start of every token it issues.
const (
	tvdbTestKey   = "c0ffee00-1d2e-4f3a-8b4c-5d6e7f8091a2"
	tvdbTestPIN   = "QX7M2K9P"
	tvdbTokenHead = "eyJhbGciOiJIUzI1NiJ9.eyJhdWQiOiJ0dmRiLXRlc3QifQ."
)

// tvdbExt is the query of /series/{id}/extended.
var tvdbExt = params("meta", "translations", "short", "false")

type tvdbFake struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	routes   map[string]fakeRoute
	reqs     []fakeReq
	key, pin string   // what /login accepts
	issued   int      // tokens handed out
	valid    string   // the one token the routes accept: none before a login, or after expire
	refuse   bool     // every authenticated request is answered 401, whatever the token
	tokens   []string // every token handed out
	bodies   []string // the bodies of the logins
}

func newTVDBFake(t *testing.T) *tvdbFake {
	t.Helper()
	f := &tvdbFake{t: t, routes: map[string]fakeRoute{}, key: tvdbTestKey, pin: tvdbTestPIN}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *tvdbFake) URL() string { return f.srv.URL + "/v4" }

// on answers 200 with a fixture for the path and canonical params.
func (f *tvdbFake) on(path, params, file string) {
	f.set(path, params, fakeRoute{status: 200, file: file})
}

// fail answers with a status, a fixture (or empty) and headers.
func (f *tvdbFake) fail(path, params string, status int, file string, headers map[string]string) {
	f.set(path, params, fakeRoute{status: status, file: file, headers: headers})
}

func (f *tvdbFake) handle(path, params string, h http.HandlerFunc) {
	f.set(path, params, fakeRoute{handler: h})
}

var totalItemsRe = regexp.MustCompile(`"total_items":\s*\d+`)

// onTotal answers 200 with a fixture whose links.total_items is set to total.
// The fixtures of long lists are cut to a few entries and keep TVDB's own count
// (890 for The Simpsons), which a list that adds up needs to match.
func (f *tvdbFake) onTotal(path, params, file string, total int) {
	f.handle(path, params, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(totalItemsRe.ReplaceAll(f.fixture(file), []byte(`"total_items": `+strconv.Itoa(total))))
	})
}

func (f *tvdbFake) set(path, params string, r fakeRoute) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[path+"|"+params] = r
}

// expire makes the server forget its token, as it does when one runs out.
func (f *tvdbFake) expire() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.valid = ""
}

// setPIN changes the PIN the server accepts, as a user does at TVDB's end.
func (f *tvdbFake) setPIN(pin string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pin = pin
}

func (f *tvdbFake) refuseAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refuse = true
}

func (f *tvdbFake) currentToken() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.valid
}

func (f *tvdbFake) issuedTokens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.tokens)
}

func (f *tvdbFake) loginBodies() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.bodies)
}

func (f *tvdbFake) serve(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v4")
	req := fakeReq{Path: path, Params: tvdbCanon(r.URL.Query()), Query: r.URL.Query(), Header: r.Header.Clone()}
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	rt, ok := f.routes[path+"|"+req.Params]
	valid, refuse := f.valid, f.refuse
	f.mu.Unlock()

	if path == "/login" {
		if !ok {
			f.login(w, r)
			return
		}
	} else if valid == "" || refuse || r.Header.Get("Authorization") != "Bearer "+valid {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write(f.fixture("error_401.json"))
		return
	}

	switch {
	case ok && rt.handler != nil:
		rt.handler(w, r)
	case ok:
		for k, v := range rt.headers {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rt.status)
		if rt.file != "" {
			w.Write(f.fixture(rt.file))
		} else {
			w.Write([]byte(rt.body))
		}
	case path == "/search":
		// A search nobody prepared finds nothing, as TVDB would.
		w.Header().Set("Content-Type", "application/json")
		w.Write(f.fixture("search_empty.json"))
	default:
		f.t.Errorf("fake TVDB: unexpected request %s", req)
		w.WriteHeader(http.StatusNotImplemented)
	}
}

// login is POST /login: it checks the credentials and hands out a new token,
// which is the only one the routes accept from then on.
func (f *tvdbFake) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		f.t.Errorf("fake TVDB: /login with %s", r.Method)
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		f.t.Errorf("fake TVDB: /login with Content-Type %q", ct)
	}
	if r.Header.Get("Authorization") != "" {
		f.t.Errorf("fake TVDB: /login with an Authorization header")
	}
	body, _ := io.ReadAll(r.Body)
	var cred struct {
		APIKey string `json:"apikey"`
		PIN    string `json:"pin"`
	}
	json.Unmarshal(body, &cred)

	f.mu.Lock()
	f.bodies = append(f.bodies, string(body))
	if cred.APIKey != f.key || cred.PIN != f.pin {
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write(f.fixture("login_401.json"))
		return
	}
	f.issued++
	tok := fmt.Sprintf("%ssig-%d", tvdbTokenHead, f.issued)
	f.valid = tok
	f.tokens = append(f.tokens, tok)
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.Write(bytes.ReplaceAll(f.fixture("login.json"), []byte("{{token}}"), []byte(tok)))
}

func (f *tvdbFake) fixture(name string) []byte {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "tvdb", name))
	if err != nil {
		f.t.Fatalf("fixture %s: %v", name, err)
	}
	return data
}

// requests returns what the server has been asked, in order.
func (f *tvdbFake) requests() []fakeReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reqs)
}

// asked returns the requests as "path?params" strings.
func (f *tvdbFake) asked() []string {
	var out []string
	for _, r := range f.requests() {
		out = append(out, r.String())
	}
	return out
}

func (f *tvdbFake) count(path string) int {
	n := 0
	for _, r := range f.requests() {
		if r.Path == path {
			n++
		}
	}
	return n
}

func (f *tvdbFake) logins() int { return f.count("/login") }

// testTVDBClient returns a client for the fake with fast settings and a silent logger.
func testTVDBClient(t *testing.T, f *tvdbFake, mod ...func(*TVDBOptions)) *TVDBClient {
	t.Helper()
	o := TVDBOptions{Key: f.key, PIN: f.pin, BaseURL: f.URL(), Rate: 1000, Timeout: 5 * time.Second, Logger: discardLogger()}
	for _, m := range mod {
		m(&o)
	}
	return NewTVDBClient(o)
}

// useTVDBClock makes the client (and its token bucket) run on the fake clock.
func useTVDBClock(c *TVDBClient, clk *fakeClock, rate float64, burst int) {
	c.now, c.sleep = clk.now, clk.sleep
	c.bucket = newBucket(rate, burst, clk.now)
}

func TestTVDBLoginIsLazyAndTheTokenIsReused(t *testing.T) {
	ctx := t.Context()
	f := newTVDBFake(t)
	f.on("/search", params("query", "Breaking Bad", "type", "series"), "search_breaking_bad.json")
	f.on("/series/81189/extended", tvdbExt, "series_81189_extended.json")
	f.on("/people/296140/extended", "", "person_296140.json")
	c := testTVDBClient(t, f)
	if n := len(f.requests()); n != 0 {
		t.Fatalf("NewTVDBClient made %d requests; it logs in with the first call", n)
	}

	if _, err := c.Search(ctx, "Breaking Bad", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SeriesExtended(ctx, 81189); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Person(ctx, 296140); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Search(ctx, "Breaking Bad", 0); err != nil {
		t.Fatal(err)
	}

	if got, want := f.asked(), []string{
		"/login",
		"/search?query=Breaking Bad&type=series",
		"/series/81189/extended?meta=translations&short=false",
		"/people/296140/extended",
		"/search?query=Breaking Bad&type=series",
	}; !slices.Equal(got, want) {
		t.Errorf("requests:\n got %q\nwant %q", got, want)
	}
	tokens := f.issuedTokens()
	if len(tokens) != 1 {
		t.Fatalf("%d tokens issued, want 1", len(tokens))
	}
	for _, r := range f.requests()[1:] {
		if got := r.Header.Get("Authorization"); got != "Bearer "+tokens[0] {
			t.Errorf("%s: Authorization = %q, want the token from the login", r.Path, got)
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("%s: Accept = %q", r.Path, r.Header.Get("Accept"))
		}
	}
	if got := f.requests()[0].Header.Get("Authorization"); got != "" {
		t.Errorf("the login carried Authorization %q", got)
	}
}

func TestTVDBLoginBodyCarriesThePINOnlyWhenThereIsOne(t *testing.T) {
	for _, tc := range []struct {
		name, key, pin, wantPIN string
		want                    string // the body as it should arrive
	}{
		{"key and PIN", tvdbTestKey, tvdbTestPIN, tvdbTestPIN, `{"apikey":"` + tvdbTestKey + `","pin":"` + tvdbTestPIN + `"}`},
		{"key alone", tvdbTestKey, "", "", `{"apikey":"` + tvdbTestKey + `"}`},
		{"a PIN of only blanks is no PIN", tvdbTestKey, "  \n", "", `{"apikey":"` + tvdbTestKey + `"}`},
		{"blanks around the key and PIN are cut", "  " + tvdbTestKey + "\n", " " + tvdbTestPIN + " ", tvdbTestPIN, `{"apikey":"` + tvdbTestKey + `","pin":"` + tvdbTestPIN + `"}`},
	} {
		f := newTVDBFake(t)
		f.setPIN(tc.wantPIN) // what the server expects to be sent
		c := testTVDBClient(t, f, func(o *TVDBOptions) { o.Key, o.PIN = tc.key, tc.pin })
		if err := c.Check(t.Context()); err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		bodies := f.loginBodies()
		if len(bodies) != 1 || bodies[0] != tc.want {
			t.Errorf("%s: login body %q, want %q", tc.name, bodies, tc.want)
		}
		if tc.wantPIN == "" && strings.Contains(bodies[0], "pin") {
			t.Errorf("%s: the login mentions a PIN when there is none: %s", tc.name, bodies[0])
		}
	}

	// Positive control: a server that wants a PIN is not satisfied by the key alone.
	f := newTVDBFake(t)
	c := testTVDBClient(t, f, func(o *TVDBOptions) { o.PIN = "" })
	if err := c.Check(t.Context()); !IsUnauthorized(err) {
		t.Errorf("a PIN that is needed and not sent: err = %v", err)
	}
}

func TestTVDBTokenIsRenewedAfter25Days(t *testing.T) {
	ctx := t.Context()
	f, clk := newTVDBFake(t), newFakeClock()
	f.on("/series/81189/extended", tvdbExt, "series_81189_extended.json")
	c := testTVDBClient(t, f)
	useTVDBClock(c, clk, 1000, 1000)

	if _, err := c.SeriesExtended(ctx, 81189); err != nil {
		t.Fatal(err)
	}
	clk.advance(25*24*time.Hour - time.Second)
	if _, err := c.SeriesExtended(ctx, 81189); err != nil {
		t.Fatal(err)
	}
	if f.logins() != 1 {
		t.Fatalf("%d logins after 24 days 23:59:59, want 1: the token is still good", f.logins())
	}
	clk.advance(time.Second)
	if _, err := c.SeriesExtended(ctx, 81189); err != nil {
		t.Fatal(err)
	}
	if f.logins() != 2 {
		t.Errorf("%d logins after 25 days, want 2", f.logins())
	}
	tokens := f.issuedTokens()
	last := f.requests()[len(f.requests())-1]
	if len(tokens) != 2 || last.Path != "/series/81189/extended" || last.Header.Get("Authorization") != "Bearer "+tokens[1] {
		t.Errorf("the renewed token was not the one sent: %v, %q", tokens, last.Header.Get("Authorization"))
	}
	// The new token is good for another 25 days.
	clk.advance(24 * 24 * time.Hour)
	if _, err := c.SeriesExtended(ctx, 81189); err != nil || f.logins() != 2 {
		t.Errorf("after the renewal: err %v, %d logins", err, f.logins())
	}
}

func TestTVDBReLoginsOnceOnA401(t *testing.T) {
	ctx := t.Context()
	f := newTVDBFake(t)
	f.on("/series/81189/extended", tvdbExt, "series_81189_extended.json")
	c := testTVDBClient(t, f)
	if _, err := c.SeriesExtended(ctx, 81189); err != nil {
		t.Fatal(err)
	}

	// The server forgets the token; the next call renews it and goes through.
	f.expire()
	if s, err := c.SeriesExtended(ctx, 81189); err != nil || s.Name != "Breaking Bad" {
		t.Fatalf("after the token was refused: %v, %v", s, err)
	}
	if got, want := f.asked(), []string{
		"/login",
		"/series/81189/extended?meta=translations&short=false",
		"/series/81189/extended?meta=translations&short=false", // refused: 401
		"/login",
		"/series/81189/extended?meta=translations&short=false",
	}; !slices.Equal(got, want) {
		t.Errorf("requests:\n got %q\nwant %q", got, want)
	}
	tokens := f.issuedTokens()
	reqs := f.requests()
	if len(tokens) != 2 || reqs[2].Header.Get("Authorization") != "Bearer "+tokens[0] || reqs[4].Header.Get("Authorization") != "Bearer "+tokens[1] {
		t.Errorf("tokens: %v; the retry should carry the new one", tokens)
	}

	// A server that refuses every token: one new login and one retry, then a
	// final error. Not a loop.
	f2 := newTVDBFake(t)
	f2.on("/series/81189/extended", tvdbExt, "series_81189_extended.json")
	f2.refuseAll()
	c2 := testTVDBClient(t, f2)
	_, err := c2.SeriesExtended(ctx, 81189)
	var e *TVDBError
	if !errors.As(err, &e) || e.Status != 401 || e.Transient || e.Message != tvdbRejected {
		t.Fatalf("err = %#v, want a final 401", err)
	}
	if !IsUnauthorized(err) || IsTransient(err) {
		t.Errorf("IsUnauthorized %v IsTransient %v", IsUnauthorized(err), IsTransient(err))
	}
	if f2.logins() != 2 || f2.count("/series/81189/extended") != 2 {
		t.Errorf("%d logins and %d requests; want 2 and 2 (the first, and one retry after a new login)", f2.logins(), f2.count("/series/81189/extended"))
	}
	// And it is remembered: the calls after it do not log in and ask again each time.
	for i := 0; i < 3; i++ {
		if _, err := c2.SeriesExtended(ctx, 81189); !IsUnauthorized(err) || IsTransient(err) || !strings.Contains(err.Error(), tvdbRejected) {
			t.Fatalf("call %d after the refusal: %v", i+2, err)
		}
	}
	if f2.logins() != 2 || f2.count("/series/81189/extended") != 2 {
		t.Errorf("%d logins and %d requests after 3 more calls; want 2 and 2: a refusal is remembered", f2.logins(), f2.count("/series/81189/extended"))
	}
}

// A 403 is about the record, not the key: TVDB's message is kept, nobody logs in
// again, and the next call is not turned away.
func TestTVDBForbiddenIsAboutTheRecord(t *testing.T) {
	ctx := t.Context()
	f := newTVDBFake(t)
	f.set("/series/81189/extended", tvdbExt, fakeRoute{status: 403, body: `{"status":"failure","message":"series is for subscribers"}`})
	f.on("/people/296140/extended", "", "person_296140.json")
	c := testTVDBClient(t, f)
	_, err := c.SeriesExtended(ctx, 81189)
	var e *TVDBError
	if !errors.As(err, &e) || e.Status != 403 || e.Transient || e.Message != "series is for subscribers" {
		t.Fatalf("err = %#v, want a final 403 with TVDB's message", err)
	}
	if strings.Contains(err.Error(), tvdbRejected) {
		t.Errorf("%q calls a forbidden record a bad key or PIN", err)
	}
	if IsUnauthorized(err) || IsTransient(err) || IsNotFound(err) {
		t.Errorf("a forbidden record: IsUnauthorized %v IsTransient %v IsNotFound %v; want none", IsUnauthorized(err), IsTransient(err), IsNotFound(err))
	}
	if f.logins() != 1 {
		t.Errorf("%d logins; a 403 is no reason to log in again", f.logins())
	}
	if _, err := c.Person(ctx, 296140); err != nil {
		t.Errorf("the call after a 403: %v", err)
	}
	if f.logins() != 1 {
		t.Errorf("%d logins after the next call", f.logins())
	}

	// A 403 from the login itself is a refusal of the key or PIN.
	login := newTVDBFake(t)
	login.fail("/login", "", 403, "", nil)
	if err := testTVDBClient(t, login).Check(ctx); !IsUnauthorized(err) || IsTransient(err) || !strings.Contains(err.Error(), tvdbRejected) {
		t.Errorf("a 403 from /login: err = %v, want a refusal of the key or PIN", err)
	}
}

func TestTVDBConcurrentCallsLogInOnce(t *testing.T) {
	ctx := t.Context()
	f := newTVDBFake(t)
	f.on("/series/81189/extended", tvdbExt, "series_81189_extended.json")
	f.handle("/login", "", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(60 * time.Millisecond) // long enough for the others to arrive
		f.login(w, r)
	})
	c := testTVDBClient(t, f)
	run := func() {
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := c.SeriesExtended(ctx, 81189); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
	}

	run()
	if f.logins() != 1 {
		t.Errorf("%d logins for 8 calls on a client with no token, want 1", f.logins())
	}
	// All of them see the token refused at once; the token is renewed once, and
	// the calls that were late use the one the first got.
	f.expire()
	run()
	if f.logins() != 2 {
		t.Errorf("%d logins after the token was refused to 8 calls, want 2", f.logins())
	}
	if n := f.count("/series/81189/extended"); n < 16 {
		t.Errorf("%d data requests, want at least 16", n)
	}
}

func TestTVDBBadPIN(t *testing.T) {
	ctx := t.Context()
	f, clk := newTVDBFake(t), newFakeClock()
	f.on("/series/81189/extended", tvdbExt, "series_81189_extended.json")
	const wrongPIN = "WRONG123"
	c := testTVDBClient(t, f, func(o *TVDBOptions) { o.PIN = wrongPIN })
	useTVDBClock(c, clk, 1000, 1000)

	err := c.Check(ctx)
	var e *TVDBError
	if !errors.As(err, &e) || e.Status != 401 || e.Transient || e.Message != tvdbRejected {
		t.Fatalf("Check = %#v, want a final 401 saying the key or PIN was rejected", err)
	}
	if !IsUnauthorized(err) || IsTransient(err) || IsNotFound(err) {
		t.Errorf("IsUnauthorized %v IsTransient %v IsNotFound %v", IsUnauthorized(err), IsTransient(err), IsNotFound(err))
	}
	if msg := err.Error(); !strings.Contains(msg, "TVDB rejected the key or PIN") || !strings.Contains(msg, "POST /login") ||
		strings.Contains(msg, wrongPIN) || strings.Contains(msg, tvdbTestKey) {
		t.Errorf("error text %q", msg)
	}
	if f.logins() != 1 {
		t.Fatalf("%d logins, want 1: a 401 from the login itself is not retried", f.logins())
	}

	// Later calls are refused at once, without asking TVDB again for every show.
	for i := 0; i < 3; i++ {
		if _, err := c.SeriesExtended(ctx, 81189); !IsUnauthorized(err) || IsTransient(err) || !strings.Contains(err.Error(), tvdbRejected) {
			t.Fatalf("call %d: err = %v", i+1, err)
		}
	}
	if f.logins() != 1 || f.count("/series/81189/extended") != 0 {
		t.Errorf("%d logins and %d data requests after a refusal; want no more", f.logins(), f.count("/series/81189/extended"))
	}
	// Check always asks (the Test button).
	if err := c.Check(ctx); !IsUnauthorized(err) || f.logins() != 2 {
		t.Errorf("Check after a refusal: %v, %d logins", err, f.logins())
	}
	// The refusal is forgotten after a minute, to the nanosecond.
	clk.advance(tvdbRejectTTL - time.Nanosecond)
	if _, err := c.SeriesExtended(ctx, 81189); !IsUnauthorized(err) || f.logins() != 2 {
		t.Errorf("a nanosecond before the minute: %v, %d logins", err, f.logins())
	}
	clk.advance(time.Nanosecond)
	if _, err := c.SeriesExtended(ctx, 81189); !IsUnauthorized(err) || f.logins() != 3 {
		t.Errorf("at the minute: %v, %d logins; want another try", err, f.logins())
	}

	// Positive control: with the right PIN the same server and calls work, and a
	// Check that succeeds clears the memory of a refusal.
	f.setPIN(wrongPIN) // the user's PIN was changed at the other end
	if err := c.Check(ctx); err != nil {
		t.Fatalf("Check with an accepted PIN: %v", err)
	}
	if _, err := c.SeriesExtended(ctx, 81189); err != nil {
		t.Errorf("after a good Check: %v", err)
	}
}

// A Check that works wipes out the memory of a refusal: if the token is refused
// again inside the minute, the call logs in again instead of being turned away
// on account of a PIN that has been put right.
func TestTVDBCheckClearsTheRefusal(t *testing.T) {
	ctx := t.Context()
	f, clk := newTVDBFake(t), newFakeClock()
	f.on("/series/81189/extended", tvdbExt, "series_81189_extended.json")
	const wrongPIN = "WRONG123"
	c := testTVDBClient(t, f, func(o *TVDBOptions) { o.PIN = wrongPIN })
	useTVDBClock(c, clk, 1000, 1000)
	if err := c.Check(ctx); !IsUnauthorized(err) {
		t.Fatalf("Check with a wrong PIN: %v", err)
	}
	f.setPIN(wrongPIN) // fixed at the other end
	if err := c.Check(ctx); err != nil {
		t.Fatal(err)
	}
	f.expire() // the token is refused once, inside the minute
	if _, err := c.SeriesExtended(ctx, 81189); err != nil {
		t.Errorf("after a good Check and a refused token inside the window: %v", err)
	}
}

func TestTVDBNoKeyMakesNoRequest(t *testing.T) {
	f := newTVDBFake(t)
	c := testTVDBClient(t, f, func(o *TVDBOptions) { o.Key = "  " })
	for name, call := range map[string]func() error{
		"Check":  func() error { return c.Check(t.Context()) },
		"Search": func() error { _, err := c.Search(t.Context(), "x", 0); return err },
	} {
		err := call()
		if !IsUnauthorized(err) || IsTransient(err) || !strings.Contains(err.Error(), "no TVDB key") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if n := len(f.requests()); n != 0 {
		t.Errorf("%d requests without a key", n)
	}
}

func TestTVDBErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		file      string
		body      string
		headers   map[string]string
		transient bool
		notFound  bool
		unauth    bool
		message   string
	}{
		{"404", 404, "error_404.json", "", nil, false, true, false, "NotFoundException: error fetching series"},
		{"401 even after a new login", 401, "error_401.json", "", nil, false, false, true, tvdbRejected},
		{"403 with TVDB's message: about the record, not the key", 403, "", `{"message":"subscribers only"}`, nil, false, false, false, "subscribers only"},
		{"400", 400, "", "", nil, false, false, false, ""},
		{"422", 422, "", "", nil, false, false, false, ""},
		{"500", 500, "", "", nil, true, false, false, ""},
		{"502 with an html body", 502, "", "<html>Bad gateway</html>", nil, true, false, false, ""},
		{"503", 503, "", "", nil, true, false, false, ""},
		{"504", 504, "", "", nil, true, false, false, ""},
		{"408", 408, "", "", nil, true, false, false, ""},
		{"425 too early", 425, "", "", nil, true, false, false, ""},
		{"3xx is a final error, not followed", 307, "", "", map[string]string{"Location": "https://elsewhere.example/v4"}, false, false, false, "redirect not followed"},
		{"429 to wait out later", 429, "error_429.json", "", map[string]string{"Retry-After": "3600"}, true, false, false, "Too many requests"},
	} {
		f := newTVDBFake(t)
		f.set("/series/81189/extended", tvdbExt, fakeRoute{status: tc.status, file: tc.file, body: tc.body, headers: tc.headers})
		_, err := testTVDBClient(t, f).SeriesExtended(t.Context(), 81189)
		var e *TVDBError
		if !errors.As(err, &e) {
			t.Errorf("%s: error %v is not a *TVDBError", tc.name, err)
			continue
		}
		if IsTransient(err) != tc.transient || IsNotFound(err) != tc.notFound || IsUnauthorized(err) != tc.unauth {
			t.Errorf("%s: transient %v notFound %v unauthorized %v; want %v %v %v",
				tc.name, IsTransient(err), IsNotFound(err), IsUnauthorized(err), tc.transient, tc.notFound, tc.unauth)
		}
		if e.Status != tc.status || !strings.Contains(e.Message, tc.message) || !strings.Contains(err.Error(), fmt.Sprint(tc.status)) {
			t.Errorf("%s: %+v / %q", tc.name, e, err)
		}
		if !strings.Contains(err.Error(), "GET /series/81189/extended") || !strings.HasPrefix(err.Error(), "tvdb ") {
			t.Errorf("%s: %q does not say what failed", tc.name, err)
		}
	}
}

func TestTVDBErrorsAreSeenByTheTMDBHelpers(t *testing.T) {
	retry := &TVDBError{Op: "GET /updates", Status: 429, Transient: true, RetryAfter: 3 * time.Hour}
	wrapped := fmt.Errorf("refreshing: %w", retry)
	for name, err := range map[string]error{"direct": retry, "wrapped": wrapped} {
		if !IsTransient(err) || IsNotFound(err) || IsUnauthorized(err) {
			t.Errorf("%s: IsTransient %v IsNotFound %v IsUnauthorized %v", name, IsTransient(err), IsNotFound(err), IsUnauthorized(err))
		}
		if got := RetryDelay(err, 1); got != 3*time.Hour {
			t.Errorf("%s: RetryDelay(1) = %v, want the Retry-After", name, got)
		}
		if got := RetryDelay(err, 5); got != 6*time.Hour {
			t.Errorf("%s: RetryDelay(5) = %v, want the backoff", name, got)
		}
		var e *TVDBError
		if !errors.As(err, &e) || e != retry {
			t.Errorf("%s: errors.As to *TVDBError gave %v", name, e)
		}
	}
	if !IsNotFound(&TVDBError{Status: 404}) || IsTransient(&TVDBError{Status: 404}) {
		t.Error("a 404 is a not-found and not transient")
	}
	if !IsUnauthorized(&TVDBError{Status: 401}) || !IsUnauthorized(tvdbRefused("POST /login", 403)) || !IsUnauthorized(tvdbRefused("GET /x", 401)) {
		t.Error("a 401, and a 403 that refuses the key or PIN, are unauthorized")
	}

	// A 403 that is about a record is none of: unauthorized (the worker would end
	// its pass on it), not found (it would take the title for gone) or transient
	// (it would be asked again at once). It is a final failure of that record.
	for _, tc := range []struct{ name, msg, want string }{
		{"with TVDB's message", "series is for subscribers", "forbidden for this record: series is for subscribers"},
		{"without one", "", "forbidden for this record"},
	} {
		record := &TVDBError{Op: "GET /series/1/extended", Status: 403, Message: tc.msg}
		for name, err := range map[string]error{"direct": record, "wrapped": fmt.Errorf("matching: %w", record)} {
			if IsUnauthorized(err) || IsTransient(err) || IsNotFound(err) {
				t.Errorf("%s, %s: IsUnauthorized %v IsTransient %v IsNotFound %v; want none", tc.name, name, IsUnauthorized(err), IsTransient(err), IsNotFound(err))
			}
			var e *Error
			if !errors.As(err, &e) || e.Message != tc.want || e.Status != 0 || e.Transient {
				t.Errorf("%s, %s: seen as %+v, want a final failure saying %q", tc.name, name, e, tc.want)
			}
			if got := RetryDelay(err, 2); got != 5*time.Minute {
				t.Errorf("%s, %s: RetryDelay = %v, want the plain backoff", tc.name, name, got)
			}
			// The TVDBError itself is still the 403 it was.
			var own *TVDBError
			if !errors.As(err, &own) || own.Status != 403 || own.Message != tc.msg {
				t.Errorf("%s, %s: errors.As to *TVDBError gave %+v", tc.name, name, own)
			}
		}
	}
	// Positive control: the same 403 with the refusal's message is a refusal.
	if !IsUnauthorized(&TVDBError{Status: 403, Message: tvdbRejected}) {
		t.Error("a 403 that says the key or PIN was rejected is unauthorized")
	}
	if IsTransient(ErrTVDBTruncated) || IsTransient(errors.New("x")) {
		t.Error("a list that is too long is not transient")
	}
	if got := (&TVDBError{Op: "GET /x", Status: 503, Message: "down"}).Error(); got != "tvdb GET /x: 503 Service Unavailable: down" {
		t.Errorf("Error() = %q", got)
	}
}

func TestTVDBBrokenResponsesAreTransient(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		call       func(c *TVDBClient) error
	}{
		{"truncated json", "", func(c *TVDBClient) error { _, err := c.SeriesExtended(t.Context(), 81189); return err }},
		{"empty body", "", func(c *TVDBClient) error { _, err := c.SeriesExtended(t.Context(), 81189); return err }},
		{"html", "<html>Bad gateway</html>", func(c *TVDBClient) error { _, err := c.SeriesExtended(t.Context(), 81189); return err }},
		{"no record", `{"status":"success","data":null}`, func(c *TVDBClient) error { _, err := c.SeriesExtended(t.Context(), 81189); return err }},
	} {
		f := newTVDBFake(t)
		if tc.name == "truncated json" {
			f.on("/series/81189/extended", tvdbExt, "malformed.json")
		} else {
			f.set("/series/81189/extended", tvdbExt, fakeRoute{status: 200, body: tc.body})
		}
		err := tc.call(testTVDBClient(t, f))
		if err == nil || !IsTransient(err) || IsNotFound(err) {
			t.Errorf("%s: err = %v, want a transient error", tc.name, err)
		}
	}

	f := newTVDBFake(t)
	f.set("/people/1/extended", "", fakeRoute{status: 200, body: `{"data":{}}`})
	c := testTVDBClient(t, f)
	if _, err := c.Person(t.Context(), 1); err == nil || !IsTransient(err) {
		t.Errorf("an empty person: err = %v", err)
	}

	// A login that answers 200 without a token, or with rubbish.
	for _, body := range []string{`{"status":"success","data":{}}`, `{"status":"success"}`, `not json`} {
		f := newTVDBFake(t)
		f.set("/login", "", fakeRoute{status: 200, body: body})
		err := testTVDBClient(t, f).Check(t.Context())
		if err == nil || !IsTransient(err) || !strings.Contains(err.Error(), "POST /login") {
			t.Errorf("login answering %q: err = %v", body, err)
		}
	}
	// And one that is down.
	f = newTVDBFake(t)
	f.fail("/login", "", 503, "", nil)
	if err := testTVDBClient(t, f).Check(t.Context()); !IsTransient(err) || IsUnauthorized(err) {
		t.Errorf("login with a 503: err = %v", err)
	}
}

// JSON that is valid but of a shape that does not fit says that TVDB's answers
// are not what they were: asking again will not help, so it is not transient.
func TestTVDBAnswersOfTheWrongShapeAreFinal(t *testing.T) {
	for _, tc := range []struct{ name, path, body string }{
		{"an array for a record", "/series/81189/extended", `[1,2,3]`},
		{"a list where a record is", "/series/81189/extended", `{"status":"success","data":[]}`},
		{"a string where the list is", "/search", `{"data":"oops"}`},
		{"an object for an id", "/people/1/extended", `{"data":{"id":{"a":1}}}`},
		{"a fractional id", "/people/1/extended", `{"data":{"id":1.5}}`},
		{"an id that is words", "/people/1/extended", `{"data":{"id":"one"}}`},
	} {
		f := newTVDBFake(t)
		query := tvdbExt
		switch tc.path {
		case "/search":
			query = "query=x&type=series"
		case "/people/1/extended":
			query = ""
		}
		f.set(tc.path, query, fakeRoute{status: 200, body: tc.body})
		c := testTVDBClient(t, f)
		var err error
		switch tc.path {
		case "/search":
			_, err = c.Search(t.Context(), "x", 0)
		case "/people/1/extended":
			_, err = c.Person(t.Context(), 1)
		default:
			_, err = c.SeriesExtended(t.Context(), 81189)
		}
		var e *TVDBError
		if !errors.As(err, &e) || IsTransient(err) || e.Message != "unexpected response shape" || e.Status != 200 {
			t.Errorf("%s: err = %#v, want a final \"unexpected response shape\"", tc.name, err)
		}
	}
}

func TestTVDBNetworkFailureIsTransient(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	c := NewTVDBClient(TVDBOptions{Key: tvdbTestKey, PIN: tvdbTestPIN, BaseURL: dead.URL + "/v4", Logger: discardLogger()})
	_, err := c.Search(t.Context(), "x", 0)
	var e *TVDBError
	if !errors.As(err, &e) || !e.Transient || e.Status != 0 {
		t.Fatalf("err = %#v, want a transient error without a status", err)
	}
	if IsNotFound(err) || IsUnauthorized(err) {
		t.Error("a network error was classified as a response")
	}
	if !strings.Contains(err.Error(), "POST /login") {
		t.Errorf("%q: the login is what failed", err)
	}
}

func TestTVDBTimeout(t *testing.T) {
	f := newTVDBFake(t)
	slow := func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(300 * time.Millisecond):
			w.Write(f.fixture("series_81189_extended.json"))
		case <-r.Context().Done():
		}
	}
	f.handle("/series/81189/extended", tvdbExt, slow)

	start := time.Now()
	_, err := testTVDBClient(t, f, func(o *TVDBOptions) { o.Timeout = 50 * time.Millisecond }).SeriesExtended(t.Context(), 81189)
	if err == nil || !IsTransient(err) || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("err = %v, want a transient timeout", err)
	}
	if took := time.Since(start); took > 250*time.Millisecond {
		t.Errorf("the timeout took %v", took)
	}
	// Positive control: with enough time the same server answers.
	if _, err := testTVDBClient(t, f, func(o *TVDBOptions) { o.Timeout = 5 * time.Second }).SeriesExtended(t.Context(), 81189); err != nil {
		t.Errorf("slow server with a long timeout: %v", err)
	}
}

func TestTVDBCancelledContextIsNotATVDBFailure(t *testing.T) {
	f := newTVDBFake(t)
	f.on("/series/81189/extended", tvdbExt, "series_81189_extended.json")
	c := testTVDBClient(t, f)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for name, call := range map[string]func() error{
		"SeriesExtended": func() error { _, err := c.SeriesExtended(ctx, 81189); return err },
		"Search":         func() error { _, err := c.Search(ctx, "x", 0); return err },
		"Episodes":       func() error { _, err := c.Episodes(ctx, 81189, "official", "eng"); return err },
		"Updates":        func() error { _, err := c.Updates(ctx, time.Now()); return err },
		"Check":          func() error { return c.Check(ctx) },
	} {
		if err := call(); !errors.Is(err, context.Canceled) || IsTransient(err) {
			t.Errorf("%s: err = %v; want context.Canceled and not transient", name, err)
		}
	}
	if n := len(f.requests()); n != 0 {
		t.Errorf("a cancelled call reached the server (%d requests, the login among them)", n)
	}

	// Cancelled while the request is in flight.
	f.handle("/people/1/extended", "", func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel = context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)
	if _, err := c.Person(ctx, 1); !errors.Is(err, context.Canceled) || IsTransient(err) {
		t.Errorf("in flight: err = %v", err)
	}

	// Cancelled while the login is in flight.
	f2 := newTVDBFake(t)
	// The body is read first: net/http only notices a client that has gone while
	// a handler is running once the request body has been consumed.
	f2.handle("/login", "", func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	})
	ctx, cancel = context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)
	if _, err := testTVDBClient(t, f2).Search(ctx, "x", 0); !errors.Is(err, context.Canceled) || IsTransient(err) {
		t.Errorf("during the login: err = %v", err)
	}
}

// A call that waits for another call's login can be cancelled while it waits,
// and the login it waited for is not lost.
func TestTVDBCancelledWhileWaitingForAnotherLogin(t *testing.T) {
	f := newTVDBFake(t)
	f.on("/series/81189/extended", tvdbExt, "series_81189_extended.json")
	gate, entered := make(chan struct{}), make(chan struct{}, 1)
	f.handle("/login", "", func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		select {
		case <-gate:
			f.login(w, r)
		case <-r.Context().Done():
		}
	})
	c := testTVDBClient(t, f)
	first := make(chan error, 1)
	go func() { _, err := c.SeriesExtended(t.Context(), 81189); first <- err }()
	<-entered

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.SeriesExtended(ctx, 81189); !errors.Is(err, context.DeadlineExceeded) || IsTransient(err) {
		t.Errorf("waiting call: err = %v, want its own deadline", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("the waiting call sat there for %v", took)
	}
	close(gate)
	if err := <-first; err != nil {
		t.Errorf("the call that was logging in: %v", err)
	}
	if f.logins() != 1 {
		t.Errorf("%d logins, want 1", f.logins())
	}
}

func TestTVDBHonoursRetryAfter(t *testing.T) {
	ctx := t.Context()
	respond429 := func(retryAfter string, until int32, hits *atomic.Int32, f *tvdbFake) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if hits.Add(1) <= until {
				if retryAfter != "" {
					w.Header().Set("Retry-After", retryAfter)
				}
				w.WriteHeader(http.StatusTooManyRequests)
				w.Write(f.fixture("error_429.json"))
				return
			}
			w.Write(f.fixture("series_81189_extended.json"))
		}
	}

	t.Run("waits the seconds asked and retries", func(t *testing.T) {
		f, clk, hits := newTVDBFake(t), newFakeClock(), new(atomic.Int32)
		f.handle("/series/81189/extended", tvdbExt, respond429("3", 1, hits, f))
		c := testTVDBClient(t, f)
		useTVDBClock(c, clk, 1000, 1000)
		if _, err := c.SeriesExtended(ctx, 81189); err != nil {
			t.Fatalf("err = %v, want success after the retry", err)
		}
		if hits.Load() != 2 || !slices.Equal(clk.slept(), []time.Duration{3 * time.Second}) {
			t.Errorf("hits %d, slept %v; want 2 requests and one 3s wait", hits.Load(), clk.slept())
		}
	})

	t.Run("an http date", func(t *testing.T) {
		f, clk, hits := newTVDBFake(t), newFakeClock(), new(atomic.Int32)
		f.handle("/series/81189/extended", tvdbExt, respond429(clk.now().Add(7*time.Second).UTC().Format(http.TimeFormat), 1, hits, f))
		c := testTVDBClient(t, f)
		useTVDBClock(c, clk, 1000, 1000)
		if _, err := c.SeriesExtended(ctx, 81189); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(clk.slept(), []time.Duration{7 * time.Second}) {
			t.Errorf("slept %v, want 7s", clk.slept())
		}
	})

	t.Run("no header means two seconds", func(t *testing.T) {
		f, clk, hits := newTVDBFake(t), newFakeClock(), new(atomic.Int32)
		f.handle("/series/81189/extended", tvdbExt, respond429("", 1, hits, f))
		c := testTVDBClient(t, f)
		useTVDBClock(c, clk, 1000, 1000)
		if _, err := c.SeriesExtended(ctx, 81189); err != nil || !slices.Equal(clk.slept(), []time.Duration{2 * time.Second}) {
			t.Errorf("err %v, slept %v; want 2s", err, clk.slept())
		}
	})

	t.Run("the login is paced by it too", func(t *testing.T) {
		f, clk := newTVDBFake(t), newFakeClock()
		f.on("/series/81189/extended", tvdbExt, "series_81189_extended.json")
		var hits atomic.Int32
		f.handle("/login", "", func(w http.ResponseWriter, r *http.Request) {
			if hits.Add(1) == 1 {
				w.Header().Set("Retry-After", "4")
				w.WriteHeader(http.StatusTooManyRequests)
				w.Write(f.fixture("error_429.json"))
				return
			}
			f.login(w, r)
		})
		c := testTVDBClient(t, f)
		useTVDBClock(c, clk, 1000, 1000)
		if _, err := c.SeriesExtended(ctx, 81189); err != nil {
			t.Fatal(err)
		}
		if hits.Load() != 2 || !slices.Equal(clk.slept(), []time.Duration{4 * time.Second}) {
			t.Errorf("login hits %d, slept %v; want 2 and one 4s wait", hits.Load(), clk.slept())
		}
	})

	t.Run("gives up after a few tries with the wait for the caller", func(t *testing.T) {
		f, clk, hits := newTVDBFake(t), newFakeClock(), new(atomic.Int32)
		f.handle("/series/81189/extended", tvdbExt, respond429("1", 100, hits, f))
		c := testTVDBClient(t, f)
		useTVDBClock(c, clk, 1000, 1000)
		_, err := c.SeriesExtended(ctx, 81189)
		var e *TVDBError
		if !errors.As(err, &e) || e.Status != 429 || !e.Transient || e.RetryAfter != time.Second {
			t.Fatalf("err = %#v", err)
		}
		if hits.Load() != 1+maxRateRetries || len(clk.slept()) != maxRateRetries {
			t.Errorf("hits %d, sleeps %v; want %d tries and %d waits", hits.Load(), clk.slept(), 1+maxRateRetries, maxRateRetries)
		}
		if f.logins() != 1 {
			t.Errorf("%d logins; a 429 is not a reason to log in again", f.logins())
		}
	})

	t.Run("a long wait is not sat out; calls during it fail fast, then go through", func(t *testing.T) {
		f, clk, hits := newTVDBFake(t), newFakeClock(), new(atomic.Int32)
		f.handle("/series/81189/extended", tvdbExt, respond429("120", 1, hits, f))
		c := testTVDBClient(t, f)
		useTVDBClock(c, clk, 1000, 1000)
		_, err := c.SeriesExtended(ctx, 81189)
		var e *TVDBError
		if !errors.As(err, &e) || e.RetryAfter != 120*time.Second || hits.Load() != 1 || len(clk.slept()) != 0 {
			t.Fatalf("err %#v, hits %d, slept %v; want one request, no waiting, RetryAfter 120s", err, hits.Load(), clk.slept())
		}
		if got := RetryDelay(err, 1); got != 120*time.Second {
			t.Errorf("RetryDelay = %v, want the 120s TVDB asked for", got)
		}
		// Still inside the pause: nothing is sent.
		clk.advance(10 * time.Second)
		_, err = c.SeriesExtended(ctx, 81189)
		if !errors.As(err, &e) || !e.Transient || e.RetryAfter != 110*time.Second || hits.Load() != 1 {
			t.Errorf("during the pause: err %#v, hits %d; want a fast transient failure with 110s left", err, hits.Load())
		}
		// Positive control: once it is over, requests flow again.
		clk.advance(111 * time.Second)
		if _, err := c.SeriesExtended(ctx, 81189); err != nil || hits.Load() != 2 {
			t.Errorf("after the pause: err %v, hits %d", err, hits.Load())
		}
	})

	t.Run("a pause holds back every path", func(t *testing.T) {
		f, clk := newTVDBFake(t), newFakeClock()
		var hits atomic.Int32
		f.handle("/series/81189/extended", tvdbExt, respond429("5", 1, &hits, f))
		f.on("/people/296140/extended", "", "person_296140.json")
		c := testTVDBClient(t, f)
		useTVDBClock(c, clk, 1000, 1000)
		if _, err := c.SeriesExtended(ctx, 81189); err != nil { // 429, waits 5s, retries
			t.Fatal(err)
		}
		start := clk.now()
		c.pause(4 * time.Second) // as a 429 on some other call would
		if _, err := c.Person(ctx, 296140); err != nil {
			t.Fatal(err)
		}
		if got := clk.now().Sub(start); got != 4*time.Second {
			t.Errorf("a call during a pause waited %v, want 4s", got)
		}
	})

	t.Run("an absurd wait is cut to an hour", func(t *testing.T) {
		// Numbers that overflow a Duration when they are made seconds, and the end
		// of time as a date: each used to give a negative or endless wait.
		for _, v := range []string{"3601", "86400", "9223372036", "9223372037", "99999999999", "9223372036854775807",
			"99999999999999999999999", "Fri, 31 Dec 9999 23:59:59 GMT"} {
			f, clk := newTVDBFake(t), newFakeClock()
			var hits atomic.Int32
			f.handle("/series/81189/extended", tvdbExt, respond429(v, 1000, &hits, f))
			f.on("/people/296140/extended", "", "person_296140.json")
			c := testTVDBClient(t, f)
			useTVDBClock(c, clk, 1000, 1000)
			_, err := c.SeriesExtended(ctx, 81189)
			var e *TVDBError
			if !errors.As(err, &e) || e.RetryAfter != time.Hour || !e.Transient || hits.Load() != 1 || len(clk.slept()) != 0 {
				t.Errorf("Retry-After %q: err %#v, hits %d, slept %v; want one request, no waiting, RetryAfter 1h", v, err, hits.Load(), clk.slept())
				continue
			}
			if got := RetryDelay(err, 1); got != time.Hour {
				t.Errorf("Retry-After %q: RetryDelay = %v, want 1h", v, got)
			}
			// The pause is an hour, not forever: a call made well inside it fails at once
			// (the last MaxWait of it is sat out in place), and one made after it goes through.
			clk.advance(time.Hour - 2*DefaultMaxWait)
			if _, err := c.Person(ctx, 296140); !IsTransient(err) || f.count("/people/296140/extended") != 0 {
				t.Errorf("Retry-After %q: inside the hour: %v", v, err)
			}
			clk.advance(2*DefaultMaxWait + time.Second)
			if _, err := c.Person(ctx, 296140); err != nil {
				t.Errorf("Retry-After %q: after the hour: %v", v, err)
			}
		}
	})
}

// A call that is queued for a slot must not sleep through a pause that another
// call's 429 set while it waited. The pause must be recorded before the slot is
// given back; that is only a few instructions apart, so once the 429 is on its
// way the clock is made slow, which stretches recording the pause (it reads the
// time) while the queued call, woken by the slot, looks for one.
func TestTVDBQueuedCallDoesNotSitOutALongPauseSetMeanwhile(t *testing.T) {
	f, clk := newTVDBFake(t), newFakeClock()
	gate := make(chan struct{})
	var hits atomic.Int32
	f.handle("/series/81189/extended", tvdbExt, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-gate
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write(f.fixture("error_429.json"))
	})
	c := testTVDBClient(t, f, func(o *TVDBOptions) { o.MaxInFlight = 1 })
	useTVDBClock(c, clk, 1000, 1000)
	if err := c.Check(t.Context()); err != nil { // the login is not what is being tested
		t.Fatal(err)
	}
	var slow atomic.Bool
	c.now = func() time.Time {
		if slow.Load() {
			time.Sleep(30 * time.Millisecond)
		}
		return clk.now()
	}

	first := make(chan error, 1)
	go func() { _, err := c.SeriesExtended(t.Context(), 81189); first <- err }()
	for hits.Load() == 0 {
		time.Sleep(2 * time.Millisecond)
	}
	second := make(chan error, 1)
	go func() { _, err := c.SeriesExtended(t.Context(), 81189); second <- err }() // queued: the only slot is taken
	time.Sleep(100 * time.Millisecond)
	slow.Store(true)
	close(gate) // the first gets its 429 and sets the pause

	for i, ch := range []chan error{first, second} {
		select {
		case err := <-ch:
			var e *TVDBError
			if !errors.As(err, &e) || e.Status != 429 || !e.Transient || e.RetryAfter < 3500*time.Second || !strings.Contains(e.Op, "GET /series/81189/extended") {
				t.Errorf("call %d: err = %#v", i+1, err)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("call %d sat out the pause", i+1)
		}
	}
	if s := clk.slept(); len(s) != 0 {
		t.Errorf("slept %v; a pause longer than MaxWait is never sat out", s)
	}
	if hits.Load() != 1 {
		t.Errorf("%d requests reached TVDB, want 1", hits.Load())
	}
}

// Calls cancelled while they wait out a pause give their slot back: after as
// many cancellations as there are slots the client still works.
func TestTVDBCancelledCallsDuringAPauseReleaseTheirSlot(t *testing.T) {
	f := newTVDBFake(t)
	f.on("/series/81189/extended", tvdbExt, "series_81189_extended.json")
	c := testTVDBClient(t, f, func(o *TVDBOptions) { o.MaxInFlight = 1; o.MaxWait = 20 * time.Second })
	c.pause(10 * time.Second) // within MaxWait: calls sleep it out
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
		_, err := c.SeriesExtended(ctx, 81189)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("call %d: %v, want its own deadline", i+1, err)
		}
	}
	c.mu.Lock()
	c.pauseUntil = time.Time{}
	c.mu.Unlock()
	done := make(chan error, 1)
	go func() { _, err := c.SeriesExtended(t.Context(), 81189); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("after the cancellations: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the slots leaked: the client is stuck after cancelled calls")
	}
}

func TestTVDBPacesRequests(t *testing.T) {
	f, clk := newTVDBFake(t), newFakeClock()
	f.on("/people/296140/extended", "", "person_296140.json")
	c := testTVDBClient(t, f, func(o *TVDBOptions) { o.Rate = 10 })
	useTVDBClock(c, clk, 10, 10)
	if err := c.Check(t.Context()); err != nil { // takes the first of the burst
		t.Fatal(err)
	}
	start := clk.now()
	for i := 0; i < 20; i++ {
		if _, err := c.Person(t.Context(), 296140); err != nil {
			t.Fatal(err)
		}
	}
	// 9 left in the burst, then 11 more at 10 a second.
	if got, want := clk.now().Sub(start), 11*100*time.Millisecond; got != want {
		t.Errorf("20 requests took %v of waiting, want %v", got, want)
	}
}

func TestTVDBLimitsRequestsInFlight(t *testing.T) {
	f := newTVDBFake(t)
	var cur, high atomic.Int32
	gate := make(chan struct{})
	f.handle("/people/296140/extended", "", func(w http.ResponseWriter, r *http.Request) {
		n := cur.Add(1)
		for {
			h := high.Load()
			if n <= h || high.CompareAndSwap(h, n) {
				break
			}
		}
		select {
		case <-gate:
		case <-r.Context().Done():
		}
		cur.Add(-1)
		w.Write(f.fixture("person_296140.json"))
	})
	c := testTVDBClient(t, f, func(o *TVDBOptions) { o.MaxInFlight = 2 })
	if err := c.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Person(t.Context(), 296140); err != nil {
				t.Error(err)
			}
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for cur.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(60 * time.Millisecond) // time for a third to sneak in, if it could
	if got := cur.Load(); got != 2 {
		t.Errorf("%d requests in flight at the peak, want 2", got)
	}
	close(gate)
	wg.Wait()
	if high.Load() != 2 || f.count("/people/296140/extended") != 8 {
		t.Errorf("high-water mark %d, %d requests served; want 2 and 8", high.Load(), f.count("/people/296140/extended"))
	}
}

func TestTVDBTypedResponses(t *testing.T) {
	ctx := t.Context()
	f := newTVDBFake(t)
	f.on("/search", params("query", "Breaking Bad", "type", "series", "year", "2008"), "search_breaking_bad.json")
	f.on("/search", params("query", "Breaking Bad", "type", "series"), "search_breaking_bad.json")
	f.on("/search/remoteid/tt0903747", "", "remoteid_tt0903747.json")
	f.on("/search/remoteid/tt9999999", "", "remoteid_movie.json")
	f.on("/series/81189/extended", tvdbExt, "series_81189_extended.json")
	f.on("/series/81189/translations/por", "", "translation_81189_por.json")
	f.fail("/series/81189/translations/pt", "", 404, "error_404.json", nil)
	f.fail("/series/81189/translations/jpn", "", 404, "error_404.json", nil)
	f.on("/people/296140/extended", "", "person_296140.json")
	c := testTVDBClient(t, f)

	// The first two are TVDB's own answers, cut down. The second has no year, no
	// first air time and no overviews: the fields it lacks come out empty.
	found, err := c.Search(ctx, "Breaking Bad", 2008)
	if err != nil || len(found) != 2 {
		t.Fatalf("Search = %d results, %v", len(found), err)
	}
	if r := found[0]; r.TVDBID != "81189" || r.SeriesID() != 81189 || r.Name != "Breaking Bad" || r.Year != "2008" || r.Slug != "breaking-bad" ||
		r.FirstAirTime != "2008-01-20" || r.ImageURL != "https://artworks.thetvdb.com/banners/posters/81189-10.jpg" || !strings.HasSuffix(r.Thumbnail, "81189-10_t.jpg") ||
		r.PrimaryLanguage != "eng" || r.Translations["por"] != "Breaking Bad: Ruptura Total" || r.Translations["pt"] != "Breaking Bad" ||
		r.Overviews["por"] == "" || r.Overview == "" || r.Status != "Ended" || r.Network != "AMC" ||
		r.RemoteIDs.IMDbID() != "tt0903747" || r.RemoteIDs.TMDBID() != 1396 || len(r.RemoteIDs) != 4 {
		t.Errorf("first result = %+v", r)
	}
	if r := found[1]; r.SeriesID() != 357407 || r.Name != "Baking Bad" || r.Year != "" || r.FirstAirTime != "" || r.Overview != "" || len(r.Overviews) != 0 ||
		len(r.Translations) != 0 || r.PrimaryLanguage != "deu" || r.Network != "Twitch" || r.RemoteIDs.IMDbID() != "tt3808074" || r.RemoteIDs.TMDBID() != 0 ||
		r.ImageURL != "https://artworks.thetvdb.com/banners/images/missing/series.jpg" {
		t.Errorf("second result = %+v", r)
	}
	if _, err := c.Search(ctx, "Breaking Bad", 0); err != nil {
		t.Fatal(err)
	}
	for _, r := range f.requests() {
		if r.Path != "/search" {
			continue
		}
		// The language is not sent: TVDB takes it as a filter on the show's own language.
		if r.Query.Has("language") || r.Query.Get("type") != "series" || r.Query.Get("query") != "Breaking Bad" {
			t.Errorf("search request: %v", r.Query)
		}
	}
	if last := f.requests()[len(f.requests())-1]; last.Query.Has("year") {
		t.Errorf("year 0 still sent year: %v", last.Query)
	}
	if none, err := c.Search(ctx, "Nothing At All", 1900); err != nil || len(none) != 0 {
		t.Errorf("an empty search = %v, %v", none, err)
	}

	s, err := c.SeriesExtended(ctx, 81189)
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != 81189 || s.Name != "Breaking Bad" || s.Slug != "breaking-bad" || s.Year != "2008" || s.FirstAired != "2008-01-20" || s.LastAired != "2013-09-29" ||
		s.NextAired != "" || s.Status.Name != "Ended" || s.OriginalLanguage != "eng" || s.OriginalCountry != "usa" || s.AverageRuntime != 48 ||
		s.Score != 3776757 || s.DefaultSeasonType != 1 || s.OriginalNetwork.Name != "AMC" || s.OriginalNetwork.ID != 389 ||
		len(s.Genres) != 4 || s.Genres[1].Name != "Crime" || s.Genres[1].ID != 14 || len(s.Artworks) != 4 || len(s.Characters) != 3 ||
		len(s.Seasons) != 4 || len(s.ContentRatings) != 2 || s.ContentRatings[1].Name != "TV-MA" || s.ContentRatings[1].Country != "usa" {
		t.Errorf("SeriesExtended = %+v", s)
	}
	if a := s.Artworks[0]; a.ID != 913598 || a.Type != 2 || a.Language != "eng" || a.Score != 100142 || !strings.HasSuffix(a.Thumbnail, "81189-10_t.jpg") || a.Image != "https://artworks.thetvdb.com/banners/posters/81189-10.jpg" {
		t.Errorf("first artwork = %+v", a)
	}
	if a := s.Artworks[1]; a.Language != "" || a.Type != 3 {
		t.Errorf("a textless artwork = %+v", a)
	}
	if ch := s.Characters[0]; ch.PersonName != "Anna Gunn" || ch.Name != "Skyler White" || ch.PeopleType != "Actor" || ch.Type != 3 || ch.PeopleID != 269404 || ch.Sort != 2 ||
		!ch.IsFeatured || !strings.HasPrefix(ch.PersonImgURL, "https://artworks.thetvdb.com/banners/") || !strings.HasPrefix(ch.Image, "https://artworks.thetvdb.com/banners/") {
		t.Errorf("characters = %+v", s.Characters)
	}
	// A series has the seasons of every order, told apart by their type.
	if got := s.Seasons[0]; got.Number != 0 || got.Type.Type != "official" || s.Seasons[2].Number != 1 || s.Seasons[2].Type.Type != "dvd" || s.Seasons[3].Type.Type != "absolute" || s.Seasons[3].Image != "" {
		t.Errorf("seasons = %+v", s.Seasons)
	}
	if s.RemoteIDs.IMDbID() != "tt0903747" || s.RemoteIDs.TMDBID() != 1396 || s.RemoteIDs.Get("official website") == "" || s.RemoteIDs.Get("TMS (Zap2It)") != "EP01009396" || s.RemoteIDs.Get("Nope") != "" {
		t.Errorf("remote ids = %+v", s.RemoteIDs)
	}
	// por is the Portuguese of Portugal and pt that of Brazil, in TVDB.
	if s.Translations.Name("pt-PT") != "Breaking Bad: Ruptura Total" || s.Translations.Name("por") != "Breaking Bad: Ruptura Total" || s.Translations.Name("pt-BR") != "Breaking Bad" ||
		s.Translations.Name("cs") != "Perníkový táta" || s.Translations.Name("ja") != "" || s.Translations.Name("") != "" ||
		!strings.HasPrefix(s.Translations.Overview("pt-PT"), "Um professor do secundário") || !strings.HasPrefix(s.Translations.Overview("pt-BR"), "O drama") ||
		s.Translations.Overview("ja") != "" {
		t.Errorf("translations: pt-PT %q, por %q, pt-BR %q, cs %q, ja %q; overviews pt-PT %.20q, pt-BR %.20q, ja %.20q", s.Translations.Name("pt-PT"), s.Translations.Name("por"),
			s.Translations.Name("pt-BR"), s.Translations.Name("cs"), s.Translations.Name("ja"), s.Translations.Overview("pt-PT"), s.Translations.Overview("pt-BR"), s.Translations.Overview("ja"))
	}

	tr, err := c.Translation(ctx, 81189, "pt-PT")
	if err != nil || tr.Name != "Breaking Bad: Ruptura Total" || tr.Language != "por" || !strings.Contains(tr.Overview, "professor") {
		t.Errorf("Translation = %+v, %v", tr, err)
	}
	// pt-BR is asked for as TVDB's "pt", not "por".
	if _, err := c.Translation(ctx, 81189, "pt-BR"); !IsNotFound(err) || f.count("/series/81189/translations/pt") != 1 {
		t.Errorf("pt-BR: err = %v, %d requests for /translations/pt", err, f.count("/series/81189/translations/pt"))
	}
	if _, err := c.Translation(ctx, 81189, "ja-JP"); !IsNotFound(err) || IsTransient(err) {
		t.Errorf("a language TVDB has nothing in: err = %v, want a not-found", err)
	}

	p, err := c.Person(ctx, 296140)
	if err != nil || p.ID != 296140 || p.Name != "Bryan Cranston" || p.Birth != "1956-03-07" || p.Death != "" || p.BirthPlace != "Hollywood, California, USA" ||
		!strings.HasSuffix(p.Image, "bryan.jpg") || p.RemoteIDs.IMDbID() != "nm0186505" || p.RemoteIDs.TMDBID() != 17419 || len(p.Biographies) != 2 {
		t.Errorf("Person = %+v, %v", p, err)
	}
	// The fixture's Portuguese is TVDB's "por", the Portuguese of Portugal; Brazil's is "pt".
	if p.Biography("pt-PT") != "Bryan Lee Cranston e um ator americano." || p.Biography("pt-BR") != "" || p.Biography("en-US") == "" || p.Biography("de") != "" {
		t.Errorf("biographies: pt-PT %q pt-BR %q en %q de %q", p.Biography("pt-PT"), p.Biography("pt-BR"), p.Biography("en-US"), p.Biography("de"))
	}

	m, err := c.SearchRemoteID(ctx, "tt0903747")
	if err != nil || len(m) != 1 || m[0].Series == nil || m[0].Series.ID != 81189 || m[0].Series.Name != "Breaking Bad" || m[0].Series.Status.Name != "Ended" ||
		m[0].Movie != nil || m[0].People != nil {
		t.Errorf("SearchRemoteID(series) = %+v, %v", m, err)
	}
	m, err = c.SearchRemoteID(ctx, " tt9999999 ")
	if err != nil || len(m) != 1 || m[0].Series != nil || m[0].Movie == nil || m[0].Movie.ID != 9001 || m[0].Movie.Year != "2019" {
		t.Errorf("SearchRemoteID(movie) = %+v, %v", m, err)
	}
	before := len(f.requests())
	if _, err := c.SearchRemoteID(ctx, "  "); err == nil || len(f.requests()) != before {
		t.Errorf("an empty remote id: err = %v, %d requests", err, len(f.requests())-before)
	}

	if got := f.logins(); got != 1 {
		t.Errorf("%d logins for all of that, want 1", got)
	}
}

func TestTVDBRemoteIDsOfOtherShapes(t *testing.T) {
	for _, tc := range []struct {
		in   TVDBRemoteIDs
		tmdb int64
		imdb string
	}{
		{nil, 0, ""},
		{TVDBRemoteIDs{{ID: "1396", SourceName: "TheMovieDB.com"}}, 1396, ""},
		{TVDBRemoteIDs{{ID: "tv/1396", SourceName: "TheMovieDB.com"}}, 1396, ""},
		{TVDBRemoteIDs{{ID: "movie/603", SourceName: "THEMOVIEDB.COM"}}, 603, ""},
		{TVDBRemoteIDs{{ID: "abc", SourceName: "TheMovieDB.com"}}, 0, ""},
		{TVDBRemoteIDs{{ID: "-5", SourceName: "TheMovieDB.com"}}, 0, ""},
		{TVDBRemoteIDs{{ID: "", SourceName: "TheMovieDB.com"}}, 0, ""},
		{TVDBRemoteIDs{{ID: "tt1", SourceName: "IMDB"}, {ID: "5", SourceName: "TheMovieDB.com"}}, 5, "tt1"},
	} {
		if tc.in.TMDBID() != tc.tmdb || tc.in.IMDbID() != tc.imdb {
			t.Errorf("%v: TMDBID %d IMDbID %q, want %d %q", tc.in, tc.in.TMDBID(), tc.in.IMDbID(), tc.tmdb, tc.imdb)
		}
	}
	for in, want := range map[string]int64{"81189": 81189, " 7 ": 7, "": 0, "series-81189": 0, "0": 0, "-1": 0} {
		if got := (TVDBSearchResult{TVDBID: in}).SeriesID(); got != want {
			t.Errorf("SeriesID of %q = %d, want %d", in, got, want)
		}
	}
}

func TestTVDBRequestPaths(t *testing.T) {
	ctx := t.Context()
	f := newTVDBFake(t)
	var uris []string
	var mu sync.Mutex
	record := func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		uris = append(uris, r.RequestURI)
		mu.Unlock()
		w.Write([]byte(`{"status":"success","data":[]}`))
	}
	f.handle("/search/remoteid/a/b c", "", record)
	c := testTVDBClient(t, f)
	// An id from outside cannot reach beyond its segment of the path.
	if _, err := c.SearchRemoteID(ctx, "a/b c"); err != nil {
		t.Fatal(err)
	}
	if len(uris) != 1 || uris[0] != "/v4/search/remoteid/a%2Fb%20c" {
		t.Errorf("request URIs %q; the id must be escaped into one path segment", uris)
	}

	// The season type is checked against what TVDB knows, the language mapped.
	before := len(f.requests())
	for _, bad := range []string{"", "aired", "official/../../login", "OFFICIAL?x=1", "dvd,official"} {
		_, err := c.Episodes(ctx, 81189, bad, "eng")
		if err == nil || !strings.Contains(err.Error(), "unknown season type") || IsTransient(err) {
			t.Errorf("season type %q: err = %v", bad, err)
		}
	}
	if n := len(f.requests()) - before; n != 0 {
		t.Errorf("%d requests for season types that are not TVDB's", n)
	}
	for _, st := range tvdbSeasonTypes {
		f.onTotal("/series/5/episodes/"+st+"/eng", params("page", "0"), "episodes_81189_official_eng.json", 3)
		if _, err := c.Episodes(ctx, 5, st, "eng"); err != nil {
			t.Errorf("season type %q: %v", st, err)
		}
	}
	// TVDB's "pt" is the Portuguese of Brazil and its "por" that of Portugal.
	for lang, code := range map[string]string{"pt-BR": "pt", "pt": "pt", "pt-PT": "por", "por": "por", "": "eng", "xx-YY": "eng", "ja": "jpn", "zh-TW": "zhtw", "FRA": "fra"} {
		f.onTotal("/series/6/episodes/official/"+code, params("page", "0"), "episodes_81189_official_eng.json", 3)
		if _, err := c.Episodes(ctx, 6, "Official", lang); err != nil {
			t.Errorf("language %q: %v", lang, err)
		}
	}
	pt := testTVDBClient(t, f, func(o *TVDBOptions) { o.Language = "pt-BR" })
	f.onTotal("/series/7/episodes/dvd/pt", params("page", "0"), "episodes_81189_official_eng.json", 3)
	f.on("/series/7/translations/pt", "", "translation_81189_por.json")
	if _, err := pt.Episodes(ctx, 7, "dvd", ""); err != nil {
		t.Errorf("the client's language for an empty one: %v", err)
	}
	if _, err := pt.Translation(ctx, 7, ""); err != nil || f.count("/series/7/translations/pt") != 1 {
		t.Errorf("Translation in the client's language: %v, %d requests", err, f.count("/series/7/translations/pt"))
	}
}

// The Simpsons in the default order, as TVDB answered the route with no language
// (500 episodes on page 0, 390 on page 1, and no next on the last), cut down to
// three episodes a page. The total TVDB counts, 890, is patched to the six that
// are left; TestTVDBEpisodesMustAddUpToTheirCount serves it as it is.
func TestTVDBEpisodesFollowEveryPage(t *testing.T) {
	ctx := t.Context()
	f := newTVDBFake(t)
	const path = "/series/71663/episodes/default/eng"
	f.onTotal(path, params("page", "0"), "episodes_71663_default_p0.json", 6)
	f.onTotal(path, params("page", "1"), "episodes_71663_default_p1.json", 6)
	c := testTVDBClient(t, f)

	eps, err := c.Episodes(ctx, 71663, "default", "en-US")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := f.asked(), []string{"/login", path + "?page=0", path + "?page=1"}; !slices.Equal(got, want) {
		t.Errorf("requests:\n got %q\nwant %q", got, want)
	}
	var ids []int64
	for _, e := range eps.Episodes {
		ids = append(ids, e.ID)
	}
	if !slices.Equal(ids, []int64{4350173, 4350174, 4350175, 406855, 413426, 420649}) {
		t.Fatalf("episodes %v, want the six of both pages in order", ids)
	}
	if e := eps.Episodes[0]; e.SeriesID != 71663 || e.SeasonNumber != 0 || e.Number != 1 || e.AbsoluteNumber != 0 || e.Name != "Good Night" || e.Aired != "1987-04-19" ||
		e.Runtime != 2 || e.Image != "https://artworks.thetvdb.com/banners/episodes/71663/4350173.jpg" || !strings.HasPrefix(e.Overview, "Good Night was the first") {
		t.Errorf("first episode = %+v", e)
	}
	if e := eps.Episodes[3]; e.SeasonNumber != 20 || e.Number != 7 || e.AbsoluteNumber != 427 || e.Name != "Mypods and Boomsticks" || e.Runtime != 25 {
		t.Errorf("first episode of page 2 = %+v", e)
	}
	// The series is the one of the first page.
	if s := eps.Series; s.ID != 71663 || s.Name != "The Simpsons" || s.Slug != "the-simpsons" || s.Status.Name != "Continuing" || s.AverageRuntime != 24 || s.FirstAired != "1989-12-17" || s.Year != "1989" {
		t.Errorf("series = %+v", s)
	}

	// A page that fails is an error, not a short list.
	broken := newTVDBFake(t)
	broken.onTotal(path, params("page", "0"), "episodes_71663_default_p0.json", 6)
	broken.fail(path, params("page", "1"), 503, "", nil)
	if got, err := testTVDBClient(t, broken).Episodes(ctx, 71663, "default", "eng"); err == nil || got != nil || !IsTransient(err) {
		t.Errorf("episodes with a failing page 2 = %v, %v", got, err)
	}
}

// TVDB answers the route with a language with the series record as "data" and
// the episodes inside it, and the route without one with data = {series,
// episodes}. Breaking Bad's first three specials, as each of them answered.
func TestTVDBEpisodesReadBothShapesOfAnswer(t *testing.T) {
	for _, tc := range []struct{ name, file string }{
		{"the route with a language: data is the series", "episodes_81189_official_eng.json"},
		{"the route without one: data = {series, episodes}", "episodes_81189_official.json"},
	} {
		f := newTVDBFake(t)
		const path = "/series/81189/episodes/official/eng"
		f.onTotal(path, params("page", "0"), tc.file, 3)
		eps, err := testTVDBClient(t, f).Episodes(t.Context(), 81189, "official", "eng")
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if s := eps.Series; s.ID != 81189 || s.Name != "Breaking Bad" || s.Slug != "breaking-bad" || s.Status.Name != "Ended" || s.AverageRuntime != 48 ||
			s.FirstAired != "2008-01-20" || s.LastAired != "2013-09-29" || s.OriginalLanguage != "eng" || s.Year != "2008" {
			t.Errorf("%s: series = %+v", tc.name, s)
		}
		if len(eps.Episodes) != 3 {
			t.Errorf("%s: %d episodes, want 3", tc.name, len(eps.Episodes))
			continue
		}
		if e := eps.Episodes[0]; e.ID != 3859781 || e.SeriesID != 81189 || e.SeasonNumber != 0 || e.Number != 1 || e.Name != "Good Cop / Bad Cop" || e.Aired != "2009-02-17" ||
			e.Runtime != 3 || !strings.HasSuffix(e.Image, "/episodes/81189/3859781.jpg") || e.Overview == "" {
			t.Errorf("%s: first episode = %+v", tc.name, e)
		}
		if e := eps.Episodes[2]; e.ID != 9700129 || e.Number != 20 || e.Name != "Live Saul Cam" || e.Image != "" || e.Runtime != 5 {
			t.Errorf("%s: an episode without an image = %+v", tc.name, e)
		}
	}
}

// links is absent from an answer that has none to give: there is then nothing
// to check the episodes against, and nothing to follow.
func TestTVDBEpisodesWithoutLinks(t *testing.T) {
	f := newTVDBFake(t)
	f.on("/series/81189/episodes/official/eng", params("page", "0"), "episodes_81189_official_eng_nolinks.json")
	eps, err := testTVDBClient(t, f).Episodes(t.Context(), 81189, "official", "eng")
	if err != nil || len(eps.Episodes) != 3 || eps.Series.Name != "Breaking Bad" || f.count("/series/81189/episodes/official/eng") != 1 {
		t.Errorf("episodes with no links = %v, %v", shown(eps), err)
	}
}

// TVDB counts the entries of a list in links.total_items. Episodes that do not
// add up to it are not handed over as if they were all.
func TestTVDBEpisodesMustAddUpToTheirCount(t *testing.T) {
	ctx := t.Context()

	// The cut-down fixtures with TVDB's own totals: 3 episodes of 80, 6 of 890.
	short := newTVDBFake(t)
	short.on("/series/81189/episodes/official/eng", params("page", "0"), "episodes_81189_official_eng.json")
	if eps, err := testTVDBClient(t, short).Episodes(ctx, 81189, "official", "eng"); !errors.Is(err, ErrTVDBTruncated) || eps != nil {
		t.Errorf("3 episodes of 80: %v, %v; want ErrTVDBTruncated", shown(eps), err)
	}
	paged := newTVDBFake(t)
	paged.on("/series/71663/episodes/default/eng", params("page", "0"), "episodes_71663_default_p0.json")
	paged.on("/series/71663/episodes/default/eng", params("page", "1"), "episodes_71663_default_p1.json")
	if eps, err := testTVDBClient(t, paged).Episodes(ctx, 71663, "default", "eng"); !errors.Is(err, ErrTVDBTruncated) || eps != nil {
		t.Errorf("6 episodes of 890: %v, %v; want ErrTVDBTruncated", shown(eps), err)
	}
	if IsTransient(ErrTVDBTruncated) {
		t.Error("an incomplete list is not a transient error")
	}

	// A page TVDB gave twice, or an episode on two pages, counts once; and the
	// total is the number of episodes, the repeat left out.
	page := func(ids string, next, total string) fakeRoute {
		return fakeRoute{status: 200, body: `{"status":"success","data":{"id":9,"name":"Show","episodes":[` + ids + `]},"links":{"next":` + next + `,"total_items":` + total + `}}`}
	}
	ep := func(ids ...int) string {
		var parts []string
		for _, id := range ids {
			parts = append(parts, fmt.Sprintf(`{"id":%d,"name":"E%d","number":%d}`, id, id, id))
		}
		return strings.Join(parts, ",")
	}
	const path = "/series/9/episodes/official/eng"
	for _, tc := range []struct {
		name    string
		total   string
		wantIDs []int64 // nil: ErrTVDBTruncated
	}{
		{"the repeat is dropped and the rest add up", `3`, []int64{1, 2, 3}},
		{"the total as a string", `"3"`, []int64{1, 2, 3}},
		{"the repeat was counted by TVDB: an episode is missing", `4`, nil},
		{"more than TVDB counted", `2`, nil},
	} {
		f := newTVDBFake(t)
		f.set(path, params("page", "0"), page(ep(1, 2), `"https://api4.thetvdb.com/v4/next"`, tc.total))
		f.set(path, params("page", "1"), page(ep(2, 3), `null`, tc.total))
		eps, err := testTVDBClient(t, f).Episodes(ctx, 9, "official", "eng")
		if tc.wantIDs == nil {
			if !errors.Is(err, ErrTVDBTruncated) || eps != nil {
				t.Errorf("%s: %v, %v; want ErrTVDBTruncated", tc.name, shown(eps), err)
			}
			continue
		}
		var ids []int64
		if err == nil {
			for _, e := range eps.Episodes {
				ids = append(ids, e.ID)
			}
		}
		if err != nil || !slices.Equal(ids, tc.wantIDs) {
			t.Errorf("%s: ids %v, %v; want %v", tc.name, ids, err, tc.wantIDs)
		}
	}

	// A show with no episodes counts 0 and has none.
	none := newTVDBFake(t)
	none.set(path, params("page", "0"), page(``, `null`, `0`))
	if eps, err := testTVDBClient(t, none).Episodes(ctx, 9, "official", "eng"); err != nil || len(eps.Episodes) != 0 || eps.Series.Name != "Show" {
		t.Errorf("no episodes of 0: %v, %v", shown(eps), err)
	}
}

// Twelve of the records TVDB's /updates answered with, as two pages of six (the
// real answer was one page). Every one has recordType "" and entityType
// "series"; the ids are repeated, within a page and from one to the next.
func TestTVDBUpdatesFollowEveryPage(t *testing.T) {
	ctx := t.Context()
	f := newTVDBFake(t)
	since := time.Date(2026, 11, 1, 23, 30, 0, 0, time.FixedZone("x", -5*3600)) // 2026-11-02 04:30 UTC
	const unix = "1793593800"
	f.on("/updates", params("action", "update", "page", "0", "since", unix, "type", "series"), "updates_p0.json")
	f.on("/updates", params("action", "update", "page", "1", "since", unix, "type", "series"), "updates_p1.json")
	c := testTVDBClient(t, f)

	ids, err := c.Updates(ctx, since)
	if err != nil || !slices.Equal(ids, []int64{255390, 274590, 484780, 484789, 484779, 465020}) {
		t.Errorf("Updates = %v, %v; want each series once, in order", ids, err)
	}
	if got, want := f.asked(), []string{
		"/login",
		"/updates?action=update&page=0&since=" + unix + "&type=series",
		"/updates?action=update&page=1&since=" + unix + "&type=series",
	}; !slices.Equal(got, want) {
		t.Errorf("requests:\n got %q\nwant %q", got, want)
	}

	// An error on a later page is an error, not a short list.
	broken := newTVDBFake(t)
	broken.on("/updates", params("action", "update", "page", "0", "since", unix, "type", "series"), "updates_p0.json")
	broken.fail("/updates", params("action", "update", "page", "1", "since", unix, "type", "series"), 503, "", nil)
	if ids, err := testTVDBClient(t, broken).Updates(ctx, since); err == nil || ids != nil || !IsTransient(err) {
		t.Errorf("updates with a failing page 2 = %v, %v", ids, err)
	}

	// Nothing changed.
	empty := newTVDBFake(t)
	empty.set("/updates", params("action", "update", "page", "0", "since", unix, "type", "series"), fakeRoute{status: 200, body: `{"status":"success","data":null,"links":{"next":null}}`})
	if ids, err := testTVDBClient(t, empty).Updates(ctx, since); err != nil || len(ids) != 0 {
		t.Errorf("no updates = %v, %v", ids, err)
	}
}

// A list that never ends is cut off, and says so: one that stops short would
// pass for all of it.
func TestTVDBEndlessListsAreCutOff(t *testing.T) {
	ctx := t.Context()
	since := time.Unix(1793593800, 0)
	var pages atomic.Int32
	// feed serves pages 0..total-1 of a list; every page but the last has a next.
	feed := func(path string, total int, query func(page int) string, body func(page int) string) *tvdbFake {
		f := newTVDBFake(t)
		pages.Store(0)
		for p := 0; p < maxTVDBPages+5; p++ {
			f.handle(path, query(p), func(w http.ResponseWriter, r *http.Request) {
				pages.Add(1)
				next := `"https://api4.thetvdb.com/v4/next"`
				if p == total-1 {
					next = "null"
				}
				fmt.Fprintf(w, `{"status":"success","data":%s,"links":{"next":%s}}`, body(p), next)
			})
		}
		return f
	}
	episodes := func(total int) *tvdbFake {
		return feed("/series/1/episodes/official/eng", total,
			func(p int) string { return params("page", strconv.Itoa(p)) },
			func(p int) string { return fmt.Sprintf(`{"series":{"id":1},"episodes":[{"id":%d}]}`, p+1) })
	}
	updates := func(total int) *tvdbFake {
		return feed("/updates", total,
			func(p int) string {
				return params("action", "update", "page", strconv.Itoa(p), "since", "1793593800", "type", "series")
			},
			func(p int) string { return fmt.Sprintf(`[{"recordId":%d}]`, p+1) })
	}

	if got, err := testTVDBClient(t, episodes(99999)).Episodes(ctx, 1, "official", "eng"); !errors.Is(err, ErrTVDBTruncated) || got != nil || int(pages.Load()) != maxTVDBPages {
		t.Errorf("endless episodes: %v, %d pages, %v; want ErrTVDBTruncated after %d pages", got, pages.Load(), err, maxTVDBPages)
	}
	if got, err := testTVDBClient(t, episodes(maxTVDBPages+1)).Episodes(ctx, 1, "official", "eng"); !errors.Is(err, ErrTVDBTruncated) || got != nil {
		t.Errorf("episodes one page too long: %v, %v", got, err)
	}
	if got, err := testTVDBClient(t, episodes(maxTVDBPages)).Episodes(ctx, 1, "official", "eng"); err != nil || len(got.Episodes) != maxTVDBPages || int(pages.Load()) != maxTVDBPages {
		t.Errorf("episodes of exactly %d pages: %v, %d pages, %v", maxTVDBPages, got, pages.Load(), err)
	}

	if ids, err := testTVDBClient(t, updates(99999)).Updates(ctx, since); !errors.Is(err, ErrTVDBTruncated) || ids != nil || int(pages.Load()) != maxTVDBPages {
		t.Errorf("endless updates: %d ids, %d pages, %v; want ErrTVDBTruncated after %d pages", len(ids), pages.Load(), err, maxTVDBPages)
	}
	if ids, err := testTVDBClient(t, updates(maxTVDBPages+1)).Updates(ctx, since); !errors.Is(err, ErrTVDBTruncated) || ids != nil {
		t.Errorf("updates one page too long: %d ids, %v", len(ids), err)
	}
	if ids, err := testTVDBClient(t, updates(maxTVDBPages)).Updates(ctx, since); err != nil || len(ids) != maxTVDBPages || int(pages.Load()) != maxTVDBPages {
		t.Errorf("updates of exactly %d pages: %d ids, %d pages, %v", maxTVDBPages, len(ids), pages.Load(), err)
	}
	if IsTransient(ErrTVDBTruncated) {
		t.Error("a list that is too long is not a transient error: retrying cannot make it shorter")
	}
}

func TestTVDBLanguageMapping(t *testing.T) {
	for tag, want := range map[string]string{
		"en-US": "eng", "en": "eng", "EN-gb": "eng", " en-US ": "eng", "en_US": "eng",
		// TVDB's "pt" is Portuguese - Brazil, and its "por" Portuguese - Portugal.
		"pt-BR": "pt", "pt_BR": "pt", "pt": "pt", "PT-br": "pt", "pt-Latn-BR": "pt", "pt-PT": "por", "pt_pt": "por", "pt-AO": "por", "pt-MZ": "por",
		"es-ES": "spa", "es-MX": "spa", "fr-FR": "fra", "fr-CA": "fra", "de-DE": "deu", "it-IT": "ita",
		"ja-JP": "jpn", "ko-KR": "kor", "nl-NL": "nld", "ru-RU": "rus", "pl-PL": "pol", "sv-SE": "swe",
		"da-DK": "dan", "fi-FI": "fin", "nb-NO": "nor", "no": "nor", "tr-TR": "tur", "el-GR": "ell",
		"cs-CZ": "ces", "sk-SK": "slk", "hu-HU": "hun", "ro-RO": "ron", "uk-UA": "ukr", "bg-BG": "bul",
		"he-IL": "heb", "iw": "heb", "ar-SA": "ara", "hi-IN": "hin", "th-TH": "tha", "vi-VN": "vie",
		"id-ID": "ind", "in": "ind", "fa-IR": "fas", "af-ZA": "afr", "bn-BD": "ben", "ca-ES": "cat", "hr-HR": "hrv", "sr-RS": "srp", "fil": "tgl", "tl": "tgl",
		"yue": "yue", "eu": "eus", "cy": "cym", "ga": "gle", "gl": "glg", "is": "isl", "lt": "lit", "lv": "lav", "et": "est", "sl": "slv", "nn-NO": "nor",
		"zh": "zho", "zh-CN": "zho", "zh-Hans-CN": "zho", "zh-TW": "zhtw", "zh-HK": "zhtw", "zh-Hant": "zhtw", "zh_TW": "zhtw",
		// Already TVDB's own, or the other ISO spelling of it.
		"eng": "eng", "ENG": "eng", "por": "por", "POR": "por", "deu": "deu", "fra": "fra", "zhtw": "zhtw", "tlh": "tlh",
		"ger": "deu", "fre": "fra", "chi": "zho", "dut": "nld", "gre": "ell", "cze": "ces",
		// Not a language TVDB has a code for.
		"": "", "  ": "", "xx": "", "x": "", "12": "", "e1g": "", "english": "", "-US": "", "en1": "",
	} {
		if got := tvdbLanguage(tag); got != want {
			t.Errorf("tvdbLanguage(%q) = %q, want %q", tag, got, want)
		}
	}
	// The common ones.
	if tvdbLanguage("en-US") != "eng" || tvdbLanguage("pt-BR") != "pt" || tvdbLanguage("es-ES") != "spa" ||
		tvdbLanguage("fr-FR") != "fra" || tvdbLanguage("de-DE") != "deu" || tvdbLanguage("ja-JP") != "jpn" {
		t.Error("the common languages do not map to the codes TVDB uses")
	}

	// The table against TVDB's own list of languages (/languages): every code the
	// table can give is one TVDB has, and the ones that are not what their ISO
	// code would lead to are what TVDB says they are.
	var list struct {
		Data []struct{ ID, Name string }
	}
	data, err := os.ReadFile(filepath.Join("testdata", "tvdb", "languages.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, l := range list.Data {
		names[l.ID] = l.Name
	}
	if len(names) < 150 {
		t.Fatalf("only %d languages in the fixture", len(names))
	}
	for k, v := range tvdbLanguages {
		if _, ok := names[v]; !ok {
			t.Errorf("tvdbLanguages[%q] = %q, which TVDB's /languages does not have", k, v)
		}
		if k != strings.ToLower(k) || len(k) < 2 || len(k) > 4 {
			t.Errorf("tvdbLanguages key %q is malformed", k)
		}
	}
	for code, name := range map[string]string{"pt": "Portuguese - Brazil", "por": "Portuguese - Portugal", "zho": "Chinese - China", "zhtw": "Chinese - Taiwan", "eng": "English"} {
		if names[code] != name {
			t.Errorf("TVDB's %q is %q, not %q", code, names[code], name)
		}
	}

	c := NewTVDBClient(TVDBOptions{})
	if c.Language() != "en-US" || c.LanguageCode() != "eng" {
		t.Errorf("default language %q / %q", c.Language(), c.LanguageCode())
	}
	c = NewTVDBClient(TVDBOptions{Language: "pt-BR"})
	if c.Language() != "pt-BR" || c.LanguageCode() != "pt" || c.langCode("de") != "deu" || c.langCode(" ") != "pt" || c.langCode("xx") != "eng" {
		t.Errorf("pt-BR client: %q / %q", c.Language(), c.LanguageCode())
	}
	if got := NewTVDBClient(TVDBOptions{Language: "klingon!"}).LanguageCode(); got != "eng" {
		t.Errorf("a language TVDB has no code for gave %q, want English", got)
	}
}

func TestTVDBImageURLs(t *testing.T) {
	for _, tc := range []struct {
		ref   string
		small bool
		want  string
	}{
		{"/banners/posters/81189-10.jpg", false, "https://artworks.thetvdb.com/banners/posters/81189-10.jpg"},
		{"banners/posters/81189-10.jpg", false, "https://artworks.thetvdb.com/banners/posters/81189-10.jpg"},
		{"/banners/posters/81189-10.jpg", true, "https://artworks.thetvdb.com/banners/posters/81189-10_t.jpg"},
		{"/banners/posters/81189-10_t.jpg", true, "https://artworks.thetvdb.com/banners/posters/81189-10_t.jpg"},
		{"/banners/posters/81189-10_t.jpg", false, "https://artworks.thetvdb.com/banners/posters/81189-10_t.jpg"},
		{"/banners/v4/series/81189/posters/5f1e2d3c4b5a6.png", true, "https://artworks.thetvdb.com/banners/v4/series/81189/posters/5f1e2d3c4b5a6_t.png"},
		{"https://artworks.thetvdb.com/banners/v4/series/81189/posters/5f1e2d3c4b5a6.jpg", false, "https://artworks.thetvdb.com/banners/v4/series/81189/posters/5f1e2d3c4b5a6.jpg"},
		{"https://artworks.thetvdb.com/banners/v4/series/81189/posters/5f1e2d3c4b5a6.jpg", true, "https://artworks.thetvdb.com/banners/v4/series/81189/posters/5f1e2d3c4b5a6_t.jpg"},
		{"http://artworks.thetvdb.com/banners/episodes/81189/349232.jpg", false, "https://artworks.thetvdb.com/banners/episodes/81189/349232.jpg"},
		{"HTTPS://Artworks.TheTVDB.com/banners/Seasons/81189-1.JPG", true, "https://artworks.thetvdb.com/banners/Seasons/81189-1_t.JPG"},
		{"/banners/no-extension", true, "https://artworks.thetvdb.com/banners/no-extension_t"},
		{"/banners/v1.2/picture", true, "https://artworks.thetvdb.com/banners/v1.2/picture_t"},
		{"/banners/a.b.c.jpg", true, "https://artworks.thetvdb.com/banners/a.b.c_t.jpg"},
		{"/banners/.hidden", true, "https://artworks.thetvdb.com/banners/.hidden_t"},
	} {
		if got := TVDBImageURL(tc.ref, tc.small); got != tc.want {
			t.Errorf("TVDBImageURL(%q, %v) = %q, want %q", tc.ref, tc.small, got, tc.want)
		}
		if !ValidTVDBImagePath(tc.ref) {
			t.Errorf("ValidTVDBImagePath(%q) = false", tc.ref)
		}
	}

	for ref, want := range map[string]string{
		"/banners/x.jpg": "/banners/x.jpg",
		"banners/x.jpg":  "/banners/x.jpg",
		"https://artworks.thetvdb.com/banners/x.jpg":        "/banners/x.jpg",
		"http://ARTWORKS.thetvdb.com/banners/Dir/Y_1-2.png": "/banners/Dir/Y_1-2.png",
	} {
		if got, ok := TVDBImagePath(ref); !ok || got != want {
			t.Errorf("TVDBImagePath(%q) = %q, %v; want %q", ref, got, ok, want)
		}
	}

	long := "/" + strings.Repeat("a", maxTVDBImagePath) + ".jpg"
	for _, ref := range []string{
		// Nothing, or nothing but the host.
		"", "/", "//", "https://artworks.thetvdb.com", "https://artworks.thetvdb.com/", "https://", "https:///banners/x.jpg",
		// Traversal, plain and disguised.
		"../etc/passwd", "/../etc/passwd", "/banners/../../etc/passwd", "banners/..", "/banners/..%2f..%2fetc/passwd",
		"/banners/%2e%2e/x.jpg", "/banners/%2E%2E/x.jpg", "/banners/..;/x.jpg", "/banners/a..b.jpg", "/banners/..\\x.jpg",
		"https://artworks.thetvdb.com/banners/../../x", "https://artworks.thetvdb.com/banners/%2e%2e/x.jpg",
		"/banners/./x.jpg", "/./x.jpg", "/banners//x.jpg", "/banners/x.jpg/", "/banners/x.jpg/.",
		// Things that change what an address means.
		"//evil.com/x.jpg", "/banners/x.jpg?a=b", "/banners/x.jpg#f", "/banners/x y.jpg", "/banners/x%20y.jpg", "/banners/x\\y.jpg",
		"/banners/x\n.jpg", "/banners/x\x00.jpg", "/banners/é.jpg", "/banners/x:80.jpg", "/banners/x@y.jpg", "/banners/x;y.jpg", " /banners/x.jpg",
		long,
		// Other hosts, and ways to look like the right one.
		"https://evil.com/banners/x.jpg", "https://evil.com/artworks.thetvdb.com/banners/x.jpg",
		"https://artworks.thetvdb.com.evil.com/banners/x.jpg", "https://artworks.thetvdb.com@evil.com/banners/x.jpg",
		"https://evil.com@artworks.thetvdb.com/banners/x.jpg", "https://user:pw@artworks.thetvdb.com/banners/x.jpg",
		"https://artworks.thetvdb.com:8443/banners/x.jpg", "https://artworks.thetvdb.com:80@evil.com/x.jpg",
		"https://artworks.thetvdb.com\\@evil.com/x.jpg", "https://artworks.thetvdb.com./banners/x.jpg",
		"https://artworks.thetvdb.com?x=/banners/x.jpg", "https://artworks.thetvdb.com#/banners/x.jpg",
		// Hosts as long as the right one, which only comparing them can tell apart.
		"https://artworks.thetvdb.net/banners/x.jpg", "https://artworx.thetvdb.com/banners/x.jpg", "https://xrtworks.thetvdb.com/banners/x.jpg",
		"https://thetvdb.com/banners/x.jpg", "https://www.thetvdb.com/banners/x.jpg", "https://api4.thetvdb.com/v4/login",
		"https://image.tmdb.org/t/p/w92/x.jpg", "ftp://artworks.thetvdb.com/banners/x.jpg", "file:///etc/passwd",
		"javascript:alert(1)", "data:image/png;base64,AAAA", "tvdb:/banners/x.jpg", "tmdb:/x.jpg",
		// No scheme and no slash, and the first segment is a host: not a directory.
		"evil.com/banners/x.jpg", "artworks.thetvdb.com/banners/x.jpg", "a.b/c.jpg", "localhost.localdomain/x.jpg",
	} {
		if got := TVDBImageURL(ref, false); got != "" {
			t.Errorf("TVDBImageURL(%q) = %q, want it refused", ref, got)
		}
		if got := TVDBImageURL(ref, true); got != "" {
			t.Errorf("TVDBImageURL(%q, small) = %q, want it refused", ref, got)
		}
		if ValidTVDBImagePath(ref) {
			t.Errorf("ValidTVDBImagePath(%q) = true", ref)
		}
		if p, ok := TVDBImagePath(ref); ok || p != "" {
			t.Errorf("TVDBImagePath(%q) = %q, %v", ref, p, ok)
		}
	}
}

// The longest path is 512 bytes, with its slash: the 512th is let in, the 513th not.
func TestTVDBImagePathLengthLimit(t *testing.T) {
	atLimit := "/" + strings.Repeat("a", maxTVDBImagePath-1)
	overLimit := atLimit + "a"
	if len(atLimit) != 512 || len(overLimit) != 513 || maxTVDBImagePath != 512 {
		t.Fatalf("limit %d, paths of %d and %d", maxTVDBImagePath, len(atLimit), len(overLimit))
	}
	for _, ref := range []string{atLimit, atLimit[1:], "https://" + TVDBImageHost + atLimit} {
		if p, ok := TVDBImagePath(ref); !ok || p != atLimit {
			t.Errorf("a path of %d bytes: %v", len(atLimit), ok)
		}
	}
	for _, ref := range []string{overLimit, overLimit[1:], "https://" + TVDBImageHost + overLimit} {
		if ValidTVDBImagePath(ref) || TVDBImageURL(ref, false) != "" {
			t.Errorf("a path of %d bytes was let in", len(overLimit))
		}
	}
	// The thumbnail's extra "_t" does not matter to the limit of what comes in.
	if got := TVDBImageURL(atLimit, true); got != tvdbImageBase+atLimit+"_t" {
		t.Errorf("thumbnail of a path at the limit = %q", got)
	}
	// A single name with a dot is a file, not a host.
	if p, ok := TVDBImagePath("x.jpg"); !ok || p != "/x.jpg" {
		t.Errorf("x.jpg: %q, %v", p, ok)
	}
	if p, ok := TVDBImagePath("banners/v1.2/pic.jpg"); !ok || p != "/banners/v1.2/pic.jpg" {
		t.Errorf("a dot below the first segment: %q, %v", p, ok)
	}
}

// The thumbnail TVDB names is used when there is one; the "_t" made up only when
// there is not.
func TestTVDBArtworkURL(t *testing.T) {
	for _, tc := range []struct {
		name  string
		a     TVDBArtwork
		small bool
		want  string
	}{
		{"the image", TVDBArtwork{Image: "https://artworks.thetvdb.com/banners/posters/1-1.jpg", Thumbnail: "https://artworks.thetvdb.com/banners/posters/1-1_t.jpg"}, false, "https://artworks.thetvdb.com/banners/posters/1-1.jpg"},
		{"the thumbnail TVDB names, which is not image + _t", TVDBArtwork{Image: "https://artworks.thetvdb.com/banners/posters/1-1.jpg", Thumbnail: "https://artworks.thetvdb.com/banners/thumbs/one.jpg"}, true, "https://artworks.thetvdb.com/banners/thumbs/one.jpg"},
		{"no thumbnail named", TVDBArtwork{Image: "/banners/posters/1-1.jpg"}, true, "https://artworks.thetvdb.com/banners/posters/1-1_t.jpg"},
		{"a thumbnail that is not an artworks address", TVDBArtwork{Image: "/banners/posters/1-1.jpg", Thumbnail: "https://evil.example/t.jpg"}, true, "https://artworks.thetvdb.com/banners/posters/1-1_t.jpg"},
		{"an image that is not either", TVDBArtwork{Image: "https://evil.example/a.jpg", Thumbnail: "https://evil.example/t.jpg"}, true, ""},
		{"nothing", TVDBArtwork{}, false, ""},
	} {
		if got := tc.a.URL(tc.small); got != tc.want {
			t.Errorf("%s: URL(%v) = %q, want %q", tc.name, tc.small, got, tc.want)
		}
	}
}

func FuzzTVDBImagePath(f *testing.F) {
	for _, s := range []string{"/banners/x.jpg", "https://artworks.thetvdb.com/banners/x.jpg", "../x", "https://artworks.thetvdb.com@evil.com/x", "%2e%2e/"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, ref string) {
		for _, small := range []bool{false, true} {
			got := TVDBImageURL(ref, small)
			if got == "" {
				continue
			}
			// Whatever was accepted is an address on the artworks host, with a plain path.
			u, err := url.Parse(got)
			if err != nil || u.Scheme != "https" || u.Host != TVDBImageHost || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
				u.Path != u.EscapedPath() || strings.Contains(u.Path, "..") || strings.Contains(u.Path, "//") || !strings.HasPrefix(got, tvdbImageBase+"/") {
				t.Fatalf("%q was accepted as %q", ref, got)
			}
		}
	})
}

func TestTVDBSecretsNeverAppearInErrorsOrLogs(t *testing.T) {
	logger, logs := capture()
	f := newTVDBFake(t)
	// A server that echoes the credentials and the token in its error messages,
	// the worst case.
	echo := func(srv *tvdbFake, status int, headers map[string]string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			for k, v := range headers {
				w.Header().Set(k, v)
			}
			w.WriteHeader(status)
			fmt.Fprintf(w, `{"status":"failure","message":"bad apikey=%s pin=%s token %s (%s)"}`,
				srv.key, srv.pin, srv.currentToken(), r.Header.Get("Authorization"))
		}
	}
	f.on("/series/81189/extended", tvdbExt, "series_81189_extended.json")
	f.handle("/series/401/extended", tvdbExt, echo(f, 401, nil))
	f.handle("/series/500/extended", tvdbExt, echo(f, 500, nil))
	f.handle("/series/404/extended", tvdbExt, echo(f, 404, nil))
	f.handle("/series/429/extended", tvdbExt, echo(f, 429, map[string]string{"Retry-After": "99999"}))
	f.on("/series/777/extended", tvdbExt, "malformed.json")
	f.handle("/series/888/extended", tvdbExt, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	// A 429 with a long Retry-After pauses its client, so every scenario gets its own.
	newClient := func() *TVDBClient {
		return testTVDBClient(t, f, func(o *TVDBOptions) { o.Logger = logger; o.Timeout = 100 * time.Millisecond })
	}

	var texts []string
	note := func(err error) {
		if err == nil {
			t.Errorf("a call that should fail succeeded")
			return
		}
		texts = append(texts, err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err), fmt.Sprintf("%s", err))
		for u := errors.Unwrap(err); u != nil; u = errors.Unwrap(u) {
			texts = append(texts, u.Error(), fmt.Sprintf("%#v", u))
		}
		var e *Error // as the TMDB helpers see it
		if errors.As(err, &e) {
			texts = append(texts, e.Error(), e.Message, fmt.Sprintf("%#v", e))
		}
	}
	if _, err := newClient().SeriesExtended(t.Context(), 81189); err != nil {
		t.Fatalf("a good call failed: %v", err)
	}
	for _, id := range []int64{401, 500, 404, 429, 777, 888} {
		_, err := newClient().SeriesExtended(t.Context(), id)
		note(err)
	}
	// Logins that fail while echoing the credentials back.
	for _, status := range []int{401, 403, 500, 429} {
		bad := newTVDBFake(t)
		bad.handle("/login", "", echo(bad, status, map[string]string{"Retry-After": "99999"}))
		note(testTVDBClient(t, bad, func(o *TVDBOptions) { o.Logger = logger }).Check(t.Context()))
	}
	// A PIN that is wrong is not echoed either.
	wrong := testTVDBClient(t, f, func(o *TVDBOptions) { o.Logger = logger; o.PIN = "WRONG-PIN-0000" })
	note(wrong.Check(t.Context()))
	_, err := wrong.Search(t.Context(), "x", 0)
	note(err)
	// The transport failure, whose net/http error carries the request URL.
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	down := NewTVDBClient(TVDBOptions{Key: f.key, PIN: f.pin, BaseURL: dead.URL + "/v4", Logger: logger})
	_, err = down.Search(t.Context(), "x", 0)
	note(err)
	// An unreachable host, by name.
	nohost := NewTVDBClient(TVDBOptions{Key: f.key, PIN: f.pin, BaseURL: "http://nonexistent.invalid/v4", Logger: logger, Timeout: time.Second})
	_, err = nohost.Person(t.Context(), 1)
	note(err)
	// A bad base URL fails before any connection.
	badURL := NewTVDBClient(TVDBOptions{Key: f.key, PIN: f.pin, BaseURL: "http://[::1/v4", Logger: logger})
	note(badURL.Check(t.Context()))
	// A transport that puts what it was handed in its error text: the key and
	// PIN of the login, and the token of the request.
	proxy := roundTripper(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/login") {
			return http.DefaultTransport.RoundTrip(r)
		}
		return nil, fmt.Errorf("proxyconnect tcp: refused for %s (apikey %s pin %s)", r.Header.Get("Authorization"), f.key, f.pin)
	})
	viaProxy := testTVDBClient(t, f, func(o *TVDBOptions) { o.Logger = logger; o.HTTPClient = &http.Client{Transport: proxy} })
	_, err = viaProxy.SeriesExtended(t.Context(), 81189)
	note(err)
	if !IsTransient(err) || !strings.Contains(err.Error(), "proxyconnect") || !strings.Contains(err.Error(), "***") {
		t.Errorf("proxy failure = %q; the cause should be there with the secrets cut out", err)
	}
	loginProxy := roundTripper(func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("proxyconnect tcp: refused for apikey=%s&pin=%s", f.key, f.pin)
	})
	note(testTVDBClient(t, f, func(o *TVDBOptions) { o.Logger = logger; o.HTTPClient = &http.Client{Transport: loginProxy} }).Check(t.Context()))

	secrets := []string{f.key, f.pin, "WRONG-PIN-0000", url.QueryEscape(f.pin)}
	tokens := f.issuedTokens()
	if len(tokens) < 6 {
		t.Fatalf("only %d tokens were issued", len(tokens))
	}
	for _, tok := range tokens {
		secrets = append(secrets, tok, tok[strings.LastIndex(tok, ".")+1:], "Bearer "+tok)
	}
	everything := strings.Join(texts, "\n") + "\n" + logs.String()
	for _, secret := range secrets {
		if strings.Contains(everything, secret) {
			t.Errorf("%q appears in errors or logs, for example in: %s", secret, lineWith(everything, secret))
		}
	}

	// Positive controls: the capture works, the redaction ran, and the thing we
	// guard against is real.
	if !strings.Contains(logs.String(), "tvdb request") || !strings.Contains(logs.String(), "/series/81189/extended") {
		t.Errorf("the log capture saw no request log:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "tvdb rate limited") {
		t.Errorf("the log capture saw no 429 warning:\n%s", logs.String())
	}
	if !strings.Contains(everything, "***") {
		t.Error("no redaction marker anywhere: the echoed secrets were never seen")
	}
	for _, want := range []string{"apikey=***", "pin=***", "token ***"} {
		if !strings.Contains(everything, want) {
			t.Errorf("%q not found: the echo of a secret was not redacted where expected", want)
		}
	}
	if len(texts) < 60 {
		t.Errorf("only %d error texts were checked", len(texts))
	}
	_, raw := http.Get(dead.URL + "/v4/search?apikey=" + f.key)
	if raw == nil || !strings.Contains(raw.Error(), f.key) {
		t.Errorf("control: net/http's own error should show a secret that is in the URL, got %v", raw)
	}
}

// shown describes episodes for a failure message, without printing them all.
func shown(eps *TVDBEpisodes) string {
	if eps == nil {
		return "nil"
	}
	return fmt.Sprintf("%d episodes of %q", len(eps.Episodes), eps.Series.Name)
}

// lineWith is the first line of text that has sub in it.
func lineWith(text, sub string) string {
	for _, l := range strings.Split(text, "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}

func TestTVDBRedact(t *testing.T) {
	const key, pin = "a b/c+d&e", "p!n"
	c := NewTVDBClient(TVDBOptions{Key: key, PIN: pin})
	for _, in := range []string{key, url.QueryEscape(key), url.PathEscape(key), pin, url.QueryEscape(pin)} {
		if got := c.redact(in); got != "***" {
			t.Errorf("redact(%q) = %q, want ***", in, got)
		}
	}
	if got := c.redact("not a secret"); got != "not a secret" {
		t.Errorf("redact changed text without a secret: %q", got)
	}
	if got := NewTVDBClient(TVDBOptions{}).redact("anything"); got != "anything" {
		t.Errorf("redact without a key changed the text: %q", got)
	}
	// Every token the client has held, not just the current one.
	for i := 1; i <= maxTVDBTokens; i++ {
		c.setToken(fmt.Sprintf("tok-%d-secret", i))
	}
	if got := c.redact("old tok-1-secret, new tok-4-secret"); got != "old ***, new ***" {
		t.Errorf("redact of tokens = %q", got)
	}
	// A PIN inside the key (or the other way round) does not leave half a secret.
	c = NewTVDBClient(TVDBOptions{Key: "cd", PIN: "abcdef"})
	if got := c.redact("key cd pin abcdef"); got != "key *** pin ***" {
		t.Errorf("overlapping secrets: %q", got)
	}
}

func TestTVDBResponsesAreCappedAtEightMiB(t *testing.T) {
	big := func(n int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			// Valid JSON of about n bytes.
			fmt.Fprintf(w, `{"status":"success","data":{"id": 1, "overview": "%s"}}`, strings.Repeat("x", n))
		}
	}
	f := newTVDBFake(t)
	f.handle("/series/1/extended", tvdbExt, big(7<<20))
	f.handle("/series/2/extended", tvdbExt, big(9<<20))
	c := testTVDBClient(t, f)
	if s, err := c.SeriesExtended(t.Context(), 1); err != nil || len(s.Overview) != 7<<20 {
		t.Errorf("7 MiB response: %v", err)
	}
	// Asking again gets the same answer: a body that is too large is a final error.
	var e *TVDBError
	if _, err := c.SeriesExtended(t.Context(), 2); !errors.As(err, &e) || IsTransient(err) || e.Message != "response too large" || e.Status != 200 {
		t.Errorf("9 MiB response: err = %v; want a final \"response too large\"", err)
	}
	// A body of exactly 8 MiB is still read: the limit is on what is over it.
	const head, tail = `{"status":"success","data":{"id": 4, "overview": "`, `"}}`
	fill := maxResponseBytes - len(head) - len(tail)
	exact := head + strings.Repeat("x", fill) + tail
	if len(exact) != maxResponseBytes {
		t.Fatalf("the test body is %d bytes, want %d", len(exact), maxResponseBytes)
	}
	f.handle("/series/4/extended", tvdbExt, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, exact) })
	if s, err := c.SeriesExtended(t.Context(), 4); err != nil || len(s.Overview) != fill {
		t.Errorf("a body of exactly %d bytes: %v", maxResponseBytes, err)
	}
	// And it is the size that does it, not what is in it: a 8 MiB body of spaces is too large too.
	f.handle("/series/3/extended", tvdbExt, func(w http.ResponseWriter, r *http.Request) {
		w.Write(bytes.Repeat([]byte(" "), maxResponseBytes+1))
	})
	if _, err := c.SeriesExtended(t.Context(), 3); !errors.As(err, &e) || IsTransient(err) || e.Message != "response too large" {
		t.Errorf("a body one byte over: err = %v", err)
	}
}

// Redirects are not followed: a 3xx from the base URL would hand the key, the PIN
// and the token to whoever it points at.
func TestTVDBRedirectsAreNotFollowed(t *testing.T) {
	ctx := t.Context()
	type seen struct{ method, body, auth string }
	var mu sync.Mutex
	var reached []seen
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		reached = append(reached, seen{r.Method, string(body), r.Header.Get("Authorization")})
		mu.Unlock()
		w.Write([]byte(`{"status":"success","data":{"token":"from-elsewhere"}}`))
	}))
	defer elsewhere.Close()
	got := func() []seen {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(reached)
	}
	bounce := func(status int) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			http.Redirect(w, r, elsewhere.URL+r.URL.Path, status)
		}))
		t.Cleanup(srv.Close)
		return srv
	}

	for _, status := range []int{301, 302, 303, 307, 308} {
		front := bounce(status)
		c := NewTVDBClient(TVDBOptions{Key: tvdbTestKey, PIN: tvdbTestPIN, BaseURL: front.URL + "/v4", Logger: discardLogger()})
		err := c.Check(ctx)
		var e *TVDBError
		if !errors.As(err, &e) || e.Status != status || IsTransient(err) || e.Message != "redirect not followed" || !strings.Contains(e.Op, "POST /login") {
			t.Errorf("login redirected with %d: err = %#v, want a final error for the 3xx", status, err)
		}
		if strings.Contains(fmt.Sprint(err), tvdbTestKey) || strings.Contains(fmt.Sprint(err), tvdbTestPIN) {
			t.Errorf("the error shows a secret: %v", err)
		}
		if r := got(); len(r) != 0 {
			t.Errorf("login redirected with %d: the other server was reached: %+v", status, r)
		}
	}

	// A client of the caller's with no CheckRedirect of its own is copied and gets the same.
	caller := &http.Client{Timeout: time.Minute}
	cc := NewTVDBClient(TVDBOptions{Key: tvdbTestKey, PIN: tvdbTestPIN, BaseURL: bounce(307).URL + "/v4", HTTPClient: caller, Logger: discardLogger()})
	if err := cc.Check(ctx); err == nil || IsTransient(err) || !strings.Contains(err.Error(), "307") || len(got()) != 0 {
		t.Errorf("a caller's client with no CheckRedirect: err = %v, the other server saw %+v", err, got())
	}
	if caller.CheckRedirect != nil || caller.Timeout != time.Minute {
		t.Error("the caller's client was changed")
	}

	// A request with the token, redirected to another port of the same host: net/http
	// would send the Authorization header along, since it compares host names only.
	front := newTVDBFake(t)
	front.handle("/search", "query=x&type=series", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/v4/search", http.StatusFound)
	})
	c := testTVDBClient(t, front)
	if _, err := c.Search(ctx, "x", 0); err == nil || IsTransient(err) || !strings.Contains(err.Error(), "302") {
		t.Errorf("a redirected search: err = %v", err)
	}
	if r := got(); len(r) != 0 {
		t.Errorf("a redirected search reached the other server: %+v", r)
	}

	// Positive control: the stock client follows both and hands everything over.
	stock := bounce(307)
	resp, err := http.Post(stock.URL+"/v4/login", "application/json", strings.NewReader(`{"apikey":"`+tvdbTestKey+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	req, _ := http.NewRequest(http.MethodGet, front.URL()+"/search?query=x&type=series", nil)
	req.Header.Set("Authorization", "Bearer "+front.currentToken())
	if front.currentToken() == "" {
		req.Header.Set("Authorization", "Bearer stock-token")
	}
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}
	var sawKey, sawBearer bool
	for _, r := range got() {
		sawKey = sawKey || strings.Contains(r.body, tvdbTestKey)
		sawBearer = sawBearer || strings.HasPrefix(r.auth, "Bearer ")
	}
	if !sawKey || !sawBearer {
		t.Errorf("control: a stock http.Client should have handed over the key (%v) and the token (%v): %+v", sawKey, sawBearer, got())
	}

	// A client of the caller's: copied, not changed; its own CheckRedirect is kept.
	plain := &http.Client{Timeout: time.Minute}
	NewTVDBClient(TVDBOptions{HTTPClient: plain})
	if plain.CheckRedirect != nil {
		t.Error("the caller's client was changed")
	}
	var asked atomic.Int32
	mine := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { asked.Add(1); return nil }}
	mc := NewTVDBClient(TVDBOptions{Key: tvdbTestKey, PIN: tvdbTestPIN, BaseURL: bounce(307).URL + "/v4", HTTPClient: mine, Logger: discardLogger()})
	if err := mc.Check(ctx); err != nil || asked.Load() != 1 {
		t.Errorf("a caller's CheckRedirect: err %v, asked %d times; it should be used and not replaced", err, asked.Load())
	}
}

// The key, the PIN and the token go in the requests, so the base URL they go to
// must be https, except for a server on this machine.
func TestTVDBBaseURLMustBeHTTPS(t *testing.T) {
	for _, base := range []string{
		"http://api4.thetvdb.com/v4", "http://api.example.com/v4", "ftp://api.example.com/v4", "api4.thetvdb.com/v4", "//api4.thetvdb.com/v4",
		"http://", "http://[::1/v4", "http://127.0.0.1.evil.example/v4", "http://localhost.evil.example/v4", "http://192.168.1.10/v4", "http://0.0.0.0/v4",
	} {
		c := NewTVDBClient(TVDBOptions{Key: tvdbTestKey, PIN: tvdbTestPIN, BaseURL: base, Logger: discardLogger()})
		for name, err := range map[string]error{"Check": c.Check(t.Context()), "Search": func() error { _, err := c.Search(t.Context(), "x", 0); return err }()} {
			var e *TVDBError
			if !errors.As(err, &e) || e.Status != 0 || IsTransient(err) || IsUnauthorized(err) || !(strings.Contains(e.Message, "must use https") || strings.Contains(e.Message, "not valid")) {
				t.Errorf("%s with base %q: err = %v; want a final error about the base URL", name, base, err)
			}
			if err != nil && (strings.Contains(err.Error(), tvdbTestKey) || strings.Contains(err.Error(), tvdbTestPIN)) {
				t.Errorf("%s with base %q: the error shows a secret", name, base)
			}
		}
	}
	for _, base := range []string{
		"", DefaultTVDBBaseURL, "https://api.example.com/v4", "HTTPS://API.EXAMPLE.COM/v4",
		"http://127.0.0.1:8080/v4", "http://127.1.2.3/v4", "http://localhost:3000/v4", "http://LOCALHOST/v4", "http://[::1]:8080/v4",
	} {
		if c := NewTVDBClient(TVDBOptions{BaseURL: base}); c.baseErr != "" {
			t.Errorf("base %q was refused: %s", base, c.baseErr)
		}
	}
}

// Ids and years may come as numbers or as strings, and a map of translations as
// [] when empty: one of them of the other kind must not fail the whole list.
func TestTVDBLenientShapes(t *testing.T) {
	ctx := t.Context()
	f := newTVDBFake(t)
	f.set("/search", "query=x&type=series", fakeRoute{status: 200, body: `{"data":[
		{"tvdb_id":81189,"name":"A","year":2008,"translations":[],"overviews":null,"remote_ids":[{"id":603,"sourceName":"TheMovieDB.com"},{"id":"tt1","sourceName":"IMDB"}]},
		{"tvdb_id":"81190","name":"B","year":"2009","translations":{"por":"B2"},"overviews":{},"remote_ids":null},
		{"tvdb_id":null,"name":"C","year":null,"translations":null,"overviews":[]}
	],"links":{"next":null,"total_items":"3"}}`})
	f.set("/series/9/extended", tvdbExt, fakeRoute{status: 200, body: `{"data":{"id":"9","name":"S","year":2010,"remoteIds":[{"id":1396,"sourceName":"TheMovieDB.com"}],"seasons":[{"id":5,"number":1}]}}`})
	f.set("/people/7/extended", "", fakeRoute{status: 200, body: `{"data":{"id":"7","name":"P"}}`})
	f.set("/series/9/episodes/official/eng", params("page", "0"), fakeRoute{status: 200, body: `{"data":{"id":"9","name":"S","episodes":[{"id":"11","seriesId":"9","number":1},{"id":12,"seriesId":9,"number":2}]},"links":{"next":null,"total_items":"2"}}`})
	f.set("/updates", params("action", "update", "page", "0", "since", "1793593800", "type", "series"), fakeRoute{status: 200, body: `{"data":[{"recordId":"5","entityType":"series"},{"recordId":6,"recordType":"series"},{"recordId":""}],"links":{}}`})
	f.set("/search/remoteid/tt1", "", fakeRoute{status: 200, body: `{"data":[{"series":{"id":"9","name":"S"}},{"movie":{"id":"10","year":1999}}]}`})
	c := testTVDBClient(t, f)

	found, err := c.Search(ctx, "x", 0)
	if err != nil || len(found) != 3 {
		t.Fatalf("Search = %d results, %v", len(found), err)
	}
	if r := found[0]; r.SeriesID() != 81189 || r.TVDBID != "81189" || r.Year != "2008" || len(r.Translations) != 0 || len(r.Overviews) != 0 ||
		r.RemoteIDs.TMDBID() != 603 || r.RemoteIDs.IMDbID() != "tt1" {
		t.Errorf("numbers for ids: %+v", r)
	}
	if r := found[1]; r.SeriesID() != 81190 || r.Year != "2009" || r.Translations["por"] != "B2" {
		t.Errorf("strings and an object: %+v", r)
	}
	if r := found[2]; r.SeriesID() != 0 || r.TVDBID != "" || r.Year != "" || r.Name != "C" || len(r.Translations) != 0 {
		t.Errorf("nulls: %+v", r)
	}

	if s, err := c.SeriesExtended(ctx, 9); err != nil || s.ID != 9 || s.Year != "2010" || s.RemoteIDs.TMDBID() != 1396 || len(s.Seasons) != 1 || s.Seasons[0].ID != 5 {
		t.Errorf("SeriesExtended with an id as a string: %+v, %v", s, err)
	}
	if p, err := c.Person(ctx, 7); err != nil || p.ID != 7 || p.Name != "P" {
		t.Errorf("Person with an id as a string: %+v, %v", p, err)
	}
	eps, err := c.Episodes(ctx, 9, "official", "eng")
	if err != nil || len(eps.Episodes) != 2 || eps.Episodes[0].ID != 11 || eps.Episodes[0].SeriesID != 9 || eps.Episodes[1].ID != 12 || eps.Series.ID != 9 {
		t.Errorf("Episodes with ids as strings: %v, %v", shown(eps), err)
	}
	if ids, err := c.Updates(ctx, time.Unix(1793593800, 0)); err != nil || !slices.Equal(ids, []int64{5, 6}) {
		t.Errorf("Updates with a recordId as a string = %v, %v", ids, err)
	}
	m, err := c.SearchRemoteID(ctx, "tt1")
	if err != nil || len(m) != 2 || m[0].Series == nil || m[0].Series.ID != 9 || m[1].Movie == nil || m[1].Movie.ID != 10 || m[1].Movie.Year != "1999" {
		t.Errorf("SearchRemoteID with ids as strings: %+v, %v", m, err)
	}
}

// What TVDB says in an error is cut to 200 bytes between two runes, on one line,
// without control characters.
func TestTVDBMessageIsTidied(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"600 bytes of ascii", strings.Repeat("a", 600), strings.Repeat("a", 200)},
		{"exactly 200", strings.Repeat("a", 200), strings.Repeat("a", 200)},
		{"201", strings.Repeat("a", 201), strings.Repeat("a", 200)},
		{"two-byte runes: 100 fit", strings.Repeat("é", 150), strings.Repeat("é", 100)},
		{"three-byte runes: 66 fit, the 67th would be cut in two", strings.Repeat("€", 100), strings.Repeat("€", 66)},
		{"four-byte runes: 50 fit", strings.Repeat("\U0001F600", 80), strings.Repeat("\U0001F600", 50)},
		{"control characters and newlines", "line1\nline2\r\n\x00\x07\x1b[31mred" + string(rune(0x202e)) + string(rune(0x2028)) + " x", "line1 line2 [31mred x"},
		{"padding", "  \t spaced   out \n", "spaced out"},
		{"invalid utf-8", "ok\xff\xfebad", "okbad"},
		{"nothing", "", ""},
	} {
		got := tidyMessage(tc.in, maxTVDBMessage)
		if got != tc.want || !utf8.ValidString(got) {
			t.Errorf("%s: tidyMessage = %q, want %q", tc.name, got, tc.want)
		}
	}

	// And on the way through the client.
	for _, tc := range []struct{ name, msg, want string }{
		{"600 bytes", strings.Repeat("x", 600), strings.Repeat("x", 200)},
		{"multibyte", strings.Repeat("€", 100), strings.Repeat("€", 66)},
		{"control characters", "bad\nthing\x1b[0m\x00!", "bad thing [0m !"},
	} {
		body, _ := json.Marshal(map[string]string{"message": tc.msg})
		f := newTVDBFake(t)
		f.set("/series/81189/extended", tvdbExt, fakeRoute{status: 500, body: string(body)})
		_, err := testTVDBClient(t, f).SeriesExtended(t.Context(), 81189)
		var e *TVDBError
		if !errors.As(err, &e) || e.Message != tc.want || !IsTransient(err) {
			t.Errorf("%s: err = %#v, want the message %q", tc.name, err, tc.want)
		}
	}
}

// Only a record that says it is something else than a series, or has no id, is
// left out of the updates; the feed from the beginning of time is refused.
func TestTVDBUpdatesIgnoreWhatIsNotASeries(t *testing.T) {
	ctx := t.Context()
	const unix = "1793593800"
	f := newTVDBFake(t)
	f.set("/updates", params("action", "update", "page", "0", "since", unix, "type", "series"), fakeRoute{status: 200, body: `{"status":"success","data":[
		{"recordType":"episodes","recordId":999,"entityType":"episodes"},
		{"recordType":"","recordId":998,"entityType":"movies"},
		{"recordType":"movies","recordId":997,"entityType":"series"},
		{"recordType":"series","recordId":5},
		{"recordType":"","recordId":6,"entityType":"series"},
		{"recordId":7},
		{"recordType":"series","recordId":0},
		{"recordType":"series","recordId":-3},
		{"recordType":"series"},
		{"recordType":"","recordId":8,"entityType":"series"}
	],"links":{"next":null}}`})
	c := testTVDBClient(t, f)
	ids, err := c.Updates(ctx, time.Unix(1793593800, 0))
	if err != nil || !slices.Equal(ids, []int64{5, 6, 7, 8}) {
		t.Errorf("Updates = %v, %v; want [5 6 7 8]: no episodes, movies, zeros or negatives", ids, err)
	}

	before := len(f.requests())
	for _, since := range []time.Time{{}, time.Unix(0, 0), time.Unix(-5, 0), time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)} {
		if ids, err := c.Updates(ctx, since); err == nil || ids != nil || IsTransient(err) {
			t.Errorf("Updates(%v) = %v, %v; want an error", since, ids, err)
		}
	}
	if n := len(f.requests()) - before; n != 0 {
		t.Errorf("%d requests for a feed from the beginning of time", n)
	}
}

// The key and the PIN stay out of whatever prints or logs the options or the client.
func TestTVDBSecretsStayOutOfPrintedOptionsAndClients(t *testing.T) {
	logger, logs := capture()
	o := TVDBOptions{Key: tvdbTestKey, PIN: tvdbTestPIN, BaseURL: "http://127.0.0.1:9/v4", Language: "pt-BR", Logger: logger}
	c := NewTVDBClient(o)
	c.setToken(tvdbTokenHead + "printed")
	var printed []string
	for _, v := range []any{o, &o, c} {
		printed = append(printed, fmt.Sprint(v), fmt.Sprintf("%v", v), fmt.Sprintf("%+v", v), fmt.Sprintf("%#v", v), fmt.Sprintf("%s", v), fmt.Sprintf("%q", v))
	}
	type holder struct {
		Options TVDBOptions
		Client  *TVDBClient
	}
	printed = append(printed, fmt.Sprintf("%+v", holder{o, c}), fmt.Sprintf("%#v", holder{o, c}))
	logger.Info("configured", "options", o, "client", c)
	logger.Info("configured", slog.Any("options", &o))
	printed = append(printed, logs.String())

	all := strings.Join(printed, "\n")
	for _, secret := range []string{tvdbTestKey, tvdbTestPIN, tvdbTokenHead + "printed", "printed"} {
		if strings.Contains(all, secret) {
			t.Errorf("%q is in what was printed: %s", secret, lineWith(all, secret))
		}
	}
	// Positive controls: it printed, it says what is set, and the rest is there.
	if !strings.Contains(fmt.Sprint(o), `Key: "***"`) || !strings.Contains(fmt.Sprint(o), `PIN: "***"`) || !strings.Contains(fmt.Sprint(o), "pt-BR") ||
		!strings.Contains(fmt.Sprint(TVDBOptions{}), `Key: ""`) {
		t.Errorf("options print as %q", fmt.Sprint(o))
	}
	if !strings.Contains(logs.String(), "key=***") || !strings.Contains(logs.String(), "http://127.0.0.1:9/v4") {
		t.Errorf("the log shows %q", logs.String())
	}
	if !strings.Contains(fmt.Sprint(c), "127.0.0.1") {
		t.Errorf("the client prints as %q", fmt.Sprint(c))
	}
}

// A nil *TVDBError in an error is a nil pointer, not a panic.
func TestTVDBNilErrorIsHarmless(t *testing.T) {
	var nilErr *TVDBError
	var e *Error
	if errors.As(error(nilErr), &e) {
		t.Errorf("a nil *TVDBError was taken for an *Error: %+v", e)
	}
	if IsTransient(error(nilErr)) || IsNotFound(error(nilErr)) || IsUnauthorized(error(nilErr)) {
		t.Error("a nil *TVDBError was classified")
	}
	if nilErr.Error() == "" || errors.Unwrap(error(nilErr)) != nil {
		t.Errorf("Error() = %q", nilErr.Error())
	}
	// Positive control: a real one is taken for an *Error.
	real := &TVDBError{Op: "GET /x", Status: 503, Transient: true}
	if !errors.As(error(real), &e) || e.Status != 503 {
		t.Error("a real *TVDBError was not seen as an *Error")
	}
}

func TestTVDBClientDefaults(t *testing.T) {
	c := NewTVDBClient(TVDBOptions{})
	if c.bucket.rate != DefaultTVDBRate || c.bucket.burst != DefaultTVDBRate || DefaultTVDBRate != 10 {
		t.Errorf("rate %v burst %v, want %d and %d", c.bucket.rate, c.bucket.burst, DefaultTVDBRate, DefaultTVDBRate)
	}
	if cap(c.slots) != DefaultMaxInFlight || c.timeout != DefaultTimeout || c.maxWait != DefaultMaxWait || c.lang != "en-US" || c.base != DefaultTVDBBaseURL {
		t.Errorf("slots %d timeout %v maxWait %v lang %q base %q", cap(c.slots), c.timeout, c.maxWait, c.lang, c.base)
	}
	if DefaultTVDBBaseURL != "https://api4.thetvdb.com/v4" || TVDBImageHost != "artworks.thetvdb.com" || tvdbTokenLife != 25*24*time.Hour {
		t.Error("the documented defaults changed")
	}
	// What is asked for is what is used.
	c = NewTVDBClient(TVDBOptions{Key: " k ", PIN: " p ", BaseURL: "http://x.test/v4/", Rate: 5, MaxInFlight: 2, Timeout: time.Second, MaxWait: time.Minute, Language: "fr-FR"})
	if c.bucket.rate != 5 || c.bucket.burst != 5 || cap(c.slots) != 2 || c.timeout != time.Second || c.maxWait != time.Minute || c.lang != "fr-FR" ||
		c.key != "k" || c.pin != "p" || c.base != "http://x.test/v4" {
		t.Errorf("explicit options: rate %v slots %d timeout %v maxWait %v lang %q key %q pin %q base %q",
			c.bucket.rate, cap(c.slots), c.timeout, c.maxWait, c.lang, c.key, c.pin, c.base)
	}
}
