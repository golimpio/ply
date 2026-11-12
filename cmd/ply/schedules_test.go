package main

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ply/internal/jobs"
	"ply/internal/metadata"
	"ply/internal/schedule"
	"ply/internal/store"
)

// scheduleTestSettings is the settings resolver the schedule checker reads, over a store of the
// test's and with the keys so gives.
func scheduleTestSettings(t *testing.T, so func(*metadata.SettingsOptions)) *metadata.Settings {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "ply.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	o := metadata.SettingsOptions{Store: st, Logger: slog.New(slog.DiscardHandler)}
	if so != nil {
		so(&o)
	}
	return metadata.NewSettings(o)
}

func statusOf(t *testing.T, c schedule.Checker, id string) schedule.Status {
	t.Helper()
	for _, task := range scheduleTasks() {
		if task.ID == id {
			return c(context.Background(), task)
		}
	}
	t.Fatalf("the catalog has no task %q", id)
	return schedule.Status{}
}

// The catalog is the four kinds the queue already has, at the times the documentation gives, and the
// scheduler accepts it.
func TestScheduleCatalog(t *testing.T) {
	tasks := scheduleTasks()
	want := []struct {
		id, key, at string
		days        []time.Weekday
	}{
		{kindScanFull, "full", "02:30", nil},
		{kindMetaMatch, "match", "03:00", nil},
		{kindRatingsMDBList, keyRatingsMDBList, "03:30", nil},
		{kindRatingsIMDb, keyRatingsIMDb, "04:00", []time.Weekday{time.Sunday}},
	}
	if len(tasks) != len(want) {
		t.Fatalf("the catalog has %d tasks, want %d", len(tasks), len(want))
	}
	for i, w := range want {
		got := tasks[i]
		if got.ID != w.id || got.Key != w.key || got.DefaultAt.String() != w.at || !got.DefaultEnabled {
			t.Errorf("task %d = %+v, want %s/%s at %s and on", i, got, w.id, w.key, w.at)
		}
		if !slices.Equal(got.DefaultDays, w.days) {
			t.Errorf("task %s runs on %v, want %v", w.id, got.DefaultDays, w.days)
		}
		if got.Title == "" || got.Detail == "" {
			t.Errorf("task %s has no title or detail for the page", w.id)
		}
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "ply.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := schedule.New(schedule.Options{Store: st, Queue: jobs.New(jobs.Options{}), Tasks: tasks}); err != nil {
		t.Fatalf("the catalog the server registers is not accepted: %v", err)
	}
}

// What the page shows and the scheduler obeys: the privacy switch first, then the keys each task
// needs. A warning is something it will not do with the keys it has.
func TestScheduleChecker(t *testing.T) {
	var (
		tmdbKey = func(o *metadata.SettingsOptions) { o.KeyDefault = "tmdb-key" }
		tvdbKey = func(o *metadata.SettingsOptions) { o.TVDBKeyDefault = "tvdb-key" }
		mdbKey  = func(o *metadata.SettingsOptions) { o.MDBListKeyFlag = "mdblist-key" }
	)
	both := func(o *metadata.SettingsOptions) { tmdbKey(o); tvdbKey(o); mdbKey(o) }
	for name, tc := range map[string]struct {
		so      func(*metadata.SettingsOptions)
		meta    schedule.Status
		mdblist schedule.Status
		imdb    schedule.Status
	}{
		"all keys": {both, schedule.Status{}, schedule.Status{}, schedule.Status{}},
		"no keys": {func(o *metadata.SettingsOptions) {}, schedule.Status{
			Missing: []string{"TMDB key", "TVDB key"}, Blocked: "Neither the TMDB key nor the TVDB key is set."},
			schedule.Status{Missing: []string{"MDBList key"}, Blocked: "The MDBList key is not set."}, schedule.Status{}},
		"TMDB only": {func(o *metadata.SettingsOptions) { tmdbKey(o); mdbKey(o) }, schedule.Status{
			Warnings: []string{"The TVDB key is not set: shows are looked up at TMDB."}}, schedule.Status{}, schedule.Status{}},
		"TVDB only": {func(o *metadata.SettingsOptions) { tvdbKey(o); mdbKey(o) }, schedule.Status{
			Warnings: []string{"The TMDB key is not set: movies are not looked up."}}, schedule.Status{}, schedule.Status{}},
		"no MDBList key": {func(o *metadata.SettingsOptions) { tmdbKey(o); tvdbKey(o) }, schedule.Status{},
			schedule.Status{Missing: []string{"MDBList key"}, Blocked: "The MDBList key is not set."}, schedule.Status{}},
		"lookups off": {func(o *metadata.SettingsOptions) { both(o); o.Disabled = true },
			schedule.Status{Blocked: "Online metadata lookups are off: turn them on in Details."},
			schedule.Status{Blocked: "Online metadata lookups are off: turn them on in Details."},
			schedule.Status{Blocked: "Online metadata lookups are off: turn them on in Details."}},
	} {
		t.Run(name, func(t *testing.T) {
			check := scheduleChecker(scheduleTestSettings(t, tc.so))
			for id, want := range map[string]schedule.Status{
				kindMetaMatch: tc.meta, kindRatingsMDBList: tc.mdblist, kindRatingsIMDb: tc.imdb,
			} {
				got := statusOf(t, check, id)
				if got.Blocked != want.Blocked || !slices.Equal(got.Missing, want.Missing) {
					t.Errorf("%s = %+v, want blocked %q and missing %v", id, got, want.Blocked, want.Missing)
				}
				if len(want.Warnings) == 0 && len(got.Warnings) != 0 {
					t.Errorf("%s warns %v, want none", id, got.Warnings)
				}
				for _, w := range want.Warnings {
					if !slices.Contains(got.Warnings, w) {
						t.Errorf("%s warnings = %v, want %q", id, got.Warnings, w)
					}
				}
			}
			if scan := statusOf(t, check, kindScanFull); scan.Blocked != "" || len(scan.Missing) != 0 {
				t.Errorf("the scan needs no key: %+v", scan)
			}
		})
	}
}

// ratingsTasks is the catalog's two ratings tasks: the kinds a ratingsRig's queue has.
func ratingsTasks() []schedule.Task {
	var out []schedule.Task
	for _, task := range scheduleTasks() {
		if task.ID == kindRatingsMDBList || task.ID == kindRatingsIMDb {
			out = append(out, task)
		}
	}
	return out
}

// startRatingSchedule runs a schedule over the rig's queue and store, at now, until stop.
func startRatingSchedule(t *testing.T, r *ratingsRig, now *time.Time, log *bytes.Buffer) func() {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	if log != nil {
		logger = slog.New(slog.NewTextHandler(log, nil))
	}
	s, err := schedule.New(schedule.Options{Store: r.st, Queue: r.q, Tasks: ratingsTasks(), Check: scheduleChecker(r.settings),
		Logger: logger, Now: func() time.Time { return *now }, Every: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
	t.Cleanup(stop)
	return stop
}

// The ratings schedules queue their jobs when they fall due, at the priority of refreshes and with
// the keys that merge repeats; the run is recorded in the database, so a restart at the same time
// does not scan or ask again, and the next time it is due it does.
func TestScheduledRatingsRunOnceAndRememberTheRun(t *testing.T) {
	r := newRatingsRig(t, func(o *metadata.SettingsOptions) {
		o.KeyDefault, o.TVDBKeyDefault = "tmdb-key", "tvdb-key"
	}, nil)
	r.addMovies(3)
	r.queue(nil)
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC) // Monday
	stop := startRatingSchedule(t, r, &now, nil)
	waitUntil(t, "the IMDb job and the titles' job queued", 5*time.Second, func() bool { return len(r.queued()) == 2 })
	got := r.queued()
	if j, ok := got[kindRatingsIMDb+"/"+keyRatingsIMDb]; !ok || j.Priority != jobs.PriorityRefresh || j.Title != "Fetch IMDb ratings" {
		t.Errorf("the IMDb job = %+v, %v", j, ok)
	}
	if j, ok := got[kindRatingsMDBList+"/"+keyRatingsMDBList]; !ok || j.Priority != jobs.PriorityRefresh || j.Title != "Fetch ratings from MDBList" {
		t.Errorf("the MDBList job = %+v, %v", j, ok)
	}
	// The schedule only queues: nobody is asked by the look itself.
	if r.hits.Load() != 0 || r.mdb.Load() != 0 {
		t.Errorf("the schedule made %d + %d requests", r.hits.Load(), r.mdb.Load())
	}
	stop()

	// A restart at the same time: the database remembers both runs.
	r.queue(nil)
	startRatingSchedule(t, r, &now, nil)
	time.Sleep(150 * time.Millisecond)
	if n := len(r.queued()); n != 0 {
		t.Errorf("after a restart at the same time: %d queued, want none", n)
	}

	// A week later, on the IMDb schedule's Sunday, both are due again.
	now = time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC) // Sunday
	r.queue(nil)
	startRatingSchedule(t, r, &now, nil)
	waitUntil(t, "both jobs queued a week later", 5*time.Second, func() bool { return len(r.queued()) == 2 })
}

// A task whose key is missing is not queued and the log says which key; with lookups off neither
// ratings task runs. The control has the keys and runs both.
func TestScheduledRatingsAreSkippedWithoutTheirKeys(t *testing.T) {
	for name, tc := range map[string]struct {
		so      func(*metadata.SettingsOptions)
		imdb    bool
		mdblist bool
		logWord string
	}{
		"control": {func(o *metadata.SettingsOptions) { o.KeyDefault = "t"; o.TVDBKeyDefault = "t"; o.MDBListKeyFlag = "m" },
			true, true, ""},
		"no MDBList key": {func(o *metadata.SettingsOptions) { o.KeyDefault = "t"; o.TVDBKeyDefault = "t"; o.MDBListKeyFlag = "" },
			true, false, "API key is missing"},
		"lookups off": {func(o *metadata.SettingsOptions) {
			o.KeyDefault, o.TVDBKeyDefault, o.MDBListKeyFlag = "t", "t", "m"
			o.Disabled = true
		}, false, false, "Online metadata lookups are off"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRatingsRig(t, tc.so, nil)
			r.addMovies(2)
			r.queue(nil)
			now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
			var log bytes.Buffer
			startRatingSchedule(t, r, &now, &log)
			time.Sleep(200 * time.Millisecond) // the first look, and every task's decision
			_, hasIMDb := r.queued()[kindRatingsIMDb+"/"+keyRatingsIMDb]
			_, hasMDBList := r.queued()[kindRatingsMDBList+"/"+keyRatingsMDBList]
			if hasIMDb != tc.imdb || hasMDBList != tc.mdblist {
				t.Errorf("queued IMDb %v, MDBList %v; want %v and %v", hasIMDb, hasMDBList, tc.imdb, tc.mdblist)
			}
			if tc.logWord != "" && !strings.Contains(log.String(), tc.logWord) {
				t.Errorf("the log does not say %q: %s", tc.logWord, log.String())
			}
			if tc.logWord != "" {
				// The reason is said once, not at every look of the loop.
				if n := strings.Count(log.String(), "scheduled task skipped"); n > 2 {
					t.Errorf("the skip is logged %d times: %s", n, log.String())
				}
			}
		})
	}
}

// A job for the titles is already queued: the schedule does not queue a second one, and the look is
// still recorded.
func TestScheduledRatingsDoNotQueueWhileBusy(t *testing.T) {
	r := newRatingsRig(t, func(o *metadata.SettingsOptions) {
		o.KeyDefault, o.TVDBKeyDefault = "t", "t"
	}, nil)
	r.addMovies(2)
	r.queue(nil)
	if _, err := r.q.Enqueue(t.Context(), kindRatingsMDBList, jobs.Opt{Key: "by hand"}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	startRatingSchedule(t, r, &now, nil)
	waitUntil(t, "the IMDb job while the titles' job is busy", 5*time.Second, func() bool {
		_, ok := r.queued()[kindRatingsIMDb+"/"+keyRatingsIMDb]
		return ok
	})
	time.Sleep(100 * time.Millisecond)
	n := 0
	for _, j := range r.queued() {
		if j.Kind == kindRatingsMDBList {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d jobs for the titles queued, want only the one that was busy", n)
	}
}

// When the MDBList key is stored the task that could not run is filled in at once: the ratings
// service says the keys changed, and the schedule looks again.
func TestStoringAKeyRunsTheSkippedScheduleAtOnce(t *testing.T) {
	r := newRatingsRig(t, func(o *metadata.SettingsOptions) {
		o.KeyDefault, o.TVDBKeyDefault = "t", "t"
		o.MDBListKeyFlag = "" // nothing yet: the task is blocked, and the log says so
	}, nil)
	r.addMovies(2)
	r.queue(nil)
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	var log bytes.Buffer
	s, err := schedule.New(schedule.Options{Store: r.st, Queue: r.q,
		Tasks: []schedule.Task{{ID: kindRatingsMDBList, Key: keyRatingsMDBList, Title: "Ratings", Detail: "Rated.",
			DefaultEnabled: true, DefaultAt: schedule.MustClock("03:30")}},
		Check: scheduleChecker(r.settings), Logger: slog.New(slog.NewTextHandler(&log, nil)),
		Now: func() time.Time { return now }, Every: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	go watchRatingsWake(ctx, r.rt, s)
	t.Cleanup(func() { cancel(); <-done })

	time.Sleep(150 * time.Millisecond)
	if n := len(r.queued()); n != 0 {
		t.Fatalf("control: a task with no key queued %d jobs", n)
	}
	if !strings.Contains(log.String(), "The MDBList key is not set.") {
		t.Fatalf("control: the skip is not logged: %s", log.String())
	}

	key := "mdblist-key-stored-now"
	if err := r.settings.Update(ctx, metadata.Update{MDBListKey: &key}); err != nil {
		t.Fatal(err)
	}
	r.rt.Wake() // what the settings API does after a key is stored
	waitUntil(t, "the task to run with the key stored", 5*time.Second, func() bool {
		_, ok := r.queued()[kindRatingsMDBList+"/"+keyRatingsMDBList]
		return ok
	})
}
