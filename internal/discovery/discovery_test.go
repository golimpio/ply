package discovery

import (
	"net"
	"reflect"
	"testing"
)

func cidr(t *testing.T, s string) net.Addr {
	t.Helper()
	ip, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatal(err)
	}
	n.IP = ip
	return n
}

func TestURLsFromAddrs(t *testing.T) {
	addrs := []net.Addr{
		cidr(t, "192.168.1.20/24"),
		cidr(t, "10.0.0.5/8"),
		cidr(t, "127.0.0.1/8"),     // loopback
		cidr(t, "169.254.12.3/16"), // link-local
		cidr(t, "fe80::1/64"),      // IPv6
		cidr(t, "2001:db8::5/64"),  // IPv6
		&net.IPAddr{IP: net.IPv4(172, 16, 0, 9)},
		&net.UnixAddr{Name: "/tmp/x", Net: "unix"}, // not an IP
	}
	got := urlsFromAddrs(addrs, 8420)
	want := []string{"http://10.0.0.5:8420", "http://172.16.0.9:8420", "http://192.168.1.20:8420"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("urls = %v, want %v", got, want)
	}
}

func TestURLsForSpecificBind(t *testing.T) {
	got := URLs(&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9000})
	if want := []string{"http://127.0.0.1:9000"}; !reflect.DeepEqual(got, want) {
		t.Errorf("urls = %v, want %v", got, want)
	}
	if got := URLs(&net.UnixAddr{Name: "x", Net: "unix"}); got != nil {
		t.Errorf("non-TCP addr: %v", got)
	}
}

func TestURLsForWildcardBindNeverLoopback(t *testing.T) {
	for _, u := range URLs(&net.TCPAddr{IP: net.IPv4zero, Port: 8420}) {
		host, _, err := net.SplitHostPort(u[len("http://"):])
		if err != nil {
			t.Fatalf("bad url %q: %v", u, err)
		}
		if ip := net.ParseIP(host); ip == nil || ip.IsLoopback() || ip.To4() == nil {
			t.Errorf("url %q is not a LAN IPv4 address", u)
		}
	}
}

func TestShutdownNilSafe(t *testing.T) {
	var a *Advertiser
	a.Shutdown()
	(&Advertiser{}).Shutdown()
}
