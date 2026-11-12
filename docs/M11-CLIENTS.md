# M11: the client contract (native apps, casts, the live feed)

The owner will build native apps for iOS, iPadOS, tvOS and Android, cast to Chromecast and AirPlay to an Apple TV,
all on the home network over plain HTTP. This file is the contract those clients are built against, written down
before the first of them (2026-11-12) so that the architecture does not have to change for any of them. What is
marked FUTURE is designed here and built later; everything else is the groundwork this milestone builds.

## Decisions
- **Push is SSE, and stays SSE.**
  - It is plain HTTP, so it runs on whatever version the connection speaks. With TLS, Go speaks HTTP/2 by itself,
    and the stream then shares one connection with everything else instead of holding one of the browser's six
    per host.
  - Every platform has a client: `EventSource` in browsers, `URLSession` on Apple platforms, `okhttp-sse` or Ktor
    on Android (see Platforms for what each needs).
  - `Last-Event-ID` replay is exactly what an app needs when it comes back from the background.
  - WebSocket was weighed (M9 "Now playing across devices") and is not used. It is not obsolete (SSE is the older
    of the two), but nothing here needs a second protocol beside HTTP.
  - WebTransport needs HTTPS and HTTP/3.
- **Pull and actions are plain HTTP requests.** Their status codes carry meaning (404 expired, 410 stopped, 409,
  503 at the limit), and `fetch(keepalive)` still works as a page goes away.
- **The server owns the truth about what plays.** A play is a lease kept alive by heartbeats and by the requests
  the server sees. It ends when it is stopped or when the lease runs out, never because a client failed to say
  goodbye.
- **Every URL a client follows is built by the server** (playback, segments, art, subtitles). A client knows only
  the base URL it found the server at (mDNS or typed), or the base of an AirPlay target it named (below). Those URLs
  carry what the server needs to know whose they are (the session, the token), because a media player sends no
  headers of the app's.

## Versioning
- **`api` is the version of the API, now 1. `apiMin` is the oldest version the server still serves.** Both are in
  `GET /api/info` and in the mDNS TXT record; the feed's `snapshot` has `api`. `v=1` stays the version of the TXT
  record's own layout. There is no `/api/v1` prefix.
  - A client built for version N works with a server whose `apiMin` ≤ N ≤ `api`. Otherwise it tells the person
    which of the two to update instead of guessing.
- **Additive changes do not bump it.** New endpoints, fields, event types, topics and end reasons can arrive at any
  time. A client ignores what it does not know, and treats an unknown end reason as "ended".
- **A breaking change bumps `api`.** `apiMin` moves only when the old way stops being served.
- **Errors are `{"error": "<message>"}`.** Clients switch on the status code. A machine-readable `code` field may
  be added later (additive).

## The live feed: `GET /api/events`
- `text/event-stream`, `retry: 3000`, a `: keep-alive` comment every 15 s.
- **Frames:**
  - `id:` is the event's sequence number, one counter for the whole server. It starts from the clock, so an id from
    an earlier run of ply is never mistaken for one of this run's.
  - `event:` is the type.
  - `data:` is one JSON object with one named field per payload: `{"job": …}`, `{"tier": …}`,
    `{"session": …, "reason": …}`, or `{}`.
- **Topics:** `?topics=jobs,library,sessions,tier` (comma-separated).
  - Without it, all of them.
  - A name the server does not know is ignored, so a list of only unknown names (a newer client asking an older
    ply for a later topic) gets nothing but the snapshot and the keep-alives.
  - Ids skip the events of other topics: gaps are normal.
- **Replay and the snapshot:** on reconnect the client sends `Last-Event-ID`.
  - If the server still keeps every event since then (a ring of the last 512), it replays those of its topics.
  - Otherwise it sends a `snapshot` first: `{api, jobs?, sessions?, tier?}`, with the sections of the topics asked
    for. `jobs` is what `GET /api/jobs` answers, and `sessions` what `GET /api/sessions` answers.
- **A snapshot may come at any time**, not only first, when the server's state changed in a way its events do not
  say.
  - Each of its sections replaces that state whole. A snapshot also means "fetch the library again".
  - Events are whole-state upserts (a job, a session), so one that repeats what the snapshot already said does no
    harm. An `updated` for a session the client does not have adds it; an `ended` for one it does not have is
    ignored.
- **Auth:** like every `/api/*` request.
  - Native clients send `Authorization: Bearer <token>`.
  - A browser's `EventSource` cannot send headers, so it uses `?token=`.
- **Device:** native clients send the `X-Ply-Device` header. A browser's EventSource adds `?device=<id>`.
- **Timeouts:** a client's read timeout must be longer than the keep-alive (15 s), or none: OkHttp's default is
  10 s.
- **A slow reader is dropped.** Its stream ends and it reconnects into a snapshot.
- **After repeated failures the web UI polls `GET /api/jobs`.** Native apps can do the same or simply keep
  reconnecting.

### Types
| Topic | Types | Payload |
|---|---|---|
| jobs | `job.queued`, `job.updated`, `job.held`, `job.started`, `job.progress`, `job.retrying`, `job.done`, `job.failed`, `job.cancelled` | `job` |
| jobs | `queue.paused`, `queue.resumed` | none |
| library | `library` (what the library shows changed: fetch again, after a quiet second) | none |
| sessions | `session.started`, `session.updated`, `session.ended` | `session` (as in `GET /api/sessions`), plus `reason` on ended |
| tier | `tier` (playback and browsing load) | `tier` |
| (all) | `snapshot` | `api` and the topics' sections |

### Session events
- **`started` and `ended` are published once per play**, whichever of the server's paths noticed the change.
  `started` always comes before its `ended`.
- **`updated` repeats.** It comes on a heartbeat or a change of pause, never on a timer. Between heartbeats a client
  works out the position from `positionSec`, `lastSeenAt` and `paused`.
- **`ended.reason`** is one of the following, and the list may grow:
  - `stopped`: on purpose, from any device;
  - `expired`: the lease ran out;
  - `error`: the play could not be set up;
  - `shutdown`: ply is stopping;
  - `replaced`: the same device asked to play the same item again, and a new play took this one's place.
  
  The server cannot see "finished": a client that needs it compares the position with the duration.
- **A session names the device that started it**: `device {id, name, kind}`, when the client sent one.

## Devices
- **Identity:** every client makes one stable id when it is first installed or opened and keeps it. It is
  `[A-Za-z0-9_-]{8,64}`, and a UUID is fine.
  - It travels as the `X-Ply-Device` header on the app's own requests (`?device=` only on a browser's
    EventSource).
  - An id that does not match the pattern is ignored.
  - Until pairing (below) the id is only a label: anyone on the network could send any id.
- **Registration:** `PUT /api/devices/{id}` with `{name, kind, app, version}`, at start and whenever any of them
  changes. It answers 204.
  - `kind` is an open string: `web`, `ios`, `ipados`, `tvos`, `android`, `androidtv`, …
  - `GET /api/devices` lists them, with `lastSeen`.
  - Both are private: same-site only, never opened by CORS.
- **`lastSeen` is written on registration and when a play starts**, never on every request.
- **Every play has a session id**, from `POST /api/items/{id}/playback`, direct plays (the file itself) included.
  - The URLs the server builds for the play carry it. So the requests of whatever fetches them (a `<video>`,
    AVPlayer, an Apple TV the play was handed to) count for that play, without headers.
  - The client sends heartbeats to it: `POST /api/sessions/{sid}/heartbeat {positionSec, paused, wireless?}`.
  - `wireless: true` says the video plays on another screen (AirPlay). The lease is then the paused one (3 min),
    since the target may buffer for a long time without asking for anything (docs/AIRPLAY.md).
  - The play records the device that asked for it (from its `X-Ply-Device`): `{id, name, kind}` as it registered,
    or, for one that never did, its id and the name of its User-Agent.
  - A request for the file that carries a `sid` counts for that play. One with a `sid` of a play that has run out
    since is counted as any request is: by its device, or by address and User-Agent for a client that names none.
  - A client that names its device is one client for its direct plays, whatever its address or program.
  - **Asking again replaces.** A `POST /playback` for an item the device already plays directly (another audio
    track, a retry, a second tab) starts a new play with a new id in the old one's place (it does not count twice
    against the limit). The old play ends as `replaced`, and its id answers as stopped (410). So a `DELETE` of the
    old id, which a player sends without waiting when it starts again, can never end the new play.
  - **The newest attempt wins.** A client sends `attempt` with each `POST /playback`: a whole number from 1 to
    2^53-1 (anything else is a 400) that it makes larger every time it asks, across restarts and from every
    window it has. The web UI sends the clock's milliseconds, made to go up even when the clock goes back, kept
    in localStorage. When one client sends two asks for one item at nearly the same moment and the server handles
    them in the other order, the older one, arriving last, is answered 410 `{"stoppedBy":"a newer attempt"}`
    and ends nothing; it is refused too after the newer play was stopped, and at the limit (410, not 503).
    - It orders direct plays (the only ones an ask replaces). A remux or transcode ask makes a session of its own,
      which the client deletes when it has moved on.
    - The client is the device, or for one that names none its address and User-Agent.
    - The newest attempt is kept 10 minutes (`DirectGrace`) from the last ask that was taken, so a client that
      starts again from 1 (reinstalled) waits that long at most. Without `attempt`, or with 0, what the server
      sees last wins, and the newest attempt stays as it was.
  - A direct play is held like a session: 30 s after its last heartbeat or request, 3 minutes while its last
    heartbeat said it is paused.
- **Cast and AirPlay targets are routes, not devices** (AirPlay targets below). The sender (the phone, the browser)
  keeps the play's lease with its heartbeats. When it can no longer do so, the requests of the target keep the lease
  alive while it fetches.

## AirPlay targets
- **Ply finds them on the LAN** with DNS-SD (`_airplay._tcp`), beside advertising itself as `_ply._tcp`: an
  Apple TV, a Mac that accepts AirPlay, or another receiver (docs/AIRPLAY.md, "Network setup").
  - `GET /api/receivers` lists them, private like the devices: `[{id, name, model, kind, address, port, base}]`.
  - `id` is the receiver's `deviceid` (its MAC address); `name` is the name it advertises.
  - `base` is the URL of this server on the receiver's own network (`http://192.168.4.196:8420`), the base of the
    URLs that receiver fetches. It is absent when the server is on no network of the receiver's.
- **A play may name one.** `POST /api/items/{id}/playback {"receiver": "<id>"}` answers with every URL on that
  receiver's network; the segments follow their playlist, which is relative to it. This is for any client, not only
  a browser: a native app that will hand the play to a TV asks for that TV.
- **An AirPlay play that names none is routed automatically** to the Apple TVs' network when they all share one and
  the request did not come in on it (a page opened at `localhost`, or on an Internet Sharing or VPN address). When
  the TVs disagree, the URLs stay on the page's address.
- **A receiver the server cannot route is refused**: 422 with the reason, or 400 for an id that is not there. A play
  is never given a URL that the target cannot fetch without saying so.
- **The web player offers them only when the choice matters**: the AirPlay button opens Safari's own picker
  directly when the play is already routed to the Apple TVs' network (the usual case), so a person picks their TV
  once. It lists the TVs by name first only when they are on more than one network, or on one the page is not on
  while ply could not route the play there; choosing a TV then starts the play again for its network, and the next
  AirPlay opening sends it there. Safari's picker needs the person's click, so those two cannot be one.

## Auth
- **The owner's credential is the shared token** (`-token`). An empty token means none is required, and then
  nothing below applies: every request is the owner's and URLs carry nothing. `/api/info` says whether one is
  (`authRequired`).
  - Native clients send a credential as `Authorization: Bearer`.
  - `?token=` is only for what cannot send headers: a browser's EventSource and media elements.
- **Three kinds of credential:**
  - **the shared token**, the owner's: the browser and scripts, as Bearer or `?token=`;
  - **a device token**, one per paired device, as Bearer only (never in a URL). The request is the device's: its
    `X-Ply-Device` is the token's, whatever the header says;
  - **a URL token**, which the server puts in the URLs it builds, only as `?token=`, only for a GET or HEAD.
- **What only the owner may do** (403 for a device token): pair and revoke, register another device, change the
  server (writes to `/api/settings`, `/api/folders`, `/api/queue`, `/api/jobs`, `/api/schedules`, `/api/scan` and
  `/api/metadata`; anything under `/api/fs`; `/api/settings/test`), and curate the library (a match, a refresh of
  an item's or a show's metadata, `PATCH /api/titles/{id}`, a subtitle file added or removed). A device may do the
  rest: browse, play, its plays and their heartbeats, progress and watched state, a subtitle downloaded, its own
  registration and the feed.
- **The checks read the path as the router matches it**: each segment unescaped (`%73ettings` is `settings`), a
  slash inside a segment kept as `%2F` (a show named "AC/DC" is one segment), so a URL token, the owner-only list
  and the private endpoints cannot be read one way and routed another. An `/api` path with an invalid escape or a
  `.` or `..` segment, encoded or not, is a 400.

### Pairing
- **The owner asks for a code:** Settings › Devices › Pair a device, `POST /api/pairing` →
  `{code, expiresAt, expiresIn}`. Six digits, good for 5 minutes (`expiresIn` seconds as the server answers, which a
  page counts down from, whatever its own clock says) and once. There is one at a time: asking again replaces it.
  The page reads the list again while it shows a code, and drops the code once a device has paired with it.
- **The person types it into the app**, which sends `POST /api/pair {code, id, name, kind, app, version}` without
  a credential. It answers `{token}` once, and registers the device as `PUT /api/devices/{id}` does. A wrong or
  expired code is a 403; after 5 wrong codes the code is spent, so guessing has 5 tries in a million.
- **The token is kept hashed** (SHA-256) in `devices`. Pairing a device again gives it a new token in place of the
  old one. `GET /api/devices` says which are paired (`paired`, additive).
- **Revoking** is the owner's `DELETE /api/devices/{id}/token` (Settings › Devices › Revoke). The device's token
  and every URL token made for it answer 401 at once, and what it has open ends (its feed); a file already being
  sent finishes. The device stays in the list, unpaired, and anyone else's credential is untouched.

### URL tokens
The URLs the server builds (playback, playlists and their segments, direct files, subtitles, art and images) are
followed by players that send no headers: a `<video>`, AVPlayer, a Chromecast or an Apple TV the play was handed to.
They used to carry the shared token, so revoking a device revoked nothing, and a URL handed to a TV held the
owner's credential for good. Two ways were weighed:
- (a) the requesting device's own token in its URLs: revoking works, but a URL that lands on a TV, in a receiver's
  state or in a log is still a long-lived credential;
- **(b), chosen: short-lived signed tokens.** A URL carries `token=u1.<device>.<expires>.<mac>`, never a secret:
  - `mac` is an HMAC-SHA256 (16 bytes, base64url) of the URL's scope, the device and the expiry, with a key made
    from a random secret in the data directory (`url-key`) and the shared token, so changing `-token` voids every
    URL token too;
  - the scope is the URL's own path, or for a playlist the directory it is in (`/api/sessions/<sid>/`: its
    segments and the playlists it names), told apart in the MAC: a token opens what its URL is for, nothing else;
  - it expires at the start of the UTC day after next, between 24 and 48 hours away, so that a URL stays the same
    all day (the browser's cache, the lists' ETags, which change with the day);
  - `<device>` is the device the URL was made for, empty for the owner. A token for a device that is no longer
    paired answers 401;
  - what plays (the playback URL, playlists and their segments, the file) is made for the requesting device.
    What describes an item (art, images, subtitle tracks) is made for no device: a list is the same for every
    device, and such a token opens that one picture or track alone, until it expires.
  A player that holds a URL for longer than that (a film paused for two days) gets a 401, and its client asks for
  the play again. A playlist fetched again signs its URIs anew, so a play that is followed renews its segments'
  tokens for as long as its session lives.
- **The web UI** keeps the shared token for its own requests (Bearer, and `?token=` on its EventSource) and follows
  the URLs the server gave it; `mediaUrl` adds the shared token only to a URL that has none.

## FUTURE: remote control
- **Commands go down the device's own feed:**
  - an event can be addressed to one device (`to`), and the hub filters it on subscribe;
  - they use a `commands` topic: `command.play {itemId, positionSec}`, `command.pause`, `command.resume`,
    `command.seek {positionSec}`, `command.stop`;
  - each carries `expiresAt`, and a device drops one that has expired. A TV that wakes up hours later must not
    start a film it was sent at lunchtime.
- **Pending commands are kept apart from the ring**, until they are acked or have expired, so the busy topics
  (`job.progress`) cannot push them out. A reconnecting device gets them replayed, or as the `commands` section of
  its snapshot.
- **Acks come back as plain requests:** `POST /api/devices/{id}/commands/{seq}/ack {ok, error?}`. The sender sees
  the outcome as `command.acked` on its feed.
- **The device's state is its session:** the heartbeats and `session.*` events above. There is no second channel.
- **Who a command is for** is the device of the token that subscribes, once pairing exists. Before that, remote
  control is limited to a network the owner trusts.
- **Nothing in this needs a new transport.** A command is one more topic and one more field on the event.

## HTTPS
HTTPS is what a Chromecast's HLS will need the day its receiver page blocks mixed content: the page is served
over HTTPS, and a fetch from it to `http://` is then refused (Platforms, Chromecast).
- **`-tls-cert` and `-tls-key`** (env `PLY_TLS_CERT`, `PLY_TLS_KEY`): PEM files of a certificate the owner
  already has. The listener then speaks HTTPS only, on the same address, and Go speaks HTTP/2 on it by itself:
  the feed shares one connection with everything else instead of holding one of the browser's six.
  - The files are read again when they change (looked at once a minute at most, on a handshake), so a renewed
    certificate is used without a restart. A pair that does not read yet (the key not written), or a certificate
    that is not for the host, leaves the one in use, and the log says so once, until the files change again.
  - A pair that does not load, or a certificate for no DNS name, stops ply at start.
- **The host** is the name clients connect to: `-tls-host` (env `PLY_TLS_HOST`), which must be one the certificate
  is for, else its first name that is not a wildcard. A wildcard certificate needs `-tls-host`.
  - Every name the certificate is for is an allowed host (DESIGN.md, `-allow-host`).
  - The banner and the log list `https://<host>:<port>` alone: an address is not one the certificate is for.
- **Discovery says how to connect.** The TXT record adds `tls=1` and `host=<host>` (additive: `v=1` and `api=1`
  stay). A client then connects to `https://<host>:<port>`, and to `http://` plus the address without them.
- **The URLs the server builds for a receiver name the host.** A receiver could not verify `https://<address>`.
  - A receiver's `base` (`GET /api/receivers`), and so the URLs of a play routed to it, is `https://<host>:<port>`
    when the host resolves to this server's address on the receiver's network (looked up at most once a minute;
    a name that did not resolve, after 5 s), and none otherwise: the owner's DNS points the name at one address,
    so on another network of ours it leads elsewhere. A play that names such a receiver is refused (422), as one
    on no network of ours is.
  - An AirPlay play from a page opened by an address (its certificate warning accepted) is routed to the host,
    since the URLs relative to that page are ones the TV cannot verify. A page opened by the host keeps its
    relative URLs.
  - Every other URL stays relative (`playbackFrom`), so it is on whatever base the client reached.
- **FUTURE: ACME** with a DNS-01 challenge (lego), for a name in a domain the owner controls that points at the LAN
  address, so the owner need not bring a certificate. A publicly trusted certificate does not mean the server is
  reachable from outside: only its name and certificate are public. This is how Plex does it (`*.plex.direct`).
- **Self-signed certificates and `.local` names are no use for a Chromecast.** It trusts neither the owner's own
  CA nor the router's DNS: it uses Google's DNS. An Apple TV needs a certificate it trusts too.
- **Clients follow the URLs they were given.** Nothing in the contract assumes `http`.

## Platforms
- **iOS, iPadOS, tvOS:**
  - **App Transport Security** allows plain HTTP to IP addresses, `.local` names and unqualified names with
    `NSAllowsLocalNetworking`. Other names, such as `ply.home.arpa` (which ply's host check accepts), are refused
    unless they get an exception of their own. Connect by the address that mDNS gives.
  - **Local network privacy** needs `NSLocalNetworkUsageDescription` and `NSBonjourServices` = `_ply._tcp`.
  - **Discovery** is `NWBrowser` for `_ply._tcp`. The TXT record gives `id`, `name`, `api` and `apiMin`.
  - **AirPlay receivers are the server's**: `GET /api/receivers` names the TVs and the base URL each can fetch from,
    and `receiver` on a play routes its media to one. The app does not browse for them itself, and AVPlayer hands a
    routed URL to the TV unchanged.
  - **The feed:** read `URLSession.bytes(for:)` and parse the SSE frames yourself. `AsyncBytes.lines` hides the
    blank line that ends a frame, so it cannot be used as it is. Use a session of its own, so the stream does not
    hold one of the session's connections to the host.
  - **In the background** iOS suspends the app. Close the feed on backgrounding and reconnect with `Last-Event-ID`
    on return.
- **Android:**
  - Cleartext HTTP is allowed in `network_security_config` with a `base-config`, because a domain-config cannot
    name IP ranges.
  - Discovery is `NsdManager`.
  - The feed is `okhttp-sse` with `readTimeout(0)` or a timeout well over 15 s.
  - Doze ends connections: reconnect with `Last-Event-ID`.
  - Newer Android versions add a local-network permission. Check it against the current documentation when the
    app is built.
- **AirPlay** (docs/AIRPLAY.md): the Apple TV fetches MPEG-TS HLS itself over plain HTTP. No HTTPS is needed.
  The default browser profile targets Apple TV 3rd generation and newer with H.264 High level 4.0,
  up to 1080p/30 fps and AAC-LC stereo 48 kHz. It re-encodes both selected streams because Safari
  cannot expose receiver capabilities. Older Apple TV software predates fMP4 HLS; a modern sender
  decoding fMP4 does not prove receiver support. Other profiles retain the existing fMP4 path.
- **Chromecast:** the receiver is a web page the Chromecast loads from a registered HTTPS URL (Google's Default
  Media Receiver, or a custom one). ply cannot serve it.
  - **Plain HTTP works, measured on 2026-11-12** (results below). The receiver page, served from
    `https://www.gstatic.com`, fetched ply's `http://` playlist and segments: the mixed-content block that was
    expected did not happen on this device and firmware. So HTTPS is not needed for casting today.
  - **What fails is 1080p over HLS.** The same film plays as a 1080p file and as 720p HLS, but not as 1080p HLS: the
    receiver fetches the playlist, the init segment and the first segment, then answers `LOAD_FAILED` (no detailed
    code).
  - **So, on this device, a play for a Chromecast is a direct file, or HLS at 720p at most.** The `chromecast`
    profile says so (`maxHlsHeight: 720`, beside `maxHeight: 1080`): a 1080p film it cannot play as a file is
    transcoded to 720p, not remuxed at 1080p (which is what failed).
  - Newer Chromecasts (Google TV) and newer firmware may block mixed content where this one did not, so the HTTPS
    listener stays the plan for the day one does.

### The Chromecast plain-HTTP test
The test is automated: `pychromecast` finds the Chromecast and casts. A logging proxy in front of ply, or a small
file server with CORS open, records every request it makes. The test reads the receiver's own player state, and
stops the play and closes the app when it is done. catt cannot test ply's HLS: ply's segments are fMP4, and the
Default Media Receiver reads HLS segments as MPEG-TS unless the sender sets `hlsSegmentFormat: fmp4`.

**Result, 2026-11-12.** A Chromecast ("Chromecast", CrKey 1.56.467165, stable channel), the Default Media
Receiver, ply on `http://192.168.4.196:8420`:

| What was cast | Played | What the Chromecast did |
|---|---|---|
| ply, direct file, H.264/AAC MP4, 1280 wide | yes | Range requests on the file over plain HTTP, no Origin |
| ply, direct file, H.264/AAC MP4, 1920×1080 | yes | |
| ply, HLS (`chromecast` profile: 1080p video copied, E-AC-3 to AAC), media playlist | no | fetched the playlist, `init.mp4` and `seg00000.m4s` (200, CORS `*`, Origin `https://www.gstatic.com`), then `LOAD_FAILED` |
| the same, behind a master playlist with `CODECS` | no | the same |
| Apple's fMP4 HLS sample, HTTPS | yes | |
| ffmpeg fMP4 HLS of a test pattern, 720p, over HTTP | yes | segment after segment |
| ffmpeg fMP4 HLS of the film (copied, muxed or separate audio, re-encoded, 44.1 kHz, 25 fps), 1080p, over HTTP | no | the playlist, the init segment, the first segment, `LOAD_FAILED` |
| the film re-encoded at 720p, ffmpeg fMP4 HLS over HTTP | yes | |

## Groundwork, built (2026-11-12)
1. This contract.
2. The event hub is `internal/events`, with topics. Jobs, the library, sessions and the tier publish to it. The
   feed has its envelope, `?topics` and a snapshot made of sections; `api` and `apiMin` are in the TXT record.
3. `api` and `apiMin` in `/api/info`.
4. `stream.Manager` tells every play's start, update and end (`OnPlayChange`, `internal/stream/changes.go`):
   `started` and `ended` once each, whichever path noticed, under the manager's lock so they keep their order.
5. `session.*` on the feed (`cmd/ply/main.go` connects the two). `PlayingEntry` names its device. The Activity page
   follows the feed for what plays, through its reconnects, and polls only when the feed is off.
6. Devices: migration 014, `PUT` and `GET /api/devices` (private), `X-Ply-Device` allowed by CORS.
7. A session id for every direct play (`StartDirect` at `POST /playback`), carried by the file URL the server
   builds (`DirectByID`). The web player already sends heartbeats to any session id it is given, and ends the play
   when it lets go of it.
8. The web UI makes its device id (`web/src/lib/device.ts`), registers it at start, and sends it with its requests
   and on the feed.
9. The Chromecast result, recorded above, and the `chromecast` profile's 720p limit on HLS.
10. AirPlay targets: a browse for `_airplay._tcp` (`internal/discovery`), `GET /api/receivers`, and `receiver` on
    `POST /playback`, with every media URL of the play on the receiver's own network and automatic routing for an
    AirPlay play (docs/AIRPLAY.md, "Network setup").
11. HTTPS (above): `-tls-cert`, `-tls-key` and `-tls-host`, the certificate read again when it changes
    (`internal/tlscert`), HTTP/2, `tls=1` and `host=` in the TXT record, and receiver URLs on the certificate's
    name.
12. Pairing and URL tokens (Auth, above): migration 016 (`devices.token_hash`, `paired_at`), `POST /api/pairing`,
    `POST /api/pair`, `DELETE /api/devices/{id}/token`, the owner-only endpoints, URL tokens in every URL the
    server builds (`internal/api/auth.go`, the secret in `<data>/url-key`), and Settings › Devices.
