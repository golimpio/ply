//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package discovery

import "net"

// listenMDNS opens UDP port 5353 on every address. Where the port cannot be shared, it fails
// while another responder has it, and the server is not advertised.
func listenMDNS() (*net.UDPConn, error) {
	return net.ListenUDP("udp4", &net.UDPAddr{Port: mdnsPort})
}
