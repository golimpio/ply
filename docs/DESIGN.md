# ply — design & phase-1 plan

Local-only Plex-like media server. Hosts: macOS and Linux. One Go binary with an
embedded Svelte web UI. Phase 2 (later, NOT now) is an iOS app (AVPlayer + AirPlay,
Google Cast SDK) that consumes the same API — design the API so it drops in unchanged.

## Working rules (all agents)

- **Read `AGENTS.md` first.** Its rule 1, above every other, is to always use plumb MCP. Its other rules
  cover the public repo, reviews, mutants and positive controls.
- Workspace: `/Users/gilberto/Projects/ply`. Do not commit unless you are the agent that commits for this task.
- No background processes left running. Temp fixtures in `t.TempDir()` or the scratchpad.
- Absence assertions need a positive control (e.g. "item deleted" test first asserts it existed).
- Keep code idiomatic, small packages, comments only where non-obvious. stdlib first.

## Tooling present
go 1.27.2 darwin/arm64, ffmpeg/ffprobe 9.0.2 (Homebrew, on PATH), node/npm.

## Layout (Go module `ply`, go.mod at repo root)

```
cmd/ply/main.go            flags, wiring, graceful shutdown
internal/config            flags + data dir resolution + server id
internal/probe             ffprobe wrapper → probe.Result
internal/library           scanner + nameparse
internal/store             SQLite (modernc.org/sqlite)
internal/playback          Profile, built-in profiles, Decide()
internal/api               HTTP handlers, auth, CORS, SPA serving
internal/discovery         mDNS (_ply._tcp, github.com/grandcat/zeroconf)
internal/stream            (M3) HLS session manager, ffmpeg runner
internal/hwaccel           (M4) encoder probe
web/                       Svelte 5 + Vite SPA; web/embed.go: package web, //go:embed all:dist
deploy/                    launchd plist + systemd unit
```

CGO_ENABLED=0 must build for darwin/linux × amd64/arm64.

## Config
Flags: `-media` (repeatable roots), `-addr` (default `:8420`, `:0` allowed), `-data`, `-token`, `-name`
(default hostname), `-scan-interval` (default 15m, 0 disables), `-ffprobe`, `-ffmpeg` (default from PATH).
M6 (metadata, live library):
- `-watch` (default true): watch the media folders and pick up changes within seconds (`internal/watch`).
- `-allow-host <name>` (repeatable; env `PLY_ALLOW_HOSTS`, comma-separated; `.example.org` allows every name under it):
  extra Host names to answer. Every request whose Host is not an IP, localhost, a single label, `.local`,
  `.home.arpa`, `.internal` (ICANN private-use, e.g. `host.docker.internal`), an RFC 2606/6761 reserved name, the
  machine's hostname or one of these gets 403 (DNS-rebinding guard). Entries are case-insensitive, a trailing dot is
  ignored, and `.name` also matches `name` itself; `name` alone matches only `name`.
  Non-GET requests with a foreign `Origin` also need the token.
  Names that are not on that list need `-allow-host`: `.lan`, `.home`, `.localdomain`, `*.ts.net` (Tailscale
  MagicDNS) and any real domain. Behind a reverse proxy the proxy must pass the original Host through, or the
  Host is the proxy's upstream name and the same-origin check on writes (Origin vs Host) fails: for nginx,
  `proxy_set_header Host $host;` (use `$http_host` to keep a non-default port). The 403 bodies say which name or
  Origin was refused.
- `-tmdb-key` (env `PLY_TMDB_KEY`): TMDB v3 API key or v4 read access token. Precedence: flag > env > the key saved
  from the web UI (settings table) > the build default (`-ldflags -X ply/internal/metadata.DefaultKey=...`). The key
  is never returned by the API nor logged.
- `-metadata` (env `PLY_METADATA`, default true): `false` switches every online lookup and image download off.
- `-metadata-language` (env `PLY_METADATA_LANGUAGE`, default `en-US`), `-metadata-refresh` (env
  `PLY_METADATA_REFRESH`, default 24h; 0 turns the periodic refresh off, not the matching of new files).
- Hidden, for tests: `-tmdb-url` (env `PLY_TMDB_URL`) and `-tmdb-image-url` (env `PLY_TMDB_IMAGE_URL`).
Data dir: macOS `~/Library/Application Support/ply`; Linux `$XDG_DATA_HOME/ply` or `~/.local/share/ply`;
`-data` overrides. Persist a random server id (`server-id` file, 16 hex bytes). DB: `<data>/ply.db` (it holds the TMDB key:
the data dir is created 0700, a new DB file is created 0600 (never briefly readable), and the DB with its `-wal` and
`-shm` files is chmod 0600 at every start). Where chmod is refused (EPERM, ENOTSUP, EROFS: vfat, exfat, some network
mounts) ply starts anyway and logs a warning (`store.Warnings()`).

## Store
modernc.org/sqlite, DSN `file:<path>?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)`
(URL-escape the path — the macOS default contains a space; tests use a dir with a space). `SetMaxOpenConns(1)`.
Schema via embedded migrations (`schema_version` table). When an existing database has migrations waiting, `Open` first
writes a copy of it to `<db>.bak-v<version it is at>` (`VACUUM INTO`: a consistent copy, owner-only from the moment it is
created and chmod-ed and checked like the database itself, written under `.tmp` and renamed), keeps the 3 newest copies,
and reports one line with its path (`Store.Notices`, logged at start). A new database makes none; a copy that cannot be
made stops the start with the database as it was.

```
items(id INTEGER PK, kind TEXT CHECK(kind IN('movie','episode')), title, year INT, show, season INT, episode INT,
      path TEXT UNIQUE /*raw bytes as string*/, root, size INT, mtime INT /*unix ns*/, container,
      duration_sec REAL, bitrate INT, added_at INT, updated_at INT)
streams(item_id FK ON DELETE CASCADE, idx INT /*ffprobe index*/, type TEXT /*video|audio|subtitle*/, codec,
        codec_tag, profile, width INT, height INT, pix_fmt, color_transfer, channels INT, language, title,
        is_default INT, forced INT, PRIMARY KEY(item_id, idx))
subtitles_external(item_id FK CASCADE, path, language, format /*srt|vtt|ass*/)        -- M5
progress(item_id PK FK CASCADE, position_sec REAL, duration_sec REAL, watched_at INT, play_count INT, updated_at INT)  -- M5, M11 (see "Watch state")
```

## Probe
`ffprobe -v error -print_format json -show_format -show_streams -i <ABS path>` with a context timeout.
Keep `codec_tag_string`, drop ones that look like `[0][0][0][0]`. Container normalisation from `format_name`
+ extension: `mov,mp4,m4a,3gp,3g2,mj2` → `mp4`/`mov`/`m4v` by ext; `matroska,webm` → `mkv` or `webm` by ext;
`mpegts` → `ts`; `avi` → `avi`. Tags read case-insensitively (language, title). HDR: keep `color_transfer`
(`smpte2084`/`arib-std-b67`) and detect Dolby Vision via side_data `DOVI configuration record`.

## Scanner
- Walk roots; skip dot files/dirs (covers AppleDouble `._*`). Video exts (case-insensitive ext only):
  mp4 m4v mov mkv webm avi ts m2ts. Follow file symlinks via `os.Stat`.
- Skip unchanged files (size + mtime match DB). Probe with worker pool (4). One scan at a time (`TryLock`;
  concurrent request returns "already running").
- **Safety**: delete DB rows for missing files under a root ONLY if that root's walk finished with zero errors
  and the root exists. Unmounted/permission errors never wipe the library.
- Titles NFC-normalised (`golang.org/x/text/unicode/norm`); path stored RAW (never normalised).
- Periodic rescan (`-scan-interval`) + `POST /api/scan`. M6 adds the watcher (`-watch`), which runs partial scans.
- Sidecar subtitles (M5): `<basename>.srt|.vtt|.ass` and `<basename>.<lang>.srt` etc.

## Name parsing
- Episode: `(?i)^(.*?)[\s._-]*\bs(\d{1,2})[\s._-]*e(\d{1,3})` and `(\d{1,2})x(\d{2,3})`. Empty show part →
  parent dir name, skipping `Season N` / `Specials` dirs (go up one more). Episode title = remainder after the
  SxxExx token if any (cleaned), else empty.
- Movie: LAST plausible year (1900–2099) wins: "Blade Runner 2049 (2017)" → "Blade Runner 2049", 2017;
  "1917 (2019)" → "1917", 2019; "2001 A Space Odyssey (1968)" → "2001 A Space Odyssey", 1968. Drop junk after the
  year (1080p, BluRay, x264…). Strip brackets/parens around year.
- Clean: `.` and `_` → space, collapse whitespace, trim trailing ` -`.

## Playback decision (`internal/playback`)
```go
type Profile struct {
    Name string; Containers, VideoCodecs, AudioCodecs []string
    RequireHVC1 bool          // avplayer: hevc must be tagged hvc1 for direct play
    MaxHeight int             // 0 = unlimited
    HLS bool                  // client can play HLS (all of ours can)
    TextSubs []string         // subtitle formats renderable as sidecar (vtt)
}
type Decision struct {
    Method string            // "direct" | "remux" | "transcode"
    VideoAction, AudioAction string // "copy" | "transcode"
    AudioIndex int           // chosen audio stream (ffprobe idx), -1 none
    BurnSubtitle int         // ffprobe idx of image sub to burn, -1 none
    Reasons []string         // human readable, e.g. "container mkv not supported by avplayer"
}
func Decide(item Item, streams []Stream, p Profile, opts Options) Decision // Options{AudioIndex, SubtitleIndex}
```
Rules: direct iff container ∈ profile, video codec ok (+hvc1 rule), chosen audio ok, height ≤ MaxHeight, no burn-in.
Otherwise remux (copy video, copy or transcode audio→aac) iff video ok; otherwise transcode (h264, aac).
Selecting an image subtitle (pgs/dvd_subtitle/dvb_subtitle) forces transcode with burn-in.
Built-ins (M1: avplayer; M4: browser, chromecast):
- `avplayer`: containers mp4 mov m4v; video h264 hevc(hvc1); audio aac ac3 eac3 alac mp3.
- `chromecast`: containers mp4 webm; video h264 (vp9 omitted — conservative); audio aac mp3 opus flac; MaxHeight 1080.
- `browser`: mp4 webm; h264 vp9 av1; aac mp3 opus flac. Web client normally POSTs its own measured profile.
Table-test heavily.

## HTTP API (Go 1.22 mux, JSON camelCase, errors `{"error": "..."}`)
All URLs returned are RELATIVE (clients build absolute LAN URLs). When a token is configured, every returned
stream/file/subtitle URL carries `?token=<t>` (AirPlay/Cast receivers can't send headers).
Auth: if `-token` set, all `/api/*` except `/api/info` need `Authorization: Bearer <t>` or `?token=<t>`
(constant-time compare). SPA assets are public. CORS middleware on everything: `Access-Control-Allow-Origin: *`,
allow headers `Range, Authorization, Content-Type`, methods `GET, POST, PUT, PATCH, DELETE, OPTIONS`, expose
`Content-Range, Accept-Ranges, Content-Length, Content-Type`; OPTIONS → 204.
Cross-site writes: a request that is not GET, HEAD or OPTIONS and has an `Origin` other than its own `scheme://host`
(`X-Forwarded-Proto: https` counts as https) is refused with 403 unless it carries the valid token; native clients send
no `Origin`. Reads keep CORS `*` (media, Cast); a preflight is answered as above and the request it announces is refused.

| Method | Path | Body / result |
|---|---|---|
| GET | `/api/info` | `{id, name, version, authRequired}` (no auth) |
| GET | `/api/items?kind=movie\|episode&show=<name>` | `[ItemSummary]` (movies by title; episodes by season, episode); a movie row has `sortTitle`, the key to sort movies by (below); `ETag`/`304` and gzip: see "Compression and list caching" |
| GET | `/api/shows` | `[ShowSummary]`: `{show, seasons:[int], episodeCount, watchedEpisodes, inProgress}` + (M6) `year?, overview?, titleId?, poster?, backdrop?, logo?, rating?, certification?, genres?, matchState, needsReview` + `sortTitle` (the key to sort shows by); `ETag`/`304` |
| GET | `/api/items/{id}?target=<profile>` | `ItemDetail` incl. `playback` (Decision + `url`) for built-in profile (default avplayer) |
| POST | `/api/items/{id}/playback` | body `{profile?: name, custom?: Profile, startSec?, audioIndex?, subtitleIndex?}` → `Playback` |
| GET | `/api/items/{id}/file` | direct play, `http.ServeContent` (Range/ETag), explicit Content-Type: mp4 `video/mp4`, mov `video/quicktime`, m4v `video/x-m4v`, mkv `video/x-matroska`, webm `video/webm`, ts `video/mp2t`, avi `video/x-msvideo` |
| POST | `/api/scan` | 202 `{started:true}` or 409 if running |
| GET | `/api/scan` | `{running, lastStarted, lastFinished, lastError, itemCount, watching:[{root, watching, dirs, error?}]}` (`watching` is `[]` when `-watch=false`) |
| GET | `/api/sessions/{sid}/index.m3u8`, `/api/sessions/{sid}/{file}` | (M3) HLS playlist / init.mp4 / segments |
| DELETE | `/api/sessions/{sid}` | (M3) stop session; 204 or 404. A session stopped this way is remembered for 2 min as *stopped on purpose*: its heartbeat, playlist, init and segments answer **410** `{"error":"stopped","stoppedBy":"another device"}` (404 stays for an unknown id, or one ply ended for its silence; a player restarts on a 404, and stops on a 410). For a *direct play* id (below) it drops the play from the count and, for 2 min, the client's `/file` requests and the id's heartbeat get the same 410 (ply cannot interrupt a client that is reading the file); a `POST /playback` from that client allows it again |
| GET | `/api/sessions` | (M9) **private** (like `/api/folders`, `/api/fs`; so are DELETE and the heartbeat below: no CORS headers, a cross-site `Origin` or `Sec-Fetch-Site` is refused with 403; the playlist and segments stay open for Cast) everything playing, oldest first: `[{id, sessionId, itemId, title, kind: transcode\|remux\|direct, client, startedAt, lastSeenAt, positionSec, paused}]` (`id` = `sessionId`; a direct play's is `d-…`; `client` = `"<ip> · <browser>"`; `positionSec` is the player's last heartbeat, else the start of the last segment it asked for) |
| POST | `/api/sessions/{sid}/heartbeat` | (M9) `{positionSec?, paused?}` (empty body ok) → 204; 404 if the session is unknown or ply ended it; 410 (above) if it was stopped on purpose. From its first heartbeat a session is stopped 30 s after its last beat or segment request (never later than `-stream-idle`); if the last beat said `paused: true` it has 3 min instead (a hidden tab's timers run about once a minute), until a segment request or a beat that does not say `paused: true` brings the 30 s back. The player beats every 10 s. A session that never beat keeps the old rule: `-stream-idle` (60 s) without a request. A direct play id can be beaten too |
| GET | `/api/items/{id}/subtitles/{key}.vtt` | (M5) key = stream idx or `ext<N>` for sidecar; WebVTT. (M9) A cached track is a plain file (`Content-Length`, ranges). An embedded track that is not cached yet is extracted from the container (reading all of it), and the answer does not wait for that: `422` if it fails within a second of the request, `503` + `Retry-After` if the file cannot be read (a share not mounted yet) or the extraction is stopped by someone else (a job cancelled, ply stopping), the finished file if it ends within that second, otherwise `200` chunked (`Cache-Control: no-store`, no ranges) with each cue as ffmpeg writes it, ending at the last cue; the same read extracts the item's other text tracks. A `HEAD` of a track that is not cached answers `200` with no body and starts nothing. The queue extracts them ahead of time for what is being watched and for new files (`subs.preextract`, `docs/M9-ARCHITECTURE.md`) |
| PUT | `/api/items/{id}/progress` | (M5) `{positionSec, durationSec}`; watched when ≥ 90%; answers `{positionSec, watched}` or `null` |
| PUT | `/api/items/{id}/watched` | (M11) `{watched: true\|false}` (required, else 400) → `WatchResult`; a film that has a title is marked in all its versions (as `/api/titles/{id}/watched`); same token and cross-site rules as `progress` |
| PUT | `/api/titles/{id}/watched` | (M11) same body, every version of a film or every episode of a series; 404 when the title has no files |
| PUT | `/api/shows/{name}/watched`, `/api/shows/{name}/seasons/{n}/watched` | (M11) same body, every episode of the show (matched or not; name as for `GET /api/shows/{name}`) or of one season (0 = specials); 404 for an unknown show or a season with no episodes |
| GET | `/api/continue` | (M5) in-progress items, most recent first |

M6 (metadata; full details and the data model in `docs/M6-METADATA.md`). All need the token; `{id}` etc. unknown → 404,
bad input → 400, TMDB unreachable → 502, lookups off (no key / `-metadata=false`) → 409, no metadata subsystem → 503.

| Method | Path | Body / result |
|---|---|---|
| GET | `/api/shows/{name}` | `ShowDetail`: ShowSummary fields minus `seasons` + `meta?: TitleDetail`, `seasons:[{season, name, overview?, poster?, airDate?, episodeCount, watchedEpisodes, inProgress}]` (M11: `episodeCount` counts episodes, an episode with two files being one; the show has the three counts too, without the specials); name matched without regard to case |
| PUT | `/api/shows/{name}/match` | `{tmdbId}` or `{imdbId}` → whole show linked by hand (`manual`); returns ShowDetail |
| DELETE | `/api/shows/{name}/match` | unmatch (`manual`, no auto re-match); returns ShowDetail |
| POST | `/api/shows/{name}/metadata/refresh`, `/api/items/{id}/metadata/refresh`, `/api/metadata/refresh` | 202 `{started:true}`; the worker refreshes in the background |
| PUT | `/api/items/{id}/match` | `{tmdbId}` or `{imdbId}` (a movie; episodes → 400) → `manual`; returns ItemDetail |
| DELETE | `/api/items/{id}/match` | unmatch; returns ItemDetail |
| PATCH | `/api/titles/{id}` | `{title?, sortTitle?, year?, overview?, tagline?}` (at least one) → title `locked`; returns TitleDetail |
| GET | `/api/titles/{id}/trailers` | `[{site: youtube\|vimeo, key, name?, type?: trailer\|teaser, language?, official, publishedAt?}]` best first; fetched the first time it is asked, kept 30 days (`docs/M6-METADATA.md`, "Trailers"); `[]` when there are none, the fetch failed or online lookups are off; unknown title → 404, no metadata subsystem → 503 |
| GET | `/api/people?q=` | `[PersonCard]` (≥1 title in the library, max 20; blank q → `[]`) |
| GET | `/api/people/{id}` | `Person` (details fetched from TMDB on first visit) with `credits` for library titles, newest first |
| GET | `/api/genres` | `[{id, name, count}]` |
| GET | `/api/titles?genre=&collection=&person=` | `[TitleCard]` (ids; non-numeric → 400), ordered by `sortTitle` (without regard to case), year, id; `ETag`/`304` |
| GET | `/api/collections/{id}` | `{id, name, overview?, poster?, backdrop?, titles:[TitleCard]}` in release order |
| GET | `/api/upcoming` | `[{titleId, show, poster?, backdrop?, nextAirDate, nextEpisode?: {season, episode, name}}]` library series airing today or later, soonest first, max 50 |
| GET | `/api/metadata/search?kind=movie\|series&q=&year=` | `[{tmdbId, title, originalTitle?, year, overview?, poster?, score}]` best first |
| GET | `/api/metadata/status` | `{enabled, keySet, running, pending, unmatched, needsReview, lastRefresh, lastError}` |
| GET | `/api/metadata/review` | `[{kind, itemId?, show?, title, year, matchState, needsReview, score, candidate?}]` unmatched first |
| GET, PATCH (PUT too) | `/api/settings` | `{tmdbKeySet, tmdbKeySource: flag\|env\|settings\|default\|"", metadataEnabled, metadataForcedOff, language, maxStreams, maxTranscodes, maxStreamsSource, maxTranscodesSource: flag\|settings\|default}`; PATCH `{tmdbKey?, metadataEnabled?, language?, maxStreams?: 1..32, maxTranscodes?: 1..16}`: a new key is checked against TMDB (400 refused, 502 unreachable), `""` removes the stored key; a limit out of range → 400, one given by `-max-streams`/`-max-transcodes` (or `PLY_MAX_*`) → 409; everything is checked before anything is stored; limits apply at once; **the key is never returned or logged** |
| GET | `/api/images/tmdb/{size}/{file}` | size ∈ `w92 w185 w342 w500 w780 w1280 original`, file `^[A-Za-z0-9_-]+\.(jpg\|png\|svg)$` else 400; downloaded once into `<data>/images`, `Cache-Control: public, max-age=31536000, immutable`; not available → 404 |
| GET | `/api/images/local/{item\|series}/{id}/{poster\|backdrop\|thumb}` | sidecar art (path from the DB, must be a regular file whose bytes sniff as an image) or a cover extracted from the media file; `no-cache` |

```
ItemSummary {id, kind, title, year, show, season, episode, episodeTitle, durationSec, container, videoCodec,
             audioCodec, width, height, hdr, addedAt, progress: {positionSec, watched}|null, art?,
             watched, watchedAt|null, playCount}                                  -- (M11) see "Watch state"
             + (M6) titleId?, poster?, backdrop?, logo?, rating?, certification?, genres?: [string], matchState,
             needsReview, sortTitle (movies only: episodes sort by show, season, episode), and for episodes overview?,
             still? (image fields are relative URLs, with ?token=)
ItemDetail  ItemSummary + {path(basename only), size, bitrate, streams:[Stream], subtitles:[{key, language,
             title, format, image:bool, url?}], playback: Playback, meta?: TitleDetail,
             episodeInfo?: {name, overview, airDate, still, rating}}   -- "episode" stays the episode number
TitleCard   {id, kind: movie|series, title, sortTitle, year, poster?, backdrop?, rating?, itemId?, show?, nextAirDate?,
             watched, and for a series episodeCount, watchedEpisodes, inProgress}   -- (M11)
TitleDetail TitleCard + {tmdbId?, imdbId?, originalTitle?, sortTitle, overview?, tagline?, releaseDate?, endDate?,
             status?, runtimeMin?, voteCount?, certification?, logo?, genres:[{id,name}],
             collection?: {id,name}, cast:[{personId,name,character,profile?}], crew:[{personId,name,job,profile?}],
             source: tmdb|nfo|mixed, locked, matchState, needsReview}
PersonCard  {id, name, profile?, knownFor?}
Stream      {index, type, codec, codecTag, profile, width, height, pixFmt, colorTransfer, channels, language,
             title, default, forced}
Playback    {method, videoAction, audioAction, audioIndex, reasons, url, sessionId?, startSec}
```
File paths come only from the DB, never from the request. Unknown id → 404.
SPA: non-`/api` paths serve embedded `web/dist` files; unknown paths fall back to `index.html`.

## Watch state (M11)
The one place it is kept is the `progress` row of a file: `position_sec` (where to resume), `watched_at` (unix seconds
when the file was last watched to the end or marked; 0 = not watched), `play_count` (how many times), `updated_at`.
There is no `watched` flag: watched is `watched_at <> 0`, and a table CHECK keeps `watched_at` and `play_count` in
agreement (both 0 or both set). A file with no row is not started and not watched; a row exists while the file is
started (position ≥ 30 s) or watched. Code: `internal/store/progress.go` and `watch.go`.

- **Playing.** A watch is counted when the position gets to the end (≥ 90 %, or within 120 s of it with half played)
  from a stored position that was not there: reports from the credits on do not count again, and a second viewing
  counts when it gets to the end. A watched file stays watched when it is played again, whatever position it stops
  at (it used to turn unwatched at the first position under 30 s). It is not in Continue Watching while watched.
- **Marking.** `PUT .../watched {watched: true}` gives each file a row at position 0, watched now, once; a file that
  was watched keeps its time and its count (marking twice, or a season of which some episodes were seen, rewrites no
  history). `{watched: false}` deletes the rows, positions with them.
- **Decision: a title, a season and a show have no state of their own; it is read off the files.** One table for
  files, no second flag that could disagree, and everything that moves a file (a rename, a move, `item_archive` when a
  folder is removed and added back, which carries `watched_at` and `play_count` with the position) keeps the state
  with it. A film with a title is watched when *any* of its files is; a season when *all* its episodes are, and a show
  when all its episodes are *but the specials* (season 0), which have counts of their own (a show of nothing but
  specials is counted by them). `episodeCount`, `watchedEpisodes` and `inProgress` (started, not watched) say how far.
  They count **episodes, not files**: an episode is a (show, season, number), watched when any of its files is, started
  when none is watched and one is started; a file with no number in its name is an episode of its own. A series
  title's counts are over all the shows linked to it, an episode two of them have being one.
- **A film is one thing to watch.** Marking a film (by its title, or by any of its files) marks all the files that
  are linked to the title now, and unmarking clears them all: unmarking only the file shown would leave the film
  watched through another. A version that arrives later has no row and is watched all the same, as the film was seen;
  the first mark or unmark after that covers it. `watchedAt` and `playCount` of a film are the latest time and the
  highest count among its files. A file no title is known for is its own film.
- **A replaced file keeps the state (marks).** An upgrade puts a new file at another path and deletes the old one, and
  the old one's progress row goes with it. So when the scanner deletes a watched file (`DeleteItems`) that is linked to
  a title (an episode: its show's series title, with its season and number), it leaves a **mark** in `title_watch`
  (`title_id, season, episode`, `watched_at`, `play_count`, `removed_at`; a film's is season 0, episode 0). At the end of
  every write (`applyWatchMarks`, so every way a file gets a title, or a show gets one, is covered) a mark with a file to
  carry it gives that file its state, a row like any other, and goes: a film's mark to any file linked to the title (the
  next version, or a copy that is still there, at once), an episode's to a file of that number in a show linked to
  the series. The state stays in the files; a mark is only the bridge. A mark belongs to its title (another title is
  never given it), is deleted by unmarking the title, the season, the show or the episode, keeps its title from
  `PruneMetadata` while it waits, and is dropped after 90 days. Removing a library folder leaves none (the archive keeps
  the files' state by path, and "forget" forgets); a file no title is known for leaves none.
- **What is left.** A watched file that goes with no replacement that is linked to the same title within 90 days is
  unwatched again, and a file that was never linked to a title has no state beyond itself.
  Keying all the state by title would survive more, but would make every list answer join two sources of truth.
- **Answers.** `ItemSummary.watched`, `watchedAt` (null when not) and `playCount` are the file's, except for a film
  with a title, where they are the film's; `progress` stays the file's own `{positionSec, watched}` (null with no
  row). `ShowSummary`, `ShowDetail` and each `ShowDetail.seasons[]` entry have the three counts; a series `TitleCard`
  has them and `watched` (all episodes); a film's has `watched`. `WatchResult` (the answer to every `PUT .../watched`)
  is `{watched, items: [{id, watched, watchedAt, playCount, progress}]}`: the files the call covered, as they are now.

## Discovery
Listen first (supports `:0`), then advertise `_ply._tcp` with the real port, TXT `id=<serverid>`, `v=1`,
`name=<name>`. Log LAN IPv4 URLs at startup (never only localhost). Shutdown un-registers.

## Streaming (M3/M4)
- `POST /playback` with method remux or transcode creates a session: temp dir under `<data>/tmp/sessions/<pid>/<sid>`
  (it was `os.TempDir()/ply-sessions`; that is still swept at start for one release).
- **Seeking (M11 notes).** The playlist is always the whole file's, with the file's own times (segment `i` starts at
  the keyframe `plan.Segments[i].Start`; the fragments carry those times plus `TimelineOffset`, 2 s, which a player
  strips by itself), so a seek anywhere is a plain request for the segment that covers the time, and ffmpeg is cut in
  at that segment's first keyframe: up to one segment (6 s or the GOP length) before the target, never after it. The
  player has the data from there to the target and seeks to the exact time by itself (`video.currentTime = t`); the
  server never moves a seek. `POST /playback {startSec}` echoes `startSec` as asked, starts ffmpeg at the segment
  that covers it, and from then on the session's playlist carries `#EXT-X-START:TIME-OFFSET=<startSec>,PRECISE=YES`
  (only for a start inside the file) so that a player that honours it (Apple's) begins at that second and asks for
  the segment ffmpeg is already writing, not for the first one (which would stop that ffmpeg and start another from
  the top, and again at the resume position). hls.js is given its `startPosition`, which wins over the tag. (Not tried in Safari: the tag is Apple's own, but no test here plays it.)
- **Limits on plays (M9)**: `-max-streams` (default 4, `PLY_MAX_STREAMS`) caps all plays, direct ones included;
  `-max-transcodes` (default 1 with a software encoder, 3 with a hardware one, `PLY_MAX_TRANSCODES`) caps the
  transcodes among them. Precedence flag > settings table (`playback.max_streams`, `playback.max_transcodes`) >
  default; `Manager.SetLimits` applies a change live. A session being stopped does not count, so the player's own
  stop-then-start never meets a 503 (a session that takes more than 10 s to stop does count again, so that an ffmpeg
  that will not die cannot pile up unseen). A direct play is `(item, client IP, User-Agent)`: counted for as long as a
  `/file` GET is being served (a player may read the whole film over one connection) and for 30 s after the last one
  ended; a client that comes back to the item within 10 min of being forgotten is admitted even at the cap (it is
  carrying on, not starting another play). A HEAD is neither counted nor refused. At the cap: 503, `Retry-After: 5`, `{error: "4 of 4 plays in use"\|
  "1 of 1 transcodes in use", limit: streams\|transcodes, max, inUse, playing: [<GET /api/sessions entries>]}`. Nothing
  is stopped to make room.
- **Orphan-proof (M9)**: every running ffmpeg is recorded in `<data>/sessions/<sid>.json` (pid, process group, process
  start time, item, session dir; written before `Create` returns, removed when ffmpeg has been waited for). At start a
  new ply kills the groups the files name whose pid is alive *and* started when the file says (pid reuse is safe),
  SIGCONT then SIGKILL, and removes their directories. A file is only believed if its pid is its pgid (ffmpeg leads
  its group), and a group is never killed if it is ply's own; the registry directory is checked like the temp root
  (a real directory of ours, mode 0700). Every 5 s the manager's housekeeping compares the registry with its sessions
  and does the same for any registered group that belongs to none; it does not look for ffmpeg that is not in the
  registry. On macOS ply starts a detached reaper per ffmpeg group, `<data>/ply-reaper --reap <pgid> <ppid>` (a link
  to the same binary, so that `killall ply` does not take it too), that kqueue-watches ply and kills the group when
  it dies, at once if ply is already gone when it starts; ply ends it when the ffmpeg ends. On Linux ffmpeg has
  `Pdeathsig = SIGKILL`, with the fork on a locked OS thread. (A *stopped* orphaned group is also ended by the kernel,
  which sends it SIGHUP: only a running one outlives ply.)
- **Server-generated complete VOD playlist** (`#EXT-X-PLAYLIST-TYPE:VOD`, `#EXT-X-ENDLIST`, exact EXTINF) is returned
  immediately so players can seek anywhere. Segment boundaries come from a keyframe list, never from ffmpeg's own
  segmenter (which cuts differently after a restart):
  - remux (`-c:v copy`): the source's keyframes, extracted once with a nice'd ffprobe packet scan and cached in the
    `keyframes` table; keyframes are grouped into segments of at least 6 s.
  - transcode: a synthetic keyframe every 6 s (`-force_key_frames`, no scan); each segment is exactly one GOP.
- ffmpeg runs the hls muxer with `-hls_time 0.001`, so it writes **one fMP4 fragment per keyframe** (`gopNNNNN.m4s`,
  `-start_number` = keyframe index). A playlist segment is served as the concatenation of its fragments (later
  `styp` boxes dropped), and only when each fragment is complete (`temp_file` plus a box-tiling check).
  Timestamps: `-copyts -start_at_zero -output_ts_offset 2` plus `movflags=frag_discont` give every fragment its true
  decode time on the file's timeline (+2 s), whichever run produced it, so runs from different seeks line up and the
  init segment is the same bytes across runs. See `docs/NOTES.md` for the ffmpeg details.
- **Seek** = a request outside [current run start, encoded + ~18 s] kills ffmpeg and restarts it at that fragment with
  fast input seek (`-ss` before `-i`; a keyframe-aware bias for copy, exact for a transcode).
  **Throttle**: more than 20 segments ahead of the player, SIGSTOP; SIGCONT below 10. A session is stopped
  `-stream-idle` (60 s) after its last request, or, once its player has sent a heartbeat, 30 s after its last beat
  or request (3 min, at most 5, if that beat said it is paused; a request being waited for counts as activity); shutdown kills every process group and removes the temp dirs.
  A 503 at a limit lists what is playing only to the server's own pages, not to a cross-site request.
- Playlist URL `/api/sessions/<sid>/index.m3u8`; with a token configured every URI in the playlist carries `?token=`.
  `Playback.startSec` echoes the requested resume position; stream time 0 = file time 0.
- ffmpeg binary: jellyfin-ffmpeg if installed, else PATH; `-ffmpeg`/`-ffprobe` override. Always a subprocess.
- **Transcode (M4)** (`internal/hwaccel`). At startup (in the background) ply lists `-encoders`, `-hwaccels`,
  `-filters` and runs a real 1-frame lavfi test encode (10 s timeout) per candidate: macOS `h264_videotoolbox`;
  Linux `h264_vaapi` (`/dev/dri/renderD128`), `h264_qsv`, `h264_nvenc`; else `libx264 -preset veryfast -crf 21`.
  `-hwaccel auto|none|videotoolbox|vaapi|qsv|nvenc`. Output is H.264 High, yuv420p, scaled down to the profile's
  MaxHeight, bitrate capped by output height (<=720p 4 Mbps, <=1080p 8, <=1440p 12, above 20), audio AAC 192k
  stereo when it cannot be copied. The GPU path is end to end with no hwdownload (VideoToolbox decode + `scale_vt` +
  encoder; VAAPI `scale_vaapi`); decode and scale fall back to software only when the chain needs it (burn-in,
  HDR tone mapping, a codec the GPU decoder is not trusted with) and the reasons say so. HDR is tone-mapped
  (`zscale` + `tonemap`) when this ffmpeg has both, else plainly converted with a "washed-out colours" reason.
  An image subtitle (`Decision.BurnSubtitle`) forces a transcode with `-filter_complex` overlay. If a GPU ffmpeg
  fails before its first segment, the session retries once with libx264.

## Compression and list caching
The lists of a big library are what the browse path asks for most and what costs most (5,000 films: 4.4 MB of JSON,
~150 ms to build once the titles are read in few queries, 250 ms before). Two things keep that off the wire and off
the disk.

- **gzip** (`internal/api/compress.go`; the standard library, pooled writers at `BestSpeed`: a person is waiting and
  the machine may be playing a film; 10× smaller, within 8% of the default level). GET answers that are JSON, HTML,
  JavaScript, CSS, SVG or WebVTT and at least 1 KB, for a client that sends `Accept-Encoding: gzip` (not `q=0`); the web
  UI's files too. `Vary: Accept-Encoding` is on every GET and HEAD that could have been compressed. Never: the media
  (`/api/items/{id}/file`, `/api/items/{id}/art/*`, `/api/images/*`, `/api/sessions/{sid}/*`) and the event stream
  (`/api/events`), whose requests are not wrapped at all (sendfile and flush are as they were); a request with `Range`;
  HEAD and the writes; any status but 200; an answer with a `Content-Encoding` or a type that is not text; one that
  flushes before it has 1 KB, which is a stream (the subtitles while they are extracted). A compressed answer has no
  `Content-Length` or `Accept-Ranges`, and its `ETag`, if any, is weak. No brotli: it is no part of the standard
  library, and a dependency for a few percent more on a LAN is not worth it.
- **ETag / 304** on `GET /api/items`, `/api/shows` and `/api/titles` (`internal/api/listcache.go`): a strong
  `ETag: "<generation>.<colours>"` and `Cache-Control: private, no-cache`; a request with a matching `If-None-Match`
  (weak comparison, `*` too) is answered `304` before the library is read (3 µs). The generation is
  `Store.Generation()`: in memory, it starts at the time the store was opened (so a number kept from an earlier run never
  matches) and goes up after every committed write through `Store.write` or `Store.exec` (items, matches, titles,
  progress and watch state, ratings, settings), except the writes no list shows: the background queue's table, the
  keyframe cache, an item's fingerprint and when MDBList was last asked about a title (`Store.execQuiet`). The colours are the artwork
  generator's `ColourVersion()`, because the list carries the colour of each picture and that is not in the database.
  The ETag is taken before the library is read, so a write that lands in between can only make the list newer than its
  ETag says (the next request then gets it again), never older: a client is never told that an old list is the current
  one. A 400 or a 500 has no ETag. A list that is missing something it should have (the titles, the watch state or the
  episodes of its rows could not be read; the rows are sent without them, and the failure is logged) has no ETag either
  and says `Cache-Control: no-store`, so that it is not kept as the current one. A browser does the rest by itself (`fetch` revalidates a `no-cache` answer with
  `If-None-Match` and uses its copy on a 304). Not covered yet: `/api/continue`, `/api/upcoming`, `/api/genres`,
  `/api/collections/{id}`, `/api/shows/{name}`, `/api/items/{id}`.
- **`?fields=card`** on `GET /api/items` answers the rows in the shape the browsing UI draws, one shape per kind: `[ItemCard]` for `kind=movie` and `[EpisodeCard]` for `kind=episode` (`internal/api/card.go`; `fields=card` with no `kind`, or any other value of `fields`, is 400). A movie card is exactly `{id, kind, title, sortTitle, year?, titleId?, addedAt, poster?, backdrop?, art: {thumb, color?}|null, rating: {source, value, scale, score100, state?}|null, watched, progress: {positionSec, watched}|null, durationSec, container, width, height, hdr?, genres?: [name], matchState, needsReview}`; an episode card is exactly `{id, kind, title, year?, show?, season?, episode?, episodeTitle?, addedAt, durationSec, still?, overview?, art: {thumb, color?}|null, ratings?: [...], watched, watchedAt, playCount, progress: ...|null, width, height, hdr?}`, each with the name, the shape and the omitempty of the full row (`art.thumb` is its frame-grab link with
  the `?v=<mtime>` stamp, `backdrop` its TMDB 16:9 link, `rating.state` Rotten Tomatoes' word). Those are the fields a page draws: `still` because `lib/meta.ts` paints an episode from it, `overview` because the season list prints it under the episode, `width`/`height`/`hdr` because MediaCard's badge reads them, and the watch fields because the client patches them after a mark. Same rows, same order; the `sort` and `minRating` filters apply. It is a fraction of the weight: 581 against 884 bytes a row for 5,000 films, and 358 against 885 for 5,000 episodes (4.42 MB, 192 KB gzipped at the level `compress` uses, becomes 1.79 MB, 96 KB), measured by `PLY_LISTPERF=1 go test -run TestListPayload -v ./internal/api/`. Each shape has its own ETag (`"<generation>.<colours>.card"`, `...episode-card`), so two shapes never share a validator; a change in a shape needs no new key, because the generation starts anew on every run.
- **`limit=N`** on `GET /api/items` (a number from 1 to 500; anything else is 400) answers the first N rows after `sort` and `minRating`, in either shape, so a client that draws a rail can ask for the few it shows instead of the whole library (the web UI does not use it yet). It is part of the ETag's key (`...limit20`), so a limited list never shares a validator with the whole one. The cut is made in the store's query (`LIMIT`, and the streams of only the listed rows: 20 rows of a 5,000-film library cost 16 ms against 159, and 11.6 KB against 2.91 MB) unless `sort=rating` or `minRating` is given, which the server applies in Go over the whole list; then the cut is in Go too. Everything else about a film or an episode is in
  `GET /api/items/{id}`.
- **`sortTitle`.** The name a title is listed under: `titles.sort_title` ("Matrix, The": `metadata.SortTitle` moves a
  leading The, A or An to the end when it is made, and a person can write their own with `PATCH /api/titles/{id}`),
  or its title when it has none (`store.sortTitleCol`, which `GET /api/titles` is ordered by, lower-cased by SQLite:
  ASCII letters only, then year, then id). `TitleCard.sortTitle` is that. A movie row of `/api/items` and a row of
  `/api/shows` carry the sort title of the title they are matched to; one that is matched to none carries
  `metadata.SortTitle` of its name. Compare it without regard to case. `/api/items` and `/api/shows` themselves stay in
  the order of the file and folder names ("The Matrix" under T): a client sorts them by `sortTitle`. Only English articles
  are moved; the ones of other languages ("Le", "Der", "El") are not.

## Efficiency rules (hard requirement — build in and test)
1. Decision order: direct > remux (copy all) > audio-only transcode (copy video) > full transcode; reasons say why.
2. Zero-copy direct play: `http.ServeContent` with `*os.File` → sendfile. Every middleware ResponseWriter wrapper
   must delegate `io.ReaderFrom` (and `http.Flusher`, `Unwrap()`); test asserts the wrapped writer still exposes
   ReaderFrom. Never gzip video/segments. The one wrapper there is, `compress` (below), does not wrap the media at
   all, and delegates ReaderFrom, Flusher and Unwrap for the text it does.
3. GPU transcode end-to-end, no hwdownload: VideoToolbox `-hwaccel videotoolbox -hwaccel_output_format
   videotoolbox_vld` + `scale_vt` + `h264_videotoolbox`; VAAPI `-hwaccel vaapi -hwaccel_output_format vaapi
   -vf scale_vaapi=...` + `h264_vaapi`; fall back to software decode/scale only when the filter chain needs it
   (burn-in, tone-map) and say so in reasons.
4. Keyframe-index VOD playlists for remux (see Streaming).
5. Seek = kill + restart with fast input seek; serve only complete segments.
6. Throttle via SIGSTOP/SIGCONT; idle kill 60 s without a request (30 s after the last beat for a player that sends heartbeats); ffmpeg in its own process group; shutdown kills all and removes
   temp dirs; tests assert no ffmpeg children remain.
7. Thumbnails/trickplay: later, keyframe-only, nice'd.
8. Scans: incremental (size+mtime), bounded probe pool, ffprobe/ffmpeg for scans run at reduced priority
   (`nice -n 10` wrapper on unix or `syscall.Setpriority` on the child).
9. Measure: M3/M4 notes record CPU% and RSS (server + ffmpeg) while streaming a generated 4K HEVC 10-bit 60 s
   fixture (direct, remux, transcode) and time-to-first-segment. Put numbers in `docs/NOTES.md`.

Libraries (all optional — use only when they beat ffmpeg/ffprobe for the job): Eyevinn/mp4ff, abema/go-mp4,
remko/go-mkvparse (keyframe index), bluenviron/gohlslib, asticode/go-astisub (subtitle conversion).
Jellyfin/jellyfin-ffmpeg are GPL — read for design ideas only, never copy code.

## Web UI (`web/`)
Svelte 5 (runes) + Vite + TypeScript, hls.js. No UI framework; small hand-written CSS, dark theme, responsive
(phone browser usable). Hash routing (`#/movies`, `#/shows`, `#/shows/<name>`, `#/item/<id>`, `#/play/<id>`).
- Token: if `/api/info.authRequired`, prompt once, store in localStorage, send Bearer; append `?token=` to media URLs.
- Library grid (Movies; Shows → seasons → episodes), item detail (codecs, streams, duration, playback decision &
  reasons for this browser), player.
- Player: computes a browser profile (MediaSource.isTypeSupported / canPlayType for containers & codecs, incl.
  `video/mp4; codecs="hvc1"`, `audio/mp4; codecs="ac-3"`/`"ec-3"`), POSTs `/playback`, then: direct → `<video src>`;
  HLS → native if `canPlayType('application/vnd.apple.mpegurl')` (Safari) else hls.js. Seek past encoded range →
  new playback with `startSec` (display time = startSec + currentTime). DELETE session on leave.
- **Player chrome** (`pages/Play.svelte`, `components/Player.svelte`, `components/PlayerControls.svelte`): Play fills
  the browser window (a fixed layer, not the Fullscreen API; App leaves the header and tab bar out), the picture
  letterboxed on black; the full screen button still uses the Fullscreen API on the frame. An overlay over it: the
  title at the top, the page's `h1` (a film: its title and year; an episode: show · SxxEyy above its title), how it
  is played (a badge; its reasons in a popover) and close (✕), the controls at the bottom; both bars pad into the
  safe areas (an iPhone held sideways). It shows on a mousemove, a touch or a key and hides 3 s (`IDLE_MS`) after
  the last while playing, the cursor with it; it stays while paused, with a menu or the popover open, while
  scrubbing, with the mouse on a control (not on a bar's empty space: a mouse parked at the edge lets it go), or
  with keyboard focus that came in by keyboard. Touch: a tap on the picture shows or hides it (never plays or
  pauses), a double tap on a side skips 10 s. Close and Escape call `goBack()` (to the item page from a deep link);
  the unmount ends the session as any leaving does (report with keepalive, heartbeat stopped, DELETE). Escape takes
  one layer at a time: a menu or the popover, then full screen, then the player (an Escape within 500 ms of leaving
  full screen is taken as the one that left it). While the item loads or fails, Play shows the same ✕ and takes
  Escape. The scrubber seeks to the fraction of the bar under the pointer (pointerdown captures, a drag previews,
  pointerup seeks, and the fill stays there while the element seeks); its `<input type=range step=any>` takes no
  pointer (a native range maps a click with the thumb's width taken off both ends) and is there for the keyboard
  (arrows ±5 s, Home, End; Space and K play and pause) and assistive technology. Cost while playing: the bars fade
  by opacity, the fill and tooltip move by transform; a mousemove only notes the time (one idle timer at a time,
  plain listeners that stop at the player, as Svelte's own handlers each set a timer) and reads no layout (the bar's
  rect is read on pointerdown and on resize); while hidden the clock and buffer bars don't update, so nothing runs,
  and they catch up as it shows; the lock screen's position is set on play, pause, seeked, ratechange and
  loadedmetadata only. Tested by `e2e/player-ui.mjs`.
- **Back** (`lib/router.svelte.ts`, `components/BackButton.svelte`): the top-level sections, Home, Movies and Shows,
  have no Back button; every other page has one under the header (floating over a hero): a film, an episode, a
  show, a person, a collection, a genre, Settings and Activity (the player's close and Escape do the same). The router
  keeps the views of the tab (at most 50), one per history entry, keyed in `history.state`; each holds its scroll
  position, the link that was followed (or the one in the middle of the screen), and state a page `remember()`s
  (the genre page's sort). `goBack()` goes to the previous view with `history.back()`, so the browser's Back and
  Forward stay in step; with none (a deep link) it replaces the entry with the parent (`parentOf`: a film → Movies,
  an episode → its show, a show → Shows, the player → its item, everything else → Home), and the button names it.
  Programmatic navigation goes through `navigate()` (a new entry) or `redirect()` (replaceState). On Back the scroll
  waits a frame (content-visibility lays a new grid out once, at its real sizes), then for the page: no `aria-busy`
  (skeletons, or Item's and Show's details on their way), and the followed link drawn, or room enough, or a page
  that stopped growing; it jumps, puts that link back where it was over up to 10 frames, and gives it the focus
  (else the page's `h1`). A page that restores itself (a virtualized grid) calls `claimRestore()` as it mounts and
  the router leaves the scroll and the focus alone (`refocus` says whether to put it back: not on a reload). A
  reload keeps the views: pagehide writes them to sessionStorage, and the entry left by `navigate()` carries its
  scroll position. Sort and filters are in the hash, so Back returns to them
  exactly. Tested by `router.test.ts` and `e2e/navigation.mjs`.
- **Movies and Shows at scale** (`components/VirtualGrid.svelte`, `components/JumpRail.svelte`, `lib/vgrid.ts`,
  `lib/az.ts`): the grid keeps in the page only the rows on screen, about a screen more above and below
  (`nearRows`) and the focused card's row with its neighbours, each row absolutely placed at row x pitch; 5,000
  films are 70 to 100 cards in the DOM. The columns are the CSS grid's own (`.grid`, `.grid.posters`, read from its
  computed style on resize); every card is as tall as the next (MediaCard's line under the title is always there and
  one line tall, a rating glyph or not), so the pitch is one measured card (ResizeObserver) plus the gap. A scroll
  costs one read of the grid's place per frame (rAF). When the pitch or the columns change (a resize, a zoom, the
  fonts) the page scrolls so that the first card on screen stays in the first row. Posters on screen load eager with
  `fetchpriority=high`, the others lazy and low; `sizes` is the column width, so the srcset picks the smallest
  that fills it (w185 for a 177 px column at 1x, w500 at 2x). A new row's posters aren't asked for until it has
  been in the page 100 ms (one timer for the row due first; a first mount, a Back and a rail jump load at once): a
  fling through 5,000 films starts about 36 requests instead of about 1,000, and a scroll at reading speed still
  finds every poster asked for, its row mounted a screen ahead. A poster unmounted before it loaded drops its
  `src`, which cancels its request (without that, the last screen's posters after a fling waited 25 s at 150 ms
  an image; now under 1 s). It remembers the first card of the first row wholly under the header and, on Back, Forward
  or a reload, claims the restore and scrolls that card back into the first row (the same place at the same width;
  the same row at another), then focuses the followed card unless reloading. Arrow keys move between cards (up and
  down by a row). The grid is a list (`role=list`, each card a listitem with `aria-setsize` and `aria-posinset`).
  Movies, Shows and Home's Movies rail sort by name as the server does (`byName`: the ASCII lower-case sort title,
  "Matrix, The", then year, then id) since `/api/items` and `/api/shows` keep file order. Sorted by name, a list of
  40 or more with two letters or more has the **A to Z rail** on the right edge: the letters present (# for digits
  and symbols, accents dropped: É is E), each a 24 px target (`--rail-w`, `--rail-letter-h`; a screen too short
  for all shows fewer, evenly, and a drag still reaches every one), faint (`--opacity-rail-idle`) until hovered,
  focused or touched; where the gutter is narrower than the rail the grid makes room. A click jumps that letter's
  first card to the top, a drag jumps as it goes (a touch drag shows the letter big), and from the keyboard it is
  one Tab stop (roving tabindex: ↑ ↓ Home End, Enter jumps and focuses the card). Tested by `vgrid.test.ts`,
  `az.test.ts` and `e2e/library-ui.mjs` (5,000 films: the DOM bound, priorities mid-list and at 2x, cards with a
  rating glyph, the measured pitch and a live resize, the rail's targets at five sizes, by mouse, touch and
  keyboard, Back at another width, a 304 revisit).
- **Settings** (`pages/Settings.svelte`, a tab each in `pages/settings/`): five tabs, **Library** (folders, a scan,
  Needs attention), **Details** (online lookups, language, the matching status, Refresh all details), **Ratings**
  (primary, shown order, a link to the MDBList key), **Playback** (the limits) and **API keys** (TMDB, TVDB key and
  PIN, MDBList, each with its notice). The tab is in the hash, `#/settings?tab=details|ratings|playback|keys`
  (Library, the default, is the bare `#/settings`; the player's older `?s=playback` opens Playback:
  `lib/settings-tabs.ts`), and a switch is a `redirect()`, so tabs add no history entries and Back leaves Settings.
  `components/Tabs.svelte` is the WAI-ARIA tabs pattern (tablist/tab/tabpanel, `aria-selected`, `aria-controls` on
  the selected tab, roving tabindex, arrows, Home and End, selection follows focus; the panel is not a Tab stop of its
  own, every panel having controls); only the selected panel is mounted, so a tab fetches only what it shows
  (Library: folders, review, status; Details: settings and status; API keys: settings and the status once, for the
  server's word on a key; Ratings and Playback: settings). Library and Details poll the status while matching runs
  (`lib/matching.svelte.ts`, `poll: true`); API keys doesn't. From 900 px the tabs are a column beside the panel (the
  selected one a quiet fill and the accent's bar), below a row that scrolls sideways, the selected tab in its middle
  and a fade (`--fade-edge`, a mask) at an edge with more past it. The three key cards share one layout (`.key-card`
  in app.css) and one field, `components/KeyField.svelte`: a key that is set shows as a mask, eight dots in the
  placeholder only (the server never sends a key). An empty save sends nothing; Remove asks first, in place
  (`ConfirmRemove`: "Remove the TMDB key?", the focus on Keep). Test is off while a key is typed and not saved (it
  would test the saved one; Save checks the typed one). Each Test (`KeyTest`) shows "Worked when tested 5 minutes
  ago" or "Failed when tested ..." from `<p>KeyTestedAt`/`<p>KeyTestOk` (M8-TVDB.md › Last tested), its own result at
  once, and drops it when the key changes. The TMDB notice (`TmdbNotice`) is in the TMDB card and in Details, whether
  or not the settings load. Settings and Activity are centred in `.page-container` (app.css, `--page-max`). Tested by
  `lib/settings-tabs.test.ts` and `e2e/settings-ui.mjs`.
- **Season posters** (`components/SeasonRow.svelte`, `lib/seasons.ts`): a show's seasons are a row of 2:3 posters
  (`--season-w`, `--season-w-narrow` from 600 px down), each with its name ("Specials" for 0, last), its episode count
  and its watch badge; a season with no poster of its own has the show's, its name on a band over it. The row
  scrolls sideways with snap points and never wraps. Several seasons are the WAI-ARIA tabs pattern (as Settings: one
  Tab stop, arrows, Home and End, selection follows focus; the episode list is the tabpanel); the selected one has
  `--ring-selected`. The season is the hash's `?s=N`, and a switch is a `redirect()`, which leaves the page where it
  is scrolled to for another state of the same page. The posters in view (the selected card scrolled into view
  first) load with `fetchpriority=high`, the rest lazily; none is asked for until the show's details are in, so a
  card never loads the show's poster and then its own. One season is a card beside its name, not a row of one.
  Tested by `e2e/watched-ui.mjs`.
- **Watched** (M11, see "Watch state"; `lib/watched.ts`, `lib/library.svelte.ts`, `components/WatchBadge.svelte`):
  toggles on a film's and an episode's page ("Mark as watched" / "Mark as unwatched", text that says what happens),
  on each episode of a show's list (an icon button labelled "Mark S01E02 as watched"), on the season ("Mark season
  as watched", "Mark specials as ...") and on the show's hero. A film with a title goes to `PUT /api/titles/{id}/watched`
  (all its versions), an episode or a file with no title to the item's, a season and a show to theirs.
  `markWatched()` changes the cached library at once, as the server will, then writes the answer's `items[]` into it
  (`applyWatch`; a film's versions share the film's state); on an error it puts every file back and the page says
  why in its `.error` style. Marks can overlap: each change stamps the files it touches with a generation, and an
  answer or a rollback only writes files whose last change is its own. The toggles stay enabled while a mark is out
  (`aria-disabled`, a press meanwhile does nothing), so the focus stays on the button. An episode's toggle marks
  its own file (the item route): an episode with two files is counted watched when either is, and unmarking one
  file leaves the other, by design (the state is the file's). A `PUT /progress` answering `watched: true` makes the
  item watched (`applyProgress`). `findItem` and `episodesOf` read an index (by id, by show) built once per fetched
  list, so a page of 2,800 title cards does no linear search.
  So every card, rail and count follows without a refetch: the counts of a show and a season are read off the cached
  episodes (`countEpisodes`, `showCounts`: an episode is a show, season and number, watched when any of its files is;
  a show's counts leave the specials out unless it has nothing else), as the server counts them. The Unwatched filter reads `item.watched` (the film's). Badges (in the
  style of `.badge`, over the picture so a card keeps its height): ✓ on a watched film or episode ("Watched" to a
  screen reader), "N left" on a show or season with some watched ("3 episodes left"; a dot when one is started),
  "In progress" when none is watched and one is started, ✓ when all are; a partly watched film keeps its progress
  bar. A title card (genre, collection, person) of a film reads its cached item; of a series, the card's own counts
  (over every show linked to it), plus what changed since the list was fetched in its show (`titleBadge`: the
  show's counts from before its first change are kept). Continue watching drops an item whose own file is watched,
  not its film (a film seen in one version and being watched again in another stays). Card lines say the same
  distinct-episode count as the badge.
  Tested by `lib/watched.test.ts`, `lib/library.test.ts` and `e2e/watched-ui.mjs`.
- **Library cache** (`lib/library.svelte.ts` on `lib/rowstore.svelte.ts`, `lib/details.svelte.ts`): two lists, each
  loaded once per library version. `movies` holds the movie cards (`GET /api/items?kind=movie&fields=card`) and
  `episodes` the episode cards (`GET /api/items?kind=episode&fields=card`), with `/api/shows` alongside (a failed
  show list costs only the show metadata).
  Movies loads only the cards, Shows and Show only the episodes; Home, Item and search load both. Rows are
  `$state.raw` and never changed in place: a write replaces the row and the list, and a write made while a list is
  loading is applied again when it lands. A page that needs a film's full item (Item, Home's hero) asks
  `detail(id)`: shared in flight, 32 kept, cleared on a scan; it paints the cached copy at once and always
  refetches, and Item's playback decision reads only the fresh one. Watch state comes from the list row, never the
  detail. Search runs over both lists only while the palette is open. Tested by `lib/rowstore.svelte.test.ts`,
  `lib/details.test.ts`, `lib/library.test.ts`, `lib/palette-search.test.ts` and `e2e/library-ui.mjs` (a Movies
  revisit sends only the card request, then a 304).
- **Trailers** (`components/TrailerButton.svelte`, `lib/trailers.ts`): a film's page (not an episode's: its
  title is the show's) and a show's have a Trailer button in the hero, after Play, once
  `GET /api/titles/{id}/trailers` has one that can be played (none, an error, lookups off: no button). It opens
  a `Dialog` (`video`: 16:9, as wide as the window allows) with the site's player in an `<iframe>` made on the
  click: `youtube-nocookie.com/embed/<key>?autoplay=1&rel=0&playsinline=1` or
  `player.vimeo.com/video/<key>?autoplay=1&dnt=1`, `referrerpolicy=strict-origin-when-cross-origin` (YouTube
  refuses an embed with no referrer), full screen allowed. Several are chips under it (`aria-pressed`); each one
  picked gets a new iframe (a new `src` in the same one would add a history entry that Back would spend). Closing
  the dialog (✕, Escape, the backdrop) removes the iframe, which stops the video, and gives the focus back to the
  button. The key is checked again before the address is built. The button is keyed by the title id: after a
  fixed match the old title's trailers are gone at once. Tested by `lib/trailers.test.ts` and
  `e2e/trailers-ui.mjs` (the players' sites answered by the test: nothing leaves the machine).
- Dev: `vite` proxy `/api` → `http://localhost:8420`. `npm run build` → `web/dist`, embedded by `web/embed.go`.
  Keep `web/dist/.gitkeep` so `go build` works before the first web build (`all:dist`).

### Styles
- **Tokens**: every colour, font, size, radius, shadow, duration and layer of the UI is a CSS custom property in
  `web/src/styles/tokens.css`, imported first by `app.css`. Two layers:
  - *primitives*, named for what they are: the palette (`--velvet-900`, `--paper-100`, `--tungsten-400`...), its
    translucent steps (`--white-a08`, `--black-a50`, `--velvet-950-a78`...), `--space-*`, `--radius-2`...`--radius-16`,
    `--dur-*` and `--curve-*`, `--family-*`, `--size-*`. Nothing outside tokens.css uses them (the checker reads
    their names between tokens.css's "Primitives" and "Semantic tokens" markers, and fails on any other use).
  - *semantic tokens*, named for what they do, pointing at primitives: `--surface-*` (room, raised, control, dialog,
    menu, bar...), `--fill-*`, `--line-*`, `--text-*`, `--accent`, `--danger`/`--ok`/`--warn` and their `--on-*`,
    `--rating-*`, `--scrim-*`, `--shadow-*`/`--ring-*`, `--radius-*`, `--motion-*`/`--ease*`, `--z-*`. Components use
    these. The names from before (`--bg`, `--bg-2`, `--bg-3`, `--muted`, `--font-sans`) are kept as aliases.
- **Fonts**: `--font-text` (body), `--font-display` (headings, the brand) and `--font-mono` (`code`); all three are
  the system stack today. **Type scale**: `--fs-3xs` ... `--fs-4xl` (a `-2`/`-3` step sits just above its name, e.g.
  `--fs-xs-3` = 0.85rem), fluid `--fs-lede`, `--fs-h1`, `--fs-display`, `--fs-tile`, relative `--fs-rel-*`, and the
  root size `--fs-root`; line heights `--lh-*`, weights `--fw-*`, tracking `--tracking-*`. The positional names
  (`--fs-xs-3`, `--fs-sm-4`...) are interim: they go once the merge candidates below are decided, and the scale is
  named again from what is left. `--space-*` and the old names stay.
- **Swapping the theme or the fonts** is an edit of tokens.css alone: change the primitives, or point the semantic
  tokens at others; change `--font-*`. `index.html` and `public/manifest.json` repeat the room colour (`#0c0c0b`, Cinema Dark) as
  `theme-color` (switched dynamically to `#f6f4ef`, Warm Paper, when the light theme is toggled): they can't read CSS, so change them with it.
- **Shared classes** in `app.css`, for what repeats: `.btn`, `.chip`/`.chips`, `.badge`, `.score-chip`,
  `.settings-panel` (every card of Settings), `.text-field` and `.settings-input` (text fields and selects), `.switch`
  (recoloured with `--switch-on`), `.grid`/`.poster-grid`, and for a title's page `.cert`, `.badges`, `.hero-actions`,
  `.review-note`, `.title-overview`, `.genres`, `.credits`, `.hero-sk`.
- **The checker**, `npm run check:tokens` (also run by `npm run check`): `web/scripts/check-tokens.mjs` reads the
  `<style>` blocks and `.css` files under `web/src` (not tokens.css) and fails, with file:line, on a raw colour (hex,
  `rgb()`/`hsl()`/..., a named or system colour), a raw `font-family`/`font` or `font-size`, a raw `border-radius`
  (corners and logical corners too), a raw duration or easing in `transition`/`animation` (and their `-delay`), a raw
  length, duration or easing in a component's own custom property, and any use of a primitive. `0`, `inherit`,
  `transparent`, `currentColor`, strings and `url()`s are fine; its `ALLOW` list holds the one exception (the
  generated tile's per-title hue). `src/styles/tokens.test.ts` tests it.
- **Frosted glass**: write `backdrop-filter` alone. The build adds `-webkit-backdrop-filter`; given both, it kept only
  the prefixed one, so Chrome and Firefox drew dialogs, the menu and the bars without their blur until
  2026-11-06. `src/styles/backdrop.test.ts` builds the CSS and checks every blur reaches the unprefixed property.
- **No visible change, proven**: `e2e/visual.mjs` screenshots about 45 pages and states (hover, focus, errors, the
  scrolled header, the player's menus...) at 360, 700, 1280 and 1920 px against a mocked API with a fake clock
  (`PLY_VISUAL_OUT=<dir>`, `PLY_E2E_DIST=<build>`). Two runs on one build give the same bytes; compare two builds
  with `cmp`. Beside each PNG: `.motion.txt`, every element's computed transition and animation with motion not
  reduced (pixels can't show motion), and `.glass.txt`, its computed `backdrop-filter` (Chrome's blur is not
  repeatable, so the pictures are taken without it; `PLY_VISUAL_BLUR=1` keeps it, to look at).
- **Merge candidates** (kept apart so the token layer changed nothing; merging is a design decision, to be shown
  with before/after shots): font sizes 0.7/0.72rem (`--fs-3xs` TabBar, Settings tiles / `--fs-2xs` badges),
  0.75/0.78/0.8rem (`--fs-2xs-2` kbd, card notes / `--fs-2xs-3` folder tag / `--fs-xs`), 0.82/0.85/0.875/0.88/0.9rem
  (`--fs-xs-2` PersonCard role, Activity times / `--fs-xs-3`, the most used small size / `--fs-sm` / `--fs-sm-2`
  folder type buttons / `--fs-sm-3`), 0.92/0.925/0.95rem (`--fs-sm-4` path field, PersonCard name / `--fs-md` TVDB
  labels / `--fs-md-2` folder rows, Activity lanes), 1.05/1.08rem (`--fs-lg` / `--fs-lg-2` overviews), 1.15/1.2rem
  and the two `--fs-lede` clamps (Item tagline / Collection overview); radii 4/5px (`--radius-sm` / `--radius-kbd`,
  the header's kbd) and 6/8px (`--radius-item` menus, palette thumbs / `--radius-row` folder rows, token field);
  weights 500/550 and 600/650; line heights 1.25/1.3 and 1.45/1.5/1.55; the floating surfaces 0.92/0.94/0.95/0.96
  (`--surface-palette`/`-dialog`/`-popover`/`-menu`) with blurs 24/20/16px and shadows `--shadow-menu`/`-popover`;
  the room at 0.70/0.72/0.75/0.78 and 0.85/0.86/0.90 (`--surface-bar-fade`/`-badge`/`-tag`/`-bar`,
  `--scrim-hero-side`/`--surface-tabbar`/`--surface-tooltip`); white lines 0.10/0.12/0.14 and 0.16/0.18/0.20
  (`--line-overlay`/`-control`/`-quiet`, `--glass-line`/`--line-badge`/`--line-spinner`); fills 0.07/0.08
  (`--fill-subtle`/`--fill`); text 0.82/0.86 (`--text-hero-meta`/`--text-lede`); black 0.72/0.75; durations
  0.2/0.22/0.25s, 0.35/0.38/0.4s and 1/1.1s.

## Milestones & acceptance
- **M1 server core**: config, probe, scanner, nameparse, store, playback (avplayer), API (info, items, shows, item
  detail, file, scan), auth, CORS, mDNS, deploy/ units. Tests: nameparse + Decide tables; store tests in a dir with
  a space; API httptest incl. 206 Range, auth matrix (no token configured / missing / bearer / query / wrong);
  integration test (`//go:build integration`) generating fixtures with `ffmpeg -f lavfi` (testsrc+sine:
  mp4/h264/aac and mkv/hevc/eac3), scanning, asserting items + decisions, rescan-after-delete removes the item,
  missing root does NOT wipe. `go vet` clean, cross-compile check (CGO_ENABLED=0 GOOS=linux GOARCH=amd64).
- **M2 web UI**: browse + detail + direct play; embedded in binary; SPA fallback.
- **M3 HLS remux**: session manager, playlist/segment endpoints, token rewrite, MKV plays in browser via hls.js;
  tests incl. no-orphan-ffmpeg and temp dir cleanup.
- **M4 transcode**: hwaccel probe, transcode path, browser/chromecast profiles, custom profile POST, burn-in plumbing.
- **M5 progress + subtitles**: progress/continue endpoints + UI resume ("Resume from 12:34"), subtitles: embedded
  text → VTT extraction (cached in `<data>/subs`), sidecar srt/vtt/ass, image subs → burn-in, web `<track>` with
  offset-aware cues for HLS sessions.
