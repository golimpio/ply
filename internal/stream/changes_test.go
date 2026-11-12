//go:build unix

package stream

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

// changes records what OnPlayChange is told, as "type id reason".
type changes struct {
	mu  sync.Mutex
	got []PlayChange
}

func recordChanges(m *Manager) *changes {
	c := &changes{}
	m.OnPlayChange(func(pc PlayChange) {
		c.mu.Lock()
		c.got = append(c.got, pc)
		c.mu.Unlock()
	})
	return c
}

// of lists the changes of play id, as "type" or "ended:reason".
func (c *changes) of(id string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, pc := range c.got {
		if pc.Play.ID != id {
			continue
		}
		s := pc.Type
		if pc.Type == PlayEnded {
			s += ":" + pc.Reason
		}
		out = append(out, s)
	}
	return out
}

// A direct play is told as started on its first request, updated on each beat with
// where it is, and ended once when it runs out, however many times the manager
// looks at it afterwards.
func TestPlayChangesOfADirectPlay(t *testing.T) {
	f := newFake(t)
	clk := newTestClock()
	m := fakeManager(t, f, func(o *Options) { o.Clock = clk.Now })
	c := recordChanges(m)
	k := DirectKey{ItemID: 9, IP: "10.0.0.2", UserAgent: "AppleCoreMedia"}

	id, err := brief(m, k, "Some Movie", "10.0.0.2 · AVPlayer", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := brief(m, k, "Some Movie", "10.0.0.2 · AVPlayer", 0); err != nil { // another range request
		t.Fatal(err)
	}
	if got := c.of(id); !slices.Equal(got, []string{PlayStarted}) {
		t.Fatalf("after two requests: %v, want started once", got)
	}
	m.Heartbeat(id, Beat{Position: new(42.0), Paused: new(true)})
	c.mu.Lock()
	last := c.got[len(c.got)-1]
	c.mu.Unlock()
	if last.Type != PlayUpdated || last.Play.PositionSec != 42 || !last.Play.Paused {
		t.Fatalf("after a beat: %+v, want updated at 42 s, paused", last)
	}

	clk.Advance(DefaultPausedBeatTimeout + time.Second) // its last beat said paused: the paused lease
	m.Playing()                                         // the manager looks: the play has run out
	m.Playing()
	m.PlaybackLoad()
	if got := c.of(id); !slices.Equal(got, []string{PlayStarted, PlayUpdated, PlayEnded + ":" + EndExpired}) {
		t.Fatalf("changes = %v, want started, updated, ended:expired, each once", got)
	}
	// What plays changes again (another play starts): the old one's end is not told twice.
	other, err := brief(m, DirectKey{ItemID: 10, IP: "10.0.0.3", UserAgent: "Chrome"}, "Other", "10.0.0.3 · Chrome", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.of(other); !slices.Equal(got, []string{PlayStarted}) { // positive control: the change was seen
		t.Fatalf("the other play: %v", got)
	}
	if got := c.of(id); len(got) != 3 {
		t.Fatalf("after another play started, the ended one: %v, want its three changes still", got)
	}
	// A beat for a play that has ended does not bring it back.
	m.Heartbeat(id, Beat{Position: new(50.0)})
	if got := c.of(id); len(got) != 3 {
		t.Fatalf("a beat after the end was told: %v", got)
	}
}

// A heartbeat that read its session just before the session ended reports it after
// the end (Heartbeat takes the lock again to tell it): it must not be told.
func TestABeatThatLosesTheRaceWithTheEndIsNotTold(t *testing.T) {
	f := newFake(t)
	m := fakeManager(t, f, nil)
	c := recordChanges(m)
	s, err := m.Create(context.Background(), fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	info := s.info()
	m.mu.Lock()
	m.updatedLocked(info) // positive control: while it plays, a beat is told
	m.mu.Unlock()
	m.Delete(s.ID)
	m.mu.Lock()
	m.updatedLocked(info) // the late one
	m.mu.Unlock()
	if got := c.of(s.ID); !slices.Equal(got, []string{PlayStarted, PlayUpdated, PlayEnded + ":" + EndStopped}) {
		t.Fatalf("changes = %v, want started, updated, ended:stopped and nothing after", got)
	}
}

// Every way a session ends is told once, with its reason: stopped (Delete), expired
// (idle), error (it could not start) and shutdown (Close, for the direct plays too).
func TestPlayChangesOfSessions(t *testing.T) {
	f := newFake(t)
	clk := newTestClock()
	m := fakeManager(t, f, func(o *Options) { o.Clock = clk.Now; o.IdleTimeout = 60 * time.Second })
	c := recordChanges(m)
	ctx := context.Background()

	stopped, err := m.Create(ctx, fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	if got := c.of(stopped.ID); !slices.Equal(got, []string{PlayStarted}) {
		t.Fatalf("a new session: %v, want started", got)
	}
	m.Delete(stopped.ID)
	m.Delete(stopped.ID) // again: nothing more
	if got := c.of(stopped.ID); !slices.Equal(got, []string{PlayStarted, PlayEnded + ":" + EndStopped}) {
		t.Fatalf("deleted session: %v", got)
	}

	idle, err := m.Create(ctx, fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(61 * time.Second)
	waitUntil(t, "the idle session to end", 5*time.Second, func() bool { return len(c.of(idle.ID)) == 2 })
	if got := c.of(idle.ID); !slices.Equal(got, []string{PlayStarted, PlayEnded + ":" + EndExpired}) {
		t.Fatalf("idle session: %v", got)
	}

	open, err := m.Create(ctx, fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	direct, err := brief(m, DirectKey{ItemID: 3, IP: "10.0.0.9", UserAgent: "Chrome"}, "Film", "10.0.0.9 · Chrome", 0)
	if err != nil {
		t.Fatal(err)
	}
	m.Close()
	for _, id := range []string{open.ID, direct} {
		if got := c.of(id); !slices.Equal(got, []string{PlayStarted, PlayEnded + ":" + EndShutdown}) {
			t.Errorf("play %s at Close: %v", id, got)
		}
	}
}

func TestPlayChangesOfASessionThatCannotStart(t *testing.T) {
	f := newFake(t)
	m := fakeManager(t, f, func(o *Options) { o.FFmpeg = filepath.Join(t.TempDir(), "no-such-ffmpeg") })
	c := recordChanges(m)
	if _, err := m.Create(context.Background(), fakeRequest()); err == nil {
		t.Fatal("control: a session started without an ffmpeg")
	}
	c.mu.Lock()
	got := slices.Clone(c.got)
	c.mu.Unlock()
	if len(got) != 2 || got[0].Type != PlayStarted || got[1].Type != PlayEnded || got[1].Reason != EndError ||
		got[0].Play.ID != got[1].Play.ID {
		t.Fatalf("changes = %v, want the session started, then ended:error", fmt.Sprint(got))
	}
}
