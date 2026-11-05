package jobs

import (
	"sync"
	"time"

	"ply/internal/tier"
)

// Event is one change to the queue, for GET /api/events.
//
// Types: snapshot (reload everything), job.queued, job.updated (merged, bumped),
// job.held (waiting for playback or browsing, or no longer), job.started,
// job.progress, job.retrying, job.done, job.failed, job.cancelled, queue.paused,
// queue.resumed, tier (the state of playback and browsing changed).
type Event struct {
	Seq  int64       `json:"seq"`
	Type string      `json:"type"`
	Job  *Job        `json:"job,omitempty"`
	Tier *tier.State `json:"tier,omitempty"`
}

// hub fans events out to subscribers and keeps the last few for a client that
// reconnects (Last-Event-ID).
type hub struct {
	mu    sync.Mutex
	seq   int64
	start int64 // seq when the hub began: an id at or before it is another process's
	ring  []Event
	size  int
	subs  map[chan Event]struct{}
}

// newHub starts the sequence from the clock: a browser that comes back after a
// restart of ply has an id from the old process, which must not look like one of
// this process's (a replay of the wrong events instead of a snapshot).
func newHub(size int, now time.Time) *hub {
	return &hub{size: size, subs: map[chan Event]struct{}{}, seq: now.UnixMilli() * 1000, start: now.UnixMilli() * 1000}
}

func (h *hub) publish(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	e.Seq = h.seq
	h.ring = append(h.ring, e)
	if len(h.ring) > h.size {
		h.ring = h.ring[len(h.ring)-h.size:]
	}
	for ch := range h.subs {
		select {
		case ch <- e:
		default:
			// A subscriber that cannot keep up is dropped: its stream ends, the
			// browser reconnects and starts from a snapshot.
			delete(h.subs, ch)
			close(ch)
		}
	}
}

// subscribe returns the events after `after` that are still kept (ok false when
// some are gone: the caller must start from a snapshot), and a channel of the
// ones to come. cancel ends the subscription.
func (h *hub) subscribe(after int64) (missed []Event, ok bool, ch <-chan Event, cancel func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := make(chan Event, 256)
	h.subs[c] = struct{}{}
	ok = after > h.start && after <= h.seq
	if ok && len(h.ring) > 0 && h.ring[0].Seq > after+1 {
		ok = false // the ring no longer reaches back that far
	}
	if ok {
		for _, e := range h.ring {
			if e.Seq > after {
				missed = append(missed, e)
			}
		}
	}
	return missed, ok, c, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, live := h.subs[c]; live {
			delete(h.subs, c)
			close(c)
		}
	}
}

func (h *hub) last() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.seq
}

func (q *Queue) publish(e Event) { q.hub.publish(e) }

// Subscribe follows the queue. With after > 0 (the Last-Event-ID a client saw)
// it returns the events since then, if they are still kept; otherwise resync is
// true and the caller sends a Snapshot first. Events come on ch until cancel.
func (q *Queue) Subscribe(after int64) (missed []Event, resync bool, ch <-chan Event, cancel func()) {
	missed, ok, ch, cancel := q.hub.subscribe(after)
	return missed, !ok, ch, cancel
}

// LastSeq is the sequence number of the newest event.
func (q *Queue) LastSeq() int64 { return q.hub.last() }
