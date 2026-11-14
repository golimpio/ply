package api

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"
)

// stopServer is a server whose Stop records what it was asked.
func stopServer(token string) (*Server, *[]bool) {
	var asked []bool
	return New(Options{Token: token, ControlKey: "k3y", Stop: func(force bool) { asked = append(asked, force) }}), &asked
}

func postStop(s *Server, target string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, target, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func TestStopNeedsTheControlKey(t *testing.T) {
	for _, token := range []string{"", "tok"} {
		s, asked := stopServer(token)
		for name, hdr := range map[string]map[string]string{
			"no key":                       nil,
			"wrong key":                    {ControlHeader: "nope"},
			"the token instead of the key": {"Authorization": "Bearer tok"},
		} {
			if rec := postStop(s, stopPath, hdr); rec.Code != http.StatusForbidden {
				t.Errorf("token %q, %s: %d %s, want 403", token, name, rec.Code, rec.Body)
			}
		}
		if len(*asked) != 0 {
			t.Fatalf("token %q: Stop called %v without the key", token, *asked)
		}
		// The key is the credential, with or without -token.
		rec := postStop(s, stopPath, map[string]string{ControlHeader: "k3y"})
		if rec.Code != http.StatusAccepted || len(*asked) != 1 || (*asked)[0] {
			t.Fatalf("token %q, the key: %d %s, Stop asked %v; want 202 and one nice stop", token, rec.Code, rec.Body, *asked)
		}
	}
}

func TestStopForce(t *testing.T) {
	s, asked := stopServer("")
	key := map[string]string{ControlHeader: "k3y"}
	if rec := postStop(s, stopPath+"?force=true", key); rec.Code != http.StatusAccepted {
		t.Fatalf("force: %d %s", rec.Code, rec.Body)
	}
	if rec := postStop(s, stopPath+"?force=maybe", key); rec.Code != http.StatusBadRequest {
		t.Errorf("force=maybe: %d %s, want 400", rec.Code, rec.Body)
	}
	if len(*asked) != 1 || !(*asked)[0] {
		t.Fatalf("Stop asked %v, want one forced stop", *asked)
	}
}

func TestStopIsPrivate(t *testing.T) {
	s, asked := stopServer("")
	// Positive control: the same request without the browser's headers goes through.
	if rec := postStop(s, stopPath, map[string]string{ControlHeader: "k3y"}); rec.Code != http.StatusAccepted {
		t.Fatalf("own request: %d %s", rec.Code, rec.Body)
	}
	for name, hdr := range foreignRequests {
		hdr = maps.Clone(hdr)
		hdr[ControlHeader] = "k3y"
		rec := postStop(s, stopPath, hdr)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d %s, want 403", name, rec.Code, rec.Body)
		}
		noCORS(t, rec, name)
	}
	if len(*asked) != 1 {
		t.Fatalf("Stop asked %v, want only the own request", *asked)
	}
}

func TestStopUnavailable(t *testing.T) {
	s := New(Options{})
	if rec := postStop(s, stopPath, map[string]string{ControlHeader: ""}); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no Stop: %d %s, want 503", rec.Code, rec.Body)
	}
}
