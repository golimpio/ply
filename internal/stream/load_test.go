//go:build unix

package stream

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countLoadChanges installs a hook that counts its calls.
func countLoadChanges(m *Manager) *atomic.Int32 {
	n := new(atomic.Int32)
	m.OnLoadChange(func() { n.Add(1) })
	return n
}

func checkLoad(t *testing.T, m *Manager, what string, want PlaybackLoad) {
	t.Helper()
	if got := m.PlaybackLoad(); !reflect.DeepEqual(got, want) {
		t.Errorf("%s: load = %+v, want %+v", what, got, want)
	}
}

func transcodeWith(path, encoder string) Request {
	r := transcodeRequest()
	r.Path = path
	r.Video.Encode = []string{"-c:v", encoder}
	return r
}

// sleepingFFmpeg is an ffmpeg that writes nothing: whatever waits for it blocks.
func sleepingFFmpeg(t *testing.T) fake {
	t.Helper()
	sleeper := t.TempDir() + "/ffmpeg"
	if err := writeExecutable(sleeper, "#!/bin/sh\nexec sleep 60\n"); err != nil {
		t.Fatal(err)
	}
	return fake{ffmpeg: sleeper}
}

// The governor is told how many plays there are, how many of them are not
// paused, which are video transcodes and which of those use the CPU, and which
// files are read: sessions and direct plays alike.
func TestPlaybackLoadCounts(t *testing.T) {
	f := newFake(t)
	clk := newTestClock()
	m := fakeManager(t, f, func(o *Options) {
		o.MaxSessions, o.MaxTranscodes = 8, 4
		o.Clock = clk.Now
		o.IdleTimeout, o.BeatTimeout = time.Hour, time.Hour
	})
	ctx := context.Background()
	checkLoad(t, m, "nothing playing", PlaybackLoad{})

	remux := fakeRequest()
	remux.Path = "/media/a.mkv"
	a, err := m.Create(ctx, remux)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(ctx, remux); err != nil { // the same file again
		t.Fatal(err)
	}
	checkLoad(t, m, "two remuxes of one file", PlaybackLoad{Sessions: 2, Active: 2, Paths: []string{"/media/a.mkv"}})

	sw, err := m.Create(ctx, transcodeWith("/media/b.mkv", "libx264"))
	if err != nil {
		t.Fatal(err)
	}
	hw, err := m.Create(ctx, transcodeWith("/media/c.mkv", "h264_videotoolbox"))
	if err != nil {
		t.Fatal(err)
	}
	direct := DirectKey{ItemID: 9, IP: "10.0.0.2", UserAgent: "VLC"}
	did, done, err := m.DirectFile(direct, "Movie", "10.0.0.2 · VLC", "/nas/d.mkv", 0)
	if err != nil {
		t.Fatal(err)
	}
	_, doneUnknown, err := m.Direct(DirectKey{ItemID: 10, IP: "10.0.0.3", UserAgent: "Chrome"}, "Movie", "10.0.0.3 · Chrome", 0)
	if err != nil {
		t.Fatal(err)
	}
	all := []string{"/media/a.mkv", "/media/b.mkv", "/media/c.mkv", "/nas/d.mkv"} // the sixth play did not say its file
	checkLoad(t, m, "everything running", PlaybackLoad{Sessions: 6, Active: 6, Transcodes: 2, Software: 1, Paths: all})

	// Paused is what the player says; one that says nothing else is not paused.
	paused, running := ptrTo(true), ptrTo(false)
	m.Heartbeat(hw.ID, Beat{Paused: paused})
	m.Heartbeat(did, Beat{Paused: paused})
	m.Heartbeat(a.ID, Beat{Position: ptrTo(30.0)})
	checkLoad(t, m, "two paused", PlaybackLoad{Sessions: 6, Active: 4, Transcodes: 2, Software: 1, Paths: all})
	m.Heartbeat(hw.ID, Beat{Paused: running})
	checkLoad(t, m, "one resumed", PlaybackLoad{Sessions: 6, Active: 5, Transcodes: 2, Software: 1, Paths: all})

	if !m.Delete(sw.ID) {
		t.Fatal("Delete found no session")
	}
	checkLoad(t, m, "the software transcode stopped", PlaybackLoad{Sessions: 5, Active: 4, Transcodes: 1,
		Paths: []string{"/media/a.mkv", "/media/c.mkv", "/nas/d.mkv"}})

	// A direct play is counted for DirectTTL after its last request.
	done()
	doneUnknown()
	clk.Advance(DirectTTL - time.Second)
	if got := m.PlaybackLoad().Sessions; got != 5 {
		t.Errorf("sessions before the direct plays ran out = %d, want 5", got)
	}
	clk.Advance(time.Second)
	checkLoad(t, m, "the direct plays ran out", PlaybackLoad{Sessions: 3, Active: 3, Transcodes: 1,
		Paths: []string{"/media/a.mkv", "/media/c.mkv"}})
}

// When the GPU encoder fails and the software one takes over, the transcode is on
// the CPU from then on.
func TestPlaybackLoadFollowsTheFallbackToSoftware(t *testing.T) {
	m := fakeManager(t, failingFake(t), func(o *Options) { o.Cache = noKeyframes{t} })
	r := transcodeWith("/media/b.mkv", "bad_enc_nvenc") // a GPU name the fake refuses
	r.Fallback = &VideoSpec{Codec: "hevc", Encode: []string{"-c:v", "libx264"}}
	s, err := m.Create(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	checkLoad(t, m, "on the GPU", PlaybackLoad{Sessions: 1, Active: 1, Transcodes: 1, Paths: []string{"/media/b.mkv"}})
	init, err := s.OpenInit(context.Background())
	if err != nil {
		t.Fatalf("init after fallback: %v", err)
	}
	init.Close()
	checkLoad(t, m, "after the fallback", PlaybackLoad{Sessions: 1, Active: 1, Transcodes: 1, Software: 1, Paths: []string{"/media/b.mkv"}})
}

func TestSoftwareEncoder(t *testing.T) {
	for _, tt := range []struct {
		v    *VideoSpec
		want bool
	}{
		{nil, false},
		{&VideoSpec{}, false}, // a copy
		{&VideoSpec{Encode: []string{"-c:v", "libx264", "-preset", "veryfast"}}, true},
		{&VideoSpec{Encode: []string{"-c:v", "h264_videotoolbox", "-b:v", "4000000"}}, false},
		{&VideoSpec{Encode: []string{"-c:v", "h264_vaapi"}}, false},
		{&VideoSpec{Encode: []string{"-c:v", "h264_qsv"}}, false},
		{&VideoSpec{Encode: []string{"-c:v", "h264_nvenc"}}, false},
		{&VideoSpec{Encode: []string{"-vcodec", "h264_nvenc"}}, false},
		{&VideoSpec{Encode: []string{"-profile:v", "high"}}, true}, // no encoder named: the safe side
	} {
		if got := softwareEncoder(tt.v); got != tt.want {
			t.Errorf("softwareEncoder(%+v) = %v, want %v", tt.v, got, tt.want)
		}
	}
}

// Waiting is the number of requests blocked on ffmpeg right now, of whatever
// kind, and the hook is told when it goes from none to some, not when it grows.
func TestPlaybackLoadWaiting(t *testing.T) {
	m := fakeManager(t, sleepingFFmpeg(t), func(o *Options) { o.WaitTimeout = 30 * time.Second })
	s, err := m.Create(context.Background(), fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	if w := m.PlaybackLoad().Waiting; w != 0 { // positive control: nothing asked yet
		t.Fatalf("Waiting = %d before any request", w)
	}
	n := countLoadChanges(m)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	request := func(f func() error) {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- f() }()
	}
	segment := func() error { _, err := s.OpenSegment(ctx, 0); return err }
	request(segment)
	waitUntil(t, "a segment request to be waiting", 5*time.Second, func() bool { return m.PlaybackLoad().Waiting == 1 })
	request(segment)
	request(func() error { _, err := s.OpenInit(ctx); return err })
	waitUntil(t, "three requests to be waiting", 5*time.Second, func() bool { return m.PlaybackLoad().Waiting == 3 })
	if got := m.PlaybackLoad(); got.Sessions != 1 || got.Transcodes != 0 {
		t.Errorf("load while waiting = %+v", got)
	}

	cancel()
	wg.Wait()
	for range 3 {
		if err := <-errs; !errors.Is(err, context.Canceled) {
			t.Errorf("a waiting request ended with %v, want context.Canceled", err)
		}
	}
	if w := m.PlaybackLoad().Waiting; w != 0 {
		t.Errorf("Waiting = %d after the requests ended", w)
	}
	if got := n.Load(); got != 1 {
		t.Errorf("hook called %d times for three requests that began waiting together, want once (0 to 1 only)", got)
	}

	// Again from none: told again. This time the wait ends with the session.
	got := make(chan error, 1)
	go func() { _, err := s.OpenSegment(context.Background(), 0); got <- err }()
	waitUntil(t, "a request to be waiting again", 5*time.Second, func() bool { return m.PlaybackLoad().Waiting == 1 })
	if !m.Delete(s.ID) {
		t.Fatal("Delete found no session")
	}
	if err := <-got; !errors.Is(err, ErrClosed) {
		t.Errorf("request of a deleted session = %v, want ErrClosed", err)
	}
	if w := m.PlaybackLoad().Waiting; w != 0 {
		t.Errorf("Waiting = %d after the session was deleted", w)
	}
	if got := n.Load(); got != 3 { // the second 0 to 1, and the session ending
		t.Errorf("hook called %d times in all, want 3", got)
	}
}

// The wait for the playlist is a wait for ffmpeg too: the plan is confirmed by
// the first fragment it writes.
func TestPlaybackLoadCountsTheWaitForThePlaylist(t *testing.T) {
	m := fakeManager(t, sleepingFFmpeg(t), nil)
	s, err := m.Create(context.Background(), fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.guard = newPlanGuard(s.plan.Keyframes, 1, time.Now().Add(time.Minute))
	s.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan error, 1)
	go func() { _, err := s.Playlist(ctx, ""); got <- err }()
	waitUntil(t, "the playlist request to be waiting", 5*time.Second, func() bool { return m.PlaybackLoad().Waiting == 1 })
	cancel()
	if err := <-got; !errors.Is(err, context.Canceled) {
		t.Errorf("Playlist = %v, want context.Canceled", err)
	}
	if w := m.PlaybackLoad().Waiting; w != 0 {
		t.Errorf("Waiting = %d after the request left", w)
	}
}

// A request for what ffmpeg has written already is answered at once, not
// waited for: it is never counted, and the governor is not woken for it.
func TestARequestAnsweredAtOnceIsNotWaiting(t *testing.T) {
	f := newFake(t)
	m := fakeManager(t, f, nil)
	ctx := context.Background()
	s, err := m.Create(ctx, fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the first segment to be written", 5*time.Second, func() bool { _, ok := s.gopFile(2); return ok })
	n := countLoadChanges(m)

	seg, err := s.OpenSegment(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	seg.Close()
	if got := n.Load(); got != 0 {
		t.Errorf("hook called %d times for a request answered at once", got)
	}

	// Positive control: one that has to wait for ffmpeg (a seek) is counted.
	seg, err = s.OpenSegment(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	seg.Close()
	if got := n.Load(); got != 1 {
		t.Errorf("hook called %d times for a request that had to wait, want 1", got)
	}
	if w := m.PlaybackLoad().Waiting; w != 0 {
		t.Errorf("Waiting = %d with no request in progress", w)
	}
}

// The hook is told when a play starts or ends, and only then.
func TestLoadHookFiresWhenPlaysStartAndEnd(t *testing.T) {
	f := newFake(t)
	clk := newTestClock()
	m := fakeManager(t, f, func(o *Options) {
		o.Clock = clk.Now
		o.IdleTimeout, o.BeatTimeout = time.Hour, time.Hour
	})
	ctx := context.Background()
	n := countLoadChanges(m)
	calls := func(what string, want int32) {
		t.Helper()
		if got := n.Load(); got != want {
			t.Fatalf("after %s: hook called %d times, want %d", what, got, want)
		}
	}

	s, err := m.Create(ctx, fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	calls("a session started (its place was reserved, then it was there)", 2)

	k := DirectKey{ItemID: 5, IP: "10.0.0.2", UserAgent: "VLC"}
	_, done, err := m.DirectFile(k, "Movie", "VLC", "/media/y.mkv", 0)
	if err != nil {
		t.Fatal(err)
	}
	calls("a direct play began", 3)
	_, done2, err := m.DirectFile(k, "Movie", "VLC", "/media/y.mkv", 0) // another connection of the same player
	if err != nil {
		t.Fatal(err)
	}
	done()
	done2()
	m.Heartbeat(s.ID, Beat{Paused: ptrTo(true)})
	calls("more requests of a play already counted, and a heartbeat", 3)

	if !m.Delete(s.ID) {
		t.Fatal("Delete found no session")
	}
	calls("a session ended", 4)

	clk.Advance(DirectTTL)
	if m.PlaybackLoad().Sessions != 0 {
		t.Fatal("the direct play is still counted after DirectTTL")
	}
	calls("a direct play ran out", 5)
	m.PlaybackLoad()
	m.Playing()
	calls("looking again", 5)

	id, _, err := m.DirectFile(DirectKey{ItemID: 6, IP: "10.0.0.2", UserAgent: "VLC"}, "Movie", "VLC", "/media/z.mkv", 0)
	if err != nil {
		t.Fatal(err)
	}
	calls("another direct play began", 6)
	if !m.Delete(id) {
		t.Fatal("Delete found no direct play")
	}
	calls("a direct play was dropped", 7)

	if _, err := m.Create(ctx, fakeRequest()); err != nil {
		t.Fatal(err)
	}
	calls("a session started", 9)
	m.Close()
	calls("the manager closed", 10)
}

// gatedKeyframes is a keyframe cache that holds Create where the keyframe scan of a first
// play would, until the test lets it go (or fails it).
type gatedKeyframes struct {
	entered chan struct{} // told when Create is inside
	release chan error    // what Create is let go with: nil for the index, or why not
}

func (g gatedKeyframes) Keyframes(ctx context.Context, _ int64) ([]float64, bool, error) {
	g.entered <- struct{}{}
	select {
	case err := <-g.release:
		if err != nil {
			return nil, false, err
		}
		return evenKeyframes(600, 2), true, nil
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
}
func (gatedKeyframes) SetKeyframes(context.Context, int64, []float64) error { return nil }

// A play is being set up from the moment Create reserves its place: the file is in the load
// (and the governor told at once) while the keyframe index is being read, which on the first
// play of a film over a slow share is minutes; and when Create gives up, it is out of it
// again, and the governor is told that too.
func TestPlaybackLoadCountsAPlayBeingSetUp(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fail    error
		wantErr bool
		after   PlaybackLoad
	}{
		{"the session comes", nil, false, PlaybackLoad{Sessions: 1, Active: 1, Paths: []string{"/media/x.mkv"}}},
		{"it ends without one", errors.New("disk gone"), true, PlaybackLoad{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gate := gatedKeyframes{entered: make(chan struct{}, 1), release: make(chan error, 1)}
			m := fakeManager(t, sleepingFFmpeg(t), func(o *Options) { o.Cache = gate })
			n := countLoadChanges(m)
			checkLoad(t, m, "before the play", PlaybackLoad{}) // positive control

			// The hook is counted before anything asks for the load: PlaybackLoad lets go of the
			// lock as every change does, and would tell the governor of a change nobody told it of.
			created := make(chan error, 1)
			go func() { _, err := m.Create(context.Background(), fakeRequest()); created <- err }()
			<-gate.entered
			reserved := n.Load()
			if reserved != 1 {
				t.Errorf("hook called %d times when the place was reserved, want 1", reserved)
			}
			checkLoad(t, m, "while the index is read", PlaybackLoad{Sessions: 1, Active: 1, Paths: []string{"/media/x.mkv"}})
			select {
			case err := <-created:
				t.Fatalf("Create returned (%v) before the index was read", err)
			default:
			}

			gate.release <- tc.fail
			err := <-created
			all := n.Load() // before checkLoad, which would make up for a hook that was not called
			if (err != nil) != tc.wantErr {
				t.Fatalf("Create = %v", err)
			}
			if all != 2 {
				t.Errorf("hook called %d times in all, want 2: the place reserved, and then the session or the place given up", all)
			}
			checkLoad(t, m, "after Create", tc.after)
			if got := n.Load(); got != all {
				t.Errorf("asking for the load told the governor of %d more changes: nothing had changed since", got-all)
			}
		})
	}
}

// Two plays of one file being set up are two plays; a refused one (at the limit) is none.
func TestPlaybackLoadCountsEachPlayBeingSetUp(t *testing.T) {
	gate := gatedKeyframes{entered: make(chan struct{}, 2), release: make(chan error, 2)}
	m := fakeManager(t, sleepingFFmpeg(t), func(o *Options) { o.Cache = gate; o.MaxSessions = 2 })
	created := make(chan error, 3)
	for range 2 {
		go func() { _, err := m.Create(context.Background(), fakeRequest()); created <- err }()
	}
	<-gate.entered
	<-gate.entered
	checkLoad(t, m, "two being set up", PlaybackLoad{Sessions: 2, Active: 2, Paths: []string{"/media/x.mkv"}})

	var limit *LimitError
	if _, err := m.Create(context.Background(), fakeRequest()); !errors.As(err, &limit) {
		t.Fatalf("a third play at the limit = %v, want the limit", err)
	}
	checkLoad(t, m, "a refused play adds nothing", PlaybackLoad{Sessions: 2, Active: 2, Paths: []string{"/media/x.mkv"}})

	gate.release <- errors.New("one fails")
	gate.release <- nil
	for range 2 {
		<-created
	}
	checkLoad(t, m, "one failed, one became a session", PlaybackLoad{Sessions: 1, Active: 1, Paths: []string{"/media/x.mkv"}})
}

// The hook is called with no lock of the manager held: it may ask for the load.
func TestLoadHookMayCallBackIntoTheManager(t *testing.T) {
	f := newFake(t)
	m := fakeManager(t, f, nil)
	seen := make(chan PlaybackLoad, 8)
	var lockHeld atomic.Bool
	m.OnLoadChange(func() {
		// Taking the lock is what a call back would do; a caller that still holds it
		// would be waited on for good, so only try (for a moment: the housekeeping
		// takes it too, now and then).
		for range 100 {
			if m.mu.TryLock() {
				m.mu.Unlock()
				seen <- m.PlaybackLoad()
				return
			}
			time.Sleep(time.Millisecond)
		}
		lockHeld.Store(true)
	})
	if _, err := m.Create(context.Background(), fakeRequest()); err != nil {
		t.Fatal(err)
	}
	if lockHeld.Load() {
		t.Fatal("the hook was called with the manager's lock held")
	}
	select {
	case l := <-seen:
		if l.Sessions != 1 {
			t.Errorf("load seen by the hook of a started session = %+v, want the session in it", l)
		}
	default:
		t.Fatal("the hook was not called")
	}
	for len(seen) > 0 { // (it was told twice: the place reserved, and the session)
		<-seen
	}
	m.OnLoadChange(nil) // removed
	m.Close()
	select {
	case l := <-seen:
		t.Errorf("a removed hook was called: %+v", l)
	default:
	}
}
