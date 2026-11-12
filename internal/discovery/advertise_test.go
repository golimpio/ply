package discovery

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/ipv4"
)

// mdnsListener is a client's mDNS socket: port 5353, shared, in the group on every interface.
func mdnsListener(t *testing.T) *ipv4.PacketConn {
	t.Helper()
	conn, err := listenMDNS()
	if err != nil {
		t.Skipf("cannot open the mDNS port here: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	pc := ipv4.NewPacketConn(conn)
	joined := 0
	for _, ifi := range multicastIfaces() {
		if pc.JoinGroup(&ifi, &net.UDPAddr{IP: mdnsGroup.IP}) == nil {
			joined++
		}
	}
	if joined == 0 {
		t.Skip("no interface to join the mDNS group on")
	}
	_ = pc.SetMulticastLoopback(true)
	return pc
}

// read returns the next mDNS message pc receives within d, or false.
func read(pc *ipv4.PacketConn, d time.Duration) (dnsmessage.Message, bool) {
	buf := make([]byte, 9000)
	_ = pc.SetReadDeadline(time.Now().Add(d))
	for {
		n, _, _, err := pc.ReadFrom(buf)
		if err != nil {
			return dnsmessage.Message{}, false
		}
		var m dnsmessage.Message
		if m.Unpack(buf[:n]) == nil {
			return m, true
		}
	}
}

func sendQuery(t *testing.T, pc *ipv4.PacketConn, m dnsmessage.Message) {
	t.Helper()
	b, err := m.Pack()
	if err != nil {
		t.Fatal(err)
	}
	for _, ifi := range multicastIfaces() {
		_ = pc.SetMulticastInterface(&ifi)
		_, _ = pc.WriteTo(b, nil, mdnsGroup)
	}
}

// multicastClient is a client's mDNS socket, once it has heard its own query back: it is skipped
// where there is no multicast (a sandbox, no route), where nothing could be seen.
func multicastClient(t *testing.T) *ipv4.PacketConn {
	t.Helper()
	client := mdnsListener(t)
	probe := query(question("_ply-probe._tcp.local.", dnsmessage.TypePTR))
	sendQuery(t, client, probe)
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		m, ok := read(client, time.Until(deadline))
		if ok && !m.Header.Response && len(m.Questions) == 1 && sameName(m.Questions[0].Name, probe.Questions[0].Name) {
			return client
		}
	}
	t.Skip("the client's own query was not heard back: no multicast on this machine")
	return nil
}

// browseFor browses for _ply._tcp as a client does (a PTR query from port 5353, answered to the
// group) until instance is seen, within d; it returns its SRV (nil if not seen) and TXT.
func browseFor(t *testing.T, client *ipv4.PacketConn, instance string, d time.Duration) (*dnsmessage.SRVResource, []string) {
	t.Helper()
	browse := query(question("_ply._tcp.local.", dnsmessage.TypePTR))
	var srv *dnsmessage.SRVResource
	var txt []string
	for deadline := time.Now().Add(d); srv == nil && time.Now().Before(deadline); {
		sendQuery(t, client, browse)
		for until := time.Now().Add(500 * time.Millisecond); srv == nil && time.Now().Before(until); {
			m, ok := read(client, time.Until(until))
			if !ok || !m.Header.Response {
				continue
			}
			for _, r := range append(m.Answers, m.Additionals...) {
				if !strings.EqualFold(r.Header.Name.String(), instance+"._ply._tcp.local.") || r.Header.TTL == 0 {
					continue
				}
				switch b := r.Body.(type) {
				case *dnsmessage.SRVResource:
					srv = b
				case *dnsmessage.TXTResource:
					txt = b.TXT
				}
			}
		}
	}
	return srv, txt
}

func tag(t *testing.T) string {
	t.Helper()
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(suffix)
}

// TestAdvertiseAnnouncesTheService advertises the service the way the server does and browses
// for it as a client does: the instance (dots turned to dashes, they would split it into labels),
// the port and the TXT records (id, v=1, the name as typed and the API versions) are what a client
// such as the iOS app sees. A client that is not an mDNS one gets its answer too, and Shutdown
// says goodbye.
//
// mDNS is multicast on the local network, and nothing here can fake that: where the client does
// not hear its own query, the test is skipped; where it does and the service is not seen,
// Advertise is wrong and the test fails. On a Mac, `dns-sd -B _ply._tcp` and
// `dns-sd -L <instance> _ply._tcp` check it against the system's own client.
func TestAdvertiseAnnouncesTheService(t *testing.T) {
	tg := tag(t)
	name := "ply.test." + tg // dots on purpose
	instance := "ply-test-" + tg
	id := "0123456789abcdef0123456789abcdef"

	ln, err := net.Listen("tcp", "127.0.0.1:0") // a free port to announce
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	client := multicastClient(t)
	adv, err := Advertise(Service{Name: name, ID: id, Port: port, API: 3, APIMin: 2})
	if err != nil {
		t.Fatalf("Advertise where a client can use mDNS: %v", err)
	}
	defer adv.Shutdown()

	srv, txt := browseFor(t, client, instance, 8*time.Second)
	if srv == nil {
		t.Fatalf("multicast works here, but Advertise(%q) was not seen as %q within 8 s", name, instance)
	}
	if int(srv.Port) != port || !strings.EqualFold(srv.Target.String(), hostLabel()+".local.") {
		t.Errorf("SRV = %s:%d, want %s.local.:%d", srv.Target, srv.Port, hostLabel(), port)
	}
	want := []string{"api=3", "apiMin=2", "id=" + id, "name=" + name, "v=1"}
	got := slices.Clone(txt)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("TXT records = %q, want %q", got, want)
	}

	// A query once the name is claimed, from a client that is not an mDNS one (another port): the
	// answer comes back to it alone, so it is the responder answering, not an announcement.
	legacy, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	q := query(question(instance+"._ply._tcp.local.", dnsmessage.TypeSRV))
	q.Header.ID = 0x5150
	b, err := q.Pack()
	if err != nil {
		t.Fatal(err)
	}
	answered := false
	for try := 0; try < 4 && !answered; try++ {
		if _, err := legacy.WriteToUDP(b, mdnsGroup); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 9000)
		_ = legacy.SetReadDeadline(time.Now().Add(time.Second))
		for {
			n, err := legacy.Read(buf)
			if err != nil {
				break
			}
			var m dnsmessage.Message
			if m.Unpack(buf[:n]) == nil && m.Header.ID == 0x5150 && len(m.Answers) == 1 && m.Answers[0].Header.TTL <= legacyTTL {
				answered = true
				break
			}
		}
	}
	if !answered {
		t.Error("a legacy unicast query for the SRV got no answer")
	}

	// Shut down, it says goodbye: the PTR again, with a TTL of 0.
	adv.Shutdown()
	bye := false
	for deadline := time.Now().Add(2 * time.Second); !bye && time.Now().Before(deadline); {
		m, ok := read(client, time.Until(deadline))
		for _, r := range m.Answers {
			if p, isPTR := r.Body.(*dnsmessage.PTRResource); ok && isPTR && r.Header.TTL == 0 && strings.EqualFold(p.PTR.String(), instance+"._ply._tcp.local.") {
				bye = true
			}
		}
	}
	if !bye {
		t.Error("no goodbye after Shutdown")
	}
	adv.Shutdown() // twice is fine
}

// A second server of the same name takes "name (2)": the first answers its probes. When every
// name it may try is taken, it says nothing at all.
func TestATakenNameIsRenamed(t *testing.T) {
	client := multicastClient(t)
	name := "ply-dup-" + tag(t)
	first, err := Advertise(Service{Name: name, ID: "a", Port: 9, API: 1, APIMin: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Shutdown()
	if srv, _ := browseFor(t, client, name, 8*time.Second); srv == nil { // positive control
		t.Fatal("the first server was not seen")
	}

	second, err := Advertise(Service{Name: name, ID: "b", Port: 10, API: 1, APIMin: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Shutdown()
	if srv, _ := browseFor(t, client, name+" (2)", 8*time.Second); srv == nil || srv.Port != 10 {
		t.Fatalf("the second server as %q: %+v, want it on port 10", name+" (2)", srv)
	}

	defer func(n int) { maxRenames = n }(maxRenames)
	maxRenames = 1 // only the name itself may be tried
	third, err := Advertise(Service{Name: name, ID: "c", Port: 11, API: 1, APIMin: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer third.Shutdown()
	time.Sleep(4 * probeWait) // its probes, and what it would announce after them
	if srv, _ := browseFor(t, client, name+" (2)", 2*time.Second); srv == nil || srv.Port != 10 {
		t.Errorf("%q is %+v, want the second server's (the third took no name)", name+" (2)", srv)
	}
	if srv, _ := browseFor(t, client, name, time.Second); srv == nil || srv.Port != 9 {
		t.Errorf("%q is %+v, want the first server's", name, srv)
	}
	if srv, _ := browseFor(t, client, name+" (3)", 2*time.Second); srv != nil {
		t.Errorf("the third server took %q, past its last name", name+" (3)")
	}
}

// The TXT records, without the network: the multicast test above skips where
// there is no multicast. HTTPS adds tls and host, and changes none of the others.
func TestTXTRecords(t *testing.T) {
	s := Service{Name: "Living room", ID: "abc", Port: 8420, API: 1, APIMin: 1}
	plain := []string{"id=abc", "v=1", "name=Living room", "api=1", "apiMin=1"}
	if got := txtRecords(s); !slices.Equal(got, plain) {
		t.Fatalf("TXT records = %q, want %q", got, plain)
	}
	s.TLSHost = "ply.home.example.com"
	if got, want := txtRecords(s), append(slices.Clone(plain), "tls=1", "host=ply.home.example.com"); !slices.Equal(got, want) {
		t.Fatalf("TXT records with HTTPS = %q, want %q", got, want)
	}
}
