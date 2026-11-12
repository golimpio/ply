package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ply/internal/discovery"
)

// fakeReceivers is the browse a test gives the API.
type fakeReceivers []discovery.Receiver

func (f fakeReceivers) Receivers() []discovery.Receiver { return f }

func ip(s string) net.IP { return net.ParseIP(s) }

// appleTV is a receiver as internal/discovery reports one.
func appleTV(id, name, model, addr string) discovery.Receiver {
	return discovery.Receiver{ID: id, Name: name, Model: model, Addrs: []net.IP{ip(addr)}, Port: 7000}
}

// playFrom asks to play item id with the client connected to local, as a browser on that
// address would: an AirPlay play compares the page's address with the TVs'.
func (e *env) playFrom(local string, id int64, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	addr, err := net.ResolveTCPAddr("tcp", local)
	if err != nil {
		e.t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/items/%d/playback", id), strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, addr))
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	return rec
}

func TestReceiversListsTheBaseOnTheTargetsNetwork(t *testing.T) {
	e := newEnv(t, "")
	e.srv.Receivers = fakeReceivers{appleTV("atv", "Living Room Apple TV", "AppleTV3,2", "192.168.4.39")}
	e.srv.localIPFor = func(remote net.IP) (net.IP, bool) { return ip("192.168.4.196"), true }

	rec := e.get("/api/receivers")
	wantStatus(t, rec, 200)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want none: the household's screens are private", got)
	}
	got := decode[[]Receiver](t, rec)
	if len(got) != 1 {
		t.Fatalf("receivers = %+v, want the Apple TV", got)
	}
	want := Receiver{ID: "atv", Name: "Living Room Apple TV", Model: "AppleTV3,2", Kind: "airplay",
		Address: "192.168.4.39", Port: 7000, Base: "http://192.168.4.196:80"}
	if got[0] != want {
		t.Errorf("receiver = %+v, want %+v", got[0], want)
	}
}

func TestReceiversSaysWhenThisServerIsNotOnItsNetwork(t *testing.T) {
	e := newEnv(t, "")
	e.srv.Receivers = fakeReceivers{appleTV("atv", "Living Room Apple TV", "AppleTV3,2", "192.168.4.39")}
	e.srv.localIPFor = func(net.IP) (net.IP, bool) { return nil, false }

	got := decode[[]Receiver](t, e.get("/api/receivers"))
	if len(got) != 1 || got[0].Address != "192.168.4.39" {
		t.Fatalf("receivers = %+v, want the Apple TV with its own address", got)
	}
	if got[0].Base != "" {
		t.Errorf("base = %q, want none: no address of this server is on the TV's network", got[0].Base)
	}
}

func TestRoutingOnlyNamesAnAddressTheServerListensOn(t *testing.T) {
	e := newEnv(t, "")
	e.srv.Receivers = fakeReceivers{appleTV("atv", "Living Room Apple TV", "AppleTV3,2", "192.168.4.39")}
	e.srv.localIPFor = func(remote net.IP) (net.IP, bool) {
		if remote.Equal(ip("192.168.4.39")) {
			return ip("192.168.4.196"), true
		}
		return nil, false
	}
	cases := []struct {
		name string
		addr net.IP
		base string
	}{
		{"wildcard bind serves every LAN address", net.IPv4zero, "http://192.168.4.196:80"},
		{"the TV's network address", ip("192.168.4.196"), "http://192.168.4.196:80"},
		{"loopback only", ip("127.0.0.1"), ""}, // nothing listens on the LAN address
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e.srv.ListenAddr = &net.TCPAddr{IP: c.addr, Port: 8420}
			got := decode[[]Receiver](t, e.get("/api/receivers"))
			if len(got) != 1 || got[0].Base != c.base {
				t.Errorf("receiver = %+v, want base %q", got, c.base)
			}
			if c.base != "" {
				return
			}
			rec := e.post(fmt.Sprintf("/api/items/%d/playback", e.mp4), `{"receiver":"atv"}`, nil)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("playback = %d %q, want 422: this server serves no address the TV reaches", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestPlaybackForAReceiverUsesItsNetwork(t *testing.T) {
	e := newEnv(t, "")
	e.srv.Receivers = fakeReceivers{appleTV("atv", "Living Room Apple TV", "AppleTV3,2", "192.168.4.39")}
	e.srv.localIPFor = func(remote net.IP) (net.IP, bool) {
		if remote.Equal(ip("192.168.4.39")) {
			return ip("192.168.4.196"), true
		}
		return nil, false
	}

	t.Run("named receiver", func(t *testing.T) {
		rec := e.post(fmt.Sprintf("/api/items/%d/playback", e.mp4), `{"receiver":"atv"}`, nil)
		wantStatus(t, rec, 200)
		pb := decode[Playback](t, rec)
		if want := "http://192.168.4.196:80/api/items/"; !strings.HasPrefix(pb.URL, want) {
			t.Errorf("url = %q, want the TV's network (%s...)", pb.URL, want)
		}
	})
	t.Run("no receiver", func(t *testing.T) {
		rec := e.post(fmt.Sprintf("/api/items/%d/playback", e.mp4), `{}`, nil)
		wantStatus(t, rec, 200)
		pb := decode[Playback](t, rec)
		if !strings.HasPrefix(pb.URL, "/api/items/") {
			t.Errorf("url = %q, want a URL relative to the page's own address", pb.URL)
		}
	})
}

func TestPlaybackRejectsAReceiverItCannotReach(t *testing.T) {
	e := newEnv(t, "")
	e.srv.Receivers = fakeReceivers{
		appleTV("atv", "Living Room Apple TV", "AppleTV3,2", "192.168.4.39"),
		appleTV("far", "Study TV", "AppleTV6,2", "10.9.8.7"),
	}
	e.srv.localIPFor = func(remote net.IP) (net.IP, bool) {
		if remote.Equal(ip("192.168.4.39")) {
			return ip("192.168.4.196"), true
		}
		return nil, false
	}

	rec := e.post(fmt.Sprintf("/api/items/%d/playback", e.mp4), `{"receiver":"far"}`, nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d %q, want 422 for a TV on a network this server is not on", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Study TV") {
		t.Errorf("error = %q, want the TV's name so the person knows which one", rec.Body.String())
	}

	for _, body := range []string{`{"receiver":"nope"}`, `{"receiver":"  "}`} {
		rec = e.post(fmt.Sprintf("/api/items/%d/playback", e.mp4), body, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d %q, want 400 for a receiver that is not there", body, rec.Code, rec.Body.String())
		}
	}
}

func TestAirPlayPlayIsRoutedToTheAppleTVsNetwork(t *testing.T) {
	e := newEnv(t, "")
	e.withAirPlayStreams()
	e.srv.Receivers = fakeReceivers{
		appleTV("atv", "Living Room Apple TV", "AppleTV3,2", "192.168.4.39"),
		// A Mac that accepts AirPlay must not decide the route of a play meant for a TV.
		appleTV("mac", "Studio Mac", "MacBookPro18,3", "10.0.0.20"),
	}
	e.srv.localIPFor = func(remote net.IP) (net.IP, bool) {
		switch remote.String() {
		case "192.168.4.39":
			return ip("192.168.4.196"), true
		case "10.0.0.20":
			return ip("10.0.0.5"), true
		}
		return nil, false
	}

	// The page was opened on localhost: an address the TV cannot fetch from.
	rec := e.playFrom("127.0.0.1:8420", e.mp4, `{"profile":"airplay"}`)
	wantStatus(t, rec, 200)
	pb := decode[Playback](t, rec)
	if want := "http://192.168.4.196:8420/api/sessions/"; !strings.HasPrefix(pb.URL, want) {
		t.Errorf("url = %q, want the Apple TV's network (%s...)", pb.URL, want)
	}
	if pb.SessionID == "" {
		t.Errorf("playback = %+v, want a session", pb)
	}
}

func TestAirPlayPlayKeepsThePagesAddressWhenItIsAlreadyRight(t *testing.T) {
	e := newEnv(t, "")
	e.withAirPlayStreams()
	e.srv.Receivers = fakeReceivers{appleTV("atv", "Living Room Apple TV", "AppleTV3,2", "192.168.4.39")}
	e.srv.localIPFor = func(remote net.IP) (net.IP, bool) { return ip("192.168.4.196"), true }

	rec := e.playFrom("192.168.4.196:8420", e.mp4, `{"profile":"airplay"}`)
	wantStatus(t, rec, 200)
	pb := decode[Playback](t, rec)
	if !strings.HasPrefix(pb.URL, "/api/sessions/") {
		t.Errorf("url = %q, want a URL relative to the page, which is already on the TV's network", pb.URL)
	}
}

func TestAirPlayPlayIsNotRoutedWhenTheTVsDisagree(t *testing.T) {
	e := newEnv(t, "")
	e.withAirPlayStreams()
	e.srv.Receivers = fakeReceivers{
		appleTV("living", "Living Room Apple TV", "AppleTV3,2", "192.168.4.39"),
		appleTV("study", "Study TV", "AppleTV6,2", "192.168.139.9"),
	}
	e.srv.localIPFor = func(remote net.IP) (net.IP, bool) {
		switch remote.String() {
		case "192.168.4.39":
			return ip("192.168.4.196"), true
		case "192.168.139.9":
			return ip("192.168.139.3"), true
		}
		return nil, false
	}

	rec := e.playFrom("127.0.0.1:8420", e.mp4, `{"profile":"airplay"}`)
	wantStatus(t, rec, 200)
	pb := decode[Playback](t, rec)
	if !strings.HasPrefix(pb.URL, "/api/sessions/") {
		t.Errorf("url = %q, want the page's address: only the client can choose between two TVs", pb.URL)
	}
}

func TestPlaybackWithoutReceiversIsUnchanged(t *testing.T) {
	e := newEnv(t, "")
	rec := e.post(fmt.Sprintf("/api/items/%d/playback", e.mp4), `{}`, nil)
	wantStatus(t, rec, 200)
	if pb := decode[Playback](t, rec); !strings.HasPrefix(pb.URL, "/api/items/") {
		t.Errorf("url = %q, want the page's own address when the browse is off", pb.URL)
	}
}
