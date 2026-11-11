# M9 — performance architecture: playback first, browsing second, a queue for the rest

The owner's direction (2026-11-04):
- Performance must be "stellar". Playback has its own priority system, browsing comes second, and a background job
  queue (scans, metadata, admin tasks) comes third and never interferes with the other two.
- An activity icon shows when work is running and opens an Activity page listing what is running and what is queued.
- Never lose track of what is playing (no orphan ffmpeg); a setting for the maximum number of parallel plays.
- macOS (Apple Silicon) is the only host the owner can test: tune for it first; Linux is "works, not tuned".
- Build autonomously, best outcome over speed. New dependencies are allowed when they earn their place over the stdlib.

This document is the architecture review (a read-only pass by the strongest model available, then checked against
the code) plus the measurements behind it. File:line references are from HEAD 5384c78 plus the M7 working tree.

## Measured on the owner's setup (NAS test library `/Volumes/NAS-Disk3-6T/Movies-3`, SMB over Wi-Fi)

- Wi-Fi link: 802.11ax, 5 GHz ch 36 / 80 MHz, 1200 Mbit/s, -56 dBm, MCS 9. Healthy; not the bottleneck.
- NAS round trip: 5-11 ms (avg 8 ms).
- Sequential read from the NAS (`dd bs=4m`, cold): **about 2.5 MB/s (20 Mbit/s)** measured on en1 while reading a
  6.5 GB MKV. The limit is on the NAS side (its disk, its own link or SMB settings), not the Mac.
- Consequences at that rate: today's first-play keyframe scan of a 6.5 GB MKV reads the whole file: ~44 minutes
  before the first segment. A 4K direct play (50-80 Mbit/s) cannot keep up at all; 1080p rips (5-10 Mbit/s) can, with
  nothing else on the link.
- Library: ~263 movie folders, mostly 1080p MKV rips (3-7 GB). The share holds AppleDouble `._*.mkv` files; the
  scanner already skips dot-files (`internal/library/scanner.go:465`).

## The three findings that matter most

**F1. Time-to-first-frame over SMB is a whole-file read on the critical path.** `stream.Manager.Create`
(`internal/stream/manager.go:339-347`) blocks on `m.keyframes` for every remux; on a cache miss that runs
`ExtractKeyframes` (`internal/stream/keyframes.go:18-23`), `ffprobe -skip_frame nokey -show_entries packet=...`, which
demuxes every packet (`-skip_frame` skips decoding, not reading). Local SSD hid it (42 ms for 98 MB,
`docs/NOTES.md:17-18`).

**F2. One SQLite connection serialises every tier behind every other.** `internal/store/store.go:66-68`
`SetMaxOpenConns(1)`. Browse reads (`ListItems`, unpaged), the direct-play open (`internal/api/handlers.go:233-257`),
session start (`manager.go:406-413`) and `PUT /progress` every 10 s queue behind the scanner (`UpsertItem`, one tx
per file, 4 workers) and the metadata worker (`UpsertTitle`, one big tx per title). The per-connection-pragma reason
is moot: `_pragma=` in the DSN (`store.go:172`) applies to every connection. `synchronous` is FULL.

**F3. Nothing yields CPU or disk to playback.** Background children get `nice 10` only (`cmd/ply/main.go:38`,
`internal/probe/nice_unix.go`, `internal/art/proc_unix.go`, `internal/subs/proc_unix.go`, `keyframes.go:35`): no
Darwin `PRIO_DARWIN_BG`, no Linux ioprio. The scanner keeps 4 ffprobes running whatever the sessions do
(`scanner.go:1038`); software transcodes use ~9 cores with no `-threads` cap. Whole-file readers (keyframe scan,
subtitle extraction `internal/subs/subs.go:62`, art grabs `internal/art/art.go:345-354`) share the NAS link with
playback. `nice` does not throttle I/O on either OS.

## Other findings, ranked
4. One semaphore for full and partial scans (`scanner.go:69,136-145`; `partial.go:57`): a big full scan hides a new
   file for minutes. The watcher's coalescing is right and stays.
5. Browse can block on the WAN: `handleTMDBImage` (`internal/api/images.go:31-45`) downloads on a miss inside the
   request (4-slot semaphore, `internal/metadata/images.go:38,189`).
6. The metadata pass is one coarse, uncancellable unit doing TMDB calls and image downloads inline. The TMDB client's
   bucket, 429 pause and backoff (`internal/metadata/tmdb.go:31-34,225,263-316`) are good: they become the queue's
   per-service limiter.
7. Completion is polled, not signalled: `afterScans` polls every 5 s (`main.go:237-285`), the UI polls `/api/scan`
   every 1 s and Settings every 3 s. Three status shapes, no history, no cancel, no progress.
8. Big write transactions: `DeleteItems` (`items.go:404-414`) and M7's `ArchiveAndDeleteRoot` delete any number of
   rows in one tx.
9. HTTP server is fine (`ReadHeaderTimeout` only; middleware does not wrap the writer, so sendfile survives). No
   admission control. GC/GOMAXPROCS are non-issues (22 MB RSS).
10. Every partial scan loads the whole library state (`partial.go:63`). Cheap once F2 is fixed.
11. **`deploy/com.ply.server.plist:42-43` sets `ProcessType=Background`**: launchd then throttles ply and every
    ffmpeg it starts (low CPU, throttled I/O), playback included. Same for `Nice=5` in `deploy/ply.service:24`.
    Make the server Interactive/Standard and demote only background children.

## Time-to-first-frame (TTFF)

Today, remux of an MKV to hls.js:

| Step | Where | Reads over SMB |
|---|---|---|
| Play page `GET /api/items/{id}` + art | `internal/api/art.go:46-62` | with no TMDB art, an ffmpeg frame grab (`thumbnail=12`): tens of MB, concurrent with the session start |
| `POST /playback` → `Decide` → `attachSession` | `internal/api/stream.go:28` | one stat |
| `Manager.Create` → `keyframes()` miss → `ExtractKeyframes` | `manager.go:342,406-452` | **the whole file**, on the critical path; cached in `keyframes` afterwards |
| ffmpeg `-ss X -i file -c copy … -hls_time 0.001` | `session.go:114-189`, `args.go:62-118` | probe pre-roll (~5 MB / 5 s), SeekHead + Cues, first GOPs |
| hls.js playlist, init, seg0 | `session.go:104,423-611` | all GOPs of segment 0 (≥ 6 s of video) |
| subtitles, if a default/forced text track | `Player.svelte:92-112` → `subs.Generator` | **the whole container again** (`subs.go:62`, 10-minute timeout) |

Target: about 1-2 s to first frame even over SMB.
1. **Keyframe index from the container index, not a packet scan.**
   - MKV: SeekHead → Cues. Filter `CueTrack` to the video track's TrackNumber, honour `TimestampScale`, look for a
     second SeekHead at the end, and subtract the first cluster timestamp the way `start_time` is subtracted today.
   - MP4/MOV: `stss` + `stts` + **`ctts` and `edts/elst`**. Presentation time needs composition offsets and the edit
     list, otherwise every keyframe is off by a frame or two and `SeekTarget` (`args.go:138`) lands on the wrong
     frame. No `stss` means all-intra (valid). Fragmented MP4 (`moof`, no `stss`): `mfra/tfra`, or fall back. Pick
     the `trak` of `Request.VideoIndex`.
   - Cache into `keyframes` as today. The full ffprobe scan stays as the fallback, never on the request path.
2. **Sparse Cues break the fragment-per-keyframe design.** If Cues list fewer keyframes than the stream has, ffmpeg
   writes more fragments than the plan expects and numbering slips. Map fragments by their `tfdt` (the box parser in
   `internal/stream/mp4box.go` can read it), not by file number, or validate the index against the first run's
   fragments and switch to the fallback when they disagree.
3. **The fallback for an index-less file** (MKV without Cues, TS, AVI) is an EVENT playlist built from fragment
   `tfdt`s as they land, flipping to VOD when the background full scan finishes; or a VideoToolbox transcode (never
   libx264). Uniform transcode-style segments are not valid for copy.
4. **Subtitles:** pre-extract text tracks in the background index job, stream a VTT that is still being made, and
   never extract at full rate while a session reads the same volume.

   **Built (2026-11-05, reviewed and reworked 2026-11-06):**
   - `subs.preextract` (below, "Kinds in use"): after a file is indexed, and from a sweep at start and after each
     scan, the queue reads the container once for all its text tracks (one ffmpeg run with one `-map` output per
     track, so a film with five languages costs one read, not five) and writes them to the subs cache. It needs the
     item's disk, steps aside through the queue's yield between its reads (each sidecar, then the container), and a
     play that starts while ffmpeg reads *suspends* it (SIGSTOP to its group, SIGCONT after) instead of killing it:
     a read that took 40 minutes is not thrown away. SIGSTOP cannot take back a read already asked of the share,
     though: the macOS kernel keeps several MB of a sequential read in flight (seconds of a 2.5 MB/s link), and the
     play's own first requests (a stat, an open, the Cues) queue behind them on the one SMB connection. Measured on the
     NAS (2026-11-09): `POST /playback` took 2.1 to 19 s while the pre-extract read (10 plays of 2 films; the first
     `os.Stat` of the file was 95 to 99.8 % of it, asked before `Create` could tell the governor); with the fix below,
     66 to 279 ms for a replay, and for a first play what the Cues read costs (27 to 1026 ms over 10 films, the job
     reading or not). So for Matroska/WebM, ply does the reading (`internal/subs/feed.go`): 256 KB at a time with the
     kernel's reading ahead off (`F_RDAHEAD`), handed to ffmpeg on its stdin (`-i pipe:0`). A suspended ffmpeg stops
     taking the pipe and the reading stops, with at most one chunk in flight (a run held from the start reads one: the
     feed waits for the run to have started and been suspended). Which files: a pipe cannot be sought, so an MP4 (its
     index may be at the end) and a Matroska file whose Info or Tracks come after the first Cluster (ffmpeg finds them
     by a seek: "Stream map matches no streams"; `headersFirst` walks the top-level headers to see) are opened by ffmpeg
     as before, and still queue a play behind them. If a run on a pipe fails all the same, it is started once more on
     the file before any failure is recorded (so a file read twice is the price of one the walk got wrong). A file
     that ends before the size it had when it was opened ("the file ended at X of Y bytes") is `ErrUnavailable` if it has
     a different size when looked at again (cut, replaced, still being copied): nothing is recorded and the job asks
     again later. If it is still that long it lies about its length, and reading it on every backoff for days would not
     change that: `ErrConvert`, recorded once. Neither is tried again with the first track alone or on the file itself. A play that is only being set up counts (`Manager.Create`
     reserves its place at once: the keyframe scan of a first play is minutes of reading, and the governor is told
     when the place is taken and when it is given up), and so does one that is paused, for its first two minutes.
     Time spent suspended does not count against the run's limit (3 hours of running time, up from 10 minutes: a
     7 GB remux at 2.5 MB/s takes 50); a run that uses it is a failure, not retried.
   - **The sweep is not the library.** Reading every film's container over the NAS is about 8.5 days of the link for
     263 films of 7 GB (at 2.5 MB/s), and most will never be played. The sweep queues the items somebody is part way
     through (the 20 last played), and the next episode of each show they watch (20 at most); a new file is queued
     when the scanner adds it, ahead of the rest, but only when a scan adds 20 or fewer: more is an import (a library
     just added, a disk copied in), which is not a sign of what will be played. Any other film is extracted on its
     first play (all its tracks, in one read).
   - **Failures.** A file that cannot be read (the share is not mounted yet, or asleep: `ErrUnavailable`) is never a
     failure: the job is retried with the queue's backoff (a minute, doubling to 30 minutes) up to 500 times, about ten
     days, and the sweep queues it again after that. An ffmpeg failure is recorded next to the cache
     (`<id>-<key>-<mtime>.fail`, pruned with the file), so that no start and no scan reads the film for an hour to find
     out again; a track with a record is not taken along in anyone's run, but a person's request for the track itself
     tries it anyway, and a success clears the record. A job cancelled in Activity counts as failed until ply
     restarts. One track that makes ffmpeg fail does not cost the others: a run of several tracks that fails is tried
     again with the first alone, and the tracks it gave up are free at once for a run of their own, if somebody asks
     for one before the first track's read ends.
   - A play that asks for a track that is not cached (`GET .../subtitles/{key}.vtt`) *joins* the run if there is one
     (and promotes it: released if suspended, `sysprio.Foreground`), or starts one for that item: not demoted, and
     extracting the item's other text tracks in the same read. When the last request that waits for a run leaves (the
     page was closed) the run is background work again: demoted, and paced like any other. The answer is chunked: `200` once the
     request has waited a second for the extraction to end, counted from the request, not from the start of a run it
     joined (before that, `422` for a failure, `503` for a file that cannot be read, or the finished file with its
     length), then each cue as ffmpeg writes it (`-flush_packets 1`, polled every
     100 ms, whole cues only, `Cache-Control: no-store`) until the file is complete. ffmpeg writes the blank line
     after a cue before the next one, so the last cue written is given out once the file has not grown for a poll. A
     browser parses a `<track>` as it arrives (verified in Chromium), so the web player needs no change; a failure after
     the first byte ends the answer at the last whole cue and the next request meets the remembered failure (`422`). An
     extraction stopped by someone else (a job cancelled in Activity, ply stopping) is a `503` with `Retry-After`, not
     an empty `200`. A `HEAD` of a track that is not cached does not start a run.
   - The cues of the first minutes arrive after about that share of the file has been read: a play from the start
     sees subtitles within seconds, a resume at 1:30:00 sees them when the read gets there. The pre-extraction job is
     what makes the second case fast.
   - Not covered: a sidecar-only item (an `.srt` next to the file) gets no job: it is a small file, converted on
     its first play.
   - **No orphans.** Every ffmpeg ply starts (playback, subtitles, artwork) goes through `internal/proc`: its own process
     group, `Pdeathsig` on Linux, a reaper per group on macOS (`ply --reap`), stopped by ply when the group has ended.
     A `kill -9` of ply leaves none, running or suspended (`TestKillMinus9LeavesNoSubtitleFFmpeg`, and the playback one).
5. **Hold the art frame grab** for the item being started; serve TMDB/sidecar art first.

   **Built (2026-11-05, reworked 2026-11-06):** `POST /playback` notes that the item is being set up (`api.playStarts`);
   the hold lasts while the session is created, however long, and 10 s after it is. A film that plays from the same disk
   holds every frame grab on that disk too (`Governor.Check`, the question the queue asks of `art.grab`; browsing alone
   does not; the question is about the item's library folder, so that a page of a hundred films does not make the
   governor look up the disk of each file), so the grid of a library page does not take the link from a film. In that time `GET /api/items/{id}/art/{kind}`
   serves the image if it is cached; otherwise it answers `503` with `Retry-After` (at least a second; the placeholder the
   page already shows for an image that is not there) and does *not* start ffmpeg. A play that was refused (the limit on
   plays) holds nothing. A grab that is already running for the item when the play begins is stopped
   (`art.Generator.Defer`; its waiters get the same `503`, nothing is remembered against the file; a grab that nobody
   waits for any more is not handed back and not queued). Whatever was put off
   is queued as `art.grab` (key `art:<item>:<kind>`, needs the CPU and the item's disk), so it runs when no play reads
   the disk; a grab that was stopped is queued again whatever was asked a moment ago, and repeated asks are passed on
   once in 30 s. The job looks at the file when it runs: one that was replaced since (another mtime than the one it
   was queued with) is left alone, the page asking again once the scanner has the new mtime. TMDB, sidecar and embedded art are chosen by the page from the item's `poster`/`backdrop`/`still` and
   need no frame grab; the frame grab is only the fallback.
6. ffmpeg input pre-roll: try `-analyzeduration 0 -probesize 1M` for mkv/mp4 inputs (measure first). hls.js
   `startFragPrefetch: true`.
7. **Pre-index in the queue:** a low-priority `keyframes.index` job per new remux-candidate item, held while a
   session reads that volume. With 1 it reads headers only, so the NAS backlog clears in minutes.

Acceptance: remux of a cold MKV from the NAS: `POST /playback` < 300 ms, first segment < 1.5 s, first frame < 2 s;
an MKV without Cues starts within 2 s via the EVENT playlist.

## Orphan-proof playback
Today: one `run` per session; ffmpeg in its own process group, killed as a group (`internal/stream/proc_unix.go`);
idle kill 60 s; DELETE with `keepalive` on `pagehide` (`web/src/lib/api.ts:606-608`, `Player.svelte:177,213`);
`streams.Close()` on shutdown; a startup sweep of `os.TempDir()/ply-sessions/<pid>/<sid>/ffmpeg.pid`
(`manager.go:201-251`).

Gaps:
- The sweep only finds dirs under the current `TMPDIR` (launchd and a Terminal have different ones).
- No Linux `Pdeathsig`.
- `Setpgid` means launchd's job kill does not reach ffmpeg.
- A SIGSTOPped ffmpeg survives a crash forever.
- Nothing lists what is playing.
- **A shorter idle would kill healthy players:** hls.js buffers 40 s (`Player.svelte:256`) and may not request for
  40+ s.
- iOS Safari often skips `pagehide`.

Design:
1. Session registry in `<data>/sessions/<sid>.json` (`pgid`, ffmpeg path, process start time, item, dir), written
   before Create returns and removed after Wait. The temp root moves to `<data>/tmp/sessions`. The startup sweep
   verifies pid and start time, SIGCONTs, then SIGKILLs the group, and removes the dir.
2. Linux: `Pdeathsig = SIGKILL`, with the fork on a `LockOSThread` goroutine; `KillMode=control-group` in the unit.
3. macOS: a `ply --reap <pgid> <ppid>` helper (same binary) that kqueue-watches ply (`EVFILT_PROC`/`NOTE_EXIT`) and
   kills the group when ply dies.
4. Heartbeat: `POST /api/sessions/{sid}/heartbeat` every 10 s while the tab lives (`{positionSec, paused}`; built).
   The idle rule: a session that has had a heartbeat is stopped 30 s after its last beat or
   segment request; one that never beat keeps `-stream-idle` (60 s), so a player without heartbeats (hls.js with its
   40 s buffer, AVPlayer) is safe. A segment request being waited for is not idle. Pausing no longer kills; DELETE stays an optimisation.

   **Built (steps 1 and 3 of the build order):** the registry, the startup sweep, the macOS reaper, `Pdeathsig`, the
   leak check, the caps and `GET /api/sessions` are in; see `docs/DESIGN.md` (Streaming) for the behaviour and
   `internal/stream/{play,registry,reap_darwin}.go` and `internal/proc` (the process group, the `Pdeathsig` fork, the
   reaper: shared with the subtitle and artwork extractions) for the code. Found while testing: the kernel ends a
   *stopped* orphaned process group with SIGHUP in some cases only, so a running ffmpeg outlives a killed ply (and a
   stopped one is not safe for sure); the reaper and the registry matter for those.

   **Stopping does not wait for the exit.** A process stuck in an SMB read does not die until the read returns (18 s
   measured), though SIGKILL is sent at once. `Session.stop` kills and returns a channel; a goroutine waits for the exit
   and only then removes the registry entry, ends the reaper and removes the directory, so the guards last until the
   real exit. The session counts in `Count` meanwhile, against the limits only after `StopGrace`. `Delete` waits at
   most `Options.StopWait` (500 ms); `Close` kills all sessions first, then waits once for all at most `CloseWait`
   (2 s, `proc.ExitGrace`, which the subtitle and artwork generators use too); the next start finishes the rest. A seek
   still waits for the old run (the GOP file names are shared between runs).
5. A leak check in the manager's housekeeping (every 5 s): the registry files vs the sessions; a registered group (pid
   alive, started when the file says) that belongs to no session is killed. Not built, from the review, for later: a
   maximum time for a paused session; a flag for the heartbeat window; the client address behind a reverse proxy
   (`X-Forwarded-For`); counting a GPU session that fell back to software as a software transcode.
6. Tests:
   - kill -9 a subprocess ply mid-transcode (plain, and SIGSTOPped), then start a new ply with a different TMPDIR:
     the group is dead within 2 s;
   - Linux Pdeathsig; darwin reaper;
   - a heartbeat holds a paused session;
   - pid reuse is not killed.

## Maximum parallel playback
- `-max-streams N` (env `PLY_MAX_STREAMS`, default 4 = today's `MaxSessions`, `manager.go:41,72`) and
  `-max-transcodes N` (default 1 with a software encoder, 3 with VideoToolbox). Settings → Playback, precedence
  flag > settings > default, applied live (`Manager.SetLimits`).
- Direct plays count too: tracked as `(item, client IP, User-Agent)`, refreshed by `/file` range requests, expired
  30 s after the last.
- At the cap: 503 + `Retry-After: 5` + `{"error": "4 of 4 plays in use", "playing": [...]}`. The player shows the
  list with a Stop per entry. No automatic pre-emption, except optionally of idle, paused sessions.
- `stopping` sessions must not count (`manager.go:329`), or `Create` waits up to `WaitDelay` for them: the player's
  own stop-then-start would otherwise 503 at the cap.

## Target architecture

### Store: read pool + single writer
- Two `*sql.DB` on the same file (done, `internal/store`): `ro` (`SetMaxOpenConns(min(NumCPU, 4))`, at least 2, all
  kept idle) and `rw` (1, `_txlock=immediate`). `ro` is `query_only`, not `mode=ro`: `mode=ro` measured twice as slow
  on a WAL database (3,000-item `ListItems`: 55 ms against 30 ms, and 4 readers at once 101 ms against 38 ms).
  Both have `synchronous=NORMAL`. With NORMAL in WAL mode a commit does not wait for the disk: a crash of ply loses
  nothing, but a power cut rolls back every commit since the last checkpoint, which SQLite makes only at 1,000 WAL
  pages (days, for a person's writes), and that would take the TMDB key, folders and manual matches with it. So a
  `PRAGMA wal_checkpoint(PASSIVE)` follows an interactive write within 2 s (the first one starts the timer; later
  ones do not move it) and any write once the writer has been idle for 30 s.
- Write batches capped at ~500 rows per tx (`DeleteItems`, `ArchiveAndDeleteRoot`, `SetParsed`, `PruneMetadata`
  (50 titles per round: each cascades), `ReleasePending`, `RequeueUnmatched`; `removeMissing` calls `DeleteItems`).
  A bulk write is no longer all-or-nothing: after an error the earlier batches stay, and calling again continues.
- Metadata commits per title.
- A small writer-priority lock: tier 1/2 writes (progress, settings, manual match, folder add) go first; tier-3
  writers yield between chunks.
- Keep modernc.org/sqlite; bench ncruces/go-sqlite3 later; mattn is cgo (rejected).

### Tiers (`internal/tier`): how the queue yields
- A `Governor` fed by `stream.Manager` (active sessions, demanded segments outstanding, the volume each reads,
  whether a software transcode runs) and an EWMA of `/api` read traffic.
- Weighted semaphores (`golang.org/x/sync/semaphore`) per resource class: `cpu`, `disk:<device>` (one budget per
  volume), `net:<service>`.
- A software transcode running → `cpu` capacity 1. Any session → `disk:<its volume>` capacity 0 for whole-file jobs.
  High browse traffic → the dispatcher sleeps between jobs. A running fallback scan on the volume of a demanded
  segment gets SIGSTOP, then SIGCONT once the segment is served.
- Playback is never niced. libx264 capped at `-threads max(1, NumCPU-2)`.
- Index, subtitle and art work for the item being played is tier 1; the same work for other items is tier 3.

### Playback tier's own priority
- Order: demanded segments of every session > init/playlist > prefetch.
- With ≥ 2 software transcodes: `ThrottleHigh` 20 → 8, and SIGSTOP the session furthest ahead while another has
  waited > 500 ms for a demanded segment.
- Publish `tier`.

### Unified persistent queue (`internal/jobs`)
- Hand-rolled on SQLite. River is Postgres-only; goqite is FIFO without priorities or dedupe; backlite cannot yield
  to playback or do resource classes.
- Table `jobs(id, kind, key, priority, state, payload, progress, attempts, run_at, lease_until, created_at,
  started_at, finished_at, error)`, with a partial unique index on `key` while queued or running. `job_events` keeps
  7 days of history.
- Kinds: `scan.full`, `scan.dirs` (coalesced), `scan.root.add`/`drop`, `fingerprint.backfill`, `meta.match.item`,
  `meta.match.series`, `meta.refresh.due`, `meta.refresh.title`, `meta.seasons`, `image.prefetch`,
  `keyframes.index`, `subs.preextract`, `art.grab`, `prune.caches`, `nfo.write`, later `thumbs.trickplay`, `tvdb.*`.
- Priorities: user-requested 0 > `scan.dirs` 10 > match new 20 > `scan.full` 30 > refresh due 40 > prefetch, index,
  thumbs 50. Each kind declares its resource class and weight.
- Dedupe by key: enqueueing onto a queued job merges; onto a running one sets `rerun`.
- Retries: backoff from `metadata.Backoff`. One `Limiter` per provider (TMDB, TVDB, image CDN), extracted from
  `tmdb.go:234-316`. A 429 pauses the service, not the queue.
- Cancel via `context.WithCancelCause`. Pause/resume through a setting. Bump = priority 0.
- Persistence is hybrid. Persist only what can't be re-derived: user actions, the pause flag, history. At startup,
  expired leases go back to queued, and `sweep.*` jobs re-enqueue derived work from the library state (match state,
  missing keyframes, files newer than their row).
- The full scan stays one job (its invariants need the whole walk), but `indexAll` takes a `cpu` permit per file.
  In-request image misses enqueue at priority 0 and return 404 + `Retry-After: 2`.

### Live activity feed: SSE, stdlib only
- `GET /api/events` (`text/event-stream`, 15 s heartbeat, replay on `Last-Event-ID` from the queue's in-memory ring of
  512 events, not from `job_events`; the token travels as `?token=`).
- Events (`jobs.Event`, `EVENT_TYPES` in `web/src/lib/activity.svelte.ts`): `snapshot`,
  `job.queued|updated|held|started|progress|retrying|done|failed|cancelled`, `queue.paused|resumed`, `tier`.
  `session.started|ended|idle` were planned but are not published yet (see "Now playing across devices" below).
- `library` (2026-11-11, `Queue.LibraryChanged`): what the library shows changed. Sent after a scan, full or
  partial (the watcher's), that indexed, moved, removed or re-read a file (`Scanner.OnChanged`), after folders were
  read again for a new type, and after a metadata pass that matched or fetched something (`WorkerOptions.OnChanged`);
  never for a scan or pass that found nothing. Every open page follows the stream (the app shell opens it), so a
  film that lands on the NAS shows up in every browser without a reload: the page bumps `app.libraryVersion` once a
  burst has been quiet for 1 s (`LIBRARY_SETTLE_MS`), and what it shows is fetched again. A hidden tab has no
  stream: it fetches again when it is seen (a list that did not change answers 304).
- Header icon: hidden when idle, a spinner with a count while running, amber when paused or on a recent failure.
  Click → `#/activity`.
- Activity page: Running (progress, elapsed, cancel), Queued (grouped, bump/cancel), Playing now (with stop), History
  (last 200, filter failed, retry), a pause toggle, and a tier strip ("queue held: transcode running").
- Endpoints: `GET /api/jobs`, `POST /api/jobs/{id}/cancel|retry|bump`, `POST /api/queue/pause|resume`,
  `GET /api/sessions`. `/api/scan` and `/api/metadata/status` stay as thin views.

### Now playing across devices, and why not WebSocket (2026-11-12)
The aim is a "now playing" that is always right, including a cast to Apple TV or Chromecast; Plex's is not.
- **Why it goes wrong elsewhere:** the client is trusted to say "stopped". A cast plays on a device that is not
  the one that started it, and a client that sleeps or is killed never says it. A socket does not help: a
  sleeping device does not close its TCP connection either.
- **What ply has:** the server owns the truth. A play is a lease (`internal/api/plays.go`, `internal/stream`):
  a 10 s heartbeat with position and paused, and expiry 30 s after the last heartbeat or request (3 min while
  paused). An HLS session stopped on purpose answers 410, an expired one 404; an expired direct play is simply
  admitted again.
- **What is missing, in order:**
  1. Move the event hub out of `internal/jobs` into its own package. Sessions become its third publisher, after
     jobs and the library.
  2. Publish `session.started|updated|ended` from `stream.Manager`, with position updates at the heartbeat's
     pace.
  3. A stable device id and name, made by the client and sent with the session and its heartbeats.
  4. A heartbeat for direct plays, which today are known only by item, IP and User-Agent (`directKey`).
  5. Tests on `testing/synctest`: stop the heartbeats and see `session.ended` after 30 s, having first seen
     `session.started`.
- **Devices:** an AirPlay'd Apple TV fetches the HLS itself, so its segment requests keep the lease alive. A
  Chromecast custom Web Receiver is a page served from a registered HTTPS URL, not by ply: whether it may call
  `http://` ply on the LAN must be tried before anything is designed on it, and plain POSTs are the likeliest to
  pass. If a phone sleeps while the Apple TV is paused, no requests come, so the paused lease (3 min) applies
  only if the last heartbeat said paused.
- **WebSocket: not now.** It was weighed, and these four points decided it:
  - the feed is one-way notifications;
  - client actions are REST calls whose status codes mean something (409, 404 vs 410);
  - progress needs `fetch(keepalive)` when the page goes away, which a socket cannot do;
  - its one real gain, freeing one of the browser's 6 HTTP/1.1 connections per host (plain HTTP, so no
    HTTP/2), is small: a hidden tab already drops its stream.

  WebTransport needs HTTPS and HTTP/3, so it is out. What would change this is remote control (stop, seek or
  "play this" sent to one player) or a native tvOS app. Then the shape would be:
  - `github.com/coder/websocket`, not a hand-rolled RFC 6455;
  - `GET /api/devices/{id}/socket` for player devices only, beside SSE, which the dashboards keep;
  - JSON `{seq, type, data}` on the hub's sequence, with replay by `?after=` and commands acked by id;
  - `?token=` auth;
  - a ping every 10 s with a 20 s deadline, and client backoff from 0.5 to 10 s with jitter;
  - the POST heartbeat and the lease stay the fallback and the truth.

### As built (2026-11-05)
- `internal/tier`:
  - `Governor.Check(Need)` and `Wait(ctx, Need, held)`:
    - a stalled viewer (`Waiting>0`) holds everything;
    - `Need.CPU` is held while a software transcode runs;
    - `Need.Disks` is held while a play reads from the same disk;
    - browsing holds everything for 300 ms after the last browse request (`Track(isBrowse)`, which counts no polling,
      feed or playback request). Browsing alone holds a piece of work for at most `BrowseMaxHold` (2 s): a client
      that polls the library without pause must not starve the queue. Playback holds have no limit.
  - Disks are compared by key. On macOS a network share's key is its server (`statfs` `f_mntfromname`), since two
    shares of one NAS share the link; anything else is keyed by its filesystem id (`st_dev` on Linux).
    - Only exact answers are cached. A path that is not there yet (a share not mounted when ply starts) counts as on
      its parent's disk for now, and is looked at again after 10 s.
  - Polls every 250 ms, and at once on `stream.Manager.OnLoadChange` (`Changed()`).
  - This replaces the weighted semaphores for now: the queue's lanes (below) bound the concurrency.
- `internal/jobs`, the queue:
  - Lanes: one heavy job at a time (any Need) beside two light ones.
  - **Starting:** the dispatcher asks `Gate.Check` before a job gets a slot. A held job does not take its lane from a
    lower-priority one that may run: a full scan held by a film on the NAS does not keep the watcher's small scan
    waiting. The next held job of each lane is shown with its reason (`Job.Held`, listed with the running ones).
  - **Between steps** a running job calls `Run.Yield`, against `Spec.YieldNeed` (default `Need`). `scan.full` yields
    to CPU and stalls only: once it holds the scanner, waiting out a whole film would block every other scan too.
  - Order: priority, then age.
  - Dedupe: one queued job per kind+key, merged (`Spec.Merge`), with the merge check and the insert as one step;
    a job never runs beside one with its key. A job the last run left queued and one this run queued with the same
    key are one job.
  - Retries: a failure retries with backoff (1 min doubling, at most 30 min, 5 attempts); `Permanent` fails at once.
  - A panic is a failure.
  - A person's actions: cancel (`context.WithCancelCause`), bump (priority 0), retry, and pause/resume (persisted).
  - `Do` returns the job's own error (`errors.Is` works); `RunFrom(ctx)` gives code deep in a job its handle.
  - Persisted in `jobs` (migration 007): state changes only, not progress. At startup a running job goes back to
    queued, and a kind nobody registers fails. History is kept 7 days, the last 200 shown.
  - An in-memory ring of 512 events serves `Last-Event-ID` replays. Ids start from the clock, so an id from an
    earlier process gets a snapshot, not a replay.
- Kinds in use:
  - `scan.full` (30): every interval, and at once. "Scan now" queues it at 0, or moves a queued one to the front. It
    needs CPU and every root: it does not start while a video plays from the library's disk.
  - `scan.dirs` (10): the watcher's batches, through `Queue.Do`. It needs CPU only: a few headers, so a play from the
    same disk does not hold it. A batch that never ran is reported to the watcher as `ErrNotScanned`, which it retries.
  - `meta.match` (20): the worker's passes, woken by `OnWake` (asynchronously), and every minute.
    - Each item may probe its file and read its sidecars, even with lookups off. On a NAS that shares the link
      with a film (measured 2026-11-05), so it needs the roots' disks: it does not start while a video plays from
      them.
    - Before each item (`WorkerOptions.Pace`) it yields to that, to a stalled viewer, a CPU transcode and browsing.
    - `Spec.Light` keeps it in the light lane, so it never waits behind a full scan.
    - Outside a job (`paceFor`) the same pace waits on the governor directly.
  - `cache.prune` (50): after each scan.
  - `subs.preextract` (50, key `subs:<itemID>`, light lane; `Need` and `YieldNeed` are the item's disk): writes an
    item's text subtitles to the cache. It does not start while a play reads from the disk (a play being set up counts),
    yields between its reads, and the run is suspended if a play starts. One job in the queue at a time
    (`subsFeeder`: a job reads a whole film and two would hold up the lookups of new files), fed by the scanner's
    `OnAdded` (new files first) and by a sweep of what is being watched (see item 4 above). It retries a file it cannot
    reach (500 attempts with the queue's backoff: about ten days; the default is 5), and fails only for what ffmpeg
    cannot do. The job says "Waiting: a video is playing from the same disk" while suspended, and stops saying it
    when a viewer starts to wait for the run.
    Progress: the container's ffmpeg is run with `-progress pipe:1` (`internal/subs/progress.go`; a sidecar's is not),
    and `Generator.FillProgress` tells the job how far into the film its last subtitle packet is (`out_time_us`, about
    twice a second; a Fill that joins a run a play started is told too). `subsMeter` (`cmd/ply/subs_meter.go`) turns
    that into the job's bar (percent, 0-99 while reading) and a note, "Reading the file: 43%, about 12 min left": the
    rest of the film at the speed of the last minute of running time, smoothed, with the time ffmpeg is suspended left
    out; the bar stays where it was under "Waiting: ...". At most one update every 2 s. A film whose length is not
    known gets the plain "Reading the file" and an unknown bar. A subtitle track is sparse, so the position moves in
    steps and the time left is rough.
  - The light lane holds 3 jobs (`Options.Light`, up from 2), so a subtitle extraction or a picture that takes minutes
    leaves two for the lookups and the ratings.
  - `art.grab` (50, key `art:<itemID>:<kind>`, light lane): the frame grab that `GET .../art/...` put off because a
    play of the item was starting. Needs the CPU and the item's disk.
  - The scanner's own passes (a folder just added, a type changed) appear as a read-only `scan.folders` entry
    (`Options.Extra`, from `Scanner.Busy`). They pace through the governor directly.
- The scanner yields per file: `Scanner.SetPace` is called before each probe (with done/total, which becomes the
  job's progress) and every 100 directories of a walk.
- Verified live (a remux session plus "Scan now"): the scan waits with "a video is playing from the same disk", and
  runs within a second of the session stopping.

## macOS-first optimisations (in order of expected effect)
1. Plist `ProcessType` → Interactive/Standard; demote children, not the server.
2. Darwin BG for background children:
   - `setpriority(PRIO_DARWIN_PROCESS=4, pid, PRIO_DARWIN_BG=0x1000)` right after `cmd.Start` (what `taskpolicy -b`
     does): low CPU, throttled I/O, efficiency cores. Value 0 promotes a job back.
   - Verify that it also slows smbfs reads.
3. VideoToolbox: `hevc_videotoolbox` (`hvc1`) for Apple clients, so 4K HDR keeps 10-bit; `aac_at` instead of
   `aac`; `-allow_sw 0` so the libx264 fallback takes over cleanly. Verify option names on ffmpeg 9.0.2.
4. `F_NOCACHE` on whole-file background readers ply opens itself. `F_RDADVISE` for the index reader's ranges and
   ahead of direct play (verify the struct in `x/sys/unix`).
5. kqueue `EVFILT_VNODE` on the session dir instead of 25 ms `os.Stat` polls (`session.go:426,553`).
   `EVFILT_PROC/NOTE_EXIT` for the reaper.
6. Linux: `ioprio` IDLE, `SCHED_IDLE`, `Pdeathsig`, `KillMode=control-group`; no cgroup work until there is a box.

## Dependencies (each must earn its place)
- **Add** `golang.org/x/sync` (weighted semaphores).
- **Promote** `golang.org/x/sys/unix` to a direct dependency (Kevent, ioprio, fcntl).
- **One MP4 parser,** `github.com/Eyevinn/mp4ff` or `abema/go-mp4`, after checking its current API; or hand-roll the
  few boxes needed.
- **Hand-roll** the MKV Cues reader (~300 lines of EBML).
- **Stdlib** for SSE, the queue, the registry and the reaper.
- **Skip** `x/time/rate` (reuse the existing bucket), WebSocket libraries (until remote control, see "Now playing
  across devices"), River, goqite and backlite.
- **Refreshed 2026-11-12** (all at their latest):
  - Go: toolchain go1.27.4 (pinned in `go.mod`), fsnotify 1.10.1, x/sys 0.50.0, x/text 0.44.0,
    modernc.org/sqlite 1.62.0. fsnotify 1.10 closes its kqueue watches on Close (#740), so ply's own workaround
    is gone; `TestFSRunReturnsAndLeavesNothingBehind` fails on 1.9.0 without it.
  - Web: vite 8.3.8, vitest 5.1.0, jsdom 31, playwright-core 1.65, svelte-check 4.7.8.
  - TypeScript 7 (`@typescript/native`, aliased) does the checking through `svelte-check --tsgo`. TypeScript 6
    stays installed because svelte2tsx needs its JS API; `npm run check:ts6` is the old check, for comparison.
    The alias owns the `tsc` link: `npx tsc` is TypeScript 7, and `tsserver` (editors) is still 6.
  - Kept: modernc.org/sqlite (pure Go; ncruces/go-sqlite3 runs SQLite as wasm), mattn/go-isatty.
  - grandcat/zeroconf (no release since 2020) stays until it is replaced by an advertise-only responder on
    `x/net/dns/dnsmessage` or by `brutella/dnssd`. Not hashicorp/mdns: it has no probing, conflict handling or
    goodbye packets.

## What to keep
- The HLS fragment design (`docs/NOTES.md` M3/M4).
- "Newest request owns ffmpeg", the SIGSTOP throttle, process groups, and the stale-dir sweep.
- The scanner's walk/pair/probe/remove order, fingerprint move pairing, the settle rule, unmounted-root safety and
  mount boundaries.
- The watcher's debouncing.
- The TMDB limiter and backoff.
- The keyframe cache in SQLite.
- The middleware: no writer wrap, the host guard, same-origin writes.
- Pure Go, `CGO_ENABLED=0`.

## Build order
1. Plist/service fix, Darwin BG for children, `-max-streams`/`-max-transcodes` with the teardown fix.
2. Container-index keyframes with the gaps above closed (`tfdt` mapping, `ctts/elst`, subtitle pre-extraction and
   progressive VTT, art hold, EVENT fallback), plus the lavfi fixture matrix.
3. Heartbeat idle, registry in the data dir, reaper, the kill -9 test; `GET /api/sessions` with direct plays.
4. Store pools and batching.
5. Tiers, the queue core, SSE and the Activity UI. VideoToolbox HEVC/`aac_at` and kqueue fragment readiness slot in
   as self-contained tasks.
6. Playback fairness and the remaining fallbacks.
7. Optional: bench the ncruces driver.

M7 (folders) finishes first as specced, with chunked deletes. Its `SetRoots`/`DropRoot` become jobs in step 5 with a
two-line wrapper. M8 (TVDB) writes its client against the `Limiter` interface.

Every step has a latency harness. ~3k lavfi files, the fake TMDB, and a full scan plus refresh running while a client
fetches segments and `/api/items` for 60 s. It records p50/p99 of segment latency, `/api/items`, `/api/items/{id}`
and `POST /playback`. Acceptance once tiers land: segment p99 within 20 % of the idle baseline with a transcode and a
scan running; `/api/items` p99 < 50 ms; `POST /playback` (warm) p99 < 150 ms. Results go in `docs/NOTES.md`.

## Open questions (decided by default unless the owner says otherwise)
- Pre-emption at the cap: off. Only refuse, with the list and a Stop button.
- Direct plays count toward the cap: yes.
- Browse may get "image not ready" (404 + retry) instead of blocking on TMDB: yes.
- Failed jobs: capped backoff, 5 attempts, then wait for a click. History: 7 days.
- Fairness tests target 2 simultaneous viewers.
