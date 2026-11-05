package tier

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakePlayback is a PlaybackLoad that a test sets.
type fakePlayback struct {
	mu sync.Mutex
	l  PlaybackLoad
}

func (f *fakePlayback) set(l PlaybackLoad) {
	f.mu.Lock()
	f.l = l
	f.mu.Unlock()
}

func (f *fakePlayback) PlaybackLoad() PlaybackLoad {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.l
}

// devices maps path prefixes to disks: "/nas/..." is "nas", "/gone..." and "/"
// cannot be read, anything else is "local".
func devices(path string) (string, bool) {
	if strings.HasPrefix(path, "/nas/") || path == "/nas" {
		return "nas", true
	}
	if strings.HasPrefix(path, "/gone") || path == "/" {
		return "", false
	}
	return "local", true
}

func newGov(pb *fakePlayback) *Governor {
	return New(Options{Playback: pb, Disk: devices, Poll: 5 * time.Millisecond, BrowseQuiet: 40 * time.Millisecond})
}

func TestCheck(t *testing.T) {
	cpu := Need{CPU: true}
	nasScan := Need{Disks: []string{"/nas/Movies"}}
	localScan := Need{Disks: []string{"/home/me/Movies"}}
	light := Need{}
	cases := []struct {
		name string
		pb   PlaybackLoad
		need Need
		held bool
		why  string
	}{
		{"idle: everything runs", PlaybackLoad{}, Need{CPU: true, Disks: []string{"/nas/Movies"}}, false, ""},
		{"a stalled viewer holds even light work", PlaybackLoad{Sessions: 1, Waiting: 1}, light, true, "waiting for video"},
		{"a software transcode holds CPU work", PlaybackLoad{Sessions: 1, Transcodes: 1, Software: 1}, cpu, true, "on the CPU"},
		{"…but not light work", PlaybackLoad{Sessions: 1, Transcodes: 1, Software: 1}, light, false, ""},
		{"a hardware transcode leaves the CPU to the queue", PlaybackLoad{Sessions: 1, Transcodes: 1}, cpu, false, ""},
		{"a play from the NAS holds a scan of the NAS", PlaybackLoad{Sessions: 1, Paths: []string{"/nas/Movies/a.mkv"}}, nasScan, true, "same disk"},
		{"…but not a scan of another disk", PlaybackLoad{Sessions: 1, Paths: []string{"/nas/Movies/a.mkv"}}, localScan, false, ""},
		{"…nor work that reads no disk", PlaybackLoad{Sessions: 1, Paths: []string{"/nas/Movies/a.mkv"}}, cpu, false, ""},
		{"an unknown device is not the same disk", PlaybackLoad{Sessions: 1, Paths: []string{"/gone/a.mkv"}}, Need{Disks: []string{"/gone/b"}}, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pb := &fakePlayback{}
			pb.set(c.pb)
			h := newGov(pb).Check(c.need)
			if h.Held != c.held || !strings.Contains(h.Reason, c.why) {
				t.Fatalf("Check = %+v, want held=%v reason ~%q", h, c.held, c.why)
			}
		})
	}
}

func TestNilPlaybackHoldsNothing(t *testing.T) {
	if h := New(Options{}).Check(Need{CPU: true, Disks: []string{"/"}}); h.Held {
		t.Fatalf("held with nothing playing: %+v", h)
	}
}

func TestBrowsingHoldsUntilQuiet(t *testing.T) {
	g := newGov(&fakePlayback{})
	release := make(chan struct{})
	entered := make(chan struct{})
	mw := g.Track(func(r *http.Request) bool { return strings.HasPrefix(r.URL.Path, "/api/items") })
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/items" {
			close(entered)
			<-release
		}
	}))

	// A request that is not browsing (the Activity page's polling) holds nothing.
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/jobs", nil))
	if hold := g.Check(Need{}); hold.Held {
		t.Fatalf("polling counted as browsing: %+v", hold)
	}

	go h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/items", nil))
	<-entered
	if hold := g.Check(Need{}); !hold.Held || !strings.Contains(hold.Reason, "browsing") {
		t.Fatalf("in-flight browse request: %+v", hold)
	}
	close(release)
	// Still held just after it ends (the quiet time), free once that has passed.
	deadline := time.Now().Add(2 * time.Second)
	sawHeldAfter := false
	for g.browse.inflight.Load() > 0 {
		time.Sleep(time.Millisecond)
	}
	if g.Check(Need{}).Held {
		sawHeldAfter = true
	}
	for g.Check(Need{}).Held {
		if time.Now().After(deadline) {
			t.Fatal("still held long after the last browse request")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !sawHeldAfter {
		t.Fatal("not held right after a browse request ended: the quiet time is not applied")
	}
}

func TestWaitReturnsWhenPlaybackLetsGo(t *testing.T) {
	pb := &fakePlayback{}
	pb.set(PlaybackLoad{Sessions: 1, Waiting: 1})
	g := New(Options{Playback: pb, Disk: devices, Poll: time.Hour}) // only Notify can wake it
	var reasons []string
	var mu sync.Mutex
	done := make(chan error, 1)
	go func() {
		done <- g.Wait(context.Background(), Need{}, func(r string) {
			mu.Lock()
			reasons = append(reasons, r)
			mu.Unlock()
		})
	}()
	select {
	case err := <-done:
		t.Fatalf("Wait returned while a viewer waits: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	pb.set(PlaybackLoad{Sessions: 1})
	g.Notify()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Notify did not wake Wait")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reasons) != 2 || !strings.Contains(reasons[0], "waiting for video") || reasons[1] != "" {
		t.Fatalf("held reports = %q, want [the reason, \"\"]", reasons)
	}
}

// Browsing that never pauses (a client polling the library) lets a held job take
// a step after BrowseMaxHold; a stalled viewer holds it for as long as it lasts.
func TestBrowsingCannotStarveTheQueueButPlaybackCan(t *testing.T) {
	pb := &fakePlayback{}
	g := New(Options{Playback: pb, Disk: devices, Poll: 5 * time.Millisecond, BrowseMaxHold: 60 * time.Millisecond})
	g.browse.inflight.Add(1) // a browse request that never ends
	defer g.browse.inflight.Add(-1)

	start := time.Now()
	if err := g.Wait(context.Background(), Need{CPU: true}, nil); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 60*time.Millisecond {
		t.Fatalf("returned after %v: browsing did not hold at all", d)
	}

	pb.set(PlaybackLoad{Sessions: 1, Waiting: 1}) // and now a viewer is stalled too
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := g.Wait(ctx, Need{CPU: true}, nil); err != context.DeadlineExceeded {
		t.Fatalf("Wait = %v: a stalled viewer must hold past BrowseMaxHold", err)
	}
}

func TestWaitNotHeldReportsNothing(t *testing.T) {
	g := newGov(&fakePlayback{})
	called := false
	if err := g.Wait(context.Background(), Need{CPU: true}, func(string) { called = true }); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("held was told about a hold that never happened")
	}
}

func TestWaitEndsWithItsContext(t *testing.T) {
	pb := &fakePlayback{}
	pb.set(PlaybackLoad{Waiting: 1})
	g := newGov(pb)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := g.Wait(ctx, Need{}, nil); err != context.DeadlineExceeded {
		t.Fatalf("Wait = %v, want the context's error", err)
	}
}

func TestDevicesAreRemembered(t *testing.T) {
	var calls atomic.Int32
	pb := &fakePlayback{}
	pb.set(PlaybackLoad{Sessions: 1, Paths: []string{"/nas/a.mkv"}})
	g := New(Options{Playback: pb, Disk: func(p string) (string, bool) {
		calls.Add(1)
		return devices(p)
	}})
	for range 10 {
		g.Check(Need{Disks: []string{"/nas/Movies"}})
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("Disk called %d times for two paths, want 2", n)
	}
}

// A folder that is not there yet (a share not mounted when ply starts) is taken
// for now as on its parent's disk, but that is not remembered: once the share is
// mounted, the folder is on the share.
func TestAMissingFolderIsLookedAtAgain(t *testing.T) {
	var mounted atomic.Bool
	now := time.Unix(1_800_000_000, 0)
	var clock atomic.Int64
	clock.Store(now.UnixNano())
	pb := &fakePlayback{}
	g := New(Options{Playback: pb, Now: func() time.Time { return time.Unix(0, clock.Load()) },
		Disk: func(p string) (string, bool) {
			switch {
			case strings.HasPrefix(p, "/Volumes/NAS/") || p == "/Volumes/NAS":
				if !mounted.Load() {
					return "", false
				}
				return "net:nas", true
			default:
				return "boot", true
			}
		}})
	scan := Need{Disks: []string{"/Volumes/NAS/Movies"}}

	// Not mounted: the folder counts as on the boot disk.
	pb.set(PlaybackLoad{Sessions: 1, Paths: []string{"/Users/me/clip.mkv"}})
	if !g.Check(scan).Held {
		t.Fatal("an unmounted folder is not taken as on its parent's disk")
	}
	// Mounted, and the retry time has passed: it is on the NAS now.
	mounted.Store(true)
	clock.Add(int64(missingRetry + time.Second))
	if g.Check(scan).Held {
		t.Fatal("the folder is still taken as on the boot disk after its share was mounted")
	}
	pb.set(PlaybackLoad{Sessions: 1, Paths: []string{"/Volumes/NAS/Movies/Alien.mkv"}})
	if !g.Check(scan).Held {
		t.Fatal("a play from the NAS does not hold a scan of the NAS")
	}
}

func TestBrowsingHoldsAreMarked(t *testing.T) {
	g := newGov(&fakePlayback{})
	g.browse.inflight.Add(1)
	defer g.browse.inflight.Add(-1)
	if h := g.Check(Need{}); !h.Held || !h.Browsing {
		t.Fatalf("browsing: %+v", h)
	}
	pb := &fakePlayback{}
	pb.set(PlaybackLoad{Waiting: 1})
	g2 := newGov(pb)
	g2.browse.inflight.Add(1)
	if h := g2.Check(Need{}); !h.Held || h.Browsing {
		t.Fatalf("a stalled viewer while browsing is not a browsing hold: %+v", h)
	}
}

func TestState(t *testing.T) {
	pb := &fakePlayback{}
	pb.set(PlaybackLoad{Sessions: 2, Active: 1, Waiting: 1})
	s := newGov(pb).State()
	if s.Playback.Sessions != 2 || !s.Hold.Held || s.Browsing {
		t.Fatalf("State = %+v", s)
	}
}

func TestDiskOfTheRealDisk(t *testing.T) {
	dir := t.TempDir()
	k, ok := diskOf(dir)
	if !ok || k == "" {
		t.Fatalf("diskOf(%s) = %q, %v", dir, k, ok)
	}
	if _, ok := diskOf(dir + "/not/there/yet"); ok {
		t.Fatal("a missing path must not report a disk (the governor falls back to its parent itself)")
	}
	// Through the governor: a missing child is on its parent's disk.
	pb := &fakePlayback{}
	pb.set(PlaybackLoad{Sessions: 1, Paths: []string{dir + "/clip.mkv"}})
	if !New(Options{Playback: pb}).Check(Need{Disks: []string{dir + "/not/there/yet"}}).Held {
		t.Fatal("a missing folder under a playing disk is not held")
	}
}
