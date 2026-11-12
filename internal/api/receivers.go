package api

// The AirPlay targets on the LAN (docs/AIRPLAY.md, "Network setup"; docs/M11-CLIENTS.md,
// "AirPlay"):
//
//	GET /api/receivers   the targets found on the LAN, and this server's base URL on each
//	                     one's own network
//
// A play can name one of them (POST /api/items/{id}/playback {"receiver": id}), and the URLs
// it gets are then on the address that target can reach. An AirPlay play that names none is
// routed to the Apple TVs' network when there is only one and this page did not come in on
// it: a page opened on localhost, or on a bridge or VPN address, then still gives the TV a
// URL it can fetch.
//
// The list is private, like the devices: it is this household's screens.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"ply/internal/discovery"
	"ply/internal/playback"
)

// ReceiverFinder follows the AirPlay targets on the LAN (internal/discovery).
type ReceiverFinder interface {
	Receivers() []discovery.Receiver
}

// Receiver is one entry of GET /api/receivers.
type Receiver struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Model string `json:"model,omitempty"` // "AppleTV3,2", or a Mac's model
	Kind  string `json:"kind"`            // "airplay"
	// Address and Port are where the target is, as the browse found it.
	Address string `json:"address,omitempty"`
	Port    int    `json:"port,omitempty"`
	// Base is this server's URL on the target's own network, the base of the URLs it
	// fetches. Empty when no address of this server is on that network.
	Base string `json:"base,omitempty"`
}

// errUnknownReceiver says a play named a receiver that is not on the LAN now.
var errUnknownReceiver = errors.New("unknown receiver")

// receivers are the AirPlay targets known now; nil when the browse is off.
func (s *Server) receivers() []discovery.Receiver {
	if s.Receivers == nil {
		return nil
	}
	return s.Receivers.Receivers()
}

// handleReceivers is GET /api/receivers.
func (s *Server) handleReceivers(w http.ResponseWriter, r *http.Request) {
	list := s.receivers()
	out := make([]Receiver, 0, len(list))
	for _, rec := range list {
		out = append(out, s.receiverFor(r, rec))
	}
	writeJSON(w, http.StatusOK, out)
}

// receiverFor is what a client needs of one target: where it is, and the base URL of this
// server that it can reach.
func (s *Server) receiverFor(r *http.Request, rec discovery.Receiver) Receiver {
	info := Receiver{ID: rec.ID, Name: rec.Name, Model: rec.Model, Kind: "airplay", Port: rec.Port}
	for _, a := range rec.Addrs {
		if info.Address == "" {
			info.Address = a.String()
		}
		if base := s.mediaBaseFor(r, a); base != "" {
			info.Address, info.Base = a.String(), base
			break
		}
	}
	return info
}

// playbackBase is the base URL the media URLs of a play are built on. A client that named a
// receiver gets that receiver's network; an AirPlay play gets the Apple TVs' network when they
// share one that this page did not come in on; anything else keeps the page's own address,
// which is the empty base (the URLs stay relative).
func (s *Server) playbackBase(r *http.Request, p playback.Profile, receiverID string) (string, error) {
	if receiverID != "" {
		for _, rec := range s.receivers() {
			if rec.ID != receiverID {
				continue
			}
			ours := false // an address of ours is on its network: only the name leads elsewhere
			for _, a := range rec.Addrs {
				if base := s.mediaBaseFor(r, a); base != "" {
					return base, nil
				}
				ours = ours || s.ourAddrOn(a) != nil
			}
			if ours && s.TLSHost != "" {
				return "", fmt.Errorf("%s does not lead to this server's address on the network of %s", s.TLSHost, rec.Name)
			}
			return "", fmt.Errorf("no address of this server is on the network of %s", rec.Name)
		}
		return "", fmt.Errorf("%w: %q", errUnknownReceiver, receiverID)
	}
	if p.Name == "airplay" {
		return s.airplayBase(r), nil
	}
	return "", nil
}

// airplayBase is the address of ours an AirPlay play should send the TV to, when it named no
// receiver: the Apple TVs' network, if they are all on one and this page did not come in on
// it (a page opened on localhost, or on a bridge or VPN address, cannot give the TV a URL it
// reaches). "" leaves the page's address: the usual case, and the only one when the page
// already is on the TVs' network or the TVs disagree.
func (s *Server) airplayBase(r *http.Request) string {
	if r.Header.Get("X-Forwarded-Proto") != "" {
		return "" // behind a proxy: the page's address is what the client reached us on
	}
	if p := hostPort(r.Host); p != "" && p != requestPort(r) {
		return "" // the request came through something that is not this listener
	}
	if requestLocalIP(r) == nil {
		return "" // no connection: nothing to compare the TVs' network with
	}
	page := s.pageBase(r) // "" when the page's URLs are none a TV can use: it is routed
	base := ""
	for _, rec := range s.receivers() {
		if !isAppleTV(rec) {
			continue
		}
		for _, a := range rec.Addrs {
			b := s.mediaBaseFor(r, a)
			if b == "" {
				continue
			}
			if b == page {
				return "" // the page's address is one a TV can use: nothing to change
			}
			if base != "" && b != base {
				return "" // TVs on different networks: only the client can choose
			}
			base = b
		}
	}
	return base
}

// isAppleTV says whether a receiver is an Apple TV (model "AppleTV3,2", "AppleTV6,2", ...)
// rather than a Mac or another device that accepts AirPlay.
func isAppleTV(rec discovery.Receiver) bool {
	return strings.HasPrefix(rec.Model, "AppleTV")
}

// mediaBaseFor is the base URL of this server that a receiver at remote fetches from: the
// address of ours on remote's network, with the port this request reached us on. "" when no
// address of ours is on that network, or when the address found is one this server does not
// listen on (a server bound to loopback serves no LAN address).
func (s *Server) mediaBaseFor(r *http.Request, remote net.IP) string {
	ip := s.ourAddrOn(remote)
	if ip == nil {
		return ""
	}
	return s.baseAt(r, ip)
}

// ourAddrOn is the address of ours on remote's network that this server listens on; nil for none.
func (s *Server) ourAddrOn(remote net.IP) net.IP {
	if ip, ok := s.localIPFor(remote); ok && ip != nil && s.servesOn(ip) {
		return ip
	}
	return nil
}

// baseAt is the base URL of this server at ip, an address of ours, with the port this request
// reached us on. Plain HTTP names the address. HTTPS names the certificate's host, the only name
// a receiver can verify, and only where that name leads to ip: the owner's DNS points it at one
// address of ours, so on another of our networks there is no URL to give ("").
func (s *Server) baseAt(r *http.Request, ip net.IP) string {
	host := ip.String()
	if s.TLSHost != "" {
		if !slices.ContainsFunc(s.hostIPs.lookup(r.Context(), s.TLSHost), ip.Equal) {
			return ""
		}
		host = s.TLSHost
	}
	return s.scheme() + "://" + net.JoinHostPort(host, requestPort(r))
}

// servesOn reports whether this server listens on ip: every address of ours when it was given no
// listener (a test), every LAN address for a wildcard bind, and only its own for a specific one.
func (s *Server) servesOn(ip net.IP) bool {
	if s.ListenAddr == nil {
		return true
	}
	tcp, ok := s.ListenAddr.(*net.TCPAddr)
	if !ok {
		return true
	}
	if tcp.IP.IsUnspecified() {
		return true
	}
	return tcp.IP.Equal(ip)
}

// pageBase is the base of this listener the page itself is on, from the connection the request
// came in on. "" when the request came through no connection (a handler called directly, as in
// a test), and with HTTPS when the page came by another name than the certificate's (an
// address): a TV could not verify the URLs relative to it.
func (s *Server) pageBase(r *http.Request) string {
	ip := requestLocalIP(r)
	if ip == nil || s.TLSHost != "" && !strings.EqualFold(hostOnly(r.Host), s.TLSHost) {
		return ""
	}
	return s.baseAt(r, ip)
}

// requestLocalIP is the address of this server the client connected to: for a server with
// several networks, the one this page is on.
func requestLocalIP(r *http.Request) net.IP {
	if a, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		if t, ok := a.(*net.TCPAddr); ok {
			return t.IP
		}
	}
	return nil
}

// requestPort is the port this request reached our listener on: the connection's own port, or
// the Host's when there is no connection (a test), or the scheme's default.
func requestPort(r *http.Request) string {
	if a, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		if _, port, err := net.SplitHostPort(a.String()); err == nil && port != "" {
			return port
		}
	}
	if p := hostPort(r.Host); p != "" {
		return p
	}
	if r.TLS != nil {
		return "443"
	}
	return "80"
}

// hostPort is the port a Host header names; "" when it names none.
func hostPort(host string) string {
	if _, port, err := net.SplitHostPort(host); err == nil {
		return port
	}
	return ""
}

// scheme is the scheme this server's own listener speaks, for the URLs a receiver fetches from
// it. A proxy's X-Forwarded-Proto is not it: these URLs name an address of ours, not the
// proxy's.
func (s *Server) scheme() string {
	if s.TLSHost != "" {
		return "https"
	}
	return "http"
}

// hostCache keeps where a name leads for a minute, so that a list of receivers does not wait on
// DNS once for each of their addresses.
type hostCache struct {
	resolve func(ctx context.Context, host string) ([]net.IP, error) // a variable for the tests

	mu    sync.Mutex
	host  string
	ips   []net.IP
	until time.Time
}

const (
	hostCacheFor  = time.Minute
	hostMissFor   = 5 * time.Second // a name that did not resolve is asked again sooner
	hostLookupFor = 2 * time.Second
)

// lookup is where host leads; none when it does not resolve. The lookup is not the request's:
// a client that goes away while it runs must not leave "nowhere" for the next ones.
func (c *hostCache) lookup(ctx context.Context, host string) []net.IP {
	c.mu.Lock()
	defer c.mu.Unlock()
	if host == c.host && time.Now().Before(c.until) {
		return c.ips
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), hostLookupFor)
	defer cancel()
	ips, err := c.resolve(ctx, host)
	keep := hostCacheFor
	if err != nil || len(ips) == 0 {
		keep = hostMissFor
	}
	c.host, c.ips, c.until = host, ips, time.Now().Add(keep)
	return ips
}

func resolveHost(ctx context.Context, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}
