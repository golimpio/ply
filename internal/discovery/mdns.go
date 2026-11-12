package discovery

// The part of mDNS (RFC 6762) and DNS-SD (RFC 6763) an advertiser needs: probe for the
// instance name, announce it, answer the queries for it, and say goodbye. IPv4 only, as
// the URLs the server gives are.

import (
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/ipv4"
)

const (
	mdnsPort = 5353
	// RFC 6762 §10: a record that names a host is kept 120 s, the others 75 minutes.
	hostTTL    = 120
	serviceTTL = 4500
	// legacyTTL caps the answers to a client that is not an mDNS one (it asks from another port):
	// it caches by the TTL, and never hears a goodbye (§6.7).
	legacyTTL = 10
	// topBit is the top bit of a class: in an answer, the record is this host's alone and replaces
	// what a cache holds (cache-flush, §10.2); in a question, a unicast answer is asked for (§5.4).
	topBit = 1 << 15

	probeWait   = 250 * time.Millisecond // between probes (§8.1)
	probes      = 3
	announceGap = time.Second // between the two announcements (§8.3)

	maxLabel = 63 // bytes in a DNS label
)

// maxRenames is how many names are tried, "name", "name (2)", ... (a variable for the tests).
var maxRenames = 15

var (
	mdnsGroup    = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: mdnsPort}
	servicesName = dnsmessage.MustNewName("_services._dns-sd._udp.local.")
)

// zone is what the advertiser answers for: its service instance and the host it is on.
type zone struct {
	instance, service, host dnsmessage.Name
	port                    uint16
	txt                     []string
}

// newZone is the zone of instance on host. Both are one DNS label (1 to 63 bytes, no dot).
func newZone(instance, host string, port int, txt []string) (zone, error) {
	z := zone{service: dnsmessage.MustNewName(ServiceType + ".local."), txt: txt}
	if port <= 0 || port > 65535 {
		return zone{}, fmt.Errorf("port %d", port)
	}
	z.port = uint16(port)
	h, err := labelName(host, ".local.")
	if err != nil {
		return zone{}, fmt.Errorf("host name: %w", err)
	}
	z.host = h
	return z.withInstance(instance)
}

// withInstance is z under another instance name.
func (z zone) withInstance(instance string) (zone, error) {
	in, err := labelName(instance, "."+ServiceType+".local.")
	if err != nil {
		return zone{}, fmt.Errorf("instance name: %w", err)
	}
	z.instance = in
	return z, nil
}

// labelName is the name of label followed by rest, refused when label is not one DNS label.
func labelName(label, rest string) (dnsmessage.Name, error) {
	if label == "" || len(label) > maxLabel || strings.Contains(label, ".") {
		return dnsmessage.Name{}, fmt.Errorf("%q is not 1 to %d bytes without a dot", label, maxLabel)
	}
	return dnsmessage.NewName(label + rest)
}

// fitLabel cuts s, at a character, so that it and suffix make one DNS label.
func fitLabel(s, suffix string) string {
	for len(s)+len(suffix) > maxLabel {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s + suffix
}

func header(name dnsmessage.Name, typ dnsmessage.Type, ttl uint32, unique bool) dnsmessage.ResourceHeader {
	class := dnsmessage.ClassINET
	if unique {
		class |= topBit
	}
	return dnsmessage.ResourceHeader{Name: name, Type: typ, Class: class, TTL: ttl}
}

// The PTR records are shared (many hosts have a _ply._tcp instance); the rest are unique.
func (z zone) ptr(ttl uint32) dnsmessage.Resource {
	return dnsmessage.Resource{Header: header(z.service, dnsmessage.TypePTR, ttl, false), Body: &dnsmessage.PTRResource{PTR: z.instance}}
}

func (z zone) enumeration(ttl uint32) dnsmessage.Resource {
	return dnsmessage.Resource{Header: header(servicesName, dnsmessage.TypePTR, ttl, false), Body: &dnsmessage.PTRResource{PTR: z.service}}
}

func (z zone) srv(ttl uint32) dnsmessage.Resource {
	return dnsmessage.Resource{Header: header(z.instance, dnsmessage.TypeSRV, ttl, true),
		Body: &dnsmessage.SRVResource{Port: z.port, Target: z.host}}
}

func (z zone) text(ttl uint32) dnsmessage.Resource {
	return dnsmessage.Resource{Header: header(z.instance, dnsmessage.TypeTXT, ttl, true), Body: &dnsmessage.TXTResource{TXT: z.txt}}
}

func (z zone) addresses(addrs []net.IP, ttl uint32) []dnsmessage.Resource {
	out := make([]dnsmessage.Resource, 0, len(addrs))
	for _, ip := range addrs {
		if ip4 := ip.To4(); ip4 != nil {
			out = append(out, dnsmessage.Resource{Header: header(z.host, dnsmessage.TypeA, ttl, true), Body: &dnsmessage.AResource{A: [4]byte(ip4)}})
		}
	}
	return out
}

// announcement is every record of the service and its host, as announced (§8.3).
func (z zone) announcement(addrs []net.IP) []dnsmessage.Resource {
	return append([]dnsmessage.Resource{z.ptr(serviceTTL), z.srv(hostTTL), z.text(serviceTTL), z.enumeration(serviceTTL)},
		z.addresses(addrs, hostTTL)...)
}

// goodbye says the service is gone: its records with a TTL of 0 (§10.1). The host's address
// records are left alone: on a Mac the system's own responder answers for the same name.
func (z zone) goodbye() []dnsmessage.Resource {
	return []dnsmessage.Resource{z.ptr(0), z.srv(0), z.text(0), z.enumeration(0)}
}

func sameName(a, b dnsmessage.Name) bool { return strings.EqualFold(a.String(), b.String()) }

// asks reports whether q asks for records of name and type t.
func asks(q dnsmessage.Question, name dnsmessage.Name, t dnsmessage.Type) bool {
	class := q.Class &^ topBit
	return (class == dnsmessage.ClassINET || class == dnsmessage.ClassANY) &&
		(q.Type == t || q.Type == dnsmessage.TypeALL) && sameName(q.Name, name)
}

// known reports whether the query already lists r with at least half its TTL left: the
// client has it, and it is not sent again (known-answer suppression, §7.1).
func known(answers []dnsmessage.Resource, r dnsmessage.Resource) bool {
	p, ok := r.Body.(*dnsmessage.PTRResource)
	if !ok {
		return false
	}
	for _, k := range answers {
		if kp, ok := k.Body.(*dnsmessage.PTRResource); ok && k.Header.Type == dnsmessage.TypePTR &&
			sameName(k.Header.Name, r.Header.Name) && sameName(kp.PTR, p.PTR) && k.Header.TTL >= r.Header.TTL/2 {
			return true
		}
	}
	return false
}

// answer is the reply to a query: the records asked for, and in the additional section the
// ones a client asks for next (RFC 6763 §12). Both are empty when nothing asked for is ours.
// addrs are the host's addresses on the network the query came from.
func (z zone) answer(q dnsmessage.Message, addrs []net.IP) (answers, extra []dnsmessage.Resource) {
	var srv, txt, host bool // what goes in the additional section
	for _, question := range q.Questions {
		switch {
		case asks(question, z.service, dnsmessage.TypePTR):
			if r := z.ptr(serviceTTL); !known(q.Answers, r) {
				answers = append(answers, r)
				srv, txt, host = true, true, true
			}
		case asks(question, servicesName, dnsmessage.TypePTR):
			if r := z.enumeration(serviceTTL); !known(q.Answers, r) {
				answers = append(answers, r)
			}
		case sameName(question.Name, z.instance):
			if asks(question, z.instance, dnsmessage.TypeSRV) {
				answers = append(answers, z.srv(hostTTL))
				host = true
			}
			if asks(question, z.instance, dnsmessage.TypeTXT) {
				answers = append(answers, z.text(serviceTTL))
			}
		case asks(question, z.host, dnsmessage.TypeA):
			answers = append(answers, z.addresses(addrs, hostTTL)...)
		}
	}
	if len(answers) == 0 {
		return nil, nil
	}
	has := func(name dnsmessage.Name, t dnsmessage.Type) bool {
		for _, a := range answers {
			if a.Header.Type == t && sameName(a.Header.Name, name) {
				return true
			}
		}
		return false
	}
	if srv && !has(z.instance, dnsmessage.TypeSRV) {
		extra = append(extra, z.srv(hostTTL))
		host = true
	}
	if txt && !has(z.instance, dnsmessage.TypeTXT) {
		extra = append(extra, z.text(serviceTTL))
	}
	if host && !has(z.host, dnsmessage.TypeA) {
		extra = append(extra, z.addresses(addrs, hostTTL)...)
	}
	return answers, extra
}

// reply is the message that answers q, which came from src: a multicast response; a unicast one
// when a question asks for it; or for a client that is not an mDNS one (it asked from another
// port) a unicast reply that echoes its id and questions, with short TTLs and no cache-flush
// bits (§6.7). to is where to send it: nil for the multicast group. ok is false when there is
// nothing to say.
func (z zone) reply(q dnsmessage.Message, src *net.UDPAddr, addrs []net.IP) (m dnsmessage.Message, to *net.UDPAddr, ok bool) {
	if q.Header.Response || q.Header.OpCode != 0 {
		return m, nil, false
	}
	answers, extra := z.answer(q, addrs)
	if len(answers) == 0 {
		return m, nil, false
	}
	m = dnsmessage.Message{Header: dnsmessage.Header{Response: true, Authoritative: true}, Answers: answers, Additionals: extra}
	if src != nil && src.Port != mdnsPort {
		m.Header.ID = q.Header.ID
		m.Questions = q.Questions
		for _, rs := range [][]dnsmessage.Resource{m.Answers, m.Additionals} {
			for i := range rs {
				rs[i].Header.TTL = min(rs[i].Header.TTL, legacyTTL)
				rs[i].Header.Class &^= topBit
			}
		}
		return m, src, true
	}
	for _, question := range q.Questions {
		if question.Class&topBit != 0 && src != nil {
			return m, src, true
		}
	}
	return m, nil, true
}

// probe is a query for the instance name, with the records it would claim (§8.1). The first
// asks for a unicast answer.
func (z zone) probe(first bool) dnsmessage.Message {
	class := dnsmessage.ClassINET
	if first {
		class |= topBit
	}
	return dnsmessage.Message{
		Questions:   []dnsmessage.Question{{Name: z.instance, Type: dnsmessage.TypeALL, Class: class}},
		Authorities: []dnsmessage.Resource{z.srv(hostTTL), z.text(serviceTTL)},
	}
}

// claims reports whether a response from another responder holds a record of the instance
// name: while probing, the name is someone else's.
func (z zone) claims(m dnsmessage.Message) bool {
	if !m.Header.Response {
		return false
	}
	for _, rs := range [][]dnsmessage.Resource{m.Answers, m.Additionals} {
		for _, r := range rs {
			if r.Header.TTL > 0 && sameName(r.Header.Name, z.instance) { // a goodbye claims nothing
				return true
			}
		}
	}
	return false
}

// renamed is the instance name to try when name is taken: "Living room (2)", then (3), ...
func renamed(base string, n int) string { return fitLabel(base, fmt.Sprintf(" (%d)", n)) }

// named reports whether q asks about a name of z: the only queries worth answering.
func (z zone) named(q dnsmessage.Message) bool {
	for _, question := range q.Questions {
		for _, n := range []dnsmessage.Name{z.service, servicesName, z.instance, z.host} {
			if sameName(question.Name, n) {
				return true
			}
		}
	}
	return false
}

// hostLabel is the host name the service points at (target of the SRV record, owner of the A
// records): the first label of the machine's name, as the system's own responder uses it.
func hostLabel() string {
	h, _ := os.Hostname()
	h, _, _ = strings.Cut(h, ".")
	if h == "" {
		return "ply"
	}
	return fitLabel(h, "")
}

// lanIPs are the IPv4 addresses of ifi, when it is up and not loopback: the addresses clients on
// its network can use.
func lanIPs(ifi *net.Interface) []net.IP {
	if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
		return nil
	}
	addrs, err := ifi.Addrs()
	if err != nil {
		return nil
	}
	var out []net.IP
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() {
			out = append(out, n.IP.To4())
		}
	}
	return out
}

// multicastIfaces are the interfaces mDNS is spoken on: up, multicast, not loopback, with an
// IPv4 address.
func multicastIfaces() []net.Interface {
	all, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []net.Interface
	for _, i := range all {
		if i.Flags&net.FlagUp != 0 && i.Flags&net.FlagMulticast != 0 && i.Flags&net.FlagLoopback == 0 && len(lanIPs(&i)) > 0 {
			out = append(out, i)
		}
	}
	return out
}

// responder speaks mDNS for one zone on one socket.
type responder struct {
	pc      *ipv4.PacketConn
	ifaces  []net.Interface // fixed at start, as the system's group memberships are
	renames int             // maxRenames when it was made

	mu        sync.Mutex
	z         zone
	probing   bool
	conflict  bool
	announced bool // something was said that a goodbye must take back

	// live is held (read) while a query is answered, and taken (write) by shutdown to set closed:
	// nothing is answered after the goodbye.
	live   sync.RWMutex
	closed bool

	wmu sync.Mutex // one write at a time: the outgoing interface is set before each

	stop    chan struct{}
	claimed sync.WaitGroup // claim
	done    sync.WaitGroup // serve
	once    sync.Once
}

func newResponder(z zone) (*responder, error) {
	ifaces := multicastIfaces()
	if len(ifaces) == 0 {
		return nil, errors.New("no network interface for multicast")
	}
	conn, err := listenMDNS()
	if err != nil {
		return nil, err
	}
	pc := ipv4.NewPacketConn(conn)
	var joined []net.Interface
	for _, ifi := range ifaces {
		if pc.JoinGroup(&ifi, &net.UDPAddr{IP: mdnsGroup.IP}) == nil {
			joined = append(joined, ifi)
		}
	}
	if len(joined) == 0 {
		conn.Close()
		return nil, errors.New("could not join the mDNS group on any interface")
	}
	_ = pc.SetMulticastTTL(255)                        // §11
	_ = pc.SetMulticastLoopback(true)                  // a client on this machine sees the service too
	_ = pc.SetControlMessage(ipv4.FlagInterface, true) // which network a query came from
	return &responder{pc: pc, ifaces: joined, renames: maxRenames, z: z, probing: true, stop: make(chan struct{})}, nil
}

// start answers queries and claims the name, in the background. base is the name as asked for.
func (r *responder) start(base string) {
	r.done.Add(1)
	go r.serve()
	r.claimed.Add(1)
	go r.claim(base)
}

// send writes m to dst (the group when nil) out of ifi (when not nil).
func (r *responder) send(m dnsmessage.Message, ifi *net.Interface, dst *net.UDPAddr) {
	b, err := m.Pack()
	if err != nil {
		return // newZone checked the names: nothing else can fail to pack
	}
	if dst == nil {
		dst = mdnsGroup
	}
	r.wmu.Lock()
	defer r.wmu.Unlock()
	if ifi != nil {
		_ = r.pc.SetMulticastInterface(ifi)
	}
	_, _ = r.pc.WriteTo(b, nil, dst)
}

// everywhere sends what build makes for each interface (its addresses differ) to the group.
func (r *responder) everywhere(build func(addrs []net.IP) dnsmessage.Message) {
	for i := range r.ifaces {
		r.send(build(lanIPs(&r.ifaces[i])), &r.ifaces[i], nil)
	}
}

func (r *responder) serve() {
	defer r.done.Done()
	buf := make([]byte, 9000) // §17
	for {
		n, cm, src, err := r.pc.ReadFrom(buf)
		if errors.Is(err, net.ErrClosed) {
			return
		}
		if err != nil { // a datagram that could not be read: the next one may be
			if !r.sleep(10 * time.Millisecond) {
				return
			}
			continue
		}
		var m dnsmessage.Message
		if m.Unpack(buf[:n]) != nil {
			continue
		}
		r.handle(m, cm, src)
	}
}

func (r *responder) handle(m dnsmessage.Message, cm *ipv4.ControlMessage, from net.Addr) {
	r.live.RLock()
	defer r.live.RUnlock()
	r.mu.Lock()
	z, probing := r.z, r.probing
	if probing && z.claims(m) {
		r.conflict = true
	}
	r.mu.Unlock()
	// Not while the name is not ours yet, nor once the goodbye is said; and only a query that
	// names something of ours is worth looking up the addresses for.
	if probing || r.closed || m.Header.Response || !z.named(m) {
		return
	}
	// Answer on the network the query came from. One that came from elsewhere (looped back from
	// this machine, seen as the loopback interface) is answered on every network: only those hear
	// the goodbye, so nothing is said where it could not be taken back.
	targets := r.ifaces
	if cm != nil {
		if i := slices.IndexFunc(r.ifaces, func(ifi net.Interface) bool { return ifi.Index == cm.IfIndex }); i >= 0 {
			targets = r.ifaces[i : i+1]
		}
	}
	src, _ := from.(*net.UDPAddr)
	for i := range targets {
		reply, to, ok := z.reply(m, src, lanIPs(&targets[i]))
		if !ok {
			return
		}
		r.send(reply, &targets[i], to)
		if to != nil {
			return // a unicast reply goes once
		}
	}
}

// claim probes for the instance name, taking the next one while another responder answers
// for it, then announces the service. base is the name as asked for. When every name it tries
// is taken, it says nothing: the service is not advertised.
func (r *responder) claim(base string) {
	defer r.claimed.Done()
	for n := 1; ; n++ {
		r.mu.Lock()
		r.probing, r.conflict = true, false
		z := r.z
		r.mu.Unlock()
		for i := range probes {
			r.everywhere(func([]net.IP) dnsmessage.Message { return z.probe(i == 0) })
			if !r.sleep(probeWait) {
				return
			}
		}
		r.mu.Lock()
		taken := r.conflict
		if !taken {
			r.probing, r.announced = false, true
		}
		r.mu.Unlock()
		if !taken {
			break
		}
		if n >= r.renames {
			return
		}
		next, err := z.withInstance(renamed(base, n+1))
		if err != nil {
			return
		}
		r.mu.Lock()
		r.z = next
		r.mu.Unlock()
	}
	z := r.zoneNow()
	for i := range 2 {
		select {
		case <-r.stop:
			return
		default:
		}
		r.everywhere(func(addrs []net.IP) dnsmessage.Message {
			return dnsmessage.Message{Header: dnsmessage.Header{Response: true, Authoritative: true}, Answers: z.announcement(addrs)}
		})
		if i == 0 && !r.sleep(announceGap) {
			return
		}
	}
}

func (r *responder) zoneNow() zone {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.z
}

func (r *responder) sleep(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-r.stop:
		return false
	}
}

// shutdown stops claiming and answering, says goodbye (when anything was announced) and closes
// the socket.
func (r *responder) shutdown() {
	r.once.Do(func() {
		close(r.stop)
		r.claimed.Wait()
		r.live.Lock()
		r.closed = true
		r.live.Unlock()
		r.mu.Lock()
		z, announced := r.z, r.announced
		r.mu.Unlock()
		if announced {
			r.everywhere(func([]net.IP) dnsmessage.Message {
				return dnsmessage.Message{Header: dnsmessage.Header{Response: true, Authoritative: true}, Answers: z.goodbye()}
			})
		}
		r.pc.Close()
		r.done.Wait()
	})
}
