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

Transcode numbers arrive with M4.

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
