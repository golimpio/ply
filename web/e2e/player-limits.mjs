// The player's heartbeat and the limits on plays (docs/M9-ARCHITECTURE.md, "Orphan-proof playback"
// and "Maximum parallel playback"), in a real browser against a mocked API: the beats an HLS session
// gets (with the position, paused or not), the new session after a 404, the panel a play at the limit
// shows instead of a broken video (what is playing, Stop, Try again, the countdown), and the Playback
// card of Settings. The unit tests (lib/heartbeat.test.ts, lib/playlimit.test.ts) pin the modules;
// this pins that Player.svelte, PlayLimit.svelte and PlaybackLimits.svelte drive them.
//
// Self-contained: serves web/dist (build it first: `npm run build`; PLY_E2E_DIST picks another
// directory), answers every /api/ request with page.route, and builds its media with ffmpeg. Needs
// Google Chrome (Playwright's bundled Chromium lacks H.264/AAC) and ffmpeg on PATH.
//
//   npm run build && node e2e/player-limits.mjs
//
// PLY_E2E_ONLY=<text> runs the scenarios whose name contains it; PLY_E2E_VERBOSE=1 names each one as
// it starts; PLY_E2E_SHOTS=<dir> saves screenshots of the cap panel and of the Playback card there.
// Each scenario names the mutation it was confirmed to catch (build the mutant into another
// directory with `vite build --outDir ... --emptyOutDir` and point PLY_E2E_DIST at it).
import { createServer } from 'node:http';
import { execFileSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, extname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright-core';
import { eventStream, jobsSnapshot } from './mock-feed.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const dist = process.env.PLY_E2E_DIST ?? join(here, '..', 'dist');
if (!existsSync(join(dist, 'index.html'))) {
  console.error(`No build in ${dist}: run \`npm run build\` first.`);
  process.exit(2);
}
const shots = process.env.PLY_E2E_SHOTS;
if (shots) mkdirSync(shots, { recursive: true });

const results = [];
const check = (name, ok, detail = '') => results.push(`${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? `  (${detail})` : ''}`);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const near = (x, want, tol) => Math.abs(x - want) <= tol;

/** Poll until fn returns something truthy; throws, so a scenario fails instead of hanging. */
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

const work = mkdtempSync(join(tmpdir(), 'ply-limits-'));
const directFile = join(work, 'direct.mp4');
const hlsDir = join(work, 'hls');
execFileSync('ffmpeg', ['-v', 'error', '-y', '-f', 'lavfi', '-i', 'testsrc2=s=320x180:r=10:d=60', '-f', 'lavfi', '-i', 'sine=f=440:d=60',
  '-c:v', 'libx264', '-preset', 'ultrafast', '-b:v', '150k', '-g', '20', '-keyint_min', '20', '-sc_threshold', '0', '-pix_fmt', 'yuv420p',
  '-c:a', 'aac', '-b:a', '32k', '-movflags', '+faststart', directFile]);
mkdirSync(hlsDir);
execFileSync('ffmpeg', ['-v', 'error', '-y', '-i', directFile, '-c', 'copy', '-f', 'hls', '-hls_time', '2', '-hls_playlist_type', 'vod',
  '-hls_segment_filename', join(hlsDir, 'seg%03d.ts'), join(hlsDir, 'index.m3u8')]);
const directBytes = readFileSync(directFile);

// ---- the app: web/dist over http -----------------------------------------------------------------

const types = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.json': 'application/json', '.png': 'image/png', '.svg': 'image/svg+xml' };
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
const detail = {
  id: ITEM, kind: 'movie', title: 'Limits Test', year: 2020, durationSec: 60, container: 'mp4', videoCodec: 'h264', audioCodec: 'aac',
  width: 320, height: 180, addedAt: 1, progress: null, art: null, path: '/m/limits.mp4', size: 1, bitrate: 1,
  streams: [
    { index: 0, type: 'video', codec: 'h264' },
    { index: 1, type: 'audio', codec: 'aac', language: 'eng', channels: 2 },
    { index: 2, type: 'audio', codec: 'aac', language: 'fra', channels: 2 },
  ],
  subtitles: [],
  playback: { method: 'direct', videoAction: 'copy', audioAction: 'copy', audioIndex: 1, reasons: [], url: '', startSec: 0 },
};
const ago = (sec) => new Date(Date.now() - sec * 1000).toISOString();
const PLAYING = [
  { id: 'abc', sessionId: 'abc', itemId: 7, title: 'Alien', kind: 'transcode', client: '192.168.1.20 · Safari', startedAt: ago(900), lastSeenAt: ago(2), positionSec: 754, paused: true },
  { id: 'def', sessionId: 'def', itemId: 8, title: 'Arrival', kind: 'direct', client: '192.168.1.31 · Chrome', startedAt: ago(300), lastSeenAt: ago(1), positionSec: 3725, paused: false },
];
const capBody = (over = {}) => ({ error: '2 of 2 plays in use', limit: 'streams', max: 2, inUse: 2, playing: PLAYING, ...over });
const SETTINGS = {
  tmdbKeySet: false, tmdbKeySource: '', metadataEnabled: false, metadataForcedOff: true, language: 'en-US',
  maxStreams: 4, maxTranscodes: 1, maxStreamsSource: 'settings', maxTranscodesSource: 'default',
};

/**
 * One browser context against one mocked server.
 *   hls        every playback is a remux session served through hls.js (else a direct play)
 *   refuse     (n, st) -> a 503 body for the n-th POST /playback, or nothing to accept it
 *   retryAfter the Retry-After of those 503s (default 5)
 *   slowPost   (n) -> ms to hold the n-th POST /playback before answering it
 *   deleteDelay ms to hold a DELETE of one of the other plays (PLAYING) before answering it
 *   file503    GET /file answers 503 with the cap body
 *   fastBeat   run the 10 s timers (heartbeat, progress) every 300 ms; the asked delays are in window.__intervals
 *   settings   GET /api/settings (settingsStatus: its status); put(body, st) -> { status, body } answers PUT
 *              /api/settings; putDelay(body) -> ms before the PUT is answered, and applied (as if the
 *              requests crossed on the way to the server)
 * `log` holds every non-GET call: { method, path, body, at }; `beats` every heartbeat: { sid, positionSec, paused, at }.
 * Each session has flags the scenarios set: beat404, beat410 (its heartbeat only), stopped (everything
 * answers 410, as after DELETE from elsewhere), segments404. st.fileStopped: GET /file answers 410.
 * st.probes counts the one-byte GETs of the file (Range: bytes=0-0), st.maxPutsInFlight the PUTs at once.
 */
async function open(browser, opts = {}) {
  const context = await browser.newContext({ viewport: { width: 960, height: 640 }, serviceWorkers: 'block' });
  const st = { full: true, settings: opts.settings ?? SETTINGS, fileStopped: !!opts.fileStopped, probes: 0, putsInFlight: 0, maxPutsInFlight: 0 };
  const log = [];
  const beats = [];
  const sessions = new Map();
  const errors = [];
  const unmocked = [];
  let posts = 0;

  await context.addInitScript(({ fast }) => {
    // hls.js, not the browser's own HLS
    const can = HTMLMediaElement.prototype.canPlayType;
    HTMLMediaElement.prototype.canPlayType = function (type) {
      return /mpegurl/i.test(type) ? '' : can.call(this, type);
    };
    window.__intervals = [];
    const realSetInterval = window.setInterval.bind(window);
    window.setInterval = (fn, ms, ...args) => {
      window.__intervals.push(ms);
      return realSetInterval(fn, fast && ms === 10000 ? 300 : ms, ...args);
    };
  }, { fast: !!opts.fastBeat });

  const json = (route, body, status = 200, headers = {}) =>
    route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body), headers: { 'cache-control': 'no-store', ...headers } });
  const refusal = (route, body) => json(route, body, 503, { 'Retry-After': String(opts.retryAfter ?? 5) });
  await context.route((u) => u.pathname.startsWith('/api/'), async (route) => {
    const req = route.request();
    const { pathname: p } = new URL(req.url());
    const m = req.method();
    const body = req.postData() ? JSON.parse(req.postData()) : null;
    if (m === 'PUT' && p.startsWith('/api/devices/')) return route.fulfill({ status: 204 }); // the app says who it is (M11)
    if (m !== 'GET') log.push({ method: m, path: p, body, at: Date.now() });
    if (m === 'GET' && p === '/api/info') return json(route, { id: 'x', name: 'limits', version: '0', authRequired: false });
    if (m === 'GET' && p === '/api/scan') return json(route, { running: false, itemCount: 1 });
    if (m === 'GET' && ['/api/items', '/api/shows', '/api/continue', '/api/upcoming', '/api/genres', '/api/folders'].includes(p)) return json(route, []);
    if (m === 'GET' && p === '/api/jobs') return json(route, jobsSnapshot());
    if (m === 'GET' && p === '/api/events')
      return route.fulfill(eventStream());
    if (m === 'GET' && p === '/api/settings') return json(route, st.settings, opts.settingsStatus ?? 200);
    if (m === 'PUT' && p === '/api/settings') {
      st.maxPutsInFlight = Math.max(st.maxPutsInFlight, ++st.putsInFlight);
      const delay = opts.putDelay?.(body) ?? 0;
      if (delay) await sleep(delay);
      st.putsInFlight--;
      const a = opts.put?.(body, st) ?? { status: 200, body: (st.settings = { ...st.settings, ...body }) };
      return json(route, a.body, a.status);
    }
    if (m === 'GET' && p === '/api/metadata/status') return json(route, { enabled: false, keySet: false, running: false, pending: 0, unmatched: 0, needsReview: 0, lastError: '' });
    if (m === 'GET' && p === '/api/metadata/review') return json(route, []);
    if (m === 'GET' && p === `/api/items/${ITEM}`) return json(route, detail);
    if (m === 'PUT' && p === `/api/items/${ITEM}/progress`) return json(route, null);
    if (m === 'POST' && p === `/api/items/${ITEM}/playback`) {
      const n = ++posts;
      const hold = opts.slowPost?.(n);
      if (hold) await sleep(hold);
      const no = opts.refuse?.(n, st);
      if (no) return refusal(route, no);
      if (opts.post410?.(n)) return json(route, { error: 'stopped', stoppedBy: 'a newer attempt' }, 410);
      const id = `s${n}`;
      if (!opts.hls) return json(route, { method: 'direct', videoAction: 'copy', audioAction: 'copy', audioIndex: 1, reasons: [], url: `/api/items/${ITEM}/file`, startSec: 0 });
      sessions.set(id, { deleted: false, beat404: false, beat410: false, stopped: false, segments404: false });
      return json(route, { method: 'remux', videoAction: 'copy', audioAction: 'transcode', audioIndex: 1, reasons: [], url: `/api/sessions/${id}/index.m3u8`, sessionId: id, startSec: 0 });
    }
    const sid = /^\/api\/sessions\/([^/]+)(?:\/(.+))?$/.exec(p);
    if (sid && m === 'DELETE' && !sid[2]) {
      if (opts.deleteDelay && PLAYING.some((x) => x.id === sid[1])) await sleep(opts.deleteDelay);
      const s = sessions.get(sid[1]);
      if (s) s.deleted = true;
      if (PLAYING.some((x) => x.id === sid[1])) st.full = false; // one of the others was stopped: there is room
      return route.fulfill({ status: 204 });
    }
    const stopped = (r) => json(r, { error: 'stopped', stoppedBy: 'another device' }, 410);
    if (sid && m === 'POST' && sid[2] === 'heartbeat') {
      beats.push({ sid: sid[1], ...body, at: Date.now() });
      const s = sessions.get(sid[1]);
      if (s?.stopped || s?.beat410) return stopped(route);
      return s && !s.deleted && !s.beat404 ? route.fulfill({ status: 204 }) : json(route, { error: 'session not found' }, 404);
    }
    if (sid && m === 'GET' && sid[2]) {
      const s = sessions.get(sid[1]);
      if (s?.stopped) return stopped(route);
      const playlist = sid[2] === 'index.m3u8';
      if (!s || s.deleted || (s.segments404 && !playlist)) return json(route, { error: 'no such session' }, 404);
      return route.fulfill({ status: 200, contentType: playlist ? 'application/vnd.apple.mpegurl' : 'video/mp2t', body: readFileSync(join(hlsDir, sid[2])) });
    }
    if (m === 'GET' && p === `/api/items/${ITEM}/file`) {
      if (req.headers().range === 'bytes=0-0') st.probes++;
      if (st.fileStopped) return stopped(route);
      if (opts.file503) return refusal(route, capBody());
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

  const page = await context.newPage();
  page.on('pageerror', (e) => errors.push(e.message));
  const s = {
    page, context, st, log, beats, sessions, errors, unmocked,
    go: (hash) => page.goto(`${base}/${hash}`),
    posts: () => log.filter((e) => e.method === 'POST' && e.path === `/api/items/${ITEM}/playback`),
    deletes: () => log.filter((e) => e.method === 'DELETE').map((e) => e.path.split('/').pop()),
    puts: () => log.filter((e) => e.method === 'PUT' && e.path === '/api/settings').map((e) => e.body),
    video: () => page.evaluate(() => {
      const v = document.querySelector('video');
      return v && { t: v.currentTime, paused: v.paused, rs: v.readyState };
    }),
    playingAt: (what, t, before = Infinity, ms = 10000) => until(what, async () => {
      const v = await s.video();
      return v && v.rs >= 3 && !v.paused && v.t >= t && v.t < before && v;
    }, ms),
    seek: (t) => page.evaluate((t) => { document.querySelector('video').currentTime = t; }, t),
    pause: () => page.evaluate(() => document.querySelector('video').pause()),
    play: () => page.evaluate(() => document.querySelector('video').play()),
    text: (sel) => page.evaluate((sel) => document.querySelector(sel)?.textContent?.replace(/\s+/g, ' ').trim() ?? '', sel),
    panel: () => page.locator('.overlay.limit'),
    status: (field) => page.evaluate((f) => document.querySelector(`[data-limit="${f}"] + [role=status]`)?.textContent?.trim() ?? '', field),
  };
  return s;
}

const browser = await chromium.launch({ channel: 'chrome', headless: true, args: ['--autoplay-policy=no-user-gesture-required', '--mute-audio'] });

async function scenario(name, opts, body) {
  if (process.env.PLY_E2E_ONLY && !name.includes(process.env.PLY_E2E_ONLY)) return;
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

const full = (n, st) => (st.full ? capBody() : null);

try {
  // ---- the heartbeat ----

  // HB1: an HLS session gets a beat every 10 s (shortened here), with where the player is.
  // Mutants: heartbeat.start() never called for the session; the beat sending 0 for the position;
  // HEARTBEAT_MS other than 10 s (only a 10 s timer is shortened, so the beats would stop coming).
  await scenario('HB1 beats carry the position', { hls: true, fastBeat: true }, async (s) => {
    await s.go('#/play/1?t=10');
    await s.playingAt('hls.js to play from 10 s', 11, 20);
    await until('three beats', () => s.beats.length >= 3);
    // The beats themselves, not the progress ticker's 10 s timer: playing on with no pause or play,
    // they keep coming at the shortened pace, which only a timer asked for 10 s gets.
    const from = s.beats.length;
    await sleep(1500);
    const more = s.beats.length - from;
    check('HB1 the heartbeat runs on the 10 s timer', more >= 3 && more <= 7, `${more} beats in 1.5 s at 300 ms`);
    const v = await s.video();
    check('HB1 the beats are for the session', s.beats.every((b) => b.sid === 's1'), JSON.stringify(s.beats));
    check('HB1 with the position (never below the resume point)', s.beats.every((b) => b.positionSec >= 9.5), JSON.stringify(s.beats.map((b) => b.positionSec)));
    check('HB1 the last one where the player is', near(s.beats.at(-1).positionSec, v.t, 1.5), `${s.beats.at(-1).positionSec} vs ${v.t}`);
    check('HB1 not paused', s.beats.every((b) => b.paused === false), JSON.stringify(s.beats));
  });

  // HB2: pausing and playing beat at once (the timer is the real 10 s here: these beats are not its).
  // Mutants: onPause without heartbeat.beat(); onPlay without heartbeat.beat().
  await scenario('HB2 a beat at once on pause and on play', { hls: true }, async (s) => {
    await s.go('#/play/1?t=10');
    await s.playingAt('hls.js to play from 10 s', 11, 20);
    const before = s.beats.length;
    const t0 = Date.now();
    await s.pause();
    const paused = await until('the beat of the pause', () => s.beats.slice(before).find((b) => b.paused === true), 2000);
    check('HB2 the pause beats at once, saying so', paused.at - t0 < 1500, `${paused.at - t0} ms`);
    const v = await s.video();
    check('HB2 with the paused position', near(paused.positionSec, v.t, 0.5), `${paused.positionSec} vs ${v.t}`);
    const n = s.beats.length;
    await s.play();
    await until('the beat of the play', () => s.beats.slice(n).find((b) => b.paused === false), 2000);
  });

  // HB3: a paused player asks for no segments, so it is the beats that keep its session: they go on.
  // Mutant: the 10 s timer skipping its beat while the player is paused (the "no beat while paused" mutant).
  await scenario('HB3 beats go on while paused', { hls: true, fastBeat: true }, async (s) => {
    await s.go('#/play/1?t=10');
    await s.playingAt('hls.js to play from 10 s', 11, 20);
    await s.pause();
    await sleep(100);
    const from = s.beats.length;
    await until('three more beats while paused', () => s.beats.length >= from + 3, 4000);
    check('HB3 every one says paused', s.beats.slice(from).every((b) => b.paused === true), JSON.stringify(s.beats.slice(from)));
    check('HB3 for the same session (nothing restarted)', s.posts().length === 1 && s.beats.slice(from).every((b) => b.sid === 's1'));
  });

  // HB4: the session is gone (404): a new playback at the position, the dead session ended, and a
  // player that was paused stays paused. The next beats are for the new session.
  // Mutant: onGone doing nothing (the "no recovery on a 404" mutant); start() ignoring `play`.
  await scenario('HB4 a 404 starts a new session at the position', { hls: true }, async (s) => {
    await s.go('#/play/1?t=20');
    await s.playingAt('hls.js to play from 20 s', 21, 30);
    s.sessions.get('s1').beat404 = true;
    await s.pause();
    const at = (await s.video()).t;
    await until('the new playback request', () => s.posts().length === 2, 4000);
    check('HB4 at the position it was paused at', near(s.posts()[1].body.startSec, at, 0.5), `${s.posts()[1].body.startSec} vs ${at}`);
    check('HB4 the dead session is ended', s.deletes().includes('s1'), JSON.stringify(s.deletes()));
    await until('the new stream to load', async () => (await s.video())?.rs >= 2);
    await sleep(500);
    const v = await s.video();
    check('HB4 it stays paused', v.paused === true, JSON.stringify(v));
    check('HB4 where it was', near(v.t, at, 2), `${v.t} vs ${at}`);
    check('HB4 no error shown', !(await s.text('[role=alert]')).includes('Cannot play'));
    const n = s.beats.length;
    await s.play();
    const next = await until('a beat for the new session', () => s.beats.slice(n).find((b) => b.sid === 's2'), 2000);
    check('HB4 the beats moved to the new session', next.paused === false);
    check('HB4 one recovery only', s.posts().length === 2, JSON.stringify(s.posts().map((p) => p.body)));
  });

  // HB5: closing the player stops the beats (and ends the session, as before); a direct play has none.
  // Mutants: release() without heartbeat.stop(); a beat started for a play without a session.
  await scenario('HB5 closing stops the beats', { hls: true, fastBeat: true }, async (s) => {
    await s.go('#/play/1?t=10');
    await s.playingAt('hls.js to play from 10 s', 11, 20);
    await until('a beat', () => s.beats.length >= 2);
    await s.page.evaluate(() => document.querySelector('button[aria-label="Close player"]').click());
    await until('the session to be deleted', () => s.deletes().includes('s1'));
    const n = s.beats.length;
    await sleep(1500);
    check('HB5 no beat after closing', s.beats.length === n, `${s.beats.length - n} more`);
  });
  await scenario('HB5 a direct play sends no beat', { fastBeat: true }, async (s) => {
    await s.go('#/play/1?t=10');
    await s.playingAt('the direct play', 11, 20);
    await s.pause();
    await sleep(1200);
    check('HB5 none for a direct play', s.beats.length === 0, JSON.stringify(s.beats));
  });

  // ---- the limits ----

  // CAP1: a play at the limit shows the panel: why, what is playing (title, client, position), a Stop
  // for each, Try again, the countdown and the way to Settings. No broken video.
  // Mutant: start() not looking for the limit (limitOf) in the error: the generic error shows instead.
  await scenario('CAP1 the panel at the limit of plays', { refuse: full }, async (s) => {
    await s.go('#/play/1?t=30');
    await s.panel().waitFor({ timeout: 8000 });
    const text = await s.text('.overlay.limit');
    check('CAP1 says why', text.includes('2 videos are already playing, the most this server allows.'), text);
    const rows = await s.page.evaluate(() => [...document.querySelectorAll('.overlay.limit li')].map((li) => li.textContent.replace(/\s+/g, ' ').trim()));
    check('CAP1 lists what is playing', rows.length === 2 && rows[0].includes('Alien') && rows[1].includes('Arrival'), JSON.stringify(rows));
    check('CAP1 with the client and the position', rows[0].includes('192.168.1.20 · Safari') && rows[0].includes('Paused at 12:34') && rows[1].includes('At 1:02:05'), JSON.stringify(rows));
    check('CAP1 a Stop for each', (await s.page.getByRole('button', { name: /^Stop / }).count()) === 2);
    check('CAP1 Try again', await s.page.getByRole('button', { name: 'Try again' }).isVisible());
    check('CAP1 the countdown', /Trying again in [45] s/.test(text), text);
    const link = s.page.getByRole('link', { name: 'Raise the limit in Settings' });
    check('CAP1 a link to Settings', (await link.getAttribute('href')) === '#/settings?tab=playback');
    check('CAP1 no error overlay', !(await s.text('[role=alert]')).includes('Cannot play'));
    check('CAP1 the resume position was asked for', s.posts()[0].body.startSec === 30);
    if (shots) await s.page.screenshot({ path: join(shots, 'cap-panel.png') });
    await link.click();
    await until('Settings', async () => (await s.page.locator('#playback-h').count()) === 1);
    await until('the Playback card in view', () => s.page.evaluate(() => {
      const r = document.querySelector('#max-streams')?.getBoundingClientRect();
      return r && r.top >= 0 && r.bottom <= innerHeight;
    }), 3000);
  });

  // CAP2: Stop ends the other play (DELETE /api/sessions/{id}) and tries again at once, which plays.
  // Mutant: Stop without api.stopSession (the "no Stop in the cap panel" mutant); Stop not retrying.
  await scenario('CAP2 Stop, then the retry plays', { refuse: full }, async (s) => {
    await s.go('#/play/1?t=30');
    await s.panel().waitFor({ timeout: 8000 });
    const shown = Date.now();
    await s.page.getByRole('button', { name: 'Stop Arrival' }).click();
    await until('the second playback request', () => s.posts().length === 2, 3000);
    const del = s.log.find((e) => e.method === 'DELETE');
    check('CAP2 Stop ends that play', del?.path === '/api/sessions/def', JSON.stringify(s.deletes()));
    check('CAP2 then tries again at once (not the countdown)', del && s.posts()[1].at >= del.at && s.posts()[1].at - shown < 3500, `${s.posts()[1].at - shown} ms`);
    check('CAP2 at the same position', s.posts()[1].body.startSec === 30, JSON.stringify(s.posts()[1].body));
    await s.playingAt('the video to play', 30, 40);
    check('CAP2 the panel is gone', (await s.panel().count()) === 0);
  });

  // CAP3: the countdown is visible, and runs out into a retry on its own; a new refusal starts it over.
  // Mutants: no countdown timer in PlayLimit (it never retries on its own); the countdown not ticking down.
  await scenario('CAP3 the countdown retries on its own', { retryAfter: 3, refuse: (n) => (n <= 2 ? capBody() : null) }, async (s) => {
    await s.go('#/play/1?t=30');
    await s.panel().waitFor({ timeout: 8000 });
    const first = s.posts()[0].at;
    check('CAP3 starts at Retry-After', (await s.text('.countdown')) === 'Trying again in 3 s', await s.text('.countdown'));
    await until('the countdown to tick', async () => (await s.text('.countdown')) === 'Trying again in 2 s', 1600);
    await until('the retry on its own', () => s.posts().length === 2, 5000);
    const after = s.posts()[1].at - first;
    check('CAP3 after Retry-After', after > 2500 && after < 4500, `${after} ms`);
    await until('the countdown to start over', async () => (await s.text('.countdown')) === 'Trying again in 3 s', 2000);
    await until('the third request', () => s.posts().length === 3, 5000);
    await s.playingAt('the video to play once there is room', 30, 40);
    check('CAP3 no click needed', s.log.every((e) => e.method !== 'DELETE'));
  });

  // CAP4: the limit of conversions says so, and Try again asks at once.
  // Mutant: limitMessage ignoring `limit` (the streams sentence for a transcode limit).
  await scenario('CAP4 the limit of conversions', {
    refuse: (n) => (n === 1 ? capBody({ error: '1 of 1 transcodes in use', limit: 'transcodes', max: 1, inUse: 1, playing: [PLAYING[0]] }) : null),
  }, async (s) => {
    await s.go('#/play/1');
    await s.panel().waitFor({ timeout: 8000 });
    const text = await s.text('.overlay.limit');
    check('CAP4 says ply is converting', text.includes('ply is already converting 1 video, the most it allows.'), text);
    check('CAP4 lists the conversion', (await s.page.locator('.overlay.limit li').count()) === 1);
    if (shots) await s.page.screenshot({ path: join(shots, 'cap-panel-transcodes.png') });
    await s.page.getByRole('button', { name: 'Try again' }).click();
    await until('the retry', () => s.posts().length === 2, 1500);
    await s.playingAt('the video to play', 0, 10);
  });

  // CAP5: a direct play whose file is refused (GET /file 503): the element only says it failed; the
  // player asks the file itself and shows the panel.
  // Mutant: onVideoError without fileLimit (the generic "could not play" error).
  await scenario('CAP5 a refused direct file', { file503: true }, async (s) => {
    await s.go('#/play/1?t=5');
    await s.panel().waitFor({ timeout: 8000 });
    const text = await s.text('.overlay.limit');
    check('CAP5 the panel, not an error', text.includes('2 videos are already playing') && !(await s.text('[role=alert]')).includes('Cannot play'), text);
  });

  // ---- stopped on purpose elsewhere (410): no restart ----

  const stoppedShown = (s) => s.text('.overlay.stopped');

  // STOP1: the heartbeat answers 410: the stopped state, no new session (not now, and not when the
  // server has forgotten the stop and would answer 404), and Play again starts at the position.
  // Mutant: Heartbeat treating a 410 like a 5xx (onStopped never called).
  await scenario('STOP1 a beat answered 410', { hls: true }, async (s) => {
    await s.go('#/play/1?t=20');
    await s.playingAt('hls.js to play from 20 s', 21, 30);
    s.sessions.get('s1').beat410 = true;
    await s.pause(); // a beat at once
    const at = (await s.video()).t;
    await until('the stopped state', async () => (await stoppedShown(s)).includes('Stopped from another device'), 3000);
    if (shots) await s.page.screenshot({ path: join(shots, 'stopped-elsewhere.png') });
    s.sessions.get('s1').beat410 = false;
    s.sessions.get('s1').beat404 = true; // what the server says once it forgets the stop
    await sleep(1500);
    check('STOP1 nothing started on its own', s.posts().length === 1, JSON.stringify(s.posts().map((p) => p.body.startSec)));
    check('STOP1 no error shown', !(await s.text('[role=alert]')).includes('Cannot play'));
    await s.page.getByRole('button', { name: 'Play again' }).click();
    await until('the new playback', () => s.posts().length === 2, 2000);
    check('STOP1 Play again starts where it was', near(s.posts()[1].body.startSec, at, 0.5), `${s.posts()[1].body.startSec} vs ${at}`);
    await s.playingAt('the new session to play', at, at + 8);
  });

  // STOP2: a segment answered 410 (hls.js), before any beat (the heartbeat is the real 10 s here).
  // Mutant: the hls error handler without sessionStopped (the 410 ends in "Stream error", or a restart).
  await scenario('STOP2 a segment answered 410', { hls: true }, async (s) => {
    await s.go('#/play/1?t=5');
    await s.playingAt('hls.js to play from 5 s', 6, 15);
    s.sessions.get('s1').stopped = true;
    const beats = s.beats.length;
    await s.seek(56); // beyond what is buffered: a segment request
    await until('the stopped state', async () => (await stoppedShown(s)).includes('Stopped from another device'), 4000);
    check('STOP2 it was the segment, not a beat', s.beats.length === beats, `${s.beats.length - beats} beats`);
    await sleep(1000);
    check('STOP2 nothing started on its own', s.posts().length === 1);
  });

  // STOP3: a direct play whose file answers 410 (dropped on purpose): the element only says it
  // failed; the probe of the file finds out.
  // Mutant: probeMedia ignoring 410 ("a network error" instead).
  await scenario('STOP3 a direct file answered 410', { fileStopped: true }, async (s) => {
    await s.go('#/play/1?t=5');
    await until('the stopped state', async () => (await stoppedShown(s)).includes('Stopped from another device'), 8000);
    check('STOP3 not a network error', !(await s.text('[role=alert]')).includes('Cannot play'));
    s.st.fileStopped = false; // announcing a play again lifts it, on the server
    await s.page.getByRole('button', { name: 'Play again' }).click();
    await s.playingAt('the direct play', 5, 15);
  });

  // STOP4: the POST itself answered 410: another window of this browser asked since, and has the
  // play. The stopped state, nothing started on its own, and Play again asks again.
  // Mutant: start() without the 410 branch ("Cannot play").
  await scenario('STOP4 a POST answered 410', { post410: (n) => n === 1 }, async (s) => {
    await s.go('#/play/1?t=5');
    await until('the stopped state', async () => (await stoppedShown(s)).includes('Stopped from another device'), 4000);
    check('STOP4 not an error', !(await s.text('[role=alert]')).includes('Cannot play'));
    await sleep(1000);
    check('STOP4 nothing started on its own', s.posts().length === 1);
    await s.page.getByRole('button', { name: 'Play again' }).click();
    await s.playingAt('the direct play', 5, 15);
  });

  // ---- races and the paused state ----

  // RACE: the countdown holds while a Stop's retry is under way, however long that takes: one retry.
  // Mutant: the countdown ticking while busy (it fires a second start).
  await scenario('RACE Stop with a slow retry', { refuse: full, retryAfter: 2, slowPost: (n) => (n === 2 ? 3500 : 0) }, async (s) => {
    await s.go('#/play/1?t=30');
    await s.panel().waitFor({ timeout: 8000 });
    await s.page.getByRole('button', { name: 'Stop Arrival' }).click();
    await s.playingAt('the video to play', 30, 40, 9000);
    await sleep(500);
    check('RACE one retry after Stop, not two', s.posts().length === 2, `${s.posts().length} POSTs`);
  });

  // PAUSE1: a session lost while paused (a seek's segment answered 404) comes back paused.
  // Mutant: the hls.js 404 path asking sessionLost to play (true instead of !v.paused).
  await scenario('PAUSE1 a lost session comes back paused', { hls: true }, async (s) => {
    await s.go('#/play/1?t=5');
    await s.playingAt('hls.js to play from 5 s', 6, 15);
    await s.pause();
    s.sessions.get('s1').segments404 = true;
    await s.seek(56);
    await until('the new playback request', () => s.posts().length === 2, 5000);
    check('PAUSE1 at the position', near(s.posts()[1].body.startSec, 56, 1), JSON.stringify(s.posts()[1].body.startSec));
    await until('the new stream to load', async () => (await s.video())?.rs >= 2, 8000);
    await sleep(700);
    const v = await s.video();
    check('PAUSE1 still paused', v.paused === true, JSON.stringify(v));
  });

  // PAUSE3: switching the audio track while paused (a new session) stays paused.
  // Mutant: restart() always playing (start(pos) with play=true).
  await scenario('PAUSE3 an audio switch while paused', { hls: true }, async (s) => {
    await s.go('#/play/1?t=10');
    await s.playingAt('hls.js to play from 10 s', 11, 20);
    await s.pause();
    await s.page.evaluate(() => document.querySelector('button[aria-label="Audio track"]').click());
    await s.page.evaluate(() => document.querySelectorAll('[role=menu][aria-label="Audio track"] [role=menuitemradio]')[1].click());
    await until('the new playback request', () => s.posts().length === 2, 3000);
    check('PAUSE3 the new track is asked for', s.posts()[1].body.audioIndex === 2, JSON.stringify(s.posts()[1].body.audioIndex));
    await until('the new stream to load', async () => (await s.video())?.rs >= 2, 8000);
    await sleep(700);
    const v = await s.video();
    check('PAUSE3 still paused', v.paused === true, JSON.stringify(v));
  });

  // PAUSE2: a paused player whose session is lost meets the limit; the retry that gets in stays paused.
  // Mutant: retryLimit always playing (start(pos) instead of start(pos, wantPlay)).
  await scenario('PAUSE2 paused through a refusal', { hls: true, retryAfter: 1, refuse: (n) => (n === 2 ? capBody() : null) }, async (s) => {
    await s.go('#/play/1?t=20');
    await s.playingAt('hls.js to play from 20 s', 21, 30);
    s.sessions.get('s1').beat404 = true;
    await s.pause(); // the beat at once is answered 404: a new session, refused, then let in
    await until('the retry that gets in', () => s.posts().length === 3, 6000);
    await until('the new stream to load', async () => (await s.video())?.rs >= 2, 8000);
    await sleep(700);
    const v = await s.video();
    check('PAUSE2 still paused', v.paused === true, JSON.stringify(v));
    check('PAUSE2 the panel is gone', (await s.panel().count()) === 0);
  });

  // A11Y: the panel stays mounted across refusals (the alert is not announced again), takes the focus
  // when it first shows, keeps it on Try again when a refusal refreshes it, and is a labelled region.
  // Mutants: {#key limit} around the panel (remounted each time); the buttons `disabled` while busy
  // (the focused one drops the focus to the page); no focus on first show.
  await scenario('A11Y the panel across refusals', { refuse: (n) => (n <= 2 ? capBody() : null), slowPost: (n) => (n === 2 ? 600 : 0) }, async (s) => {
    await s.go('#/play/1?t=30');
    await s.panel().waitFor({ timeout: 8000 });
    const focus = () => s.page.evaluate(() => document.activeElement?.id || document.activeElement?.textContent?.trim() || document.activeElement?.tagName);
    await until('the focus on the heading', async () => (await focus()) === 'limit-h', 2000);
    check('A11Y a region named by its heading', (await s.page.getByRole('region', { name: "This video can't start yet" }).count()) === 1);
    await s.page.evaluate(() => {
      document.querySelector('.limit').__mark = 1;
      document.querySelector('.limit [role=alert]').__mark = 1;
    });
    await s.page.getByRole('button', { name: 'Try again' }).focus();
    await s.page.keyboard.press('Enter');
    await until('the second refusal', async () => s.posts().length === 2 && (await s.text('.countdown')).startsWith('Trying again in'), 4000);
    const kept = await s.page.evaluate(() => [document.querySelector('.limit')?.__mark, document.querySelector('.limit [role=alert]')?.__mark]);
    check('A11Y the panel and its alert are the same nodes (nothing announced again)', kept[0] === 1 && kept[1] === 1, JSON.stringify(kept));
    check('A11Y the focus stays on Try again', (await focus()) === 'Try again', await focus());
  });

  // ---- the guards of the panel's buttons and countdown ----

  // GUARD1: a slow Stop with a short Retry-After: the countdown holds while the Stop is under way.
  // Mutant: the interval checking only `busy` (not `stopping`): it retries during the Stop, then again.
  await scenario('GUARD1 a slow Stop holds the countdown', { refuse: full, retryAfter: 1, deleteDelay: 2500 }, async (s) => {
    await s.go('#/play/1?t=30');
    await s.panel().waitFor({ timeout: 8000 });
    await s.page.getByRole('button', { name: 'Stop Arrival' }).click();
    await sleep(3500);
    await s.playingAt('the video to play', 30, 40, 9000);
    await sleep(300);
    check('GUARD1 one retry after Stop, not more', s.posts().length === 2, `${s.posts().length} POSTs`);
  });

  // GUARD2: a double click on Try again asks once.
  // Mutant: tryAgain() without its `blocked` check.
  await scenario('GUARD2 a double click on Try again', { refuse: (n) => (n === 1 ? capBody() : null), slowPost: (n) => (n === 2 ? 800 : 0) }, async (s) => {
    await s.go('#/play/1?t=30');
    await s.panel().waitFor({ timeout: 8000 });
    await s.page.evaluate(() => {
      const b = [...document.querySelectorAll('.limit button')].find((x) => /Try again/.test(x.textContent));
      b.click();
      b.click();
    });
    await s.playingAt('the video to play', 30, 40, 9000);
    await sleep(300);
    check('GUARD2 one retry, not two', s.posts().length === 2, `${s.posts().length} POSTs`);
  });

  // GUARD3: a double click on Stop stops once and retries once.
  // Mutant: stop() without its `blocked` check.
  await scenario('GUARD3 a double click on Stop', { refuse: full, deleteDelay: 800 }, async (s) => {
    await s.go('#/play/1?t=30');
    await s.panel().waitFor({ timeout: 8000 });
    await s.page.evaluate(() => {
      const b = document.querySelector('.limit button[aria-label="Stop Arrival"]');
      b.click();
      b.click();
    });
    await s.playingAt('the video to play', 30, 40, 9000);
    await sleep(300);
    check('GUARD3 one DELETE and one retry', s.deletes().filter((x) => x === 'def').length === 1 && s.posts().length === 2, `${JSON.stringify(s.deletes())} ${s.posts().length} POSTs`);
  });

  // FOCUS: when a retry gets in, the focus the panel had goes to the player's play button, not the page.
  // Mutant: closeLimit() without moving the focus.
  await scenario('FOCUS after the panel goes', { refuse: (n) => (n === 1 ? capBody() : null) }, async (s) => {
    await s.go('#/play/1?t=30');
    await s.panel().waitFor({ timeout: 8000 });
    await s.page.getByRole('button', { name: 'Try again' }).focus();
    await s.page.keyboard.press('Enter');
    await s.playingAt('the video to play', 30, 40, 9000);
    const f = await s.page.evaluate(() => document.activeElement?.getAttribute('aria-label') ?? document.activeElement?.tagName);
    check('FOCUS on the play button', f === 'Pause' || f === 'Play', f);
  });

  // ---- what the viewer wants: playing, unless they paused ----

  const pickAudio = async (s) => {
    await s.page.evaluate(() => document.querySelector('button[aria-label="Audio track"]').click());
    await s.page.evaluate(() => document.querySelectorAll('[role=menu][aria-label="Audio track"] [role=menuitemradio]')[1].click());
  };

  // WANT1: the audio track is switched while the stream is still being prepared (the element is
  // paused by the teardown, not by the viewer): the new stream plays.
  // Mutant: restart() going by el.paused whatever the phase.
  await scenario('WANT1 an audio switch while preparing', { hls: true, slowPost: (n) => (n === 1 ? 2000 : 0) }, async (s) => {
    await s.go('#/play/1?t=10');
    await until('the spinner while preparing', async () => (await s.page.locator('.spinner[role=status]').count()) > 0, 3000);
    await sleep(300);
    await pickAudio(s);
    await until('the new playback request', () => s.posts().length === 2, 3000);
    await s.playingAt('the new stream to play (the viewer never paused)', 10, 20);
  });

  // WANT2: the same while the cap panel is up.
  // Mutant: the same.
  await scenario('WANT2 an audio switch at the limit', { hls: true, refuse: (n) => (n === 1 ? capBody() : null) }, async (s) => {
    await s.go('#/play/1?t=10');
    await s.panel().waitFor({ timeout: 8000 });
    await pickAudio(s);
    await until('the new playback request', () => s.posts().length === 2, 3000);
    await s.playingAt('the new stream to play (the viewer never paused)', 10, 20);
  });

  // WANT3: a paused player restored from the back/forward cache stays paused (one that was playing
  // plays: player-wiring.mjs W4).
  // Mutant: restore() always playing.
  await scenario('WANT3 a paused player restored from the bfcache', { hls: true }, async (s) => {
    await s.go('#/play/1?t=10');
    await s.playingAt('hls.js to play from 10 s', 11, 20);
    await s.pause();
    await s.page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true })));
    await until('the session to be deleted', () => s.deletes().includes('s1'));
    await s.page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })));
    await until('the restart', () => s.posts().length === 2, 3000);
    await until('the new stream to load', async () => (await s.video())?.rs >= 2, 8000);
    await sleep(700);
    const v = await s.video();
    check('WANT3 still paused', v.paused === true, JSON.stringify(v));
  });

  // DEC: a direct file that will not decode was served: no probe of the file (it would keep the play
  // counted on the server for nothing), just the error.
  // Mutant: onVideoError probing for MEDIA_ERR_DECODE too.
  await scenario('DEC no probe for a decode error', {}, async (s) => {
    await s.go('#/play/1?t=5');
    await s.playingAt('the direct play', 5, 15);
    await s.page.evaluate(() => {
      const v = document.querySelector('video');
      Object.defineProperty(v, 'error', { configurable: true, get: () => ({ code: 3 }) }); // MEDIA_ERR_DECODE
      v.dispatchEvent(new Event('error'));
    });
    await until('the error', async () => (await s.text('[role=alert]')).includes('could not be decoded'), 3000);
    await sleep(500);
    check('DEC the file was not asked again', s.st.probes === 0, `${s.st.probes} probes`);
  });
  // The positive control: any other error of a direct play does probe.
  await scenario('DEC a network error probes', {}, async (s) => {
    await s.go('#/play/1?t=5');
    await s.playingAt('the direct play', 5, 15);
    await s.page.evaluate(() => {
      const v = document.querySelector('video');
      Object.defineProperty(v, 'error', { configurable: true, get: () => ({ code: 2 }) }); // MEDIA_ERR_NETWORK
      v.dispatchEvent(new Event('error'));
    });
    await until('the error', async () => (await s.text('[role=alert]')).includes('network error'), 3000);
    check('DEC the file was probed', s.st.probes === 1, `${s.st.probes} probes`);
  });

  // ---- Settings ----

  // SET1: each limit saves on change, with its own field, and says so.
  // Mutant: the conversions row sending maxStreams (the "wrong field" mutant).
  await scenario('SET1 Settings saves each limit', {}, async (s) => {
    await s.go('#/settings?tab=playback');
    const streams = s.page.getByLabel('Videos playing at once');
    const transcodes = s.page.getByLabel('Conversions at once');
    await streams.waitFor();
    check('SET1 shows the limits', (await streams.inputValue()) === '4' && (await transcodes.inputValue()) === '1');
    await streams.fill('6');
    await streams.press('Tab');
    await until('the save', () => s.puts().length === 1);
    check('SET1 sends maxStreams only', JSON.stringify(s.puts()[0]) === '{"maxStreams":6}', JSON.stringify(s.puts()[0]));
    await until('the confirmation', async () => (await s.status('maxStreams')) === 'Saved: up to 6 videos at once.');
    await transcodes.fill('2');
    await transcodes.press('Tab');
    await until('the second save', () => s.puts().length === 2);
    check('SET1 sends maxTranscodes only', JSON.stringify(s.puts()[1]) === '{"maxTranscodes":2}', JSON.stringify(s.puts()[1]));
    await until('its confirmation', async () => (await s.status('maxTranscodes')) === 'Saved: up to 2 conversions at once.');
    if (shots) await s.page.locator('section[aria-labelledby="playback-h"]').screenshot({ path: join(shots, 'settings-playback.png') });
  });

  // SET2: a limit set on the command line is locked; a 409 (it became one since the page loaded) locks it too.
  // Mutants: isLocked ignoring the source; the 409 shown as a plain error, the field left open.
  await scenario('SET2 a limit set when ply was started', {
    settings: { ...SETTINGS, maxTranscodes: 3, maxTranscodesSource: 'flag' },
    put: (body) => (body.maxStreams !== undefined ? { status: 409, body: { error: 'maxStreams is set by -max-streams or PLY_MAX_STREAMS and cannot be changed here' } } : undefined),
  }, async (s) => {
    await s.go('#/settings?tab=playback');
    const streams = s.page.getByLabel('Videos playing at once');
    const transcodes = s.page.getByLabel('Conversions at once');
    await streams.waitFor();
    check('SET2 the flag-set limit is locked', (await transcodes.isDisabled()) && (await transcodes.inputValue()) === '3');
    check('SET2 and says why', (await s.text('[data-limit="maxTranscodes"]')).includes('Set when ply was started.'));
    check('SET2 the other is open', !(await streams.isDisabled()) && !(await s.text('[data-limit="maxStreams"]')).includes('Set when ply was started'));
    await streams.fill('8');
    await streams.press('Tab');
    await until('the refusal', async () => (await s.status('maxStreams')).includes("Set when ply was started, so it can't be changed here."));
    check('SET2 a 409 locks it', await streams.isDisabled());
    check('SET2 back at the value in force', (await streams.inputValue()) === '4', await streams.inputValue());
    check('SET2 said once, not as an error', !(await s.text('[data-limit="maxStreams"]')).includes('Set when ply was started') && (await streams.getAttribute('aria-invalid')) === null);
    if (shots) await s.page.locator('section[aria-labelledby="playback-h"]').screenshot({ path: join(shots, 'settings-playback-locked.png') });
  });

  // SET3: a number out of range is refused by the server (400): the error shows inline, at the field.
  // Mutant: the 400 not shown (the catch swallowing it).
  await scenario('SET3 an inline 400', {
    put: (body) => (body.maxStreams > 32 ? { status: 400, body: { error: 'maxStreams must be a whole number from 1 to 32' } } : undefined),
  }, async (s) => {
    await s.go('#/settings?tab=playback');
    const streams = s.page.getByLabel('Videos playing at once');
    await streams.waitFor();
    await streams.fill('40');
    await streams.press('Tab');
    await until('the save', () => s.puts().length === 1);
    check('SET3 sends what was typed', JSON.stringify(s.puts()[0]) === '{"maxStreams":40}', JSON.stringify(s.puts()[0]));
    await until('the inline error', async () => (await s.status('maxStreams')) === 'Choose a number from 1 to 32.');
    check('SET3 the field is marked', (await streams.getAttribute('aria-invalid')) === 'true');
    check('SET3 the error is styled as one', await s.page.locator('[data-limit="maxStreams"] + [role=status].error').count() === 1);
    await streams.fill('5');
    await streams.press('Tab');
    await until('the good save', async () => (await s.status('maxStreams')) === 'Saved: up to 5 videos at once.');
    check('SET3 a good number clears it', (await streams.getAttribute('aria-invalid')) === null);
  });

  // SET4: quick changes are saved one at a time, the last one last, though the first answer is slow
  // (and is applied when it arrives, as if the requests crossed).
  // Mutant: saves sent as they come (concurrent): the server ends up with an older number.
  await scenario('SET4 quick changes in order', { putDelay: (b) => (b.maxStreams === 6 ? 1200 : 50) }, async (s) => {
    await s.go('#/settings?tab=playback');
    const streams = s.page.getByLabel('Videos playing at once');
    await streams.waitFor();
    for (const n of ['6', '7', '8']) {
      await streams.fill(n);
      await streams.press('Tab');
      await streams.focus();
    }
    await until('the last confirmation', async () => (await s.status('maxStreams')) === 'Saved: up to 8 videos at once.', 5000);
    await sleep(300);
    check('SET4 one save at a time', s.st.maxPutsInFlight === 1, `${s.st.maxPutsInFlight} at once`);
    check('SET4 the server has the last number', s.st.settings.maxStreams === 8, String(s.st.settings.maxStreams));
    check('SET4 the last one sent last', s.puts().at(-1)?.maxStreams === 8, JSON.stringify(s.puts()));
    check('SET4 the field shows it', (await streams.inputValue()) === '8');
  });

  // SET5: the answer to a save of one limit does not overwrite the other while it has a change on its way.
  // Mutant: `latest` set from both fields of every answer.
  await scenario('SET5 no clobbering of the other field', {
    putDelay: (b) => (b.maxTranscodes !== undefined ? 1500 : 30),
    // The server's answer to the streams save carries a transcodes value from before the pending change.
    put: (b, st) => (b.maxStreams !== undefined ? { status: 200, body: (st.settings = { ...st.settings, ...b, maxTranscodes: 5 }) } : undefined),
  }, async (s) => {
    await s.go('#/settings?tab=playback');
    const streams = s.page.getByLabel('Videos playing at once');
    const transcodes = s.page.getByLabel('Conversions at once');
    await streams.waitFor();
    await transcodes.fill('2');
    await transcodes.press('Tab');
    await streams.fill('6');
    await streams.press('Tab');
    await until('the streams confirmation', async () => (await s.status('maxStreams')) === 'Saved: up to 6 videos at once.', 3000);
    check('SET5 the pending change is still shown', (await transcodes.inputValue()) === '2', await transcodes.inputValue());
    await until('the transcodes confirmation', async () => (await s.status('maxTranscodes')) === 'Saved: up to 2 conversions at once.', 4000);
    check('SET5 and kept', (await transcodes.inputValue()) === '2', await transcodes.inputValue());
  });

  // SET7: an entry that is not a number, made while a save is on its way, keeps its error when that
  // save is answered.
  // Mutant: the answer's "Saved" written over the newer error.
  await scenario('SET7 an invalid entry during a save', { putDelay: () => 1200 }, async (s) => {
    await s.go('#/settings?tab=playback');
    const streams = s.page.getByLabel('Videos playing at once');
    await streams.waitFor();
    await streams.fill('6');
    await streams.press('Tab');
    await until('the save to leave', () => s.puts().length === 1);
    await streams.fill('2.5');
    await streams.press('Tab');
    await until('the error', async () => (await s.status('maxStreams')) === 'Enter a whole number from 1 to 32.', 1000);
    await sleep(1600); // the save of 6 is answered
    check('SET7 the error stands', (await s.status('maxStreams')) === 'Enter a whole number from 1 to 32.', await s.status('maxStreams'));
    check('SET7 and the entry', (await streams.inputValue()) === '2.5', await streams.inputValue());
    check('SET7 nothing else sent', s.puts().length === 1, JSON.stringify(s.puts()));
  });

  // SET6: no empty Playback heading when the server has no limits (older than M9), or the settings
  // did not load. The positive control is SET1, where it shows.
  // Mutant: the section shown whatever the settings say.
  const { maxStreams: _a, maxTranscodes: _b, maxStreamsSource: _c, maxTranscodesSource: _d, ...OLD } = SETTINGS;
  await scenario('SET6 no Playback section without limits', { settings: OLD }, async (s) => {
    await s.go('#/settings?tab=playback');
    await s.page.locator('#settings-panel-playback').waitFor();
    await sleep(500);
    check('SET6 older server: no Playback heading', (await s.page.locator('#playback-h').count()) === 0);
  });
  await scenario('SET6 no Playback section when the settings fail', { settings: { error: 'boom' }, settingsStatus: 500 }, async (s) => {
    await s.go('#/settings?tab=playback');
    await s.page.locator('#settings-panel-playback').waitFor();
    await until('the error', async () => (await s.text('.settings .error')).includes('boom'), 3000);
    check('SET6 failed settings: no Playback heading', (await s.page.locator('#playback-h').count()) === 0);
  });
} finally {
  await browser.close();
  server.close();
  rmSync(work, { recursive: true, force: true });
}
console.log(results.join('\n'));
process.exit(results.some((r) => r.startsWith('FAIL')) ? 1 : 0);
