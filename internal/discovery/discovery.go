// Package discovery advertises the server on the LAN via mDNS/DNS-SD and
// works out which URLs clients can reach it on.
package discovery

import (
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/grandcat/zeroconf"
)

// ServiceType is the DNS-SD service type clients browse for.
const ServiceType = "_ply._tcp"

// Advertiser is a registered mDNS service.
type Advertiser struct {
	srv *zeroconf.Server
}

// Advertise registers a _ply._tcp service on port with the TXT records of
// txtRecords. api and apiMin are the versions of the API the server serves
// (docs/M11-CLIENTS.md, "Versioning").
func Advertise(name, id string, port, api, apiMin int) (*Advertiser, error) {
	// A dot would split the instance name into extra labels.
	instance := strings.ReplaceAll(name, ".", "-")
	srv, err := zeroconf.Register(instance, ServiceType, "local.", port, txtRecords(name, id, api, apiMin), nil)
	if err != nil {
		return nil, fmt.Errorf("mdns register: %w", err)
	}
	return &Advertiser{srv: srv}, nil
}

// txtRecords are what a client learns before it connects: the server's id and
// name, the API versions it serves, and v, the version of this list's own layout.
// Keys may be added (tls and host are reserved for HTTPS); a client ignores what
// it does not know.
func txtRecords(name, id string, api, apiMin int) []string {
	return []string{"id=" + id, "v=1", "name=" + name, fmt.Sprintf("api=%d", api), fmt.Sprintf("apiMin=%d", apiMin)}
}

// Shutdown un-registers the service. It is safe on a nil Advertiser.
func (a *Advertiser) Shutdown() {
	if a != nil && a.srv != nil {
		a.srv.Shutdown()
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
