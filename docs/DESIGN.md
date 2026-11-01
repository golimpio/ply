# ply — design & phase-1 plan

Local-only Plex-like media server. Hosts: macOS and Linux. One Go binary with an
embedded Svelte web UI. Phase 2 (later, NOT now) is an iOS app (AVPlayer + AirPlay,
Google Cast SDK) that consumes the same API — design the API so it drops in unchanged.

## Working rules (all agents)

- Workspace: `/Users/gilberto/Projects/ply`. Git repo exists. **Do not commit** — the orchestrator commits.
- **Use plumb MCP tools for everything** they cover: `session_start` first, `read_file` → `edit_file`/`write_file`
  (pass `expected_mtime`/`expected_sha` from the read), `search_in_files`, `find_files`, `file_outline`,
  `diagnostics`, `git` (read-only for you). Build/test via `run_task` (`language: "go"`, slots `build`, `test`, `e2e`;
  `e2e` = `-tags=integration`) and `run_command` (`go-tidy`, `go-vet`, `go-test-race`, `go-test-integration`,
  `web-install`, `web-build`, `web-check`, `ply-build`). Native tools only where plumb has no equivalent.
  Any plumb friction/bug: report it with `tool_feedback` (one line, concrete) and carry on.
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
Data dir: macOS `~/Library/Application Support/ply`; Linux `$XDG_DATA_HOME/ply` or `~/.local/share/ply`;
`-data` overrides. Persist a random server id (`server-id` file, 16 hex bytes). DB: `<data>/ply.db`.

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
- Periodic rescan (`-scan-interval`) + `POST /api/scan`. fsnotify is out of scope.
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
allow headers `Range, Authorization, Content-Type`, methods `GET, POST, PUT, DELETE, OPTIONS`, expose
`Content-Range, Accept-Ranges, Content-Length, Content-Type`; OPTIONS → 204.

| Method | Path | Body / result |
|---|---|---|
| GET | `/api/info` | `{id, name, version, authRequired}` (no auth) |
| GET | `/api/items?kind=movie\|episode&show=<name>` | `[ItemSummary]` (movies by title; episodes by season, episode) |
| GET | `/api/shows` | `[{show, seasons:[int], episodeCount}]` |
| GET | `/api/items/{id}?target=<profile>` | `ItemDetail` incl. `playback` (Decision + `url`) for built-in profile (default avplayer) |
| POST | `/api/items/{id}/playback` | body `{profile?: name, custom?: Profile, startSec?, audioIndex?, subtitleIndex?}` → `Playback` |
| GET | `/api/items/{id}/file` | direct play, `http.ServeContent` (Range/ETag), explicit Content-Type: mp4 `video/mp4`, mov `video/quicktime`, m4v `video/x-m4v`, mkv `video/x-matroska`, webm `video/webm`, ts `video/mp2t`, avi `video/x-msvideo` |
| POST | `/api/scan` | 202 `{started:true}` or 409 if running |
| GET | `/api/scan` | `{running, lastStarted, lastFinished, lastError, itemCount}` |
| GET | `/api/sessions/{sid}/index.m3u8`, `/api/sessions/{sid}/{file}` | (M3) HLS playlist / init.mp4 / segments |
| DELETE | `/api/sessions/{sid}` | (M3) stop session |
| GET | `/api/items/{id}/subtitles/{key}.vtt` | (M5) key = stream idx or `ext<N>` for sidecar; WebVTT |
| PUT | `/api/items/{id}/progress` | (M5) `{positionSec, durationSec}`; watched when ≥ 90% |
| GET | `/api/continue` | (M5) in-progress items, most recent first |

```
ItemSummary {id, kind, title, year, show, season, episode, episodeTitle, durationSec, container, videoCodec,
             audioCodec, width, height, hdr, addedAt, progress?: {positionSec, watched}}
ItemDetail  ItemSummary + {path(basename only), size, bitrate, streams:[Stream], subtitles:[{key, language,
             title, format, image:bool, url?}], playback: Playback}
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
- `POST /playback` with method remux/transcode creates a session: temp dir under `os.TempDir()/ply-sessions/<sid>`.
- **Server-generated complete VOD playlist** (`#EXT-X-PLAYLIST-TYPE:VOD`, `#EXT-X-ENDLIST`, exact EXTINF per
  segment) is returned immediately so players can seek anywhere:
  - remux (`-c:v copy`): segment boundaries = keyframes. Keyframe times extracted once via
    `ffprobe -v error -select_streams v:0 -skip_frame nokey -show_entries packet=pts_time,flags -of csv=p=0`
    (nice'd) and cached in DB table `keyframes(item_id PK FK CASCADE, times BLOB /*little-endian float64*/)`;
    group keyframes into segments of ≥ 6 s (target 6 s).
  - transcode: equal 6 s segments, `-force_key_frames expr:gte(t,n_forced*6)`.
- ffmpeg writes fMP4 segments: `-ss <segStart> -i <abs> -map 0:v:0 -map 0:<audioIdx> -c:v copy|<enc>
  -c:a copy|aac -b:a 192k -ac 2(if transcoding audio) -copyts`(as needed) `-f hls -hls_segment_type fmp4
  -hls_fmp4_init_filename init.mp4 -start_number <segIdx> -hls_segment_filename seg%05d.m4s
  -hls_flags temp_file` with `-hls_time`/segment times matching the playlist; ffmpeg's own playlist is written to
  `ffmpeg.m3u8` and used only to know which segments are complete. hevc copy → `-tag:v hvc1`.
  Output timestamps must line up with the server playlist (verify with ffprobe on two consecutive segments).
- **Seek** = a segment request outside [current encode start, encoded + ~3 segments] → kill ffmpeg and restart with
  `-ss` BEFORE `-i` at that segment's start, `-start_number <idx>`. A segment is served only when complete
  (listed in `ffmpeg.m3u8` or the next one has started), never just because the file exists.
- **Throttle**: when ffmpeg is > 20 segments (~120 s) ahead of the last requested segment, SIGSTOP it; SIGCONT
  when the client catches up to < 10 ahead.
- Playlist URL `/api/sessions/<sid>/index.m3u8?token=…`; when a token is configured, rewrite segment/init URIs
  in the playlist with `?token=`. Playlist `Cache-Control: no-cache`.
- `Playback.startSec` echoes the requested resume position (client seeks there); stream time 0 = file time 0.
- ffmpeg binary: prefer jellyfin-ffmpeg if found (`/usr/lib/jellyfin-ffmpeg/ffmpeg`,
  `/opt/homebrew/opt/jellyfin-ffmpeg/bin/ffmpeg`, `/usr/local/opt/jellyfin-ffmpeg/bin/ffmpeg`), else PATH;
  `-ffmpeg`/`-ffprobe` override. FFmpeg is always a subprocess, never cgo.
- Manager: max N concurrent sessions (4), idle timeout 60s since last request, kill process group, remove temp dir on
  stop and on server shutdown; startup removes stale `ply-sessions` dirs. **No orphan ffmpeg** — use
  `exec.CommandContext` + `cmd.Cancel` killing the process group (`Setpgid`) and `WaitDelay`. Test it.
- Transcode (M4): `internal/hwaccel` probes at startup by actually encoding 1 frame of `-f lavfi testsrc`:
  macOS `h264_videotoolbox`; Linux `h264_vaapi` (`/dev/dri/renderD128`), `h264_qsv`, `h264_nvenc`; fallback
  `libx264 -preset veryfast -crf 21`. Scale down to profile MaxHeight. `-force_key_frames expr:gte(t,n_forced*6)`.
  Burn-in subtitles via `-filter_complex [0:v][0:<subidx>]overlay`. 10-bit/HDR → `format=yuv420p` (tone mapping
  out of scope; note it in reasons).

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
6. Throttle via SIGSTOP/SIGCONT; idle kill 60 s; ffmpeg in its own process group; shutdown kills all and removes
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
