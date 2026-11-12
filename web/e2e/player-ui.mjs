// The full-window player and its overlay, in a real browser, against a mocked API: the player
// fills the window, the overlay hides when idle and comes back on activity, close (✕) and Escape
// go back and end the session, a click on the scrubber seeks to the time under the pointer, and the
// overlay costs nothing while it is hidden and next to nothing per mousemove.
//
// Self-contained, like player-wiring.mjs: serves web/dist (PLY_E2E_DIST picks another directory),
// answers every /api/ request with page.route, and builds its media with ffmpeg (a 2 h film of one
// frame a second, for the seek precision, and a 60 s clip cut into HLS, for the sessions). Needs
// Google Chrome and ffmpeg on PATH.
//
//   npm run build && node e2e/player-ui.mjs
//
// PLY_E2E_ONLY=<text> runs the scenarios whose name contains it. Each scenario lists the mutations
// it was confirmed to catch (build a mutant into another directory with `vite build --outDir ...
// --emptyOutDir` and point PLY_E2E_DIST at it).
import { createServer } from 'node:http';
import { execFileSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs';
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
const CLOSE = 'button[aria-label="Close player"]';
/** The overlay's idle time (PlayerControls' IDLE_MS) and its fade, with some room. */
const HIDES_AFTER = 3000 + 300 + 500;

async function until(what, fn, ms = 10000) {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > end) throw new Error(`timed out waiting for ${what}`);
    await sleep(40);
  }
}

// ---- media ------------------------------------------------------------------------------------------

const work = mkdtempSync(join(tmpdir(), 'ply-player-ui-'));
const LONG = 7200;
const longFile = join(work, 'long.mp4');
execFileSync('ffmpeg', ['-v', 'error', '-y', '-f', 'lavfi', '-i', `color=c=0x2b3a4a:s=64x36:r=1:d=${LONG}`,
  '-c:v', 'libx264', '-preset', 'ultrafast', '-g', '10', '-pix_fmt', 'yuv420p', '-movflags', '+faststart', longFile]);
const clipFile = join(work, 'clip.mp4');
const hlsDir = join(work, 'hls');
execFileSync('ffmpeg', ['-v', 'error', '-y', '-f', 'lavfi', '-i', 'testsrc2=s=320x180:r=10:d=60', '-f', 'lavfi', '-i', 'sine=f=440:d=60',
  '-c:v', 'libx264', '-preset', 'ultrafast', '-b:v', '150k', '-g', '20', '-keyint_min', '20', '-sc_threshold', '0', '-pix_fmt', 'yuv420p',
  '-c:a', 'aac', '-b:a', '32k', '-movflags', '+faststart', clipFile]);
mkdirSync(hlsDir);
execFileSync('ffmpeg', ['-v', 'error', '-y', '-i', clipFile, '-c', 'copy', '-f', 'hls', '-hls_time', '2', '-hls_playlist_type', 'vod',
  '-hls_segment_filename', join(hlsDir, 'seg%03d.ts'), join(hlsDir, 'index.m3u8')]);
const longBytes = readFileSync(longFile);

// ---- the app ----------------------------------------------------------------------------------------

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

// ---- the mocked server ------------------------------------------------------------------------------

const ITEM = 1;
const detail = (opts) => ({
  id: ITEM, kind: 'movie', title: 'Overlay Test', year: 2020, durationSec: opts.hls ? 60 : LONG, container: 'mp4', videoCodec: 'h264',
  audioCodec: 'aac', width: 64, height: 36, addedAt: 1, progress: null, art: null, path: '/m/overlay.mp4', size: 1, bitrate: 1,
  streams: [
    { index: 0, type: 'video', codec: 'h264' },
    { index: 1, type: 'audio', codec: 'aac', language: 'eng', channels: 2 },
    { index: 2, type: 'audio', codec: 'aac', language: 'fra', channels: 2 },
  ],
  subtitles: [],
  playback: { method: 'direct', videoAction: 'copy', audioAction: 'copy', audioIndex: 1, reasons: [], url: '', startSec: 0 },
  ...opts.item,
});

/**
 * One browser context against one mocked server.
 *   hls      every playback is a session served through hls.js (60 s clip); else the 2 h film, direct
 *   item     extra fields of the item
 *   width, height   the viewport
 *   touch    a phone: touch events, mobile viewport
 *   hangItem  GET of the item never answers; itemStatus: it answers this error
 */
async function open(browser, opts = {}) {
  const context = await browser.newContext({
    viewport: { width: opts.width ?? 1440, height: opts.height ?? 900 }, serviceWorkers: 'block',
    ...(opts.touch ? { hasTouch: true, isMobile: true } : {}),
  });
  const log = [];
  const sessions = new Map();
  const errors = [];
  const unmocked = [];
  let posts = 0;

  await context.exposeFunction('__rec', (e) => log.push(e));
  await context.addInitScript(({ hls }) => {
    const rec = (method, url, body, keepalive) => window.__rec({ method, url: new URL(url, location.href).pathname, body, keepalive: !!keepalive });
    const realFetch = window.fetch.bind(window);
    window.fetch = (input, init = {}) => {
      const method = init.method ?? 'GET';
      if (method !== 'GET') rec(method, typeof input === 'string' ? input : input.url, init.body ? JSON.parse(init.body) : null, init.keepalive);
      return realFetch(input, init);
    };
    if (hls) {
      const can = HTMLMediaElement.prototype.canPlayType;
      HTMLMediaElement.prototype.canPlayType = function (type) {
        return /mpegurl/i.test(type) ? '' : can.call(this, type);
      };
    }
    // Every seek asked of a media element, as asked (before the element clamps it).
    window.__seeks = [];
    const ct = Object.getOwnPropertyDescriptor(HTMLMediaElement.prototype, 'currentTime');
    Object.defineProperty(HTMLMediaElement.prototype, 'currentTime', {
      configurable: true,
      get: ct.get,
      set(v) {
        window.__seeks.push(v);
        ct.set.call(this, v);
      },
    });
    // The work the page does: layout reads, frames asked for, timers set and run.
    const n = (window.__n = { rects: 0, raf: 0, timersSet: 0, timersRun: 0 });
    const rect = Element.prototype.getBoundingClientRect;
    Element.prototype.getBoundingClientRect = function () {
      n.rects++;
      return rect.call(this);
    };
    const raf = window.requestAnimationFrame.bind(window);
    window.requestAnimationFrame = (fn) => {
      n.raf++;
      return raf(fn);
    };
    for (const name of ['setTimeout', 'setInterval']) {
      const real = window[name].bind(window);
      window[name] = (fn, ms, ...args) => {
        n.timersSet++;
        const wrapped = typeof fn === 'function' ? (...a) => { n.timersRun++; return fn(...a); } : fn;
        return real(wrapped, ms, ...args);
      };
    }
  }, { hls: !!opts.hls });

  const json = (route, body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body), headers: { 'cache-control': 'no-store' } });
  await context.route((u) => u.pathname.startsWith('/api/'), async (route) => {
    const req = route.request();
    const { pathname: p } = new URL(req.url());
    const m = req.method();
    if (m === 'PUT' && p.startsWith('/api/devices/')) return route.fulfill({ status: 204 }); // the app says who it is (M11)
    if (m === 'GET' && p === '/api/info') return json(route, { id: 'x', name: 'overlay', version: '0', authRequired: false });
    if (m === 'GET' && p === '/api/scan') return json(route, { running: false, itemCount: 1 });
    if (m === 'GET' && p === '/api/items') return json(route, []);
    if (m === 'GET' && p === '/api/shows') return json(route, []);
    const idleQueue = { paused: false, running: [], queued: [], history: [], tier: { playback: {}, browsing: false, hold: { held: false } } };
    if (m === 'GET' && p === '/api/jobs') return json(route, idleQueue);
    if (m === 'GET' && p === '/api/events')
      return route.fulfill({ contentType: 'text/event-stream', body: `retry: 5000\n\nevent: snapshot\ndata: ${JSON.stringify(idleQueue)}\n\n` });
    if (m === 'GET' && p === `/api/items/${ITEM}` && opts.hangItem) return new Promise(() => {}); // never answered
    if (m === 'GET' && p === `/api/items/${ITEM}` && opts.itemStatus) return json(route, { error: 'the server failed' }, opts.itemStatus);
    if (m === 'GET' && p === `/api/items/${ITEM}`) return json(route, detail(opts));
    if (m === 'PUT' && p === `/api/items/${ITEM}/progress`) return json(route, null);
    if (m === 'POST' && p === `/api/items/${ITEM}/playback`) {
      const body = JSON.parse(req.postData());
      const id = `s${++posts}`;
      const pb = opts.hls
        ? { method: 'remux', videoAction: 'copy', audioAction: 'transcode', audioIndex: body.audioIndex ?? 1, reasons: ['the browser cannot play this container'], url: `/api/sessions/${id}/index.m3u8`, sessionId: id, startSec: 0 }
        : { method: 'direct', videoAction: 'copy', audioAction: 'copy', audioIndex: body.audioIndex ?? 1, reasons: [], url: `/api/items/${ITEM}/file`, startSec: 0 };
      if (opts.hls) sessions.set(id, { deleted: false, segments: 0 });
      return json(route, pb);
    }
    const sid = /^\/api\/sessions\/([^/]+)(?:\/(.+))?$/.exec(p);
    if (sid && m === 'DELETE' && !sid[2]) {
      const s = sessions.get(sid[1]);
      if (s) s.deleted = true;
      return route.fulfill({ status: 204 });
    }
    if (sid && m === 'POST' && sid[2] === 'heartbeat') {
      const s = sessions.get(sid[1]);
      return s && !s.deleted ? route.fulfill({ status: 204 }) : json(route, { error: 'session not found' }, 404);
    }
    if (sid && m === 'GET' && sid[2]) {
      const s = sessions.get(sid[1]);
      if (!s || s.deleted) return json(route, { error: 'no such session' }, 404);
      const playlist = sid[2] === 'index.m3u8';
      if (!playlist) s.segments++;
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
      const end = range?.[2] ? Math.min(Number(range[2]), longBytes.length - 1) : longBytes.length - 1;
      return route.fulfill({
        status: range ? 206 : 200,
        headers: { 'content-type': 'video/mp4', 'accept-ranges': 'bytes', 'content-range': `bytes ${start}-${end}/${longBytes.length}` },
        body: longBytes.subarray(start, end + 1),
      });
    }
    unmocked.push(`${m} ${p}`);
    return json(route, { error: `unmocked ${m} ${p}` }, 404);
  });

  const page = await context.newPage();
  page.on('pageerror', (e) => errors.push(e.message));

  const s = {
    page, context, sessions, errors, unmocked,
    go: (hash) => page.goto(`${base}/${hash}`),
    hash: () => page.evaluate(() => location.hash),
    deletes: () => log.filter((e) => e.method === 'DELETE').map((e) => ({ id: e.url.split('/').pop(), keepalive: e.keepalive })),
    video: () => page.evaluate(() => {
      const v = document.querySelector('video');
      return v && { t: v.currentTime, paused: v.paused, rs: v.readyState, d: v.duration };
    }),
    /** The media is loaded (its duration known); `play` waits for it to be playing as well. */
    ready: (play = true) => until('the video', async () => {
      const v = await s.video();
      return v && v.d > 0 && v.rs >= (play ? 3 : 1) && (!play || !v.paused) && v;
    }, 20000),
    seeks: () => page.evaluate(() => window.__seeks.slice()),
    counts: () => page.evaluate(() => ({ ...window.__n })),
    resetCounts: () => page.evaluate(() => Object.assign(window.__n, { rects: 0, raf: 0, timersSet: 0, timersRun: 0 })),
    hidden: () => page.evaluate(() => document.querySelector('.controls')?.classList.contains('hidden') ?? null),
    opacity: () => page.evaluate(() => getComputedStyle(document.querySelector('.controls .bottom')).opacity),
    menuOpen: () => page.evaluate(() => !!document.querySelector('[role=menu]')),
    popoverOpen: () => page.evaluate(() => !!document.querySelector('.controls details[open]')),
    paused: () => page.evaluate(() => document.querySelector('video').paused),
    clock: () => page.evaluate(() => document.querySelector('.controls .time').firstChild.textContent.trim()),
    /** Media events stopped before the controls hear them (capture on window), or let through again. */
    block: (events) => page.evaluate((events) => {
      window.__block?.();
      const stop = (e) => e.stopImmediatePropagation();
      for (const ev of events) window.addEventListener(ev, stop, true);
      window.__block = () => events.forEach((ev) => window.removeEventListener(ev, stop, true));
    }, events),
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

try {
  // The player fills the window: nothing of the app around it, the picture letterboxed on black.
  // Mutant: Play.svelte's wrapper not fixed to the window.
  await scenario('window', {}, async (s) => {
    await s.go('#/play/1');
    await s.ready(false);
    const r = await s.page.evaluate(() => {
      const box = (sel) => {
        const b = document.querySelector(sel)?.getBoundingClientRect();
        return b && [b.left, b.top, b.width, b.height].map(Math.round).join(',');
      };
      const inside = [[2, 2], [innerWidth - 3, 2], [2, innerHeight - 3], [innerWidth - 3, innerHeight - 3], [innerWidth / 2, innerHeight / 2]]
        .every(([x, y]) => !!document.elementFromPoint(x, y)?.closest('.play'));
      const v = getComputedStyle(document.querySelector('video'));
      return {
        win: `0,0,${innerWidth},${innerHeight}`, play: box('.play'), frame: box('.frame'), video: box('video'),
        header: document.querySelectorAll('header').length, tabs: document.querySelectorAll('nav.tabs').length,
        inside, fit: v.objectFit, bg: getComputedStyle(document.querySelector('.frame')).backgroundColor,
        scroll: document.documentElement.scrollHeight - innerHeight,
      };
    });
    check('window: the player is the whole window', r.play === r.win && r.frame === r.win, JSON.stringify(r));
    check('window: so is the picture, letterboxed (object-fit: contain) on black', r.video === r.win && r.fit === 'contain' && r.bg === 'rgb(0, 0, 0)', JSON.stringify(r));
    check('window: no header and no tab bar', r.header === 0 && r.tabs === 0, JSON.stringify(r));
    check('window: every corner of the window is the player', r.inside);
    const fsLabel = await s.page.locator('.controls button[aria-label="Full screen"]').count();
    check('window: the full screen button is still there', fsLabel === 1);
  });

  // The title in the overlay, the page's heading: a film's with its year, or for an episode its show
  // and number above its own title.
  const titles = (s) => s.page.evaluate(() => ({
    sub: document.querySelector('.top .subtitle')?.textContent ?? null,
    title: document.querySelector('.top .title')?.textContent,
    h1: document.querySelector('.top h1')?.textContent.replace(/\s+/g, ' ').trim(),
  }));
  await scenario('title: episode', { item: { kind: 'episode', title: 'Lost', show: 'Lost', season: 1, episode: 4, episodeTitle: 'Walkabout' } }, async (s) => {
    await s.go('#/play/1');
    await s.ready(false);
    const t = await titles(s);
    check('title: an episode shows its show and number, then its title', t.sub === 'Lost · S01E04' && t.title === 'Walkabout', JSON.stringify(t));
    check('title: it is the heading', t.h1 === 'Lost · S01E04 Walkabout', JSON.stringify(t));
  });
  await scenario('title: film', {}, async (s) => {
    await s.go('#/play/1');
    await s.ready(false);
    const t = await titles(s);
    check('title: a film shows its title and year', t.title === 'Overlay Test (2020)' && t.sub === null, JSON.stringify(t));
    check('title: it is the heading', t.h1 === 'Overlay Test (2020)', JSON.stringify(t));
  });

  // Before the player is there: the item loading for ever, or failing. ✕ and Escape still leave.
  // Mutants: no ✕ in Play's loading state; no Escape handler in Play.
  await scenario('loading: a fetch that never answers', { hangItem: true }, async (s) => {
    await s.go('#/play/1');
    await s.page.locator('.play [role=status]').waitFor();
    check('loading: it says so (control)', (await s.page.locator('.play [role=status]').textContent()).includes('Loading'));
    await s.page.keyboard.press('Escape');
    await until('Escape to leave', async () => (await s.hash()) === '#/item/1', 3000).catch(() => {});
    check('loading: Escape leaves (to the film, a deep link)', (await s.hash()) === '#/item/1', await s.hash());
    await s.page.evaluate(() => { location.hash = '#/play/1'; });
    await s.page.locator('.play [role=status]').waitFor();
    await s.page.click(CLOSE);
    await until('✕ to leave', async () => (await s.hash()) === '#/item/1', 3000).catch(() => {});
    check('loading: ✕ leaves, back where it was opened from', (await s.hash()) === '#/item/1', await s.hash());
  });
  await scenario('loading: the item fails', { itemStatus: 500 }, async (s) => {
    await s.go('#/play/1');
    await s.page.locator('.play [role=alert]').waitFor();
    await s.page.click(CLOSE);
    await until('✕ to leave', async () => (await s.hash()) === '#/item/1', 3000).catch(() => {});
    check('error: ✕ leaves', (await s.hash()) === '#/item/1', await s.hash());
  });

  // A click on the scrubber seeks to the time under the pointer: fraction of the bar times the
  // length, within 1 s on a 2 h film (a native range's thumb inset put 5% some 35 s early here).
  // A drag previews and seeks once, on release; the keyboard seeks from the focused slider.
  // Mutants: the old range-based seek (the input taking the pointer, step 1, no pointerdown handler);
  // the keys not preventDefault()ed (the slider steps as well); seekTo not setting the fill.
  await scenario('seek', {}, async (s) => {
    await s.go('#/play/1');
    await s.ready();
    await s.page.evaluate(() => document.querySelector('video').pause());
    const box = await s.page.locator('.scrubber').boundingBox();
    const y = box.y + box.height / 2;
    for (const f of [0.05, 0.25, 0.5, 0.75, 0.95]) {
      const x = Math.round(box.x + f * box.width);
      const want = ((x - box.x) / box.width) * LONG;
      const n = (await s.seeks()).length;
      await s.page.mouse.click(x, y);
      const seeks = await until(`the seek at ${f * 100}%`, async () => {
        const all = await s.seeks();
        return all.length > n && all;
      });
      const got = seeks.at(-1);
      check(`seek: a click at ${f * 100}% seeks to ${want.toFixed(1)} s, within 1 s`, Math.abs(got - want) <= 1, `got ${got.toFixed(1)}`);
      check(`seek: one seek per click at ${f * 100}%`, seeks.length === n + 1, `${seeks.length - n} seeks`);
    }

    // The fill goes where the click was at once, not back to where it was until the element has
    // sought (its seeked and timeupdate held back here). Mutant: seekTo without setting `current`.
    await s.block(['seeked', 'timeupdate']);
    const x30 = Math.round(box.x + 0.3 * box.width);
    await s.page.mouse.click(x30, y);
    await sleep(300);
    const fill = await s.page.evaluate(() => parseFloat(/scaleX\(([^)]*)\)/.exec(document.querySelector('.played').style.transform)?.[1]));
    check('seek: the fill stays where the click was while the element seeks', Math.abs(fill - (x30 - box.x) / box.width) < 0.005, `${fill}`);
    await s.page.evaluate(() => window.__block());

    // A drag: the time shown follows the pointer, nothing seeks until it is let go.
    const n = (await s.seeks()).length;
    const at = (f) => Math.round(box.x + f * box.width);
    await s.page.mouse.move(at(0.2), y);
    await s.page.mouse.down();
    await s.page.mouse.move(at(0.4), y, { steps: 5 });
    const shown = await s.page.evaluate(() => document.querySelector('.controls .time').firstChild.textContent.trim());
    const want40 = ((at(0.4) - box.x) / box.width) * LONG;
    const seconds = (clock) => clock.split(':').reduce((acc, part) => acc * 60 + Number(part), 0);
    check('seek: dragging shows the time under the pointer', Math.abs(seconds(shown) - want40) <= 1, `${shown} vs ${want40.toFixed(1)} s`);
    check('seek: and seeks nothing yet', (await s.seeks()).length === n);
    await s.page.mouse.move(at(0.6), y, { steps: 5 });
    await s.page.mouse.up();
    const after = await until('the seek of the drag', async () => {
      const all = await s.seeks();
      return all.length > n && all;
    });
    const want60 = ((at(0.6) - box.x) / box.width) * LONG;
    check('seek: letting go seeks once, where it was let go', after.length === n + 1 && Math.abs(after.at(-1) - want60) <= 1, `${after.slice(n)} vs ${want60.toFixed(1)}`);

    // The keyboard, on the focused slider: the arrows 5 s, Home and End the ends; once per key.
    await s.page.evaluate(() => { document.querySelector('video').currentTime = 1000; });
    await until('the seek to 1000 s', async () => (await s.video()).t === 1000);
    await s.page.focus('.scrubber input');
    const keys = [['ArrowRight', 1005], ['ArrowRight', 1010], ['ArrowLeft', 1005], ['ArrowUp', 1010], ['ArrowDown', 1005], ['End', LONG - 0.5], ['Home', 0]];
    for (const [key, want] of keys) {
      const k = (await s.seeks()).length;
      await s.page.keyboard.press(key);
      const all = await until(`the seek of ${key}`, async () => {
        const all = await s.seeks();
        return all.length > k && all;
      });
      await until(`the element at ${want}`, async () => Math.abs((await s.video()).t - want) < 0.6);
      check(`seek: ${key} on the slider seeks to ${want}`, all.length === k + 1 && Math.abs(all.at(-1) - want) < 0.01, JSON.stringify(all.slice(k)));
    }
    // Space and K on the focused slider play and pause; Space on a focused button presses it, only.
    check('seek: paused (control)', await s.paused());
    await s.page.keyboard.press(' ');
    await until('Space to play', async () => !(await s.paused()), 3000).catch(() => {});
    check('seek: Space on the slider plays', !(await s.paused()));
    await s.page.keyboard.press('k');
    await until('K to pause', () => s.paused(), 3000).catch(() => {});
    check('seek: K on the slider pauses', await s.paused());
    await s.page.evaluate(() => { document.querySelector('video').currentTime = 3000; });
    await until('the seek to 3000 s', async () => (await s.video()).t === 3000);
    await s.page.focus('button[aria-label="Back 10 seconds"]');
    const before = (await s.seeks()).length;
    await s.page.keyboard.press(' ');
    await sleep(500);
    const back = (await s.seeks()).slice(before);
    check('seek: Space on a button presses it (one seek back 10 s), and plays nothing', back.length === 1 && back[0] === 2990 && (await s.paused()), JSON.stringify(back));
    await s.page.focus('.scrubber input');
    await s.page.keyboard.press('Home');

    const label = () => s.page.evaluate(() => document.querySelector('.scrubber input').getAttribute('aria-valuetext'));
    await until('the slider at 0', async () => (await label()) === '0:00 of 2:00:00').catch(() => {});
    check('seek: the slider tells assistive technology where it is', (await label()) === '0:00 of 2:00:00', await label());
  });

  // The overlay hides after 3 s idle while playing (the cursor with it), comes back on a move, and
  // stays while paused, with a menu or the info popover open, with the mouse on a control, or keyboard
  // focus inside. Mutants: the idle timer never hiding it; the whole bottom bar keeping it up (a
  // parked mouse); the popover not counted; Escape not closing the popover.
  await scenario('overlay', {}, async (s) => {
    await s.go('#/play/1');
    await s.ready();
    await s.page.mouse.move(700, 400);
    check('overlay: shown at first', (await s.hidden()) === false);
    await sleep(HIDES_AFTER);
    const cursor = await s.page.evaluate(() => getComputedStyle(document.querySelector('.controls')).cursor);
    check('overlay: hidden after 3 s idle while playing', (await s.hidden()) === true && (await s.opacity()) === '0', `opacity ${await s.opacity()}`);
    check('overlay: the cursor hides with it', cursor === 'none', cursor);
    await s.page.mouse.move(720, 420);
    check('overlay: a mousemove brings it back', (await s.hidden()) === false);
    await sleep(500);
    check('overlay: fully', (await s.opacity()) === '1');

    // Paused: stays.
    await s.page.evaluate(() => document.querySelector('video').pause());
    await sleep(HIDES_AFTER);
    check('overlay: stays while paused', (await s.hidden()) === false);
    await s.page.evaluate(() => document.querySelector('video').play());
    await sleep(HIDES_AFTER);
    check('overlay: hides again once playing (control)', (await s.hidden()) === true);

    // A menu open: stays; Escape closes the menu only.
    await s.page.mouse.move(700, 400);
    await s.page.click('button[aria-label="Audio track"]');
    await s.page.mouse.move(700, 400);
    await sleep(HIDES_AFTER);
    check('overlay: stays while a menu is open', (await s.hidden()) === false && (await s.menuOpen()));
    await s.page.keyboard.press('Escape');
    check('overlay: Escape closes the menu', !(await s.menuOpen()));
    check('overlay: and only the menu', (await s.hash()) === '#/play/1' && s.deletes().length === 0, await s.hash());

    // The mouse on a control: stays. Parked at the window's edge, on the bar but on no control: hides.
    // Mutant: the whole bottom bar keeping it up.
    const btn = await s.page.locator('button[aria-label="Back 10 seconds"]').boundingBox();
    await s.page.mouse.move(btn.x + btn.width / 2, btn.y + btn.height / 2);
    await sleep(HIDES_AFTER);
    check('overlay: stays while the mouse is on a control', (await s.hidden()) === false);
    await s.page.mouse.move(700, 400);
    await sleep(HIDES_AFTER);
    check('overlay: hides once the mouse is off it (control)', (await s.hidden()) === true);
    await s.page.mouse.move(720, 897); // shows the overlay
    await sleep(100);
    await s.page.mouse.move(722, 897); // onto the bar, now that it takes the pointer
    const parked = await s.page.evaluate(() => document.elementFromPoint(722, 897)?.className ?? '');
    check('overlay: the parked mouse is on the bottom bar (control)', /\bbottom\b/.test(parked), parked);
    await sleep(HIDES_AFTER);
    check('overlay: hides with the mouse parked at the bottom edge, on no control', (await s.hidden()) === true, parked);

    // The info popover open: stays.
    await s.page.mouse.move(700, 400);
    await s.page.click('.controls details summary');
    await s.page.mouse.move(700, 400);
    await sleep(HIDES_AFTER);
    check('overlay: stays while the info popover is open', (await s.hidden()) === false && (await s.popoverOpen()));
    await s.page.keyboard.press('Escape');
    check('overlay: Escape closes the popover, only', !(await s.popoverOpen()) && (await s.hash()) === '#/play/1');

    // Keyboard focus inside: stays.
    await s.page.keyboard.press('Tab');
    await sleep(HIDES_AFTER);
    const focused = await s.page.evaluate(() => document.activeElement?.closest('.controls') !== null && document.activeElement.matches(':focus-visible'));
    check('overlay: stays while keyboard focus is inside', focused && (await s.hidden()) === false);
  });

  // The volume slider slides open on the mute button's hover or keyboard focus, the clock moving aside
  // with it, and slides shut: not a jump, and with no layout while it slides (only clip-path, opacity
  // and transform move; the bar's boxes stay put). The transitions are paused a quarter of the way
  // (Web Animations on the CSS transitions) and the slider measured as shown: the part of it the
  // pointer can hit. A mouse click on mute doesn't hold it open; reduced motion opens it at once.
  // Mutants: the instant slider of 3da7639 (width 0 to 5.5rem at once, only its opacity animated); an
  // in-flow width transition (the slide before 3da7639); :focus-within holding it open after a click.
  const volumeState = (s) => s.page.evaluate(() => {
    const input = document.querySelector('.volume input');
    const r = input.getBoundingClientRect();
    const y = r.top + r.height / 2;
    let shown = 0;
    for (let x = Math.ceil(r.left); x < r.right; x++) if (document.elementFromPoint(x, y) === input) shown = x + 1 - r.left;
    const clock = document.querySelector('.controls .time').getBoundingClientRect().left;
    // The layout around it: what a slide that moves boxes would change.
    const w = (sel) => document.querySelector(sel).getBoundingClientRect().width;
    const layout = [w('.controls .grow'), w('.volume'), document.querySelector('.controls .row').getBoundingClientRect().height].join(' ');
    return { left: r.left, width: r.width, shown, clock, gap: clock - (r.left + shown), layout };
  });
  await scenario('volume', {}, async (s) => {
    await s.go('#/play/1');
    await s.ready(false);
    await s.page.evaluate(() => document.querySelector('video').pause()); // the overlay stays up
    const state = () => volumeState(s);
    /** Pauses the row's running transitions at a quarter of their course; their properties. */
    const pauseAtQuarter = () => s.page.evaluate(() => document.getAnimations()
      .filter((a) => a instanceof CSSTransition && a.effect?.target?.closest?.('.controls .row'))
      .map((a) => {
        a.pause();
        a.currentTime = a.effect.getComputedTiming().duration / 4;
        return a.transitionProperty;
      }));
    const resume = () => s.page.evaluate(() => document.getAnimations().forEach((a) => a.playState === 'paused' && a.play()));
    const COMPOSITED = new Set(['clip-path', 'opacity', 'transform']);
    const noLayout = (props) => props.length > 0 && props.every((p) => COMPOSITED.has(p));
    const mute = await s.page.locator('.volume button').boundingBox();
    const onMute = () => s.page.mouse.move(mute.x + mute.width / 2, mute.y + mute.height / 2);
    const off = () => s.page.mouse.move(700, 400);

    await off();
    await sleep(500);
    const shut = await state();
    check('volume: shut, none of the slider shows (control)', shut.shown === 0, JSON.stringify(shut));

    await onMute();
    const opening = await pauseAtQuarter();
    const mid = await state();
    await resume();
    await sleep(500);
    const open = await state();
    check('volume: open, all of the slider shows (control)', open.shown > 0 && open.shown >= open.width - 2, JSON.stringify(open));
    check('volume: the clock moves aside by the slider\'s room', Math.abs(open.clock - shut.clock - open.width) <= 4, `${shut.clock} → ${open.clock}`);
    check('volume: opening, part way: some of the slider shows, not all', mid.shown > 2 && mid.shown < open.shown - 2,
      `${mid.shown} of ${open.shown}; transitions ${JSON.stringify(opening)}`);
    check('volume: opening, part way: so is the clock', mid.clock > shut.clock + 2 && mid.clock < open.clock - 2, `${shut.clock} < ${mid.clock} < ${open.clock}`);
    check('volume: opening, the clock stays clear of the slider', mid.gap >= 0 && mid.gap <= 16, JSON.stringify(mid));
    check('volume: opening animates only clip-path, opacity and transform', noLayout(opening), JSON.stringify(opening));

    await off();
    const closing = await pauseAtQuarter();
    const midShut = await state();
    await resume();
    await sleep(500);
    const shutAgain = await state();
    check('volume: shutting, part way', midShut.shown > 2 && midShut.shown < open.shown - 2 && midShut.clock > shut.clock + 2 && midShut.clock < open.clock - 2, JSON.stringify(midShut));
    check('volume: shutting animates only clip-path, opacity and transform', noLayout(closing), JSON.stringify(closing));
    check('volume: then shut, the clock back', shutAgain.shown === 0 && Math.abs(shutAgain.clock - shut.clock) <= 1, JSON.stringify(shutAgain));
    const layouts = [shut, mid, open, midShut, shutAgain].map((x) => x.layout);
    check('volume: the bar\'s layout is the same shut, sliding and open', layouts.every((l) => l === layouts[0]), JSON.stringify(layouts));

    // A mouse click on mute leaves the button focused, but doesn't hold the slider open.
    await onMute();
    await s.page.mouse.down();
    await s.page.mouse.up();
    const clicked = await s.page.evaluate(() => document.activeElement?.closest('.volume') !== null);
    await off();
    await sleep(500);
    check('volume: after a click on mute, the pointer gone, it shuts', (await state()).shown === 0, `mute focused: ${clicked}`);
    await onMute();
    await s.page.mouse.down();
    await s.page.mouse.up(); // unmuted again
    await off();

    // The keyboard: Tab onto mute opens it; so does focus on the slider, however it got there.
    await s.page.focus('button[aria-label="Forward 10 seconds"]');
    await s.page.keyboard.press('Tab');
    await sleep(500);
    const tabbed = await state();
    const onMuteKey = await s.page.evaluate(() => document.activeElement?.matches('.volume button:focus-visible'));
    check('volume: Tab onto mute opens it', onMuteKey && tabbed.shown >= open.width - 2 && Math.abs(tabbed.clock - open.clock) <= 1, JSON.stringify({ onMuteKey, ...tabbed }));
    await s.page.focus('.volume input');
    await sleep(500);
    const focused = await state();
    check('volume: focus on the slider opens it', focused.shown >= open.width - 2 && Math.abs(focused.clock - open.clock) <= 1, JSON.stringify(focused));
    await s.page.evaluate(() => document.activeElement.blur());
    await sleep(500);
    check('volume: shut once it loses focus (control)', (await state()).shown === 0);

    // Reduced motion: open at once (well inside the 200 ms of a slide).
    await s.page.emulateMedia({ reducedMotion: 'reduce' });
    await onMute();
    await sleep(50);
    const reduced = await state();
    check('volume: with reduced motion it opens at once', reduced.shown >= open.width - 2 && Math.abs(reduced.clock - open.clock) <= 1, JSON.stringify(reduced));
  });

  // Every control there (subtitles, two audio tracks, PiP) and an h:mm:ss clock: the room kept for the
  // slider never wraps the bar. Just over 700 px it fits with room to spare for Safari's AirPlay
  // button; at 700 px and below the slider goes (mute stays) and its room with it.
  // Mutant: the 700 px rule dropped (the bar wraps at 620 px).
  const everyControl = { item: { subtitles: [{ key: '3', language: 'eng', format: 'dvd_subtitle', image: true, default: false }] } };
  const atAnHour = (s) => s.page.evaluate(async () => {
    const v = document.querySelector('video');
    v.pause();
    v.currentTime = 3600;
    await new Promise((r) => v.addEventListener('seeked', r, { once: true }));
  });
  const bar = (s) => s.page.evaluate(() => {
    const row = document.querySelector('.controls .row');
    const ctl = document.querySelector('.controls .row .ctl').getBoundingClientRect().height;
    const shown = [...row.querySelectorAll('button')].filter((b) => b.offsetParent !== null).map((b) => b.getAttribute('aria-label'));
    return {
      oneLine: row.getBoundingClientRect().height <= ctl + 1, shown, clock: row.querySelector('.time').textContent.trim(),
      slider: getComputedStyle(row.querySelector('.volume input')).display, margin: getComputedStyle(row.querySelector('.time')).marginLeft,
      spare: row.querySelector('.grow').getBoundingClientRect().width,
      airplay: row.querySelector('.ctl').getBoundingClientRect().width + parseFloat(getComputedStyle(row).columnGap),
    };
  });
  for (const w of [701, 700, 620]) {
    await scenario(`volume: ${w} px wide`, { width: w, height: 800, ...everyControl }, async (s) => {
      await s.go('#/play/1');
      await s.ready(false);
      await atAnHour(s);
      await s.page.mouse.move(300, 300);
      await sleep(300);
      const b = await bar(s);
      const all = ['Subtitles', 'Audio track', 'Picture in picture', 'Full screen', 'Mute'].every((l) => b.shown.includes(l));
      check(`volume: ${w} px, every control and an h:mm:ss clock (control)`, all && /^1:00:00 \/ 2:00:00$/.test(b.clock), JSON.stringify(b));
      check(`volume: ${w} px, the bar is one line`, b.oneLine, JSON.stringify(b));
      if (w > 700) {
        check(`volume: ${w} px, the slider is there`, b.slider !== 'none', JSON.stringify(b));
        check(`volume: ${w} px, with room left for an AirPlay button`, b.spare >= b.airplay, JSON.stringify(b));
      } else {
        check(`volume: ${w} px, no slider and no room kept for it`, b.slider === 'none' && b.margin === '8px', JSON.stringify(b));
      }
    });
  }
  await scenario('volume: phone', { width: 500, height: 900 }, async (s) => {
    await s.go('#/play/1');
    await s.ready(false);
    await s.page.mouse.move(250, 400);
    const r = await s.page.evaluate(() => {
      const play = document.querySelector('.controls .row .ctl.big').getBoundingClientRect();
      const clock = document.querySelector('.controls .time').getBoundingClientRect();
      const time = getComputedStyle(document.querySelector('.controls .time'));
      return {
        volume: getComputedStyle(document.querySelector('.volume')).display, gap: clock.left - play.right, top: clock.top - play.top, clock: clock.width,
        margin: time.marginLeft, transform: time.transform,
      };
    });
    check('volume: on a phone there is no slider', r.volume === 'none', JSON.stringify(r));
    check('volume: and the clock sits just after the play button', r.clock > 0 && r.gap > 0 && r.gap <= 16 && Math.abs(r.top) < 40, JSON.stringify(r));
    check('volume: the room kept for the slider is given back (margin 0.5rem, no shift)', r.margin === '8px' && r.transform === 'none', JSON.stringify(r));
  });

  // The cost: hidden and idle, nothing runs (no frame loop, no DOM change, hardly a timer); a
  // mousemove reads no layout and sets no timer of its own.
  // Mutants: a timer per mousemove (clearTimeout + setTimeout in poke); the scrubber's rect read on
  // every pointermove; the clock updated while hidden; the pointermove let through to the app's
  // delegated listener (no stopPropagation: Svelte sets a timer per event).
  await scenario('cost', {}, async (s) => {
    await s.go('#/play/1');
    await s.ready();
    await s.page.mouse.move(700, 400);
    await sleep(HIDES_AFTER);
    check('cost: hidden (control)', (await s.hidden()) === true);
    await s.page.evaluate(() => {
      window.__mutations = 0;
      new MutationObserver((list) => (window.__mutations += list.length)).observe(document.body, { subtree: true, childList: true, attributes: true, characterData: true });
    });
    await s.resetCounts();
    const t0 = (await s.video()).t;
    await sleep(5000);
    const idle = await s.counts();
    const mutations = await s.page.evaluate(() => window.__mutations);
    const t1 = (await s.video()).t;
    check('cost: it was playing all along (control)', t1 - t0 > 4, `${t0} → ${t1}`);
    check('cost: hidden and idle, no frame asked for in 5 s', idle.raf === 0, JSON.stringify(idle));
    check('cost: no DOM change in 5 s', mutations === 0, `${mutations} mutations`);
    check('cost: at most a few timers a second', idle.timersRun <= 10, JSON.stringify(idle));

    // Over the picture: 40 moves.
    await s.resetCounts();
    for (let i = 0; i < 40; i++) await s.page.mouse.move(400 + i * 10, 300 + (i % 5));
    const moves = await s.counts();
    check('cost: 40 moves over the picture read no layout', moves.rects === 0, JSON.stringify(moves));
    check('cost: and set at most one timer (the idle one)', moves.timersSet <= 1, JSON.stringify(moves));
    check('cost: and ask for no frame', moves.raf === 0, JSON.stringify(moves));

    // Along the scrubber: 40 moves, the tooltip following.
    const box = await s.page.locator('.scrubber').boundingBox();
    const y = box.y + box.height / 2;
    await s.page.mouse.move(box.x + 50, y);
    await s.resetCounts();
    const tip = () => s.page.evaluate(() => document.querySelector('.scrubber .tip')?.style.transform ?? null);
    const tip0 = await tip();
    for (let i = 0; i < 40; i++) await s.page.mouse.move(box.x + 60 + i * 20, y);
    const along = await s.counts();
    const tip1 = await tip();
    check('cost: the tooltip follows the pointer along the scrubber (control)', !!tip0 && !!tip1 && tip0 !== tip1, `${tip0} → ${tip1}`);
    check('cost: 40 moves along the scrubber read no layout', along.rects === 0, JSON.stringify(along));
    check('cost: and set at most one timer', along.timersSet <= 1 && along.raf === 0, JSON.stringify(along));
  });

  // Shown again, the clock is right at once: it doesn't wait for the next timeupdate (held back here,
  // so only the catch-up can move it). Mutant: no sync when the overlay shows again (the reviewer's B).
  await scenario('catch-up', {}, async (s) => {
    await s.go('#/play/1');
    await s.ready();
    await s.page.mouse.move(700, 400);
    await sleep(HIDES_AFTER + 3000);
    const seconds = (clock) => clock.split(':').reduce((acc, part) => acc * 60 + Number(part), 0);
    const stale = seconds(await s.clock());
    const t0 = (await s.video()).t;
    check('catch-up: hidden, the clock stood still (control)', (await s.hidden()) === true && t0 - stale > 2, `${stale} vs ${t0}`);
    await s.block(['timeupdate', 'progress']);
    await s.page.mouse.move(720, 420);
    await sleep(300);
    const shown = seconds(await s.clock());
    const t1 = (await s.video()).t;
    await s.page.evaluate(() => window.__block());
    check('catch-up: shown again, the clock is where the film is', Math.abs(shown - t1) <= 1.5, `${shown} vs ${t1}`);
  });

  // A phone held sideways: a tap shows the overlay, a tap on it shown hides it, and neither plays nor
  // pauses; a double tap on a side skips 10 s (not full screen); a tap or a drag on the scrubber
  // seeks to the time under the finger. Mutants: a tap toggling playback, as a click does; the touch
  // double tap's dblclick going full screen.
  await scenario('touch', { touch: true, width: 844, height: 390 }, async (s) => {
    await s.go('#/play/1');
    await s.ready();
    await until('the overlay to hide', () => s.hidden(), 8000);
    await s.page.touchscreen.tap(422, 195);
    await sleep(100);
    check('touch: a tap shows the overlay', (await s.hidden()) === false);
    check('touch: and doesn\'t pause', !(await s.paused()));
    await sleep(500);
    await s.page.touchscreen.tap(422, 195);
    await sleep(100);
    check('touch: a tap on the shown overlay hides it', (await s.hidden()) === true);
    check('touch: and doesn\'t pause either', !(await s.paused()));

    await sleep(500);
    const t = (await s.video()).t;
    const n = (await s.seeks()).length;
    await s.page.touchscreen.tap(700, 195);
    await s.page.touchscreen.tap(700, 195);
    await sleep(300);
    const skips = (await s.seeks()).slice(n);
    check('touch: a double tap on the right skips 10 s', skips.length === 1 && Math.abs(skips[0] - (t + 10)) < 1.5, `${JSON.stringify(skips)} from ${t}`);
    check('touch: and is not full screen', await s.page.evaluate(() => !document.fullscreenElement));
    check('touch: nor a pause', !(await s.paused()));

    // The scrubber, with the overlay up (paused).
    await s.page.evaluate(() => document.querySelector('video').pause());
    const box = await s.page.locator('.scrubber').boundingBox();
    const y = Math.round(box.y + box.height / 2);
    const x = Math.round(box.x + 0.3 * box.width);
    const m = (await s.seeks()).length;
    await s.page.touchscreen.tap(x, y);
    const tapped = await until('the seek of the tap', async () => {
      const all = await s.seeks();
      return all.length > m && all;
    });
    const want = ((x - box.x) / box.width) * LONG;
    check('touch: a tap on the scrubber seeks to the time under it, within 1 s', Math.abs(tapped.at(-1) - want) <= 1, `${tapped.at(-1)} vs ${want.toFixed(1)}`);

    const cdp = await s.context.newCDPSession(s.page);
    const at = (f) => Math.round(box.x + f * box.width);
    const k = (await s.seeks()).length;
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: at(0.2), y }] });
    for (const f of [0.3, 0.4, 0.5, 0.6]) await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x: at(f), y }] });
    const during = (await s.seeks()).length;
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
    const dragged = await until('the seek of the drag', async () => {
      const all = await s.seeks();
      return all.length > k && all;
    });
    const want60 = ((at(0.6) - box.x) / box.width) * LONG;
    check('touch: a drag seeks once, where the finger left it, within 1 s', during === k && dragged.length === k + 1 && Math.abs(dragged.at(-1) - want60) <= 1,
      `${JSON.stringify(dragged.slice(k))} vs ${want60.toFixed(1)}`);
  });

  // Close (✕): back where the player was opened from, through the history (no new entry), and the
  // session ends. Mutant: the player's teardown without leave() (no DELETE).
  await scenario('close', { hls: true }, async (s) => {
    await s.go('#/item/1');
    await s.page.locator('a.btn.primary[href="#/play/1"]').click();
    await s.ready();
    check('close: the session is open (control)', s.sessions.get('s1')?.deleted === false && s.sessions.get('s1').segments > 0);
    const entries = await s.page.evaluate(() => history.length);
    await s.page.mouse.move(700, 400);
    await s.page.click(CLOSE);
    await until('the film page', async () => (await s.hash()) === '#/item/1');
    check('close: goes back to the film', (await s.hash()) === '#/item/1');
    check('close: through the history (no entry added)', (await s.page.evaluate(() => history.length)) === entries);
    await until('the session to end', () => s.sessions.get('s1').deleted);
    check('close: ends the session (DELETE, keepalive)', s.deletes().some((d) => d.id === 's1' && d.keepalive), JSON.stringify(s.deletes()));
    check('close: the video is gone', (await s.page.locator('video').count()) === 0);
  });

  // Escape: one layer at a time. A menu, then full screen, then the player (a deep link: to the
  // film's page), whose session ends. Mutant: Escape not closing the player.
  await scenario('escape', { hls: true }, async (s) => {
    await s.go('#/play/1');
    await s.ready();
    check('escape: the session is open (control)', s.sessions.get('s1')?.deleted === false);
    await s.page.mouse.move(700, 400);
    await s.page.click('button[aria-label="Audio track"]');
    check('escape: a menu is open (control)', await s.menuOpen());
    await s.page.keyboard.press('Escape');
    await sleep(600);
    check('escape: the first closes the menu, only', !(await s.menuOpen()) && (await s.hash()) === '#/play/1' && !s.sessions.get('s1').deleted);

    await s.page.mouse.move(700, 410);
    await s.page.click('button[aria-label="Full screen"]');
    const fs = await until('full screen', () => s.page.evaluate(() => document.fullscreenElement?.className ?? null), 3000).catch(() => null);
    if (fs) {
      check('escape: full screen is the frame, by the Fullscreen API (control)', fs.includes('frame'), fs);
      await s.page.keyboard.press('Escape');
      await until('full screen left', () => s.page.evaluate(() => !document.fullscreenElement), 3000);
      await sleep(600);
      check('escape: the next leaves full screen, only', (await s.hash()) === '#/play/1' && !s.sessions.get('s1').deleted);

      // A browser that also hands the page the Escape that left full screen, after the
      // fullscreenchange: that Escape must not close the player as well. Mutant: the 500 ms guard dropped.
      await s.page.mouse.move(700, 420);
      await s.page.click('button[aria-label="Full screen"]');
      await until('full screen again', () => s.page.evaluate(() => !!document.fullscreenElement), 3000);
      await s.page.evaluate(async () => {
        const changed = new Promise((r) => document.addEventListener('fullscreenchange', r, { once: true }));
        await document.exitFullscreen();
        await changed;
        window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
      });
      await sleep(600);
      check('escape: an Escape right after leaving full screen closes nothing', (await s.hash()) === '#/play/1' && !s.sessions.get('s1').deleted, await s.hash());
    } else {
      check('escape: full screen (not available in this browser; skipped)', true);
    }

    await s.page.keyboard.press('Escape');
    await until('the film page', async () => (await s.hash()) === '#/item/1');
    check('escape: then it closes the player, to the film (a deep link)', (await s.hash()) === '#/item/1');
    await until('the session to end', () => s.sessions.get('s1').deleted);
    check('escape: and ends the session', s.deletes().some((d) => d.id === 's1'), JSON.stringify(s.deletes()));
  });
} finally {
  await browser.close();
  server.close();
  rmSync(work, { recursive: true, force: true });
}
console.log(results.join('\n'));
process.exit(results.some((r) => r.startsWith('FAIL')) ? 1 : 0);
