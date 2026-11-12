package main

import (
	"context"
	"encoding/json"
	"errors"

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

// How the ratings jobs look for work: the metadata pass fills their schedules, which queue them
// (docs/M12-SCHEDULES.md).
const mdblistBatch = 20 // titles the MDBList job reads from the store at a time

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
