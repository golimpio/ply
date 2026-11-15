# M11: the client contract (native apps, casts, the live feed)

The owner will build native apps for iOS, iPadOS, tvOS and Android, cast to Chromecast and AirPlay to an Apple TV,
all on the home network, over plain HTTP or HTTPS (below). This file is the contract those clients are built
against, written down before the first of them (2026-11-12) so that the architecture does not have to change for
any of them. What is marked FUTURE is designed here and built later; everything else is the groundwork this
milestone builds.

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
- **Topics:** `?topics=jobs,library,sessions,tier,commands` (comma-separated).
  - Without it, all of them.
  - A name the server does not know is ignored, so a list of only unknown names (a newer client asking an older
    ply for a later topic) gets nothing but the snapshot and the keep-alives.
  - Ids skip the events of other topics: gaps are normal.
- **Replay and the snapshot:** on reconnect the client sends `Last-Event-ID`.
  - If the server still keeps every event since then (a ring of the last 512), it replays those of its topics.
  - Otherwise it sends a `snapshot` first: `{api, jobs?, sessions?, tier?, commands?}`, with the sections of the
    topics asked for (`commands` for a feed that names its device). `jobs` is what `GET /api/jobs` answers, and
    `sessions` what `GET /api/sessions` answers.
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
| commands | `command.play`, `command.pause`, `command.resume`, `command.seek`, `command.stop` (to one device); `command.acked` (to its sender) | `command`; `ack` (Remote control, below) |
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

## Remote control
One device asks another to play, pause, resume, seek or stop: a phone driving the TV.
- **Sending:** `POST /api/devices/{id}/commands {type, itemId?, positionSec?, ttlSec?}` → 202 `{seq, expiresAt}`.
  - `type` is `play` (needs `itemId`; `positionSec` is where to start), `pause`, `resume`, `seek` (needs
    `positionSec`) or `stop`; anything else is a 400, an item that is not there a 404.
  - The target is a device that registered (404 otherwise). The sender is the request's device (a paired one's
    own), named in the command as `from`; any credential may send, a device's included.
  - A command lives `ttlSec` (default 30, at most 600) and carries `expiresAt`. A device drops one it gets after
    that: a TV that wakes up hours later must not start a film it was sent at lunchtime.
- **Commands go down the device's own feed:** an event can be addressed to one device, and the hub sends it to
  that device's subscribers alone (`GET /api/events` subscribes as the request's device, `?device=` or a paired
  device's own). The topic is `commands`, the types `command.<type>`, the payload `{"command": {seq, type,
  itemId?, positionSec?, from?, expiresAt}}`; `seq` is the event's id.
- **Pending commands are kept apart from the ring**, until they are acked or have expired, so the busy topics
  (`job.progress`) cannot push them out; 32 a device at most, the oldest going first. A reconnecting device gets
  them replayed by seq, in order with the rest, or, when the ring no longer reaches back, as the `commands` section
  of its snapshot.
- **A device acts on a `seq` once.** The same command can come again: in a snapshot (its `commands` are the ones
  still waiting, not new ones), in a replay after a snapshot, and live in the moment a feed opens. `play` is not
  idempotent, so a TV remembers the seqs it has acted on, and acks each.
- **Acks come back as plain requests:** `POST /api/devices/{id}/commands/{seq}/ack {ok, error?}` (the body is
  required) → 204, or 404 for a command that is not that device's to answer (acked already, expired, another's).
  A paired device acks only its own (403). The sender sees the outcome as
  `command.acked {"ack": {seq, device, ok, error?}}` on its feed; a command sent with no device (a script) is
  answered to every feed of the topic.
- **The device's state is its session:** the heartbeats and `session.*` events above. There is no second channel.
- **Who a command is for** is the device of the subscribing token once it is paired; on a server without a token,
  the `?device=` the feed names, so remote control there is for a network the owner trusts.
- **The web UI does not act on commands yet**: it is the native apps' (the TV's). Nothing in this needed a new
  transport: a command is one more topic and one more field on the event.

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
- **Browser:** the web client measures its own profile and sends it whole, audio channels included: it says
  `maxAudioChannels: 6`, because Chrome's demuxer refuses an 8-channel (7.1) AAC stream while its capability
  APIs claim it plays (measured 2026-11-13, Chrome 157, macOS: native HLS and Media Source both fail the
  segment). The server then copies the video and downmixes the audio to stereo; a native player keeps 7.1.
- **AirPlay** (docs/AIRPLAY.md): the Apple TV fetches MPEG-TS HLS itself over plain HTTP. No HTTPS is needed.
  The default browser profile targets Apple TV 3rd generation and newer with H.264 High level 4.0,
  up to 1080p/30 fps and AAC-LC stereo 48 kHz. It re-encodes both selected streams because Safari
  cannot expose receiver capabilities. Older Apple TV software predates fMP4 HLS; a modern sender
  decoding fMP4 does not prove receiver support. Other profiles retain the existing fMP4 path.
- **Chromecast:** the receiver is a web page the Chromecast loads from the HTTPS URL registered for its app id
  (Google's Default Media Receiver, or a custom one). ply does not serve it, even with HTTPS: a custom receiver has
  one URL per app id, registered with Google, so it lives once on a host of its own (a static site), not on each
  owner's server. Either receiver plays the URLs ply gives the sender, with their URL tokens (Auth): it sends no
  headers, and needs none.
  - **Plain HTTP works, measured on 2026-11-12** (results below). The receiver page, served from
    `https://www.gstatic.com`, fetched ply's `http://` playlist and segments: the mixed-content block that was
    expected did not happen on this device and firmware. So HTTPS is not needed for casting today.
  - **What fails is 1080p over HLS.** The same film plays as a 1080p file and as 720p HLS, but not as 1080p HLS: the
    receiver fetches the playlist, the init segment and the first segment, then answers `LOAD_FAILED` (no detailed
    code).
  - **So, on this device, a play for a Chromecast is a direct file, or HLS at 720p at most.** The `chromecast`
    profile says so (`maxHlsHeight: 720`, beside `maxHeight: 1080`): a 1080p film it cannot play as a file is
    transcoded to 720p, not remuxed at 1080p (which is what failed).
  - Newer Chromecasts (Google TV) and newer firmware may block mixed content where this one did not. The HTTPS
    listener (above) is for the day one does: with a publicly trusted certificate, the URLs are `https://<host>`.

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
13. Remote control (above): addressed events and the `commands` topic in `internal/events` (pending commands kept
    apart from the ring, replayed by seq), `POST /api/devices/{id}/commands` and `.../commands/{seq}/ack`, and the
    `commands` section of the snapshot.

## The apps (`ios/`, 2026-11-12; iPhone, iPad and Apple TV)
The first native clients: SwiftUI, iOS 17 and tvOS 17 and later. Everything they need to build is in `ios/`
(XcodeGen's `project.yml` and a Makefile: `make`, `make test`, `make app-test`, `make tv-test`, `make check`,
`make ci-build`, `make run`, `make tv`, `make tv-run`, `make device TEAM=<team id>`), so they can move to a repository
of their own. The generated `Ply.xcodeproj` is not committed. Xcode 26 or newer (Swift 6.2) and XcodeGen are
required; the Makefile checks both. The app-hosted tests need a released Xcode (27.2 works): the Xcode 27.0 beta
started the test host suspended and never resumed it, so a run hung until it timed out.
- **Connecting:** `NWBrowser` for `_ply._tcp`, then the IPv4 address mDNS resolves (`https://<host>` with `tls=1`),
  or an address typed (`http` or `https`, port 8420 when none is given). `api` and `apiMin` of `/api/info` are
  checked first: a server to update and an app to update are told apart, and only an unreachable one has "try again".
  A service seen on several interfaces is listed once.
- **Pairing:** when `authRequired`, the six-digit code; the device token is kept in the Keychain by server id, and a
  401 goes back to pairing. The app registers as `kind` `ios`, `ipados` or `tvos`, `app` `ply-ios` or `ply-tvos`.
- **Browsing:** Home (Continue Watching, Coming Up, the newest films), Movies (sort and 4K/HDR/unwatched
  filters), Shows (sort by name, newest file, rating or episode count; unwatched only; a whole season or show
  can be marked watched), and Browse: a search over films, shows, episodes, people, genres and collections,
  ranked by the web's own matcher (`Core/Fuzzy.swift`, ported from `web/src/lib/fuzzy.ts`: accents folded in
  both directions, a word start and a run of characters ahead of a scattered match), a People page over the
  server's search (`GET /api/people?q=`, the only list of people it has), the collections and genres
  themselves, a title's page (cast and crew, trailers, its other versions, collection links), a person's page
  (library credits and filmography), a genre's and a collection's titles, a show's page; watched state. The
  lists load together, except the episodes — the search's, and a show's newest file — which are fetched once,
  when Browse opens or Shows sorts by newest, never with Home.
- **Playing:** the `avplayer` profile in AVKit's own player, with `attempt`, a heartbeat every 10 s (`wireless`
  while AirPlay is on), progress once the stream has reached where it starts, a new session where it was after a
  404, and an end after a 410. The audio track can be chosen; a text subtitle is drawn from its WebVTT, a bitmap one
  burned in (`subtitleIndex`); a text subtitle still downloading says so, and one that failed says that. An
  episode that ends starts the next distinct episode (several files of one episode count once), and the lock
  screen and remote controls show the play and act on it.
- **AirPlay:** external playback switches to the `airplay` profile when the player's own AirPlay button connects,
  and back when it ends, so a text subtitle is the TV's `subtitleKey` rendition rather than a phone overlay. The
  targets of `GET /api/receivers` whose `base` is set are also offered as "Play on Apple TV", which asks for that
  profile with `receiver` up front and routes the URLs to that TV's network. (Compiled, not exercised on hardware.)
- **Picture in Picture:** on iOS. The player leaving the screen for the PiP window is not a close; restoring brings it
  back, and closing the PiP window ends the play. (Compiled, not exercised here: it needs a device.)
- **The feed:** `library` and `commands`, parsed byte by byte off the main actor; closed in the background and
  resumed with `Last-Event-ID`. Remote-control commands are acted on once and acked; the newest seq is remembered
  across launches, so one performed but not acked is not performed again.
- **Apple TV:** the same core, feed, player and pages as the phone, with the TV's focus navigation (`AppShell.swift`
  and the shared views). The cards are a 10-foot size (`posterColumns`, `shelfCardWidth`, `genreTileWidth` and
  `shelfSpacing` in `Components.swift`: 220 pt posters, 240 pt shelf cards at a 28 pt gap), and both card kinds carry
  the focus effect (`hoverEffect`); the phone keeps its own sizes. Its icon is a layered `AppIcon.brandassets`
  (`PlyTV/Assets.xcassets`): a drawn background gradient behind a drawn front play mark and wordmark at 1x and 2x, so
  it parallaxes without a design tool, with `Top Shelf Image` and `Top Shelf Image Wide` beside it. `make tv-run`
  installs it on a tvOS simulator. Picture in Picture is iPhone/iPad only.
- **Accessibility and localization:** a card is one element to VoiceOver — its title, then its caption and its
  watch state in words (`PosterCard`, `LandscapeCard`, `BrowseRow` via `accessibilityElement(children: .ignore)`,
  an episode row by `children: .combine` + its watch state), with the picture, the watched badge and the progress
  bar behind it hidden; a filter chip says whether it is on (`isSelected`). The strings written as
  `Text`/`LocalizedStringKey` are in `Ply/Localizable.xcstrings`, a String Catalog the toolchain extracts
  (`SWIFT_EMIT_LOC_STRINGS`; `xcodebuild -exportLocalizations` lists them, 85 today) and both apps ship. Plain
  Swift `String` values are not extracted — alerts, sort names, the facts lines (`Format.runtime`) — so a new
  language needs those moved into the catalog first; the apps are English-only until a language is added. Still to
  come: the VoiceOver audit of every page and the Dynamic Type pass at the largest sizes.
- **iPad:** several windows at once (the generated scene manifest allows them). `RootView` gives each window its
  own `PlaybackScene` — its own AVKit presenter and its own hold on the play — while one `AppModel` keeps the single
  connection, feed, remote commands and play; a play started in one window ends the one in another where it was
  shown, and a remote command's play lands in the window that started the last one, or the newest to appear. The
  presenter resolves its window from the scene (`PlaybackScene.windowScene`, handed over by a `WindowSceneReader` in
  `RootView`), never from whichever window UIKit calls active — and never guesses between two. Closing a window
  ends its play there: the scene's disconnect notification (the one UIKit always sends) and the view leaving the
  window both call `AppModel.unregister`, which closes the player, sends its progress and forgets the presentation
  without dismissing into a window that is gone. `PlaybackPresentationTests` and `PlaybackWindowsTests` pin the
  per-window state and the window registry; two windows at once cannot be driven from a terminal.
- **Tests and CI:** `make test` runs the Foundation-only core on this Mac as a macOS logic test: no simulator and no
  test host, so it is quick and runs anywhere. It covers the SSE and WebVTT parsers, RFC
  3339 and path handling, the API client's requests and answers (with a URLProtocol stub), the models the server
  sends, the AirPlay play plan, next-episode selection, connect-failure classification, command history, discovery
  dedupe, the feed's own reconnect with `Last-Event-ID`, the search matcher (the web's own fixtures, so that both
  clients rank a search alike), a show's newest file, that the episodes are fetched once, when a page asks, not
  with Home (`LibraryModelTests`), the window registry and a remote play's outcome (`PlaybackWindowsTests`), and a
  window's hold on the play (`PlaybackPresentationTests`). What only the built app can show is in `PlyAppTests`,
  hosted in the iPhone/iPad app (`make app-test`, on `SIM`, the iPhone 18 Pro by default) and in the Apple TV app
  (`make tv-test`, on `TVSIM`, the Apple TV 4K by default): that its Info.plist lists the Bonjour type `Discovery`
  browses for, asks for the local network, and allows plain HTTP to it. They are the test action of the `Ply` and
  `PlyTV` schemes, so Xcode's own test runner finds them too; `make check` runs all three suites. A simulator name is
  matched exactly, on the newest runtime that has it; when the default devices are missing (an older Xcode), the
  newest iPhone and Apple TV are used, while a name given as `SIM=`/`TVSIM=` must exist. Agents run these through
  plumb (`ios-test`, `ios-app-test`, `ios-tv-test`, `ios-check`, `ios-build`). CI runs the core tests and a generic
  iOS and tvOS build with Xcode 26+ (`make ci-build tv`).
- **Xcode's tools for agents:** Xcode serves its own tools over MCP (`xcrun mcpbridge`, 27.2 here): build, run
  the tests of the active scheme, render a SwiftUI preview, read the build log and a file's diagnostics, run the app
  and read its console. An agent opens `ios/Ply.xcodeproj` (`make project` first, so it matches `project.yml`), and
  the first time it does, Xcode asks the owner to approve that agent and the folder. The project is generated, so
  nothing is changed through Xcode's file and project tools: `project.yml` and the sources are edited, and the project
  is generated again. The rules for agents are in `AGENTS.md`.
- **Distribution:** the apps ship as `com.ply.ios` and `com.ply.tvos` (`MARKETING_VERSION`/`CURRENT_PROJECT_VERSION` in
  `project.yml`), with the icon family (light, dark and tinted in `Ply/Assets.xcassets/AppIcon.appiconset`, the TV's
  layered brand assets), a launch screen in the app's own velvet (`UILaunchScreen` in `Ply/Info.plist`,
  `LaunchBackground`), the privacy manifest (`PrivacyInfo.xcprivacy`), and `ITSAppUsesNonExemptEncryption` false.
  `make device TEAM=<team>` builds a signed device binary, but this machine has no Apple ID in Xcode and no
  provisioning profile for `com.ply.ios`, so that step and TestFlight need the owner's account. The checklist, in
  order: an App Store Connect app record for each bundle id; the team in Xcode's Accounts (or `make device TEAM=`);
  a first signed archive (Product > Archive, or `xcodebuild -exportArchive`); TestFlight with an internal tester;
  then the store listing (screenshots at the required sizes, description, privacy answers drawn from the privacy
  manifest, and tvOS's own listing). Nothing here can be done from a terminal without that account.
- **Not in the apps:** owner-only administration (pair/revoke, settings, folders, queue/jobs, scanning, metadata
  editing/matching, subtitle search/download) stays in the web UI; a paired device's token gets 403 by this
  contract. Chromecast, Android, watchOS and visionOS are separate clients, not this app.
- **Not yet:** AirPlay and Picture in Picture on real hardware.

## Offline downloads — an assessment (2026-11-15, not built)
The apps can browse, search and play over the network, and nothing more: there is no download, no offline library
and no queue of pending progress. What the server has today is enough to *move bytes* — `GET /api/items/{id}/file`
answers `Range` and `ETag`, so a client can fetch and resume a direct-playable file, and sidecar subtitles are
`/api/items/{id}/subtitles/{key}.vtt` — but not enough to promise a download that works:
- **A plan, not a URL.** A downloader needs the exact byte count, a checksum to verify a resumed file, and the
  sidecar subtitles that go with the item. `size` is on the summary, but there is no plan endpoint and no checksum,
  and a resumed transfer cannot be trusted without one.
- **Portable copies.** A file that only remuxes or transcodes (HEVC 10-bit, TrueHD) plays through a live HLS session
  that dies on idle; saving it means either storing the segments as a private format or asking the server for a
  portable encode. Neither exists, and `/playback` is built for now, not for a queue.
- **Watch state offline.** Playback offline would write progress and watched marks with nowhere to send them: the
  client needs a durable outbox and the server's `PUT /progress` and `PUT /watched` are idempotent enough to replay,
  but the client has no such queue today.
- **Storage and eviction.** The library is personal, so there is no licence to enforce; the policy is the person's
  disk budget and their choice of what to keep, which is a UI and a storage budget rather than a server problem.
Recommendation: a server milestone first (a download plan with size and checksum, a portable-copy endpoint, and
subtitle sidecars in the plan), then the client (background `URLSession`, a storage budget, an offline badge in the
library, and the progress outbox). Building a client downloader against `/file` alone would silently fail on exactly
the files a phone cannot direct-play.

