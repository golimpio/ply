package stream

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A rescan that takes longer than the time to wait for a fragment is waited
// for: nobody is let go with the plan that was refuted when the deadline passes,
// and the plan is not swapped under anyone. (Under -race: a waiter let go with
// the old plan reads it while the rebuild writes the new.) However many wait,
// the file is scanned once.
func TestPlanGuardWaitsForTheRebuild(t *testing.T) {
	rig := newGuardRig(t)
	wrong := every(60, 0, 2)
	wrong[1] = 1.3
	rig.fragments(t, 1, wrong)
	rig.fragments(t, 2, every(30, 0, 4))
	scan, calls := slowScan(t, "3", 30, 4)
	// The wait for a fragment is over, in the middle of the scan. (Not short enough
	// to be over before the first fragment: ffmpeg can take a while to start.)
	m, logs := rig.manager(t, &recCache{}, scan, func(o *Options) { o.WaitTimeout = 1500 * time.Millisecond })
	s, err := m.Create(context.Background(), indexedRequest(indexedFile(t), 7))
	if err != nil {
		t.Fatal(err)
	}
	rig.open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	type result struct {
		pl  string
		err error
		at  time.Duration
	}
	start := time.Now()
	playlist, segment := make(chan result, 1), make(chan result, 1)
	go func() {
		pl, err := s.Playlist(ctx, "")
		playlist <- result{pl, err, time.Since(start)}
	}()
	go func() {
		md, err := s.OpenSegment(ctx, 1)
		if err == nil {
			md.Close()
		}
		segment <- result{"", err, time.Since(start)}
	}()
	for name, ch := range map[string]chan result{"playlist": playlist, "segment": segment} {
		select {
		case got := <-ch:
			if got.err != nil {
				t.Errorf("%s: %v", name, got.err)
			}
			if got.at < 2900*time.Millisecond { // the scan takes 3 s; the wait for a fragment, 1.5 s
				t.Errorf("%s was answered after %v, before the scan was done", name, got.at)
			}
			if name == "playlist" && strings.Count(got.pl, "#EXTINF") != 15 {
				t.Errorf("%d segments in the playlist, want the 15 of the scan:\n%s", strings.Count(got.pl, "#EXTINF"), got.pl)
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("no %s", name)
		}
	}
	if n := len(s.Plan().Segments); n != 15 {
		t.Errorf("%d segments in the plan", n)
	}
	if n := scansOf(calls); n != 1 {
		t.Errorf("the file was scanned %d times", n)
	}
	if strings.Contains(logs.String(), "not confirmed in time") {
		t.Errorf("the plan was given up on during the rescan:\n%s", logs.String())
	}
}

// A rescan is the manager's, single-flight, and cached when it is done, even if
// everybody who wanted it has left. It does not read the cache, which holds the
// index found wrong, and does not join a read of the index.
func TestManagerRescan(t *testing.T) {
	rig := newGuardRig(t)
	path := indexedFile(t)
	scan, calls := slowScan(t, "0.5", 30, 4)
	cache := &recCache{}
	cache.kf = map[int64][]float64{7: every(60, 0, 2)} // the index that was found wrong
	m, _ := rig.manager(t, cache, scan, nil)
	req := indexedRequest(path, 7)

	// Without force the cached index serves.
	if kf, source, err := m.keyframes(context.Background(), req, false); err != nil || source != "cache" || len(kf) != 60 {
		t.Fatalf("without force: %d keyframes from %q, %v", len(kf), source, err)
	}

	// Three callers, one scan; and the result is the scan's.
	type result struct {
		kf     []float64
		source string
		err    error
	}
	out := make(chan result, 3)
	for i := 0; i < 3; i++ {
		go func() {
			kf, source, err := m.keyframes(context.Background(), req, true)
			out <- result{kf, source, err}
		}()
	}
	for i := 0; i < 3; i++ {
		select {
		case r := <-out:
			if r.err != nil || r.source != sourceScan || !equalTimes(r.kf, every(30, 0, 4)) {
				t.Errorf("forced: %d keyframes from %q, %v", len(r.kf), r.source, r.err)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("no scan")
		}
	}
	if n := scansOf(calls); n != 1 {
		t.Errorf("%d scans for three callers", n)
	}
	if w := cache.of(7); len(w) != 1 || !equalTimes(w[0], every(30, 0, 4)) {
		t.Errorf("the scan was cached as %v", w)
	}

	// A caller that gives up does not stop the scan, nor lose it.
	req2 := indexedRequest(path, 8)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, _, err := m.keyframes(ctx, req2, true); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the caller that left: %v", err)
	}
	waitUntil(t, "the scan nobody waited for to be cached", 10*time.Second, func() bool { return len(cache.of(8)) == 1 })
	if n := scansOf(calls); n != 2 {
		t.Errorf("%d scans in all", n)
	}
}

// The playlist fails, instead of being empty, when the plan cannot be rebuilt;
// so do the other requests.
func TestPlaylistFailsWhenTheRebuildFails(t *testing.T) {
	rig := newGuardRig(t)
	wrong := every(60, 0, 2)
	wrong[1] = 1.3
	rig.fragments(t, 1, wrong)
	m, _ := rig.manager(t, &recCache{}, fakeFFprobe(t, "echo scan failed >&2; exit 1"), nil)
	s, err := m.Create(context.Background(), indexedRequest(indexedFile(t), 7))
	if err != nil {
		t.Fatal(err)
	}
	rig.open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pl, err := s.Playlist(ctx, "")
	if !errors.Is(err, ErrIndex) || pl != "" {
		t.Fatalf("playlist %q, err %v: want ErrIndex and nothing", pl, err)
	}
	if _, err := s.OpenSegment(ctx, 1); !errors.Is(err, ErrIndex) {
		t.Errorf("segment: %v", err)
	}
	if _, err := s.OpenInit(ctx); !errors.Is(err, ErrIndex) {
		t.Errorf("init: %v", err)
	}
}

// A client that has left is not waited for, and the session is not kept alive
// for it.
func TestPlaylistStopsWhenTheClientLeaves(t *testing.T) {
	rig := newGuardRig(t) // never opened: nothing is written, the guard waits
	m, _ := rig.manager(t, nil, "/nonexistent/ffprobe", nil)
	s, err := m.Create(context.Background(), indexedRequest(indexedFile(t), 7))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	pl, err := s.Playlist(ctx, "")
	if !errors.Is(err, context.DeadlineExceeded) || pl != "" {
		t.Fatalf("playlist %q, err %v: want the caller's deadline", pl, err)
	}
	if took := time.Since(start); took > 3*time.Second { // the wait for a fragment is 10 s
		t.Errorf("the playlist took %v", took)
	}
	before := s.lastUsed.Load()
	time.Sleep(300 * time.Millisecond)
	if s.lastUsed.Load() != before {
		t.Error("the session was kept alive after the client left")
	}

	gone, leave := context.WithCancel(context.Background())
	leave()
	if _, err := s.Playlist(gone, ""); !errors.Is(err, context.Canceled) {
		t.Errorf("a caller that was gone already: %v", err)
	}
	if _, err := s.OpenSegment(gone, 0); !errors.Is(err, context.Canceled) {
		t.Errorf("a segment request that was gone already: %v", err)
	}
	if s.lastUsed.Load() != before {
		t.Error("a request that was gone already kept the session alive")
	}
}

// A session deleted while its file is being scanned again ends at once, with no
// error in the log: the scan is the manager's and goes on without it.
func TestDeleteDuringTheRescan(t *testing.T) {
	rig := newGuardRig(t)
	wrong := every(60, 0, 2)
	wrong[1] = 1.3
	rig.fragments(t, 1, wrong)
	m, logs := rig.manager(t, &recCache{}, fakeFFprobe(t, "exec sleep 20"), nil)
	s, err := m.Create(context.Background(), indexedRequest(indexedFile(t), 7))
	if err != nil {
		t.Fatal(err)
	}
	rig.open(t)

	got := make(chan error, 1)
	go func() {
		_, err := s.Playlist(context.Background(), "")
		got <- err
	}()
	waitUntil(t, "the index to be refuted", 10*time.Second, func() bool { return strings.Contains(logs.String(), "disagrees") })
	if !m.Delete(s.ID) {
		t.Fatal("no such session")
	}
	select {
	case err := <-got:
		if !errors.Is(err, ErrClosed) {
			t.Errorf("playlist of a deleted session: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the playlist is still waiting for the scan of a session that is gone")
	}
	time.Sleep(100 * time.Millisecond)
	if l := logs.String(); strings.Contains(l, "level=ERROR") || strings.Contains(l, "scan after a bad index failed") {
		t.Errorf("a session deleted by its client is an error in the log:\n%s", l)
	}
}

// The log says why the index of the container was not used, when it knows.
func TestScanLogSaysWhyTheIndexWasNotUsed(t *testing.T) {
	m, logs := newGuardRig(t).manager(t, nil, "/nonexistent/ffprobe", nil)
	m.logKeyframes("/media/a.mkv", keyframeResult{times: []float64{0, 2}, source: sourceScan, skip: errors.New("cues too sparse")}, time.Second)
	m.logKeyframes("/media/b.ts", keyframeResult{times: []float64{0, 2}, source: sourceScan}, time.Second)
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "index_unusable") || !strings.Contains(lines[0], "cues too sparse") {
		t.Errorf("a scan after an index was refused: %q", lines)
	}
	if len(lines) == 2 && strings.Contains(lines[1], "index_unusable") {
		t.Errorf("a reason that is nothing: %s", lines[1])
	}
}

// An ffmpeg that fails at once is reported at once, as such, not after the wait
// for the plan to be confirmed.
func TestPlanGuardDoesNotWaitForAFailedFFmpeg(t *testing.T) {
	rig := newGuardRig(t)
	failing := filepath.Join(rig.dir, "failing-ffmpeg")
	if err := writeExecutable(failing, "#!/bin/sh\necho 'boom: bad input' >&2\nexit 1\n"); err != nil {
		t.Fatal(err)
	}
	cache := &recCache{}
	m, _ := rig.manager(t, cache, "/nonexistent/ffprobe", func(o *Options) {
		o.FFmpeg = failing
		o.WaitTimeout = 20 * time.Second
	})
	s, err := m.Create(context.Background(), indexedRequest(indexedFile(t), 7))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	start := time.Now()
	if n := strings.Count(playlistOf(t, s), "#EXTINF"); n != 20 { // the plan stands, unconfirmed
		t.Errorf("%d segments in the playlist", n)
	}
	_, err = s.OpenSegment(ctx, 0)
	if !errors.Is(err, ErrFFmpeg) {
		t.Fatalf("segment 0: %v, want ffmpeg failing", err)
	}
	if _, err := s.OpenInit(ctx); !errors.Is(err, ErrFFmpeg) {
		t.Errorf("init: %v, want ffmpeg failing", err)
	}
	if took := time.Since(start); took > 8*time.Second {
		t.Errorf("it took %v: the wait for the plan was not cut short", took)
	}
	if n := cache.total(); n != 0 {
		t.Errorf("%d writes to the cache for an index nothing confirmed", n)
	}
}

// Every fragment is compared with the plan, not only the first ones: an index
// that is wrong further on fails the session (the player has its playlist),
// the cached copy is dropped, and the next session scans.
func TestPlanGuardChecksEveryFragment(t *testing.T) {
	rig := newGuardRig(t)
	wrong := every(60, 0, 2)
	wrong[7] = 15 // the plan has 14 s
	rig.fragments(t, 1, wrong)
	rig.hold(t, 5)
	cache := &recCache{}
	m, logs := rig.manager(t, cache, scanScript(t, 30, 4), nil)
	path := indexedFile(t)
	s, err := m.Create(context.Background(), indexedRequest(path, 7))
	if err != nil {
		t.Fatal(err)
	}
	rig.open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if n := strings.Count(playlistOf(t, s), "#EXTINF"); n != 20 {
		t.Fatalf("%d segments in the playlist", n)
	}
	waitUntil(t, "the index to be cached", 5*time.Second, func() bool { return len(cache.of(7)) == 1 })

	rig.release(t, 5)
	waitUntil(t, "the session to fail", 5*time.Second, func() bool {
		md, err := s.OpenSegment(ctx, 0)
		if err == nil {
			md.Close()
		}
		return errors.Is(err, ErrIndex)
	})
	if _, err := s.OpenInit(ctx); !errors.Is(err, ErrIndex) {
		t.Errorf("init: %v", err)
	}
	if _, err := s.Playlist(ctx, ""); !errors.Is(err, ErrIndex) {
		t.Errorf("playlist: %v", err)
	}
	waitUntil(t, "the cached index to be dropped", 5*time.Second, func() bool {
		w := cache.of(7)
		return len(w) == 2 && len(w[1]) == 0
	})
	if !m.indexDistrusted(path) {
		t.Error("the index of the file is still trusted")
	}
	if !strings.Contains(logs.String(), "after it was confirmed") {
		t.Errorf("no word of it in the log:\n%s", logs.String())
	}

	s2, err := m.Create(ctx, indexedRequest(path, 7))
	if err != nil {
		t.Fatal(err)
	}
	if s2.guard != nil || len(s2.Plan().Keyframes) != 30 {
		t.Errorf("the next session: %d keyframes, a guard: %v", len(s2.Plan().Keyframes), s2.guard != nil)
	}
	if w := cache.of(7); len(w) != 3 || !equalTimes(w[2], every(30, 0, 4)) {
		t.Errorf("writes to the cache: %d", len(w))
	}
}

// A bad fragment behind a good pair, all there before anyone looked, is found
// before the plan is confirmed: the plan is rebuilt, and nobody is failed.
func TestPlanGuardLooksAtAllThereIsBeforeConfirming(t *testing.T) {
	rig := newGuardRig(t)
	wrong := every(10, 0, 2)
	wrong[7] = 15
	rig.fragments(t, 1, wrong)
	rig.fragments(t, 2, every(30, 0, 4))
	cache := &recCache{}
	m, logs := rig.manager(t, cache, scanScript(t, 30, 4), func(o *Options) { o.Tick = time.Hour }) // nobody looks before the playlist is asked for
	s, err := m.Create(context.Background(), indexedRequest(indexedFile(t), 7))
	if err != nil {
		t.Fatal(err)
	}
	rig.open(t)
	waitUntil(t, "the fragments", 5*time.Second, func() bool { _, ok := s.gopFile(9); return ok })

	if n := strings.Count(playlistOf(t, s), "#EXTINF"); n != 15 {
		t.Errorf("%d segments in the playlist, want the 15 of the scan", n)
	}
	if strings.Contains(logs.String(), "after it was confirmed") || strings.Contains(logs.String(), "confirmed by") {
		t.Errorf("confirmed:\n%s", logs.String())
	}
}

// An index that was found wrong while a session confirmed it is not cached.
func TestConfirmDoesNotCacheADistrustedIndex(t *testing.T) {
	rig := newGuardRig(t)
	rig.fragments(t, 1, every(60, 0, 2))
	cache := &recCache{}
	m, logs := rig.manager(t, cache, "/nonexistent/ffprobe", nil)
	path := indexedFile(t)
	s, err := m.Create(context.Background(), indexedRequest(path, 7))
	if err != nil {
		t.Fatal(err)
	}
	m.distrustIndex(path) // another session found the index of the file wrong
	rig.open(t)
	if n := strings.Count(playlistOf(t, s), "#EXTINF"); n != 20 {
		t.Fatalf("%d segments in the playlist", n)
	}
	waitUntil(t, "the plan to be confirmed", 5*time.Second, func() bool { return strings.Contains(logs.String(), "confirmed by") })
	time.Sleep(200 * time.Millisecond)
	if n := cache.total(); n != 0 {
		t.Errorf("%d writes to the cache", n)
	}
}

// The first fragment alone settles the plan if it says how long it lasts and that
// is as long as the plan's GOP; one that does not, needs a pair.
func TestFirstFragmentSettlesThePlanByItsDuration(t *testing.T) {
	t.Run("agrees", func(t *testing.T) {
		rig := newGuardRig(t)
		rig.spans(t, 1, every(60, 0, 2), []float64{2})
		cache := &recCache{}
		m, logs := rig.manager(t, cache, "/nonexistent/ffprobe", func(o *Options) {
			o.WaitTimeout = 20 * time.Second
			o.Tick = time.Hour
		})
		s, err := m.Create(context.Background(), indexedRequest(indexedFile(t), 7))
		if err != nil {
			t.Fatal(err)
		}
		rig.open(t)
		start := time.Now()
		if n := strings.Count(playlistOf(t, s), "#EXTINF"); n != 20 {
			t.Errorf("%d segments in the playlist", n)
		}
		if took := time.Since(start); took > 10*time.Second {
			t.Errorf("the playlist took %v: it waited for a second fragment", took)
		}
		waitUntil(t, "the index to be cached", 5*time.Second, func() bool { return len(cache.of(7)) == 1 })
		if strings.Contains(logs.String(), "not confirmed in time") {
			t.Errorf("given up on:\n%s", logs.String())
		}
	})

	t.Run("no duration", func(t *testing.T) {
		rig := newGuardRig(t)
		rig.fragments(t, 1, every(1, 0, 2)) // one fragment, that does not say how long it lasts
		cache := &recCache{}
		m, logs := rig.manager(t, cache, "/nonexistent/ffprobe", func(o *Options) { o.WaitTimeout = 400 * time.Millisecond })
		s, err := m.Create(context.Background(), indexedRequest(indexedFile(t), 7))
		if err != nil {
			t.Fatal(err)
		}
		rig.open(t)
		if n := strings.Count(playlistOf(t, s), "#EXTINF"); n != 20 {
			t.Errorf("%d segments in the playlist", n)
		}
		if !strings.Contains(logs.String(), "not confirmed in time") {
			t.Errorf("one fragment with no duration confirmed it:\n%s", logs.String())
		}
		time.Sleep(100 * time.Millisecond)
		if n := cache.total(); n != 0 {
			t.Errorf("%d writes to the cache", n)
		}
	})

	// The plan has a keyframe the file does not (the first fragment is longer than
	// the GOP) or lacks one it has (shorter): the first fragment does not say so, it
	// only does not confirm; the start of the second does.
	for _, c := range []struct {
		name  string
		start float64 // where the second fragment starts
		dur   float64 // how long the first lasts
	}{{"lasts 4 s", 4, 4}, {"lasts 1 s", 1, 1}} {
		t.Run(c.name, func(t *testing.T) {
			rig := newGuardRig(t)
			rig.spans(t, 1, []float64{0, c.start}, []float64{c.dur, 2})
			rig.fragments(t, 2, every(30, 0, 4))
			cache := &recCache{}
			m, logs := rig.manager(t, cache, scanScript(t, 30, 4), func(o *Options) { o.WaitTimeout = 20 * time.Second })
			s, err := m.Create(context.Background(), indexedRequest(indexedFile(t), 7))
			if err != nil {
				t.Fatal(err)
			}
			rig.open(t)
			if n := strings.Count(playlistOf(t, s), "#EXTINF"); n != 15 {
				t.Errorf("%d segments in the playlist, want the 15 of the scan", n)
			}
			if l := logs.String(); !strings.Contains(l, "gop=1") || !strings.Contains(l, "the fragment starts at") {
				t.Errorf("the second fragment should have shown it:\n%s", l)
			}
		})
	}

	// A gap in the timestamps before the keyframe (variable frame rate) makes a
	// good first fragment last less than the plan's GOP: it is not a refutation,
	// the second fragment starts where the plan says, and the pair confirms.
	t.Run("a gap before the next keyframe", func(t *testing.T) {
		rig := newGuardRig(t)
		rig.spans(t, 1, every(60, 0, 2), []float64{1.5, 1.5}) // the plan has 2 s
		cache := &recCache{}
		m, logs := rig.manager(t, cache, "/nonexistent/ffprobe", func(o *Options) { o.WaitTimeout = 20 * time.Second })
		s, err := m.Create(context.Background(), indexedRequest(indexedFile(t), 7))
		if err != nil {
			t.Fatal(err)
		}
		rig.open(t)
		if n := strings.Count(playlistOf(t, s), "#EXTINF"); n != 20 {
			t.Errorf("%d segments in the playlist", n)
		}
		waitUntil(t, "the index to be cached", 5*time.Second, func() bool { return len(cache.of(7)) == 1 })
		if l := logs.String(); strings.Contains(l, "disagrees") || strings.Contains(l, "not confirmed in time") {
			t.Errorf("a good file was refuted:\n%s", l)
		}
	})
}

// What the rebuilt session starts from and leaves behind. The fragments of the
// old numbering are of no use to the new plan, and the run starts where the
// player is in it.
func TestRebuildLeavesNoOldFragmentsAndStartsAtThePlayer(t *testing.T) {
	// The scan finds a keyframe a second; the index had one every two.
	t.Run("old fragments are cleared", func(t *testing.T) {
		rig := newGuardRig(t)
		path := indexedFile(t)
		m, _ := rig.manager(t, &recCache{}, scanScript(t, 120, 1), nil)
		req := indexedRequest(path, 7)
		req.StartSec = 20
		s, err := m.Create(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		old := s.Plan()
		a := old.Segments[old.SegmentAt(20)].First // where the first run starts
		wrong := every(60, 0, 2)
		wrong[a+1] -= 1 // the second fragment of the run is a second early
		rig.fragments(t, 1, wrong)
		rig.fragments(t, 2, every(120, 0, 1))
		rig.open(t)

		playlistOf(t, s)
		plan := s.Plan()
		if len(plan.Keyframes) != 120 {
			t.Fatalf("%d keyframes in the plan: not the scan's", len(plan.Keyframes))
		}
		if b := plan.Segments[plan.SegmentAt(20)].First; b <= a+1 {
			t.Fatalf("the new run starts at %d, not past the old fragments (%d, %d): the scenario does not test anything", b, a, a+1)
		}
		for _, g := range []int{a, a + 1} {
			if _, err := os.Stat(s.gopPath(g)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("fragment %d of the old numbering is still there: %v", g, err)
			}
		}
	})

	t.Run("starts at the segment of the start time", func(t *testing.T) {
		rig := newGuardRig(t)
		path := indexedFile(t)
		m, _ := rig.manager(t, &recCache{}, scanScript(t, 30, 4), nil)
		req := indexedRequest(path, 7)
		req.StartSec = 60
		s, err := m.Create(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		old := s.Plan()
		a := old.Segments[old.SegmentAt(60)].First
		wrong := every(60, 0, 2)
		wrong[a+1] -= 1
		rig.fragments(t, 1, wrong)
		rig.fragments(t, 2, every(30, 0, 4))
		rig.open(t)

		if n := strings.Count(playlistOf(t, s), "#EXTINF"); n != 15 {
			t.Fatalf("%d segments in the playlist, want the 15 of the scan", n)
		}
		plan := s.Plan()
		seg := plan.SegmentAt(60)
		want := plan.Segments[seg].First
		if seg == old.SegmentAt(60) {
			t.Fatalf("segment %d in both plans: the scenario does not test anything", seg)
		}
		waitUntil(t, "the second start", 5*time.Second, func() bool { return len(rig.starts()) >= 2 })
		args := strings.Fields(rig.starts()[1])
		got := -1
		for i, a := range args {
			if a == "-start_number" && i+1 < len(args) {
				fmt.Sscan(args[i+1], &got)
			}
		}
		if got != want {
			t.Errorf("ffmpeg was restarted at GOP %d, want %d (segment %d of the new plan)", got, want, seg)
		}
	})
}

// The monitor compares the fragments as they land, with nobody asking for
// anything.
func TestTheMonitorVerifiesWithoutAnyRequest(t *testing.T) {
	rig := newGuardRig(t)
	rig.fragments(t, 1, every(60, 0, 2))
	cache := &recCache{}
	m, _ := rig.manager(t, cache, "/nonexistent/ffprobe", nil)
	if _, err := m.Create(context.Background(), indexedRequest(indexedFile(t), 7)); err != nil {
		t.Fatal(err)
	}
	rig.open(t)
	waitUntil(t, "the index to be cached", 5*time.Second, func() bool { return len(cache.of(7)) == 1 })
}

// A fragment that lands after the plan was confirmed is looked at before a
// segment made of it is served, not only when the monitor gets to it.
func TestPlanGuardLooksBeforeServing(t *testing.T) {
	rig := newGuardRig(t)
	wrong := every(60, 0, 2)
	wrong[3] = 7 // the plan has 6 s
	rig.fragments(t, 1, wrong)
	rig.hold(t, 3)
	m, _ := rig.manager(t, &recCache{}, scanScript(t, 30, 4), func(o *Options) { o.Tick = time.Hour }) // no monitor
	s, err := m.Create(context.Background(), indexedRequest(indexedFile(t), 7))
	if err != nil {
		t.Fatal(err)
	}
	rig.open(t)
	if n := strings.Count(playlistOf(t, s), "#EXTINF"); n != 20 {
		t.Fatalf("%d segments in the playlist", n)
	}

	// The request is made while the fragments are held back, passes the wait for the
	// plan, and is waiting for GOPs 3, 4 and 5 when they land, all at once. Nobody
	// has looked at them: the monitor is off.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	got := make(chan error, 1)
	go func() {
		md, err := s.OpenSegment(ctx, 1)
		if err == nil {
			md.Close()
		}
		got <- err
	}()
	time.Sleep(300 * time.Millisecond)
	rig.release(t, 3)
	if err := <-got; !errors.Is(err, ErrIndex) {
		t.Fatalf("a segment with a fragment the plan does not have: %v", err)
	}
}

// A request that is waiting for a fragment when the session fails does not wait
// for it any longer.
func TestPlanGuardFailsAWaitingRequest(t *testing.T) {
	rig := newGuardRig(t)
	wrong := every(60, 0, 2)
	wrong[3] = 7
	rig.fragments(t, 1, wrong)
	rig.hold(t, 3)
	m, _ := rig.manager(t, &recCache{}, scanScript(t, 30, 4), func(o *Options) { o.WaitTimeout = 20 * time.Second })
	s, err := m.Create(context.Background(), indexedRequest(indexedFile(t), 7))
	if err != nil {
		t.Fatal(err)
	}
	rig.open(t)
	if n := strings.Count(playlistOf(t, s), "#EXTINF"); n != 20 {
		t.Fatalf("%d segments in the playlist", n)
	}
	got := make(chan error, 1)
	go func() {
		md, err := s.OpenSegment(context.Background(), 3) // GOPs 9 to 11: ffmpeg is held at 3
		if err == nil {
			md.Close()
		}
		got <- err
	}()
	time.Sleep(200 * time.Millisecond)
	rig.release(t, 3)
	select {
	case err := <-got:
		if !errors.Is(err, ErrIndex) {
			t.Errorf("the waiting request: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the request waited on for a fragment of a session that had failed")
	}
}

// A plan of one GOP cannot be confirmed by a pair: the one fragment is all there
// will be, and settles it.
func TestPlanGuardConfirmsAPlanOfOneGOP(t *testing.T) {
	data := goodMKV(func(f *mkvFile) {
		f.duration = 2000
		f.clusters = f.clusters[:1]
		var cues []mkvCue
		for _, c := range f.cues {
			if c.time < 2000 {
				cues = append(cues, c)
			}
		}
		f.cues = cues
	}).build()
	path := filepath.Join(t.TempDir(), "clip.mkv")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	rig := newGuardRig(t)
	rig.fragments(t, 1, every(1, 0, 2))
	cache := &recCache{}
	m, _ := rig.manager(t, cache, "/nonexistent/ffprobe", func(o *Options) { o.WaitTimeout = 20 * time.Second })
	req := indexedRequest(path, 7)
	req.DurationSec = 2
	s, err := m.Create(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(s.Plan().Keyframes); n != 1 {
		t.Fatalf("%d keyframes in the plan", n)
	}
	rig.open(t)
	start := time.Now()
	playlistOf(t, s)
	if took := time.Since(start); took > 8*time.Second {
		t.Errorf("the playlist took %v: it waited for a second fragment", took)
	}
	waitUntil(t, "the index to be cached", 5*time.Second, func() bool { return len(cache.of(7)) == 1 })
}

// deafCache is a cache whose drops fail: what was in it stays.
type deafCache struct{ *recCache }

func (c deafCache) SetKeyframes(ctx context.Context, id int64, kf []float64) error {
	if len(kf) == 0 {
		return errors.New("cannot write")
	}
	return c.recCache.SetKeyframes(ctx, id, kf)
}

// The rescan does not read the cache, which may hold the index that was found
// wrong (another session confirmed it meanwhile, or the drop failed).
func TestRebuildDoesNotReadAStaleCache(t *testing.T) {
	rig := newGuardRig(t)
	wrong := every(60, 0, 2)
	wrong[1] = 1.3
	rig.fragments(t, 1, wrong)
	rig.fragments(t, 2, every(30, 0, 4))
	cache := deafCache{&recCache{}}
	m, _ := rig.manager(t, cache, scanScript(t, 30, 4), nil)
	s, err := m.Create(context.Background(), indexedRequest(indexedFile(t), 7))
	if err != nil {
		t.Fatal(err)
	}
	cache.mu.Lock()
	cache.kf = map[int64][]float64{7: every(60, 0, 2)} // the index, in the cache by now
	cache.mu.Unlock()
	rig.open(t)
	if n := strings.Count(playlistOf(t, s), "#EXTINF"); n != 15 {
		t.Errorf("%d segments in the playlist, want the 15 of the scan", n)
	}
	if kf := s.Plan().Keyframes; !equalTimes(kf, every(30, 0, 4)) {
		t.Errorf("the plan has %v ...", head(kf))
	}
}

// Fragments that were gone before the guard got to them (the player is past
// them) are not counted as fragments that agreed: with two of them and one
// comparison the plan is not confirmed, and not cached.
func TestPlanGuardDoesNotCountWhatItDidNotLookAt(t *testing.T) {
	rig := newGuardRig(t)
	rig.fragments(t, 1, every(3, 0, 2)) // GOPs 0, 1 and 2, and no more
	cache := &recCache{}
	m, logs := rig.manager(t, cache, "/nonexistent/ffprobe", func(o *Options) { o.WaitTimeout = 600 * time.Millisecond })
	s, err := m.Create(context.Background(), indexedRequest(indexedFile(t), 7))
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.pruned = 2 // the player is past GOPs 0 and 1
	s.mu.Unlock()
	rig.open(t)
	if n := strings.Count(playlistOf(t, s), "#EXTINF"); n != 20 {
		t.Errorf("%d segments in the playlist", n)
	}
	if !strings.Contains(logs.String(), "not confirmed in time") {
		t.Errorf("one fragment was looked at, and that confirmed the plan:\n%s", logs.String())
	}
	time.Sleep(100 * time.Millisecond)
	if n := cache.total(); n != 0 {
		t.Errorf("%d writes to the cache", n)
	}
}

// A scan that takes longer than ExtractTimeout is a timeout (a 504 to the
// client), not a bad index.
func TestPlaylistTimesOutWithTheScan(t *testing.T) {
	rig := newGuardRig(t)
	wrong := every(60, 0, 2)
	wrong[1] = 1.3
	rig.fragments(t, 1, wrong)
	m, _ := rig.manager(t, &recCache{}, fakeFFprobe(t, "exec sleep 20"), func(o *Options) { o.ExtractTimeout = 400 * time.Millisecond })
	s, err := m.Create(context.Background(), indexedRequest(indexedFile(t), 7))
	if err != nil {
		t.Fatal(err)
	}
	rig.open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if pl, err := s.Playlist(ctx, ""); !errors.Is(err, ErrTimeout) || errors.Is(err, ErrIndex) || pl != "" {
		t.Errorf("playlist %q, err %v: want a timeout", pl, err)
	}
}

// ---- the bias of a run ---------------------------------------------------------

// shortGOPFile is an mkv file whose cues list 60 keyframes ms apart.
func shortGOPFile(t *testing.T, ms int64) string {
	t.Helper()
	data := goodMKV(func(f *mkvFile) {
		smallKeys(f)
		f.duration = float64(60 * ms)
		f.cues = nil
		for k := range f.clusters {
			ts := uint64(int64(k) * ms)
			f.clusters[k].ts = ts
			for j := range f.clusters[k].blocks {
				b := &f.clusters[k].blocks[j]
				switch b.rel { // the frames of the GOP, in short
				case 42:
					b.rel = int16(ms / 4)
				case 21:
					b.rel = int16(ms / 8)
				case 1900:
					b.rel = int16(ms / 2)
				}
			}
			f.cues = append(f.cues, mkvCue{ts, 2})
			if k%3 == 0 {
				f.cues = append(f.cues, mkvCue{ts + 7, 1})
			}
		}
	}).build()
	p := filepath.Join(t.TempDir(), "short.mkv")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// plus is the times with d added to each.
func plus(times []float64, d float64) []float64 {
	out := make([]float64, len(times))
	for i, tm := range times {
		out[i] = tm + d
	}
	return out
}

// ffmpeg's fragments of a video with B-frames start a constant number of frames
// after the keyframe times of the scan (0.083 s for two frames at 24 fps): the
// plan is not refuted for that, however short its GOPs, but for a fragment that is
// off the bias of the others, or a bias that is too big to be one.
func TestPlanGuardTakesTheBiasOfTheRun(t *testing.T) {
	for _, c := range []struct {
		name    string
		bias    float64
		mod     func(times []float64)
		refuted string // the gop in the log
	}{
		{name: "no bias"},
		{name: "0.083 s", bias: 0.083},
		{name: "minus 0.2 s", bias: -0.2},
		{name: "0.083 s, a later fragment 0.05 s off it", bias: 0.083, mod: func(v []float64) { v[5] += 0.05 }},
		{name: "0.3 s is not a bias", bias: 0.3, refuted: "gop=0"},
		{name: "minus 0.3 s is not a bias", bias: -0.3, refuted: "gop=0"},
		{name: "0.083 s, the second fragment 0.1 s off it", bias: 0.083, mod: func(v []float64) { v[1] += 0.1 }, refuted: "gop=1"},
		{name: "0.083 s, a later fragment 0.1 s off it", bias: 0.083, mod: func(v []float64) { v[5] += 0.1 }, refuted: "gop=5"},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := shortGOPFile(t, 200) // a tolerance of 0.067 s
			rig := newGuardRig(t)
			times := plus(every(60, 0, 0.2), c.bias)
			if c.mod != nil {
				c.mod(times)
			}
			rig.fragments(t, 1, times)
			cache := &recCache{}
			m, logs := rig.manager(t, cache, "/nonexistent/ffprobe", nil)
			req := indexedRequest(path, 7)
			req.DurationSec = 12
			s, err := m.Create(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if n := len(s.Plan().Keyframes); n != 60 {
				t.Fatalf("%d keyframes in the plan", n)
			}
			rig.open(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			pl, err := s.Playlist(ctx, "")
			if c.refuted != "" {
				// Found wrong at that fragment, whether before the playlist (the rescan
				// cannot be made, there is no ffprobe: an error) or after it (the session
				// fails).
				waitUntil(t, "the plan to be found wrong", 5*time.Second, func() bool {
					l := logs.String()
					return strings.Contains(l, "disagrees") && strings.Contains(l, c.refuted)
				})
				return
			}
			if err != nil || strings.Count(pl, "#EXTINF") != 2 {
				t.Fatalf("playlist %q, err %v\n%s", pl, err, logs.String())
			}
			waitUntil(t, "the index to be cached", 5*time.Second, func() bool { return len(cache.of(7)) == 1 })
			time.Sleep(300 * time.Millisecond) // all of the fragments are looked at
			if l := logs.String(); strings.Contains(l, "disagrees") {
				t.Errorf("a good file was found wrong:\n%s", l)
			}
			if md, err := s.OpenSegment(ctx, 1); err != nil {
				t.Errorf("the session failed: %v", err)
			} else {
				md.Close()
			}
		})
	}
}

// The bias is of a run: after a seek, ffmpeg's fragments may be as far from the
// scan as another number of frames makes them.
func TestPlanGuardTakesTheBiasOfEachRun(t *testing.T) {
	path := shortGOPFile(t, 200)
	rig := newGuardRig(t)
	rig.fragments(t, 1, plus(every(60, 0, 0.2), 0.083))
	rig.fragments(t, 2, every(60, 0, 0.2))
	cache := &recCache{}
	m, logs := rig.manager(t, cache, "/nonexistent/ffprobe", nil)
	req := indexedRequest(path, 7)
	req.DurationSec = 12
	s, err := m.Create(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	rig.open(t)
	playlistOf(t, s)
	waitUntil(t, "the index to be cached", 5*time.Second, func() bool { return len(cache.of(7)) == 1 })

	s.mu.Lock()
	err = s.startRunLocked(10) // a seek
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the second run to write", 5*time.Second, func() bool { _, ok := s.gopFile(20); return ok })
	time.Sleep(300 * time.Millisecond)
	s.mu.Lock()
	failure, bias := s.failure, 1.0
	if s.run != nil && s.run.haveBias {
		bias = s.run.bias
	}
	s.mu.Unlock()
	if failure != nil || strings.Contains(logs.String(), "disagrees") {
		t.Errorf("the session failed on the second run: %v\n%s", failure, logs.String())
	}
	if bias > 0.01 || bias < -0.01 {
		t.Errorf("the bias of the second run is %v", bias)
	}
}
