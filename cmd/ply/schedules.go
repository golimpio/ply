package main

import (
	"context"
	"time"

	"ply/internal/metadata"
	"ply/internal/schedule"
)

// scheduleTasks is the catalog of background tasks a person can schedule, one row each on Settings ›
// Schedules. Every one is a kind the queue already has, so a scheduled run is the same job a button
// would queue: the queue's priorities, the Activity page and the tier governor treat it the same.
// The keys each needs are checked when it comes due (scheduleChecker).
func scheduleTasks() []schedule.Task {
	return []schedule.Task{
		{
			ID: kindScanFull, Key: "full", Title: "Scan the library",
			Detail:         "Walks every library folder and reads the files that are new or changed. No key is needed.",
			DefaultEnabled: true, DefaultAt: schedule.MustClock("02:30"),
		},
		{
			ID: kindMetaMatch, Key: "match", Title: "Fill in movie and show details",
			Detail:         "Matches what has no details yet and refreshes what is due. Needs the TMDB key, the TVDB key, or both.",
			DefaultEnabled: true, DefaultAt: schedule.MustClock("03:00"),
		},
		{
			ID: kindRatingsMDBList, Key: keyRatingsMDBList, Title: "Fill in Rotten Tomatoes and other ratings",
			Detail:         "Asks MDBList for the Rotten Tomatoes, Metacritic, Letterboxd and Trakt ratings of the titles that lack one or are due.",
			DefaultEnabled: true, DefaultAt: schedule.MustClock("03:30"),
			After: []string{kindScanFull, kindMetaMatch},
		},
		{
			ID: kindRatingsIMDb, Key: keyRatingsIMDb, Title: "Download IMDb ratings",
			Detail:         "Downloads IMDb's datasets and fills in the IMDb ratings of movies, shows and episodes. IMDb refreshes them daily, so a weekly run is enough.",
			DefaultEnabled: true, DefaultAt: schedule.MustClock("04:00"), DefaultDays: []time.Weekday{time.Sunday},
			After: []string{kindScanFull, kindMetaMatch},
		},
	}
}

// scheduleChecker says what each task can do now: the privacy switch, the keys it needs, and which
// key fills which kind of title. The scheduler obeys it before it queues anything, and the settings
// page shows the same words, so the page and the log agree on why a task did not run.
func scheduleChecker(settings *metadata.Settings) schedule.Checker {
	return func(ctx context.Context, task schedule.Task) schedule.Status {
		if settings == nil {
			return schedule.Status{}
		}
		r := settings.Resolve(ctx)
		lookupsOff := schedule.Status{Blocked: "Online metadata lookups are off: turn them on in Details."}
		switch task.ID {
		case kindScanFull:
			return schedule.Status{} // reads the files; asks nobody
		case kindMetaMatch:
			if !r.Enabled {
				return lookupsOff
			}
			var st schedule.Status
			switch {
			case !r.KeySet() && !r.TVDBKeySet():
				st.Missing = []string{"TMDB key", "TVDB key"}
				st.Blocked = "Neither the TMDB key nor the TVDB key is set."
			case !r.KeySet():
				st.Warnings = []string{"The TMDB key is not set: movies are not looked up."}
			case !r.TVDBKeySet():
				st.Warnings = []string{"The TVDB key is not set: shows are looked up at TMDB."}
			}
			return st
		case kindRatingsMDBList:
			if !r.Enabled {
				return lookupsOff
			}
			if !r.MDBListKeySet() {
				return schedule.Status{Missing: []string{"MDBList key"}, Blocked: "The MDBList key is not set."}
			}
		case kindRatingsIMDb:
			if !r.Enabled {
				return lookupsOff
			}
		}
		return schedule.Status{}
	}
}

// watchRatingsWake looks at the schedules again when the ratings service says the keys or the
// privacy switch changed: a task that could not run for a missing key runs at once once it is
// stored, instead of waiting for the next night.
func watchRatingsWake(ctx context.Context, rt *metadata.Ratings, sched *schedule.Scheduler) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-rt.Woken():
			sched.Wake()
		}
	}
}
