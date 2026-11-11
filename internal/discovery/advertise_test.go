package discovery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/grandcat/zeroconf"
)

// TestAdvertiseAnnouncesTheService registers the service the way the server
// does and browses for it with the zeroconf client: the instance (dots turned
// to dashes, they would split it into labels), the port and the TXT records
// (id, v=1, the name as typed and the API versions) are what a client such as
// the iOS app sees.
//
// mDNS is multicast on the local network, and nothing here can fake that. A
// reference service registered straight through zeroconf tells the two ways of
// not seeing ours apart: if even the reference is not seen there is no
// multicast here (a sandbox, no route) and the test is skipped; if the
// reference is seen and ours is not, Advertise is wrong and the test fails.
// Both use throwaway instance names and are shut down at the end.
func TestAdvertiseAnnouncesTheService(t *testing.T) {
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	tag := hex.EncodeToString(suffix)
	name := "ply.test." + tag // dots on purpose
	instance, refInstance := "ply-test-"+tag, "ply-ref-"+tag
	id := "0123456789abcdef0123456789abcdef"

	ln, err := net.Listen("tcp", "127.0.0.1:0") // a free port to announce
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	adv, err := Advertise(name, id, port, 3, 2)
	if err != nil {
		t.Skipf("mDNS is not available here: %v", err)
	}
	defer adv.Shutdown()
	ref, err := zeroconf.Register(refInstance, ServiceType, "local.", port, []string{"ref=1"}, nil)
	if err != nil {
		t.Skipf("mDNS is not available here: %v", err)
	}
	defer ref.Shutdown()

	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		t.Skipf("no mDNS client here: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	entries := make(chan *zeroconf.ServiceEntry)
	if err := resolver.Browse(ctx, ServiceType, "local.", entries); err != nil {
		t.Skipf("cannot browse mDNS here: %v", err)
	}
	seen := map[string]*zeroconf.ServiceEntry{}
	for finished := false; !finished; {
		select {
		case e, ok := <-entries:
			if !ok {
				finished = true
				break
			}
			seen[e.Instance] = e
			finished = seen[instance] != nil && seen[refInstance] != nil
		case <-ctx.Done():
			finished = true
		}
	}
	cancel()
	go func() {
		for range entries { // let the browse goroutine finish
		}
	}()

	if seen[refInstance] == nil {
		t.Skip("not even the reference service was seen within 8 s: no multicast on this machine")
	}
	e := seen[instance]
	if e == nil {
		t.Fatalf("the reference service was seen but Advertise(%q) was not (instance %q); seen: %v", name, instance, keys(seen))
	}
	if e.Port != port {
		t.Errorf("port = %d, want %d", e.Port, port)
	}
	if e.Service != "_ply._tcp" { // the name clients browse for: part of the protocol
		t.Errorf("service = %q, want _ply._tcp", e.Service)
	}
	want := []string{"api=3", "apiMin=2", "id=" + id, "name=" + name, "v=1"}
	got := slices.Clone(e.Text)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("TXT records = %q, want %q", got, want)
	}
}

// The TXT records, without the network: the multicast test above skips where
// there is no multicast.
func TestTXTRecords(t *testing.T) {
	got := txtRecords("Living room", "abc", 1, 1)
	want := []string{"id=abc", "v=1", "name=Living room", "api=1", "apiMin=1"}
	if !slices.Equal(got, want) {
		t.Fatalf("TXT records = %q, want %q", got, want)
	}
}

func keys(m map[string]*zeroconf.ServiceEntry) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
