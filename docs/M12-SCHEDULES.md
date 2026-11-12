# M12: schedules for the background work

The owner asked (2026-11-12) that the background work run at set times rather than on fixed intervals: folder scans
at a time of night, and each metadata source with a schedule of its own that fills in what a title lacks. A task
whose API key is missing must not run, must say so in the log, and the page must show which tasks cannot run. And
the jobs had to stay the jobs that already existed: the same kinds, the same queue, the same Activity page — only
when they are queued changes.

## Tasks

Every schedule is a kind the queue already has (`cmd/ply/schedules.go`), so a scheduled run and a "Scan now" queue
the same work, with the same priority, governor and Activity entries.

| Task (queue kind) | What it fills in | Key | Default |
|---|---|---|---|
| `scan.full` | walks every library folder and reads what is new or changed | none | every day at 02:30 |
| `meta.match` | details that are missing or due, movies at TMDB and shows at TVDB | a TMDB key, a TVDB key, or both | every day at 03:00 |
| `ratings.mdblist` | Rotten Tomatoes, Metacritic, Letterboxd and Trakt ratings of the titles that lack one or are due, within MDBList's daily budget | the MDBList key | every day at 03:30 |
| `ratings.imdb` | IMDb's datasets, and the IMDb ratings of movies, shows and episodes | none (the privacy switch) | Sundays at 04:00 |

`ratings.imdb` is weekly because IMDb republishes daily and the job itself keeps its own week (`IMDbEvery`); running
it every night would ask for a file that has not changed.

## A schedule

`{task, enabled, at (a time of day, HH:MM), days (ISO weekdays 1..7; empty is every day), last_run}`. The times are
the server's own local clock: ply runs where its library is, and the browser is usually on the same clock.

- **The database is the truth** (migration 015, `schedules`): the table keeps the settings and `last_run`, so a
  restart neither repeats a run that happened nor loses one that is due. The scheduler writes a task's defaults the
  first time it sees it, so every task with a schedule has a row.
- **A schedule that never ran is due at once.** That is what the first start does (an empty library is scanned), and
  the first start after this version: every task runs once on its defaults and then keeps its time. A restart does
  not scan again.
- **A run missed while ply was off is caught up** at the first look: the next occurrence after `last_run` is in the
  past, so the task runs now, and `last_run` becomes that moment. Not the occurrence's own time: a schedule that was
  due long ago would otherwise be queued again at every look until `last_run` caught up.
- **A task that is already queued or running is not queued twice**; the look is still recorded, so it is not tried
  again every 30 s.
- **Turned off**, a task stores no run and queues nothing. Its settings stay.

## Keys and the privacy switch

The checker (`scheduleChecker`) is the same answer for the log and for the page:

- the privacy switch is first: with online lookups off, `meta.match` and both ratings tasks cannot run, and say so;
- `ratings.mdblist` needs the MDBList key; without it the task is blocked and the page names the key;
- `meta.match` needs one of the two provider keys: with neither it is blocked, with one of them it runs and warns
  what the absent key means (movies at TMDB only, shows looked up at TMDB); the built-in TMDB and TVDB keys mean
  this is usually a warning rather than a refusal;
- `scan.full` needs nothing, and `ratings.imdb` needs only the privacy switch.

**A blocked task is not queued.** The scheduler logs, once a day for each occurrence it could not run,

```
level=WARN msg="scheduled task skipped: API key is missing" task=ratings.mdblist key="MDBList key" reason="The MDBList key is not set."
```

or, with nothing missing, `msg="scheduled task skipped"` with the reason (lookups off). `last_run` is left alone: the
page shows the same reason, and the next look after the key is stored runs the task at once. The page and the log
use the same words, so a task's card always says why the log did not show it queued.

**`last_run` is when the task was queued**, not the occurrence's time. The page's "Last run" is therefore true; the
next run is the next occurrence after it.

## API and page

- `GET /api/schedules` → `[{task, title, detail, enabled, at ("HH:MM"), days ([1..7], [] every day), lastRun?,
  next?, blocked?, missing: [...], warnings: [...]}]`, in the catalog's order. `next` is absent when the schedule is
  off or blocked. The lists are never null.
- `PUT /api/schedules/{task}` `{enabled?, at?, days?}`: one field at a time or all three; 400 for a time or a day
  that is not one, 404 for a task this server does not schedule.
- `POST /api/schedules/{task}/run`: queues the task now as a person's request (priority 0). 204; 409 with the reason
  when it is blocked, or already queued or running. It does not need the schedule to be on, and a manual run does not
  move the schedule.
- All three are private, like the queue's own endpoints (they are behind the token and refuse a page of another
  site).
- Settings → **Schedules** (`pages/settings/SchedulesTab.svelte`, `components/ScheduleCard.svelte`) shows one card
  per task: the switch, the time, the seven day chips (none ticked is every day), the next and last run, and a
  "Run now" button (there whether the schedule is on or off: a person may ask for one run). A blocked task says why, names the missing key, links to API keys (or Details for lookups off),
  and its "Run now" is disabled. Every change is saved at once (`PUT`), and the card keeps the server's answer. The
  words for days and times are `lib/schedules.ts`, tested on their own.

## The daemon, its jobs, and the children

The jobs stay in the daemon's queue (`internal/jobs`), which is also how they talk to it: progress, holding reasons,
cancel, and the persisted state. The change here is that **the daemon's stop is a signal of its own**:

- `jobs.ErrStopping` is the cause of a running job's context when ply stops (a person's cancel stays
  `jobs.ErrCancelled`), and `Run.Stopping()` tells a job which of the two it is. The jobs queue's `Run` gives its
  jobs a context of its own, cancelled explicitly when its own context ends, so a shutdown is not the anonymous
  `context.Canceled` a parent would give.
- Every running job is cancelled with that cause at once, and the queue waits for them (as it always did): a scan
  stops between files, kills the ffprobe it was waiting on (its context), and is queued again for the next start.
  `finish` recognises the cause, so the job goes back to `queued` rather than being recorded as failed or cancelled.
- A job that starts in the same moment sees `q.stopping` and is signalled at once, so there is no window in which a
  new job begins after the stop.
- The scanner's pace is checked before every probe and every 100 directories of a walk, and a cancelled scan writes
  nothing of the pass it did not finish.
- **Low priority** is the queue's: `scan.full` is `PriorityScan` (30), below a person, the watcher's files and new
  matches; it is the heavy lane and does not start while a video plays from the library's disks. Its only OS-level
  demotion is on what it starts: every ffprobe is niced (`scanNice`, 10), with the macOS background policy and
  Linux idle I/O class (`internal/sysprio`). The daemon's own walk is not niced — that would demote playback too —
  and the tier governor is what keeps it out of playback's way.
- No separate worker process: the jobs keep the scanner, the worker and the store in one process, which is what keeps
  a scan, the watcher and a partial scan consistent with each other (the owner's choice, 2026-11-12).

## The scheduler loop

`internal/schedule.Scheduler.Run` looks at the schedules at once and then every 30 s (`Options.Every`), and at once
when woken:

- a key or the privacy switch changed (`Ratings.Wake`, watched in `cmd/ply/main.go`);
- the page saved a schedule: `Set` stores the row and wakes the loop, so a task turned on, or one made due by a new
  time, runs at once rather than at the next half minute;
- on start, the first look is before the queue's own start finishes; an enqueue before `Queue.Run` is supported.

Each look reads the table once, asks the checker per task, and queues what is due. Nothing in the loop asks a
provider: the ratings jobs do that when they run, and a look of an empty schedule is one small read.

## As built (2026-11-12)

- **Store:** `migrations/015_schedules.sql`, `store/schedules.go` (`Schedules`, `PutSchedule`,
  `MarkScheduleRun`; days as ISO weekdays; high-priority write for a person's change, low for `last_run`; no list
  Generation change). Tested by `store/schedules_test.go`, the migration test included.
- **`internal/schedule`:** `Clock` (HH:MM in JSON), `nextAt`/`due`/`nextRun`, the catalog types, `Scheduler`
  (`Run`, `Wake`, `List`, `Set`, `RunNow`), `Status`/`Refused`/`ErrBusy`/`ErrInvalid`/`ErrUnknownTask`. Tests:
  `schedule_test.go` (the clock and its JSON, the next weekday, due and next, the tick once and after a restart, a
  blocked task logged once and run when unblocked, busy, a failed enqueue, off, Set/List/RunNow, Run with a wake,
  a bad catalog).
- **`cmd/ply`:** `schedules.go` (catalog, checker, the ratings wake watcher); `main.go` builds the scheduler after
  the queue, serves `GET/PUT/POST` through `api.Options.Schedules`, and the old `schedule()` and `scheduleRatings()`
  tickers are gone. `-scan-interval` is still read (an old unit file starts) and warns that the scan now runs on its
  schedule. Tests: `schedules_test.go` (the catalog, the checker over every key combination and privacy, the two
  ratings tasks queued once and remembered across a restart, skipped without keys with the log, busy, and a stored
  key waking the blocked task at once).
- **API:** `internal/api/schedules.go` (the interface, the three handlers, 503/404/400/409), the routes, private
  list, `Options.Schedules`; tests in `schedules_test.go` and the auth matrix in `meta_settings_test.go`.
- **Web:** `lib/schedules.ts` + tests (days, schedule words, next/last words), `lib/api.ts` (`ScheduledTask`,
  `ScheduleUpdate`, `normaliseSchedule`, `schedules`, `updateSchedule`, `runSchedule`) + tests, the Schedules tab,
  `ScheduleCard`, the tab in `lib/settings-tabs.ts`, and `e2e/settings-ui.mjs` (the page, the blocked key, the switch,
  the time, a day, Run now, the link to keys, and the tab count and keyboard order updated).

## Known limits

- **One time of day per task.** A task that should run twice runs once; the queue's own due checks (`IMDbDue`,
  `TitlesDueForRefresh`) mean a nightly run does what it needs, and a second run adds nothing.
- **The look is every 30 s**, so a run is at most half a minute late. A schedule whose time is in the hour the
  clocks go forward may fall at the normalised local time Go gives.
- **Server time, not the browser's.** A browser in another time zone sees the `at` as the server's clock, and the
  next run's instant in its own time.
- **A newly matched title gets its ratings at the next ratings run**, not at once: that is what a schedule is. A
  key stored after a skip is the exception (it runs at once), and "Run now" is the other.
- **`last_run` is one timestamp**, not a history: the Activity page keeps the jobs' history, with what each run did.
- **The ratings jobs' own gates still apply**: MDBList's daily budget, its backoff, the IMDb copies' week. A nightly
  run of a task that is not due returns quickly.
- **The scan's file walk is not niced**, only the probes it starts: the governor, not `nice`, keeps it clear of
  playback (see above).
