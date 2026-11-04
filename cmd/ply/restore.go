package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"ply/internal/metadata"
	"ply/internal/store"
)

// matcher is the part of the metadata worker that applies a person's choice of
// TMDB title.
type matcher interface {
	MatchMovie(ctx context.Context, itemID int64, ref metadata.Ref) error
	MatchShow(ctx context.Context, show string, ref metadata.Ref) error
}

// restoreRetry is when a manual match that TMDB could not be asked for is tried
// again, and how many tries it gets in a row. A match that is still not applied
// stays in the archive and is reported again after the next scan.
var (
	restoreRetry   = 30 * time.Second
	restoreRetries = 3
)

// restoreKey names a manual match to apply.
type restoreKey struct {
	itemID, tmdbID int64
	kind           string // store.KindMovie, or store.KindSeries for the item's show
}

// restoreReq is a manual match to apply: the archive of a folder that was removed
// (or whose type changed) kept a TMDB title the person chose for this item.
type restoreReq struct {
	restoreKey
	tries int
}

// matchRestorer applies those matches in the background, one at a time: the
// scanner reports them from inside a scan, which must not wait for TMDB. A show is
// matched once per batch however many of its episodes are reported. The archive
// keeps a match until it is applied (Store.ForgetArchivedMatch), so none is lost
// when TMDB is off or out of reach: the scanner reports it again after its next scan.
type matchRestorer struct {
	st   *store.Store
	m    matcher
	log  *slog.Logger
	wake chan struct{}

	mu     sync.Mutex
	queue  []restoreReq
	queued map[restoreKey]bool // in the queue, or being applied, or waiting to be retried
}

func newMatchRestorer(st *store.Store, m matcher, log *slog.Logger) *matchRestorer {
	return &matchRestorer{st: st, m: m, log: log, wake: make(chan struct{}, 1), queued: map[restoreKey]bool{}}
}

// add queues a match unless it is queued already; it never blocks (it is
// Scanner.OnRestoreMatch's function).
func (r *matchRestorer) add(itemID, tmdbID int64, kind string) {
	key := restoreKey{itemID, tmdbID, kind}
	r.mu.Lock()
	if r.queued[key] {
		r.mu.Unlock()
		return
	}
	r.queued[key] = true
	r.mu.Unlock()
	r.push(restoreReq{restoreKey: key})
}

func (r *matchRestorer) push(req restoreReq) {
	r.mu.Lock()
	r.queue = append(r.queue, req)
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// finished takes a match off the books: it may be reported again.
func (r *matchRestorer) finished(req restoreReq) {
	r.mu.Lock()
	delete(r.queued, req.restoreKey)
	r.mu.Unlock()
}

func (r *matchRestorer) take() []restoreReq {
	r.mu.Lock()
	defer r.mu.Unlock()
	batch := r.queue
	r.queue = nil
	return batch
}

// run applies the queued matches until ctx ends.
func (r *matchRestorer) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
		}
		r.drain(ctx)
	}
}

// drain applies everything queued now.
func (r *matchRestorer) drain(ctx context.Context) {
	done := map[string]bool{} // shows matched in this batch
	for _, req := range r.take() {
		if ctx.Err() != nil {
			return
		}
		path, err := r.apply(ctx, req, done)
		switch {
		case err == nil:
			r.forget(ctx, path, req.kind)
			r.finished(req)
		case ctx.Err() != nil:
			return
		case errors.Is(err, metadata.ErrLookupsOff):
			// No key, or the privacy switch is off: the match waits in the archive for
			// the day there is one (the scanner reports it again once lookups are on).
			r.log.Debug("a restored file's TMDB match waits for online lookups", "item", req.itemID, "tmdb", req.tmdbID)
			r.finished(req)
		case metadata.IsTransient(err) && req.tries < restoreRetries:
			req.tries++
			time.AfterFunc(restoreRetry, func() { r.push(req) }) // still on the books until then
		case errors.Is(err, metadata.ErrNoSuchTitle):
			// TMDB does not know the title any more: no later try will find it.
			r.log.Warn("the TMDB title of a restored file's match is gone; the match is dropped", "item", req.itemID,
				"tmdb", req.tmdbID, "kind", req.kind)
			r.forget(ctx, path, req.kind)
			r.finished(req)
		default:
			r.log.Warn("could not give a restored file its TMDB match back; it stays in the archive", "item", req.itemID,
				"tmdb", req.tmdbID, "kind", req.kind, "err", err)
			r.finished(req)
		}
	}
}

// forget takes the match of a kind off the archive row of path, which is applied.
func (r *matchRestorer) forget(ctx context.Context, path, kind string) {
	if path == "" {
		return
	}
	if err := r.st.ForgetArchivedMatch(ctx, path, kind); err != nil && ctx.Err() == nil {
		r.log.Warn("forgetting an archived match that was applied", "path", path, "err", err)
	}
}

// apply gives the item the match; it returns the item's path, for the archive row.
func (r *matchRestorer) apply(ctx context.Context, req restoreReq, done map[string]bool) (path string, err error) {
	e, err := r.st.GetEntry(ctx, req.itemID)
	if err != nil {
		return "", err
	}
	ref := metadata.Ref{TMDBID: req.tmdbID}
	switch req.kind {
	case store.KindMovie:
		return e.Path, r.m.MatchMovie(ctx, req.itemID, ref)
	case store.KindSeries:
		if e.Kind != store.KindEpisode {
			return "", errors.New("the file is no longer an episode")
		}
		key := fmt.Sprintf("%s\x00%d", e.Show, req.tmdbID)
		if done[key] {
			return e.Path, nil
		}
		err = r.m.MatchShow(ctx, e.Show, ref)
		done[key] = err == nil
		return e.Path, err
	}
	return "", fmt.Errorf("unknown title kind %q", req.kind)
}
