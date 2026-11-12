package discovery

// The AirPlay receivers on the LAN, found with DNS-SD: a browse for _airplay._tcp, then the
// SRV and TXT of each instance it names and the A record of each host (RFC 6762, RFC 6763).
// A play needs a receiver's address and the port it listens on; the API then builds the URLs
// that receiver fetches on the address of ours on its network (docs/AIRPLAY.md, "Network
// setup").
//
// The browse is small on purpose: one query per interface every browseEvery, and a sweep that
// drops what has not answered for receiverTTL. It shares the mDNS port with the system's own
// responder and with ours: every socket joined to the group receives what is multicast.

import (
	"errors"
	"net"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/ipv4"
)

const (
	// AirPlayService is the DNS-SD service an AirPlay receiver, such as an Apple TV,
	// advertises.
	AirPlayService = "_airplay._tcp"

	browseEvery  = 30 * time.Second // between asks for the service
	receiverTTL  = 3 * time.Minute  // a receiver not seen for this long is gone
	maxReceivers = 64               // more than a home needs: bounds what an announcement flood may keep
)

// Receiver is an AirPlay target on the LAN: an Apple TV, or a Mac that accepts AirPlay.
type Receiver struct {
	ID    string   // the device's own id (its deviceid), or its name when it says none
	Name  string   // what people call it: "Living Room Apple TV"
	Model string   // what it says it is: "AppleTV3,2"; "" when it says nothing
	Addrs []net.IP // its IPv4 addresses, one per network it is on
	Port  int      // where its AirPlay service listens
}

// Browser follows the AirPlay receivers on the LAN. Receivers is safe to call from any
// goroutine; Close stops the browse. A nil Browser lists none.
type Browser struct {
	pc      *ipv4.PacketConn
	ifaces  []net.Interface
	service dnsmessage.Name

	mu      sync.Mutex
	entries map[string]*receiverEntry // by instance name
	ask     bool                      // something of a receiver is missing: ask now

	wmu    sync.Mutex // one write at a time: the outgoing interface is set before each
	closed chan struct{}
	done   sync.WaitGroup
	once   sync.Once
}

// receiverEntry is one instance being followed: what its records say so far.
type receiverEntry struct {
	instance dnsmessage.Name
	host     dnsmessage.Name // its SRV target; empty until that record is seen
	port     int
	id       string // TXT deviceid
	model    string // TXT model
	addrs    []net.IP
	seen     time.Time
}

// Browse starts following the AirPlay receivers on the LAN. It fails when there is no network
// to speak mDNS on; the caller may carry on without receivers then.
func Browse() (*Browser, error) {
	ifaces := multicastIfaces()
	if len(ifaces) == 0 {
		return nil, errors.New("no network interface for multicast")
	}
	conn, err := listenMDNS()
	if err != nil {
		return nil, err
	}
	pc := ipv4.NewPacketConn(conn)
	joined := 0
	for i := range ifaces {
		if pc.JoinGroup(&ifaces[i], &net.UDPAddr{IP: mdnsGroup.IP}) == nil {
			joined++
		}
	}
	if joined == 0 {
		conn.Close()
		return nil, errors.New("could not join the mDNS group on any interface")
	}
	_ = pc.SetMulticastTTL(255)       // §11
	_ = pc.SetMulticastLoopback(true) // what a client on this machine asks is seen too
	b := &Browser{
		pc:      pc,
		ifaces:  ifaces,
		service: dnsmessage.MustNewName(AirPlayService + ".local."),
		entries: map[string]*receiverEntry{},
		closed:  make(chan struct{}),
	}
	b.query()
	b.done.Add(2)
	go b.serve()
	go b.poll()
	return b, nil
}

// Receivers are the AirPlay receivers heard from recently, by name. A receiver is listed once
// its address is known: its SRV and one A record.
func (b *Browser) Receivers() []Receiver {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Receiver, 0, len(b.entries))
	for _, e := range b.entries {
		if r, ok := e.receiver(); ok {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (e *receiverEntry) receiver() (Receiver, bool) {
	if e.host.Length == 0 || e.port <= 0 || len(e.addrs) == 0 {
		return Receiver{}, false
	}
	id := e.id
	if id == "" {
		id = e.instance.String()
	}
	return Receiver{ID: id, Name: instanceLabel(e.instance), Model: e.model, Addrs: slices.Clone(e.addrs), Port: e.port}, true
}

// serve reads what responders and receivers multicast, and keeps what is a response.
func (b *Browser) serve() {
	defer b.done.Done()
	buf := make([]byte, 9000)
	for {
		n, _, _, err := b.pc.ReadFrom(buf)
		if err != nil {
			return // closed
		}
		var m dnsmessage.Message
		if m.Unpack(buf[:n]) != nil || !m.Header.Response {
			continue // a query, ours or another client's, says nothing of a receiver
		}
		b.observe(m)
	}
}

// poll asks again now and then, and drops the receivers that stopped answering.
func (b *Browser) poll() {
	defer b.done.Done()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	last := time.Now() // query ran in Browse
	for {
		select {
		case <-b.closed:
			return
		case <-tick.C:
			b.sweep()
			b.mu.Lock()
			ask := b.ask
			b.ask = false
			b.mu.Unlock()
			if ask || time.Since(last) >= browseEvery {
				b.query()
				last = time.Now()
			}
		}
	}
}

// query asks for the instances of the service, and for what a known one still misses.
func (b *Browser) query() {
	b.mu.Lock()
	qs := []dnsmessage.Question{{Name: b.service, Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET}}
	for _, e := range b.entries {
		if e.host.Length == 0 {
			qs = append(qs,
				dnsmessage.Question{Name: e.instance, Type: dnsmessage.TypeSRV, Class: dnsmessage.ClassINET},
				dnsmessage.Question{Name: e.instance, Type: dnsmessage.TypeTXT, Class: dnsmessage.ClassINET})
			continue
		}
		if len(e.addrs) == 0 {
			qs = append(qs, dnsmessage.Question{Name: e.host, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET})
		}
	}
	b.mu.Unlock()

	query := dnsmessage.Message{Questions: qs}
	packed, err := query.Pack()
	if err != nil {
		return
	}
	b.wmu.Lock()
	defer b.wmu.Unlock()
	for i := range b.ifaces {
		_ = b.pc.SetMulticastInterface(&b.ifaces[i])
		_, _ = b.pc.WriteTo(packed, nil, mdnsGroup)
	}
}

// observe keeps every record of the answers and additionals of m.
func (b *Browser) observe(m dnsmessage.Message) {
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, rs := range [][]dnsmessage.Resource{m.Answers, m.Additionals} {
		for _, r := range rs {
			b.record(r, now)
		}
	}
}

// record updates what is known about a receiver from r. A record with a TTL of 0 is a goodbye
// (RFC 6762 §10.1): it takes its record away, and a PTR takes the whole instance with it.
func (b *Browser) record(r dnsmessage.Resource, now time.Time) {
	name := r.Header.Name
	switch body := r.Body.(type) {
	case *dnsmessage.PTRResource:
		if !sameName(name, b.service) {
			return
		}
		key := body.PTR.String()
		if r.Header.TTL == 0 {
			delete(b.entries, key)
			return
		}
		if e := b.entries[key]; e != nil {
			e.seen = now
			return
		}
		if len(b.entries) >= maxReceivers {
			return
		}
		b.entries[key] = &receiverEntry{instance: body.PTR, seen: now}
		b.ask = true // its SRV, TXT and address are still unknown
	case *dnsmessage.SRVResource:
		e := b.entries[name.String()]
		if e == nil {
			return
		}
		if r.Header.TTL == 0 {
			e.host, e.port = dnsmessage.Name{}, 0
			return
		}
		if e.host.Length == 0 || !sameName(e.host, body.Target) || e.port != int(body.Port) {
			b.ask = true // its address is still unknown
		}
		e.host, e.port, e.seen = body.Target, int(body.Port), now
	case *dnsmessage.TXTResource:
		e := b.entries[name.String()]
		if e == nil {
			return
		}
		e.seen = now
		if r.Header.TTL == 0 {
			e.id, e.model = "", ""
			return
		}
		txt := txtMap(body.TXT)
		e.id, e.model = txt["deviceid"], txt["model"]
	case *dnsmessage.AResource:
		ip := net.IP(body.A[:])
		for _, e := range b.entries {
			if e.host.Length == 0 || !sameName(e.host, name) {
				continue
			}
			if r.Header.TTL == 0 {
				e.addrs = slices.DeleteFunc(e.addrs, ip.Equal)
				continue
			}
			if !slices.ContainsFunc(e.addrs, ip.Equal) {
				e.addrs = append(e.addrs, ip)
			}
			e.seen = now
		}
	}
}

// sweep forgets the receivers that have not answered for receiverTTL.
func (b *Browser) sweep() {
	cut := time.Now().Add(-receiverTTL)
	b.mu.Lock()
	defer b.mu.Unlock()
	for k, e := range b.entries {
		if e.seen.Before(cut) {
			delete(b.entries, k)
		}
	}
}

// Close stops the browse. It is safe on a nil Browser.
func (b *Browser) Close() {
	if b == nil {
		return
	}
	b.once.Do(func() {
		close(b.closed)
		_ = b.pc.Close()
	})
	b.done.Wait()
}

// instanceLabel is the name inside a service instance: "Living Room Apple TV" from
// "Living Room Apple TV._airplay._tcp.local.".
func instanceLabel(n dnsmessage.Name) string {
	s := n.String()
	suffix := "." + AirPlayService + ".local."
	if len(s) > len(suffix) && strings.EqualFold(s[len(s)-len(suffix):], suffix) {
		return s[:len(s)-len(suffix)]
	}
	return s
}

// txtMap parses a TXT record's key=value strings, keys in lower case.
func txtMap(txt []string) map[string]string {
	m := make(map[string]string, len(txt))
	for _, kv := range txt {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[strings.ToLower(k)] = v
		}
	}
	return m
}
