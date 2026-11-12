package events

import (
	"slices"
	"testing"
	"time"
)

func seqs(es []Event) []int64 {
	out := make([]int64, len(es))
	for i, e := range es {
		out[i] = e.Seq
	}
	return out
}

// An event for one device reaches that device's subscribers alone; one for everyone, everyone.
func TestAnEventForADeviceReachesItAlone(t *testing.T) {
	h := New(16, t0)
	_, _, tv, cancelTV := h.SubscribeAs(0, nil, "tv-0000001")
	defer cancelTV()
	_, _, phone, cancelPhone := h.SubscribeAs(0, nil, "phone-0001")
	defer cancelPhone()
	_, _, nobody, cancelNobody := h.Subscribe(0, nil)
	defer cancelNobody()

	h.Publish(Sessions, "session.updated", nil) // control: everyone's
	h.PublishTo(Commands, "command.pause", "tv-0000001", t0.Add(time.Hour), nil)
	got := func(ch <-chan Event) (out []string) {
		for {
			select {
			case e := <-ch:
				out = append(out, e.Type)
			default:
				return out
			}
		}
	}
	if g := got(tv); !slices.Equal(g, []string{"session.updated", "command.pause"}) {
		t.Errorf("the TV saw %q", g)
	}
	if g := got(phone); !slices.Equal(g, []string{"session.updated"}) {
		t.Errorf("the phone saw %q, want the event for everyone alone", g)
	}
	if g := got(nobody); !slices.Equal(g, []string{"session.updated"}) {
		t.Errorf("a subscriber of no device saw %q", g)
	}
}

// Commands are kept apart from the ring: a burst of progress cannot push one out, and a device
// that comes back gets it, in order with the rest of what it missed.
func TestCommandsOutliveTheRingAndAreReplayedBySeq(t *testing.T) {
	h := New(4, t0)
	h.now = func() time.Time { return t0 }
	h.Publish(Jobs, "job.queued", nil)
	since := h.Last()
	play := h.PublishTo(Commands, "command.play", "tv-0000001", t0.Add(time.Minute), func(seq int64) any {
		return map[string]int64{"seq": seq, "itemId": 7}
	})
	h.Publish(Jobs, "job.started", nil)

	missed, resync, _, cancel := h.SubscribeAs(since, nil, "tv-0000001")
	cancel()
	if resync || !slices.Equal(types(missed), []string{"command.play", "job.started"}) || !slices.IsSorted(seqs(missed)) {
		t.Fatalf("replay = %q (%v), resync %v; want the command, then the job, by seq", types(missed), seqs(missed), resync)
	}
	other, _, _, cancel := h.SubscribeAs(since, nil, "phone-0001")
	cancel()
	if !slices.Equal(types(other), []string{"job.started"}) {
		t.Errorf("another device's replay = %q, want no command of the TV's", types(other))
	}

	for range 10 {
		h.Publish(Jobs, "job.progress", nil)
	}
	if _, resync, _, cancel := h.SubscribeAs(since, nil, "tv-0000001"); !resync {
		cancel()
		t.Fatal("control: the ring no longer reaches back, a resync is due")
	} else {
		cancel()
	}
	if p := h.Pending("tv-0000001"); len(p) != 1 || p[0].Seq != play {
		t.Errorf("pending = %v, want the command still there for the snapshot", p)
	} else if d, _ := p[0].Data.(map[string]int64); d["seq"] != play {
		t.Errorf("payload %v, want it to carry its own seq %d", p[0].Data, play)
	}
	if p := h.Pending("phone-0001"); len(p) != 0 {
		t.Errorf("pending for another device = %v", p)
	}
}

// A replay holds what came after the id the device saw: a command from before is not sent again,
// and an addressed event in the ring (an answer) reaches only its device.
func TestAReplayIsWhatCameAfterAndForTheDevice(t *testing.T) {
	h := New(64, t0)
	h.now = func() time.Time { return t0 }
	early := h.PublishTo(Commands, "command.stop", "tv-0000001", t0.Add(time.Minute), nil)
	since := h.Last()
	late := h.PublishTo(Commands, "command.pause", "tv-0000001", t0.Add(time.Minute), nil)
	answer := h.PublishTo(Commands, "command.acked", "phone-0001", time.Time{}, nil)

	tv, _, _, cancel := h.SubscribeAs(since, nil, "tv-0000001")
	cancel()
	if !slices.Equal(seqs(tv), []int64{late}) {
		t.Errorf("the TV's replay = %v, want the late command %d alone (not %d from before, nor the phone's answer %d)",
			seqs(tv), late, early, answer)
	}
	phone, _, _, cancel := h.SubscribeAs(since, nil, "phone-0001")
	cancel()
	if !slices.Equal(seqs(phone), []int64{answer}) { // control: the answer is in the ring, for the phone
		t.Errorf("the phone's replay = %v, want its answer %d", seqs(phone), answer)
	}
}

// A device that saw the last event the ring let go of still gets a replay: everything after it is kept.
func TestAReplayFromTheLastDroppedEvent(t *testing.T) {
	h := New(2, t0)
	h.Publish(Jobs, "job.queued", nil)
	last := h.Last()
	h.Publish(Jobs, "job.started", nil)
	h.Publish(Jobs, "job.done", nil) // the ring holds two: job.queued goes
	missed, resync, _, cancel := h.Subscribe(last, nil)
	cancel()
	if resync || !slices.Equal(types(missed), []string{"job.started", "job.done"}) {
		t.Errorf("replay from the dropped event = %q, resync %v; want the two after it", types(missed), resync)
	}
	if _, resync, _, cancel := h.Subscribe(last-1, nil); !resync { // control: one before it is lost
		cancel()
		t.Error("a replay from before the dropped event, without a snapshot")
	} else {
		cancel()
	}
}

// A device has PendingMax commands waiting at most: past that, the oldest goes.
func TestPendingCommandsAreCappedPerDevice(t *testing.T) {
	h := New(16, t0)
	h.now = func() time.Time { return t0 }
	var sent []int64
	for range PendingMax + 3 {
		sent = append(sent, h.PublishTo(Commands, "command.pause", "tv-0000001", t0.Add(time.Minute), nil))
	}
	h.PublishTo(Commands, "command.stop", "phone-0001", t0.Add(time.Minute), nil)
	if got := seqs(h.Pending("tv-0000001")); !slices.Equal(got, sent[3:]) {
		t.Errorf("pending = %v, want the newest %d: %v", got, PendingMax, sent[3:])
	}
	if n := len(h.Pending("phone-0001")); n != 1 {
		t.Errorf("another device's pending = %d, want its own one", n)
	}
}

// A command is acked once, by its device; one that has expired is gone, acked or not.
func TestCommandsAreAckedOrExpire(t *testing.T) {
	h := New(16, t0)
	now := t0
	h.now = func() time.Time { return now }
	stop := h.PublishTo(Commands, "command.stop", "tv-0000001", t0.Add(time.Minute), nil)
	seek := h.PublishTo(Commands, "command.seek", "tv-0000001", t0.Add(time.Minute), nil)

	if _, ok := h.Ack(stop, "phone-0001"); ok {
		t.Error("another device acked the TV's command")
	}
	if e, ok := h.Ack(stop, "tv-0000001"); !ok || e.Type != "command.stop" {
		t.Fatalf("ack = %+v, %v", e, ok)
	}
	if _, ok := h.Ack(stop, "tv-0000001"); ok {
		t.Error("a command acked twice")
	}
	if p := h.Pending("tv-0000001"); len(p) != 1 || p[0].Seq != seek { // control: the other is still pending
		t.Fatalf("pending = %v, want the seek", p)
	}

	now = t0.Add(time.Minute)
	if p := h.Pending("tv-0000001"); len(p) != 0 {
		t.Errorf("pending after it expired = %v", p)
	}
	if _, ok := h.Ack(seek, "tv-0000001"); ok {
		t.Error("an expired command was acked")
	}
}
