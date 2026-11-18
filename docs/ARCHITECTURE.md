# ply — system architecture

The full, current architecture of ply: what the pieces are, how they fit, where the data lives and
how a request or a background job flows through them. This is the **top-level map**. It deliberately
does not repeat the detailed specs — it links them. For the HTTP API and config in full see
[`DESIGN.md`](DESIGN.md); for the performance/queue model see [`M9-ARCHITECTURE.md`](M9-ARCHITECTURE.md);
for each feature see its milestone doc (§12).

> Status: living overview. If a package's job changes, update the component map here and the detail in
> its spec. The package layout in `DESIGN.md` is the phase-1 plan; **this map reflects the tree as built.**

---

## 1. What ply is

A **local-only, Plex-like media server**. One Go binary (`CGO_ENABLED=0`, darwin/linux × amd64/arm64)
with an **embedded Svelte web UI**, plus native **iOS/tvOS** clients that consume the same HTTP API.
It indexes a person's video folders, fetches metadata and artwork, and plays files to a browser,
AirPlay or Google Cast receiver — direct play when possible, remux or transcode when not.

Design stance (hard requirements, [`DESIGN.md`](DESIGN.md) "Efficiency rules"): **playback first,
browsing second, everything else yields**; stdlib first; small packages; no cgo; the NAS link and the
CPU are shared resources that background work must never steal from a viewer.

---

## 2. Bird's-eye view

```
        web SPA (Svelte, embedded)        iOS / tvOS app (AVPlayer, AirPlay, Cast)
                  │                                   │
                  └──────────────┬────────────────────┘
                                 ▼
                      internal/api  (HTTP mux, auth, CORS,
                       │           host guard, SPA, SSE, gzip/ETag)
        ┌──────────────┼───────────────────────────────────────────────┐
        ▼              ▼                          ▼                     ▼
  internal/store   internal/playback          internal/metadata     internal/events
  SQLite (WAL,     Decide(): direct /         match + refresh       live feed (SSE)
  read pool +      remux / transcode          TMDB/TVDB/NFO/local        │
  single writer)        │                    titles/seasons/eps         │
        ▲               ▼                    people/credits/art         │
        │        internal/stream                  │                     │
        │        HLS fMP4 sessions ── ffmpeg ◄── internal/art (frame grabs)
        │               │            ▲            internal/subs (WebVTT)
        │               │            │            internal/trailers + trailerdl
        │        internal/hwaccel    │
        │        (encoder choice)    │
        │               │            │
        └──── internal/library (scanner + nameparse) ◄── internal/watch (fsnotify)
                        │            │
                  internal/probe (ffprobe)
                        │
                  the media folders (filesystem / NAS)

  Background work runs through internal/jobs (persistent queue), gated by internal/tier
  (the governor) and internal/schedule; child processes via internal/proc + internal/sysprio.
  internal/discovery advertises mDNS and browses AirPlay; internal/tlscert serves HTTPS.
```

---

## 3. Component map (as built)

Top level: `cmd/ply` (the binary: flags, wiring, graceful shutdown), `web/` (Svelte 5 + Vite SPA,
`//go:embed all:dist`), `ios/` (iOS/tvOS apps + core, XcodeGen `project.yml`), `deploy/` (launchd plist +
systemd unit), `scripts/`, `docs/`.

`internal/` packages (23):

| Package | Responsibility | Detail |
|---|---|---|
| `config` | Flags, data-dir resolution, server id | [`DESIGN.md`](DESIGN.md) "Config" |
| `store` | SQLite persistence (`modernc.org/sqlite`, no cgo); read pool + single writer; migrations | [`DESIGN.md`](DESIGN.md) "Store", [`M9`](M9-ARCHITECTURE.md) |
| `probe` | ffprobe wrapper → normalised `probe.Result` (streams, tags, duration) | [`DESIGN.md`](DESIGN.md) "Probe" |
| `library` | Scans media roots into the store; parses file names (nameparse) | [`DESIGN.md`](DESIGN.md) "Scanner", [`M7`](M7-FOLDERS.md) |
| `watch` | Keeps the library in step with the filesystem between scans (fsnotify; macOS helper process) | [`M6`](M6-METADATA.md) "Watching" |
| `metadata` | Finds/scores/stores descriptions, artwork, credits; TMDB/TVDB/NFO/local; the worker; the image cache | [`M6`](M6-METADATA.md), [`M8`](M8-TVDB.md) |
| `art` | Generates + caches frame-grab artwork (ffmpeg) and dominant colour | [`M6`](M6-METADATA.md), [`M9`](M9-ARCHITECTURE.md) |
| `playback` | Decides delivery: direct play / remux / transcode; profiles | [`DESIGN.md`](DESIGN.md) "Playback decision" |
| `stream` | Turns files into HLS (fMP4) sessions by driving ffmpeg; session manager | [`DESIGN.md`](DESIGN.md) "Streaming", [`M9`](M9-ARCHITECTURE.md) |
| `hwaccel` | Chooses the H.264/HEVC encoder (VideoToolbox etc.) | [`M9`](M9-ARCHITECTURE.md) |
| `subs` | Converts text subtitles to WebVTT, caches on disk; pre-extraction | [`M9`](M9-ARCHITECTURE.md) |
| `trailers` | Downloads a title's trailer into the library when none on disk | [`M13`](M13-TRAILERS.md) |
| `trailerdl` | Native YouTube/Vimeo trailer download (no yt-dlp) | [`M13`](M13-TRAILERS.md) |
| `api` | HTTP API + embedded web UI; auth, CORS, host guard, SPA, SSE, gzip/ETag | [`DESIGN.md`](DESIGN.md) "HTTP API" |
| `events` | The live feed (what changed) for `GET /api/events` (SSE) | [`M9`](M9-ARCHITECTURE.md) |
| `jobs` | The unified persistent background queue (scans, lookups, cache, …) | [`M9`](M9-ARCHITECTURE.md) |
| `tier` | The governor: when background work may run (three tiers) | [`M9`](M9-ARCHITECTURE.md) |
| `schedule` | Keeps background tasks' schedules; queues their jobs when due | [`M12`](M12-SCHEDULES.md) |
| `proc` | Starts the child programs ply runs (ffmpeg); process groups, reaper | [`M9`](M9-ARCHITECTURE.md) |
| `sysprio` | Lowers the OS priority of background child processes | [`M9`](M9-ARCHITECTURE.md) |
| `refctx` | Ref-counted context: work shared by several callers, cancelled when the last leaves | [`M6`](M6-METADATA.md) "Images" |
| `discovery` | mDNS/DNS-SD advertise (`_ply._tcp`) + AirPlay receiver browse | [`DESIGN.md`](DESIGN.md) "Discovery", [`AIRPLAY`](AIRPLAY.md) |
| `tlscert` | Serves the HTTPS listener's certificate (`-tls-cert/-tls-key`) | [`M11`](M11-CLIENTS.md) |

(`metadata/metadatatest` and `tlscert/tlstest` are test helpers.)

---

## 4. Runtime priority model (the heart of the architecture)

ply serves **three tiers**, in strict order ([`M9`](M9-ARCHITECTURE.md), `internal/tier`):

1. **Playback** — a viewer's stream. Never blocked by background work; a play being set up counts.
2. **Browsing** — a person navigating the library (list payloads, image misses). Responsive, but yields
   to playback.
3. **Background** — scans, metadata lookups, ratings, cache maintenance, trailer downloads. Runs only
   when the tiers above allow it.

Mechanisms:

- **The governor (`tier`)** answers, per resource class (**CPU** and **the item's disk**), whether a
  background job may start or must yield. Disks are compared by key (on macOS a NAS share's key is its
  server, so two shares of one NAS share the link). A play from a disk holds frame grabs and subtitle
  reads on that disk; browsing alone does not.
- **The queue (`jobs`)** is a hand-rolled persistent SQLite queue: kinds, keys, priorities, dedupe by
  key, retries with backoff, cancel/pause/resume, a 7-day history, and an in-memory event ring for SSE
  replays. Priorities: user-requested 0 > `scan.dirs` 10 > match-new 20 > `scan.full` 30 > refresh 40 >
  prefetch/index/prune/thumbs 50. A **light lane** (3 jobs) keeps long reads (a subtitle extraction, a
  frame grab) from blocking lookups.
- **`proc` + `sysprio`** run ffmpeg/ffprobe in their own process groups at lowered OS priority
  (`PRIO_DARWIN_BG` on macOS), so a software transcode or a whole-file read cannot starve a viewer; a
  play can suspend (SIGSTOP/SIGCONT) or stop a background read.
- **`refctx`** makes a download/extraction shared by every waiter live while any waits, and cancel when
  the last leaves (a fast scroll aborts what it no longer shows).

Kinds in use (see [`M9`](M9-ARCHITECTURE.md) "Kinds in use" for the full behaviour): `scan.full`,
`scan.dirs`, `meta.match`, `cache.prune`, `subs.preextract`, `art.grab`, `keyframes.index`,
`image.prefetch`, ratings, `trailer.download`, and the schedule-driven sweeps.

---

## 5. Data & storage

**Store (`internal/store`).** SQLite via `modernc.org/sqlite` (no cgo). A **read pool** (several
`query_only` connections) + a **single writer** with priority lanes; transactions begin immediate; WAL
with periodic `wal_checkpoint(PASSIVE)`. Bulk writes are batched (~500 rows/tx) and resumable. Schema
evolves by embedded migrations **001–021** (`internal/store/migrations/`, applied in order, recorded in
`schema_version`). Core tables: `items`, `streams`, `progress`, `keyframes`, `titles`, `seasons`,
`episodes`, `people`, `credits`, `genres`, `collections`/`title_collections`, `title_ratings`/
`episode_ratings`, `series_matches`, `person_filmography`, `jobs`/`job_events`, `schedules`, `settings`,
`devices`/`device_tokens`.

**Data directory** (`-data`; macOS `~/Library/Application Support/ply`, Linux `$XDG_DATA_HOME/ply` or
`~/.local/share/ply`; created 0700, DB 0600):

```
<data>/
├── ply.db (+ -wal, -shm)   # the library, progress, metadata, queue, settings, keyframes
├── server-id               # random persistent server id
├── control.json            # {pid,url,key,…} for `ply -stop` (0600; removed on exit)
├── images/                 # the provider image cache (lazy, pruned)
│   ├── tmdb/<size>/<file>
│   ├── tvdb/<size>/<path>
│   └── embedded/<itemId>.jpg   # cover extracted from a media file
├── art/                    # frame-grab artwork: <id>-<kind>-<mtime>.jpg + .color2
└── (subtitle cache)        # extracted WebVTT tracks (internal/subs)
```

The DB holds the TMDB key (in `settings`); the data dir is the trust boundary — whoever can read it owns
the server (`control.json`'s key is the stop credential).

---

## 6. Library pipeline (files → items)

`library` walks each root, `probe` reads each file with ffprobe, nameparse derives title/year/show/
season/episode, and the store upserts `items` (+ `streams`). The **watcher** (`watch`, fsnotify) debounces
per directory and runs **partial scans** within seconds of a change; on macOS the watches live in a
**helper process** so a quitting ply never blocks the terminal on a NAS. A file is indexed only once its
size+mtime are stable (copies in progress are skipped). **Moves/renames keep identity** (size+mtime, then
a size-independent `fingerprint`), so progress, match and edits survive. Deletes are conservative (a
vanished root never deletes). Full scans run on a schedule ([`M12`](M12-SCHEDULES.md)); see
[`M6`](M6-METADATA.md) "Watching" and [`M7`](M7-FOLDERS.md).

---

## 7. Metadata & artwork

**The worker (`metadata`)** matches items to titles and refreshes them, paced by the governor. Sources,
in precedence: **local `.nfo` + sidecar artwork** (no network) → **TMDB** (and **TVDB** for shows,
[`M8`](M8-TVDB.md)) → filename parse + **frame-grab art** (`art`). Identity hints: NFO/URL ids, filename
ids (`[tmdbid-603]`, `{imdb-tt…}`, `tt\d{7,8}`), then title+year scoring. It fills `titles`, `seasons`,
`episodes`, `people`, `credits`, `genres`, `collections`; ratings come from IMDb/MDBList
([`M10`](M10-RATINGS.md)); trailers from TMDB/TVDB then downloaded ([`M13`](M13-TRAILERS.md)).

**Artwork contract** ([`M6`](M6-METADATA.md) "Images"; the model this repo now guarantees):

- **Custom is the source of truth.** A sidecar next to the media (`poster.jpg`, `<base>-poster.jpg`,
  `seasonNN-poster.jpg`, `<base>-thumb.jpg`, …; `internal/metadata/localart.go`) is served **over** the
  provider image (`pickArt`). Local beats TMDB; an embedded cover ranks below TMDB.
- **Artwork is pinned (first-wins).** Once a poster/backdrop/logo, season poster or episode still is set,
  a rescan or refresh keeps it; the provider fills a slot only while empty. A different title is a
  different row; a manual reset or leaving TVDB takes provider art fresh. This is what lets a custom —
  or **migrated-from-Plex** ([`PLEX-MIGRATION.md`](PLEX-MIGRATION.md)) — poster stay put.
- **One current image per slot, ready to display.** The cache keeps the sizes clients ask for (TMDB/TVDB
  renditions; the server does no image transcoding). `cache.prune` drops provider images whose ref no
  title/season/episode/person/collection/filmography uses (`Store.LiveImageRefs` → `ImageCache.Prune`)
  and embedded covers of gone items, skipping in-flight `.dl-` temps — so a replaced poster leaves
  nothing behind.
- **Sizes for the screens served.** API posters `w500` (sharp 3× phones), backdrops `w1280`; the web
  asks posters to `w780` and full-bleed backdrops to `original` (4K/6K) with per-element `sizes` so small
  cards never over-fetch.
- **Season posters** are discovered by the worker (`seasonArt`), stored in `seasons.local_poster`
  (migration 020) and served via `local:season:<titleId>:<season>`.
- **Frame-grab fallback** (`art`): when nothing matches, ffmpeg grabs a frame (~10% in) for the card and
  hero, plus a dominant colour; held while a play reads the disk, queued as `art.grab` otherwise.

---

## 8. Playback pipeline (item → pixels)

1. `POST /api/items/{id}/playback` → **`playback.Decide`** picks direct play / remux / transcode from the
   client profile, the file's codecs/container and the hardware ([`DESIGN.md`](DESIGN.md) "Playback
   decision").
2. **`stream`** creates an HLS (fMP4) session driving **ffmpeg** (`proc`), choosing the encoder via
   **`hwaccel`** (VideoToolbox on Apple). Keyframes (`keyframes` table, extracted lazily, never on the
   request path) drive fragment cuts; sparse Cues are handled by `tfdt`. Direct play serves the file
   with Range/ETag.
3. **Subtitles** (`subs`): text tracks → WebVTT, pre-extracted (`subs.preextract`) and cached; a play
   joins or starts a run, suspended while a viewer reads the disk.
4. **Discovery** (`discovery`): mDNS advertise + AirPlay receiver browse; Cast via the API
   ([`AIRPLAY.md`](AIRPLAY.md), [`M11`](M11-CLIENTS.md)). Sessions heartbeat; idle/stopped sessions are
   reclaimed; `GET /api/sessions` is the private now-playing view across devices.

Time-to-first-frame and orphan-proof playback are specified in [`M9`](M9-ARCHITECTURE.md).

---

## 9. HTTP API & clients

**`api`** (Go 1.22 mux, JSON camelCase). Auth: optional `-token` (Bearer or `?token=` on media URLs);
a **DNS-rebinding host guard** (`-allow-host`), **same-origin** checks on writes, CORS scoped to private
endpoints; optional **HTTPS** (`tlscert`). The full endpoint table and payload shapes are in
[`DESIGN.md`](DESIGN.md) "HTTP API" (items, shows, titles, playback, sessions, subtitles, progress/watched,
match, metadata, people, genres, collections, upcoming, settings, schedules, images, events).

Performance surfaces ([`M9`](M9-ARCHITECTURE.md), [`DESIGN.md`](DESIGN.md) "Compression and list caching"):
**gzip** (pooled, `BestSpeed`) on JSON; **strong ETag/304** on the big lists; **`?fields=card`** shapes
that carry only what a page draws; **`limit=N`**; image misses answer fast (404 + `Retry-After`) rather
than blocking on the WAN. **`events`** pushes changes over **SSE** (`GET /api/events`, `Last-Event-ID`
replay).

**Clients:** the **web SPA** (`web/`, Svelte 5 + Vite, embedded via `//go:embed`) and the **iOS/tvOS**
apps (`ios/`, AVPlayer + AirPlay + Cast) consume the same API unchanged — the API is the contract
([`M11`](M11-CLIENTS.md), [`UI-BRIEF.md`](UI-BRIEF.md)).

---

## 10. Background work & schedules

`schedule` ([`M12`](M12-SCHEDULES.md)) is a small loop beside the queue: every 30 s it reads the
`schedules` table, asks the server's checker what each task may do (its key, the privacy switch) and
enqueues the kinds when due; a blocked task is logged once a day, never queued twice; `last_run` lives in
the DB so a restart does not re-run. Tasks: full scan, metadata refresh, ratings, cache prune, trailer
checks, etc. `cache.prune` also runs after each scan (§7). Ratings ([`M10`](M10-RATINGS.md)) and trailer
downloads ([`M13`](M13-TRAILERS.md)) are queue kinds with their own limiters/backoff.

---

## 11. Cross-cutting concerns

- **Efficiency** ([`DESIGN.md`](DESIGN.md) "Efficiency rules", [`M9`](M9-ARCHITECTURE.md)): playback
  first; whole-file reads off the request path; `F_NOCACHE`/`F_RDADVISE` on background readers; bounded
  caches; measured on the owner's NAS setup.
- **Security & secrets**: the repo is public — no keys/tokens/user data committed; the TMDB key lives in
  `settings` (or flags/env), never returned or logged; the data dir is 0700/0600; host guard + same-origin
  + optional TLS.
- **Build & deploy**: `CGO_ENABLED=0` for darwin/linux × amd64/arm64; the web UI is embedded; `deploy/`
  has launchd + systemd units; `ios/` is generated by XcodeGen from `project.yml`.
- **Stopping** ([`DESIGN.md`](DESIGN.md) "Stopping"): Ctrl-C/SIGTERM stops nicely (15 s deadline);
  `ply -stop [-force]` reads `control.json` and calls `POST /api/server/stop` with the control key; the
  macOS watch helper lets go of watches in the background so ply exits at once.
- **Testing bar** ([`AGENTS.md`](../AGENTS.md)): every fix has a test that fails without it (mutant-proven);
  absence assertions need a positive control; each change reviewed by a non-author; plumb MCP for all
  tooling.

---

## 12. Where to read more (spec index)

| Doc | Covers |
|---|---|
| [`DESIGN.md`](DESIGN.md) | Overall design, config, store, playback decision, **full HTTP API**, watch state, streaming, compression/caching, web UI |
| [`M6-METADATA.md`](M6-METADATA.md) | Metadata, matching, NFO, **artwork (pinned, cache, sizes, seasons)**, watching, the worker |
| [`M7-FOLDERS.md`](M7-FOLDERS.md) | Library folders / types |
| [`M8-TVDB.md`](M8-TVDB.md) | TVDB as a show source, episode order |
| [`M9-ARCHITECTURE.md`](M9-ARCHITECTURE.md) | **Performance architecture**: tiers, governor, queue, TTFF, orphan-proof playback, macOS optimisations |
| [`M10-RATINGS.md`](M10-RATINGS.md) | IMDb / MDBList ratings |
| [`M11-CLIENTS.md`](M11-CLIENTS.md) | iOS/tvOS clients, TLS, watch state API |
| [`M12-SCHEDULES.md`](M12-SCHEDULES.md) | Background task schedules |
| [`M13-TRAILERS.md`](M13-TRAILERS.md) | Trailers (fetch + native download) |
| [`AIRPLAY.md`](AIRPLAY.md) | AirPlay / Cast |
| [`PLEX-MIGRATION.md`](PLEX-MIGRATION.md) | Importing a Plex library (production cutover) |
| [`UI-BRIEF.md`](UI-BRIEF.md), [`UI-REDESIGN-PLAN.md`](UI-REDESIGN-PLAN.md) | Web UI design |

---

## 13. Recent architecture-relevant changes

- **Pinned artwork + image-cache pruning + season posters + the 4K/6K size ladder** (commit `0d9a8dc`):
  artwork is first-wins across refresh; `cache.prune` reclaims orphaned provider images and embedded
  covers (skipping in-flight temps); season sidecar posters are discovered and served; API posters at
  `w500`, web ladders to `w780`/`original`. Spec: [`M6`](M6-METADATA.md); queue: [`M9`](M9-ARCHITECTURE.md).
- **Plex migration spec** (commit `9af15e6`): [`PLEX-MIGRATION.md`](PLEX-MIGRATION.md) — join by path,
  migrate selected art as pinned sidecars, import watch state, match by external id.
- **Store indexing** (peer, commit `83be9a4`): migration 021 (`titles(kind, imdb_id)`), byte-range
  prefix lookups (`internal/store/prefix.go`) replacing `substr` scans.