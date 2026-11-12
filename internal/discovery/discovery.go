// Package discovery advertises the server on the LAN via mDNS/DNS-SD and
// works out which URLs clients can reach it on.
package discovery

import (
	"fmt"
	"net"
	"sort"
	"strings"
)

// ServiceType is the DNS-SD service type clients browse for.
const ServiceType = "_ply._tcp"

// Advertiser is a registered mDNS service.
type Advertiser struct {
	r *responder
}

// Advertise registers a _ply._tcp service on port with the TXT records of
// txtRecords. api and apiMin are the versions of the API the server serves
// (docs/M11-CLIENTS.md, "Versioning"). It returns once the socket is open; the
// name is probed for and announced in the background, and taken as "name (2)"
// (and so on) while another responder has it.
func Advertise(name, id string, port, api, apiMin int) (*Advertiser, error) {
	// A dot would split the instance name into extra labels; and it is one label at most.
	instance := fitLabel(strings.ReplaceAll(name, ".", "-"), "")
	z, err := newZone(instance, hostLabel(), port, txtRecords(name, id, api, apiMin))
	if err != nil {
		return nil, fmt.Errorf("mdns register: %w", err)
	}
	r, err := newResponder(z)
	if err != nil {
		return nil, fmt.Errorf("mdns register: %w", err)
	}
	r.start(instance)
	return &Advertiser{r: r}, nil
}

// txtRecords are what a client learns before it connects: the server's id and
// name, the API versions it serves, and v, the version of this list's own layout.
// Keys may be added (tls and host are reserved for HTTPS); a client ignores what
// it does not know.
func txtRecords(name, id string, api, apiMin int) []string {
	return []string{"id=" + id, "v=1", "name=" + name, fmt.Sprintf("api=%d", api), fmt.Sprintf("apiMin=%d", apiMin)}
}

// Shutdown says goodbye and closes the responder. It is safe on a nil Advertiser,
// and to call twice.
func (a *Advertiser) Shutdown() {
	if a != nil && a.r != nil {
		a.r.shutdown()
	}
}

// URLs returns the http URLs clients can use to reach a listener bound to
// addr. For a wildcard bind these are the host's LAN IPv4 addresses; for a
// specific bind it is just that address.
func URLs(addr net.Addr) []string {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		return nil
	}
	if !tcp.IP.IsUnspecified() {
		return []string{httpURL(tcp.IP, tcp.Port)}
	}
	return LANURLs(tcp.Port)
}

// LANURLs returns http URLs for every non-loopback, non-link-local IPv4
// address on an interface that is up.
func LANURLs(port int) []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var addrs []net.Addr
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		a, err := ifc.Addrs()
		if err != nil {
			continue
		}
		addrs = append(addrs, a...)
	}
	return urlsFromAddrs(addrs, port)
}

func urlsFromAddrs(addrs []net.Addr, port int) []string {
	var urls []string
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		ip4 := ip.To4()
		if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() || ip4.IsUnspecified() {
			continue
		}
		urls = append(urls, httpURL(ip4, port))
	}
	sort.Strings(urls)
	return urls
}

func httpURL(ip net.IP, port int) string {
	return "http://" + net.JoinHostPort(ip.String(), fmt.Sprint(port))
}
