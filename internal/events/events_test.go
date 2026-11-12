package events

import (
	"testing"
	"time"
)

var t0 = time.Unix(1_800_000_000, 0)

func types(es []Event) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Type
	}
	return out
}

// Event ids go on growing across a restart, so a client that reconnects with an
// id of the old process gets a snapshot, not a replay of unrelated events.
func TestAnIdFromAnotherProcessResyncs(t *testing.T) {
	old := New(16, t0)
	old.Publish(Jobs, "job.queued", nil)
	stale := old.Last()
	now := New(16, t0.Add(time.Minute)) // ply restarted a minute later
	if _, resync, _, cancel := now.Subscribe(stale, nil); !resync {
		cancel()
		t.Fatal("an id from the old process, before any new event: no snapshot")
	}
	for range 3 {
		now.Publish(Jobs, "job.progress", nil)
	}
	if _, resync, _, cancel := now.Subscribe(stale, nil); !resync {
		cancel()
		t.Fatal("an id from the old process replayed the new one's events")
	}
	// Positive control: an id of this process replays.
	missed, resync, _, cancel := now.Subscribe(now.Last()-1, nil)
	cancel()
	if resync || len(missed) != 1 {
		t.Fatalf("own id: resync=%v missed=%d", resync, len(missed))
	}
	// The hub's very first id is its start: a client that saw nothing of this
	// process must not be told it is up to date.
	fresh := New(16, t0)
	if _, resync, _, cancel := fresh.Subscribe(fresh.Last(), nil); !resync {
		cancel()
		t.Fatal("the start id of a hub with no events replayed (nothing) instead of a snapshot")
	}
}

func TestReplayAndLiveFollowTheTopics(t *testing.T) {
	h := New(16, t0)
	h.Publish(Jobs, "job.queued", nil) // replay needs an id of this process, after its start
	since := h.Last()
	h.Publish(Sessions, "session.started", nil)
	h.Publish(Jobs, "job.done", nil)
	h.Publish(Library, "library", nil)

	// Positive control: all topics replay all three.
	all, resync, _, cancel := h.Subscribe(since, nil)
	cancel()
	if resync || len(all) != 3 {
		t.Fatalf("all topics: resync=%v missed=%v", resync, types(all))
	}
	only, resync, ch, cancel := h.Subscribe(since, Topics{Sessions: true})
	defer cancel()
	if resync || len(only) != 1 || only[0].Type != "session.started" {
		t.Fatalf("sessions only: resync=%v missed=%v", resync, types(only))
	}

	h.Publish(Jobs, "job.started", nil)
	h.Publish(Sessions, "session.ended", nil)
	select {
	case e := <-ch:
		if e.Type != "session.ended" {
			t.Fatalf("live: got %s, want only the sessions topic", e.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("no live event")
	}
}

func TestTheRingForgets(t *testing.T) {
	h := New(4, t0)
	h.Publish(Jobs, "a", nil)
	since := h.Last()
	for range 5 {
		h.Publish(Jobs, "b", nil)
	}
	// The ring holds the 4 newest; the one right after since is gone.
	if _, resync, _, cancel := h.Subscribe(since, nil); !resync {
		cancel()
		t.Fatal("replayed past the ring's reach")
	}
	// Positive control: one step later is still covered.
	missed, resync, _, cancel := h.Subscribe(since+1, nil)
	cancel()
	if resync || len(missed) != 4 {
		t.Fatalf("within reach: resync=%v missed=%d", resync, len(missed))
	}
}

func TestASlowSubscriberIsDropped(t *testing.T) {
	h := New(1024, t0)
	_, _, slow, cancel := h.Subscribe(0, Topics{Jobs: true})
	defer cancel()
	_, _, other, cancelOther := h.Subscribe(0, Topics{Library: true})
	defer cancelOther()
	for range 300 { // more than a subscriber's buffer
		h.Publish(Jobs, "job.progress", nil)
	}
	n := 0
	for range slow {
		n++
	}
	if n != 256 {
		t.Fatalf("the slow subscriber got %d before it was dropped, want its buffer (256)", n)
	}
	// A subscriber of another topic was never sent them, so it is still there.
	h.Publish(Library, "library", nil)
	select {
	case e, ok := <-other:
		if !ok || e.Type != "library" {
			t.Fatalf("other topic: %v %v", e, ok)
		}
	case <-time.After(time.Second):
		t.Fatal("the other topic's subscriber was dropped too")
	}
}

func TestParseTopics(t *testing.T) {
	if ParseTopics("") != nil || ParseTopics("  ") != nil {
		t.Fatal("empty must be all (nil)")
	}
	got := ParseTopics("sessions, jobs,later")
	if len(got) != 2 || !got[Sessions] || !got[Jobs] {
		t.Fatalf("got %v", got)
	}
	if c := ParseTopics("commands"); len(c) != 1 || !c[Commands] {
		t.Fatalf("commands = %v, want the topic of remote control", c)
	}
	none := ParseTopics("later")
	if none == nil || len(none) != 0 || none.Has(Jobs) {
		t.Fatalf("only unknown names must be none, not all: %v", none)
	}
	if !Topics(nil).Has(Tier) {
		t.Fatal("nil must have every topic")
	}
}
