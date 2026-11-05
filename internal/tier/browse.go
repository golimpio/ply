package tier

import (
	"net/http"
	"sync/atomic"
	"time"
)

// browseMeter tells whether someone is browsing: a browse request is in flight,
// or one ended less than the quiet time ago.
type browseMeter struct {
	inflight atomic.Int64
	lastEnd  atomic.Int64 // UnixNano of the end of the last browse request
}

func (b *browseMeter) busy(now time.Time, quiet time.Duration) bool {
	if b.inflight.Load() > 0 {
		return true
	}
	last := b.lastEnd.Load()
	return last != 0 && now.Sub(time.Unix(0, last)) < quiet
}

// Track returns middleware that counts the requests isBrowse picks as browsing:
// the library pages a person is waiting for. Polling (status, events) and playback
// (segments, files) must not be counted, or the queue would never run.
func (g *Governor) Track(isBrowse func(*http.Request) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isBrowse(r) {
				next.ServeHTTP(w, r)
				return
			}
			g.browse.inflight.Add(1)
			defer func() {
				g.browse.lastEnd.Store(g.o.Now().UnixNano())
				g.browse.inflight.Add(-1)
			}()
			next.ServeHTTP(w, r)
		})
	}
}
