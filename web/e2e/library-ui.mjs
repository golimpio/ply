// Movies and Shows at library scale, in a real browser against a mocked server: the virtual grid
// (components/VirtualGrid.svelte) keeping a screenful of cards in the page out of 5,000, the
// posters on screen loading first, the A to Z rail (components/JumpRail.svelte) by mouse, finger
// and keyboard, the arrow keys between cards, Back and a reload putting the grid back where it
// was (claimRestore), and a revisit of the list answered 304 from its ETag.
//
// Self-contained: serves web/dist (build it first: `npm run build`; PLY_E2E_DIST picks another
// directory) and the mocked API from the same node server. Not page.route: routing turns the
// browser's HTTP cache off, and the 304 needs it. Needs Google Chrome.
//
//   npm run build && node e2e/library-ui.mjs
//
// PLY_E2E_ONLY=<text> runs the scenarios whose name contains it. PLY_E2E_MEASURE=1 measures
// instead (DOM nodes, JS heap, long tasks on the first mount and on Back, scroll frame times) and
// prints them: run it on two builds to compare. Each scenario names the mutants it was confirmed
// to catch.
import { createHash } from 'node:crypto';
import { createServer } from 'node:http';
import { existsSync, readFileSync } from 'node:fs';
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

// ---- the library: 5,000 films and 300 shows, as the server sends them -----------------------------

// No word starts with Q, T or X: the rail must not have them, though a fiftieth of the films are
// "The ..." (listed under their name, as the server's sortTitle has it).
const WORDS = ['Alpha', 'Amber', 'Bravo', 'Blue', 'Cobalt', 'Crimson', 'Delta', 'Dune', 'Echo', 'Ember', 'Falcon', 'Frost', 'Gamma',
  'Garnet', 'Harbor', 'Helix', 'Indigo', 'Iris', 'Jade', 'Juno', 'Kilo', 'Krypton', 'Lima', 'Lunar', 'Mango', 'Matrix', 'Nova', 'Nectar',
  'Oscar', 'Onyx', 'Pearl', 'Prism', 'Ruby', 'Raven', 'Sierra', 'Solar', 'Umber', 'Union', 'Velvet', 'Vortex', 'Whiskey', 'Willow',
  'Yankee', 'Yonder', 'Zulu', 'Zephyr'];
const FILMS = 5000;

/** The server's metadata.SortTitle: a leading The, A or An moved to the end. */
const sortTitleOf = (t) => {
  const m = /^(The|A|An) (.+)$/i.exec(t);
  return m ? `${m[2]}, ${m[1]}` : t;
};
function filmTitle(i) {
  const word = WORDS[(i * 7919) % WORDS.length];
  switch (i % 50) {
    case 0: return `The ${word} ${i}`;
    case 1: return `A ${word} ${i}`;
    case 2: return `${1900 + (i % 100)} ${word}`; // under #
    case 3: return `Élan ${i}`; // under E; sorts after Z, as the server has it
    default: return `${word} ${i}`;
  }
}
const poster = (kind, id) => `/api/images/tmdb/w342/${kind}${id}.svg`;
/** A Rotten Tomatoes rating: its glyph is taller than the line of text it sits in. */
const tomato = { source: 'rt_critics', value: 91, scale: 100, score100: 91, state: 'fresh', url: '', fetchedAt: '' };
// A fifth of the films watched (a check over the poster), a seventh part way (a progress bar).
const watchOf = (i) => {
  const watched = i % 5 === 0;
  return {
    watched, watchedAt: watched ? 1_700_000_000 : null, playCount: watched ? 1 : 0,
    progress: watched ? { positionSec: 0, watched: true } : i % 7 === 0 ? { positionSec: 600, watched: false } : null,
  };
};
const film = (i, rated) => {
  const title = filmTitle(i);
  return {
    id: i, kind: 'movie', title, sortTitle: sortTitleOf(title), year: 1950 + ((i * 37) % 70), durationSec: 5400 + i, container: 'mkv',
    videoCodec: 'h264', audioCodec: 'aac', width: 1920, height: 1080, addedAt: i, art: null, titleId: 10_000 + i, poster: poster('p', i),
    matchState: 'matched', needsReview: false, rating: rated?.test(sortTitleOf(title)) ? tomato : null, ...watchOf(i),
  };
};
const showName = (i) => `${WORDS[(i * 31) % WORDS.length]} Show ${i}`;
const episode = (i) => ({
  id: 100_000 + i, kind: 'episode', title: showName(i), show: showName(i), season: 1, episode: 1, durationSec: 2800, container: 'mkv',
  videoCodec: 'h264', audioCodec: 'aac', width: 1920, height: 1080, addedAt: i, art: null, matchState: 'matched', needsReview: false, ...watchOf(i),
});
const showRow = (i) => ({
  show: showName(i), sortTitle: showName(i), seasons: [1], episodeCount: 1, year: 2000 + (i % 20), poster: poster('s', i),
  matchState: 'matched', needsReview: false, rating: null,
});

/** The server's order of names (store/meta_read.go): ASCII lower-case sort title, year, id. */
const lower = (s) => s.replace(/[A-Z]/g, (c) => c.toLowerCase());
const byName = (a, b) => {
  const ka = lower(a.sortTitle);
  const kb = lower(b.sortTitle);
  return ka < kb ? -1 : ka > kb ? 1 : a.year - b.year || a.id - b.id;
};
const letterOf = (n) => {
  const c = n.normalize('NFD').charAt(0).toUpperCase();
  return c >= 'A' && c <= 'Z' ? c : '#';
};

// ---- the server: web/dist, and the mocked API ------------------------------------------------------

/** The current scenario's library and what the server saw: set by open(). */
let mock = { films: [], shows: 0, log: [] };

const types = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.json': 'application/json', '.png': 'image/png', '.svg': 'image/svg+xml' };
function serveFile(res, path) {
  const file = path === '/' ? join(dist, 'index.html') : join(dist, path);
  if (!file.startsWith(dist) || !existsSync(file) || file === dist) return res.writeHead(404).end();
  res.writeHead(200, { 'content-type': types[extname(file)] ?? 'application/octet-stream' }).end(readFileSync(file));
}

/** A list as the server sends one: a strong ETag, revalidated on every use (304 when unchanged). */
function list(req, res, body) {
  const text = JSON.stringify(body);
  const etag = `"${createHash('sha1').update(text).digest('hex').slice(0, 20)}"`;
  const inm = req.headers['if-none-match'] ?? '';
  const status = inm === etag ? 304 : 200;
  mock.log.push({ path: req.url, inm, status });
  const headers = { etag, 'cache-control': 'private, no-cache' };
  if (status === 304) return res.writeHead(304, headers).end();
  res.writeHead(200, { ...headers, 'content-type': 'application/json' }).end(text);
}
const json = (res, body, status = 200) => res.writeHead(status, { 'content-type': 'application/json', 'cache-control': 'no-store' }).end(JSON.stringify(body));

const settings = {
  tmdbKeySet: true, tmdbKeySource: 'settings', metadataEnabled: true, metadataForcedOff: false, language: 'en-US',
  maxStreams: 4, maxTranscodes: 2, maxStreamsSource: 'default', maxTranscodesSource: 'default',
  tvdbKeySet: false, tvdbPinSet: false, tvdbKeySource: '', tvdbAttribution: 'x', tvdbAttributionUrl: 'https://thetvdb.com',
  mdblistKeySet: false, mdblistKeySource: '', ratingsPrimary: 'imdb', ratingsShown: ['imdb'], ratingsAvailable: ['imdb'],
  imdbAttribution: 'x', imdbAttributionUrl: 'https://www.imdb.com', mdblistAttribution: 'x', mdblistAttributionUrl: 'https://mdblist.com',
};

function api(req, res, u) {
  const p = u.pathname;
  if (p.startsWith('/api/images/')) {
    const entry = { path: p, image: true, aborted: false, done: false };
    mock.log.push(entry);
    const hue = Number(/(\d+)\.svg$/.exec(p)?.[1] ?? 0) % 360;
    const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="342" height="513"><rect width="100%" height="100%" fill="hsl(${hue} 40% 35%)"/></svg>`;
    const send = () => {
      if (res.destroyed) return;
      res.writeHead(200, { 'content-type': 'image/svg+xml', 'cache-control': 'max-age=3600' }).end(svg);
    };
    res.on('finish', () => (entry.done = true));
    res.on('close', () => (entry.aborted ||= !entry.done));
    return mock.slowImages ? setTimeout(send, mock.slowImages) : send();
  }
  const items = [...mock.films, ...Array.from({ length: mock.shows }, (_, i) => episode(i + 1))];
  if (p === '/api/info') return json(res, { id: 'x', name: 'e2e', version: 'dev', authRequired: false });
  if (p === '/api/scan') return json(res, { running: false, itemCount: items.length, watching: [] });
  if (p === '/api/items') return list(req, res, items);
  if (p === '/api/shows') return list(req, res, Array.from({ length: mock.shows }, (_, i) => showRow(i + 1)));
  if (['/api/continue', '/api/upcoming', '/api/folders', '/api/metadata/review', '/api/sessions', '/api/genres'].includes(p)) return json(res, []);
  if (p === '/api/metadata/status') return json(res, { enabled: true, keySet: true, running: false, pending: 0, unmatched: 0, needsReview: 0, lastError: '' });
  if (p === '/api/jobs') return json(res, { paused: false, running: [], queued: [], history: [], tier: { playback: {}, browsing: false, hold: { held: false } } });
  if (p === '/api/settings') return json(res, settings);
  if (p === '/api/events') return res.writeHead(503).end();
  const m = /^\/api\/items\/(\d+)$/.exec(p);
  const it = m && items.find((i) => i.id === Number(m[1]));
  if (it) {
    return json(res, { ...it, path: `/m/${it.id}.mkv`, size: 1, bitrate: 1, streams: [], subtitles: [],
      playback: { method: 'direct', videoAction: 'copy', audioAction: 'copy', audioIndex: 1, reasons: [], url: '', startSec: 0 } });
  }
  return json(res, { error: `unmocked ${p}` }, 404);
}

const server = createServer((req, res) => {
  const u = new URL(req.url, 'http://x');
  if (u.pathname.startsWith('/api/')) return api(req, res, u);
  serveFile(res, u.pathname);
});
await new Promise((r) => server.listen(0, '127.0.0.1', r));
const base = `http://127.0.0.1:${server.address().port}`;

/**
 * A fresh page on the mocked server.
 *   films   how many films (default 5,000)    shows  how many shows (default 300)
 *   rated   the films whose sort title matches this regex have a rating with a glyph
 *   slowImages  ms before each poster is answered
 *   width, height, touch, scale               the viewport
 */
async function open(browser, opts = {}) {
  mock = { films: Array.from({ length: opts.films ?? FILMS }, (_, i) => film(i + 1, opts.rated)), shows: opts.shows ?? 300, slowImages: opts.slowImages ?? 0, log: [] };
  const context = await browser.newContext({
    viewport: { width: opts.width ?? 1440, height: opts.height ?? 900 }, deviceScaleFactor: opts.scale ?? 1,
    hasTouch: !!opts.touch, isMobile: !!opts.touch, serviceWorkers: 'block',
  });
  const page = await context.newPage();
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  await page.goto('about:blank');
  return { context, page, errors };
}

const sorted = () => [...mock.films].sort(byName);
const hash = (page) => page.evaluate(() => location.hash);
const scrollY = (page) => page.evaluate(() => window.scrollY);
const frames = (page, n = 2) => page.evaluate((n) => new Promise((r) => { const f = (k) => (k ? requestAnimationFrame(() => f(k - 1)) : r()); f(n); }), n);
async function until(fn, ms = 4000) {
  const end = Date.now() + ms;
  while (Date.now() < end) {
    if (await fn()) return true;
    await sleep(40);
  }
  return false;
}
async function settle(page) {
  let last = -1;
  for (let i = 0; i < 50; i++) {
    const y = await scrollY(page);
    if (y === last) return y;
    last = y;
    await sleep(120);
  }
  return last;
}
async function movies(page, h = '#/movies') {
  await page.goto(`${base}/${h}`);
  await page.locator('main a.card').first().waitFor();
  await frames(page);
}
const cardCount = (page) => page.evaluate(() => document.querySelectorAll('main a.card').length);
/** The first row of cards wholly under the header: their links, left to right, and where it is. */
const topRow = (page) =>
  page.evaluate(() => {
    const under = document.querySelector('header')?.getBoundingClientRect().bottom ?? 0;
    const cards = [...document.querySelectorAll('main a.card')]
      .map((a) => ({ href: a.getAttribute('href'), r: a.getBoundingClientRect() }))
      .filter((c) => c.r.top >= under - 1 && c.r.top < innerHeight);
    const top = Math.min(...cards.map((c) => c.r.top));
    return { under, top, hrefs: cards.filter((c) => Math.abs(c.r.top - top) < 2).sort((a, b) => a.r.left - b.r.left).map((c) => c.href) };
  });
const middleCard = (page) =>
  page.evaluate(() => {
    let best = null;
    let far = Infinity;
    for (const a of document.querySelectorAll('main a.card')) {
      const r = a.getBoundingClientRect();
      const d = Math.hypot(r.left + r.width / 2 - innerWidth / 2, r.top + r.height / 2 - innerHeight / 2);
      if (d < far) [best, far] = [{ href: a.getAttribute('href'), top: Math.round(r.top) }, d];
    }
    return best;
  });
const active = (page) => page.evaluate(() => document.activeElement?.getAttribute('href') ?? document.activeElement?.tagName);
const cols = (page) => page.evaluate(() => getComputedStyle(document.querySelector('main .grid')).gridTemplateColumns.split(' ').length);
const railLetters = (page) => page.locator('nav[aria-label="Jump to a letter"] button').allTextContents();
/** The distance from one row of cards to the next, as drawn. */
const pitchOf = (page) =>
  page.evaluate(() => {
    const rows = [...document.querySelectorAll('main .grid .row')].map((r) => r.getBoundingClientRect().top).sort((a, b) => a - b);
    return rows[1] - rows[0];
  });
/** The posters in the page: on screen or not, above it or below, and how they load. */
const posterLoads = (page) =>
  page.evaluate(() =>
    [...document.querySelectorAll('main a.card img')].map((img) => {
      const r = img.closest('a').getBoundingClientRect();
      return { on: r.bottom > 0 && r.top < innerHeight, above: r.bottom <= 0, priority: img.getAttribute('fetchpriority'), loading: img.getAttribute('loading'), sizes: img.sizes, width: r.width };
    }),
  );

const scenarios = {
  // Mutant caught: every row mounted (mounted = all rows): 5,000 cards in the page.
  async 'Movies with 5,000 films keeps a screenful of cards in the page, wherever it is scrolled'(browser) {
    const { context, page, errors } = await open(browser);
    await movies(page);
    const count = (await page.locator('h1 .count').textContent())?.trim();
    check('the page counts all 5,000 films', count === '5000', count);
    const n = await cols(page);
    const pitch = await pitchOf(page);
    const most = n * (Math.ceil((3 * 900) / pitch) + 2 + 3); // three screens' rows, a part row at each end, the focused card's
    const counts = [];
    const max = await page.evaluate(() => document.documentElement.scrollHeight - innerHeight);
    check('the page is as tall as 5,000 films', max > (FILMS / n - 2) * 300, `${max} px for ${n} columns`);
    let centred = true;
    for (const f of [0, 0.1, 0.33, 0.5, 0.77, 0.9, 1]) {
      await page.evaluate((y) => window.scrollTo(0, y), Math.round(max * f));
      await frames(page, 3);
      counts.push(await cardCount(page));
      // The positive control: there are cards where the screen is.
      centred &&= await page.evaluate(() => !!document.elementFromPoint(innerWidth / 2, innerHeight / 2)?.closest('a.card, .row'));
    }
    check(`never more than ${most} cards in the page (${n} columns)`, Math.max(...counts) <= most && Math.min(...counts) > 0, counts.join(' '));
    check('and cards on screen at every place', centred);
    check('the last film is there at the bottom', (await page.locator(`a.card[href="#/item/${sorted().at(-1).id}"]`).count()) === 1);
    check('the DOM stays small', (await page.evaluate(() => document.getElementsByTagName('*').length)) < 2500);
    const list = await page.evaluate(() => {
      const items = [...document.querySelectorAll('main [role="list"] [role="listitem"]')];
      const last = items.at(-1);
      return { n: items.length, size: last?.getAttribute('aria-setsize'), pos: last?.getAttribute('aria-posinset'), href: last?.querySelector('a.card')?.getAttribute('href') };
    });
    check('a list of 5,000 to a screen reader: each card says where it is in it', list.n > 0 && list.size === '5000' && list.pos === '5000' && list.href === `#/item/${sorted().at(-1).id}`, JSON.stringify(list));
    check('no page errors', errors.length === 0, errors.join('; '));
    await context.close();
  },

  // Mutants caught: Picture's priority ignored (every poster lazy, priority auto); sizes left at the
  // card's default (200px: a w342 for a 180 px column); the rows above the screen given priority
  // high (only the rows below checked); at 2x, a srcset choice other than w500.
  async 'The posters on screen load first; those far away are not in the page'(browser) {
    const { context, page } = await open(browser);
    await movies(page);
    const imgs = await posterLoads(page);
    const on = imgs.filter((i) => i.on);
    const off = imgs.filter((i) => !i.on);
    check('posters on screen: fetchpriority high, loaded at once', on.length > 0 && on.every((i) => i.priority === 'high' && i.loading === 'eager'), `${on.length} on screen`);
    check('the rows beyond the screen: fetchpriority low, lazily', off.length > 0 && off.every((i) => i.priority === 'low' && i.loading === 'lazy'), `${off.length} beyond`);
    await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight / 2));
    await frames(page, 3);
    const mid = await posterLoads(page);
    const midOn = mid.filter((i) => i.on);
    const above = mid.filter((i) => i.above);
    const below = mid.filter((i) => !i.on && !i.above);
    check('mid-list: on screen high', midOn.length > 0 && midOn.every((i) => i.priority === 'high' && i.loading === 'eager'), `${midOn.length} on screen`);
    check('mid-list: the rows above the screen low', above.length > 0 && above.every((i) => i.priority === 'low' && i.loading === 'lazy'), `${above.length} above: ${[...new Set(above.map((i) => i.priority))]}`);
    check('mid-list: the rows below the screen low', below.length > 0 && below.every((i) => i.priority === 'low' && i.loading === 'lazy'), `${below.length} below`);
    await page.evaluate(() => window.scrollTo(0, 0));
    await frames(page, 3);
    check('sizes is the width of a column', imgs.every((i) => i.sizes === `${Math.ceil(i.width)}px`), `${imgs[0]?.sizes} for ${imgs[0]?.width}`);
    await page.locator('main a.card img.loaded').first().waitFor();
    const src = await page.locator('main a.card img').first().evaluate((i) => i.currentSrc);
    check('and the poster is the smallest that fills it (w185 for a column under 185 px)', imgs[0].width <= 185 ? src.includes('/w185/') : src.includes('/w342/'), src);
    const last = `a.card[href="#/item/${sorted().at(-1).id}"]`;
    check('the last film is not in the page at the top', (await page.locator(last).count()) === 0);
    await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight));
    await frames(page, 3);
    check('it is at the bottom (the positive control)', (await page.locator(last).count()) === 1);
    check('and the first is gone from the page', (await page.locator(`a.card[href="#/item/${sorted()[0].id}"]`).count()) === 0);
    await context.close();

    // At 2x a 177 px column needs 354 px: w500, the next size up from w342. Pinned.
    const hi = await open(browser, { scale: 2 });
    await movies(hi.page);
    await hi.page.locator('main a.card img.loaded').first().waitFor();
    const two = await hi.page.locator('main a.card img').first().evaluate((i) => ({ sizes: i.sizes, width: i.getBoundingClientRect().width, src: i.currentSrc }));
    check('at 2x: sizes is still the column, and the srcset picks w500', two.sizes === `${Math.ceil(two.width)}px` && two.width * 2 > 342 && two.src.includes('/w500/'), JSON.stringify(two));
    await hi.context.close();
  },

  // Mutants caught: no hold (every row's posters asked for as it mounts: a fling starts ~1,000
  // requests); a poster unmounted before it loaded keeping its src (Chrome lets the request run:
  // none aborted when the screen moves on).
  async 'A fling asks for almost no posters; a slow scroll loads every row; a poster scrolled past lets go'(browser) {
    const { context, page } = await open(browser, { slowImages: 150 });
    await movies(page);
    await sleep(1000);
    const onScreen = () => [...document.querySelectorAll('main a.card img')].filter((i) => { const b = i.getBoundingClientRect(); return b.bottom > 0 && b.top < innerHeight; });
    let from = mock.log.length;
    for (let i = 1; i <= 40; i++) {
      await page.evaluate((f) => window.scrollTo(0, document.documentElement.scrollHeight * f), i / 41);
      await frames(page, 1);
    }
    const t0 = Date.now();
    const landed = await page
      .waitForFunction((f) => new Function(`return (${f})()`)().every((i) => i.complete && i.naturalWidth > 0), onScreen.toString(), { timeout: 8000 })
      .then(() => true, () => false);
    const ms = Date.now() - t0;
    const fling = mock.log.slice(from).filter((l) => l.image);
    const lastScreen = await page.evaluate((f) => new Function(`return (${f})()`)().length, onScreen.toString());
    check('a fling through 5,000 films starts fewer than 100 poster requests', fling.length < 100, `${fling.length} (${lastScreen} of them the last screen's)`);
    check('the last screen of posters is in within 3 s', landed && ms < 3000, `${landed ? ms : '> 8000'} ms`);

    // The positive control: a scroll at reading speed, 3 screens at 40 px a frame. Every poster on
    // screen has been asked for by the time it is there (its row was mounted a screen ahead).
    from = mock.log.length;
    await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight / 3));
    await sleep(600);
    const slow = await page.evaluate(
      (f) =>
        new Promise((done) => {
          const visible = new Function(`return (${f})()`);
          const seen = new Set();
          let missing = 0;
          let k = 0;
          const step = () => {
            window.scrollBy(0, 40);
            for (const i of visible()) {
              seen.add(i.closest('a').getAttribute('href'));
              if (k > 3 && !i.getAttribute('src')) missing++;
            }
            if (k++ < 68) requestAnimationFrame(step);
            else done({ seen: seen.size, missing });
          };
          requestAnimationFrame(step);
        }),
      onScreen.toString(),
    );
    await sleep(400);
    const asked = new Set(mock.log.slice(from).filter((l) => l.image).map((l) => l.path));
    check('a slow scroll: every poster on screen already asked for', slow.seen > 30 && slow.missing === 0, `${slow.missing} unasked on screen, ${slow.seen} cards passed`);
    check('and every row it passed was asked for', asked.size >= slow.seen, `${asked.size} posters for ${slow.seen} cards`);
    await context.close();

    // Stop and go: each stop long enough for the posters to be asked for, not to arrive (2 s).
    const sg = await open(browser, { slowImages: 2000 });
    await movies(sg.page);
    await sleep(500);
    from = mock.log.length;
    for (let i = 1; i <= 6; i++) {
      await sg.page.evaluate((f) => window.scrollTo(0, document.documentElement.scrollHeight * f), i / 7);
      await sleep(300);
    }
    await sleep(2500);
    const stops = mock.log.slice(from).filter((l) => l.image);
    check('stop and go: posters asked for at each stop (the positive control)', stops.length > 60, String(stops.length));
    check('those scrolled past before they came are let go (aborted)', stops.filter((l) => l.aborted).length > stops.length / 2, `${stops.filter((l) => l.aborted).length} of ${stops.length} aborted`);
    await sg.context.close();
  },

  // Mutants caught: a card's line under the title only there when it has something (or as tall as
  // a rating glyph): the first row with a glyph, mounting deep in the list, made every row move;
  // the pitch not kept when the cards change height (no re-anchoring).
  async 'Cards with a rating glyph are as tall as the rest: scrolling past the first moves nothing else'(browser) {
    const { context, page, errors } = await open(browser, { rated: /^[YZ]/ });
    await movies(page);
    const list = sorted();
    const n = await cols(page);
    const firstRated = list.findIndex((f) => f.rating);
    const p0 = await pitchOf(page);
    const gridTop = await page.evaluate(() => document.querySelector('main .grid').getBoundingClientRect().top + scrollY);
    // Eight rows above the first rated row: no glyph mounted yet.
    await page.evaluate((y) => window.scrollTo(0, y), gridTop + (Math.floor(firstRated / n) - 8) * p0);
    await frames(page, 3);
    const glyphsBefore = await page.locator('main .glyph').count();
    const tracked = await middleCard(page);
    let worst = 0;
    let last = { y: await scrollY(page), top: tracked.top };
    let sawGlyph = false;
    for (let s = 0; s < 40; s++) {
      await page.evaluate(() => window.scrollBy(0, 40));
      await frames(page, 2);
      const now = await page.evaluate((h) => ({ y: scrollY, top: document.querySelector(`a.card[href="${h}"]`)?.getBoundingClientRect().top ?? null }), tracked.href);
      if (now.top === null) break;
      worst = Math.max(worst, Math.abs(now.top - last.top + (now.y - last.y)));
      last = now;
      sawGlyph ||= (await page.locator('main .glyph').count()) > 0;
    }
    check('the rated films come later in the list (the positive control: no glyph at first)', glyphsBefore === 0 && sawGlyph, `${glyphsBefore} at first, a glyph later: ${sawGlyph}`);
    check('the tracked card moves by the scroll and nothing else', worst <= 1, `${worst} px off`);
    check('the pitch is the same with glyphs in the page', Math.abs((await pitchOf(page)) - p0) < 0.5, `${p0} → ${await pitchOf(page)}`);
    const heights = await page.evaluate(() => [...new Set([...document.querySelectorAll('main a.card')].map((a) => Math.round(a.getBoundingClientRect().height * 10) / 10))]);
    check('watched films have their check in the page (the positive control for the next)', (await page.locator('main a.card [data-watch]').count()) > 0);
    check('every card as tall', heights.length === 1, heights.join(' '));
    check('no page errors', errors.length === 0, errors.join('; '));
    await context.close();
  },

  // Mutants caught: the rows' ResizeObserver never updating the card height (the pitch left at its
  // estimate); the height not measured again when the columns get narrower (the old, taller one
  // kept); a resize not keeping the first card on screen in the first row.
  async 'The rows are one card and one gap apart, measured; a resize keeps the place'(browser) {
    const { context, page } = await open(browser);
    await movies(page);
    // Cards taller than the grid's estimate (their picture and 52 px of text).
    await page.addStyleTag({ content: 'main .card .title { margin-top: 3rem; }' });
    await frames(page, 4);
    const spacing = () =>
      page.evaluate(() => {
        const gap = parseFloat(getComputedStyle(document.querySelector('main .grid')).rowGap);
        const rows = [...document.querySelectorAll('main .grid .row')].map((r) => r.getBoundingClientRect()).sort((a, b) => a.top - b.top);
        const card = document.querySelector('main a.card').getBoundingClientRect().height;
        return { off: Math.max(...rows.slice(1).map((r, i) => Math.abs(r.top - rows[i].bottom - gap))), card, estimate: document.querySelector('main a.card .picture').getBoundingClientRect().height + 52 };
      });
    const a = await spacing();
    check('the cards are not the estimate (the positive control)', Math.abs(a.card - a.estimate) > 20, `${a.card} vs ${a.estimate}`);
    check('rows one gap apart', a.off < 1, `${a.off} px off`);
    await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight * 0.6));
    await settle(page);
    const first = (await topRow(page)).hrefs[0];
    await page.setViewportSize({ width: 1000, height: 900 });
    await frames(page, 4);
    await settle(page);
    const b = await spacing();
    check('narrower columns: rows one gap apart again (shorter cards measured)', b.off < 1 && b.card < a.card, `${b.off} px off, cards ${a.card} → ${b.card}`);
    const row = await topRow(page);
    check('the film first on screen is still in the first row', row.hrefs.includes(first), `${first}: ${row.hrefs.join(' ')}`);
    await context.close();
  },

  // Mutants caught: the letters taken from the title, not the sort title (a T for "The ..."); the
  // rail shown in every sort; scrollToIndex landing a row off (the gap or the header left out).
  async 'The A to Z rail: the letters of the list, in title order only; a click jumps'(browser) {
    const { context, page, errors } = await open(browser);
    await movies(page);
    const want = [...new Set(sorted().map((f) => letterOf(f.sortTitle)))].sort();
    const got = await railLetters(page);
    check('the rail has the letters the films are listed under, # first', JSON.stringify(got) === JSON.stringify(want), got.join(''));
    check('none for the articles: no T, though there are films called "The ..."', !got.includes('T') && mock.films.some((f) => f.title.startsWith('The ')));
    check('it is named', (await page.locator('nav[aria-label="Jump to a letter"] button', { hasText: 'M' }).getAttribute('aria-label')) === 'Titles starting with M');
    const opacity = () => page.locator('nav.az').evaluate((n) => Number(getComputedStyle(n).opacity));
    const faint = await opacity();
    await page.locator('nav.az').hover();
    await sleep(300);
    check('faint until hovered', faint < 0.6 && (await opacity()) === 1, `${faint} → ${await opacity()}`);

    const firstM = sorted().find((f) => letterOf(f.sortTitle) === 'M');
    await page.locator('nav.az button', { hasText: 'M' }).click();
    await frames(page, 3);
    const row = await topRow(page);
    check('M: the first M film is in the first row under the header', row.hrefs.includes(`#/item/${firstM.id}`), `${firstM.title}: ${row.hrefs.join(' ')}`);
    check('just under it', row.top - row.under >= 0 && row.top - row.under <= 40, `${row.top - row.under} px`);

    await page.goto(`${base}/#/movies?sort=year`);
    await page.locator('main a.card').first().waitFor();
    check('no rail sorted by year', (await page.locator('nav.az').count()) === 0);
    // Home's Movies rail is in the same order (its first 30).
    await page.goto(`${base}/#/`);
    await page.locator('a', { hasText: `All ${FILMS}` }).waitFor();
    const homeRail = await page.locator('a', { hasText: `All ${FILMS}` }).evaluate((more) => {
      let box = more.parentElement;
      while (box && !box.querySelector('a.card')) box = box.parentElement;
      return [...(box?.querySelectorAll('a.card') ?? [])].map((a) => a.getAttribute('href'));
    });
    const want30 = sorted().slice(0, 30).map((f) => `#/item/${f.id}`);
    check("Home's Movies rail is in the same order as Movies", JSON.stringify(homeRail.slice(0, 30)) === JSON.stringify(want30), homeRail.slice(0, 5).join(' '));
    await page.goto(`${base}/#/shows`);
    await page.locator('main a.card').first().waitFor();
    check('Shows have one', (await railLetters(page)).length > 5);
    check('no page errors', errors.length === 0, errors.join('; '));
    await context.close();

    const short = await open(browser, { films: 30, shows: 12 });
    await movies(short.page);
    const shortRail = await short.page.locator('nav.az').count();
    const shortCount = (await short.page.locator('h1 .count').textContent())?.trim();
    check('no rail on a short list (30 films)', shortRail === 0 && shortCount === '30' && (await cardCount(short.page)) > 0, `rail ${shortRail}, ${shortCount} films`);
    await short.context.close();
  },

  // Mutants caught: a touch drag not followed (a jump only where it started); the bubble never
  // shown; touch-action left to the browser (the drag scrolls the page instead).
  async 'Dragging a finger along the rail jumps as it goes, the letter shown big'(browser) {
    const { context, page } = await open(browser, { width: 375, height: 780, touch: true, scale: 2 });
    await movies(page);
    const cdp = await context.newCDPSession(page);
    const box = async (l) => page.locator('nav.az button', { hasText: new RegExp(`^${l}$`) }).boundingBox();
    const a = await box('A');
    const z = await box('Z');
    const x = a.x + a.width / 2;
    const touch = (type, y) => cdp.send('Input.dispatchTouchEvent', { type, touchPoints: type === 'touchEnd' ? [] : [{ x, y }] });
    await touch('touchStart', a.y + a.height / 2);
    const seen = [];
    const steps = 12;
    for (let s = 1; s <= steps; s++) {
      await touch('touchMove', a.y + a.height / 2 + ((z.y - a.y) * s) / steps);
      await frames(page, 2);
      seen.push({ y: await scrollY(page), bubble: await page.evaluate(() => document.querySelector('.bubble')?.textContent?.trim()) });
    }
    const ys = seen.map((s) => s.y);
    check('the page jumps as the finger moves', new Set(ys).size >= 6 && ys.every((y, i) => i === 0 || y >= ys[i - 1]), ys.join(' '));
    check('the big letter follows the finger', seen.at(-1).bubble === 'Z' && new Set(seen.map((s) => s.bubble)).size >= 6, seen.map((s) => s.bubble).join(''));
    await touch('touchEnd');
    await frames(page, 2);
    check('and goes when it lifts', (await page.locator('.bubble').count()) === 0);
    const firstZ = sorted().find((f) => letterOf(f.sortTitle) === 'Z');
    const row = await topRow(page);
    check('the first Z film is at the top', row.hrefs.includes(`#/item/${firstZ.id}`), `${firstZ.title}: ${row.hrefs.join(' ')}`);
    await context.close();
  },

  // Mutants caught: the letters' size not a 24 px target (the rail as thin as before); the grid not
  // making room for the rail where the gutter is narrower (cards under the letters at 375 and 650
  // px); a short screen squeezing every letter in (targets under 24 px).
  async "The rail's letters are 24 px targets, clear of the cards, at every width"(browser) {
    for (const [width, height] of [[375, 780], [650, 900], [700, 900], [1440, 900], [900, 420]]) {
      const { context, page } = await open(browser, { width, height, touch: width < 600 });
      await movies(page);
      const r = await page.evaluate(() => {
        const nav = document.querySelector('nav.az').getBoundingClientRect();
        const buttons = [...document.querySelectorAll('nav.az button')].map((b) => b.getBoundingClientRect());
        const right = Math.max(...[...document.querySelectorAll('main a.card')].map((a) => a.getBoundingClientRect().right));
        return { small: Math.min(...buttons.map((b) => Math.min(b.width, b.height))), shown: buttons.length, overlap: right - nav.left };
      });
      const all = new Set(sorted().map((f) => letterOf(f.sortTitle))).size;
      check(`${width}×${height}: every letter a 24 px target`, r.small >= 24, `${r.small} px`);
      check(`${width}×${height}: no card under the rail`, r.overlap <= 0, `${r.overlap} px`);
      if (height < 500) check(`${width}×${height}: a short screen shows fewer letters`, r.shown < all && r.shown >= 2, `${r.shown} of ${all}`);
      else check(`${width}×${height}: all the letters`, r.shown === all, `${r.shown} of ${all}`);
      await context.close();
    }
  },

  // Mutants caught: a keyboard jump that leaves the focus on the rail; the arrow keys not handled;
  // the focused card's row unmounted when it scrolls away (the focus lost to the body); every
  // letter a Tab stop (no roving tabindex); ↓ or End not moving between the letters.
  async 'The rail and the cards from the keyboard'(browser) {
    const { context, page } = await open(browser);
    await movies(page);
    await page.locator('a.chip[role="radio"]').last().focus();
    await page.keyboard.press('Tab');
    const letter = () => page.evaluate(() => (document.activeElement?.closest('nav[aria-label="Jump to a letter"]') ? document.activeElement.textContent : null));
    check('Tab reaches the rail after the controls, at its first letter', (await letter()) === '#', await page.evaluate(() => document.activeElement?.outerHTML.slice(0, 80)));
    await page.keyboard.press('Tab');
    check('the rail is one Tab stop: the next Tab is the first card', (await active(page)) === `#/item/${sorted()[0].id}`, await active(page));
    await page.keyboard.press('Shift+Tab');
    await page.keyboard.press('End');
    check('End: the last letter', (await letter()) === 'Z', await letter());
    await page.keyboard.press('Home');
    const letters = await railLetters(page);
    for (let i = 0; i < letters.indexOf('R'); i++) await page.keyboard.press('ArrowDown');
    check('↓ moves down the letters, to R', (await letter()) === 'R', await letter());
    await page.keyboard.press('ArrowUp');
    await page.keyboard.press('ArrowDown');
    check('↑ back up and ↓ again', (await letter()) === 'R', await letter());
    await page.keyboard.press('Enter');
    const firstR = sorted().find((f) => letterOf(f.sortTitle) === 'R');
    await until(async () => (await active(page)) === `#/item/${firstR.id}`, 2000);
    check('Enter on R jumps and moves the focus to the first R film', (await active(page)) === `#/item/${firstR.id}`, await active(page));
    check('which is on screen', (await topRow(page)).hrefs.includes(`#/item/${firstR.id}`));

    const n = await cols(page);
    const list = sorted();
    const at = list.indexOf(firstR);
    await page.keyboard.press('ArrowRight');
    check('→ moves to the next card', (await active(page)) === `#/item/${list[at + 1].id}`, await active(page));
    await page.keyboard.press('ArrowDown');
    check('↓ to the card below', (await active(page)) === `#/item/${list[at + 1 + n].id}`, await active(page));
    for (let i = 0; i < 12; i++) await page.keyboard.press('ArrowDown');
    const far = `#/item/${list[at + 1 + 13 * n].id}`;
    check('↓ twelve times more: rows down, still on a card', (await active(page)) === far, await active(page));
    check('scrolled into view', await page.evaluate(() => { const r = document.activeElement.getBoundingClientRect(); return r.top >= 0 && r.bottom <= innerHeight; }));
    await page.keyboard.press('ArrowLeft');
    check('← back one', (await active(page)) === `#/item/${list[at + 13 * n].id}`, await active(page));
    await page.keyboard.press('Tab');
    check('Tab goes on to the next card', (await active(page)) === far, await active(page));

    await page.evaluate(() => window.scrollTo(0, 0));
    await frames(page, 3);
    const off = await page.evaluate(() => document.activeElement.getBoundingClientRect().top > innerHeight);
    check('scrolled far away, the focused card stays in the page, with the focus', off && (await active(page)) === far, `${await active(page)} off screen: ${off}`);
    check('and the cards stay few', (await cardCount(page)) <= n * 12);
    await page.keyboard.press('ArrowDown');
    check('the keys go on from it', (await active(page)) === `#/item/${list[at + 1 + 14 * n].id}`, await active(page));
    await context.close();
  },

  // Mutants caught: the grid not claiming the restore (the router's scroll alone: at another width
  // other cards are at the top); the first card on screen not remembered (Back at the top); the
  // focus not put back.
  async 'Back to 5,000 films far down: the grid puts itself back, at another width too'(browser) {
    const { context, page, errors } = await open(browser);
    await page.goto(`${base}/#/`);
    await page.locator('header nav a[href="#/movies"]').click();
    await page.locator('main a.card').first().waitFor();
    await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight * 0.6));
    const y0 = await settle(page);
    const card = await middleCard(page);
    await page.locator(`a.card[href="${card.href}"]`).click();
    await page.locator('a.back').waitFor();
    await page.locator('a.back').click();
    await until(async () => (await hash(page)) === '#/movies');
    const y1 = await settle(page);
    const after = await middleCard(page);
    check('Back: the same card in the middle, in the same place', after?.href === card.href && Math.abs(after.top - card.top) <= 4, `${JSON.stringify(card)} → ${JSON.stringify(after)} (${y0} → ${y1})`);
    check('the focus is back on it', (await active(page)) === card.href, await active(page));

    // The window narrower meanwhile: fewer columns, so the same scroll position is other films.
    const first = (await topRow(page)).hrefs[0];
    await page.locator(`a.card[href="${card.href}"]`).click();
    await page.locator('a.back').waitFor();
    await sleep(800); // the card's morph (a view transition) over
    await page.setViewportSize({ width: 1000, height: 900 });
    await page.locator('a.back').click();
    await until(async () => (await hash(page)) === '#/movies');
    await settle(page);
    const row = await topRow(page);
    check('Back at another width: the film that was first on screen is in the first row', row.hrefs.includes(first), `${first}: ${row.hrefs.join(' ')}`);

    await page.reload();
    await page.locator('main a.card').first().waitFor();
    await until(async () => (await scrollY(page)) > 0);
    await settle(page);
    check('and after a reload', (await topRow(page)).hrefs.includes(first), (await topRow(page)).hrefs.join(' '));
    check('a reload leaves the focus alone', (await active(page)) === 'BODY', await active(page));
    check('no page errors', errors.length === 0, errors.join('; '));
    await context.close();
  },

  // Mutant caught: the list fetched with cache: 'no-store' (no If-None-Match: a 200 every time).
  async 'A revisit of the list asks with If-None-Match and is answered 304'(browser) {
    const { context, page } = await open(browser);
    await movies(page);
    const first = mock.log.filter((l) => l.path === '/api/items');
    check('the first visit gets the list (200)', first.length === 1 && first[0].status === 200 && first[0].inm === '', JSON.stringify(first));
    await page.reload();
    await page.locator('main a.card').first().waitFor();
    const again = mock.log.filter((l) => l.path === '/api/items' || l.path === '/api/shows');
    check('the revisit sends its ETag: 304 for the items and the shows', again.length === 4 && again.slice(2).every((l) => l.inm && l.status === 304), JSON.stringify(again.map((l) => `${l.path} ${l.status} ${l.inm ? 'If-None-Match' : '-'}`)));
    check('and shows the films from the cache', (await page.locator('h1 .count').textContent())?.trim() === '5000');
    await context.close();
  },
};

// ---- PLY_E2E_MEASURE=1: the numbers for a before and after ----------------------------------------

async function measure(browser) {
  const { context, page } = await open(browser);
  await page.addInitScript(() => {
    window.__long = [];
    new PerformanceObserver((l) => window.__long.push(...l.getEntries().map((e) => e.duration))).observe({ type: 'longtask', buffered: true });
  });
  const cdp = await context.newCDPSession(page);
  await cdp.send('Performance.enable');
  const heap = async () => {
    await cdp.send('HeapProfiler.collectGarbage');
    const { metrics } = await cdp.send('Performance.getMetrics');
    return Math.round(metrics.find((m) => m.name === 'JSHeapUsedSize').value / 1e5) / 10;
  };
  const longs = () => page.evaluate(() => { const l = window.__long; window.__long = []; return { count: l.length, total: Math.round(l.reduce((a, b) => a + b, 0)), max: Math.round(Math.max(0, ...l)) }; });
  await page.goto(`${base}/#/`);
  await page.locator('header nav a[href="#/movies"]').waitFor();
  await sleep(500);
  await longs();
  const heapHome = await heap();
  const t0 = Date.now();
  await page.locator('header nav a[href="#/movies"]').click();
  await page.locator('main a.card').first().waitFor();
  await frames(page, 2);
  const mountMs = Date.now() - t0;
  await sleep(500);
  const mount = await longs();
  const nodes = await page.evaluate(() => document.getElementsByTagName('*').length);
  const cards = await cardCount(page);
  const heapMovies = await heap();
  // Scroll frame times: 300 frames at 60 px a frame.
  const scroll = await page.evaluate(
    () =>
      new Promise((done) => {
        const d = [];
        let last = performance.now();
        const step = (k) => {
          const now = performance.now();
          d.push(now - last);
          last = now;
          window.scrollBy(0, 60);
          if (k) requestAnimationFrame(() => step(k - 1));
          else {
            d.shift();
            d.sort((a, b) => a - b);
            const q = (p) => Math.round(d[Math.floor(p * (d.length - 1))] * 10) / 10;
            done({ p50: q(0.5), p95: q(0.95), max: q(1), over25: d.filter((x) => x > 25).length });
          }
        };
        requestAnimationFrame(() => step(300));
      }),
  );
  await sleep(300);
  const scrollLong = await longs();
  await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight * 0.6));
  await settle(page);
  const card = await middleCard(page);
  await page.locator(`a.card[href="${card.href}"]`).click();
  await page.locator('a.back').waitFor();
  await sleep(500);
  await longs();
  const t1 = Date.now();
  await page.locator('a.back').click();
  await until(async () => (await middleCard(page))?.href === card.href, 8000);
  const backMs = Date.now() - t1;
  await sleep(500);
  const back = await longs();
  console.log(JSON.stringify({ films: FILMS, mountMs, mount, nodes, cards, heapHomeMB: heapHome, heapMoviesMB: heapMovies, scroll, scrollLong, backMs, back }, null, 1));
  await context.close();
}

const browser = await chromium.launch({ channel: 'chrome', headless: true });
const only = process.env.PLY_E2E_ONLY;
try {
  if (process.env.PLY_E2E_MEASURE === '1') await measure(browser);
  else {
    for (const [name, run] of Object.entries(scenarios)) {
      if (only && !name.includes(only)) continue;
      try {
        await run(browser);
      } catch (e) {
        check(name, false, e instanceof Error ? e.message.split('\n')[0] : String(e));
      }
    }
  }
} finally {
  await browser.close();
  server.close();
}
if (process.env.PLY_E2E_MEASURE !== '1') {
  console.log(results.join('\n'));
  const failed = results.filter((r) => r.startsWith('FAIL')).length;
  console.log(`\n${results.length - failed} passed, ${failed} failed`);
  process.exit(failed ? 1 : 0);
}
