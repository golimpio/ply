package discovery

import (
	"net"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

var (
	testService  = dnsmessage.MustNewName(AirPlayService + ".local.")
	testInstance = dnsmessage.MustNewName("Living Room Apple TV." + AirPlayService + ".local.")
	testHost     = dnsmessage.MustNewName("Living-Room-Apple-TV.local.")
)

// rr is one answer record of a response.
func rr(name dnsmessage.Name, typ dnsmessage.Type, ttl uint32, body dnsmessage.ResourceBody) dnsmessage.Resource {
	return dnsmessage.Resource{
		Header: dnsmessage.ResourceHeader{Name: name, Type: typ, Class: dnsmessage.ClassINET, TTL: ttl},
		Body:   body,
	}
}

func ptrRecord(instance dnsmessage.Name, ttl uint32) dnsmessage.Resource {
	return rr(testService, dnsmessage.TypePTR, ttl, &dnsmessage.PTRResource{PTR: instance})
}

func srvRecord(instance, host dnsmessage.Name, port int, ttl uint32) dnsmessage.Resource {
	return rr(instance, dnsmessage.TypeSRV, ttl, &dnsmessage.SRVResource{Port: uint16(port), Target: host})
}

func txtRecord(instance dnsmessage.Name, txt []string) dnsmessage.Resource {
	return rr(instance, dnsmessage.TypeTXT, serviceTTL, &dnsmessage.TXTResource{TXT: txt})
}

func aRecord(host dnsmessage.Name, ip string, ttl uint32) dnsmessage.Resource {
	body := &dnsmessage.AResource{}
	copy(body.A[:], net.ParseIP(ip).To4())
	return rr(host, dnsmessage.TypeA, ttl, body)
}

// response is a message that answers a query, with rs as its answers.
func response(rs ...dnsmessage.Resource) dnsmessage.Message {
	return dnsmessage.Message{Header: dnsmessage.Header{Response: true}, Answers: rs}
}

// newTestBrowser is a Browser with no socket: observe and sweep are all a test drives.
func newTestBrowser() *Browser {
	return &Browser{service: testService, entries: map[string]*receiverEntry{}}
}

func TestBrowserListsAReceiverWhoseAddressIsKnown(t *testing.T) {
	b := newTestBrowser()
	b.observe(response(ptrRecord(testInstance, serviceTTL)))
	if got := b.Receivers(); len(got) != 0 {
		t.Fatalf("a PTR alone listed %v, want nothing until the receiver's address is known", got)
	}
	if !b.ask {
		t.Error("a new instance did not set ask: its records are still missing")
	}

	b.observe(response(
		srvRecord(testInstance, testHost, 7000, hostTTL),
		txtRecord(testInstance, []string{"deviceid=A0:ED:CD:DE:D8:40", "model=AppleTV3,2", "features=0x5A7FFFF7,0x1E"}),
		aRecord(testHost, "192.168.4.39", hostTTL),
	))
	got := b.Receivers()
	if len(got) != 1 {
		t.Fatalf("receivers = %v, want the one Apple TV", got)
	}
	r := got[0]
	if r.Name != "Living Room Apple TV" {
		t.Errorf("name = %q, want the instance's label without the service", r.Name)
	}
	if r.ID != "A0:ED:CD:DE:D8:40" || r.Model != "AppleTV3,2" || r.Port != 7000 {
		t.Errorf("receiver = %+v, want the TXT deviceid, the TXT model and the SRV port", r)
	}
	if len(r.Addrs) != 1 || !r.Addrs[0].Equal(net.ParseIP("192.168.4.39")) {
		t.Errorf("addresses = %v, want its A record", r.Addrs)
	}
}

func TestBrowserGoodbyeTakesTheReceiverAway(t *testing.T) {
	b := newTestBrowser()
	b.observe(response(
		ptrRecord(testInstance, serviceTTL),
		srvRecord(testInstance, testHost, 7000, hostTTL),
		aRecord(testHost, "192.168.4.39", hostTTL),
	))
	if got := b.Receivers(); len(got) != 1 { // the positive control: it was there
		t.Fatalf("receivers = %v before the goodbye, want the Apple TV", got)
	}
	b.observe(response(ptrRecord(testInstance, 0)))
	if got := b.Receivers(); len(got) != 0 {
		t.Errorf("receivers = %v after the goodbye, want none", got)
	}
}

func TestBrowserForgetsASilentReceiver(t *testing.T) {
	b := newTestBrowser()
	b.observe(response(
		ptrRecord(testInstance, serviceTTL),
		srvRecord(testInstance, testHost, 7000, hostTTL),
		aRecord(testHost, "192.168.4.39", hostTTL),
	))
	if got := b.Receivers(); len(got) != 1 { // the positive control
		t.Fatalf("receivers = %v, want the Apple TV", got)
	}
	b.mu.Lock()
	b.entries[testInstance.String()].seen = time.Now().Add(-receiverTTL - time.Second)
	b.mu.Unlock()
	b.sweep()
	if got := b.Receivers(); len(got) != 0 {
		t.Errorf("receivers = %v after receiverTTL of silence, want none", got)
	}
}

func TestBrowserIgnoresOtherServices(t *testing.T) {
	b := newTestBrowser()
	raop := dnsmessage.MustNewName("A0EDCDDED840@Living Room Apple TV._raop._tcp.local.")
	b.observe(response(rr(dnsmessage.MustNewName("_raop._tcp.local."), dnsmessage.TypePTR, serviceTTL,
		&dnsmessage.PTRResource{PTR: raop})))
	if got := b.Receivers(); len(got) != 0 {
		t.Errorf("receivers = %v, want only what _airplay._tcp names", got)
	}
	if len(b.entries) != 0 {
		t.Errorf("entries = %v, want no entry for a service that is not ours", b.entries)
	}
}

func TestBrowserGivesUpOnAnAddressItCannotResolve(t *testing.T) {
	b := newTestBrowser()
	b.observe(response(
		ptrRecord(testInstance, serviceTTL),
		srvRecord(testInstance, testHost, 7000, hostTTL),
	))
	if got := b.Receivers(); len(got) != 0 {
		t.Errorf("receivers = %v, want none without an address", got)
	}
	b.observe(response(
		srvRecord(testInstance, testHost, 7000, hostTTL),
		aRecord(testHost, "192.168.4.39", hostTTL),
	))
	if got := b.Receivers(); len(got) != 1 {
		t.Errorf("receivers = %v after its A record, want the Apple TV", got)
	}
}

func TestInstanceLabel(t *testing.T) {
	cases := map[dnsmessage.Name]string{
		testInstance: "Living Room Apple TV",
		dnsmessage.MustNewName("ply._airplay._tcp.local."):         "ply",
		dnsmessage.MustNewName("macbook pro._airplay._tcp.local."): "macbook pro",
		dnsmessage.MustNewName("no-suffix.local."):                 "no-suffix.local.",
	}
	for name, want := range cases {
		if got := instanceLabel(name); got != want {
			t.Errorf("instanceLabel(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestTXTMap(t *testing.T) {
	got := txtMap([]string{"deviceid=A0:ED", "MODEL=AppleTV3,2", "no-equals"})
	want := map[string]string{"deviceid": "A0:ED", "model": "AppleTV3,2"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("txtMap[%q] = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("txtMap = %v, want only the key=value pairs (keys in lower case)", got)
	}
}
