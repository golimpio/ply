package api

import (
	"sync"
	"time"

	"ply/internal/art"
)

// artHoldFor is how long after a play of an item has been set up that no frame is
// grabbed from its file: the first seconds of a play read the file from the start,
// and a grab (tens of megabytes over a slow share) would share the link with them.
// A variable for the tests.
var artHoldFor = 10 * time.Second

// playStarts remembers which items are being set up to play, and which were a
// moment ago.
type playStarts struct {
	mu      sync.Mutex
	pending map[int64]int       // item -> plays being set up
	until   map[int64]time.Time // item -> the end of the hold after the last one was
}

// begin notes that a play of id is being set up. The returned func says how it ended:
// started if a session (or a direct play) was made, in which case the hold lasts
// artHoldFor from now.
func (p *playStarts) begin(id int64, now func() time.Time) (end func(started bool)) {
	p.mu.Lock()
	if p.pending == nil {
		p.pending, p.until = map[int64]int{}, map[int64]time.Time{}
	}
	p.pending[id]++
	p.mu.Unlock()
	return func(started bool) {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.pending[id]--; p.pending[id] <= 0 {
			delete(p.pending, id)
		}
		t := now()
		if started {
			p.until[id] = t.Add(artHoldFor)
		}
		if len(p.until) > 64 {
			for k, u := range p.until { // a long run plays many items: forget the old holds
				if !u.After(t) {
					delete(p.until, k)
				}
			}
		}
	}
}

// holding says whether id is being set up or just was, and for how much longer it
// is certain to be (a whole second at least, for a Retry-After).
func (p *playStarts) holding(id int64, now time.Time) (time.Duration, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pending[id] > 0 {
		return artHoldFor, true
	}
	if u, ok := p.until[id]; ok {
		if left := u.Sub(now); left > 0 {
			return left, true
		}
		delete(p.until, id)
	}
	return 0, false
}

// clock is the time the server goes by.
func (s *Server) clock() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// beginPlay notes that a play of the item is being set up and takes the disk from
// the frame grab for it, if one is under way: the item's art is made later, by the
// queue (ArtLater), once the play lets go of the disk. The returned func says how it
// ended.
func (s *Server) beginPlay(id int64) (end func(started bool)) {
	end = s.plays.begin(id, s.clock)
	if s.Art != nil {
		for _, g := range s.Art.Defer(id) {
			s.deferArt(g.Src, g.Kind, true) // the grab that was asked for a moment ago is stopped: ask again
		}
	}
	return end
}

// deferArt hands the grab of an image to the queue. again says that the grab was asked
// for before and has been stopped since, so that it must not be taken for one that is
// waiting in the queue already.
func (s *Server) deferArt(src art.Source, k art.Kind, again bool) {
	if s.ArtLater != nil {
		s.ArtLater(src, k, again)
	}
}
