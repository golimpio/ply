package api

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const tlsHost = "ply.lan.test"

// tlsEnv is a server with an HTTPS listener whose certificate is for tlsHost, which DNS points at
// 192.168.4.196, the address of ours on the living room's network; the study's TV is on another
// network of ours, 192.168.139.0/24, where the name does not lead.
func tlsEnv(t *testing.T) (*env, *atomic.Int32) {
	t.Helper()
	e := newEnv(t, "")
	e.withAirPlayStreams()
	e.srv.TLSHost = tlsHost
	var lookups atomic.Int32
	e.srv.hostIPs = &hostCache{resolve: func(_ context.Context, host string) ([]net.IP, error) {
		lookups.Add(1)
		if host != tlsHost {
			return nil, fmt.Errorf("no such host %s", host)
		}
		return []net.IP{ip("192.168.4.196")}, nil
	}}
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
	return e, &lookups
}

// tlsRequest is a request that came over the HTTPS listener at local, for host.
func (e *env) tlsRequest(method, path, host, local, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	addr, err := net.ResolveTCPAddr("tcp", local)
	if err != nil {
		e.t.Fatal(err)
	}
	var b io.Reader
	if body != "" {
		b = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, b)
	req.Host, req.TLS = host, &tls.ConnectionState{ServerName: hostOnly(host)}
	req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, addr))
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	return rec
}

// With HTTPS a receiver's base names the certificate's host, which it can verify, and only on the
// network that name leads to.
func TestHTTPSReceiversGetTheCertificatesName(t *testing.T) {
	e, lookups := tlsEnv(t)
	rec := e.tlsRequest(http.MethodGet, "/api/receivers", tlsHost+":8420", "192.168.4.196:8420", "")
	wantStatus(t, rec, 200)
	bases := map[string]string{}
	for _, r := range decode[[]Receiver](t, rec) {
		bases[r.ID] = r.Base
	}
	if want := "https://" + tlsHost + ":8420"; bases["living"] != want {
		t.Errorf("living room base %q, want %q", bases["living"], want)
	}
	if bases["study"] != "" {
		t.Errorf("study base %q, want none: the name does not lead to our address on its network", bases["study"])
	}
	if n := lookups.Load(); n != 1 {
		t.Errorf("%d lookups of the name for two receivers, want 1 (kept a minute)", n)
	}

	play := func(body string) *httptest.ResponseRecorder {
		return e.tlsRequest(http.MethodPost, fmt.Sprintf("/api/items/%d/playback", e.mp4), tlsHost+":8420", "192.168.4.196:8420", body)
	}
	rec = play(`{"receiver":"living"}`)
	wantStatus(t, rec, 200)
	if pb := decode[Playback](t, rec); !strings.HasPrefix(pb.URL, "https://"+tlsHost+":8420/api/") {
		t.Errorf("url %q, want it on the certificate's name", pb.URL)
	}
	rec = play(`{"receiver":"study"}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "does not lead") {
		t.Errorf("a play for the study: %d %s, want 422: no URL there that the TV can verify", rec.Code, rec.Body.String())
	}
}

// A client that goes away while the name is looked up does not leave "nowhere" for the others: the
// lookup is not cancelled with its request.
func TestHostLookupOutlivesItsRequest(t *testing.T) {
	c := &hostCache{resolve: func(ctx context.Context, host string) ([]net.IP, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return []net.IP{ip("192.168.4.196")}, nil
	}}
	if got := c.lookup(context.Background(), tlsHost); len(got) != 1 { // control: the resolver answers
		t.Fatalf("lookup = %v", got)
	}
	c.until = time.Time{} // looked up again
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	if got := c.lookup(gone, tlsHost); len(got) != 1 {
		t.Errorf("lookup for a request that went away = %v, want the address", got)
	}
}

// Under HTTPS, a receiver on none of our networks is refused as one is without it; the certificate's
// name is blamed only where an address of ours is on the receiver's network.
func TestHTTPSRefusalSaysWhy(t *testing.T) {
	e, _ := tlsEnv(t)
	e.srv.Receivers = append(e.srv.Receivers.(fakeReceivers), appleTV("far", "Far TV", "AppleTV6,2", "10.9.8.7"))
	play := func(id string) string {
		rec := e.tlsRequest(http.MethodPost, fmt.Sprintf("/api/items/%d/playback", e.mp4), tlsHost+":8420", "192.168.4.196:8420", `{"receiver":"`+id+`"}`)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d, want 422", id, rec.Code)
		}
		return rec.Body.String()
	}
	if b := play("study"); !strings.Contains(b, "does not lead") {
		t.Errorf("study: %s, want the name blamed", b)
	}
	if b := play("far"); !strings.Contains(b, "no address of this server") {
		t.Errorf("far: %s, want no address of ours on its network", b)
	}
}

// An AirPlay play from a page opened by address (its certificate warning accepted) is routed to
// the certificate's name: the page's own URLs are https://<address>, which the TV cannot verify.
// A page opened by the name keeps its relative URLs.
func TestHTTPSAirPlayPlayFromAnAddressIsRoutedToTheName(t *testing.T) {
	e, _ := tlsEnv(t)
	e.srv.Receivers = fakeReceivers{appleTV("living", "Living Room Apple TV", "AppleTV3,2", "192.168.4.39")}
	play := func(host string) Playback {
		t.Helper()
		rec := e.tlsRequest(http.MethodPost, fmt.Sprintf("/api/items/%d/playback", e.mp4), host, "192.168.4.196:8420", `{"profile":"airplay"}`)
		wantStatus(t, rec, 200)
		pb := decode[Playback](t, rec)
		// One transcode at a time: this one is stopped for the next.
		wantStatus(t, e.tlsRequest(http.MethodDelete, "/api/sessions/"+pb.SessionID, host, "192.168.4.196:8420", ""), 204)
		return pb
	}
	if pb := play(tlsHost + ":8420"); !strings.HasPrefix(pb.URL, "/api/sessions/") {
		t.Errorf("page by the name: url %q, want it relative (control)", pb.URL)
	}
	if pb := play("192.168.4.196:8420"); !strings.HasPrefix(pb.URL, "https://"+tlsHost+":8420/api/sessions/") {
		t.Errorf("page by the address: url %q, want the certificate's name", pb.URL)
	}
}
