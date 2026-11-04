# ply — measurements and implementation notes

## M3 measurements (DESIGN.md efficiency rule 9)

Machine: Apple M2 Max, 12 cores, 64 GB, macOS 27.2 (APFS, local SSD), Go 1.27.2, ffmpeg/ffprobe 9.0.2 (Homebrew).
Server: the real `ply` binary (`-scan-interval 0`, fresh data dir), driven over loopback HTTP by a script that
fetches like a player that never waits (init, then segments one after the other).

Fixture (generated, not committed): 60 s, 3840x2160 @ 24 fps, HEVC Main10 (`libx265 -preset ultrafast`,
`yuv420p10le`, keyframe every 2 s, closed GOP), E-AC-3 5.1 384 kb/s. 98.7 MB. Two copies in the library:
`Remux Test (2024).mkv` and `Direct Test (2024).mp4` (`-c copy -tag:v hvc1`). The avplayer profile is used, so the
mp4 is played directly and the mkv is remuxed into 10 segments of 6 s.

| Scenario | Result |
|---|---|
| Direct play, mp4 (`/file`, sendfile) | 98.7 MB in 31 ms (about 3.2 GB/s), first byte after 1.2 ms |
| Remux mkv, cold (first play: keyframe scan + ffmpeg start) | POST /playback 46 ms (keyframe scan 42 ms for 30 keyframes), first segment ready 136 ms after the POST |
| Remux, warm (keyframes cached in SQLite) | first segment 92 ms after the POST |
| Remux, far seek to segment 7 | 21 ms, but ffmpeg had already finished the whole clip, so it was served from disk; a real restart costs one ffmpeg start (about 70-90 ms) |
| Remux, the other 9 segments back to back | 88.9 MB in 219 ms (about 400 MB/s) |
| Remux with audio transcoded (E-AC-3 5.1 to AAC stereo, what Chrome gets) | first segment 174 ms, all 10 segments 0.82 s |

CPU and memory:

| Process | CPU | Peak RSS |
|---|---|---|
| ply server (all phases) | 10-20 % of one core while delivering segments, unmeasurable during direct play (sendfile) | 21-22 MB |
| ffmpeg, copy video + copy audio (whole 60 s file) | 81 ms CPU in 72 ms wall | 43 MB |
| ffmpeg, copy video + AAC audio | 995 ms CPU in 805 ms wall (about 1.2 cores) | 34 MB |
| ffprobe keyframe scan | 42 ms wall (full packet scan of the 98 MB file) | not sampled |

ffmpeg figures are rusage from the process itself (logged by the server as "ffmpeg stopped"); server figures are
`ps` samples every 50 ms, so they are coarse for phases shorter than a few hundred ms.

Caveats: the clip is short and the machine fast, so ffmpeg copies all 60 s in well under a second and finishes
before the first segment is even requested; the SIGSTOP throttle (more than 20 segments ahead) is therefore
covered by tests but not exercised here. The remux cost is dominated by disk/network throughput, not CPU: a
network share or a spinning disk will make the first segment and every seek proportionally slower, and the first
keyframe scan of a large file reads the whole file once (niced; cached afterwards).

Transcode numbers are in "M4 measurements" below.

## M4 measurements (DESIGN.md efficiency rule 9)

Same machine, server and fixture as M3 (the 60 s 4K HEVC Main10 mkv with E-AC-3 5.1, 98.7 MB), driven by a script
that fetches like a player that never waits: POST /playback, playlist, init, then segments 0..9 one after the
other. The profile is custom (`containers [mp4]`, `videoCodecs [h264]`, `audioCodecs [aac]`, `maxHeight 1080`), so
the server transcodes 4K HEVC 10-bit to 1080p H.264 High yuv420p capped at 8 Mbps, and E-AC-3 5.1 to AAC stereo
192k. Cold start, fresh data dir; one run each (the numbers are stable to a few percent).

| | h264_videotoolbox (`-hwaccel auto`) | libx264 (`-hwaccel none`, veryfast, crf 21) |
|---|---|---|
| Pipeline | VideoToolbox decode, `scale_vt`, VideoToolbox encode, no hwdownload | software decode, `scale`, `format=yuv420p`, x264 |
| Time to first segment (POST to segment 0 fully read) | 1.01 s | 1.12 s |
| All 10 segments | 6.99 s, **8.6x realtime** | 8.85 s, **6.8x realtime** |
| ffmpeg CPU | 2.3 s CPU in 6.9 s wall, **33 % of one core** | 78.8 s CPU in 8.7 s wall, **about 9 cores (908 %)** |
| ffmpeg peak RSS | 252 MB | 1172 MB |
| ply server CPU / RSS | 0.6 % of a core / 22 MB | 1.0 % of a core / 22 MB |

Notes: the first segment needs no keyframe scan (a transcode's segment boundaries are the forced keyframes, so the
M3 42 ms scan and the SQLite cache are not involved); the 1.0 s is ffmpeg start plus encoding the first 6 s of 4K.
ffmpeg figures: `ps` samples every 100 ms for CPU% and RSS, cross-checked with the rusage the server logs when
ffmpeg stops (`cpu=2.284s max_rss_mb=252` and `cpu=1m19.073s max_rss_mb=1172`). Throttling (SIGSTOP above 20
segments ahead) did not engage: the clip is only 10 segments. On this machine x264 is not far behind the GPU
path in wall time because 12 cores decode and encode in parallel; the difference is the cost: the same work uses
about 1/35 of the CPU time and 1/5 of the memory on VideoToolbox, which is what matters when a transcode runs
next to other work or on a machine with fewer cores. Both runs left no ffmpeg process behind.

## M3 implementation notes (ffmpeg behaviour that shaped the design)

These were verified against ffmpeg 9.0.2; the integration tests in `internal/stream` and `internal/api` fail if they
stop being true.

- **ffmpeg's HLS muxer cannot be trusted to cut where the playlist says.** With `-c copy` it cuts at the first
  keyframe at or after `start + n * hls_time`, with `start` the first timestamp of that run, so a restart after a
  seek cuts differently from the original run unless keyframes are perfectly regular. ply therefore runs the muxer
  with `-hls_time 0.001` (one fragment per keyframe, "GOP file") and builds each playlist segment by concatenating
  the GOP files of its keyframe range (dropping the repeated `styp`). Boundaries come only from the keyframe index.
- **Timestamps.** `-copyts -start_at_zero -avoid_negative_ts disabled -output_ts_offset 2` plus
  `-hls_segment_options movflags=frag_discont+skip_sidx+` makes the first fragment of any run carry its true
  decode time (`tfdt`) instead of 0, so segments from different runs line up on the file's timeline (+2 s, which
  keeps fragment decode times non-negative when the first DTS is negative, e.g. B-frames or AAC priming). The
  trailing `+` is required: hlsenc appends its own flags without a separator. The init segment is byte-identical
  across runs; the session serves the first one.
- **`-ss` before `-i` and the keyframe you land on.** ffmpeg moves the seek target back by 3/23 s when the video has
  frame reordering, so for mkv/mp4 the target is keyframe + 0.14 s. MPEG-TS cannot seek to a keyframe; it lands
  before the target and ffmpeg skips to the next keyframe, so for TS the target is keyframe - 0.05 s.
- **TS/ADTS AAC** needs `-bsf:a aac_adtstoasc` for the mp4 muxer.
- **Completeness.** With `-hls_flags temp_file` a GOP file only gets its final name when finished; ply additionally
  checks that the top-level boxes tile the file exactly before serving a byte.

## M4 implementation notes (transcode)

- **Segments are the forced keyframes.** A transcode has no keyframe index: the plan is a synthetic one with a
  keyframe every 6 s (`uniformKeyframes`), so each playlist segment is exactly one GOP fragment and the M3 machinery
  (one fragment per keyframe, concatenation, `-start_number`, `-output_ts_offset`) is reused unchanged. A tail
  shorter than 3 s is merged into the last segment (so the last one can be up to 9 s).
- **`-force_key_frames expr:gte(t,n_forced*6)` is wrong here.** `n_forced` counts from the first frame of the run
  and `-copyts` leaves source timestamps, so a run restarted at segment 100 (`t` = 600) would force a keyframe on
  every frame until `n_forced*6` caught up. ply uses `if(isnan(prev_forced_t),1,gte(t,floor(prev_forced_t/6+1)*6))`,
  which is relative to the previous forced keyframe and so works from any start.
- **The encoder must not add keyframes of its own**, or fragments and playlist drift apart for the rest of the
  session: libx264 gets `-g 100000 -sc_threshold 0 -forced-idr 1`, the others `-g 100000` (and `-forced_idr`/
  `-no-scenecut` where they have them). The integration tests assert exactly one keyframe per segment on both
  libx264 and VideoToolbox.
- **Seeks are exact for a transcode** (`-ss` = segment start, no keyframe bias): ffmpeg decodes from the source
  keyframe before the target and drops frames up to it, and the first frame at or after the target gets the forced
  keyframe, which is also what the continuous run does. `-fps_mode passthrough` keeps ffmpeg from duplicating or
  dropping frames to a constant rate after the seek. Measured alignment: segment N starts at exactly `6N + 2` s
  (the 2 s `TimelineOffset`) and ends 6 s later, to the millisecond, after a cold start, a far seek (segment 3 after
  0), the last segment and a seek back, for libx264 and VideoToolbox.
- **VideoToolbox end to end works** on this ffmpeg (9.0.2): `-hwaccel videotoolbox -hwaccel_output_format
  videotoolbox_vld`, `scale_vt=w:h` (no `format` option in this build, and none needed: the encoder takes the
  10-bit surface and writes High/yuv420p), `h264_videotoolbox`. Mean Y/U/V of the result match a software
  `format=yuv420p` conversion to within 0.1, so 10-bit SDR is not washed out. Without a resize there is no filter
  at all. VP9 and AV1 are not decoded on the GPU (support varies by generation): they decode and scale in
  software and still get a GPU encode; the reason says so.
- **HDR.** This ffmpeg has `tonemap` but not `zscale`, so HDR sources are converted by plain format conversion and
  the reasons say "colours will look washed out". The `zscale + tonemap` chain (hable) is only used when both
  filters exist; it is unit-tested for its text but could not be run here. Dolby Vision profile 5 would need
  libplacebo and is not handled.
- **Burn-in** (`-filter_complex`: scale the video, scale the subtitle canvas to it with `scale=rw:rh` (`scale2ref=w=iw:h=ih` on an ffmpeg whose `scale` takes no second input; never `main_w:main_h`, which is the canvas's own size), overlay, then
  the colour chain) is driven by `Decision.BurnSubtitle`. No tool here can author a bitmap (PGS/VobSub) subtitle,
  so the integration test uses a text subtitle stream as a stand-in: that proves the mapping, graph syntax and
  encoder plumbing but not that bitmaps render. Verify with a real PGS file when one is at hand.
- **GPU failure fallback.** `Request.Fallback` holds the libx264 spec; if the GPU ffmpeg dies by itself before
  the init segment was served, the session switches to it once (logged as a warning). After the init segment is
  out the encoder cannot change (the headers differ).
- **Not exercised on hardware:** VAAPI, QSV and NVENC. Their arguments are unit-tested as text and the probe logic
  is tested with a fake ffmpeg, but this host is a Mac. The chains follow the usual ffmpeg recipes
  (`scale_vaapi=format=nv12`, `vpp_qsv`, `scale_cuda`), and any failure falls back to software at runtime.
- `-hwaccel auto|none|videotoolbox|vaapi|qsv|nvenc`; a forced encoder still has to pass the 1-frame test encode.
  `-stream-idle` (default 60 s; since M9 a session whose player sends heartbeats is stopped 30 s after its last beat or request instead) replaces the old `PLY_STREAM_IDLE`; the variable now only changes the flag's
  default (tests and scripts), the flag wins.
- Also decided: an H.264 stream with more than 8 bits (High 10) is never copied, on any built-in profile.

## M5 notes (progress, resume, subtitles)

- **Progress rules** (`internal/store/progress.go`). `PUT /api/items/{id}/progress` stores `{position, duration}` in
  `progress`. Watched = position >= 90 % of the duration, or within 120 s of the end *and* at least half played (the
  half guard keeps a 90 s clip from counting as watched at second 5; for anything over 20 minutes the 90 % rule is the
  stricter one anyway). A position under 30 s that is not watched deletes the row, so "Play from start" and leaving at
  once clears a resume point; a watched item keeps its row. The server uses the client's duration, else the item's, and
  clamps a position past the end. The response is the resulting `{positionSec, watched}` or `null`.
  `updated_at` is unix nanoseconds so back-to-back reports order correctly. `GET /api/continue` = unwatched rows,
  newest first, limit 20, full ItemSummary (art included). `progress` is always present in summaries and details
  (`null` when there is none).
- **Player reporting** (`Player.svelte`). Every 10 s while playing, on pause, ended, leaving the page, `pagehide` and
  `visibilitychange` (iOS Safari often skips pagehide), with `fetch(keepalive)` (sendBeacon cannot PUT). Reports are
  gated by `armed`: nothing is sent until playback sits at the resume position, and `release()` disarms first.
  Unloading the media element resets the clock to 0 and fires `pause` afterwards, which would otherwise have wiped the
  saved position (found while designing it, covered by checking the server value after leaving in the browser pass).
  The library cache is patched in place (`applyProgress`) so cards update without a refetch.
- **Sidecar subtitles** (`internal/library/sidecar.go`). `<base>.srt|vtt|ass` and `<base>.<lang>[.forced|.sdh|.cc].<ext>`
  in the video's own directory, base and extension case-insensitive; anything else after the base (a second language,
  an unknown word, more than three parts) is not a sidecar. `hi` is Hindi, not "hearing impaired". They are listed on
  every scan even when the video is unchanged (the walk lists the directory once per folder, the store compares and
  writes only on a difference). Migration 002 adds `forced`. The key is `ext<N>`, N = position when ordered by path.
- **WebVTT** (`internal/subs`). One niced ffmpeg run per track (`-map 0:<idx> -c:s webvtt` for embedded, plain
  `-i file` for sidecars), cached as `<data>/subs/<id>-<key>-<mtime>.vtt` (sidecars add a path hash, because the list
  behind ext0 can change; their mtime is the sidecar's own). Concurrent requests share a run; a failure is remembered
  until the mtime changes (422 for the client). Embedded extraction reads the whole container once, so the first
  request on a big remux is slow (10 min timeout; the run outlives the request). Pruned after each scan with the
  artwork cache. ffmpeg exits 0 after dropping the rest of a non-UTF-8 `.srt` ("maybe missing -sub_charenc"), so
  sidecars that are neither valid UTF-8 nor UTF-16 with a BOM get `-sub_charenc WINDOWS-1252`.
  Text codecs: subrip, ass, ssa, mov_text, webvtt (and `text`/`srt`). Image codecs are 404 here.
- **Cue times need no offset.** The HLS timeline is the file's (stream time 0 = file time 0, see M3), so cue times from
  the VTT line up with `video.currentTime` for direct play and for sessions started at any `startSec`. Verified in
  Chromium: a remux session resumed at 45 s showed the cue for 40-90 s, and the burned-in clock in the picture matched.
- **Burn-in verified with a real bitmap subtitle.** ffmpeg cannot turn text into a bitmap subtitle, so
  `writeVobSub` in `internal/api/subtitles_integration_test.go` builds a VobSub pair by hand (an SPU packet with one
  RLE'd white 100x20 box, shown 2-4 s, in a PS/PES wrapper plus an `.idx`), ffmpeg muxes it into mkv as a genuine
  `dvd_subtitle` stream, and `TestImageSubtitleIsBurnedIn` decodes the first segment of a session and counts frames
  with a white box in the region: about 48 of the 144 frames of segment 0 on libx264 and on h264_videotoolbox (which falls back to software
  decode + overlay, as the reasons say), none without the subtitle. PGS itself still has no authoring tool here; the
  filter graph is the same for every bitmap codec.
- **Decide change**: an explicit `audioIndex` that is not the file's default track is no longer direct play (the file
  would play its default track whatever was asked); it becomes a remux, with a reason. The browser's audio menu relies
  on it.
- **UI**: text subtitle = a `<track kind="subtitles">` added/removed with the selection; bitmap = new playback with
  `subtitleIndex` at the current position; audio = new playback with `audioIndex` at the current position. Default
  selection is a text track flagged default, else forced; bitmap tracks are never auto-selected (that would turn a
  remux into a transcode unasked). Labels use `Intl.DisplayNames`.
- Not done: charset detection beyond "valid UTF-8 or Windows-1252", sidecars in other directories (`Subs/`), `.ssa`
  and `.sub/.idx` sidecars, remembering the viewer's subtitle language between items, PGS rendered with real PGS.
