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
Schema via embedded migrations (`schema_version` table).

```
items(id INTEGER PK, kind TEXT CHECK(kind IN('movie','episode')), title, year INT, show, season INT, episode INT,
      path TEXT UNIQUE /*raw bytes as string*/, root, size INT, mtime INT /*unix ns*/, container,
      duration_sec REAL, bitrate INT, added_at INT, updated_at INT)
streams(item_id FK ON DELETE CASCADE, idx INT /*ffprobe index*/, type TEXT /*video|audio|subtitle*/, codec,
        codec_tag, profile, width INT, height INT, pix_fmt, color_transfer, channels INT, language, title,
        is_default INT, forced INT, PRIMARY KEY(item_id, idx))
subtitles_external(item_id FK CASCADE, path, language, format /*srt|vtt|ass*/)        -- M5
progress(item_id PK FK CASCADE, position_sec REAL, duration_sec REAL, watched INT, updated_at INT)  -- M5
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
| GET | `/api/items?kind=movie\|episode&show=<name>` | `[ItemSummary]` (movies by title; episodes by season, episode) |
| GET | `/api/shows` | `[ShowSummary]`: `{show, seasons:[int], episodeCount}` + (M6) `year?, overview?, titleId?, poster?, backdrop?, logo?, rating?, certification?, genres?, matchState, needsReview` |
| GET | `/api/items/{id}?target=<profile>` | `ItemDetail` incl. `playback` (Decision + `url`) for built-in profile (default avplayer) |
| POST | `/api/items/{id}/playback` | body `{profile?: name, custom?: Profile, startSec?, audioIndex?, subtitleIndex?}` → `Playback` |
| GET | `/api/items/{id}/file` | direct play, `http.ServeContent` (Range/ETag), explicit Content-Type: mp4 `video/mp4`, mov `video/quicktime`, m4v `video/x-m4v`, mkv `video/x-matroska`, webm `video/webm`, ts `video/mp2t`, avi `video/x-msvideo` |
| POST | `/api/scan` | 202 `{started:true}` or 409 if running |
| GET | `/api/scan` | `{running, lastStarted, lastFinished, lastError, itemCount, watching:[{root, watching, dirs, error?}]}` (`watching` is `[]` when `-watch=false`) |
| GET | `/api/sessions/{sid}/index.m3u8`, `/api/sessions/{sid}/{file}` | (M3) HLS playlist / init.mp4 / segments |
| DELETE | `/api/sessions/{sid}` | (M3) stop session; 204 or 404. For a *direct play* id (below) it only drops the play from the count: ply cannot interrupt a client that reads the file, and its next range request counts again if there is room (else 503) |
| GET | `/api/sessions` | (M9) **private** (like `/api/folders`, `/api/fs`; so are DELETE and the heartbeat below: no CORS headers, a cross-site `Origin` or `Sec-Fetch-Site` is refused with 403; the playlist and segments stay open for Cast) everything playing, oldest first: `[{id, sessionId, itemId, title, kind: transcode\|remux\|direct, client, startedAt, lastSeenAt, positionSec, paused}]` (`id` = `sessionId`; a direct play's is `d-…`; `client` = `"<ip> · <browser>"`; `positionSec` is the player's last heartbeat, else the start of the last segment it asked for) |
| POST | `/api/sessions/{sid}/heartbeat` | (M9) `{positionSec?, paused?}` (empty body ok) → 204, or 404 if the session is gone. From its first heartbeat a session is stopped 30 s after its last beat or segment request (never later than `-stream-idle`); the player beats every 10 s, paused or not. A session that never beat keeps the old rule: `-stream-idle` (60 s) without a request. A direct play id can be beaten too |
| GET | `/api/items/{id}/subtitles/{key}.vtt` | (M5) key = stream idx or `ext<N>` for sidecar; WebVTT |
| PUT | `/api/items/{id}/progress` | (M5) `{positionSec, durationSec}`; watched when ≥ 90% |
| GET | `/api/continue` | (M5) in-progress items, most recent first |

M6 (metadata; full details and the data model in `docs/M6-METADATA.md`). All need the token; `{id}` etc. unknown → 404,
bad input → 400, TMDB unreachable → 502, lookups off (no key / `-metadata=false`) → 409, no metadata subsystem → 503.

| Method | Path | Body / result |
|---|---|---|
| GET | `/api/shows/{name}` | `ShowDetail`: ShowSummary fields minus `seasons` + `meta?: TitleDetail`, `seasons:[{season, name, overview?, poster?, airDate?, episodeCount}]` (`episodeCount` = files); name matched without regard to case |
| PUT | `/api/shows/{name}/match` | `{tmdbId}` or `{imdbId}` → whole show linked by hand (`manual`); returns ShowDetail |
| DELETE | `/api/shows/{name}/match` | unmatch (`manual`, no auto re-match); returns ShowDetail |
| POST | `/api/shows/{name}/metadata/refresh`, `/api/items/{id}/metadata/refresh`, `/api/metadata/refresh` | 202 `{started:true}`; the worker refreshes in the background |
| PUT | `/api/items/{id}/match` | `{tmdbId}` or `{imdbId}` (a movie; episodes → 400) → `manual`; returns ItemDetail |
| DELETE | `/api/items/{id}/match` | unmatch; returns ItemDetail |
| PATCH | `/api/titles/{id}` | `{title?, sortTitle?, year?, overview?, tagline?}` (at least one) → title `locked`; returns TitleDetail |
| GET | `/api/people?q=` | `[PersonCard]` (≥1 title in the library, max 20; blank q → `[]`) |
| GET | `/api/people/{id}` | `Person` (details fetched from TMDB on first visit) with `credits` for library titles, newest first |
| GET | `/api/genres` | `[{id, name, count}]` |
| GET | `/api/titles?genre=&collection=&person=` | `[TitleCard]` (ids; non-numeric → 400) |
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
             audioCodec, width, height, hdr, addedAt, progress?: {positionSec, watched}, art?}
             + (M6) titleId?, poster?, backdrop?, logo?, rating?, certification?, genres?: [string], matchState,
             needsReview, and for episodes overview?, still? (image fields are relative URLs, with ?token=)
ItemDetail  ItemSummary + {path(basename only), size, bitrate, streams:[Stream], subtitles:[{key, language,
             title, format, image:bool, url?}], playback: Playback, meta?: TitleDetail,
             episodeInfo?: {name, overview, airDate, still, rating}}   -- "episode" stays the episode number
TitleCard   {id, kind: movie|series, title, year, poster?, backdrop?, rating?, itemId?, show?, nextAirDate?}
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

## Discovery
Listen first (supports `:0`), then advertise `_ply._tcp` with the real port, TXT `id=<serverid>`, `v=1`,
`name=<name>`. Log LAN IPv4 URLs at startup (never only localhost). Shutdown un-registers.

## Streaming (M3/M4)
- `POST /playback` with method remux or transcode creates a session: temp dir under `<data>/tmp/sessions/<pid>/<sid>`
  (it was `os.TempDir()/ply-sessions`; that is still swept at start for one release).
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
  or request (a request being waited for counts as activity); shutdown kills every process group and removes the temp dirs.
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

## Efficiency rules (hard requirement — build in and test)
1. Decision order: direct > remux (copy all) > audio-only transcode (copy video) > full transcode; reasons say why.
2. Zero-copy direct play: `http.ServeContent` with `*os.File` → sendfile. Every middleware ResponseWriter wrapper
   must delegate `io.ReaderFrom` (and `http.Flusher`, `Unwrap()`); test asserts the wrapped writer still exposes
   ReaderFrom. Never gzip video/segments.
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
- Dev: `vite` proxy `/api` → `http://localhost:8420`. `npm run build` → `web/dist`, embedded by `web/embed.go`.
  Keep `web/dist/.gitkeep` so `go build` works before the first web build (`all:dist`).

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
