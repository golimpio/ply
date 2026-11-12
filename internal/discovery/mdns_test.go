package discovery

import (
	"net"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/net/dns/dnsmessage"
)

var lan = []net.IP{net.IPv4(192, 168, 1, 20)}

func testZone(t *testing.T) zone {
	t.Helper()
	z, err := newZone("Living room", "mac", 8420, txtRecords("Living room", "abc", 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	return z
}

func query(qs ...dnsmessage.Question) dnsmessage.Message {
	return dnsmessage.Message{Questions: qs}
}

func question(name string, t dnsmessage.Type) dnsmessage.Question {
	return dnsmessage.Question{Name: dnsmessage.MustNewName(name), Type: t, Class: dnsmessage.ClassINET}
}

var mdnsClient = &net.UDPAddr{IP: net.IPv4(192, 168, 1, 30), Port: mdnsPort}

// What a record says, for comparing: "name TYPE data ttl".
func describe(rs []dnsmessage.Resource) []string {
	var out []string
	for _, r := range rs {
		var data string
		switch b := r.Body.(type) {
		case *dnsmessage.PTRResource:
			data = b.PTR.String()
		case *dnsmessage.SRVResource:
			data = b.Target.String() + ":" + strconv.Itoa(int(b.Port))
		case *dnsmessage.TXTResource:
			data = strings.Join(b.TXT, ",")
		case *dnsmessage.AResource:
			data = net.IP(b.A[:]).String()
		}
		out = append(out, strings.ToLower(r.Header.Name.String())+" "+r.Header.Type.String()+" "+data)
	}
	return out
}

// A browse (PTR _ply._tcp) is answered with the instance, and what a client asks next comes
// along: its SRV and TXT, and the address of the host on the network the query came from.
func TestABrowseIsAnsweredWithWhatComesNext(t *testing.T) {
	z := testZone(t)
	m, to, ok := z.reply(query(question("_ply._tcp.local.", dnsmessage.TypePTR)), mdnsClient, lan)
	if !ok || to != nil {
		t.Fatalf("reply = %v, to %v; want a multicast answer", ok, to)
	}
	if got, want := describe(m.Answers), []string{"_ply._tcp.local. TypePTR Living room._ply._tcp.local."}; !slices.Equal(got, want) {
		t.Errorf("answers = %q, want %q", got, want)
	}
	want := []string{
		"living room._ply._tcp.local. TypeSRV mac.local.:8420",
		"living room._ply._tcp.local. TypeTXT id=abc,v=1,name=Living room,api=1,apiMin=1",
		"mac.local. TypeA 192.168.1.20",
	}
	if got := describe(m.Additionals); !slices.Equal(got, want) {
		t.Errorf("additional = %q, want %q", got, want)
	}
	if !m.Header.Response || !m.Header.Authoritative || m.Header.ID != 0 {
		t.Errorf("header = %+v", m.Header)
	}
	if m.Answers[0].Header.Class&topBit != 0 || m.Additionals[0].Header.Class&topBit == 0 {
		t.Error("the PTR is shared (no cache-flush bit) and the SRV is unique (cache-flush)")
	}
	if _, err := m.Pack(); err != nil {
		t.Fatal(err)
	}
}

// Each record of the instance and the host is answered when asked for, by any case of its name.
func TestTheRecordsAskedFor(t *testing.T) {
	z := testZone(t)
	for _, tc := range []struct {
		q       dnsmessage.Question
		answers []string
	}{
		{question("living ROOM._ply._tcp.local.", dnsmessage.TypeSRV), []string{"living room._ply._tcp.local. TypeSRV mac.local.:8420"}},
		{question("Living room._ply._tcp.local.", dnsmessage.TypeTXT), []string{"living room._ply._tcp.local. TypeTXT id=abc,v=1,name=Living room,api=1,apiMin=1"}},
		{question("Living room._ply._tcp.local.", dnsmessage.TypeALL), []string{"living room._ply._tcp.local. TypeSRV mac.local.:8420", "living room._ply._tcp.local. TypeTXT id=abc,v=1,name=Living room,api=1,apiMin=1"}},
		{question("MAC.local.", dnsmessage.TypeA), []string{"mac.local. TypeA 192.168.1.20"}},
		{question("_services._dns-sd._udp.local.", dnsmessage.TypePTR), []string{"_services._dns-sd._udp.local. TypePTR _ply._tcp.local."}},
	} {
		m, _, ok := z.reply(query(tc.q), mdnsClient, lan)
		if got := describe(m.Answers); !ok || !slices.Equal(got, tc.answers) {
			t.Errorf("%s %s: answers %q, want %q", tc.q.Name, tc.q.Type, got, tc.answers)
		}
	}
	// What is not ours, and what is not a question, is not answered.
	for name, m := range map[string]dnsmessage.Message{
		"another service":  query(question("_airplay._tcp.local.", dnsmessage.TypePTR)),
		"another instance": query(question("Kitchen._ply._tcp.local.", dnsmessage.TypeSRV)),
		"another host":     query(question("pc.local.", dnsmessage.TypeA)),
		"our SRV as AAAA":  query(question("mac.local.", dnsmessage.TypeAAAA)),
		"a response":       {Header: dnsmessage.Header{Response: true}, Questions: []dnsmessage.Question{question("_ply._tcp.local.", dnsmessage.TypePTR)}},
	} {
		if _, _, ok := z.reply(m, mdnsClient, lan); ok {
			t.Errorf("%s was answered", name)
		}
	}
}

// A client that lists the instance it knows, with at least half its TTL left, is not told it again.
func TestKnownAnswersAreNotSentAgain(t *testing.T) {
	z := testZone(t)
	q := query(question("_ply._tcp.local.", dnsmessage.TypePTR))
	q.Answers = []dnsmessage.Resource{z.ptr(serviceTTL / 2)}
	if _, _, ok := z.reply(q, mdnsClient, lan); ok {
		t.Error("a known instance was answered again")
	}
	q.Answers = []dnsmessage.Resource{z.ptr(serviceTTL/2 - 1)} // under half: about to expire
	if _, _, ok := z.reply(q, mdnsClient, lan); !ok {
		t.Error("an instance about to expire in the client's cache was not answered")
	}
}

// Whom the reply goes to: the group, unless the question asks for a unicast answer (QU), or the
// client is not an mDNS one (another port), which gets its id and questions back, short TTLs and
// no cache-flush bits.
func TestWhereTheReplyGoes(t *testing.T) {
	z := testZone(t)
	qu := question("_ply._tcp.local.", dnsmessage.TypePTR)
	qu.Class |= topBit
	if _, to, ok := z.reply(query(qu), mdnsClient, lan); !ok || to != mdnsClient {
		t.Errorf("a QU question: to %v, want the client", to)
	}

	legacy := &net.UDPAddr{IP: net.IPv4(192, 168, 1, 30), Port: 49152}
	q := query(question("Living room._ply._tcp.local.", dnsmessage.TypeSRV))
	q.Header.ID = 0x1234
	m, to, ok := z.reply(q, legacy, lan)
	if !ok || to != legacy || m.Header.ID != 0x1234 || len(m.Questions) != 1 {
		t.Fatalf("a legacy query: to %v, header %+v, %d questions", to, m.Header, len(m.Questions))
	}
	for _, r := range append(m.Answers, m.Additionals...) {
		if r.Header.TTL > legacyTTL || r.Header.Class&topBit != 0 {
			t.Errorf("%s: ttl %d class %#x, want at most %d and no cache-flush", r.Header.Name, r.Header.TTL, r.Header.Class, legacyTTL)
		}
	}
	// Positive control: the same records to an mDNS client keep their TTL and bits.
	m, _, _ = z.reply(q, mdnsClient, lan)
	if r := m.Answers[0]; r.Header.TTL != hostTTL || r.Header.Class&topBit == 0 {
		t.Errorf("to an mDNS client: ttl %d class %#x", r.Header.TTL, r.Header.Class)
	}
}

// While probing, a response that holds a record of the instance name says another responder
// has it; a probe of another host for it, or a response about another name, does not.
func TestClaims(t *testing.T) {
	z := testZone(t)
	other := dnsmessage.Message{Response: true, Answers: []dnsmessage.Resource{z.srv(hostTTL)}}
	if !z.claims(other) {
		t.Error("a response with our instance's SRV is not a conflict")
	}
	if z.claims(z.probe(true)) {
		t.Error("a probe (a query) was taken for a conflict")
	}
	known := query(question("Living room._ply._tcp.local.", dnsmessage.TypeSRV))
	known.Answers = []dnsmessage.Resource{z.srv(hostTTL)} // a client that has it already
	if z.claims(known) {
		t.Error("a query listing our SRV as a known answer was taken for a conflict")
	}
	bye := dnsmessage.Message{Response: true, Answers: []dnsmessage.Resource{z.srv(0)}}
	if z.claims(bye) {
		t.Error("a goodbye for the name (an older instance leaving) was taken for a conflict")
	}
	k, _ := newZone("Kitchen", "pc", 8420, nil)
	if z.claims(dnsmessage.Message{Response: true, Answers: []dnsmessage.Resource{k.srv(hostTTL)}}) {
		t.Error("a response about another instance was taken for a conflict")
	}
	if got := renamed("Living room", 2); got != "Living room (2)" {
		t.Errorf("renamed = %q", got)
	}
}

// Names are one DNS label: an empty or too long one is refused (it could never be sent), and a
// long one is cut, at a character, to fit with what is added to it.
func TestLabels(t *testing.T) {
	for _, bad := range []string{"", strings.Repeat("x", maxLabel+1), "a.b"} {
		if _, err := newZone(bad, "mac", 8420, nil); err == nil {
			t.Errorf("instance %q was taken", bad)
		}
		if _, err := newZone("ply", bad, 8420, nil); err == nil {
			t.Errorf("host %q was taken", bad)
		}
	}
	if _, err := newZone(strings.Repeat("x", maxLabel), "mac", 8420, nil); err != nil { // positive control
		t.Errorf("a %d-byte instance: %v", maxLabel, err)
	}
	long := strings.Repeat("é", 40) // 80 bytes
	got := renamed(long, 12)
	if len(got) > maxLabel || !strings.HasSuffix(got, " (12)") || !utf8.ValidString(got) {
		t.Errorf("renamed = %q (%d bytes)", got, len(got))
	}
	if _, err := testZone(t).withInstance(got); err != nil {
		t.Error(err)
	}
}

// A probe asks for the name, with the records it would claim; the first asks for a unicast answer.
func TestProbe(t *testing.T) {
	z := testZone(t)
	p := z.probe(true)
	if len(p.Questions) != 1 || !sameName(p.Questions[0].Name, z.instance) || p.Questions[0].Type != dnsmessage.TypeALL || p.Questions[0].Class&topBit == 0 {
		t.Errorf("first probe questions = %+v", p.Questions)
	}
	if got := describe(p.Authorities); len(got) != 2 {
		t.Errorf("authorities = %q, want the SRV and the TXT", got)
	}
	if z.probe(false).Questions[0].Class&topBit != 0 {
		t.Error("a later probe asks for a unicast answer")
	}
}

// Goodbye withdraws the service's records (TTL 0) but not the host's address, which the system's
// own responder may answer for too.
func TestGoodbye(t *testing.T) {
	z := testZone(t)
	g := z.goodbye()
	for _, r := range g {
		if r.Header.TTL != 0 || r.Header.Type == dnsmessage.TypeA {
			t.Errorf("goodbye has %s %s ttl %d", r.Header.Name, r.Header.Type, r.Header.TTL)
		}
	}
	// Positive control: the announcement does carry the address, with a TTL.
	a := z.announcement(lan)
	if !slices.ContainsFunc(a, func(r dnsmessage.Resource) bool { return r.Header.Type == dnsmessage.TypeA && r.Header.TTL == hostTTL }) {
		t.Errorf("announcement = %q, want the host's address", describe(a))
	}
	if len(g) != len(a)-len(lan) {
		t.Errorf("goodbye has %d records, the announcement %d of the service", len(g), len(a)-len(lan))
	}
}
