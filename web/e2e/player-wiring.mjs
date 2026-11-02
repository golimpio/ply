// How Player.svelte wires lib/progress.ts and lib/session.ts (review findings W1-W4, M5 and S5),
// in a real browser, against a mocked API. The unit tests pin the state machines; this pins that
// the component drives them: that the resume seek happens, that nothing reports a position it
// should not, that the right request leaves at the right moment with the right keepalive flag.
//
// Self-contained: serves web/dist (build it first: `npm run build`; PLY_E2E_DIST picks another
// directory), answers every /api/ request with page.route, and builds its media with ffmpeg. Needs
// Google Chrome (Playwright's bundled Chromium lacks H.264/AAC) and ffmpeg on PATH.
//
//   npm run build && node e2e/player-wiring.mjs
//
// PLY_E2E_ONLY=W3 runs the scenarios whose name contains that text; PLY_E2E_VERBOSE=1 names each one
// as it starts. Each scenario lists the mutations of Player.svelte that it was confirmed to catch
// (build a mutant into another directory with `vite build --outDir ... --emptyOutDir` and point
// PLY_E2E_DIST at it).
import { createServer } from 'node:http';
import { execFileSync } from 'node:child_process';
import { existsSync, mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, extname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright-core';

const here = dirname(fileURLToPath(import.meta.url));
const dist = process.env.PLY_E2E_DIST ?? join(here, '..', 'dist');
if (!existsSync(join(dist, 'index.html'))) {
  console.error(`No build in ${dist}: run \`npm run build\` first.`);
  process.exit(2);
}

const results = [];
const check = (name, ok, detail = '') => results.push(`${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? `  (${detail})` : ''}`);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const near = (x, want, tol) => Math.abs(x - want) <= tol;

/** Poll (here, in node) until fn returns something truthy; throws, so a scenario fails instead of hanging. */
async function until(what, fn, ms = 10000) {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > end) throw new Error(`timed out waiting for ${what}`);
    await sleep(40);
  }
}

// ---- media: a 60 s H.264/AAC clip, and the same cut into 2 s HLS segments ---------------------------

const work = mkdtempSync(join(tmpdir(), 'ply-wiring-'));
const directFile = join(work, 'direct.mp4');
const hlsDir = join(work, 'hls');
execFileSync('ffmpeg', ['-v', 'error', '-y', '-f', 'lavfi', '-i', 'testsrc2=s=320x180:r=10:d=60', '-f', 'lavfi', '-i', 'sine=f=440:d=60',
  '-c:v', 'libx264', '-preset', 'ultrafast', '-b:v', '150k', '-g', '20', '-keyint_min', '20', '-sc_threshold', '0', '-pix_fmt', 'yuv420p',
  '-c:a', 'aac', '-b:a', '32k', '-movflags', '+faststart', directFile]);
execFileSync('mkdir', ['-p', hlsDir]);
execFileSync('ffmpeg', ['-v', 'error', '-y', '-i', directFile, '-c', 'copy', '-f', 'hls', '-hls_time', '2', '-hls_playlist_type', 'vod',
  '-hls_segment_filename', join(hlsDir, 'seg%03d.ts'), join(hlsDir, 'index.m3u8')]);
const directBytes = readFileSync(directFile);

// ---- the app: web/dist over http (everything under /api/ is answered by the page routes) -----------

const types = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.json': 'application/json', '.png': 'image/png' };
const server = createServer((req, res) => {
  const path = new URL(req.url, 'http://x').pathname;
  const file = path === '/' ? join(dist, 'index.html') : join(dist, path);
  if (!file.startsWith(dist) || !existsSync(file) || file === dist) {
    res.writeHead(404).end();
    return;
  }
  res.writeHead(200, { 'content-type': types[extname(file)] ?? 'application/octet-stream' }).end(readFileSync(file));
});
await new Promise((r) => server.listen(0, '127.0.0.1', r));
const base = `http://127.0.0.1:${server.address().port}`;

// ---- the mocked server ---------------------------------------------------------------------------

const ITEM = 1;
const detail = (extra = {}) => ({
  id: ITEM, kind: 'movie', title: 'Wiring Test', year: 2020, durationSec: 60, container: 'mp4', videoCodec: 'h264', audioCodec: 'aac',
  width: 320, height: 180, addedAt: 1, progress: null, art: null, path: '/m/wiring.mp4', size: 1, bitrate: 1,
  streams: [
    { index: 0, type: 'video', codec: 'h264' },
    { index: 1, type: 'audio', codec: 'aac', language: 'eng', channels: 2 },
    { index: 2, type: 'audio', codec: 'aac', language: 'fra', channels: 2 },
  ],
  subtitles: [],
  playback: { method: 'direct', videoAction: 'copy', audioAction: 'copy', audioIndex: 1, reasons: [], url: '', startSec: 0 },
  ...extra,
});

/**
 * One browser context against one mocked server.
 *   hls       every playback is a remux session served through hls.js (the context claims no native HLS)
 *   sessions  (n, body) -> { mode } for the n-th POST /playback: 'ok', 'segments404' or 'all404'
 *   hold      POST /playback waits for s.release()
 *   item      extra fields of the item
 *   fast      run the 10 s progress ticker every 250 ms (the requested delays are kept in window.__intervals)
 *   blockHls  abort the lazy hls.js chunk
 */
async function open(browser, opts = {}) {
  const context = await browser.newContext({ viewport: { width: 960, height: 600 }, serviceWorkers: 'block' });
  const log = []; // every non-GET call the app makes: { method, url, body, keepalive }
  const sessions = new Map();
  const errors = [];
  const unmocked = [];
  let posts = 0;
  let releaseHold;
  const held = new Promise((r) => (releaseHold = r));

  await context.exposeFunction('__rec', (e) => log.push(e));
  await context.addInitScript(({ hls, fast }) => {
    const rec = (method, url, body, keepalive) => window.__rec({ method, url: new URL(url, location.href).pathname, body, keepalive: !!keepalive });
    const realFetch = window.fetch.bind(window);
    window.fetch = (input, init = {}) => {
      const method = init.method ?? 'GET';
      if (method !== 'GET') rec(method, typeof input === 'string' ? input : input.url, init.body ? JSON.parse(init.body) : null, init.keepalive);
      return realFetch(input, init);
    };
    if (hls) {
      // hls.js, not the browser's own HLS (Chrome has one now; Safari's is what the native path is for)
      const can = HTMLMediaElement.prototype.canPlayType;
      HTMLMediaElement.prototype.canPlayType = function (type) {
        return /mpegurl/i.test(type) ? '' : can.call(this, type);
      };
    }
    window.__intervals = [];
    const realSetInterval = window.setInterval.bind(window);
    window.setInterval = (fn, ms, ...args) => {
      window.__intervals.push(ms);
      return realSetInterval(fn, fast && ms >= 1000 ? 250 : ms, ...args);
    };
  }, { hls: !!opts.hls, fast: !!opts.fast });

  const json = (route, body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body), headers: { 'cache-control': 'no-store' } });
  await context.route((u) => u.pathname.startsWith('/api/'), async (route) => {
    const req = route.request();
    const { pathname: p } = new URL(req.url());
    const m = req.method();
    if (m === 'GET' && p === '/api/info') return json(route, { id: 'x', name: 'wiring', version: '0', authRequired: false });
    if (m === 'GET' && p === '/api/scan') return json(route, { running: false, itemCount: 1 });
    if (m === 'GET' && p === '/api/items') return json(route, []); // the item page's neighbours
    if (m === 'GET' && p === '/api/shows') return json(route, []); // loaded with the library since M6
    if (m === 'GET' && p === `/api/items/${ITEM}`) return json(route, detail(opts.item));
    if (m === 'GET' && p.endsWith('.vtt')) return route.fulfill({ status: 200, contentType: 'text/vtt', body: 'WEBVTT\n\n' });
    if (m === 'PUT' && p === `/api/items/${ITEM}/progress`) {
      const { positionSec } = JSON.parse(req.postData());
      return json(route, positionSec < 30 ? null : { positionSec, watched: false });
    }
    if (m === 'POST' && p === `/api/items/${ITEM}/playback`) {
      const body = JSON.parse(req.postData());
      const n = ++posts;
      if (opts.hold) await held;
      const id = `s${n}`;
      const mode = opts.sessions?.(n, body)?.mode ?? 'ok';
      // The echoed startSec is deliberately useless (0): the client must go by what it asked for.
      const pb = opts.hls
        ? { method: 'remux', videoAction: 'copy', audioAction: 'transcode', audioIndex: body.audioIndex ?? 1, reasons: [], url: `/api/sessions/${id}/index.m3u8`, sessionId: id, startSec: 0 }
        : { method: 'direct', videoAction: 'copy', audioAction: 'copy', audioIndex: body.audioIndex ?? 1, reasons: [], url: `/api/items/${ITEM}/file`, startSec: 0 };
      if (opts.hls) sessions.set(id, { mode, deleted: false });
      return json(route, pb);
    }
    const sid = /^\/api\/sessions\/([^/]+)(?:\/(.+))?$/.exec(p);
    if (sid && m === 'DELETE' && !sid[2]) {
      const s = sessions.get(sid[1]);
      if (s) s.deleted = true;
      return route.fulfill({ status: 204 });
    }
    if (sid && m === 'GET' && sid[2]) {
      const s = sessions.get(sid[1]);
      const playlist = sid[2] === 'index.m3u8';
      if (!s || s.deleted || s.mode === 'all404' || (s.mode === 'segments404' && !playlist)) return json(route, { error: 'no such session' }, 404);
      return route.fulfill({
        status: 200,
        contentType: playlist ? 'application/vnd.apple.mpegurl' : 'video/mp2t',
        headers: { 'cache-control': 'no-store' },
        body: readFileSync(join(hlsDir, sid[2])),
      });
    }
    if (m === 'GET' && p === `/api/items/${ITEM}/file`) {
      const range = /bytes=(\d*)-(\d*)/.exec(req.headers().range ?? '');
      const start = range?.[1] ? Number(range[1]) : 0;
      const end = range?.[2] ? Math.min(Number(range[2]), directBytes.length - 1) : directBytes.length - 1;
      return route.fulfill({
        status: range ? 206 : 200,
        headers: { 'content-type': 'video/mp4', 'accept-ranges': 'bytes', 'content-range': `bytes ${start}-${end}/${directBytes.length}` },
        body: directBytes.subarray(start, end + 1),
      });
    }
    unmocked.push(`${m} ${p}`);
    return json(route, { error: `unmocked ${m} ${p}` }, 404);
  });
  if (opts.blockHls) await context.route('**/assets/hls*.js', (route) => route.abort());

  const page = await context.newPage();
  page.on('pageerror', (e) => errors.push(e.message));

  const s = {
    page, context, sessions, errors, unmocked,
    release: () => releaseHold(),
    go: (hash) => page.goto(`${base}/${hash}`),
    all: (method, url) => log.filter((e) => e.method === method && e.url === url),
    puts: () => s.all('PUT', `/api/items/${ITEM}/progress`).map((e) => ({ pos: e.body.positionSec, dur: e.body.durationSec, keepalive: e.keepalive })),
    posts: () => s.all('POST', `/api/items/${ITEM}/playback`).map(({ body: { custom, ...req } }) => req), // minus the browser profile
    deletes: () => log.filter((e) => e.method === 'DELETE').map((e) => ({ id: e.url.split('/').pop(), keepalive: e.keepalive })),
    video: () => page.evaluate(() => {
      const v = document.querySelector('video');
      return v && { t: v.currentTime, paused: v.paused, rs: v.readyState, src: v.currentSrc, d: v.duration };
    }),
    /** Playing, with enough buffered to carry on, at or past `t` (and before `before`). */
    playingAt: (what, t, before = Infinity) => until(what, async () => {
      const v = await s.video();
      return v && v.rs >= 3 && !v.paused && v.t >= t && v.t < before && v;
    }),
    seek: (t) => page.evaluate((t) => { document.querySelector('video').currentTime = t; }, t),
    click: (selector) => page.evaluate((sel) => document.querySelector(sel).click(), selector), // controls auto-hide: skip the mouse
    pick: (menu, nth) => page.evaluate(([menu, nth]) => document.querySelectorAll(`[role=menu][aria-label="${menu}"] [role=menuitemradio]`)[nth].click(), [menu, nth]),
    alert: () => page.evaluate(() => document.querySelector('[role=alert]')?.textContent ?? ''),
  };
  return s;
}

const browser = await chromium.launch({ channel: 'chrome', headless: true, args: ['--autoplay-policy=no-user-gesture-required', '--mute-audio'] });

/** Run one scenario in a fresh context; an exception (a timeout, say) is a failure, not a crash. */
async function scenario(name, opts, body) {
  if (process.env.PLY_E2E_ONLY && !name.includes(process.env.PLY_E2E_ONLY)) return; // e.g. PLY_E2E_ONLY=W3
  const s = await open(browser, opts);
  if (process.env.PLY_E2E_VERBOSE) console.error(`> ${name}`);
  try {
    await body(s);
    check(`${name}: no page errors, no unmocked API call`, !s.errors.length && !s.unmocked.length, [...s.errors, ...s.unmocked].join('; '));
  } catch (e) {
    check(`${name}: ran to the end`, false, e.message);
  } finally {
    await s.context.close();
  }
}

try {
  // W1: direct play. The server echoes startSec 0 (as it used to), and the clip starts at 0 whatever
  // the client says: the client has to seek to the position it asked for, and must not report
  // anything (the server forgets progress below 30 s) before it gets there.
  // Mutants: onLoadedMetadata without the seek; start() calling progress.begin(0); pause not reporting.
  await scenario('W1 direct-play resume', {}, async (s) => {
    await s.go('#/play/1?t=30');
    const v = await s.playingAt('the clip to resume near 30 s', 30, 40);
    check('W1 the resume position was asked for', s.posts()[0]?.startSec === 30, JSON.stringify(s.posts()[0]));
    check('W1 the element was sought to it', v.t >= 30);
    check('W1 nothing was reported on the way (the ticker is 10 s away)', s.puts().length === 0, JSON.stringify(s.puts()));
    await s.page.evaluate(() => document.querySelector('video').pause());
    await until('the report that Pause sends', () => s.puts().length >= 1);
    const [put] = s.puts();
    check('W1 Pause reports the position it was resumed at, not 0', put.pos >= 30 && put.pos < 40, JSON.stringify(put));
    check('W1 with the clip duration', near(put.dur, 60, 1), JSON.stringify(put));
    check('W1 without keepalive (the page is not going anywhere)', put.keepalive === false);
  });

  // W1: the ticker reports every 10 s while playing, and what it reports is never below the resume
  // position (it used to erase it). Its delay is shortened to 250 ms here; the 10 s is checked as asked.
  // Mutants: the ticker at another interval; the ticker reporting from before the stream got there.
  await scenario('W1 progress ticker', { fast: true }, async (s) => {
    await s.go('#/play/1?t=30');
    await s.playingAt('the clip to resume near 30 s', 30, 40);
    await until('two reports from the ticker', () => s.puts().length >= 2);
    const intervals = await s.page.evaluate(() => window.__intervals);
    check('W1 the ticker runs every 10 s', intervals.includes(10000), String(intervals));
    const puts = s.puts();
    check('W1 no report below the resume position (it would erase it)', puts.every((p) => p.pos >= 30), JSON.stringify(puts));
    check('W1 each report is further on', puts.every((p, i) => i === 0 || p.pos > puts[i - 1].pos), JSON.stringify(puts));
  });

  // W2: a failed stream. Where it got to is reported before the element is torn down, tearing it
  // down (which fires timeupdate at 0) reports nothing, leaving afterwards reports nothing, and
  // Retry restarts where it failed.
  // Mutants: fail() without its progress.report(); retry() starting at 0; release() without progress.end().
  await scenario('W2 failure, Retry and leaving', {}, async (s) => {
    await s.go('#/play/1?t=20');
    await s.playingAt('the clip to play on from 20 s', 22.5, 30);
    const at = (await s.video()).t;
    await s.page.evaluate(() => document.querySelector('video').dispatchEvent(new Event('error')));
    await until('the error overlay', async () => (await s.alert()).includes('Cannot play this item'));
    await until('a report of the failure position', () => s.puts().some((p) => near(p.pos, at, 2)));
    await sleep(600); // the timeupdate of the unloaded element
    check('W2 nothing reported from the unloaded element', s.puts().every((p) => p.pos >= 20), JSON.stringify(s.puts()));
    const posts = s.posts().length;
    await s.click('.overlay .btn');
    await until('the new playback request', () => s.posts().length > posts);
    const sent = s.posts().at(-1).startSec;
    check('W2 Retry starts where playback failed', near(sent, at, 2), `asked for ${sent}, failed at ${at}`);
    // Not where the last timeupdate was (a quarter of a second earlier): where the report said.
    const reported = s.puts().find((p) => near(p.pos, at, 2));
    check('W2 exactly where the failure was reported', sent === reported.pos, `asked for ${sent}, reported ${reported.pos}`);
    await s.playingAt('Retry to resume', sent - 1, sent + 6);
    await s.click('.play-page a');
    await until('the player to be gone', async () => (await s.page.locator('video').count()) === 0);
    await sleep(300);
    check('W2 leaving after a failure erased nothing', s.puts().every((p) => p.pos >= 20), JSON.stringify(s.puts()));
  });

  // W3: the lazy hls.js chunk will not load. The player says so (it used to spin forever), the
  // session the POST opened is deleted, and Retry reloads the page at the position (a failed
  // dynamic import stays failed until then), after which it plays.
  // Mutants: no try/catch around the import; chunkFailed never set.
  await scenario('W3 hls.js chunk fails to load', { hls: true, blockHls: true }, async (s) => {
    await s.go('#/play/1?t=12');
    await until('the error overlay', async () => (await s.alert()).includes('Could not load the video player'));
    check('W3 the session opened for it is deleted', s.deletes().some((d) => d.id === 's1'), JSON.stringify(s.deletes()));
    check('W3 nothing was reported (nothing played)', s.puts().length === 0, JSON.stringify(s.puts()));
    await s.context.unroute('**/assets/hls*.js');
    await s.page.evaluate(() => { window.__sameDocument = true; });
    await s.page.locator('.overlay .btn').click(); // a real click: this one navigates
    await s.playingAt('the reloaded page to play', 12, 20);
    check('W3 Retry reloaded the page', await s.page.evaluate(() => window.__sameDocument !== true));
    check('W3 at the same position', new URL(s.page.url()).hash === '#/play/1?t=12', s.page.url());
  });

  // W4: the back/forward cache. pagehide reports (keepalive) and ends the session; a persisted
  // pageshow starts again where it was; a pageshow that is not from the cache does nothing.
  // Mutants: no pageshow handler; the persisted test dropped; no pagehide handler; report() instead of report(true).
  await scenario('W4 back/forward cache', { hls: true }, async (s) => {
    await s.go('#/play/1?t=5');
    await s.playingAt('hls.js to play from 5 s', 7, 20);
    await s.seek(40);
    const v = await s.playingAt('the seek to 40 s', 40, 46);
    const putsBefore = s.puts().length;
    await s.page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true })));
    await until('the report of pagehide', () => s.puts().length > putsBefore);
    const last = s.puts().at(-1);
    check('W4 pagehide reports the position', last.pos >= 40 && last.pos < 50, JSON.stringify(last));
    check('W4 and does so with keepalive', last.keepalive === true);
    await until('the session to be deleted', () => s.deletes().some((d) => d.id === 's1'));
    check('W4 deleting the session survives the unload', s.deletes().find((d) => d.id === 's1').keepalive === true);
    check('W4 pagehide starts no playback', s.posts().length === 1, JSON.stringify(s.posts()));

    await s.page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: false })));
    await sleep(1500);
    check('W4 a pageshow that is not from the cache starts nothing', s.posts().length === 1, JSON.stringify(s.posts()));

    await s.page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })));
    await until('the restart', () => s.posts().length === 2);
    check('W4 the restore starts at the position', near(s.posts()[1].startSec, v.t, 3), `${s.posts()[1].startSec} vs ${v.t}`);
    await s.playingAt('the restored stream to play', 40, 55);
  });

  // The page being hidden (iOS often fires no pagehide): report with keepalive.
  // Mutants: no visibilitychange handler; report() without keepalive there.
  await scenario('W4 page hidden', {}, async (s) => {
    await s.go('#/play/1?t=40');
    await s.playingAt('the clip to resume at 40 s', 41, 50);
    const before = s.puts().length;
    await s.page.evaluate(() => {
      Object.defineProperty(document, 'hidden', { configurable: true, get: () => true });
      document.dispatchEvent(new Event('visibilitychange'));
    });
    await until('the report of the page being hidden', () => s.puts().length > before);
    const last = s.puts().at(-1);
    check('W4 hiding the page reports the position, with keepalive', last.pos >= 40 && last.keepalive === true, JSON.stringify(last));
  });

  // Leaving the player (route change): report with keepalive, end the session.
  // Mutants: the unmount cleanup without leave(); leave() without the report or with report().
  await scenario('W1/W4 leaving the player', { hls: true }, async (s) => {
    await s.go('#/play/1?t=30');
    await s.playingAt('hls.js to resume at 30 s', 31, 40);
    await s.click('.play-page a');
    await until('the session to be deleted', () => s.deletes().some((d) => d.id === 's1'));
    const last = s.puts().at(-1);
    check('leaving reports the position', last && last.pos >= 30, JSON.stringify(s.puts()));
    check('leaving reports it with keepalive', last?.keepalive === true);
    check('leaving deletes the session with keepalive', s.deletes().find((d) => d.id === 's1').keepalive === true);
    check('leaving reports nothing below the resume position', s.puts().every((p) => p.pos >= 30), JSON.stringify(s.puts()));
  });

  // A playback request answered after the viewer has left: its session is deleted at once.
  // Mutant: the stale-response branch of start() without deleteSession.
  await scenario('W4 playback answered after leaving', { hls: true, hold: true }, async (s) => {
    await s.go('#/play/1?t=5');
    await until('the playback request', () => s.posts().length === 1);
    await s.click('.play-page a');
    await until('the player to be gone', async () => (await s.page.locator('video').count()) === 0);
    s.release();
    await until('the late session to be deleted', () => s.deletes().some((d) => d.id === 's1'));
    check('nothing was reported for a stream that never played', s.puts().length === 0, JSON.stringify(s.puts()));
  });

  // M5: switching audio restarts the stream at the position, ends the old session, loses nothing and
  // reports no 0; choosing the track already playing does nothing.
  // Mutants: restart() starting at 0; start() without its report() of the old stream; selectAudio
  // without the "already playing" guard; release() without deleteSession.
  await scenario('M5 audio track switch', { hls: true }, async (s) => {
    await s.go('#/play/1?t=10');
    await s.playingAt('hls.js to resume at 10 s', 12, 20);
    const at = (await s.video()).t;
    await s.click('button[aria-label="Audio track"]');
    await s.pick('Audio track', 1);
    await until('the second playback request', () => s.posts().length === 2);
    const second = s.posts()[1];
    check('M5 the new track is asked for', second.audioIndex === 2, JSON.stringify(second));
    check('M5 at the position it had', near(second.startSec, at, 3), `${second.startSec} vs ${at}`);
    check('M5 the old session is ended', s.deletes().some((d) => d.id === 's1'), JSON.stringify(s.deletes()));
    check('M5 the old stream reported where it was', s.puts().some((p) => near(p.pos, at, 3)), JSON.stringify(s.puts()));
    await s.playingAt('the new stream to play on from there', second.startSec - 1, second.startSec + 8);
    check('M5 nothing below the resume position was reported', s.puts().every((p) => p.pos >= 10), JSON.stringify(s.puts()));
    await s.click('button[aria-label="Audio track"]');
    await s.pick('Audio track', 1);
    await sleep(1200);
    check('M5 choosing the track that is playing starts nothing', s.posts().length === 2, JSON.stringify(s.posts()));
  });

  // M5, subtitles: a text track is shown by the browser, a bitmap track is burned in by a new playback
  // at the position (and removing it is another); the file's own default is a text track only.
  // Mutants: selectSubtitle restarting for text tracks; wasBurning dropped; a default taken from bitmap tracks.
  await scenario('M5 subtitle switch', {
    item: {
      subtitles: [
        { key: '3', language: 'eng', format: 'dvd_subtitle', image: true, default: true },
        { key: '2', language: 'eng', format: 'subrip', image: false, default: true, url: '/api/items/1/subtitles/2.vtt' },
      ],
    },
  }, async (s) => {
    await s.go('#/play/1?t=10');
    await s.playingAt('the clip to resume at 10 s', 12, 20);
    const tracks = () => s.page.evaluate(() => [...document.querySelectorAll('video track')].map((t) => t.track.mode));
    check('M5 the default text track is showing', JSON.stringify(await tracks()) === '["showing"]', JSON.stringify(await tracks()));
    check('M5 a default bitmap track is not burned in unasked', s.posts()[0].subtitleIndex === undefined, JSON.stringify(s.posts()[0]));
    const at = (await s.video()).t;

    await s.click('button[aria-label="Subtitles"]');
    await s.pick('Subtitles', 1); // the bitmap track (menu: Off, then the tracks in file order)
    await until('the burn-in playback', () => s.posts().length === 2);
    check('M5 the bitmap track is burned in', s.posts()[1].subtitleIndex === 3, JSON.stringify(s.posts()[1]));
    check('M5 at the position it had', near(s.posts()[1].startSec, at, 3), `${s.posts()[1].startSec} vs ${at}`);
    check('M5 the text track is gone', JSON.stringify(await tracks()) === '[]', JSON.stringify(await tracks()));
    await s.playingAt('the burned-in stream to play', 12, 25);

    await s.click('button[aria-label="Subtitles"]');
    await s.pick('Subtitles', 0); // Off
    await until('the playback without it', () => s.posts().length === 3);
    check('M5 dropping a burned-in track restarts without it', s.posts()[2].subtitleIndex === undefined, JSON.stringify(s.posts()[2]));
    await s.playingAt('the stream to play', 12, 30);

    await s.click('button[aria-label="Subtitles"]');
    await s.pick('Subtitles', 2); // the text track
    await until('the text track to show', async () => JSON.stringify(await tracks()) === '["showing"]');
    await sleep(800);
    check('M5 a text track starts no playback', s.posts().length === 3, JSON.stringify(s.posts()));
  });

  // S5: the server dropped the session (a paused player with a full buffer asks for nothing for a
  // while). The first 404 starts a new session at the position the viewer is at, and ends the dead one.
  // Mutants: sessionGone() not consulted; the restart not at the current position.
  await scenario('S5 the session expired', { hls: true }, async (s) => {
    await s.go('#/play/1?t=5');
    await s.playingAt('hls.js to play from 5 s', 7, 20);
    s.sessions.get('s1').mode = 'segments404';
    await s.seek(56); // beyond what is buffered: a segment request, which is answered 404
    await until('the new playback request', () => s.posts().length === 2);
    check('S5 the new session starts where the viewer is', near(s.posts()[1].startSec, 56, 2), JSON.stringify(s.posts()[1]));
    check('S5 the dead session is ended', s.deletes().some((d) => d.id === 's1'), JSON.stringify(s.deletes()));
    await s.playingAt('the new session to play', 56, 60);
    check('S5 no error was shown', (await s.alert()) === '');
  });

  // S5: a restart does not count against the next one once playback runs again (canplay resets it);
  // three sessions in a row die and each is replaced.
  // Mutant: canplay without sessionRestarts = 0.
  await scenario('S5 restarts are counted per outage', { hls: true }, async (s) => {
    await s.go('#/play/1?t=5');
    await s.playingAt('hls.js to play from 5 s', 7, 20);
    let n = 1;
    for (const to of [56, 10, 56]) {
      s.sessions.get(`s${n}`).mode = 'segments404';
      await s.seek(to);
      await until(`playback request ${n + 1}`, () => s.posts().length === n + 1);
      n++;
      await s.playingAt(`session ${n} to play at ${to}`, to, to + 10);
    }
    check('S5 four sessions in all, no error', s.posts().length === 4 && (await s.alert()) === '', JSON.stringify(s.posts()));
  });

  // S5: sessions that are dead on arrival are retried twice, then the player stops with an error
  // instead of going on for ever.
  // Mutants: the limit raised or removed; the fatal error ignored once the limit is reached.
  await scenario('S5 gives up after two restarts', { hls: true, sessions: () => ({ mode: 'all404' }) }, async (s) => {
    await s.go('#/play/1?t=10');
    await until('the error overlay', async () => (await s.alert()).includes('Stream error'), 30000);
    await sleep(2500);
    check('S5 one playback and two restarts, no more', s.posts().length === 3, JSON.stringify(s.posts()));
    check('S5 every restart at the same place', s.posts().every((p) => p.startSec === 10), JSON.stringify(s.posts()));
    check('S5 every dead session is ended', ['s1', 's2', 's3'].every((id) => s.deletes().some((d) => d.id === id)), JSON.stringify(s.deletes()));
  });
} finally {
  await browser.close();
  server.close();
  rmSync(work, { recursive: true, force: true });
}
console.log(results.join('\n'));
process.exit(results.some((r) => r.startsWith('FAIL')) ? 1 : 0);
