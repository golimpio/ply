package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"ply/internal/jobs"
	"ply/internal/metadata"
	"ply/internal/store"
)

// The queue's kinds of job for ratings (docs/M10-RATINGS.md). They are network work with no
// Need: only a stalled viewer holds them (they use neither the disks nor the CPU to speak of).
// Neither uses TMDB or TVDB, so neither shares a limiter with the metadata pass (meta.match).
const (
	kindRatingsIMDb    = "ratings.imdb"    // IMDb's datasets: fetch if a week old, map the episodes, store the library's ratings
	kindRatingsMDBList = "ratings.mdblist" // Rotten Tomatoes, Metacritic, Letterboxd and Trakt ratings of every title that is due
)

// Keys of the two jobs: one of each at a time, however often the schedule asks.
const (
	keyRatingsIMDb    = "imdb"
	keyRatingsMDBList = "mdblist"
)

// How the ratings schedule looks for work.
const (
	mdblistTick  = 2 * time.Minute  // titles due for MDBList, within the day's budget
	ratingsTick  = 15 * time.Minute // IMDb's datasets
	mdblistBatch = 20               // titles the MDBList job reads from the store at a time
)

// nudgeSettle is how long after a nudge (the metadata pass matched titles) the schedule looks: a first
// scan of a big library matches for minutes, and one look soon after the first is what it needs. A
// variable for the tests.
var nudgeSettle = 20 * time.Second

// registerRatings declares the ratings kinds. d.ratings nil: no ratings (tests).
func registerRatings(q *jobs.Queue, d queueDeps) {
	rt := d.ratings
	if rt == nil {
		return
	}
	// A failure that asking again cannot mend ends the job at once.
	final := func(err error) error {
		if err != nil && (errors.Is(err, metadata.ErrIMDbFormat) || errors.Is(err, metadata.ErrIMDbShort) || metadata.IsUnauthorized(err)) {
			return jobs.Permanent(err)
		}
		return err
	}
	q.Register(jobs.Spec{
		Kind:     kindRatingsIMDb,
		Priority: jobs.PriorityRefresh,
		Title:    func(json.RawMessage) string { return "Fetch IMDb ratings" },
		Run: func(ctx context.Context, r *jobs.Run) error {
			r.Progress(0, 0, "IMDb datasets")
			return final(rt.RunIMDb(ctx))
		},
	})
	// One job for every title that is due, not one each: a library of thousands would otherwise
	// fill the queue. It ends when nothing is due, the day's budget is spent, or MDBList fails (see
	// RunMDBListDue), and yields to playback between titles.
	q.Register(jobs.Spec{
		Kind:     kindRatingsMDBList,
		Priority: jobs.PriorityRefresh,
		Title:    func(json.RawMessage) string { return "Fetch ratings from MDBList" },
		Run: func(ctx context.Context, r *jobs.Run) error {
			return final(rt.RunMDBListDue(ctx, mdblistBatch, func(ctx context.Context, done int, t store.RatingsDue) error {
				r.Progress(done, 0, t.Title)
				return r.Yield(ctx)
			}))
		},
	})
}

// scheduleRatings queues the ratings work as it falls due, until ctx ends: IMDb's datasets when a
// week has passed or the library has IMDb ids the ratings were not written for, and the titles
// MDBList is due to be asked about, never more than the day's budget has left. Everything is a
// no-op while nothing may leave the machine.
func scheduleRatings(ctx context.Context, q *jobs.Queue, rt *metadata.Ratings, log *slog.Logger) {
	enqueue := func(kind, key string) {
		if _, err := q.Enqueue(ctx, kind, jobs.Opt{Key: key}); err != nil && ctx.Err() == nil {
			log.Warn("queue: scheduling", "kind", kind, "err", err)
		}
	}
	files := func() {
		if rt.IMDbDue(ctx) {
			enqueue(kindRatingsIMDb, keyRatingsIMDb)
		}
	}
	titles := func() {
		if q.Busy(kindRatingsMDBList) {
			return // the last run is still going
		}
		due, err := rt.DueMDBList(ctx, 1)
		if err != nil {
			if ctx.Err() == nil {
				log.Warn("ratings: listing titles due", "err", err)
			}
			return
		}
		if len(due) > 0 {
			enqueue(kindRatingsMDBList, keyRatingsMDBList)
		}
	}
	files()
	titles()
	fileT := time.NewTicker(ratingsTick)
	defer fileT.Stop()
	titleT := time.NewTicker(mdblistTick)
	defer titleT.Stop()
	settle := time.NewTimer(time.Hour)
	settle.Stop()
	defer settle.Stop()
	armed := false // a look is already due in nudgeSettle
	for {
		select {
		case <-ctx.Done():
			return
		case <-fileT.C:
			files()
		case <-titleT.C:
			titles()
		case <-rt.Woken():
			files()
			titles()
		case <-rt.Nudged():
			// The metadata pass nudges this each time it runs, every minute or so; reading the library
			// for the dataset's sake that often is more than it needs: one look, nudgeSettle after the first.
			if !armed {
				armed = true
				settle.Reset(nudgeSettle)
			}
		case <-settle.C:
			armed = false
			files()
			titles()
		}
	}
}
