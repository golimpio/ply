# Review: `internal/art` and `web/` at commit 1282ff3

Scope: `internal/art` (Go) and everything under `web/src`, `web/public/sw.js`, `web/public/manifest.json`,
`web/vite.config.ts`, `web/index.html`. Spec: `docs/DESIGN.md`.
Reviewer worked on a read-only `git archive` of 1282ff3; nothing in the repo was changed except this file.

Result: **2 BLOCKING, 10 SHOULD.**

| ID | Sev | Title |
|---|---|---|
| W1 | BLOCKING | Direct-play resume ignores the resume position, then the 10 s ticker deletes the saved progress |
| W2 | BLOCKING | After a fatal player error, leaving the page deletes saved progress and Retry restarts from the wrong place |
| W3 | SHOULD | A failed `import('hls.js')` leaves the player on "Preparing stream..." forever |
| W4 | SHOULD | `pagehide` handler tears the player down on bfcache entry; restore shows a dead player |
| W5 | SHOULD | Phone layout: player control row overflows and the Full screen button is clipped |
| W6 | SHOULD | Player key handler hijacks Space on focused buttons and Ctrl/Cmd chords while the scrubber is focused |
| W7 | SHOULD | Auto-hidden player controls stay focusable and do not reappear on Tab |
| W8 | SHOULD | Opening an item detail page starts and kills an ffmpeg session (remux/transcode) on every visit |
| A1 | SHOULD | ffmpeg grabs are not cancelled on shutdown and orphan their process groups |
| A2 | SHOULD | Timeouts and other non-`ErrNoFrame` failures are never remembered: every request re-runs ffmpeg for up to 30 s |
| A3 | SHOULD | Every ffmpeg failure is `ErrNoFrame` and is remembered until restart (unmounted share = no art until restart) |
| A4 | SHOULD | Test gaps: 10 of 18 quick mutants in `internal/art` survive the default test suite |

## How this was checked

- `go vet ./internal/art/` clean (also `GOOS=linux`, `GOOS=windows`). `go test -race ./internal/art/` pass; `-race -count=15` pass;
  `go test -race -tags=integration ./internal/art/` pass.
- `npm ci && npm run check` (0 errors, 0 warnings) and `npm run build` pass.
- Built the real server from the snapshot (`-token secret`) against a lavfi fixture library (VP9/Opus webm for direct play;
  H.264 mkv with two audio tracks + sidecar srt for remux; a show named `Who's [Next]? & 100% #1`). Drove the UI with
  Playwright (own isolated Google Chrome 156, headless, scripts in the scratchpad `pw/` dir).
  The Playwright MCP browser was locked by another session, so I used `playwright-core` directly.
- Chrome 156 plays HLS natively, so hls.js is not used by default. Where hls.js behaviour mattered I forced it by overriding
  `HTMLMediaElement.canPlayType` for `application/vnd.apple.mpegurl` (blob: `currentSrc` confirms hls.js was active).
  Playwright's service-worker interception hides `route()` aborts for `/assets/*`, so W3 was run with `serviceWorkers: 'block'`.
- All servers/browsers started for this review were killed; one deliberately orphaned `sleep 300` (A1 repro) was killed by hand.

Checked and found fine: no `{@html}` / `innerHTML` / `eval` anywhere (titles are text-interpolated); every media, HLS segment, subtitle
and art URL carries `?token=` (observed on all `/api/sessions/*` requests) and the token never enters the hash route; the SW never touches
`/api/*` and serves `index.html` network-first; hash router and search palette round-trip `Who's [Next]? & 100% #1`, `[`, `?`, `%`, `#1`
(deep link + reload + palette Enter all land on the right page); hls.js instance is destroyed and the session DELETEd on every exit path I
exercised (leave, restart, fail, route change); art file names come only from an int64 id, a validated `Kind` and an int64 mtime;
art Prune only deletes inside the dedicated `<data>/art` dir; ffmpeg's process group is killed on context cancel (integration test passes).

---

## BLOCKING

### W1. Direct-play resume ignores the resume position, then the ticker deletes the saved progress

- **Where:** `web/src/components/Player.svelte:269-273` (`onLoadedMetadata`) and `:135-140` (`onTimeUpdate`) both read
  `playback?.startSec`. Server side: `internal/api/handlers.go:206-207` sets `pb.StartSec = 0` for direct play
  ("the client seeks itself"), contradicting DESIGN.md ("`Playback.startSec` echoes the requested resume position").
- **Failure scenario (reproduced):** item 1 is a direct-playable webm, saved progress 45 s. Detail page shows "Resume from 0:45".
  Click it: `POST /playback` carries `startSec: 45`, the response says `"startSec":0`, so `onLoadedMetadata` never seeks.
  Video plays from 0:00. `armed` flips at once (`currentTime >= 0 - 3`), the first 10 s tick sends `PUT progress {positionSec: 9.76}`,
  and `store.SetProgress` deletes the row for any position under 30 s. Server progress went from `{45}` to `null`.
  HLS sessions are not affected (the server echoes `startSec` there). Direct play is the most common path (mp4/h264, webm).
- **Fix:** web: in `start()`, use the position the client asked for (`at`) when `pb.method === 'direct'`
  (e.g. `const target = pb.method === 'direct' ? at : pb.startSec`) for the resume seek and for the arming threshold; keep it in a variable
  rather than `playback.startSec`. Server: echo `req.StartSec` for direct plays, as the contract says.
- **Tests:**
  - Playwright e2e: `PUT` progress 45 on a direct-play item, open `#/item/<id>`, click "Resume from", assert `video.currentTime >= 44` within 3 s,
    wait 11 s, assert `GET /api/items/<id>` still has `progress.positionSec >= 44`.
  - Go API test: `POST /playback` for a direct item with `startSec: 100` returns `startSec: 100`.
  - Unit test (after extracting the resume logic, see Test gaps): `resumeTarget({requested: 45}, {method: 'direct', startSec: 0}) === 45`.

### W2. After a fatal player error, leaving deletes saved progress and Retry restarts from the wrong place

- **Where:** `web/src/components/Player.svelte:139` (`armed` set in `onTimeUpdate`), `:169-184` (`release()` calls `el.load()`),
  `:186-190` (`fail`), `:289-298` (`leave`, `retry`), `:192-195` (`start()` begins with `report()`).
- **Mechanism:** `release()` clears `armed`, but `el.load()` makes the browser reset `currentTime` to 0 and fire a fresh `timeupdate`
  (the load algorithm discards already-queued events, not the one it queues itself). `onTimeUpdate` then runs with
  `at = playback?.startSec ?? 0 = 0` (every session that started at 0, and every direct-play session, see W1), so `0 >= at - 3` re-arms
  reporting while `el.currentTime` is 0. It also stores `lastPos = 0`. The comment in `release()` ("nothing may report from here on") does not hold.
- **Failure scenario (reproduced, hls session):** start a remux item at 0, watch past the 10 s tick (server progress 88.5 s). Make the stream fail
  (I `DELETE`d the session server-side, which is what a dropped network / idle reap looks like to the client). Error overlay appears.
  - Click "← Details": `PUT progress {positionSec: 0}` is sent, server progress `{88.5, watched:true}` becomes `null`.
  - Click "Retry" instead: `start()` -> `report()` sends `PUT {positionSec: 0}` (same wipe), and `retry()` sees `lastPos === 0`, so playback restarts
    at 0:02 instead of where it failed (observed `currentTime` 2.8 after Retry).
  - The same re-arming can fire on `restart()` (audio / burned-in subtitle change) if `visibilitychange`/`pagehide` arrives during the
    `POST /playback` await; I did not hit that one in Chrome, but the guard is missing there too.
  - (While the overlay is up the "Close player" button is unreachable: the `.overlay` covers the whole frame. Only "← Details" or Back can leave.)
- **Fix:** make the guards depend on media state, not on `playback.startSec`:
  - `report()` and `onTimeUpdate` return early unless `el.readyState >= HTMLMediaElement.HAVE_METADATA` (after `load()` it is 0) and `el.currentSrc` is non-empty.
  - Track `lastGoodPos` only while armed and `readyState >= 1`; `retry()` and `restart()` use it. Capture it in `fail()` before `release()`.
  - Optionally refuse to send `positionSec === 0` when a larger `lastSent` exists for this session.
- **Tests:**
  - Playwright e2e (the repro above): force an HLS failure, click "← Details", assert `GET /api/items/<id>` progress unchanged; click Retry and assert the
    `POST /playback` body has `startSec` within 2 s of the failure position and no `PUT progress` with `positionSec: 0` was sent.
  - Unit test on an extracted progress controller with a fake `<video>` whose `load()` dispatches `timeupdate` at `currentTime = 0`, `readyState = 0`:
    assert zero `putProgress` calls after `release()`.

---

## SHOULD: web

### W3. Failed `import('hls.js')` hangs the player on "Preparing stream..."

- **Where:** `web/src/components/Player.svelte:238` (`await import('hls.js')`, no try/catch; `start()` is called as `void start()` at `:304`, `:111`, `:297`).
- **Failure scenario (reproduced):** hls.js is a 578 kB lazy chunk. If that request fails (flaky Wi-Fi on a phone, or ply upgraded while a tab was open so the old chunk
  name now returns the SPA fallback HTML), the rejection is unhandled (`pageerror: Failed to fetch dynamically imported module`). `phase` stays
  `'starting'`: spinner forever, no error, no Retry. The session opened at `:205` stays alive until the user leaves or the 60 s idle reaper runs.
- **Fix:** wrap the body of `start()` after the `POST` in try/catch (or just the import) and call `fail('Could not load the video player. Check the connection and retry.')`.
  A guard for `run !== attempt` goes in the catch as well.
- **Test:** Playwright with `serviceWorkers: 'block'`, force hls.js (canPlayType override), `route('**/assets/hls-*.js', r => r.abort())`:
  expect `[role=alert]` visible within 5 s and a `DELETE /api/sessions/<id>`.

### W4. bfcache round trip leaves a dead player

- **Where:** `web/src/components/Player.svelte:317` (`<svelte:window onpagehide={leave} />`), `:290-293`, `:168-184`.
- **Failure scenario (reproduced by dispatching `pagehide`/`pageshow` with `persisted: true`):** `pagehide` fires when the page enters the back/forward cache
  (iOS Safari, Android Chrome, desktop Chrome/Firefox all do this when the user leaves to another site and comes back). `leave()` reports progress, destroys hls.js,
  empties the `<video>` and DELETEs the session. On restore (`pageshow`, `persisted: true`) nothing restarts: observed `readyState 0`, `paused`, `t = 0`, no overlay,
  no error, Play does nothing. The user has to navigate away and back.
- **Fix:** add `<svelte:window onpageshow={(e) => e.persisted && !destroyed && (resumeFrom = lastGoodPos, void start())} />`, or skip the teardown on `e.persisted`
  and only report progress there (then the session reaper may kill the stream, so restarting is the safer choice).
- **Test:** Playwright: play, seek to 40 s, dispatch `pagehide`+`pageshow` (`persisted: true`); expect a second `POST /playback` and `currentTime >= 38` within 5 s.

### W5. Phone layout: the player control row overflows and the Full screen button is clipped

- **Where:** `web/src/components/PlayerControls.svelte:427-437` (`.controls` is a one-column grid with an implicit `auto` column), `:547-552` (`.row` is `flex`, no wrap,
  no `min-width: 0`), `:556-567` (`.ctl` is a fixed 2.75 rem), `:648-653` (only the volume slider and the "/ total" are dropped at 600 px).
- **Failure scenario (reproduced at 360x740):** for any item with subtitles and more than one audio track, the row needs about 380 px of buttons. The grid column grows to 419 px
  inside a 328 px frame with `overflow: hidden`: the Picture-in-picture button is half cut and the Full screen button (x 363-407) is entirely off-screen
  (screenshot `pw/m-__play_6.png`). The scrubber input also extends about 63 px past the frame edge, so the last ~17 % of the timeline cannot be reached by pointer or touch.
  On a phone, fullscreen is the primary way to watch.
- **Fix:** `.controls { grid-template-columns: minmax(0, 1fr); }`, `.row { flex-wrap: wrap; min-width: 0 }` or shrink `.ctl` to 2.25 rem under 600 px and hide PiP there;
  keep Full screen and the audio/subtitle menus always visible.
- **Test:** Playwright at 360x740 (and 320x568) on a fixture with two audio tracks and a sidecar subtitle: for every `.row button`, assert
  `getBoundingClientRect().right <= frame.getBoundingClientRect().right`, and `scrubber.right <= frame.right`.

### W6. Player key handler hijacks Space on buttons and modifier chords while the scrubber is focused

- **Where:** `web/src/components/PlayerControls.svelte:262-289` (`onKey` on `window`). Lines 264-266 let any key through when the target is a range input, even with
  Ctrl/Meta/Alt held, and nothing excludes buttons.
- **Failure scenarios (reproduced):**
  1. Focus any control button (Back 10 s, Subtitles, Audio, a menu item, Close) and press Space: `onKey` calls `toggle()` and `preventDefault()`, so the button is not activated.
     Observed: Space on "Back 10 seconds" while paused started playback and did not seek. Keyboard users cannot open the Subtitles/Audio menus with Space (Enter still works).
  2. After the user has used the scrubber (it stays focused), Ctrl+F toggles fullscreen and suppresses browser Find (`defaultPrevented: true`), Ctrl+L skips 10 s and suppresses
     "focus address bar", Cmd+F likewise (Ctrl/Cmd+K is fine: only the palette reacts).
- **Fix:** return first on `e.ctrlKey || e.metaKey || e.altKey` (unconditionally), skip Space/Enter when `t.closest('button, a, summary, [role="menuitemradio"]')`,
  and ignore `e.repeat` for toggle-type keys (`f`, `m`, `c`, Space).
- **Test:** Playwright: focus `button[aria-label="Back 10 seconds"]`, press Space, assert the click happened (seek) and playback state is unchanged;
  focus the scrubber, press `Control+f` and assert the keydown's `defaultPrevented` is false and fullscreen did not toggle.

### W7. Hidden player controls stay focusable and do not reappear on Tab

- **Where:** `web/src/components/PlayerControls.svelte:438-444` (`.controls.hidden { opacity: 0 }`, no `:focus-within`), `:218-231` (`poke`), `:262-289`
  (`Tab` is not in the `keys` map, so `poke()` is never called for it).
- **Failure scenario (reproduced):** while playing and idle for 2.6 s the controls fade out. Pressing Tab three times moves focus to a link, "Close player", then "Seek",
  with `opacity: 0` throughout; a keyboard-only user is operating blind (WCAG 2.4.7 Focus Visible).
- **Fix:** `.controls:focus-within { opacity: 1; }` (and `.controls.hidden:focus-within *` keep `pointer-events`), plus `onfocusin={poke}` on `.controls`; clear the hide timer while focus is inside.
- **Test:** Playwright: play, wait 3.5 s, press Tab, assert `getComputedStyle(.controls).opacity === '1'`.

### W8. Opening a detail page starts and kills a server session for remux/transcode items

- **Where:** `web/src/pages/Item.svelte:37-43` (`decision` resource does `api.playback(id, {custom: browserProfile()})`, then deletes the session). It re-runs on every
  `app.libraryVersion` bump too (line 38). Server: `internal/stream/manager.go:243-293` `Create` runs the keyframe scan and `startRunLocked` (`cmd.Start()`), and counts against `MaxSessions = 4`.
- **Failure scenario:** visiting `#/item/6` (an mkv that needs remux) logs `POST /playback` then `DELETE /api/sessions/<sid>` 4 ms later. Each detail-page view for a remux/transcode item
  spawns ffmpeg (a hardware encoder session for a transcode) and, the first time per file, an ffprobe packet scan of the whole file (minutes on a NAS), only to show a one-line label.
  Fast browsing through the library multiplies it; with four real playbacks active the page shows an error instead of the decision (`ErrTooManySessions`).
  DESIGN.md efficiency rules list this kind of work as a hard requirement to avoid.
- **Fix:** do not POST until the user presses Play. For the label use `GET /api/items/{id}?target=browser` (built-in profile, no session), or add a dry-run flag to
  `POST /playback` that returns the Decision without creating a session. The Player's own `POST` remains the only session creator.
- **Test:** Playwright: open `#/item/<remux id>`, assert no request to `/playback` (or `?dryRun`) and no `/api/sessions/*` request.
  Go API test with a fake `stream.Manager`: `Create` count stays 0 for the detail-page request.

---

## SHOULD: `internal/art`

### A1. ffmpeg grabs outlive the server

- **Where:** `internal/art/art.go:174` (`go g.fill(context.WithoutCancel(ctx), ...)`), no `Close`/`Wait` on `Generator`; `cmd/ply/main.go:93` (`art.New`), shutdown at `:150-160`
  (`streams.Close()` and `srv.Shutdown`, nothing for art).
- **Failure scenario (reproduced with a wrapper `-ffmpeg` that hangs on frame grabs, standing in for a slow network mount):** request an image, `kill -TERM` ply while the grab is running.
  ply exits, the child (own process group because of `Setpgid`) is re-parented to PID 1 and keeps running (`ps`: PPID 1, PGID = its own pid). The 30 s `Timeout` is enforced by a goroutine that no
  longer exists, so a pathological file can keep a core busy until ffmpeg finishes on its own. systemd's cgroup kill hides this on Linux; launchd and `go run` do not.
  DESIGN.md rule 6 ("ffmpeg in its own process group; shutdown kills all ... tests assert no ffmpeg children remain") applies to every ffmpeg ply spawns.
- **Fix:** give `Generator` a base context cancelled by a new `Close()`, use it instead of `context.WithoutCancel(ctx)` in `Path`, track goroutines with a `sync.WaitGroup`,
  and call `artGen.Close()` next to `streams.Close()` in `main.go`. `Path` after `Close` should return an error.
- **Test (integration, unix):** fake ffmpeg script that `exec sleep 60`; start `Path` in a goroutine, read the pid from the `g.started` hook, call `g.Close()`, assert
  `syscall.Kill(-pid, 0)` returns `ESRCH` within 3 s (same pattern as `TestCancelKillsFFmpeg`).

### A2. Timeouts and other transient errors are never remembered, so every request re-runs ffmpeg

- **Where:** `internal/art/art.go:189-198` (`fill` remembers only `ErrNoFrame`), `:311-331` (`exec` returns `ctx.Err()` on timeout), and `internal/api/art.go:62-72`
  (a deadline error becomes a 500, not a 404).
- **Failure scenario (reproduced with `Timeout: 300ms` and a hanging ffmpeg):** three consecutive `Path` calls return `context deadline exceeded` after 300 ms each and start three ffmpeg runs.
  With the real 30 s timeout, a file on a spun-down or slow share costs 30 s of niced ffmpeg per request; a grid of 100 such tiles reloaded by the browser (`loading="lazy"`, each remount, each
  rescan) keeps both slots busy for 25 minutes per pass, and the UI shows tiles as failed meanwhile.
- **Fix:** remember failures with a TTL: `failed[key] = {err, until}`; permanent for "no video stream", 10-15 minutes for timeouts and spawn errors. Map a deadline to a 404/503 with `Retry-After` in the handler.
- **Test:** unit test with the existing `stub`: make `run` return `context.DeadlineExceeded`; call `Path` twice inside the TTL and assert `Runs() == 1`; advance an injected clock past the TTL and assert a second run.
  (Mutant `failed-caches-everything` and `timeout-ignored` currently survive, see A4.)

### A3. Any ffmpeg failure becomes `ErrNoFrame` and is remembered until restart

- **Where:** `internal/art/art.go:324-329` (every `cmd.Wait` error is wrapped in `ErrNoFrame`), `:193-195` (remembered), `:239-243` (kept by `Prune` while the item is live and the mtime is unchanged).
- **Failure scenario (reproduced with real ffmpeg):** the media root (NAS/USB disk) is unmounted when the first thumbnail is requested. The scanner correctly keeps the rows (the design's safety rule),
  but `Path` returns `ErrNoFrame: ... No such file or directory` and records it. After the share is mounted and the file is readable, a second `Path` still returns the remembered error without running ffmpeg.
  The same happens after an OOM-kill of ffmpeg, a full disk (`tmp` write fails) or a transient read error. Art then 404s until ply restarts or the file's mtime changes.
- **Fix:** only treat failures as permanent when they are about the content: parse ffmpeg's stderr for "does not contain any stream" / "matches no streams", or run `os.Stat(src.Path)` first and return a
  non-remembered `fs.ErrNotExist`. Give everything else the TTL from A2.
- **Test:** integration test: `Path` for a path that does not exist yet, then create the file with ffmpeg, `Path` again must succeed (a version of this is in the scratchpad `mut/internal/art/extra_test.go`:
  it currently "passes" by asserting the bug).

### A4. Test gaps: surviving mutants

Eighteen quick mutants of `art.go` were run against the default suite (`go test ./internal/art/`, `-timeout 40s`). Killed: 8 (concurrency cap, colour pruning, mtime pruning, empty-output check,
seek retry guard, atomic rename, colour backfill, `WithoutCancel`). **Survived: 10**, each a real behaviour with no test (the `-skip_frame` and `setNice` mutants share one row):

| Mutant | What it breaks | Test to add |
|---|---|---|
| `strings.HasSuffix(.., ".tmp") && recent(e)` -> `false` in `Prune` | Prune deletes the `.tmp` of an in-flight generation; the final `os.Rename` then fails and the request 500s | write `x.jpg.tmp` (fresh) and an old one (`os.Chtimes` -10 min); Prune keeps the fresh one and removes the old one |
| `failed` cleanup loop removed from `Prune` | remembered failures of deleted/changed items live forever | fail item 1, Prune with `{}`; assert a later `Path` for the same key runs ffmpeg again |
| `mtime > c.mtime` -> `<` in `loadColours` | after restart the OLD colour wins when two colour files exist for one id | two `.color2` files for id 7 with mtimes 5 and 9; assert `Colour(src(7, 9))` ok and `Colour(src(7, 5))` not |
| `defer os.Remove(tmp)` removed | failed grabs leave `.tmp` files | stub that writes tmp then returns `ErrNoFrame`; assert directory has no `.tmp` |
| `errors.Is(c.err, ErrNoFrame)` -> `c.err != nil` | context errors become permanent | stub `run` returns `context.Canceled`/`DeadlineExceeded`; assert second `Path` runs again (A2) |
| `delete(g.inflight, key)` removed | a non-`ErrNoFrame` error is served forever from the stale in-flight entry | same test as above |
| `context.WithTimeout` -> `WithCancel` in `grab` | no timeout at all | fake ffmpeg that sleeps, `Timeout: 200ms`, assert `DeadlineExceeded` within 1 s and the process is gone |
| `-skip_frame nokey` removed / `setNice` removed | decoding every frame / no niceness | integration: assert argv contains `-skip_frame`, and `ps -o nice= -p <pid>` is 10 (via the `started` hook) |
| `validHex` always true in `loadColours` | a corrupt or hand-edited `.color2` file feeds garbage into the page's `--tint` | write `.color2` containing `javascript` / `#12` / `#zzzzzz`; assert `Colour` is not ok |

Also no test asserts that a failing image build leaves no partial `*.jpg` (only the size-0 case is covered) or exercises `Prune` while a generation is in flight.
`go test -race -count=15` is stable, so the existing tests are not flaky.

---

## Test gaps (web/src/lib)

`web/package.json` has no test runner at all (no `test` script, no vitest/playwright dependency). All of W1-W8 would have been caught by tests at the module level below.
Add `vitest` (+ `jsdom`) for the pure modules and one Playwright smoke suite against a `ply` started on the lavfi fixtures.

1. **Progress/resume logic (extract `lib/progress.ts` from `Player.svelte`)**: the `armed` / `lastSent` / `lastPos` / `resumeFrom` state machine is where both BLOCKING bugs live and today it is
   welded to a component and a real `<video>`. Pull it into a plain class that takes a video-like object and an `api` stub, then test: resume position per method (direct vs HLS), no report before
   armed, no report after `release()`, dedupe, keepalive on leave, Retry uses the last good position.
2. **`router.svelte.ts` (`parseRoute`, `href`)**: pure and user-facing; test round trips for show names with `? # % & / + ' [ ]`, malformed percent-encoding (`%E0%A4%A`), `?s=` / `?t=` edge values (`s=` currently parses as season 0), unknown routes.
3. **`api.ts` (`request`, `mediaUrl`, token handling)**: 401 clears the token and calls the handler, `mediaUrl` adds `?token=` once and keeps existing query, keepalive flag reaches `fetch`,
   null-slice normalisation in `items()/shows()/item()`.
4. **`profile.ts` (`browserProfile`)**: stub `canPlayType` and `MediaSource.isTypeSupported` per browser (Safari with `ManagedMediaSource`, Firefox without H.264, Chrome); it decides direct/remux/transcode for every playback.
5. **`library.svelte.ts` (`showsOf`, `episodeOrder`, `ensureLibrary`)**: cover choice with specials, season sets, and the stale in-flight case (`ensureLibrary` returns the older fetch after `libraryVersion` bumps).
6. **`resource.svelte.ts`**: out-of-order responses are discarded (`stale`), error then success clears `error`; needs `$effect.root`.
7. **`state.svelte.ts` (`rescan`/`follow`)**: `scan.running` resets on every exit path, 409 handling, the status message timer.
8. **`fuzzy.ts` and `format.ts`**: pure; `search` ordering and limit, no diacritic folding (`Amelie` does not match `Amélie`), `formatClock` hour rollover, `resolutionLabel` cut-offs, `hdrLabel` strings.

---

## Fixes

### rare-tiger: art and web (W5–W8, A1–A4, plus the lib test gaps)

Every fix has a test that fails without it. That was proven by mutation on a scratch copy
(Go and vitest), or by running the browser check against the 1282ff3 UI (W5–W7).

| ID | Status | Fix | Proof |
|---|---|---|---|
| A1 | FIXED (one call still needed in main.go) | `Generator` has a base context, a `WaitGroup` and `Close()` (cancel, kill groups, wait); `Path` after Close gives `ErrClosed`. **velvet's agent:** call `artGen.Close()` next to `streams.Close()` in `cmd/ply/main.go`. | unit `TestCloseStopsGenerationsAndRefusesNewOnes` (a slow-exiting stub proves Close waits); integration `TestCloseKillsTheFFmpegGroupAndRunsItNiced` (hung fake ffmpeg, kill(-pgid,0)=ESRCH, positive control). Mutants KILLED: no cancel, no wait, Background ctx. |
| A2 | FIXED | Failures are remembered by kind: content (no video, undecodable) until the mtime changes; timeouts and other errors for 10 min (`failureTTL`, injectable clock); timeouts become `ErrUnavailable`. **velvet's agent:** map `art.ErrUnavailable` and `art.ErrClosed` to 503 + `Retry-After` in `internal/api/art.go`. | `TestTransientFailuresAreRetriedAfterTheTTL` (timeout, I/O, spawn), `TestTimeoutIsReportedAsUnavailable`, integration `TestTimeoutKillsTheFFmpegGroup`. Mutants KILLED: TTL ignored, transient made permanent, inflight never cleared, no grab timeout. |
| A3 | FIXED | `generate` stats the input first: a missing file (unmounted share) gives `ErrUnavailable` wrapping `fs.ErrNotExist`, runs no ffmpeg and isn't remembered. ffmpeg errors are classified by stderr (`contentFailure`); only content errors are `ErrNoFrame`. | `TestMissingInputRunsNoFFmpegAndIsRetriedNextTime`, `TestContentFailureClassification`. Mutants KILLED: no stat, ErrNotExist remembered, everything classed as content. |
| A4 | FIXED | Tests for each surviving mutant: in-flight `.tmp` kept by Prune, Prune forgets failures, the newest colour file wins, no partial files on failure, corrupt colour files ignored, `-skip_frame nokey` and `-nostdin` pinned, nice=10 asserted. | 17/17 mutants KILLED (baseline green, unit and integration). |
| W5 | FIXED | `.controls` column `minmax(0,1fr)`, `.row` wraps with `min-width:0`; under 600 px the ±10 s and PiP buttons are hidden (double-tap seeks, PiP is a system gesture). Full screen, Subtitles and Audio always stay. | `web/e2e/player-controls.mjs` at 360×740 and 320×568: all controls inside the frame. FAILS on 1282ff3 (Full screen and PiP clipped; Audio too at 320). |
| W6 | FIXED | `onKey` ignores Ctrl/Cmd/Alt chords unconditionally; Space/Enter go to a focused button, link or menu item; repeats of the toggle keys are ignored. | e2e: Space on "Back 10 seconds" seeks and stays paused; Ctrl+F with the scrubber focused is not prevented; a held "m" mutes once. All three FAIL on 1282ff3. |
| W7 | FIXED | `onfocusin` shows the controls; the hide timer skips while `:focus-visible` is inside; CSS `.controls.hidden:has(:focus-visible)` restores opacity and pointer events. | e2e: positive control (the controls do auto-hide), Tab shows them, and they stay while focus is inside. FAILS on 1282ff3. |
| W8 | FIXED | The detail page no longer POSTs `/playback`. It shows the decision from `GET /api/items/{id}?target=browser` (the handler only computes it: sessions are created solely in `attachSession`), labelled "In a web browser". | Checked against the server: a remux item's detail request returns `sessionId: null`, with no ffmpeg running and no session dir. |
| lib | FIXED and TESTED | vitest + jsdom (`npm test`). Bugs found and fixed while testing: `?s=` parsed as season 0; `ensureLibrary` reused a fetch from before a scan and let a late old response overwrite a newer one; search ignored accents (`fold`); `href.play(id, 0.4)` emitted `?t=0`. | 59 tests (router, fuzzy, library, ui-types, nav). 7/7 vitest mutants KILLED (stale reuse, late overwrite, `?s=`, folding, `?t=0`, morph name release, modified clicks). |

Not mine, so not touched: W1–W4 (Player and Play logic) belong to velvet's fix agent, and
`api.ts`/`profile.ts` tests to its test round. `ambient.svelte.ts` has no unit test: it is a
thin `$effect` wrapper and is exercised by every page.

## Fixes: W1 to W4 (Player.svelte)

The other agent's W5 to W8 and A1 to A4 lines are separate. Not committed.

W1 to W4 share one cause, so they are fixed as one state machine, not four patches:
`web/src/lib/progress.ts` (`Progress`) follows one `<video>` through any number of streams. A
stream is a generation: `begin(target)` says where it must start, nothing is reported until the
element has really reached that position (`readyState >= 1`, a `currentSrc`, not seeking, within
3 s of the target), `end()` keeps the position and disarms, and `position()` is always where the
next stream starts from (the live position while it counts, else the last one that counted, else
the target). `Player.svelte` no longer reads the position from the element or from
`playback.startSec` anywhere; start, restart (audio or subtitle change), Retry, the S5 session
restart and the bfcache restore all call `start(progress.position())`. This replaces the earlier
`canArmProgress` and the `lastPos` / `resumeFrom` / `armed` / `lastSent` variables.

Verification: all four were reproduced and re-checked in a real browser with the Playwright MCP
tools (Chrome 156), on two builds of the same fixtures: the HEAD build ("before", server
127.0.0.1:18420) and the fixed tree ("after", 18421), each with its own data dir; 150 s H.264
clips, `Direct Clip.mp4` (direct play) and `Remux Clip.mkv` (HLS remux). Chrome plays HLS
natively, so where hls.js mattered a context init script made `canPlayType('application/vnd.apple.mpegurl')`
return `''` (`currentSrc` was a `blob:` URL then) and the service worker's registration was
blocked, so that `page.route` sees the requests. `window.fetch` was wrapped to log the non-GET
requests. Table tests of the state machine run without a browser, under vitest (`npm test`):
`web/src/lib/progress.test.ts` and `web/src/lib/session.test.ts`.

- **W1 fixed, with the server.** The resume target is what the client asked for (`start(at)` ->
  `progress.target`), used for the seek in `onLoadedMetadata`, the hls.js `startPosition` and the
  arming test; `playback.startSec` is not read. The server now echoes the requested `startSec` for
  direct play too (DESIGN.md: "echoes the requested resume position"); `api_test.go` "startSec"
  expects 30.5 for a direct item (it expected 0 before).
  - Repro: server progress for the direct clip 45 s, open `#/play/1?t=45`.
  - Before: `currentTime` 5.4 s after 3 s (playing from the start, not from 0:45), and after the
    10 s tick the server progress went from `{45}` to `null`.
  - After: 50.96 s after 3 s, and 54.9 s on the server after the tick (`{positionSec:54.9}`).
- **W2 fixed.** `fail()` reports where playback got to while the element still knows, `release()`
  calls `progress.end()` before `el.load()`, and the `timeupdate` that unloading fires at 0 can
  neither re-arm nor overwrite the position; Retry starts from `progress.position()`.
  - Repro (before): play the remux clip from 0, seek to 80 s, wait for the 10 s tick (server 84 s),
    `DELETE` the session server-side, seek 30 s ahead so a segment is requested -> error overlay.
    "Details": `PUT progress {"positionSec":0}`, server progress `{84.15}` -> `null`. "Retry":
    `PUT {positionSec:0}` first (progress -> `null`), then playback restarts at 1.4 s.
  - After (hls.js active, every `/api/sessions/*` request answered 404 so the failure is final): overlay
    "Stream error: manifestLoadError" after the S5 restarts are used up; server progress is the
    failure position (121.4 s, the seek target) and is still there after "Details" (no PUT, only
    `POST /playback` and `DELETE` of the Item page's probe); "Retry" sends one `POST /playback`
    with `startSec: 121.407` and plays from 123.3 s, no `PUT` at all.
- **W3 fixed.** A failed `import('hls.js')` calls `fail('Could not load the video player. Check the
  connection and retry.')` (with the `run !== attempt` guard) and the session opened by the POST is
  DELETEd by `fail()`. Retry reloads the page when that happened: the browser remembers a failed
  dynamic import for the life of the page, so `start()` again would fail the same way (checked: the
  chunk was fetched again and the import still rejected); the URL's `?t=` is replaced with the
  current position first.
  - Repro: block the service worker's registration, abort `**/assets/hls-*.js`, force hls.js, open `#/play/2`.
  - Before: "Preparing stream..." forever (6 s later still `.overlay.passive`, no `[role=alert]`),
    `pageerror: Failed to fetch dynamically imported module`.
  - After: `[role=alert]` "Could not load the video player...", the `DELETE /api/sessions/<id>` goes out
    right after the `POST`; with the route removed, "Retry" reloads and plays from the `?t=60` position
    (62.8 s, `blob:` source).
- **W4 fixed.** `<svelte:window onpageshow>`: a `persisted` pageshow restarts at `progress.position()`
  (pagehide's `leave()` reports, tears the stream down and keeps the position); a non-persisted
  pageshow does nothing.
  - Repro: play the remux clip with hls.js, seek to 40 s, dispatch `pagehide` then `pageshow`, both
    `persisted: true`.
  - Before: `readyState 0`, paused, `t = 0`, no overlay, no new `POST /playback`.
  - After: pagehide sent `PUT {42.43}` and `DELETE`; pageshow sent `POST /playback {"startSec":42.43}`;
    5 s later `readyState 4`, playing at 48.2 s. A `persisted: false` pageshow added no POST.
- **S5 (from the first review) re-verified live, and tightened.** With `-stream-idle 5s`, pause for
  9 s, seek 70 s ahead: before, hls.js asks for `seg00015` seven times (404 each time, about half a
  minute of "stalled" before it gives up); after, the first 404 starts a new session
  (`DELETE` of the dead one, `POST /playback`) and playback resumes at the seek position within
  0.26 s, then plays on. The first version waited for the error to turn fatal, and a restarted session
  whose playlist also 404s left the player on "Preparing stream..." because `startLoad()` cannot revive a
  playlist; both are fixed (react to the first 404; give up with an error once the restarts are used).

Also done from the orchestrator's notes: `cmd/ply/main.go` calls `artGen.Close()` next to
`streams.Close()` and `subGen.Close()` (nil-safe: it is nil when art is disabled);
`handleArt` maps `art.ErrUnavailable` and `art.ErrClosed` to 503 with `Retry-After: 60` and keeps
`art.ErrNoFrame` at 404. Tests with the fake-ffmpeg generator: `TestArtTemporaryFailureIsUnavailable`
("Permission denied" -> 503 and the header), `TestArtAfterShutdownIsUnavailable`,
`TestArtFailureIsNotFound` (now uses an ffmpeg message that art classifies as the file's fault).
Both new tests fail when the 503 case is removed.
