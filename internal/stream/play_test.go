//go:build unix

package stream

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testClock is a clock the test moves; the manager and its sessions read it from
// other goroutines.
type testClock struct{ ns atomic.Int64 }

func newTestClock() *testClock {
	c := &testClock{}
	c.ns.Store(time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC).UnixNano())
	return c
}

func (c *testClock) Now() time.Time          { return time.Unix(0, c.ns.Load()).UTC() }
func (c *testClock) Advance(d time.Duration) { c.ns.Add(int64(d)) }

func ptrTo[T any](v T) *T { return &v }

// brief counts a request for a direct play that ends at once (a short range
// request), and returns the play's id.
func brief(m *Manager, k DirectKey, title, client string, pos float64) (string, error) {
	id, done, err := m.Direct(k, title, client, pos)
	if err == nil {
		done()
	}
	return id, err
}

// The player's own stop-then-start must not be turned away at the limit by the
// session it has just stopped: a session on its way out (its ffmpeg is being
// killed, its directory removed) is held by Delete for as long as that takes, and
// does not count.
func TestStopThenStartAtTheCap(t *testing.T) {
	f := newFake(t)
	m := fakeManager(t, f, func(o *Options) { o.MaxSessions = 1 })
	gate := make(chan struct{})
	m.stopGate = gate // set before any session exists: stop() holds here until it is closed
	var open sync.Once
	release := func() { open.Do(func() { close(gate) }) }
	t.Cleanup(release) // runs before the manager's Close

	ctx := context.Background()
	a, err := m.Create(ctx, fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	// Positive control: the cap is 1, and one session is running.
	if _, err := m.Create(ctx, fakeRequest()); !errors.Is(err, ErrTooManySessions) {
		t.Fatalf("second session at a cap of 1 = %v, want ErrTooManySessions", err)
	}

	deleted := make(chan bool, 1)
	go func() { deleted <- m.Delete(a.ID) }()
	waitUntil(t, "the session to be on its way out", 5*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return len(m.stopping) == 1 && len(m.sessions) == 0
	})
	if m.Count() != 1 {
		t.Fatalf("Count() = %d with a session being stopped, want 1 (it still holds its ffmpeg and directory)", m.Count())
	}
	if n := len(m.Playing()); n != 0 {
		t.Errorf("a session being stopped is listed as playing (%d entries)", n)
	}

	b, err := m.Create(ctx, fakeRequest()) // the player starts the next one
	if err != nil {
		t.Fatalf("a new session while the old one is being stopped: %v", err)
	}
	release()
	if !<-deleted {
		t.Fatal("Delete found no session")
	}
	waitUntil(t, "the old session to be gone", 5*time.Second, func() bool { return m.Count() == 1 })
	if _, err := m.Create(ctx, fakeRequest()); !errors.Is(err, ErrTooManySessions) {
		t.Errorf("a session beyond the cap, with the first one gone = %v, want ErrTooManySessions (%s counts)", err, b.ID)
	}
}

func TestTranscodesAreCappedSeparately(t *testing.T) {
	f := newFake(t)
	m := fakeManager(t, f, func(o *Options) { o.MaxSessions = 4; o.MaxTranscodes = 1 })
	ctx := context.Background()

	t1, err := m.Create(ctx, transcodeRequest())
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Create(ctx, transcodeRequest())
	var le *LimitError
	if !errors.As(err, &le) || !errors.Is(err, ErrTooManySessions) {
		t.Fatalf("second transcode at a cap of 1 = %v, want a *LimitError matching ErrTooManySessions", err)
	}
	if le.Limit != "transcodes" || le.Max != 1 || le.Used != 1 {
		t.Errorf("limit error = %+v", le)
	}
	if len(le.Playing) != 1 || le.Playing[0].ID != t1.ID || le.Playing[0].Kind != KindTranscode {
		t.Errorf("playing in the transcode limit error = %+v, want the one transcode (stopping a remux makes no room)", le.Playing)
	}

	// Remuxes are not held back by it, up to the cap on all plays.
	for i := 0; i < 3; i++ {
		if _, err := m.Create(ctx, fakeRequest()); err != nil {
			t.Fatalf("remux %d beside a transcode: %v", i+1, err)
		}
	}
	_, err = m.Create(ctx, fakeRequest())
	if !errors.As(err, &le) || le.Limit != "streams" || le.Max != 4 || le.Used != 4 || len(le.Playing) != 4 {
		t.Fatalf("fifth play = %v (%+v), want the limit on streams, 4 of 4, with all four listed", err, le)
	}

	// Stopping the transcode makes room for another one.
	if !m.Delete(t1.ID) {
		t.Fatal("Delete failed")
	}
	if _, err := m.Create(ctx, transcodeRequest()); err != nil {
		t.Errorf("transcode after the first was stopped: %v", err)
	}
}

func TestConcurrentTranscodesRespectTheirLimit(t *testing.T) {
	f := newFake(t)
	m := fakeManager(t, f, func(o *Options) { o.MaxSessions = 10; o.MaxTranscodes = 2 })
	var wg sync.WaitGroup
	var ok, full atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.Create(context.Background(), transcodeRequest())
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, ErrTooManySessions):
				full.Add(1)
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 2 || full.Load() != 6 {
		t.Errorf("created %d transcodes, refused %d; want 2 and 6", ok.Load(), full.Load())
	}
}

func TestSetLimitsIsLive(t *testing.T) {
	f := newFake(t)
	m := fakeManager(t, f, func(o *Options) { o.MaxSessions = 1; o.MaxTranscodes = 1 })
	ctx := context.Background()
	if s, tr := m.Limits(); s != 1 || tr != 1 {
		t.Fatalf("Limits() = %d, %d", s, tr)
	}
	first, err := m.Create(ctx, fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	first.mu.Lock()
	firstPid := first.run.cmd.Process.Pid
	first.mu.Unlock()
	if _, err := m.Create(ctx, fakeRequest()); !errors.Is(err, ErrTooManySessions) {
		t.Fatalf("second play at a cap of 1 = %v", err)
	}

	m.SetLimits(3, 2) // raised: takes effect for the next play, the first one is not restarted
	if _, err := m.Create(ctx, fakeRequest()); err != nil {
		t.Fatalf("play after the limit was raised: %v", err)
	}
	if _, err := m.Create(ctx, transcodeRequest()); err != nil {
		t.Fatalf("transcode after the limit was raised: %v", err)
	}
	waitUntil(t, "three ffmpeg running", 5*time.Second, func() bool { return len(f.procs(t)) == 3 })
	if !pidAlive(firstPid) {
		t.Error("raising the limit disturbed the first session")
	}

	m.SetLimits(1, 1) // lowered below what is running: nothing is stopped, nothing new starts
	if m.Count() != 3 {
		t.Errorf("lowering the limit stopped sessions: %d left", m.Count())
	}
	if _, err := m.Create(ctx, fakeRequest()); !errors.Is(err, ErrTooManySessions) {
		t.Errorf("play beyond a lowered limit = %v", err)
	}
	if !m.Delete(first.ID) {
		t.Fatal("Delete failed")
	}

	m.SetLimits(0, -1) // not limits: left as they are
	if s, tr := m.Limits(); s != 1 || tr != 1 {
		t.Errorf("Limits() after SetLimits(0, -1) = %d, %d, want 1, 1", s, tr)
	}
}

// Direct plays are counted: with the sessions against the cap on plays, until
// DirectTTL after their last request.
func TestDirectPlaysCountAndExpire(t *testing.T) {
	f := newFake(t)
	clk := newTestClock()
	m := fakeManager(t, f, func(o *Options) { o.MaxSessions = 2; o.Clock = clk.Now; o.IdleTimeout = time.Hour })
	ctx := context.Background()
	a := DirectKey{ItemID: 1, IP: "10.0.0.2", UserAgent: "Safari"}
	b := DirectKey{ItemID: 1, IP: "10.0.0.3", UserAgent: "Chrome"}

	if _, err := m.Create(ctx, fakeRequest()); err != nil {
		t.Fatal(err)
	}
	idA, err := brief(m, a, "Movie", "10.0.0.2 · Safari", 0)
	if err != nil {
		t.Fatalf("direct play beside one session at a cap of 2: %v", err)
	}
	// Full: a third play is refused, whichever kind, with the list of the two.
	_, err = brief(m, b, "Movie", "10.0.0.3 · Chrome", 0)
	var le *LimitError
	if !errors.As(err, &le) || le.Limit != "streams" || le.Used != 2 || len(le.Playing) != 2 {
		t.Fatalf("direct play at the cap = %v (%+v)", err, le)
	}
	kinds := map[string]bool{le.Playing[0].Kind: true, le.Playing[1].Kind: true}
	if !kinds[KindRemux] || !kinds[KindDirect] {
		t.Errorf("the list at the limit has kinds %v, want a remux and a direct play", kinds)
	}
	if _, err := m.Create(ctx, fakeRequest()); !errors.Is(err, ErrTooManySessions) {
		t.Fatalf("session at the cap with a direct play counted = %v, want ErrTooManySessions", err)
	}
	if err := m.CheckDirect(b); !errors.As(err, &le) {
		t.Errorf("CheckDirect at the cap = %v", err)
	}
	if got, err := brief(m, a, "Movie", "10.0.0.2 · Safari", 0); err != nil || got != idA {
		t.Errorf("another range request of the same play = %q, %v; want the same id and no refusal at the cap", got, err)
	}

	clk.Advance(DirectTTL - time.Second)
	if _, err := m.Create(ctx, fakeRequest()); !errors.Is(err, ErrTooManySessions) {
		t.Fatalf("just before DirectTTL the direct play was forgotten: %v", err)
	}
	if _, err := brief(m, a, "Movie", "10.0.0.2 · Safari", 0); err != nil { // refreshed: counts for 30 s more
		t.Fatal(err)
	}
	clk.Advance(DirectTTL - time.Second)
	if _, err := m.Create(ctx, fakeRequest()); !errors.Is(err, ErrTooManySessions) {
		t.Fatalf("a refresh did not extend the direct play: %v", err)
	}
	clk.Advance(2 * time.Second) // 31 s after the last request
	if n := len(m.Playing()); n != 1 {
		t.Errorf("playing after the expiry = %d, want only the session", n)
	}
	if _, err := m.Create(ctx, fakeRequest()); err != nil {
		t.Errorf("session after the direct play expired: %v", err)
	}
}

func TestDirectPlayListingAndDrop(t *testing.T) {
	f := newFake(t)
	clk := newTestClock()
	m := fakeManager(t, f, func(o *Options) { o.Clock = clk.Now })
	k := DirectKey{ItemID: 9, IP: "10.0.0.2", UserAgent: "AppleCoreMedia"}
	id, err := brief(m, k, "Some Movie", "10.0.0.2 · AVPlayer", 61)
	if err != nil || len(id) < 8 || id[:2] != "d-" {
		t.Fatalf("Direct = %q, %v", id, err)
	}
	clk.Advance(5 * time.Second)
	if !m.Heartbeat(id, Beat{Position: ptrTo(75.5), Paused: ptrTo(true)}) {
		t.Fatal("a direct play cannot be beaten")
	}
	got := m.Playing()
	if len(got) != 1 {
		t.Fatalf("playing = %+v", got)
	}
	p := got[0]
	if p.ID != id || p.ItemID != 9 || p.Title != "Some Movie" || p.Kind != KindDirect || p.Client != "10.0.0.2 · AVPlayer" ||
		p.PositionSec != 75.5 || !p.Paused || !p.StartedAt.Equal(clk.Now().Add(-5*time.Second)) || !p.LastSeenAt.Equal(clk.Now()) {
		t.Errorf("direct play = %+v", p)
	}
	// Deleting one only drops it from the count: the client keeps what it has.
	if !m.Delete(id) {
		t.Error("a direct play cannot be dropped")
	}
	if len(m.Playing()) != 0 || m.Delete(id) {
		t.Error("the direct play is still there after it was dropped")
	}
}

func TestPlayingListsWhatIsPlaying(t *testing.T) {
	f := newFake(t)
	clk := newTestClock()
	m := fakeManager(t, f, func(o *Options) { o.Clock = clk.Now; o.IdleTimeout = time.Hour })
	ctx := context.Background()

	r := fakeRequest()
	r.Title, r.Client, r.StartSec, r.ItemID = "Remuxed", "10.0.0.2 · Safari", 300, 11
	rs, err := m.Create(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Second)
	tr := transcodeRequest()
	tr.Title, tr.Client, tr.ItemID = "Transcoded", "10.0.0.3 · Chrome", 12
	ts, err := m.Create(ctx, tr)
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Second)

	got := m.Playing()
	if len(got) != 2 || got[0].ID != rs.ID || got[1].ID != ts.ID {
		t.Fatalf("playing = %+v, want the remux then the transcode (oldest first)", got)
	}
	if p := got[0]; p.Kind != KindRemux || p.ItemID != 11 || p.Title != "Remuxed" || p.Client != "10.0.0.2 · Safari" ||
		p.PositionSec != 300 || p.Paused || !p.StartedAt.Equal(clk.Now().Add(-2*time.Second)) {
		t.Errorf("remux entry = %+v", p)
	}
	if p := got[1]; p.Kind != KindTranscode || p.ItemID != 12 || p.PositionSec != 0 {
		t.Errorf("transcode entry = %+v", p)
	}

	// Until the player says where it is, the position is that of the last segment it asked for.
	seg, err := rs.OpenSegment(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	seg.Close()
	if p := m.Playing()[0]; p.PositionSec != rs.Plan().Segments[10].Start {
		t.Errorf("position after asking for segment 10 = %v, want %v", p.PositionSec, rs.Plan().Segments[10].Start)
	}

	// Then it is what the player says, and a beat is a sign of life.
	clk.Advance(4 * time.Second)
	if !m.Heartbeat(rs.ID, Beat{Position: ptrTo(1234.5), Paused: ptrTo(true)}) {
		t.Fatal("heartbeat refused")
	}
	p := m.Playing()[0]
	if p.PositionSec != 1234.5 || !p.Paused || !p.LastSeenAt.Equal(clk.Now()) {
		t.Errorf("after a heartbeat: %+v", p)
	}
	// A beat that says only where leaves the rest; a bad position is not believed.
	m.Heartbeat(rs.ID, Beat{Position: ptrTo(-5.0)})
	if p := m.Playing()[0]; p.PositionSec != 0 || !p.Paused {
		t.Errorf("after a heartbeat with a negative position: %+v", p)
	}
}

func TestHeartbeatOfAnUnknownSession(t *testing.T) {
	f := newFake(t)
	m := fakeManager(t, f, nil)
	if m.Heartbeat("nope", Beat{}) {
		t.Error("heartbeat for a session that never existed was accepted")
	}
	s, err := m.Create(context.Background(), fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	if !m.Heartbeat(s.ID, Beat{}) { // positive control, and an empty beat is a beat
		t.Fatal("heartbeat for a live session was refused")
	}
	m.Delete(s.ID)
	if m.Heartbeat(s.ID, Beat{}) {
		t.Error("heartbeat for a session that was stopped was accepted")
	}
}

// settle gives the sessions' monitors time for several ticks.
func settle(m *Manager) { time.Sleep(4 * m.o.Tick) }

// A paused player makes no requests for as long as it has buffer, and must not be
// stopped for it while its heartbeats arrive. One that never beat keeps the long
// rule (IdleTimeout, 60 s here as by default): the same clock, the same silence.
func TestHeartbeatKeepsAPausedSessionAlive(t *testing.T) {
	f := newFake(t)
	clk := newTestClock()
	m := fakeManager(t, f, func(o *Options) { o.Clock = clk.Now; o.IdleTimeout = 60 * time.Second })
	ctx := context.Background()
	beaten, err := m.Create(ctx, fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	silent, err := m.Create(ctx, fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "both ffmpeg running", 5*time.Second, func() bool { return len(f.procs(t)) == 2 })

	paused := true
	for i := 0; i < 18; i++ { // three minutes of a paused player, beating every 10 s
		clk.Advance(10 * time.Second)
		if !m.Heartbeat(beaten.ID, Beat{Position: ptrTo(600.0), Paused: &paused}) {
			t.Fatalf("after %d s the session of the paused player is gone although it beats", 10*(i+1))
		}
		settle(m)
		if i == 4 { // 50 s in: the one without beats is still within its time
			if _, ok := m.Get(silent.ID); !ok {
				t.Fatal("the session without beats was stopped before IdleTimeout")
			}
		}
	}
	if _, ok := m.Get(silent.ID); ok {
		t.Error("the session that never beat survived three minutes of silence")
	}
	if _, ok := m.Get(beaten.ID); !ok {
		t.Fatal("the paused session that beat every 10 s was stopped")
	}
	if n := len(f.procs(t)); n != 1 {
		t.Errorf("%d ffmpeg running, want only the beaten session's", n)
	}
	if p := m.Playing(); len(p) != 1 || !p[0].Paused || p[0].PositionSec != 600 {
		t.Errorf("playing = %+v, want the paused session at 600 s", p)
	}

	// And when the beats stop it goes, like any session: a paused player's 3 minutes,
	// not the 30 s of one that plays.
	clk.Advance(DefaultPausedBeatTimeout - time.Second)
	settle(m)
	if _, ok := m.Get(beaten.ID); !ok {
		t.Fatal("the paused session was stopped inside its 3 minutes")
	}
	clk.Advance(2 * time.Second)
	waitUntil(t, "the session whose beats stopped to be stopped", 5*time.Second, func() bool { return m.Count() == 0 })
	if p := f.procs(t); len(p) != 0 {
		t.Errorf("ffmpeg survived the idle stop: %v", p)
	}
}

// A player that has sent a heartbeat is stopped 30 s after its last beat or
// request; one that never has keeps the old 60 s, so that a player that does not
// know the heartbeat (hls.js before the web change, AVPlayer) with 40 s of buffer
// is safe.
func TestBeatingSessionsAreStoppedSoonerThanSilentOnes(t *testing.T) {
	f := newFake(t)
	clk := newTestClock()
	m := fakeManager(t, f, func(o *Options) { o.Clock = clk.Now }) // IdleTimeout 60 s, BeatTimeout 30 s by default
	ctx := context.Background()
	beat, err := m.Create(ctx, fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	beatTwice, err := m.Create(ctx, fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	never, err := m.Create(ctx, fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	alive := func(s *Session) bool { _, ok := m.Get(s.ID); return ok }
	m.Heartbeat(beat.ID, Beat{})
	m.Heartbeat(beatTwice.ID, Beat{})

	clk.Advance(25 * time.Second)
	m.Heartbeat(beatTwice.ID, Beat{}) // 25 s after its first beat: starts another 30 s
	settle(m)
	if !alive(beat) || !alive(beatTwice) || !alive(never) {
		t.Fatal("a session was stopped 25 s in, within every limit")
	}

	clk.Advance(6 * time.Second) // 31 s after the beat of the first, 6 s after the second's
	waitUntil(t, "the session that beat once, 31 s ago, to be stopped", 5*time.Second, func() bool { return !alive(beat) })
	if !alive(beatTwice) {
		t.Error("the session that beat 6 s ago was stopped")
	}

	clk.Advance(14 * time.Second) // t = 45 s: silence of 45 s for the one that never beat, 20 s for the second
	settle(m)
	if !alive(never) {
		t.Fatal("a session that never beat was stopped after 45 s without requests (it keeps the 60 s rule)")
	}
	if !alive(beatTwice) {
		t.Error("the session that beat 20 s ago was stopped")
	}

	clk.Advance(11 * time.Second) // t = 56 s: 31 s since the second's last beat, 56 s of silence for the third
	waitUntil(t, "the session whose beats stopped to be stopped", 5*time.Second, func() bool { return !alive(beatTwice) })
	if !alive(never) {
		t.Error("the session that never beat was stopped at 56 s")
	}

	clk.Advance(5 * time.Second) // t = 61 s without a request
	waitUntil(t, "the session that never beat to be stopped after 60 s", 5*time.Second, func() bool { return !alive(never) })
	waitUntil(t, "every ffmpeg to be gone", 5*time.Second, func() bool { return len(f.procs(t)) == 0 })
}

// The shorter time of a session that beats never exceeds IdleTimeout, which a
// test or a script may set lower (PLY_STREAM_IDLE).
func TestBeatTimeoutNeverExceedsIdleTimeout(t *testing.T) {
	f := newFake(t)
	clk := newTestClock()
	m := fakeManager(t, f, func(o *Options) { o.Clock = clk.Now; o.IdleTimeout = 5 * time.Second })
	s, err := m.Create(context.Background(), fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	m.Heartbeat(s.ID, Beat{})
	clk.Advance(6 * time.Second)
	waitUntil(t, "the session to be stopped after 6 s with IdleTimeout 5 s", 5*time.Second, func() bool { return m.Count() == 0 })
}

// A player that reads the whole file over one Range: bytes=0- connection (VLC,
// Infuse, AVPlayer) makes one request, which lasts as long as the film: the play
// is counted for all of it, and DirectTTL after it ends.
func TestADirectPlayIsCountedWhileARequestIsBeingServed(t *testing.T) {
	f := newFake(t)
	clk := newTestClock()
	m := fakeManager(t, f, func(o *Options) { o.MaxSessions = 1; o.Clock = clk.Now })
	vlc := DirectKey{ItemID: 1, IP: "10.0.0.2", UserAgent: "VLC"}
	other := DirectKey{ItemID: 1, IP: "10.0.0.3", UserAgent: "Chrome"}

	_, done, err := m.Direct(vlc, "Movie", "10.0.0.2 · VLC", 0)
	if err != nil {
		t.Fatal(err)
	}
	_, done2, err := m.Direct(vlc, "Movie", "10.0.0.2 · VLC", 0) // a second connection of the same player
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(2 * time.Hour) // a long film
	if n := len(m.Playing()); n != 1 {
		t.Fatalf("%d plays after 2 hours of one request, want it still counted", n)
	}
	var le *LimitError
	if _, err := brief(m, other, "Movie", "10.0.0.3 · Chrome", 0); !errors.As(err, &le) {
		t.Fatalf("a second client at the cap while the first is still being served = %v", err)
	}

	done() // one of the two connections ends: the other is still being served
	clk.Advance(time.Hour)
	if n := len(m.Playing()); n != 1 {
		t.Fatalf("%d plays with one connection still open", n)
	}

	done2()
	done2() // twice is the same as once: a count that went below zero would never expire
	clk.Advance(DirectTTL - time.Nanosecond)
	if n := len(m.Playing()); n != 1 {
		t.Fatalf("%d plays just before DirectTTL after the last request ended, want 1", n)
	}
	clk.Advance(time.Nanosecond) // exactly DirectTTL
	if n := len(m.Playing()); n != 0 {
		t.Fatalf("%d plays DirectTTL after the last request ended, want none", n)
	}
}

// A client that comes back to an item within DirectGrace of its last request is
// carrying on (a paused, buffered player asks again), and is admitted even when
// others have filled the limit meanwhile. A stranger is not, and neither is one
// whose play was stopped on purpose.
func TestAReturningDirectPlayIsAdmittedAtTheLimit(t *testing.T) {
	f := newFake(t)
	clk := newTestClock()
	m := fakeManager(t, f, func(o *Options) { o.MaxSessions = 1; o.Clock = clk.Now; o.IdleTimeout = 24 * time.Hour })
	ctx := context.Background()
	paused := DirectKey{ItemID: 1, IP: "10.0.0.2", UserAgent: "Safari"}
	stranger := DirectKey{ItemID: 1, IP: "10.0.0.3", UserAgent: "Chrome"}

	if _, err := brief(m, paused, "Movie", "10.0.0.2 · Safari", 0); err != nil {
		t.Fatal(err)
	}
	clk.Advance(DirectTTL)                                  // no longer counted ...
	if _, err := m.Create(ctx, fakeRequest()); err != nil { // ... and somebody else takes the place
		t.Fatal(err)
	}
	if n := len(m.Playing()); n != 1 {
		t.Fatalf("%d plays, want only the session", n)
	}
	if _, err := brief(m, stranger, "Movie", "10.0.0.3 · Chrome", 0); !errors.Is(err, ErrTooManySessions) {
		t.Fatalf("a stranger at the limit = %v, want ErrTooManySessions", err)
	}
	if err := m.CheckDirect(paused); err != nil {
		t.Errorf("CheckDirect for the player that was here %v ago = %v, want it admitted", DirectTTL, err)
	}
	id, err := brief(m, paused, "Movie", "10.0.0.2 · Safari", 0)
	if err != nil {
		t.Fatalf("the player that comes back at the limit: %v", err)
	}
	if n := len(m.Playing()); n != 2 {
		t.Errorf("%d plays after it came back, want the session and it", n)
	}

	// A play stopped on purpose (Stop in the list) gets no grace: its requests for the
	// file are refused as stopped, and when it announces a play again (the page does,
	// with POST /playback) it is a stranger at the limit like any other.
	if !m.Delete(id) {
		t.Fatal("Delete found no direct play")
	}
	if _, err := brief(m, paused, "Movie", "10.0.0.2 · Safari", 0); !errors.Is(err, ErrStopped) {
		t.Errorf("a play that was stopped on purpose, asking for the file again = %v, want ErrStopped", err)
	}
	if err := m.CheckDirect(paused); !errors.Is(err, ErrTooManySessions) {
		t.Errorf("a play that was stopped on purpose, announced again at the limit = %v, want it refused", err)
	}
	if _, err := brief(m, paused, "Movie", "10.0.0.2 · Safari", 0); !errors.Is(err, ErrTooManySessions) {
		t.Errorf("... and asking for the file = %v, want the limit (no longer ErrStopped)", err)
	}
}

func TestDirectGraceEnds(t *testing.T) {
	f := newFake(t)
	clk := newTestClock()
	m := fakeManager(t, f, func(o *Options) { o.MaxSessions = 3; o.Clock = clk.Now; o.IdleTimeout = 24 * time.Hour })
	ctx := context.Background()
	c := DirectKey{ItemID: 1, IP: "10.0.0.2", UserAgent: "Safari"}
	d := DirectKey{ItemID: 2, IP: "10.0.0.2", UserAgent: "Safari"}
	for _, k := range []DirectKey{c, d} {
		if _, err := brief(m, k, "Movie", "x", 0); err != nil {
			t.Fatal(err)
		}
	}
	clk.Advance(DirectTTL)
	for i := 0; i < 3; i++ { // the limit is full
		if _, err := m.Create(ctx, fakeRequest()); err != nil {
			t.Fatal(err)
		}
	}
	clk.Advance(DirectGrace - DirectTTL - time.Nanosecond)
	if err := m.CheckDirect(c); err != nil {
		t.Errorf("just inside DirectGrace: %v, want it admitted", err)
	}
	clk.Advance(time.Nanosecond)
	if err := m.CheckDirect(d); !errors.Is(err, ErrTooManySessions) {
		t.Errorf("DirectGrace after the last request: %v, want it refused", err)
	}
}

// A session that will not stop (its ffmpeg is on a mount that has gone) does not
// count against the limits for StopGrace, so that the player's own stop-then-start
// goes through, and does after: they must not pile up for ever.
func TestSessionsThatWillNotStopCountAgainstTheLimits(t *testing.T) {
	f := newFake(t)
	clk := newTestClock()
	m := fakeManager(t, f, func(o *Options) { o.MaxSessions = 1; o.Clock = clk.Now; o.IdleTimeout = 24 * time.Hour })
	gate := make(chan struct{}) // never opened, until the test is over
	m.stopGate = gate
	var open sync.Once
	t.Cleanup(func() { open.Do(func() { close(gate) }) })
	ctx := context.Background()

	var stuck []string
	for i := 0; i < 3; i++ {
		s, err := m.Create(ctx, fakeRequest())
		if err != nil {
			t.Fatalf("session %d, each stopped (not yet for long) before the next: %v", i+1, err)
		}
		go m.Delete(s.ID) // does not come back
		id := s.ID
		stuck = append(stuck, id)
		waitUntil(t, "the session to be on its way out", 5*time.Second, func() bool {
			m.mu.Lock()
			defer m.mu.Unlock()
			return len(m.stopping) == i+1
		})
	}
	clk.Advance(StopGrace) // not more than StopGrace yet
	if _, err := m.Create(ctx, fakeRequest()); err != nil {
		t.Fatalf("at StopGrace: %v", err)
	}
	clk.Advance(time.Nanosecond)
	_, err := m.Create(ctx, fakeRequest())
	var le *LimitError
	if !errors.As(err, &le) || le.Limit != "streams" || le.Used < 3 {
		t.Fatalf("play with %d sessions stuck stopping for more than StopGrace = %v (%+v), want the limit, counting them", len(stuck), err, le)
	}

	open.Do(func() { close(gate) }) // they go
	waitUntil(t, "the stuck sessions to be gone", 5*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return len(m.stopping) == 0
	})
}

// Waiting for the init segment is a request in progress: a session whose player
// beats (and so is held to BeatTimeout) is not stopped under it.
func TestWaitForTheInitSegmentIsNotIdle(t *testing.T) {
	sleeper := t.TempDir() + "/ffmpeg"
	if err := writeExecutable(sleeper, "#!/bin/sh\nexec sleep 60\n"); err != nil {
		t.Fatal(err)
	}
	clk := newTestClock()
	m := fakeManager(t, fake{ffmpeg: sleeper}, func(o *Options) { o.Clock = clk.Now; o.WaitTimeout = 2 * time.Second })
	s, err := m.Create(context.Background(), fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	m.Heartbeat(s.ID, Beat{}) // from here on 30 s of silence is the end
	result := make(chan error, 1)
	go func() {
		_, err := s.OpenInit(context.Background())
		result <- err
	}()
	waitUntil(t, "OpenInit to be waiting", 5*time.Second, func() bool { return s.busy.Load() == 1 })
	clk.Advance(2 * time.Minute)
	settle(m)
	if _, ok := m.Get(s.ID); !ok {
		t.Fatal("the session was stopped while a request for its init segment was waiting")
	}
	if err := <-result; !errors.Is(err, ErrTimeout) {
		t.Fatalf("OpenInit = %v, want ErrTimeout (ffmpeg writes nothing)", err)
	}
	// Its silence is counted from when the request ended, not from before it began.
	settle(m)
	if _, ok := m.Get(s.ID); !ok {
		t.Fatal("the session was stopped the moment the request ended")
	}
	clk.Advance(31 * time.Second)
	waitUntil(t, "the session to be stopped 31 s after the request ended", 5*time.Second, func() bool { return m.Count() == 0 })
}

// A session stopped on purpose (Delete: the Activity page, the cap panel) is
// remembered as that for StoppedTTL, so that its player is told it was stopped and
// does not start another; one ply ended because nothing was heard from it, or that
// never was, is not.
func TestASessionStoppedOnPurposeIsRememberedAsStopped(t *testing.T) {
	f := newFake(t)
	clk := newTestClock()
	m := fakeManager(t, f, func(o *Options) { o.Clock = clk.Now })
	ctx := context.Background()
	stopped, err := m.Create(ctx, fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	lost, err := m.Create(ctx, fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	if m.Stopped(stopped.ID) || m.Stopped(lost.ID) || m.Stopped("nosuchsession") {
		t.Fatal("something was stopped before anything was") // positive control
	}
	if !m.Delete(stopped.ID) {
		t.Fatal("Delete found no session")
	}
	if !m.Stopped(stopped.ID) {
		t.Fatal("a session stopped by Delete is not remembered as stopped")
	}
	if m.Stopped("nosuchsession") || m.Stopped(lost.ID) {
		t.Error("an unknown session, or one still running, counts as stopped")
	}
	if m.Heartbeat(stopped.ID, Beat{}) {
		t.Error("a heartbeat for a stopped session was accepted")
	}

	// One that nobody stopped, that ply ended for its silence, is lost, not stopped.
	clk.Advance(61 * time.Second)
	waitUntil(t, "the silent session to be ended", 5*time.Second, func() bool {
		_, ok := m.Get(lost.ID)
		return !ok
	})
	if m.Stopped(lost.ID) {
		t.Error("a session that ply ended for its silence counts as stopped on purpose")
	}
	if !m.Stopped(stopped.ID) {
		t.Error("the stopped session was forgotten before StoppedTTL")
	}

	// It is remembered for StoppedTTL from the stop: 61 s have passed.
	clk.Advance(StoppedTTL - 61*time.Second - time.Nanosecond)
	if !m.Stopped(stopped.ID) {
		t.Error("forgotten a nanosecond before StoppedTTL")
	}
	clk.Advance(time.Nanosecond)
	if m.Stopped(stopped.ID) {
		t.Error("still remembered as stopped StoppedTTL after it was")
	}
}

// The same for a direct play dropped by Delete: its next requests for the file are
// refused as stopped (not counted again), until StoppedTTL has passed or the client
// announces a play (CheckDirect); one that merely expired is not stopped.
func TestADirectPlayDroppedOnPurposeIsRefusedAsStopped(t *testing.T) {
	f := newFake(t)
	clk := newTestClock()
	m := fakeManager(t, f, func(o *Options) { o.Clock = clk.Now })
	k := DirectKey{ItemID: 1, IP: "10.0.0.2", UserAgent: "Safari"}
	other := DirectKey{ItemID: 1, IP: "10.0.0.3", UserAgent: "Chrome"}
	expired := DirectKey{ItemID: 2, IP: "10.0.0.4", UserAgent: "VLC"}

	id, err := brief(m, k, "Movie", "10.0.0.2 · Safari", 0)
	if err != nil {
		t.Fatal(err)
	}
	idExpired, err := brief(m, expired, "Movie", "10.0.0.4 · VLC", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Delete(id) {
		t.Fatal("Delete found no direct play")
	}
	if !m.Stopped(id) {
		t.Error("a direct play dropped by Delete is not remembered as stopped")
	}
	if m.Heartbeat(id, Beat{}) {
		t.Error("a heartbeat for a dropped direct play was accepted")
	}
	if _, err := brief(m, k, "Movie", "10.0.0.2 · Safari", 0); !errors.Is(err, ErrStopped) {
		t.Fatalf("its next request for the file = %v, want ErrStopped", err)
	}
	if n := len(m.Playing()); n != 1 {
		t.Errorf("%d plays, want only the other that is still counted", n)
	}
	if _, err := brief(m, other, "Movie", "10.0.0.3 · Chrome", 0); err != nil { // it is that client that was stopped
		t.Errorf("another client: %v", err)
	}

	// A play that ran out by itself was not stopped by anybody.
	clk.Advance(DirectTTL)
	if m.Stopped(idExpired) {
		t.Error("a direct play that expired counts as stopped on purpose")
	}
	if _, err := brief(m, expired, "Movie", "10.0.0.4 · VLC", 0); err != nil {
		t.Errorf("a direct play that expired, asking again: %v", err)
	}

	// ... and the refusal ends with StoppedTTL.
	clk.Advance(StoppedTTL - DirectTTL - time.Nanosecond)
	if _, err := brief(m, k, "Movie", "10.0.0.2 · Safari", 0); !errors.Is(err, ErrStopped) {
		t.Errorf("a nanosecond before StoppedTTL = %v, want ErrStopped", err)
	}
	clk.Advance(time.Nanosecond)
	if _, err := brief(m, k, "Movie", "10.0.0.2 · Safari", 0); err != nil {
		t.Errorf("StoppedTTL after the stop: %v, want it admitted", err)
	}
	if m.Stopped(id) {
		t.Error("still stopped StoppedTTL later")
	}
}

// A hidden tab beats about once a minute: a paused player has PausedBeatTimeout
// (3 min), exactly, from its last heartbeat.
func TestAPausedPlayerHasThreeMinutes(t *testing.T) {
	f := newFake(t)
	clk := newTestClock()
	m := fakeManager(t, f, func(o *Options) { o.Clock = clk.Now })
	s, err := m.Create(context.Background(), fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	m.Heartbeat(s.ID, Beat{Paused: ptrTo(true)})
	clk.Advance(DefaultPausedBeatTimeout)
	settle(m)
	if _, ok := m.Get(s.ID); !ok {
		t.Fatal("a paused player that beat 3 minutes ago was stopped (30 s is for one that plays)")
	}
	clk.Advance(time.Nanosecond)
	waitUntil(t, "the paused session to be stopped after 3 minutes", 5*time.Second, func() bool { return m.Count() == 0 })
}

// The long window is for as long as the player is paused: a request, or a
// heartbeat that says it is not paused, brings back the 30 s; a heartbeat that
// does not say, leaves things as they are.
func TestThePausedWindowEndsWithARequestOrAnUnpausedBeat(t *testing.T) {
	for _, c := range []struct {
		name string
		do   func(t *testing.T, m *Manager, s *Session)
		kept bool // the 3 minutes still apply afterwards
	}{
		{"a request for a segment", func(t *testing.T, m *Manager, s *Session) {
			seg, err := s.OpenSegment(context.Background(), 0)
			if err != nil {
				t.Fatal(err)
			}
			seg.Close()
		}, false},
		{"a heartbeat that says it plays", func(t *testing.T, m *Manager, s *Session) {
			m.Heartbeat(s.ID, Beat{Paused: ptrTo(false)})
		}, false},
		{"a heartbeat that says nothing of it", func(t *testing.T, m *Manager, s *Session) {
			m.Heartbeat(s.ID, Beat{Position: ptrTo(5.0)})
		}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(t)
			clk := newTestClock()
			m := fakeManager(t, f, func(o *Options) { o.Clock = clk.Now })
			s, err := m.Create(context.Background(), fakeRequest())
			if err != nil {
				t.Fatal(err)
			}
			m.Heartbeat(s.ID, Beat{Paused: ptrTo(true)})
			clk.Advance(100 * time.Second) // well past 30 s, inside 3 minutes
			settle(m)
			if _, ok := m.Get(s.ID); !ok {
				t.Fatal("the paused session was stopped inside its 3 minutes")
			}
			c.do(t, m, s)
			clk.Advance(31 * time.Second)
			if c.kept {
				settle(m)
				if _, ok := m.Get(s.ID); !ok {
					t.Fatal("the paused window ended although nothing said the player plays")
				}
				clk.Advance(DefaultPausedBeatTimeout)
			}
			waitUntil(t, "the session to be stopped", 5*time.Second, func() bool { return m.Count() == 0 })
		})
	}
}

// The paused window cannot be made longer than MaxPausedBeatTimeout.
func TestThePausedWindowHasACeiling(t *testing.T) {
	f := newFake(t)
	clk := newTestClock()
	m := fakeManager(t, f, func(o *Options) { o.Clock = clk.Now; o.PausedBeatTimeout = time.Hour })
	if m.o.PausedBeatTimeout != MaxPausedBeatTimeout {
		t.Fatalf("PausedBeatTimeout = %v, want it held to %v", m.o.PausedBeatTimeout, MaxPausedBeatTimeout)
	}
	s, err := m.Create(context.Background(), fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	m.Heartbeat(s.ID, Beat{Paused: ptrTo(true)})
	clk.Advance(MaxPausedBeatTimeout)
	settle(m)
	if _, ok := m.Get(s.ID); !ok {
		t.Fatal("stopped inside the ceiling")
	}
	clk.Advance(time.Nanosecond)
	waitUntil(t, "the session to be stopped past the ceiling", 5*time.Second, func() bool { return m.Count() == 0 })
}
