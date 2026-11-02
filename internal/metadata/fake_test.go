package metadata

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// The fake TMDB: an httptest server that answers from the recorded fixtures in
// testdata/tmdb. No test talks to the network.

// searchKeys are the query parameters that tell one search from another.
var searchKeys = []string{"query", "year", "first_air_date_year", "external_source", "page", "start_date", "end_date"}

// params builds the canonical key of a request's parameters from name, value
// pairs: "query=The Thing&year=1982".
func params(kv ...string) string {
	pairs := make([]string, 0, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		pairs = append(pairs, kv[i]+"="+kv[i+1])
	}
	sort.Strings(pairs)
	return strings.Join(pairs, "&")
}

func canon(q url.Values) string {
	var kv []string
	for _, k := range searchKeys {
		if v := q.Get(k); v != "" {
			kv = append(kv, k, v)
		}
	}
	return params(kv...)
}

type fakeReq struct {
	Path   string
	Params string // canonical, see params
	Query  url.Values
	Header http.Header
}

func (r fakeReq) String() string {
	if r.Params == "" {
		return r.Path
	}
	return r.Path + "?" + r.Params
}

type fakeRoute struct {
	status  int
	file    string // fixture
	body    string // used when file is empty
	headers map[string]string
	handler http.HandlerFunc // overrides everything
}

type fakeTMDB struct {
	t   *testing.T
	srv *httptest.Server

	mu     sync.Mutex
	routes map[string]fakeRoute
	reqs   []fakeReq
}

func newFakeTMDB(t *testing.T) *fakeTMDB {
	t.Helper()
	f := &fakeTMDB{t: t, routes: map[string]fakeRoute{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTMDB) URL() string { return f.srv.URL + "/3" }

// on answers 200 with a fixture for the path and canonical params.
func (f *fakeTMDB) on(path, params, file string) {
	f.set(path, params, fakeRoute{status: 200, file: file})
}

// fail answers with a status, a fixture (or empty) and headers.
func (f *fakeTMDB) fail(path, params string, status int, file string, headers map[string]string) {
	f.set(path, params, fakeRoute{status: status, file: file, headers: headers})
}

func (f *fakeTMDB) handle(path, params string, h http.HandlerFunc) {
	f.set(path, params, fakeRoute{handler: h})
}

func (f *fakeTMDB) set(path, params string, r fakeRoute) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[path+"|"+params] = r
}

func (f *fakeTMDB) serve(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/3")
	req := fakeReq{Path: path, Params: canon(r.URL.Query()), Query: r.URL.Query(), Header: r.Header.Clone()}
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	rt, ok := f.routes[path+"|"+req.Params]
	f.mu.Unlock()

	switch {
	case ok && rt.handler != nil:
		rt.handler(w, r)
		return
	case ok:
		for k, v := range rt.headers {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json;charset=utf-8")
		w.WriteHeader(rt.status)
		if rt.file != "" {
			w.Write(f.fixture(rt.file))
		} else {
			w.Write([]byte(rt.body))
		}
	case strings.HasPrefix(path, "/search/"):
		// A search nobody prepared finds nothing, as TMDB would.
		w.Header().Set("Content-Type", "application/json;charset=utf-8")
		w.Write(f.fixture("search_empty.json"))
	default:
		f.t.Errorf("fake TMDB: unexpected request %s", req)
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func (f *fakeTMDB) fixture(name string) []byte {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "tmdb", name))
	if err != nil {
		f.t.Fatalf("fixture %s: %v", name, err)
	}
	return data
}

// requests returns what the server has been asked, in order.
func (f *fakeTMDB) requests() []fakeReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reqs)
}

// asked returns the requests as "path?params" strings.
func (f *fakeTMDB) asked() []string {
	var out []string
	for _, r := range f.requests() {
		out = append(out, r.String())
	}
	return out
}

func (f *fakeTMDB) count(path string) int {
	n := 0
	for _, r := range f.requests() {
		if r.Path == path {
			n++
		}
	}
	return n
}

// testKey is a v3-shaped key; testToken a v4-shaped token.
const (
	testKey   = "0123456789abcdef0123456789abcdef"
	testToken = "eyJhbGciOiJIUzI1NiJ9.eyJhdWQiOiJzZWNyZXQifQ.c2lnbmF0dXJlLXNlY3JldA"
)

// testClient returns a client for the fake with fast settings and a silent logger.
func testClient(t *testing.T, f *fakeTMDB, mod ...func(*Options)) *Client {
	t.Helper()
	o := Options{Key: testKey, BaseURL: f.URL(), ImageBaseURL: f.srv.URL + "/img", Rate: 1000,
		Timeout: 5 * time.Second, Logger: discardLogger()}
	for _, m := range mod {
		m(&o)
	}
	return NewClient(o)
}

// fakeClock is a clock that only moves when something sleeps on it.
type fakeClock struct {
	mu     sync.Mutex
	t      time.Time
	sleeps []time.Duration
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)} }

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sleeps = append(c.sleeps, d)
	c.t = c.t.Add(d)
	return nil
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func (c *fakeClock) slept() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.sleeps)
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// useClock makes the client (and its token bucket) run on the fake clock.
func useClock(c *Client, clk *fakeClock, rate float64, burst int) {
	c.now, c.sleep = clk.now, clk.sleep
	c.bucket = newBucket(rate, burst, clk.now)
}
