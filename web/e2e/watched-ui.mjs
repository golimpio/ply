// Season posters on a show's page (components/SeasonRow.svelte) and watched / unwatched (M11, docs/
// DESIGN.md "Watch state"), in a real browser against a mocked server: the row of season cards
// (posters, the show's poster standing in for a missing one, the tabs pattern by mouse and keys,
// the selected one, no wrapping on a phone, the cards in view loading first); the toggles of a film,
// an episode, a season (specials too) and a show, each with its route and body; every card, rail
// and count following the answer without a reload; the change undone on a 500; the badges; the
// Unwatched filter reading item.watched; and PUT /progress answering watched.
//
// The mocked server keeps the watch state, so a page that fetched again would see a mark anyway:
// the checks move between pages in the app (a hash change, no reload), and /api/continue keeps
// answering the list it began with, so Continue watching can only lose a film through the client.
//
// Self-contained: serves web/dist (build it first: `npm run build`; PLY_E2E_DIST picks another
// directory) and the mocked API from one node server. Needs Google Chrome and ffmpeg on PATH.
//
//   npm run build && node e2e/watched-ui.mjs
//
// PLY_E2E_ONLY=<text> runs the scenarios whose name contains it. PLY_E2E_SERVE=1 only serves (it
// prints "READY <url>" and runs until it is stopped), for a script that takes screenshots. Each
// scenario names the mutants it was confirmed to catch.
import { execFileSync } from 'node:child_process';
import { createServer } from 'node:http';
import { existsSync, mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, extname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright-core';
import { serveItems } from './mock-items.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const dist = process.env.PLY_E2E_DIST ?? join(here, '..', 'dist');
if (!existsSync(join(dist, 'index.html'))) {
  console.error(`No build in ${dist}: run \`npm run build\` first.`);
  process.exit(2);
}

const results = [];
const check = (name, ok, detail = '') => results.push(`${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? `  (${detail})` : ''}`);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// ---- media: a 6 s clip, played to its end for PUT /progress ----------------------------------------

const work = mkdtempSync(join(tmpdir(), 'ply-watched-'));
const clipFile = join(work, 'clip.mp4');
execFileSync('ffmpeg', ['-v', 'error', '-y', '-f', 'lavfi', '-i', 'testsrc2=s=160x90:r=10:d=6', '-f', 'lavfi', '-i', 'sine=f=440:d=6',
  '-c:v', 'libx264', '-preset', 'ultrafast', '-pix_fmt', 'yuv420p', '-c:a', 'aac', '-b:a', '32k', '-movflags', '+faststart', clipFile]);
const clip = readFileSync(clipFile);
const CLIP_SEC = 6;

// ---- the library -----------------------------------------------------------------------------------

const SHOW = 'The Handmaid’s Tale';
const SOLO = 'Solo Show';
const poster = (name) => `/api/images/tmdb/w342/${name}.svg`;
const base = { container: 'mkv', videoCodec: 'h264', audioCodec: 'aac', width: 1920, height: 1080, art: null, matchState: 'matched', needsReview: false };
const film = (id, title, o = {}) => ({ ...base, id, kind: 'movie', title, sortTitle: title, year: 2016, durationSec: 7000, addedAt: id, poster: poster(`film${id}`), rating: null, ...o });
const FILMS = [
  film(1, 'Dune', { titleId: 101 }),
  film(2, 'Dune', { titleId: 101, width: 3840, height: 2160 }), // the 4K version of the same film
  film(3, 'Arrival', { titleId: 103, durationSec: CLIP_SEC }), // in Continue watching
  film(4, 'Home video', { matchState: 'unmatched', poster: undefined }), // no title: marked alone
  film(5, 'Heat', { titleId: 105 }), // watched from the start
  film(7, 'Alien', { titleId: 107 }), // seen in this version...
  film(8, 'Alien', { titleId: 107, width: 3840, height: 2160 }), // ...and being watched again in this one: in Continue
];
let nextEp = 100;
const episode = (show, season, n) => ({
  ...base, id: nextEp++, kind: 'episode', title: `Episode ${n}`, episodeTitle: `Episode ${n}`, show, season, episode: n, durationSec: 3000, addedAt: 50,
});
// Six seasons of three, and two specials; the third season has no poster of its own.
const EPISODES = [
  ...[1, 2, 3, 4, 5, 6].flatMap((s) => [1, 2, 3].map((n) => episode(SHOW, s, n))),
  episode(SHOW, 0, 1),
  episode(SHOW, 0, 2),
  episode(SOLO, 1, 1),
  episode(SOLO, 1, 2),
];
const NO_POSTER = 3;
const ITEMS = [...FILMS, ...EPISODES];
const seasonsOf = (show) => [...new Set(EPISODES.filter((e) => e.show === show).map((e) => e.season))].sort((a, b) => (a || 1000) - (b || 1000));

// ---- the server: web/dist, and the mocked API that keeps the watch state --------------------------

/**
 * The current scenario's state: rows (id -> {positionSec, watchedAt, playCount}, the server's
 * progress rows), what it was asked (puts), fail (answer PUT .../watched with 500), hold (a promise
 * PUT .../watched waits for), continueList (the ids /api/continue answers, fixed at the start).
 */
let mock;
function reset(opts = {}) {
  const rows = new Map([
    [3, { positionSec: 1200, watchedAt: 0, playCount: 0 }],
    [5, { positionSec: 0, watchedAt: 1_700_000_000, playCount: 1 }],
    [7, { positionSec: 0, watchedAt: 1_700_000_000, playCount: 1 }],
    [8, { positionSec: 1500, watchedAt: 0, playCount: 0 }],
  ]);
  for (const [id, r] of opts.rows ?? []) rows.set(id, r);
  mock = { rows, puts: [], progress: [], fail: false, hold: null, itemsHold: null, continueList: [3, 8], log: [] };
}
reset();

const fileWatched = (id) => (mock.rows.get(id)?.watchedAt ?? 0) > 0;
const versionsOf = (it) => (it.kind === 'movie' && it.titleId ? ITEMS.filter((i) => i.titleId === it.titleId) : [it]);
/** An item as /api/items answers it: a film with a title is watched when any version is. */
function summary(it) {
  const row = mock.rows.get(it.id);
  const vs = versionsOf(it).filter((v) => fileWatched(v.id));
  const watched = vs.length > 0;
  return {
    ...it,
    progress: row ? { positionSec: row.positionSec, watched: row.watchedAt > 0 } : null,
    watched,
    watchedAt: watched ? Math.max(...vs.map((v) => mock.rows.get(v.id).watchedAt)) : null,
    playCount: watched ? Math.max(...vs.map((v) => mock.rows.get(v.id).playCount)) : 0,
  };
}
function counts(eps) {
  const watchedEpisodes = eps.filter((e) => fileWatched(e.id)).length;
  const inProgress = eps.filter((e) => !fileWatched(e.id) && (mock.rows.get(e.id)?.positionSec ?? 0) > 0).length;
  return { episodeCount: eps.length, watchedEpisodes, inProgress };
}
const regular = (eps) => (eps.some((e) => e.season) ? eps.filter((e) => e.season) : eps);
const showRow = (show) => ({
  show, sortTitle: show, seasons: seasonsOf(show), ...counts(regular(EPISODES.filter((e) => e.show === show))), year: 2017,
  poster: poster(show === SHOW ? 'show' : 'solo'), matchState: 'matched', needsReview: false, rating: null,
});
const showDetail = (show) => ({
  ...showRow(show),
  overview: 'A dystopia.',
  seasons: seasonsOf(show).map((n) => ({
    season: n, name: n === 0 ? 'Specials' : `Season ${n}`, ...(n === NO_POSTER && show === SHOW ? {} : { poster: poster(`season${n}`) }),
    ...counts(EPISODES.filter((e) => e.show === show && e.season === n)),
  })),
});
/** PUT .../watched: what the store does (docs/DESIGN.md, "Marking"), and the WatchResult. */
function setWatched(files, watched) {
  for (const f of files) {
    const row = mock.rows.get(f.id);
    if (!watched) mock.rows.delete(f.id);
    else if (!row || !row.watchedAt) mock.rows.set(f.id, { positionSec: 0, watchedAt: 1_800_000_000, playCount: 1 });
  }
  return {
    watched,
    items: files.map((f) => {
      const row = mock.rows.get(f.id);
      return { id: f.id, watched: fileWatched(f.id), watchedAt: row?.watchedAt || null, playCount: row?.playCount ?? 0,
        progress: row ? { positionSec: row.positionSec, watched: row.watchedAt > 0 } : null };
    }),
  };
}

const types = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.json': 'application/json', '.png': 'image/png', '.svg': 'image/svg+xml' };
function serveFile(res, path) {
  const file = path === '/' ? join(dist, 'index.html') : join(dist, path);
  if (!file.startsWith(dist) || !existsSync(file) || file === dist) return res.writeHead(404).end();
  res.writeHead(200, { 'content-type': types[extname(file)] ?? 'application/octet-stream' }).end(readFileSync(file));
}
const json = (res, body, status = 200) => res.writeHead(status, { 'content-type': 'application/json', 'cache-control': 'no-store' }).end(JSON.stringify(body));
const readBody = (req) => new Promise((r) => {
  let s = '';
  req.on('data', (c) => (s += c));
  req.on('end', () => r(s));
});

const settings = {
  tmdbKeySet: true, tmdbKeySource: 'settings', metadataEnabled: true, metadataForcedOff: false, language: 'en-US',
  maxStreams: 4, maxTranscodes: 2, maxStreamsSource: 'default', maxTranscodesSource: 'default', ratingsPrimary: 'imdb', ratingsShown: ['imdb'], ratingsAvailable: ['imdb'],
};

async function api(req, res, u) {
  const p = decodeURIComponent(u.pathname);
  const m = req.method;
  if (p.startsWith('/api/images/')) {
    mock.log.push(p);
    const name = /([^/]+)\.svg$/.exec(p)?.[1] ?? '';
    const hue = [...name].reduce((h, c) => (h * 31 + c.charCodeAt(0)) % 360, 7);
    const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="342" height="513"><rect width="100%" height="100%" fill="hsl(${hue} 45% 32%)"/><text x="50%" y="50%" fill="#fff" font-size="40" text-anchor="middle" font-family="sans-serif">${name}</text></svg>`;
    return res.writeHead(200, { 'content-type': 'image/svg+xml', 'cache-control': 'max-age=3600' }).end(svg);
  }
  if (p === '/api/info') return json(res, { id: 'x', name: 'e2e', version: 'dev', authRequired: false });
  if (p === '/api/receivers') return json(res, []); // no AirPlay targets: the plain picker button
  if (p === '/api/scan') return json(res, { running: false, itemCount: ITEMS.length, watching: [] });
  if (p === '/api/items') {
    if (mock.itemsHold) await mock.itemsHold;
    return serveItems(req, res, ITEMS.map(summary));
  }
  if (p === '/api/shows') return json(res, [SHOW, SOLO].map(showRow));
  if (p === '/api/continue') return json(res, mock.continueList.map((id) => summary(ITEMS.find((i) => i.id === id))));
  if (['/api/upcoming', '/api/folders', '/api/metadata/review', '/api/sessions', '/api/genres'].includes(p)) return json(res, []);
  if (p === '/api/metadata/status') return json(res, { enabled: true, keySet: true, running: false, pending: 0, unmatched: 0, needsReview: 0, lastError: '' });
  if (p === '/api/jobs') return json(res, { paused: false, running: [], queued: [], history: [], tier: { playback: {}, browsing: false, hold: { held: false } } });
  if (p === '/api/settings') return json(res, settings);
  if (p === '/api/events') return res.writeHead(503).end();

  let r;
  if (m === 'PUT' && (r = /^\/api\/(items|titles)\/(\d+)\/watched$|^\/api\/shows\/(.+?)(?:\/seasons\/(\d+))?\/watched$/.exec(p))) {
    const body = JSON.parse((await readBody(req)) || '{}');
    mock.puts.push({ path: p, body });
    if (mock.hold) await mock.hold;
    if (mock.fail) return json(res, { error: 'database is locked' }, 500);
    if (typeof body.watched !== 'boolean') return json(res, { error: 'give "watched": true or false' }, 400);
    let files;
    if (r[1] === 'items') {
      const it = ITEMS.find((i) => i.id === Number(r[2]));
      files = it && versionsOf(it);
    } else if (r[1] === 'titles') {
      files = ITEMS.filter((i) => i.titleId === Number(r[2]));
    } else {
      files = EPISODES.filter((e) => e.show === r[3] && (r[4] === undefined || e.season === Number(r[4])));
    }
    if (!files?.length) return json(res, { error: 'not found' }, 404);
    return json(res, setWatched(files, body.watched));
  }
  // The player's reports: watched once at 90 % of the clip. The state is not kept: only the client's
  // copy can show it (no page here is reloaded).
  if (m === 'PUT' && (r = /^\/api\/items\/(\d+)\/progress$/.exec(p))) {
    const body = JSON.parse(await readBody(req));
    const answer = body.positionSec < 30 && body.positionSec < CLIP_SEC * 0.9 ? null : { positionSec: body.positionSec, watched: body.positionSec >= CLIP_SEC * 0.9 };
    mock.progress.push({ id: Number(r[1]), body, answer });
    return json(res, answer);
  }
  if (m === 'POST' && (r = /^\/api\/items\/(\d+)\/playback$/.exec(p))) {
    return json(res, { method: 'direct', videoAction: 'copy', audioAction: 'copy', audioIndex: 1, reasons: [], url: `/api/items/${r[1]}/file`, startSec: 0 });
  }
  if (m === 'GET' && /^\/api\/items\/\d+\/file$/.test(p)) {
    const range = /bytes=(\d*)-(\d*)/.exec(req.headers.range ?? '');
    const start = range?.[1] ? Number(range[1]) : 0;
    const end = range?.[2] ? Math.min(Number(range[2]), clip.length - 1) : clip.length - 1;
    return res.writeHead(range ? 206 : 200, { 'content-type': 'video/mp4', 'accept-ranges': 'bytes', 'content-range': `bytes ${start}-${end}/${clip.length}` }).end(clip.subarray(start, end + 1));
  }
  if ((r = /^\/api\/shows\/(.+)$/.exec(p)) && [SHOW, SOLO].includes(r[1])) return json(res, showDetail(r[1]));
  if ((r = /^\/api\/items\/(\d+)$/.exec(p))) {
    const it = ITEMS.find((i) => i.id === Number(r[1]));
    if (it) {
      return json(res, { ...summary(it), path: `/m/${it.id}.mp4`, size: 1, bitrate: 1, streams: [{ index: 0, type: 'video', codec: 'h264' }, { index: 1, type: 'audio', codec: 'aac', channels: 2 }],
        subtitles: [], playback: { method: 'direct', videoAction: 'copy', audioAction: 'copy', audioIndex: 1, reasons: [], url: '', startSec: 0 } });
    }
  }
  return json(res, { error: `unmocked ${m} ${p}` }, 404);
}

const server = createServer((req, res) => {
  const u = new URL(req.url, 'http://x');
  if (u.pathname.startsWith('/api/')) return void api(req, res, u);
  serveFile(res, u.pathname);
});
await new Promise((r) => server.listen(0, '127.0.0.1', r));
const origin = `http://127.0.0.1:${server.address().port}`;

if (process.env.PLY_E2E_SERVE === '1') {
  console.log(`READY ${origin}`);
  const stop = () => {
    server.close();
    rmSync(work, { recursive: true, force: true });
    process.exit(0);
  };
  process.on('SIGTERM', stop);
  process.on('SIGINT', stop);
  await new Promise(() => {});
}

// ---- helpers ----------------------------------------------------------------------------------------

async function open(browser, opts = {}) {
  reset(opts);
  if (opts.holdItems) mock.itemsHold = opts.holdItems;
  const context = await browser.newContext({ viewport: { width: opts.width ?? 1440, height: opts.height ?? 900 }, serviceWorkers: 'block' });
  const page = await context.newPage();
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  await page.goto(`${origin}/${opts.hash ?? '#/'}`);
  return { context, page, errors };
}
async function until(fn, ms = 4000) {
  const end = Date.now() + ms;
  while (Date.now() < end) {
    if (await fn()) return true;
    await sleep(40);
  }
  return false;
}
/** Another page of the app, as a link would open it: no reload, so the cached library stays. */
async function go(page, hash) {
  await page.evaluate((h) => (location.hash = h), hash);
  await sleep(60);
}
const lastPut = () => mock.puts.at(-1);
const putIs = (path, watched) => lastPut()?.path === path && lastPut()?.body?.watched === watched;
const showHash = (s) => `#/shows/${encodeURIComponent(SHOW)}${s === undefined ? '' : `?s=${s}`}`;
const tabs = (page) => page.locator('[role="tablist"][aria-label="Seasons"] [role="tab"]');
const tab = (page, name) => page.locator('[role="tab"]', { hasText: name }).first();
/** The watch badge of a card or a row (its kind and its spoken words), or null. */
const badgeOf = (loc) =>
  loc.evaluate((el) => {
    const b = el.querySelector('[data-watch]');
    return b ? { kind: b.dataset.watch, label: b.querySelector('.sr-only')?.textContent?.trim(), text: b.querySelector('[aria-hidden="true"]:not(.dot)')?.textContent?.trim() } : null;
  });
const card = (page, href) => page.locator(`main a.card[href="${href}"]`).first();
const selectedTab = (page) => page.locator('[role="tab"][aria-selected="true"]').textContent();
/** PUT .../watched waits until the returned function is called. */
function holdPuts() {
  let release;
  mock.hold = new Promise((r) => (release = r));
  return () => {
    mock.hold = null;
    release();
  };
}
const focusOn = (page, selector) => page.evaluate((s) => !!document.activeElement?.matches(s), selector);
/** What has the focus: its aria-label, else its text. */
const focused = (page) => page.evaluate(() => { const a = document.activeElement; return a?.getAttribute('aria-label') ?? a?.textContent?.trim() ?? null; });

const scenarios = {
  // ---- part 1: season posters --------------------------------------------------------------------

  // Mutants caught: the season's own poster not used (poster: showPoster for all); the fallback
  // dropped (poster: own, so season 3 gets the tile); the name over a stand-in poster not drawn.
  async 'the seasons are posters, with names and episode counts; a missing one has the show’s poster under its name'(browser) {
    const { context, page, errors } = await open(browser, { hash: showHash(1) });
    await tabs(page).first().waitFor();
    await page.locator('[role="tab"] img[src*="season1"]').waitFor();
    const cards = await tabs(page).evaluateAll((els) =>
      els.map((t) => ({ name: t.querySelector('.name')?.textContent, count: t.querySelector('.eps')?.textContent, src: t.querySelector('img')?.getAttribute('src') ?? '', over: t.querySelector('.over')?.textContent ?? null })),
    );
    check('a card per season, the specials last', JSON.stringify(cards.map((c) => c.name)) === JSON.stringify(['Season 1', 'Season 2', 'Season 3', 'Season 4', 'Season 5', 'Season 6', 'Specials']), cards.map((c) => c.name).join());
    check('each says its episodes', cards[0].count === '3 episodes' && cards[6].count === '2 episodes', `${cards[0].count} / ${cards[6].count}`);
    check('a season with a poster shows its own', cards[1].src.includes('season2') && cards[1].over === null, cards[1].src);
    check('a season with none has the show’s poster, its name over it', cards[2].src.includes('/show.svg') && cards[2].over === 'Season 3', `${cards[2].src} ${cards[2].over}`);
    const srcset = await tabs(page).nth(1).locator('img').getAttribute('srcset');
    const sizes = await tabs(page).nth(1).locator('img').getAttribute('sizes');
    check('a poster comes in sizes, for the width of a card', /w185\/season2\.svg 185w/.test(srcset ?? '') && /w342/.test(srcset ?? '') && sizes === '(max-width: 600px) 7rem, 8.5rem', `${srcset} | ${sizes}`);
    check('no page errors', errors.length === 0, errors.join('; '));
    await context.close();
  },

  // Mutants caught: aria-selected on every tab; tabindex 0 on every tab (no roving); ArrowRight not
  // handled; Home/End not handled; the season chosen with navigate (a new history entry); redirect
  // scrolling to the top on another season of the same show.
  async 'a season is chosen by a click or the arrow keys, one Tab stop, in the URL, the page left where it was'(browser) {
    const { context, page, errors } = await open(browser, { hash: showHash(1), height: 700 });
    await tabs(page).first().waitFor();
    check('the selected season is the one in the URL', (await selectedTab(page))?.includes('Season 1'));
    check('one tab is in the Tab order', (await page.locator('[role="tab"][tabindex="0"]').count()) === 1 && (await tabs(page).count()) === 7);
    await page.evaluate(() => window.scrollTo(0, 300));
    await sleep(100);
    const y = await page.evaluate(() => window.scrollY);
    const entries = await page.evaluate(() => history.length);
    await tab(page, 'Season 2').click();
    await until(async () => (await page.evaluate(() => location.hash)).endsWith('?s=2'));
    check('a click selects the season, in the URL', (await page.evaluate(() => location.hash)) === showHash(2) && (await selectedTab(page))?.includes('Season 2'));
    const titles = await page.locator('#season-panel li .num').allTextContents();
    check('its episodes are listed', titles.length === 3, String(titles.length));
    const panel = await page.locator('#season-panel').evaluate((el) => ({ role: el.getAttribute('role'), by: el.getAttribute('aria-labelledby') }));
    check('the episodes are the selected tab’s panel', panel.role === 'tabpanel' && panel.by === 'season-tab-2', JSON.stringify(panel));
    check('the page stays where it was scrolled to', Math.abs((await page.evaluate(() => window.scrollY)) - y) < 2, `${y} -> ${await page.evaluate(() => window.scrollY)}`);
    await tab(page, 'Season 2').focus();
    await page.keyboard.press('ArrowRight');
    await until(async () => (await page.evaluate(() => location.hash)).endsWith('?s=3'));
    const focused = await page.evaluate(() => document.activeElement?.id);
    check('ArrowRight selects the next season and takes the focus there', (await selectedTab(page))?.includes('Season 3') && focused === 'season-tab-3', focused);
    const yKeys = await page.evaluate(() => window.scrollY);
    await page.keyboard.press('End');
    await until(async () => (await page.evaluate(() => location.hash)).endsWith('?s=0'));
    check('End goes to the last (the specials)', (await selectedTab(page))?.includes('Specials'));
    await sleep(150);
    check('End scrolls the row, not the page', (await page.evaluate(() => window.scrollY)) === yKeys, `${yKeys} -> ${await page.evaluate(() => window.scrollY)}`);
    await page.keyboard.press('Home');
    await until(async () => (await page.evaluate(() => location.hash)).endsWith('?s=1'));
    check('Home goes to the first', (await selectedTab(page))?.includes('Season 1'));
    await page.keyboard.press('ArrowLeft');
    await until(async () => (await page.evaluate(() => location.hash)).endsWith('?s=0'));
    check('ArrowLeft from the first wraps to the last', (await selectedTab(page))?.includes('Specials'));
    check('choosing seasons adds no history entries', (await page.evaluate(() => history.length)) === entries);
    check('no page errors', errors.length === 0, errors.join('; '));
    await context.close();
  },

  // Mutants caught: the selected ring dropped (--ring-selected not applied).
  async 'the selected season is clearly marked'(browser) {
    const { context, page } = await open(browser, { hash: showHash(2) });
    await tabs(page).first().waitFor();
    const look = (n) => page.locator(`#season-tab-${n} .frame`).evaluate((el) => getComputedStyle(el).boxShadow);
    const on = await look(2);
    const off = await look(1);
    check('its poster has a ring the others have not', on !== off && /4px/.test(on), `${on} | ${off}`);
    const color = (n) => page.locator(`#season-tab-${n} .name`).evaluate((el) => getComputedStyle(el).color);
    check('its name is brighter', (await color(2)) !== (await color(1)));
    await context.close();
  },

  // Mutants caught: flex-wrap: wrap on the row; scroll-snap-type dropped.
  async 'at 375 px the row scrolls sideways, snapping, and never wraps'(browser) {
    const { context, page } = await open(browser, { hash: showHash(1), width: 375, height: 800 });
    await tabs(page).first().waitFor();
    const row = await page.locator('[role="tablist"][aria-label="Seasons"]').evaluate((el) => {
      const tops = [...el.querySelectorAll('[role="tab"]')].map((t) => Math.round(t.getBoundingClientRect().top));
      const s = getComputedStyle(el);
      return { tops: [...new Set(tops)], wider: el.scrollWidth > el.clientWidth, snap: s.scrollSnapType, docWide: document.documentElement.scrollWidth <= innerWidth };
    });
    check('every card on one line', row.tops.length === 1, JSON.stringify(row.tops));
    check('the row is wider than the screen, and scrolls', row.wider);
    check('it snaps to the cards', /x/.test(row.snap) && /mandatory/.test(row.snap), row.snap);
    check('the page itself does not scroll sideways', row.docWide);
    await context.close();
  },

  // Mutants caught: priority 'high' for every card; hold never released (no poster loads);
  // the cards in view measured before the selected one is scrolled to (a deep link to season 6).
  async 'the posters in view load first; the rest lazily'(browser) {
    for (const s of [1, 6]) {
      const { context, page } = await open(browser, { hash: showHash(s), width: 375, height: 800 });
      await page.locator('[role="tab"] img[src]').first().waitFor();
      await sleep(200);
      const imgs = await tabs(page).evaluateAll((els) => {
        const row = els[0].parentElement.getBoundingClientRect();
        return els.map((t) => {
          const r = t.getBoundingClientRect();
          const img = t.querySelector('img');
          return { id: t.id, on: r.right > row.left && r.left < row.right, priority: img?.getAttribute('fetchpriority'), loading: img?.getAttribute('loading') };
        });
      });
      const on = imgs.filter((i) => i.on);
      const off = imgs.filter((i) => !i.on);
      check(`season ${s}: the cards in view are high priority, eager`, on.length > 0 && on.every((i) => i.priority === 'high' && i.loading === 'eager'), JSON.stringify(on));
      check(`season ${s}: the rest are lazy`, off.length > 0 && off.every((i) => i.priority === 'low' && i.loading === 'lazy'), JSON.stringify(off));
      check(`season ${s}: the selected card is one of those in view`, on.some((i) => i.id === `season-tab-${s}`));
      await context.close();
    }
  },

  // Mutants caught: the solo layout dropped (a lone card in a scroller); a tablist of one.
  async 'a show of one season has its card beside its name, and no tabs'(browser) {
    const { context, page } = await open(browser, { hash: `#/shows/${encodeURIComponent(SOLO)}` });
    await page.locator('.season-top .season').first().waitFor();
    check('one card', (await page.locator('.season-top .season').count()) === 1);
    check('no tablist', (await page.locator('[role="tablist"][aria-label="Seasons"]').count()) === 0);
    const beside = await page.evaluate(() => {
      const c = document.querySelector('.season-top .season').getBoundingClientRect();
      const h = document.querySelector('.season-head h2').getBoundingClientRect();
      return h.left >= c.right;
    });
    check('its name and button sit beside the card', beside);
    await context.close();
  },

  // ---- part 2: watched ---------------------------------------------------------------------------

  // Mutants caught: badges on no card (itemBadge always null); film badge from progress.watched
  // instead of watched (the 4K version of a watched film has no row).
  async 'badges: a watched film has a check; a show has "N left"; with positive controls'(browser) {
    const rows = [[2, { positionSec: 0, watchedAt: 1_700_000_000, playCount: 1 }], [EPISODES[0].id, { positionSec: 0, watchedAt: 1_700_000_000, playCount: 1 }]];
    const { context, page } = await open(browser, { hash: '#/movies', rows });
    await card(page, '#/item/5').waitFor();
    check('a watched film has a check, spoken as Watched', JSON.stringify(await badgeOf(card(page, '#/item/5'))) === JSON.stringify({ kind: 'done', label: 'Watched', text: '✓' }));
    check('an unwatched one has none', (await badgeOf(card(page, '#/item/3'))) === null);
    // Dune: its 4K file was watched; the card opens the 1080p one, which has no row of its own.
    const duneHref = await page.locator('main a.card', { hasText: 'Dune' }).first().getAttribute('href');
    check('a film watched in another version is watched', (await badgeOf(page.locator(`main a.card[href="${duneHref}"]`)))?.kind === 'done', duneHref);
    const height = (h) => page.locator(`main a.card[href="${h}"]`).evaluate((el) => el.getBoundingClientRect().height);
    check('a card is as tall with a badge as without', Math.abs((await height('#/item/5')) - (await height('#/item/3'))) < 0.5);
    await go(page, '#/shows');
    await card(page, showHash()).waitFor();
    const left = await badgeOf(card(page, showHash()));
    check('a show with some watched says how many are left', left?.kind === 'left' && left.text === '17 left' && left.label === '17 episodes left', JSON.stringify(left));
    check('a show not started has none', (await badgeOf(card(page, `#/shows/${encodeURIComponent(SOLO)}`))) === null);
    await context.close();
  },

  // Mutants caught: the title route never used (setItemWatched for a film with a title); the
  // optimistic patch only on the shown version; the Unwatched filter reading progress.watched.
  async 'a film is marked from its page: PUT /api/titles/{id}/watched, then its card, the filter and its versions follow'(browser) {
    const { context, page, errors } = await open(browser, { hash: '#/item/1' });
    const button = page.locator('.hero-actions button', { hasText: /Mark as/ });
    await button.waitFor();
    check('the button says what it will do', (await button.textContent())?.trim() === 'Mark as watched');
    await button.focus();
    let release = holdPuts();
    await page.keyboard.press('Enter');
    await until(() => mock.puts.length > 0);
    await sleep(100);
    check('the focus stays on the button while the answer is awaited', await focusOn(page, '.hero-actions .pair > button'));
    release();
    check('it puts {watched: true} to the film’s title', putIs('/api/titles/101/watched', true), JSON.stringify(lastPut()));
    await until(async () => (await button.textContent())?.trim() === 'Mark as unwatched');
    check('the button turns to undo it', (await button.textContent())?.trim() === 'Mark as unwatched');
    check('the focus stays on it', (await focused(page)) === 'Mark as unwatched', await focused(page));
    check('the hero has the check', (await page.locator('.hero .badges [data-watch="done"]').count()) === 1);
    await go(page, '#/movies');
    await card(page, '#/item/3').waitFor();
    // One card for both versions: it opens the 4K one.
    check('its card in Movies has the check', (await badgeOf(page.locator('main a.card', { hasText: 'Dune' })))?.kind === 'done');
    await go(page, '#/item/2');
    await page.locator('.hero-actions button', { hasText: /Mark as/ }).waitFor();
    check('its other version is watched too', (await page.locator('.hero-actions button', { hasText: /Mark as/ }).textContent())?.trim() === 'Mark as unwatched');
    await go(page, '#/movies?f=unwatched');
    await card(page, '#/item/3').waitFor();
    const shown = await page.locator('main a.card').evaluateAll((els) => els.map((a) => a.getAttribute('href')));
    check('Unwatched leaves the watched films out (Dune, Heat) and keeps the rest', !shown.includes('#/item/1') && !shown.includes('#/item/2') && !shown.includes('#/item/5') && shown.includes('#/item/3') && shown.includes('#/item/4'), shown.join());
    await go(page, '#/item/1');
    await page.locator('.hero-actions button', { hasText: 'Mark as unwatched' }).click();
    await until(() => mock.puts.length > 1);
    check('unmarking puts {watched: false}', putIs('/api/titles/101/watched', false), JSON.stringify(lastPut()));
    await until(async () => (await page.locator('.hero-actions button', { hasText: /Mark as/ }).textContent())?.trim() === 'Mark as watched');
    await go(page, '#/movies?f=unwatched');
    await card(page, '#/item/3').waitFor();
    check('and the film is unwatched again', (await page.locator('main a.card[href="#/item/1"], main a.card[href="#/item/2"]').count()) === 1);
    check('no page errors', errors.length === 0, errors.join('; '));
    await context.close();
  },

  // Mutants caught: a film with no title sent to /api/titles/undefined.
  async 'a file no title is known for is marked by its item'(browser) {
    const { context, page } = await open(browser, { hash: '#/item/4' });
    await page.locator('.hero-actions button', { hasText: 'Mark as watched' }).click();
    await until(() => mock.puts.length > 0);
    check('it puts to /api/items/4/watched', putIs('/api/items/4/watched', true), JSON.stringify(lastPut()));
    await context.close();
  },

  // Mutants caught: Home keeps a film in Continue watching once it is watched (no filter by the cache).
  async 'Continue watching drops a film once it is marked watched'(browser) {
    const { context, page } = await open(browser, { hash: '#/' });
    const inRail = () => page.locator('section.rail', { hasText: 'Continue watching' }).locator('a.card[href^="#/play/3"]').count();
    await until(async () => (await inRail()) > 0);
    check('the film is in Continue watching (positive control)', (await inRail()) === 1);
    await go(page, '#/item/3');
    await page.locator('.hero-actions button', { hasText: 'Mark as watched' }).click();
    await until(() => mock.puts.length > 0);
    await page.locator('.hero-actions button', { hasText: 'Mark as unwatched' }).waitFor();
    await go(page, '#/');
    await page.locator('section.rail', { hasText: 'Recently added' }).waitFor();
    await sleep(300);
    check('it is gone from the rail, though /api/continue still lists it', (await inRail()) === 0 && mock.continueList.includes(3));
    const rewatch = await page.locator('section.rail', { hasText: 'Continue watching' }).locator('a.card[href^="#/play/8"]').count();
    check('a film seen in one version and being watched again in another stays', rewatch === 1);
    const shelf = page.locator('section.rail', { hasText: 'World & Auteur Cinema' }).locator('a.card[href="#/item/3"]');
    check('its card on the Auteur Cinema shelf has the check', (await badgeOf(shelf))?.kind === 'done');
    await context.close();
  },

  // Mutants caught: the episode toggle sent to the season; the episode's check from progress.watched;
  // the season card's and the hero's counts not following the cache.
  async 'an episode is marked from the show’s list and from its page; the counts follow'(browser) {
    const { context, page, errors } = await open(browser, { hash: showHash(1) });
    const e1 = EPISODES[0];
    const e2 = EPISODES[1];
    const toggle = page.locator('#season-panel button.mark').first();
    await toggle.waitFor();
    check('the toggle says what it will do', (await toggle.getAttribute('aria-label')) === 'Mark S01E01 as watched', await toggle.getAttribute('aria-label'));
    check('no check on the episode before (positive control below)', (await page.locator('#season-panel li').first().locator('[data-watch]').count()) === 0);
    let release = holdPuts();
    await toggle.click();
    await until(() => mock.puts.length > 0);
    await sleep(100);
    check('the focus stays on the toggle while the answer is awaited', await focusOn(page, '#season-panel button.mark'));
    release();
    check('it puts {watched: true} to the episode', putIs(`/api/items/${e1.id}/watched`, true), JSON.stringify(lastPut()));
    await until(async () => (await page.locator('#season-panel li').first().locator('[data-watch="done"]').count()) === 1);
    check('the episode has the check', (await page.locator('#season-panel li').first().locator('.sr-only', { hasText: 'Watched' }).count()) === 1);
    check('its label turns to undo it', (await toggle.getAttribute('aria-label')) === 'Mark S01E01 as unwatched');
    check('the focus stays on the toggle', (await focused(page)) === 'Mark S01E01 as unwatched', await focused(page));
    const name = await page.locator('#season-tab-1').evaluate((el) => el.getAttribute('aria-label') ?? null);
    check('a season tab is named "Season 1, 3 episodes, 2 episodes left", the badge last', name === null &&
      (await page.getByRole('tab', { name: /^Season 1\s*3 episodes\s*2 episodes left$/ }).count()) === 1);
    check('the season card says 2 left', (await badgeOf(page.locator('#season-tab-1')))?.label === '2 episodes left', JSON.stringify(await badgeOf(page.locator('#season-tab-1'))));
    check('the other seasons say nothing', (await badgeOf(page.locator('#season-tab-2'))) === null);
    check('the hero says 17 left', (await badgeOf(page.locator('.hero .meta')))?.text === '17 left', JSON.stringify(await badgeOf(page.locator('.hero .meta'))));
    await go(page, '#/shows');
    await card(page, showHash()).waitFor();
    check('the show’s card in Shows says 17 left', (await badgeOf(card(page, showHash())))?.text === '17 left');
    check('and counts the same episodes as the badge (18, the specials apart)', (await card(page, showHash()).locator('.sub').textContent())?.includes('18 episodes'), await card(page, showHash()).locator('.sub').textContent());
    await go(page, `#/item/${e2.id}`);
    await page.locator('.hero-actions button', { hasText: 'Mark as watched' }).click();
    await until(() => mock.puts.length > 1);
    check('the episode’s own page puts to its item', putIs(`/api/items/${e2.id}/watched`, true), JSON.stringify(lastPut()));
    await go(page, showHash(1));
    await page.locator('#season-tab-1').waitFor();
    check('back on the show, the season says 1 left', (await badgeOf(page.locator('#season-tab-1')))?.text === '1 left');
    check('no page errors', errors.length === 0, errors.join('; '));
    await context.close();
  },

  // Mutants caught: the season route given the season shown rather than 0 for specials (season || 1);
  // the specials counted in the show's counts; the season button not following its season.
  async 'a season and the specials (n=0) are marked by their season; a season card gets its check'(browser) {
    const { context, page } = await open(browser, { hash: showHash(0) });
    const seasonButton = page.locator('.season-head button');
    await seasonButton.waitFor();
    check('the specials’ button names them', (await seasonButton.textContent())?.trim() === 'Mark specials as watched', await seasonButton.textContent());
    await seasonButton.focus();
    const release = holdPuts();
    await page.keyboard.press('Enter');
    await until(() => mock.puts.length > 0);
    await sleep(100);
    check('the focus stays on the season button while the answer is awaited', await focusOn(page, '.season-head button'));
    release();
    check('it puts to season 0', putIs(`/api/shows/${SHOW}/seasons/0/watched`, true), JSON.stringify(lastPut()));
    await until(async () => (await seasonButton.textContent())?.includes('unwatched'));
    check('the focus stays on the season button', (await focused(page)) === 'Mark specials as unwatched', await focused(page));
    await until(async () => (await badgeOf(page.locator('#season-tab-0')))?.kind === 'done');
    check('the specials card has the check', (await badgeOf(page.locator('#season-tab-0')))?.label === 'Watched');
    check('the show is not watched by its specials', (await badgeOf(page.locator('.hero .meta'))) === null);
    await tab(page, 'Season 2').click();
    await until(async () => (await seasonButton.textContent())?.includes('season'));
    check('the button follows the season', (await seasonButton.textContent())?.trim() === 'Mark season as watched');
    await seasonButton.click();
    await until(() => mock.puts.length > 1);
    check('it puts to season 2', putIs(`/api/shows/${SHOW}/seasons/2/watched`, true), JSON.stringify(lastPut()));
    await until(async () => (await badgeOf(page.locator('#season-tab-2')))?.kind === 'done');
    check('season 2 has the check, season 1 none', (await badgeOf(page.locator('#season-tab-2')))?.kind === 'done' && (await badgeOf(page.locator('#season-tab-1'))) === null);
    check('the hero says 15 left', (await badgeOf(page.locator('.hero .meta')))?.text === '15 left');
    check('the season button turns to undo it', (await seasonButton.textContent())?.trim() === 'Mark season as unwatched');
    await context.close();
  },

  // Mutants caught: the show route never used; showDone ignoring the specials rule.
  async 'a whole show is marked from its page, then unmarked'(browser) {
    const { context, page } = await open(browser, { hash: showHash(1) });
    const showButton = page.locator('.hero-actions button', { hasText: /Mark show/ });
    await showButton.waitFor();
    check('the button says what it will do', (await showButton.textContent())?.trim() === 'Mark show as watched');
    await showButton.click();
    await until(() => mock.puts.length > 0);
    check('it puts to the show', putIs(`/api/shows/${SHOW}/watched`, true), JSON.stringify(lastPut()));
    await until(async () => (await showButton.textContent())?.trim() === 'Mark show as unwatched');
    const kinds = await Promise.all([0, 1, 2, 3, 4, 5, 6].map(async (n) => (await badgeOf(page.locator(`#season-tab-${n}`)))?.kind));
    check('every season card has the check', kinds.every((k) => k === 'done'), kinds.join());
    check('the hero has the check', (await badgeOf(page.locator('.hero .meta')))?.label === 'Watched');
    await go(page, '#/shows');
    await card(page, showHash()).waitFor();
    check('the show’s card in Shows has the check', (await badgeOf(card(page, showHash())))?.kind === 'done');
    await go(page, showHash(1));
    await page.locator('.hero-actions button', { hasText: 'Mark show as unwatched' }).click();
    await until(() => mock.puts.length > 1);
    check('unmarking puts {watched: false}', putIs(`/api/shows/${SHOW}/watched`, false));
    await until(async () => (await badgeOf(page.locator('#season-tab-1'))) === null);
    check('the checks are gone', (await badgeOf(page.locator('#season-tab-1'))) === null && (await badgeOf(page.locator('.hero .meta'))) === null);
    await context.close();
  },

  // Mutants caught: no rollback (the catch keeps the optimistic state); no error shown.
  async 'a mark the server refuses (500) is undone, and says why'(browser) {
    const { context, page } = await open(browser, { hash: '#/item/3' });
    let release;
    mock.hold = new Promise((r) => (release = r));
    mock.fail = true;
    const button = page.locator('.hero-actions button', { hasText: /Mark as/ });
    await button.click();
    await until(() => mock.puts.length > 0);
    check('while it waits, the page shows it marked (positive control)', (await page.locator('.hero .badges [data-watch="done"]').count()) === 1);
    release();
    await page.locator('.body p.error[role="alert"]').waitFor();
    check('the error is said, in the page’s style', (await page.locator('.body p.error[role="alert"]').textContent())?.includes('database is locked'));
    check('the button is back', (await button.textContent())?.trim() === 'Mark as watched');
    check('the check is gone', (await page.locator('.hero .badges [data-watch]').count()) === 0);
    await go(page, '#/movies');
    await card(page, '#/item/3').waitFor();
    check('its card has no check', (await badgeOf(card(page, '#/item/3'))) === null);
    // A season's mark refused: its card goes back, the error is said by the season.
    mock.hold = new Promise((r) => (release = r));
    await go(page, showHash(2));
    await page.locator('.season-head button').click();
    await until(() => mock.puts.length > 1);
    check('the season card shows the check while it waits (positive control)', (await badgeOf(page.locator('#season-tab-2')))?.kind === 'done');
    release();
    await page.locator('.season p.error[role="alert"]').waitFor();
    check('then it goes back', (await badgeOf(page.locator('#season-tab-2'))) === null);
    await context.close();
  },

  // Mutants caught: episodes sorted with the specials first (the page opens on them); the season
  // pin in mark() removed (marking the season shown moves "next up", and the page with it).
  async 'a show opens on its first regular season, and stays on it when that season is marked'(browser) {
    const { context, page } = await open(browser, { hash: showHash() });
    const h2 = page.locator('.season-head h2');
    await h2.waitFor();
    check('it opens on Season 1, not the specials', (await h2.textContent()) === 'Season 1' && (await selectedTab(page))?.includes('Season 1'), await h2.textContent());
    await page.locator('.season-head button').click();
    await until(() => mock.puts.length > 0);
    await until(async () => (await badgeOf(page.locator('#season-tab-1')))?.kind === 'done');
    await sleep(200);
    check('marked, it still shows Season 1 (now in the URL)', (await h2.textContent()) === 'Season 1' && (await page.evaluate(() => location.hash)) === showHash(1),
      `${await h2.textContent()} ${await page.evaluate(() => location.hash)}`);
    await context.close();
  },

  // Mutants caught: the Item page's own state dropped (the toggle changes nothing until the list comes).
  async 'a film opened before the library has loaded can be marked'(browser) {
    let release;
    const held = new Promise((r) => (release = r));
    const { context, page } = await open(browser, { hash: '#/item/3', holdItems: held });
    const button = page.locator('.hero-actions button', { hasText: /Mark as/ });
    await button.waitFor();
    await button.click();
    await until(() => mock.puts.length > 0);
    const turned = await until(async () => (await button.textContent())?.trim() === 'Mark as unwatched');
    check('the button turns, the library still loading', turned && mock.itemsHold === held);
    check('the hero has the check', (await page.locator('.hero .badges [data-watch="done"]').count()) === 1);
    release();
    await context.close();
  },

  // Mutants caught: applyProgress ignoring progress.watched (only the position written).
  async 'a film played to its end (PUT /progress answering watched) is watched everywhere'(browser) {
    const { context, page } = await open(browser, { hash: '#/movies' });
    await card(page, '#/item/3').waitFor();
    check('the film has no check before (positive control)', (await badgeOf(card(page, '#/item/3'))) === null);
    await go(page, '#/play/3?t=4');
    await until(() => mock.progress.some((p) => p.answer?.watched), 15000);
    check('the player reported the end, answered watched', mock.progress.some((p) => p.id === 3 && p.answer?.watched), JSON.stringify(mock.progress));
    check('and nothing was marked through PUT .../watched', mock.puts.length === 0);
    await go(page, '#/movies');
    await card(page, '#/item/3').waitFor();
    check('its card has the check', (await badgeOf(card(page, '#/item/3')))?.kind === 'done');
    await go(page, '#/');
    await page.locator('section.rail', { hasText: 'Recently added' }).waitFor();
    await sleep(300);
    check('it is gone from Continue watching', (await page.locator('section.rail', { hasText: 'Continue watching' }).locator('a.card[href^="#/play/3"]').count()) === 0);
    await context.close();
  },
};

const browser = await chromium.launch({ channel: 'chrome', headless: true, args: ['--autoplay-policy=no-user-gesture-required', '--mute-audio'] });
const only = process.env.PLY_E2E_ONLY;
try {
  for (const [name, run] of Object.entries(scenarios)) {
    if (only && !name.includes(only)) continue;
    try {
      await run(browser);
    } catch (e) {
      check(name, false, e instanceof Error ? e.message.split('\n')[0] : String(e));
    }
  }
} finally {
  await browser.close();
  server.close();
  rmSync(work, { recursive: true, force: true });
}
console.log(results.join('\n'));
const failed = results.filter((r) => r.startsWith('FAIL')).length;
console.log(`\n${results.length - failed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);
