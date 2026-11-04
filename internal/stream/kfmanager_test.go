package stream

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// A scan is allowed what ExtractTimeout says, or what a share that gives a
// megabyte a second needs to read the file if that is more, and a third of
// ExtractTimeout more; never more than 6 hours.
func TestScanTimeout(t *testing.T) {
	const mb, gb = 1 << 20, 1 << 30
	min15 := 15 * time.Minute
	for _, c := range []struct {
		name string
		base time.Duration
		size int64
		want time.Duration
	}{
		{"no file", min15, 0, 20 * time.Minute},
		{"a small file", min15, 100 * mb, 20 * time.Minute},
		{"900 MB is the 15 minutes", min15, 900 * mb, 20 * time.Minute},
		{"1 GB", min15, 1 * gb, 1024*time.Second + 5*time.Minute},
		{"2.2 GB", min15, 2200 * mb, 2200*time.Second + 5*time.Minute},
		{"20 GB", min15, 20 * gb, 20*1024*time.Second + 5*time.Minute},
		{"21 GB is capped", min15, 21 * gb, 6 * time.Hour},
		{"a terabyte is capped", min15, 1024 * gb, 6 * time.Hour},
		{"a short timeout is a third more", 300 * time.Millisecond, 0, 400 * time.Millisecond},
		{"a timeout above the cap is kept", 8 * time.Hour, 100 * gb, 8 * time.Hour},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := scanTimeout(c.base, c.size); got != c.want {
				t.Errorf("scanTimeout(%v, %d) = %v, want %v", c.base, c.size, got, c.want)
			}
		})
	}
}

// The manager gives the scan of a big file the time it needs: here a scan of 1.2 s
// with an ExtractTimeout of 0.3 s works on a file of 5 GB (a sparse one) and not on
// a small one.
func TestScanOfABigFileGetsMoreTime(t *testing.T) {
	scan, _ := slowScan(t, "1.2", 3, 2)
	m, _ := newGuardRig(t).manager(t, nil, scan, func(o *Options) { o.ExtractTimeout = 300 * time.Millisecond })
	dir := t.TempDir()
	big, small := filepath.Join(dir, "big.ts"), filepath.Join(dir, "small.ts")
	for p, size := range map[string]int64{big: 5 << 30, small: 1 << 10} {
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(size); err != nil {
			t.Skip("no sparse files here:", err)
		}
		f.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if kf, _, err := m.keyframes(ctx, Request{Path: big, Container: "ts"}, true); err != nil || len(kf) != 3 {
		t.Errorf("the big file: %v, %v", kf, err)
	}
	if _, _, err := m.keyframes(ctx, Request{Path: small, Container: "ts"}, true); err == nil {
		t.Error("the small file had more time than ExtractTimeout")
	}
}

// The index of a file is distrusted as the file was: another size or modification
// time is another file, and the next session reads its index again.
func TestDistrustIsOfTheFileAsItWas(t *testing.T) {
	m, _ := newGuardRig(t).manager(t, nil, "/nonexistent/ffprobe", nil)
	dir := t.TempDir()
	p, other := filepath.Join(dir, "film.mkv"), filepath.Join(dir, "other.mkv")
	for _, f := range []string{p, other} {
		if err := os.WriteFile(f, []byte("0123456789"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if m.indexDistrusted(p) {
		t.Fatal("distrusted before anything was found wrong")
	}
	m.distrustIndex(p)
	if !m.indexDistrusted(p) {
		t.Error("not distrusted after it was found wrong")
	}
	if m.indexDistrusted(other) {
		t.Error("another file is distrusted")
	}

	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(p, later, later); err != nil {
		t.Fatal(err)
	}
	if m.indexDistrusted(p) {
		t.Error("still distrusted after the file was touched")
	}

	m.distrustIndex(p)
	if !m.indexDistrusted(p) {
		t.Fatal("not distrusted again")
	}
	if n := len(m.kfDistrust); n != 1 {
		t.Errorf("%d entries: the file as it was is still in", n)
	}
	st, _ := os.Stat(p)
	if err := os.WriteFile(p, []byte("0123456789abcdef"), 0o644); err != nil { // another size
		t.Fatal(err)
	}
	if err := os.Chtimes(p, st.ModTime(), st.ModTime()); err != nil { // and the same time
		t.Fatal(err)
	}
	if m.indexDistrusted(p) {
		t.Error("still distrusted after the file changed size")
	}
}

// ... and only for a day.
func TestDistrustExpires(t *testing.T) {
	m, _ := newGuardRig(t).manager(t, nil, "/nonexistent/ffprobe", nil)
	now := time.Now()
	m.clock = func() time.Time { return now }
	p := filepath.Join(t.TempDir(), "film.mkv")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.distrustIndex(p)
	now = now.Add(24*time.Hour - time.Minute)
	if !m.indexDistrusted(p) {
		t.Error("not distrusted a minute short of a day")
	}
	now = now.Add(time.Minute)
	if m.indexDistrusted(p) {
		t.Error("still distrusted after a day")
	}
	if n := len(m.kfDistrust); n != 0 {
		t.Errorf("%d entries left", n)
	}
}

// The lock on the writes of the cache is of a file, whichever session wants it,
// and is gone when nobody holds or waits for it.
func TestIndexCacheLockIsPerFile(t *testing.T) {
	m, _ := newGuardRig(t).manager(t, nil, "/nonexistent/ffprobe", nil)
	unlock := m.lockIndexCache("/media/a.mkv")

	got := make(chan string, 2)
	go func() { m.lockIndexCache("/media/a.mkv")(); got <- "same file" }()
	go func() { m.lockIndexCache("/media/b.mkv")(); got <- "another file" }()
	select {
	case g := <-got:
		if g != "another file" {
			t.Errorf("%q first", g)
		}
	case <-time.After(5 * time.Second):
		t.Error("the lock of one file held up another")
	}
	select {
	case g := <-got:
		t.Errorf("%q got the lock while it was held", g)
	case <-time.After(200 * time.Millisecond):
	}
	unlock()
	select {
	case g := <-got:
		if g != "same file" && g != "another file" {
			t.Errorf("got %q", g)
		}
	case <-time.After(5 * time.Second):
		t.Error("the lock was not given up")
	}
	m.kfMu.Lock()
	n := len(m.kfLocks)
	m.kfMu.Unlock()
	if n != 0 {
		t.Errorf("%d locks left", n)
	}
}

// gateCache holds the write of an index (a non-empty one) until the gate is
// opened, as a store does that is busy; drops go through.
type gateCache struct {
	*recCache
	entered chan struct{}
	gate    chan struct{}
	once    sync.Once
}

func newGateCache() *gateCache {
	return &gateCache{recCache: &recCache{}, entered: make(chan struct{}, 8), gate: make(chan struct{})}
}

func (c *gateCache) open() { c.once.Do(func() { close(c.gate) }) }

func (c *gateCache) SetKeyframes(ctx context.Context, id int64, kf []float64) error {
	if len(kf) > 0 && len(kf) == 60 { // the index; a scan of 30 goes through
		c.entered <- struct{}{}
		<-c.gate
	}
	return c.recCache.SetKeyframes(ctx, id, kf)
}

// A session confirmed the index, and the write to the cache is under way when it
// is found wrong further on: the drop comes after the write, not before it, or the
// wrong index would be left in the cache.
func TestIndexCacheDropFollowsTheWriteOfTheIndex(t *testing.T) {
	rig := newGuardRig(t)
	wrong := every(60, 0, 2)
	wrong[7] = 15 // the plan has 14 s
	rig.fragments(t, 1, wrong)
	rig.hold(t, 5)
	cache := newGateCache()
	m, _ := rig.manager(t, cache, "/nonexistent/ffprobe", nil)
	t.Cleanup(cache.open) // before the manager is closed: its goroutines wait for the store
	s, err := m.Create(context.Background(), indexedRequest(indexedFile(t), 7))
	if err != nil {
		t.Fatal(err)
	}
	rig.open(t)
	playlistOf(t, s)
	select {
	case <-cache.entered: // the write of the index is in the store
	case <-time.After(5 * time.Second):
		t.Fatal("the index was not written")
	}

	rig.release(t, 5) // the fragment that shows it wrong
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	waitUntil(t, "the session to fail", 5*time.Second, func() bool {
		md, err := s.OpenSegment(ctx, 0)
		if err == nil {
			md.Close()
		}
		return err != nil
	})
	time.Sleep(300 * time.Millisecond) // the drop would be done by now, if it did not wait
	cache.open()

	waitUntil(t, "the index and then its drop", 5*time.Second, func() bool { return len(cache.of(7)) == 2 })
	w := cache.of(7)
	if len(w[0]) != 60 || len(w[1]) != 0 {
		t.Errorf("writes to the cache: %d then %d keyframes, want the index and then its drop", len(w[0]), len(w[1]))
	}
	cache.mu.Lock()
	left := len(cache.kf[7])
	cache.mu.Unlock()
	if left != 0 {
		t.Errorf("%d keyframes of a wrong index are in the cache", left)
	}
}

// ... whichever session finds the index wrong: another one, refuted at its first
// fragments, drops what is in the cache before it scans, and that is after the
// write of the first, which is under way.
func TestIndexCacheDropOfAnotherSessionFollowsTheWrite(t *testing.T) {
	rig := newGuardRig(t)
	wrong := every(60, 0, 2)
	wrong[1] = 1.3
	rig.fragments(t, 1, every(60, 0, 2)) // the first session: right
	rig.fragments(t, 2, wrong)           // the second: refuted
	rig.fragments(t, 3, every(30, 0, 4)) // the second, on the plan of the scan
	cache := newGateCache()
	m, logs := rig.manager(t, cache, scanScript(t, 30, 4), nil)
	t.Cleanup(cache.open) // before the manager is closed: its goroutines wait for the store
	path := indexedFile(t)

	a, err := m.Create(context.Background(), indexedRequest(path, 7))
	if err != nil {
		t.Fatal(err)
	}
	started := func() int { // starts() of an empty log is one empty line
		b, _ := os.ReadFile(rig.log)
		return strings.Count(string(b), "\n")
	}
	waitUntil(t, "the first ffmpeg", 5*time.Second, func() bool { return started() == 1 })
	time.Sleep(100 * time.Millisecond) // it has counted itself
	b, err := m.Create(context.Background(), indexedRequest(path, 7))
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the second ffmpeg", 5*time.Second, func() bool { return started() == 2 })
	if a == b {
		t.Fatal("one session")
	}

	// The first confirms: its write of the index is held in the store. The second
	// writes nothing yet.
	rig.holdStart(t, 2, 0)
	rig.open(t)
	playlistOf(t, a)
	select {
	case <-cache.entered:
	case <-time.After(5 * time.Second):
		t.Fatalf("the index was not written:\n%s", logs.String())
	}
	// The second is refuted, and wants to drop the cache and scan.
	rig.releaseStart(t, 2, 0)
	waitUntil(t, "the second session to be refuted", 5*time.Second, func() bool { return strings.Contains(logs.String(), "disagrees") })
	time.Sleep(300 * time.Millisecond) // the drop and the scan would be done by now, if the write did not hold them up
	cache.open()
	playlistOf(t, b)
	time.Sleep(300 * time.Millisecond)

	cache.mu.Lock()
	left := cache.kf[7]
	cache.mu.Unlock()
	if len(left) != 30 {
		t.Errorf("%d keyframes in the cache at the end, want the 30 of the scan; writes: %d", len(left), len(cache.of(7)))
	}
}
