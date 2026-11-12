# Browser AirPlay

Ply sends media from the server directly to Apple TV through Safari's native AirPlay route.
The browser supplies discovery, route selection and playback controls. Screen mirroring, a native
tvOS app and Chromecast are separate work.

## Browser and stream contract

Feature detection requires both `webkitShowPlaybackTargetPicker` and native HLS playback.
Unsupported browsers keep their measured browser profile and hls.js fallback. The AirPlay button
appears when Safari reports an available target, stays available while connected, and reflects
`webkitCurrentPlaybackTargetIsWireless`. Picker errors are shown without interrupting playback.
The picker is called synchronously from its click handler; no network request precedes it.

AirPlay-capable Safari uses the built-in `airplay` profile from initial playback, even before a TV
is selected. Safari cannot report the receiver model, and the source index does not establish
H.264 level or frame-rate limits, so the profile deliberately re-encodes both video and selected audio:
H.264 High level 4.0, up to 1080p at 30 fps, with AAC-LC stereo at 48 kHz / 192 kbit/s. This includes
an otherwise compatible MP4 and local Safari playback before choosing a TV. It avoids a format
switch after opening the synchronous native picker.

AirPlay uses MPEG-TS HLS (`segNNNNN.ts`, `video/mp2t`), with a version 3 media playlist and no
`EXT-X-MAP`/MP4 init. A text-subtitle master uses version 4 and derives codec and resolution attributes
from actual generated TS media through the configured, bounded ffprobe. Each TS segment is one
synthetic transcode GOP, with the existing file timeline and receiver session ownership. The ordinary
browser, AVPlayer and Chromecast profiles retain their existing fMP4 session path.

The compatibility target is Apple TV 3rd generation and newer. The older 2nd generation has a 720p
limit and is not covered by this 1080p profile; the original silver 2007 model is also out of scope.
[Apple TV 3 specifications](https://support.apple.com/en-ca/112429) define its H.264 and audio limits.
The previous fMP4-only implementation was insufficient for an AppleTV3,2 on software 8.4.4 that
reported a load error; MPEG-TS playback was subsequently confirmed on that receiver.
[FFmpeg HLS documentation](https://ffmpeg.org/ffmpeg-formats.html#hls-2)
notes that MPEG-TS works with all HLS versions, while fMP4 requires HLS version 7. Modern macOS
AVPlayer playback of the previous output did not establish compatibility with that older receiver.

Every play has an HLS session ID. Receiver requests can therefore come from a different IP and
User-Agent without registering a second direct play or bypassing the selected audio stream.
The browser uses the returned server URL as `video.src`; no MediaSource blob is transferred.

- Audio selection keeps the current file position and play/pause intent, replaces the session,
  and requests the chosen stream.
- Text selection sends `subtitleKey` (the embedded stream key or `extN` sidecar key). It replaces
  the HLS session with a master playlist containing a subtitle rendition. The TV fetches the
  WebVTT through the session, with a timestamp map matching the media timeline.
- Off omits both subtitle selectors. A bitmap subtitle uses `subtitleIndex` and server burn-in.
- Resume and seek use the file timeline; progress is reported from the same native media element.
- Playlist, segment and subtitle URLs retain configured query-token authentication. Receivers
  are not expected to send the browser's Authorization header.

## Ownership and lifetime

App keeps the actual Play/Player/video subtree mounted while Safari reports wireless playback.
Closing the fullscreen player navigates back within ply without changing its media element or
source. A compact controller offers Controls (reopen the same player) and Stop AirPlay. Hidden
player keyboard shortcuts do not consume the library's keys. Choosing another item replaces
the old play; disconnecting AirPlay while browsing tears down the hidden local player.

A wireless pagehide reports progress and sends a heartbeat without pausing, unloading or deleting
the stream. A restored page reuses the route. Explicit Stop and normal local-player cleanup still
pause/unload the media and DELETE the session. Errors and a remote stop clear wireless ownership.

A heartbeat can include `wireless: true`. It allows a bounded receiver inactivity window while
Safari's timers are suspended or the TV has buffered ahead; receiver media requests refresh activity.
Wireless extends the current inactivity window to at least `PausedBeatTimeout` (default 3 minutes,
capped at 5 minutes). After a heartbeat the normal window is the smaller of `stream-idle` and the
heartbeat timeout, extended by this grace. An outstanding subtitle HTTP response holds the session
until it completes or its request is canceled. `wireless: false` returns to the ordinary local policy.
No session is leased indefinitely merely
because it once used AirPlay. Long paused playback can expire without controller heartbeats.

This is continuity within a living Safari document. Safari or the OS can terminate a route when
the tab is closed, the document is reloaded, or the app is evicted; ply cannot guarantee playback
after those events. If the server expires or restarts a session, the open player uses its existing
bounded recovery and resumes at its last observed position. Progress while the controller is
suspended may lag the TV; receiver segment positions provide activity, not exact watch history.

## Network setup

Open ply using an address reachable by both Safari and the TV, on a network that permits AirPlay
discovery and media access. The browser and the TV must agree on the address: AirPlay sends the TV a
media URL relative to the page, so a page served on one subnet tells a TV on another to fetch an
address it cannot reach. A Mac with more than one network (Wi-Fi and Ethernet, Internet Sharing, a
VPN) advertises several addresses; use the interface on the TV's subnet, not a bridge or tunnel
address. A server's localhost or 127.0.0.1 address is local to each device and is not a usable TV
media address. The TV must also trust any HTTPS certificate in use. Ply does not discover or
configure the home network, install certificates, or expose the server to the internet.

## Verification and device acceptance

Automated checks emulate Apple's availability/wireless events and picker in a real browser with
real decoded media. They test profile choice, native URLs, subtitles/audio requests, seeking,
node/session identity while browsing and returning, pagehide/restore, disconnect and explicit stop.
Run the focused browser checks with `make e2e-airplay`; `make e2e-web` includes them with the broader
browser suite. Unit tests cover unsupported browsers, event cleanup and synchronous picker failure. Server tests
cover receiver identity, authentication, subtitle manifests/timing and bounded lifetime.

Emulated events are not evidence of Apple's discovery, firmware or rendering behavior. Before
release, run this hardware checklist in Safari on macOS and iPhone/iPad with an actual Apple TV:

1. Open the server's address on the TV's subnet; select a TV and confirm the TV renders the media.
   The Activity page and `GET /api/sessions` show the active play and its last activity; individual
   media requests are not access-logged by default.
2. Play H.264/AAC, HEVC/4K conversion, multichannel-to-stereo audio and an alternate audio track.
   Include an Apple TV 3rd generation receiver; modern macOS AVPlayer alone is not an acceptance test.
3. Resume, seek forward/backward, pause/resume and verify reported progress.
4. Select embedded and external text subtitles, Off, and bitmap burn-in; confirm TV cue sync after seeking.
5. Browse ply, reopen Controls, use lock-screen controls and explicitly Stop AirPlay.
6. Lock/background the phone with enough media buffered to exceed the ordinary local timeout.
7. Test a long pause, network loss, remote Stop, server restart, and recovery without duplicate plays.
8. Verify documented limitations for reloading/closing Safari and OS eviction; do not label these as
   guaranteed autonomous receiver sessions.
