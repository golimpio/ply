# M13 — Trailers on disk, played in ply's own player

Status: design, 2026-11-17. Replaces the embedded YouTube/Vimeo players of `docs/M6-METADATA.md`, "Trailers".

## Why
The Trailer button opens YouTube's or Vimeo's own player in an `<iframe>` (web) or a `WKWebView` (iOS). It looks
bolted on, and it often does not play at all: on iOS YouTube refuses the embed ("This video is unavailable, error
152-4"). The owner wants a trailer to be like a film or an episode: a file on disk, played by ply's own player with the
same design, in the web app and the iOS app. Files are laid out as Plex lays them out, so that ply can replace Plex
over the same folders, and the trailers Plex users already keep are picked up.

## Goals
1. **Local trailers are recognised** in both Plex layouts (below), for films (one version or several) and shows, and
   are no longer listed as a version of the film or as an episode.
2. **A trailer plays in ply's player** (web and iOS), the same screen and controls as a film.
3. **A missing trailer is downloaded on demand**: opening a film's or a show's page with no local trailer queues a
   background download on the server, from the trailers TMDB/TVDB list (YouTube, Vimeo). It is a job on the Activity
   page; the Trailer button shows a spinner while it runs, and plays the trailer once it is on disk.
4. **The downloader is native Go.** No yt-dlp binary, no JS runtime. ffmpeg (already a dependency) muxes and, when
   needed, converts.

## Non-goals
- Other extras (featurettes, deleted scenes, interviews...) are not played. They are only kept out of the library
  (see "Other Plex extras").
- Sources other than YouTube and Vimeo (TMDB and TVDB give no others).
- Running YouTube's player JavaScript (sig/n challenges, a goja engine): not now. If the Activity page shows real
  failures (age-restricted, "made for kids"), that is a later milestone.
- Season-level trailers in the UI: a trailer under a Season folder counts as the show's.
- Watch progress, watched marks, Continue Watching for trailers.
- Deleting trailers. ply never removes a trailer file it wrote.

## Anti-goals (a failure even if it works)
- A trailer shows up anywhere a film or an episode does: Movies, a show's episodes, Continue Watching, Recently added,
  search, counts, the matching queue, the needs-review list, a film's version picker.
- Anything reaches YouTube or Vimeo with online lookups off.
- A download stalls playback: ply's own playback and browsing tiers come first, as for every job.
- A half-written file in the library (Plex or ply picking up a partial trailer).

## Decisions taken with the owner
- **Downloader: native Go, YouTube's `visionos` innertube client only** (option A). It needs no PO token and no player
  JavaScript. Verified 2026-11-17 against two trailers: watch page → `VISITOR_DATA`, then `/youtubei/v1/player` →
  `streamingData.adaptiveFormats` with plain `url`s (no `signatureCipher`); a 1 MiB ranged GET of a 1080p video stream
  and an AAC stream answered 206 at 1.4–2.4 MB/s without touching the `n` parameter. Its known gaps: age-restricted
  and "made for kids" videos fail. Its identity (client name and version, user agent, OS version, device model) is the
  part YouTube breaks: yt-dlp changed its client settings about every month or two in 2026. It lives in one file of
  constants so that a fix is a few strings.
- **A trailer is a library item** (option B rejected: a separate table and endpoint would duplicate streaming, and MKV
  or AV1 trailers would not play on iOS). A new item kind `trailer`, linked to its film's or show's title, so the
  existing player, direct play, transcoding, HLS, the web player and the iOS player all work unchanged.
- **Both Plex layouts are read; the default for writing is the inline one** (the trailer next to the film, in the
  film's or the show's folder), as the owner prefers.
- **Several versions of a film share one trailer.**

## Plex layouts
Plex documents two ways to keep a film's trailer (and the same for a show, in the show's folder):

| Layout | Film in its own folder | Show |
|---|---|---|
| Inline (default for writing) | `Movies/Vivre sa vie (1962)/Vivre sa vie (1962)-trailer.mp4` | `TV/The Wire/The Wire-trailer.mp4` |
| Subfolder | `Movies/Vivre sa vie (1962)/Trailers/<any name>.mp4` | `TV/The Wire/Trailers/<any name>.mp4` |

Rules for reading (`internal/library`, beside `ParseNameIn`):
- **Inline**: a video file whose stem ends in `-trailer` (case-insensitive; Plex's spelling, no space before the dash;
  `.trailer` and ` - trailer` are also accepted, as Jellyfin and Kodi write them). The text before the suffix may be
  anything (Plex: a descriptive name).
- **Subfolder**: any video file inside a folder named `Trailers` or `Trailer` (case-insensitive) directly inside the
  film's or the show's folder, or inside one of the show's Season folders.
- **Whose trailer it is**:
  - A **film in its own folder**: the folder holds the files of one film only (all its movie files parse to the same
    title and year, or carry the same title), and is not the root itself. Every trailer in that folder or in its
    `Trailers/` is that film's. This is the case of several versions in one folder
    (`Vivre sa vie (1962) - 1080p.mkv`, `Vivre sa vie (1962) - 4K.mkv`): one trailer, shared.
  - **Loose films in a shared folder** (the root, or a folder of several films): an inline trailer belongs to the film
    whose parsed title and year (`parseMovie`) equal the parsed text before the suffix; none equal: the trailer is not
    attributed (logged, not listed). A `Trailers/` folder in a shared folder is not attributed either (Plex ignores
    it too).
  - A **show**: anything in the show's folder (the first folder below a shows root, as `parseEpisodeIn` finds the
    show) or below it, matched by the rules above, is the show's.
- **Other Plex extras** (folders `Behind The Scenes`, `Deleted Scenes`, `Featurettes`, `Interviews`, `Scenes`,
  `Shorts`, `Other`, and suffixes `-behindthescenes`, `-deleted`, `-featurette`, `-interview`, `-scene`, `-short`,
  `-other`) in a film's folder are no longer indexed as versions of the film. In a shows folder they keep today's
  behaviour (episode 0, not for review: `reExtrasDir`, which gains the Plex names).

Rules for writing (the download's destination):
- A film, in its own folder or loose in a shared one: `<folder of its first file>/<Title> (<Year>)-trailer.mp4`, Title
  and Year as the name of the film's first file (lowest id) reads (`parseMovie`; for `movie.mkv` the folder's): not
  TMDB's title, so that reading it back attributes it to the same film in a shared folder. In a Plex-named folder this is
  the folder's name. No year: `<Title>-trailer.mp4`. (Built: `trailers.Destination`.)
- A show: `<show folder>/<show folder name>-trailer.mp4`.
- Characters a file name cannot hold on SMB or HFS (`/ \ : * ? " < > |`) become a space; the name is NFC.
- An existing file of that name is never overwritten (then the title has a trailer: nothing is downloaded).

## Data
- Migration `019_trailers.sql`: `items.kind` gains `trailer` (SQLite cannot alter a CHECK: the table is rebuilt, as
  `008_tvdb.sql` rebuilt `titles`, with every index and trigger kept).
- A trailer row keeps the columns a film has (path, root, size, probe, title, year, `title_id`). Its `title_id` is its
  owner's: the film's title, or the show's (through `series_matches` for a show). It follows the owner's match, also
  when the owner is fixed by hand, and is cleared when the owner is unmatched.
- A trailer is never matched by the metadata worker on its own.
- Rows of files that today are versions of a film, or episodes, and are trailers by the rules above become trailers on
  the first scan after the upgrade (their progress and watched state are dropped). Today nothing re-reads them:
  `upsertItemTx` leaves the columns a path gives alone and `SetParsed` takes only `movie` and `episode`, so the
  re-read path (`Scanner.Reread`, as M7 used it for typed folders) is extended to reclassify them, run once by the
  migration's note.

## Server

### Scanner
`ParseNameIn` (or a step beside it) classifies a file as `trailer` before film or episode parsing. The scanner records
the owner (see "Whose trailer it is") so that the trailer's `title_id` can follow the owner's. The probe runs as for any
file. Files whose name starts with `.` are not indexed (the download writes a hidden temp file).

### Every list keeps trailers out
Queries that filter `kind = 'movie'` or `kind = 'episode'` already do. Every query on `items` that does not filter by
kind must be audited: lists, counts, search, Continue Watching, Recently added, the matching queue, the review list,
`/api/items`, a title's versions, subtitles extraction, scheduled jobs, the archive of a removed folder (known
already: `libraryTitle`, the first-item join of `cardJoins`). A test pins
each surface with a trailer present (and a positive control: the trailer exists).

### API
`GET /api/titles/{id}/trailers` becomes:
```json
{
  "local": [{"itemId": 812, "name": "Vivre sa vie (1962)-trailer", "durationSec": 128.4}],
  "download": {"state": "queued|running|failed", "jobId": 55, "error": "…"}
}
```
- `local`: the title's trailer items, best first (the downloaded one first, then by name). Listed whether online
  lookups are on or off: they are files.
- `download`: present while a download job for the title is queued or running, or failed and not yet due again.
  Absent otherwise.
- The request also **triggers the download**: no local trailer, online lookups on, the provider list
  (`Worker.Trailers`, unchanged) has a playable video, no download job for the title queued or running, and no failure
  still remembered for it → it queues `trailer.download` and answers with `download.state = "queued"`.
- The provider list (`site`, `key`, ...) is no longer sent to clients (nothing plays it). It stays in the store as the
  download's source.
- Unknown title → 404 as today. No metadata subsystem → `local` only.
- This changes the answer's shape: the web and iOS apps change with it (one owner, no back-compat).

### The download job (`trailer.download`)
- Key `title:<id>`. The queue's key dedup only merges with a queued job (a running one's key would run again after
  it), so the API trigger checks for a queued or running job of that key itself and queues nothing then.
- One attempt (`MaxAttempts` 1): the queue's retries with backoff would hide the failure memory below.
- Priority `PriorityUser`: a person opened the page and is waiting.
- Title on the Activity page: `Trailer: Vivre sa vie (1962)`. Progress: bytes done/total while downloading; notes
  "Downloading", "Converting to H.264", "Saving to the library". Cancellable from Activity.
- Steps:
  1. Pick the first playable video of `Worker.Trailers` (already ranked: language, trailer before teaser, official,
     newest). On failure try the next, at most 3.
  2. Download into ply's own temp directory (local disk, not the library): YouTube or Vimeo (below). Need: none
     (network only), so browsing holds it only briefly. The queue gives a job one `Need` when it starts; the steps
     below need more, so `Run.Yield` gains a form that waits on a given `Need` (`YieldFor(need)`), and the job's
     `Spec.Need` is the light one.
  3. Mux with ffmpeg into MP4 (`-c copy`, `+faststart`) when the video is H.264 and the audio AAC. Otherwise convert
     the video to H.264 with the encoder ply already picks for transcoding (VideoToolbox on the Mac), audio copied
     when AAC. Need: CPU.
  4. Copy into the library as a hidden temp file in the destination folder (`.<name>.part`), fsync, rename to the
     final name. Need: the destination's disk (held while a play reads from the same NAS, as scans are).
  5. Index the new file at once: the scanner skips a file written in the last 5 s (`DefaultSettle`), and ply knows
     this one is complete, so the job calls a scanner entry point that indexes one given path now (probe, classify,
     upsert) without the settle wait. The job ends once the trailer row exists, so a client that refetches on
     `job.done` finds it in `local`. The watcher seeing the file later changes nothing.
- Failure: the job fails with the reason in words ("YouTube asks to sign in: age-restricted video", "The folder is
  read-only", ...). It is remembered for the title as provider failures are (`fetchFailure`): a "not available" kind
  for a day, others 5 min doubling to an hour. Retrying from Activity clears it.
- Online lookups off: nothing is queued; a queued download whose turn comes with lookups off fails at once ("Online
  lookups are off"), and that failure is not remembered for the title.

### The downloader (`internal/trailerdl`)
One package, no dependency beyond the standard library, ffmpeg for muxing.
- `youtube_client.go`: the `visionos` identity as constants (`clientName VISIONOS`, id 101, version, user agent,
  `osName`, `osVersion`, `deviceMake`, `deviceModel`), taken from yt-dlp `yt_dlp/extractor/youtube/_base.py`
  (`INNERTUBE_CLIENTS['visionos']`). A comment says where to look when YouTube breaks it.
- YouTube:
  1. `GET https://www.youtube.com/watch?v=<id>&bpctr=9999999999&has_verified=1` with cookies `SOCS=CAI`,
     `PREF=hl=en&tz=UTC` and a desktop browser user agent; read `VISITOR_DATA` from ytcfg. Without it YouTube
     answers "Sign in to confirm you're not a bot" (checked).
  2. `POST https://www.youtube.com/youtubei/v1/player?prettyPrint=false` with the client's headers
     (`X-YouTube-Client-Name`, `X-YouTube-Client-Version`, `Origin`, `User-Agent`, `X-Goog-Visitor-Id`) and body
     (`context.client` incl. `visitorData`, `videoId`, `contentCheckOk`, `racyCheckOk`).
  3. `playabilityStatus.status` must be `OK` and `videoDetails.videoId` the id asked for. A paid film's page that
     names a `trailerVideoId` is followed once.
  4. Pick from `adaptiveFormats`: video ≤1080p, H.264 (`avc1`) preferred at the same height, else the highest (AV1,
     VP9); audio: AAC (`mp4a`, itag 140) preferred. Formats with `signatureCipher`, DRM, or no `url` are skipped. None
     usable → fail ("YouTube gave no stream this client can read").
  5. Download each stream with `Range` requests of about 10 MiB (randomised 95–100 %), as yt-dlp does, with a desktop
     browser user agent. Honour a start-ad wait as yt-dlp does (`_video.py`, `available_at`): an `adPlacements` entry
     of kind `AD_PLACEMENT_KIND_START`, or a before-content `adSlots` entry, gives how long to wait before the media
     requests.
- Vimeo:
  1. `GET https://player.vimeo.com/video/<id>` with a `Referer` of that address; parse `playerConfig`.
  2. `request.files.progressive`: the best ≤1080p MP4, downloaded as above. None: the `hls.cdns.*.url` master
     playlist, handed to ffmpeg (`-c copy`).
  3. A 403 "privacy settings" (an embed restricted to some sites) fails with that reason. Vimeo may refuse Go's TLS
     fingerprint (yt-dlp impersonates a browser); if that happens in practice, it is noted, not worked around here.
- Everything goes through one `http.Client` with timeouts, cancelled with the job.

## Clients
The Trailer button keeps its place in the hero (after Play) on a film's and a show's page; not on an episode's. The
Apple TV app shares the iOS detail views: it gets the same button and plays the trailer the same way.

| `GET .../trailers` says | Button |
|---|---|
| `local` not empty | **Trailer**: plays `local[0]` in ply's player |
| `download` queued or running | **Trailer** with a spinner in place of the icon, disabled; the label says "Downloading trailer" to assistive tech |
| `download` failed, or nothing | no button |

- While a download is shown, the page follows the job on the live feed (`job.*` events by `jobId`) and fetches
  `/trailers` again on `job.done` (the button becomes playable) or `job.failed` / `job.cancelled` (it goes).
- Playing a trailer opens the same player as a film (web `#/play/<itemId>`, iOS `PlaybackController`), titled
  "Trailer · Vivre sa vie". No progress is saved; closing returns to the page.
- Several local trailers: the player's existing "next" control is not used; the page offers them as today's chips
  only if more than one, under the button's menu (web: the button becomes a small menu; iOS: a `Menu`).
- Removed: `TrailerButton.svelte`'s iframe dialog, `lib/trailers.ts` `embedUrl`, iOS `WebTrailerView`,
  `TrailerSheet`, `Trailer.embedURL`/`watchURL`.

## Tests
- `internal/library`: classification and ownership tables (both layouts; own folder with one and several versions;
  shared folder with matching, unmatched and ambiguous names; shows with a show-level and a season-level trailer; other
  Plex extras); a file named like a trailer that is not one ("Trailer Park Boys S01E01.mkv" stays an episode,
  "The Trailer (2020).mkv" a film).
- `internal/store`: migration 019 on a copy of a database with items; every list surface with a trailer present.
- `internal/trailerdl`: `httptest` servers playing YouTube and Vimeo from recorded, trimmed fixtures (watch page,
  player response, ranged media); format picking; failures (LOGIN_REQUIRED, no usable format, 403, cancelled
  mid-download, truncated body). A live test against YouTube, behind the integration tag, that downloads a known
  trailer's first MiB: the canary for YouTube changes.
- The job: destination naming for each layout, no overwrite, the hidden temp file, rename, the row appearing,
  failure memory, lookups off.
- API: the new shape, the trigger and its dedup, lookups off, 404/503.
- Web: unit tests of the button states; `e2e/trailers-ui.mjs` rewritten (local trailer plays in the player; spinner
  while queued/running and then playable on `job.done`; no button on failure; nothing reaches YouTube or Vimeo).
- iOS and tvOS: `ModelsTests`/`APIClientTests` for the new shape; the detail views' button states (`ios-check`).

## Docs to update
`docs/M6-METADATA.md` "Trailers" and "Later" (point here), `docs/DESIGN.md` (API table row, the web "Trailers"
bullet), `docs/M11-CLIENTS.md` (the title page), `docs/M9-ARCHITECTURE.md` (the job kind).

## Left to the builder
- How the owner is stored for a trailer row (a column, or derived at scan time) as long as `title_id` follows the
  owner's match.
- The exact SQL audit, the migration's mechanics, the feed topic the clients subscribe to.
- Fixture recording and trimming; the integration test's choice of video.
- Web, iOS and tvOS layout details within the existing hero and design tokens.
