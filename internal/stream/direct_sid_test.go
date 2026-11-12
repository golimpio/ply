//go:build unix

package stream

import (
	"errors"
	"slices"
	"testing"
	"time"
)

// A direct play asked for (POST /playback) has its id at once, and the requests that
// carry the id count for it whoever sends them: the Apple TV it was handed to, at
// another address, with another User-Agent.
func TestADirectPlayStartedByIDCountsTheRequestsThatCarryIt(t *testing.T) {
	f := newFake(t)
	clk := newTestClock()
	m := fakeManager(t, f, func(o *Options) { o.Clock = clk.Now; o.MaxSessions = 1 })
	phone := DirectKey{ItemID: 7, Device: "phone-0001"}
	dev := Device{ID: "phone-0001", Name: "Kitchen iPad", Kind: "ipados"}

	id, err := m.StartDirect(phone, "Film", "10.0.0.2 · Safari", "/m/film.mp4", dev, 12, 0)
	if err != nil || id == "" {
		t.Fatalf("StartDirect = %q, %v", id, err)
	}
	p := m.Playing()
	if len(p) != 1 || p[0].ID != id || p[0].Device != dev || p[0].Kind != KindDirect || p[0].PositionSec != 12 {
		t.Fatalf("playing = %+v", p)
	}
	// Counted from the start: the cap of 1 is reached by the play alone.
	if _, _, err := m.Direct(DirectKey{ItemID: 8, IP: "10.0.0.5", UserAgent: "VLC"}, "Other", "x", 0); !errors.Is(err, ErrTooManySessions) {
		t.Fatalf("another play beside it at a cap of 1 = %v, want the limit", err)
	}

	// The Apple TV reads the file with the play's id, for longer than DirectTTL.
	done, ok, err := m.DirectByID(id, 7)
	if err != nil || !ok {
		t.Fatalf("DirectByID = %v, %v", ok, err)
	}
	clk.Advance(2 * DirectTTL)
	if p := m.Playing(); len(p) != 1 || p[0].ID != id {
		t.Fatalf("while its request is served the play ran out: %+v", p)
	}
	done()
	clk.Advance(DirectTTL + time.Second)
	if p := m.Playing(); len(p) != 0 {
		t.Fatalf("DirectTTL after its last request: %+v, want it gone", p)
	}
	// Gone: the id no longer names a play, and the caller counts the request by its key.
	if _, ok, err := m.DirectByID(id, 7); ok || err != nil {
		t.Fatalf("DirectByID of a play that ran out = %v, %v; want not found", ok, err)
	}
}

// The same client asking again for a play of the item it plays (another audio track, a
// retry, a second tab) gets a new play in its place, with an id of its own: what it does
// with the old id (the DELETE its player sends without waiting, the requests of an
// attempt it gave up) cannot end the new one, and the old id answers as stopped. Asking
// again at the limit is not refused: the new play takes the old one's place.
func TestAskingAgainReplacesTheDirectPlay(t *testing.T) {
	f := newFake(t)
	clk := newTestClock()
	m := fakeManager(t, f, func(o *Options) { o.Clock = clk.Now; o.MaxSessions = 1 })
	c := recordChanges(m)
	k := DirectKey{ItemID: 7, Device: "phone-0001"}
	first, err := m.StartDirect(k, "Film", "c", "", Device{ID: "phone-0001"}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.StartDirect(k, "Film", "c", "", Device{ID: "phone-0001"}, 30, 0)
	if err != nil || second == "" || second == first {
		t.Fatalf("asked again at a cap of 1: %q, %v; want a new play (the first was %q)", second, err, first)
	}
	if p := m.Playing(); len(p) != 1 || p[0].ID != second || p[0].PositionSec != 30 {
		t.Fatalf("playing = %+v, want the new play alone", p)
	}
	if got := c.of(first); !slices.Equal(got, []string{PlayStarted, PlayEnded + ":" + EndReplaced}) {
		t.Errorf("the first play: %v, want started, ended:replaced", got)
	}

	// The old id's late DELETE finds nothing; its requests and beats are told it stopped.
	if m.Delete(first) {
		t.Error("the old id's DELETE ended something")
	}
	if _, _, err := m.DirectByID(first, 7); !errors.Is(err, ErrStopped) {
		t.Errorf("a request with the old id = %v, want ErrStopped", err)
	}
	if m.Heartbeat(first, Beat{}) || !m.Stopped(first) {
		t.Error("the old id's heartbeat was taken, or it is not told it stopped")
	}
	// Positive control: the new play is untouched, and its id works.
	done, ok, err := m.DirectByID(second, 7)
	if !ok || err != nil {
		t.Fatalf("the new play after the old id's DELETE: %v, %v", ok, err)
	}
	done()
	if !m.Heartbeat(second, Beat{}) {
		t.Error("the new play's heartbeat was refused")
	}
}

// Of two asks of one client for one play, the newer attempt wins whichever the server
// handles last: the older, arriving late, is refused as stopped and ends nothing, also
// after the newer play was stopped. An ask without an attempt is not ordered, and what
// was seen is forgotten after DirectGrace.
func TestTheNewerAttemptWins(t *testing.T) {
	f := newFake(t)
	clk := newTestClock()
	m := fakeManager(t, f, func(o *Options) { o.Clock = clk.Now })
	k := DirectKey{ItemID: 7, Device: "phone-0001"}
	dev := Device{ID: "phone-0001"}

	newer, err := m.StartDirect(k, "Film", "c", "", dev, 30, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartDirect(k, "Film", "c", "", dev, 0, 1); !errors.Is(err, ErrStopped) {
		t.Fatalf("the older attempt, handled last = %v, want ErrStopped", err)
	}
	if p := m.Playing(); len(p) != 1 || p[0].ID != newer || p[0].PositionSec != 30 {
		t.Fatalf("playing = %+v, want the newer attempt's play", p)
	}
	// Another client's attempts are its own (positive control: the same number is fine there).
	if _, err := m.StartDirect(DirectKey{ItemID: 7, Device: "tv-00000001"}, "Film", "c", "", Device{}, 0, 1); err != nil {
		t.Fatalf("another device's attempt 1 = %v", err)
	}

	// The same attempt again (a POST sent twice), then a newer one: each replaces.
	same, err := m.StartDirect(k, "Film", "c", "", dev, 0, 2)
	if err != nil || same == newer {
		t.Fatalf("attempt 2 again = %q, %v; want a new play", same, err)
	}
	latest, err := m.StartDirect(k, "Film", "c", "", dev, 0, 5)
	if err != nil || latest == same {
		t.Fatalf("attempt 5 = %q, %v; want a new play", latest, err)
	}
	// Stopped on purpose: a late older attempt does not bring it back.
	if !m.Delete(latest) {
		t.Fatal("DELETE of the play found nothing")
	}
	if _, err := m.StartDirect(k, "Film", "c", "", dev, 0, 4); !errors.Is(err, ErrStopped) {
		t.Fatalf("a late attempt after the stop = %v, want ErrStopped", err)
	}
	if p := m.Playing(); slices.ContainsFunc(p, func(p PlayInfo) bool { return p.Device == dev }) {
		t.Fatalf("playing = %+v, want nothing of the phone's", p)
	}
	// Once the stop itself is forgotten (StoppedTTL), and after an ask without an attempt
	// (not ordered, and the newest attempt stays as it was), the older attempt is still refused.
	clk.Advance(StoppedTTL + time.Second)
	if _, err := m.StartDirect(k, "Film", "c", "", dev, 0, 0); err != nil {
		t.Fatalf("an ask without an attempt = %v", err)
	}
	if _, err := m.StartDirect(k, "Film", "c", "", dev, 0, 4); !errors.Is(err, ErrStopped) {
		t.Fatalf("attempt 4 after StoppedTTL and an unordered ask = %v, want ErrStopped", err)
	}
	// A refused ask does not keep the newest attempt longer: it is forgotten DirectGrace after
	// the last ask it was seen with.
	clk.Advance(DirectGrace - StoppedTTL - 2*time.Second)
	if _, err := m.StartDirect(k, "Film", "c", "", dev, 0, 1); !errors.Is(err, ErrStopped) {
		t.Fatalf("attempt 1 just before DirectGrace = %v, want ErrStopped", err)
	}
	clk.Advance(time.Second)
	if _, err := m.StartDirect(k, "Film", "c", "", dev, 0, 1); err != nil {
		t.Fatalf("attempt 1 after DirectGrace (an app reinstalled) = %v", err)
	}
}

// At the limit, an older attempt is told it was given up (not that the limit is reached), and
// a newer attempt refused at the limit still counts: the older one that comes after it is
// refused too, so it cannot take a place that frees up.
func TestAnOlderAttemptAtTheLimit(t *testing.T) {
	f := newFake(t)
	clk := newTestClock()
	m := fakeManager(t, f, func(o *Options) { o.Clock = clk.Now; o.MaxSessions = 1 })
	k := DirectKey{ItemID: 7, Device: "phone-0001"}
	if _, err := m.StartDirect(k, "Film", "c", "", Device{}, 0, 5); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartDirect(k, "Film", "c", "", Device{}, 0, 3); !errors.Is(err, ErrStopped) {
		t.Fatalf("an older attempt at the limit = %v, want ErrStopped", err)
	}
	other := DirectKey{ItemID: 8, Device: "tv-00000001"}
	if _, err := m.StartDirect(other, "Other", "c", "", Device{}, 0, 9); !errors.Is(err, ErrTooManySessions) {
		t.Fatalf("a newer attempt of another play at the limit = %v, want the limit", err)
	}
	if _, err := m.StartDirect(other, "Other", "c", "", Device{}, 0, 7); !errors.Is(err, ErrStopped) {
		t.Fatalf("its older attempt after it = %v, want ErrStopped", err)
	}
}

// Asking again over a limit lowered below what plays (SetLimits) replaces the play
// like any other time: the new play takes the old one's place, it is not admitted again.
// SetLimits never ends a play that is running, and neither does asking again.
func TestAskingAgainOverALoweredLimitIsNotRefused(t *testing.T) {
	f := newFake(t)
	clk := newTestClock()
	m := fakeManager(t, f, func(o *Options) { o.Clock = clk.Now; o.MaxSessions = 2 })
	k := DirectKey{ItemID: 7, Device: "phone-0001"}
	if _, err := m.StartDirect(DirectKey{ItemID: 8, Device: "tv-00000001"}, "Other", "c", "", Device{}, 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartDirect(k, "Film", "c", "", Device{}, 0, 0); err != nil {
		t.Fatal(err)
	}
	m.SetLimits(1, 1) // two plays, a limit of one
	// Positive control: a stranger is refused at this limit.
	if _, err := m.StartDirect(DirectKey{ItemID: 9, Device: "stranger-1"}, "X", "c", "", Device{}, 0, 0); !errors.Is(err, ErrTooManySessions) {
		t.Fatalf("a stranger over the limit = %v, want refused", err)
	}
	again, err := m.StartDirect(k, "Film", "c", "", Device{}, 60, 0)
	if err != nil {
		t.Fatalf("asking again over the lowered limit = %v, want the play replaced", err)
	}
	if p := m.Playing(); len(p) != 2 {
		t.Fatalf("playing = %+v, want the other play and the new one (%s)", p, again)
	}
}

// A direct play whose player says it is paused is kept for the paused lease, as a
// session is: a hidden tab beats about once a minute.
func TestAPausedDirectPlayIsKeptForThePausedLease(t *testing.T) {
	f := newFake(t)
	clk := newTestClock()
	m := fakeManager(t, f, func(o *Options) { o.Clock = clk.Now })
	playing, err := m.StartDirect(DirectKey{ItemID: 1, Device: "playing-01"}, "A", "c", "", Device{}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	paused, err := m.StartDirect(DirectKey{ItemID: 2, Device: "paused-001"}, "B", "c", "", Device{}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	m.Heartbeat(playing, Beat{Paused: ptrTo(false)})
	m.Heartbeat(paused, Beat{Paused: ptrTo(true)})
	clk.Advance(2 * time.Minute)
	ids := func() []string {
		var out []string
		for _, p := range m.Playing() {
			out = append(out, p.ID)
		}
		return out
	}
	// The playing one went after DirectTTL (positive control); the paused one is kept.
	if got := ids(); !slices.Equal(got, []string{paused}) {
		t.Fatalf("2 min of silence: %v, want only the paused play %q", got, paused)
	}
	clk.Advance(DefaultPausedBeatTimeout - 2*time.Minute)
	if got := ids(); len(got) != 0 {
		t.Fatalf("after the paused lease: %v, want none", got)
	}
}

func TestDirectByIDIsForItsItemAndRefusesWhatWasStopped(t *testing.T) {
	f := newFake(t)
	clk := newTestClock()
	m := fakeManager(t, f, func(o *Options) { o.Clock = clk.Now })
	k := DirectKey{ItemID: 7, IP: "10.0.0.2", UserAgent: "Safari"}
	id, err := m.StartDirect(k, "Film", "c", "", Device{}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := m.DirectByID(id, 8); ok {
		t.Error("the id of a play of item 7 counted a request for item 8")
	}
	done, ok, err := m.DirectByID(id, 7) // positive control
	if !ok || err != nil {
		t.Fatalf("DirectByID of its own item = %v, %v", ok, err)
	}
	done()

	m.Delete(id)
	if _, ok, err := m.DirectByID(id, 7); ok || !errors.Is(err, ErrStopped) {
		t.Fatalf("after Delete: %v, %v; want ErrStopped", ok, err)
	}
	// Asking again is starting again.
	again, err := m.StartDirect(k, "Film", "c", "", Device{}, 0, 0)
	if err != nil || again == "" || again == id {
		t.Fatalf("asked again after a stop: %q, %v; want a new play", again, err)
	}
}
