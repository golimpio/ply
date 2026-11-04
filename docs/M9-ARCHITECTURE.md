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
5. **Hold the art frame grab** for the item being started; serve TMDB/sidecar art first.
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
4. Heartbeat: `POST /api/sessions/{sid}/keepalive` every 20 s while the tab lives (`{paused, position}`). The idle
   rule becomes "no segment request and no heartbeat for 45-60 s". Pausing no longer kills; DELETE stays an
   optimisation.
5. A leak check in the manager's housekeeping tick: our pgids vs the running ffmpegs under our temp root.
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
- Two `*sql.DB` on the same file: `ro` (`SetMaxOpenConns(NumCPU)`) and `rw` (1). Add `synchronous=NORMAL`.
- Write batches capped at ~500 rows per tx (`DeleteItems`, `ArchiveAndDeleteRoot`, `SetParsed`, `removeMissing`).
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
- Publish `tier.load`.

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
- `GET /api/events` (`text/event-stream`, 15 s heartbeat, replay from `job_events` on `Last-Event-ID`; the token
  travels as `?token=`).
- Events: `snapshot`, `job.queued|started|progress|finished|failed|cancelled`, `queue.paused|resumed`,
  `session.started|ended|idle`, `tier.load`.
- Header icon: hidden when idle, a spinner with a count while running, amber when paused or on a recent failure.
  Click → `#/activity`.
- Activity page: Running (progress, elapsed, cancel), Queued (grouped, bump/cancel), Playing now (with stop), History
  (last 200, filter failed, retry), a pause toggle, and a tier strip ("queue held: transcode running").
- Endpoints: `GET /api/jobs`, `POST /api/jobs/{id}/cancel|retry|bump`, `POST /api/queue/pause|resume`,
  `GET /api/sessions`. `/api/scan` and `/api/metadata/status` stay as thin views.

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
- **Skip** `x/time/rate` (reuse the existing bucket), WebSocket libraries, River, goqite and backlite.

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
