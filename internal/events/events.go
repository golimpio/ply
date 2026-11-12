// Package events is ply's live feed: what changed, for GET /api/events. The parts
// of the server publish to one Hub, each under its topic; a client follows the
// topics it wants and, when it reconnects, gets what it missed
// (docs/M11-CLIENTS.md, "The live feed").
package events

import (
	"cmp"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"
)

// Topic is what an event is about. A client asks for topics by name.
type Topic string

const (
	Jobs     Topic = "jobs"     // the background queue
	Library  Topic = "library"  // what the library shows
	Sessions Topic = "sessions" // what plays
	Tier     Topic = "tier"     // the load of playback and browsing
	Commands Topic = "commands" // remote control: what a device is asked to do, and the answers
)

var known = []Topic{Jobs, Library, Sessions, Tier, Commands}

// Event is one change. Data is its payload, sent as its JSON: an object, nil for
// none.
type Event struct {
	Seq   int64
	Topic Topic
	Type  string
	Data  any
	// To is the device the event is for; "" is everyone who follows its topic.
	To string
	// Expires is when a command stops being one (docs/M11-CLIENTS.md, "Remote control").
	Expires time.Time
}

// isFor reports whether a subscriber of device sees e.
func (e Event) isFor(device string) bool { return e.To == "" || e.To == device }

// Topics is a set of topics; nil is all of them.
type Topics map[Topic]bool

// ParseTopics reads ?topics=a,b. Empty is all of them; a name it does not know is
// left out (a newer client asking an older ply), so a list of nothing but unknown
// names is none.
func ParseTopics(s string) Topics {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	t := Topics{}
	for f := range strings.SplitSeq(s, ",") {
		if name := Topic(strings.TrimSpace(f)); slices.Contains(known, name) {
			t[name] = true
		}
	}
	return t
}

// Has reports whether t includes topic.
func (t Topics) Has(topic Topic) bool { return t == nil || t[topic] }

// Hub fans events out to subscribers and keeps the last few for a client that
// reconnects (Last-Event-ID). Commands are kept apart from the ring, until they are
// acked or expire, so that the busy topics cannot push them out.
type Hub struct {
	mu      sync.Mutex
	seq     int64
	start   int64 // seq when the hub began: an id at or before it is another process's
	ring    []Event
	dropped int64 // the newest seq the ring let go of: a replay reaches back to after it
	size    int
	subs    map[chan Event]subscriber
	pending map[int64]Event // the commands not acked yet, by seq
	now     func() time.Time
}

type subscriber struct {
	topics Topics
	device string // "" sees only the events for everyone
}

// New keeps the last size events. The sequence starts from the clock: a client
// that comes back after a restart of ply has an id from the old process, which
// must not look like one of this process's (a replay of the wrong events instead of
// a snapshot).
func New(size int, now time.Time) *Hub {
	start := now.UnixMilli() * 1000
	return &Hub{size: size, subs: map[chan Event]subscriber{}, pending: map[int64]Event{}, seq: start, start: start,
		dropped: start, now: time.Now}
}

// Publish sends an event for everyone to the subscribers of its topic. It never
// blocks: a subscriber that cannot keep up is dropped (its channel is closed; the
// client reconnects and starts from a snapshot).
func (h *Hub) Publish(topic Topic, typ string, data any) {
	h.publish(Event{Topic: topic, Type: typ, Data: data}, nil)
}

// PendingMax is how many commands a device may have waiting: past it, the oldest goes.
const PendingMax = 32

// PublishTo sends an event to the subscribers of device alone ("" for everyone) and
// returns its sequence number; data makes its payload, which may carry that number (it
// runs under the hub's lock: it must not block, nor call the hub). A command (topic
// Commands with an expiry) is also kept until it is acked or expires, PendingMax a device
// at most.
func (h *Hub) PublishTo(topic Topic, typ, device string, expires time.Time, data func(seq int64) any) int64 {
	return h.publish(Event{Topic: topic, Type: typ, To: device, Expires: expires}, data)
}

// publish sends e, with the payload data makes from its sequence number when data is not nil.
func (h *Hub) publish(e Event, data func(int64) any) int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	e.Seq = h.seq
	if data != nil {
		e.Data = data(e.Seq)
	}
	if e.Topic == Commands && !e.Expires.IsZero() {
		h.prune()
		if waiting := h.pendingFor(e.To, 0); len(waiting) >= PendingMax {
			delete(h.pending, waiting[0].Seq) // the oldest
		}
		h.pending[e.Seq] = e
	} else {
		h.ring = append(h.ring, e)
		if over := len(h.ring) - h.size; over > 0 {
			h.dropped = h.ring[over-1].Seq
			h.ring = h.ring[over:]
		}
	}
	for ch, sub := range h.subs {
		if !sub.topics.Has(e.Topic) || !e.isFor(sub.device) {
			continue
		}
		select {
		case ch <- e:
		default:
			delete(h.subs, ch)
			close(ch)
		}
	}
	return e.Seq
}

func bySeq(a, b Event) int { return cmp.Compare(a.Seq, b.Seq) }

// prune lets go of the commands that have expired.
func (h *Hub) prune() {
	now := h.now()
	maps.DeleteFunc(h.pending, func(_ int64, e Event) bool { return !now.Before(e.Expires) })
}

// pendingFor is the commands to device not acked yet, after seq, in order.
func (h *Hub) pendingFor(device string, after int64) []Event {
	h.prune()
	var out []Event
	for _, e := range h.pending {
		if e.To == device && e.Seq > after {
			out = append(out, e)
		}
	}
	slices.SortFunc(out, bySeq)
	return out
}

// Pending is the commands to device not acked yet, oldest first: the commands section of
// its snapshot.
func (h *Hub) Pending(device string) []Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pendingFor(device, 0)
}

// Ack takes the command seq for device away, and returns it; false when there is none
// (it was never for device, was acked already, or has expired).
func (h *Hub) Ack(seq int64, device string) (Event, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.prune()
	e, ok := h.pending[seq]
	if !ok || e.To != device {
		return Event{}, false
	}
	delete(h.pending, seq)
	return e, true
}

// Subscribe follows the hub's events of the given topics that are for everyone. With
// after > 0 (the Last-Event-ID a client saw) it returns those of them since then, if
// all the events since then are still kept; otherwise resync is true and the caller
// starts the client from a snapshot. Events come on ch until cancel.
func (h *Hub) Subscribe(after int64, topics Topics) (missed []Event, resync bool, ch <-chan Event, cancel func()) {
	return h.SubscribeAs(after, topics, "")
}

// SubscribeAs is Subscribe for device: its own events too. The commands to it not acked
// yet are replayed by seq when the ring reaches back to after; on a resync they are the
// snapshot's to give (Pending).
func (h *Hub) SubscribeAs(after int64, topics Topics, device string) (missed []Event, resync bool, ch <-chan Event, cancel func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := make(chan Event, 256)
	h.subs[c] = subscriber{topics: topics, device: device}
	// The ring has every event after dropped but the commands, which are kept apart: its ids
	// have gaps, and only what it let go of is missing.
	ok := after > h.start && after <= h.seq && after >= h.dropped
	if ok {
		for _, e := range h.ring {
			if e.Seq > after && topics.Has(e.Topic) && e.isFor(device) {
				missed = append(missed, e)
			}
		}
		if device != "" && topics.Has(Commands) {
			missed = append(missed, h.pendingFor(device, after)...)
			slices.SortFunc(missed, bySeq)
		}
	}
	return missed, !ok, c, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, live := h.subs[c]; live {
			delete(h.subs, c)
			close(c)
		}
	}
}

// Now is the hub's clock, the one commands expire by.
func (h *Hub) Now() time.Time { return h.now() }

// Last is the sequence number of the newest event.
func (h *Hub) Last() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.seq
}
