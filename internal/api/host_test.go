package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ply/internal/config"
)

func TestKnownHost(t *testing.T) {
	cases := []struct {
		host  string
		extra []string
		want  bool
	}{
		{"192.168.1.20:8420", nil, true},
		{"[fe80::1]:8420", nil, true},
		{"127.0.0.1", nil, true},
		{"localhost:8420", nil, true},
		{"app.localhost", nil, true},
		{"nas", nil, true},             // single label: LAN-only
		{"MyMac.local.", nil, true},    // mDNS, case and trailing dot
		{"media.home.arpa", nil, true}, // RFC 8375
		{"example.com", nil, true},     // RFC 2606 (httptest's default Host)
		{"build-box.lan.example", nil, true},
		{"self.corp.net", nil, true},  // the machine's own hostname (see below)
		{"", nil, true},               // HTTP/1.0, no Host
		{"evil.com:8420", nil, false}, // DNS rebinding
		{"192.168.1.20.evil.com", nil, false},
		{"localhost.evil.com", nil, false},
		{"evil.com", []string{"media.example.org"}, false},
		{"Media.Example.org:443", []string{"media.example.org"}, true},
		{"tv.example.org", []string{".example.org"}, true},
		{"example.org.evil.com", []string{".example.org"}, false},
		{"notexample.org", []string{".example.org"}, false},
	}
	for _, c := range cases {
		if got := knownHost(c.host, "self.corp.net", c.extra); got != c.want {
			t.Errorf("knownHost(%q, %v) = %v, want %v", c.host, c.extra, got, c.want)
		}
	}
}

// A page on a rebound name reaches the server with that name as Host: it must
// get nothing, read or write, even with no token configured. The same request
// by IP is the positive control.
func TestRebindingIsRefused(t *testing.T) {
	e := newEnv(t, "")
	for _, c := range []struct {
		method, host, origin string
		want                 int
	}{
		{"GET", "192.168.1.20:8420", "", http.StatusOK},
		{"GET", "evil.com:8420", "", http.StatusForbidden},
		{"GET", "evil.com:8420", "http://evil.com:8420", http.StatusForbidden},
		{"POST", "evil.com:8420", "http://evil.com:8420", http.StatusForbidden},
		{"POST", "192.168.1.20:8420", "http://192.168.1.20:8420", http.StatusAccepted},
	} {
		path := "/api/items"
		if c.method == "POST" {
			path = "/api/scan"
		}
		req := httptest.NewRequest(c.method, path, nil)
		req.Host = c.host
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		rec := httptest.NewRecorder()
		e.srv.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s %s Host=%s Origin=%q = %d, want %d: %s", c.method, path, c.host, c.origin, rec.Code, c.want, rec.Body)
		}
		if c.want == http.StatusForbidden && !strings.Contains(rec.Body.String(), "-allow-host evil.com") {
			t.Errorf("refusal should name the flag and host: %s", rec.Body)
		}
	}
}

func TestAllowedHostsOption(t *testing.T) {
	e := newEnv(t, "")
	srv := New(Options{Store: e.store, Scanner: e.scanner, AllowedHosts: []string{"ply.example.org"}})
	for host, want := range map[string]int{"ply.example.org": http.StatusOK, "other.example.org": http.StatusForbidden} {
		req := httptest.NewRequest("GET", "/api/items", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("Host %s = %d, want %d", host, rec.Code, want)
		}
	}
}

func TestAllowHostFlagAndEnv(t *testing.T) {
	t.Setenv(config.AllowHostsEnv, " a.example.org, ,.b.example.org ")
	c, err := config.Parse([]string{"-media", t.TempDir()}, new(strings.Builder))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.AllowHosts, "|") != "a.example.org|.b.example.org" {
		t.Errorf("env AllowHosts = %q", c.AllowHosts)
	}
	c, err = config.Parse([]string{"-media", t.TempDir(), "-allow-host", "x.example.org", "-allow-host", "y.example.org"}, new(strings.Builder))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.AllowHosts, "|") != "x.example.org|y.example.org" {
		t.Errorf("flag AllowHosts = %q (the flag must win over the env)", c.AllowHosts)
	}
	if _, err := config.Parse([]string{"-media", t.TempDir(), "-allow-host", " "}, new(strings.Builder)); err == nil {
		t.Error("an empty -allow-host should be refused")
	}
}
