package discovery

import (
	"net"
	"testing"
)

func mustCIDR(t *testing.T, s string) *net.IPNet {
	t.Helper()
	ip, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatal(err)
	}
	n.IP = ip
	return n
}

func mustNet(t *testing.T, cidr, ip string) hostNetwork {
	t.Helper()
	return hostNetwork{net: mustCIDR(t, cidr), ip: net.ParseIP(ip)}
}

func TestLocalForMatchesTheNetworkOfThePeer(t *testing.T) {
	nets := []hostNetwork{
		mustNet(t, "192.168.4.196/22", "192.168.4.196"), // Wi-Fi
		mustNet(t, "192.168.139.3/23", "192.168.139.3"), // Internet Sharing bridge
		mustNet(t, "169.254.10.2/16", "169.254.10.2"),   // link-local (never chosen)
	}
	cases := []struct{ remote, want string }{
		{"192.168.4.39", "192.168.4.196"},  // an Apple TV on the Wi-Fi network
		{"192.168.7.200", "192.168.4.196"}, // the /22 holds 192.168.4.0-192.168.7.255
		{"192.168.139.20", "192.168.139.3"},
		{"10.0.0.5", ""},     // routed to us by a gateway: no network of ours holds it
		{"127.0.0.1", ""},    // loopback
		{"169.254.10.9", ""}, // link-local
		{"fe80::1", ""},      // IPv6, which the browse does not use
	}
	for _, c := range cases {
		got, ok := localFor(net.ParseIP(c.remote), nets)
		if c.want == "" {
			if ok {
				t.Errorf("localFor(%s) = %v, want no address of ours", c.remote, got)
			}
			continue
		}
		if !ok || !got.Equal(net.ParseIP(c.want)) {
			t.Errorf("localFor(%s) = %v (%v), want %s", c.remote, got, ok, c.want)
		}
	}
}

func TestLocalForPrefersTheMostSpecificNetwork(t *testing.T) {
	nets := []hostNetwork{
		mustNet(t, "192.168.4.196/22", "192.168.4.196"),
		mustNet(t, "192.168.4.200/30", "192.168.4.201"),
	}
	got, ok := localFor(net.ParseIP("192.168.4.202"), nets)
	if !ok || !got.Equal(net.ParseIP("192.168.4.201")) {
		t.Errorf("localFor = %v (%v), want the /30's address, not the /22's", got, ok)
	}
	// The /22 is still the answer for an address only it holds.
	got, ok = localFor(net.ParseIP("192.168.5.10"), nets)
	if !ok || !got.Equal(net.ParseIP("192.168.4.196")) {
		t.Errorf("localFor = %v (%v), want the /22's address", got, ok)
	}
}

func TestLocalForRejectsWhatIsNotAPeer(t *testing.T) {
	nets := []hostNetwork{mustNet(t, "192.168.4.196/22", "192.168.4.196")}
	for _, remote := range []string{"0.0.0.0", "127.0.0.1", "255.255.255.255", ""} {
		if got, ok := localFor(net.ParseIP(remote), nets); ok {
			t.Errorf("localFor(%q) = %v, want no address", remote, got)
		}
	}
}

func TestHostNetworksAreUsable(t *testing.T) {
	for _, n := range hostNetworks() {
		if n.ip.To4() == nil || n.ip.IsLoopback() || n.ip.IsLinkLocalUnicast() {
			t.Errorf("%v is not a LAN IPv4 address", n.ip)
		}
		if !n.net.Contains(n.ip) {
			t.Errorf("%v is not in its own network %v", n.ip, n.net)
		}
	}
}
