package discovery

import "net"

// A server with more than one network (Wi-Fi and Ethernet, Internet Sharing, a VPN) has more
// than one address, and a receiver on one of them cannot fetch a URL on another. A peer's
// address says which of ours to use: the address on the network that holds it.

// hostNetwork is one IPv4 network this machine is on, and the address the interface holds on it.
type hostNetwork struct {
	net *net.IPNet
	ip  net.IP
}

// hostNetworks lists the IPv4 networks of this machine's interfaces that are up, not loopback
// and not link-local.
func hostNetworks() []hostNetwork {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []hostNetwork
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := n.IP.To4()
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, hostNetwork{net: n, ip: ip})
		}
	}
	return out
}

// localFor is the address of ours a peer at remote reaches: the one on the most specific
// network that holds remote. ok is false when none does (that peer is routed to us through a
// gateway, so it is on no network of ours), or when remote is not a unicast IPv4 address.
func localFor(remote net.IP, nets []hostNetwork) (net.IP, bool) {
	r := remote.To4()
	if r == nil || r.IsLoopback() || r.IsUnspecified() || r.IsLinkLocalUnicast() {
		return nil, false
	}
	best, bits := -1, -1
	for i, n := range nets {
		if n.net == nil || n.ip == nil || !n.net.Contains(r) {
			continue
		}
		if b, _ := n.net.Mask.Size(); b > bits {
			best, bits = i, b
		}
	}
	if best < 0 {
		return nil, false
	}
	return nets[best].ip, true
}

// LocalIPFor is the address of this server a peer at remote reaches (see localFor).
func LocalIPFor(remote net.IP) (net.IP, bool) {
	return localFor(remote, hostNetworks())
}
