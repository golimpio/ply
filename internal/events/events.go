// Package events is ply's live feed: what changed, for GET /api/events. The parts
// of the server publish to one Hub, each under its topic; a client follows the
// topics it wants and, when it reconnects, gets what it missed
// (docs/M11-CLIENTS.md, "The live feed").
package events

import (
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
)

var known = []Topic{Jobs, Library, Sessions, Tier}

// Event is one change. Data is its payload, sent as its JSON: an object, nil for
// none.
type Event struct {
	Seq   int64
	Topic Topic
	Type  string
	Data  any
}

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
// reconnects (Last-Event-ID).
type Hub struct {
	mu    sync.Mutex
	seq   int64
	start int64 // seq when the hub began: an id at or before it is another process's
	ring  []Event
	size  int
	subs  map[chan Event]Topics
}

// New keeps the last size events. The sequence starts from the clock: a client
// that comes back after a restart of ply has an id from the old process, which
// must not look like one of this process's (a replay of the wrong events instead of
// a snapshot).
func New(size int, now time.Time) *Hub {
	start := now.UnixMilli() * 1000
	return &Hub{size: size, subs: map[chan Event]Topics{}, seq: start, start: start}
}

// Publish sends an event to the subscribers of its topic. It never blocks: a
// subscriber that cannot keep up is dropped (its channel is closed; the client
// reconnects and starts from a snapshot).
func (h *Hub) Publish(topic Topic, typ string, data any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	e := Event{Seq: h.seq, Topic: topic, Type: typ, Data: data}
	h.ring = append(h.ring, e)
	if len(h.ring) > h.size {
		h.ring = h.ring[len(h.ring)-h.size:]
	}
	for ch, topics := range h.subs {
		if !topics.Has(topic) {
			continue
		}
		select {
		case ch <- e:
		default:
			delete(h.subs, ch)
			close(ch)
		}
	}
}

// Subscribe follows the hub's events of the given topics. With after > 0 (the
// Last-Event-ID a client saw) it returns those of them since then, if all the
// events since then are still kept; otherwise resync is true and the caller starts
// the client from a snapshot. Events come on ch until cancel.
func (h *Hub) Subscribe(after int64, topics Topics) (missed []Event, resync bool, ch <-chan Event, cancel func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := make(chan Event, 256)
	h.subs[c] = topics
	ok := after > h.start && after <= h.seq
	if ok && len(h.ring) > 0 && h.ring[0].Seq > after+1 {
		ok = false // the ring no longer reaches back that far
	}
	if ok {
		for _, e := range h.ring {
			if e.Seq > after && topics.Has(e.Topic) {
				missed = append(missed, e)
			}
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

// Last is the sequence number of the newest event.
func (h *Hub) Last() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.seq
}
