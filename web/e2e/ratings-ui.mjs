// The M10 ratings UI in a real browser, against a mocked API: the rating chips of a title's page
// (order, formats, links, tooltips, Metacritic's bands), the primary rating on cards, sorting and
// filtering the Movies and Shows pages by rating, an episode's IMDb rating, and the Settings
// Ratings card (the MDBList key, Test, the primary rating, the shown sources and their order, the
// notices). The unit tests pin the helpers (lib/ratings.ts, the api calls); this pins what the
// pages do with them.
//
// Self-contained: serves web/dist (build it first: `npm run build`; PLY_E2E_DIST picks another
// directory) and answers every /api/ request with page.route. Needs Google Chrome.
//
//   npm run build && node e2e/ratings-ui.mjs
//
// PLY_E2E_ONLY=<text> runs the scenarios whose name contains it. PLY_E2E_SHOTS=<dir> also saves
// screenshots (the chips, a card grid, the Settings card; wide and at 360 px). Each scenario names
// the mutation it was confirmed to catch (build the mutant into another directory with
// `vite build --outDir ... --emptyOutDir` and point PLY_E2E_DIST at it).
//
// Every key here is an obvious fake: the repository is public.
import { createServer } from 'node:http';
import { existsSync, mkdirSync, readFileSync } from 'node:fs';
import { dirname, extname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright-core';
import { routeItems } from './mock-items.mjs';

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
const text = async (loc) => ((await loc.textContent()) ?? '').replace(/\s+/g, ' ').trim();

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

const DAY = 86_400_000;
const ago = (days) => new Date(Date.now() - days * DAY - 3_600_000).toISOString();
// An entry as internal/api/ratings.go sends it: score100 is value / scale × 100, rounded.
const entry = (source, value, scale, over = {}) => ({ source, value, scale, score100: Math.round((value / scale) * 100), url: '', fetchedAt: ago(3), ...over });

const IMDB_URL = 'https://www.imdb.com/title/tt0113277/';
const RT_URL = 'https://www.rottentomatoes.com/m/heat_1995';
const ALL = (metascore = 74) => [
  entry('imdb', 8.7, 10, { votes: 1_234_567, url: IMDB_URL }),
  entry('rt_critics', 93, 100, { state: 'fresh', url: RT_URL }),
  entry('rt_audience', 41, 100, { state: 'spilled', votes: 250_000, url: 'https://www.rottentomatoes.com/m/heat_1995#audience' }),
  entry('metacritic', metascore, 100, { url: 'https://www.metacritic.com/movie/heat-1995/' }),
  entry('letterboxd', 4.08, 5, { votes: 300_000, url: 'https://letterboxd.com/film/heat-1995/' }),
  entry('trakt', 84, 100, { votes: 40_000, url: 'https://trakt.tv/movies/heat-1995' }),
  entry('tmdb', 7.84, 10, { votes: 7000 }), // no url: a span, not a link
];

const movie = (id, title, rating, over = {}) => ({
  id, kind: 'movie', title, year: 1990 + id, durationSec: 6000 + id, container: 'mkv', videoCodec: 'h264', audioCodec: 'aac',
  width: 1920, height: 1080, addedAt: id, progress: null, art: null, titleId: 100 + id, matchState: 'matched', needsReview: false, rating, ...over,
});
const episode = (id, show, season, ep, ratings, rating = null) => ({
  id, kind: 'episode', title: show, show, season, episode: ep, episodeTitle: `Episode ${ep}`, durationSec: 2800, container: 'mkv',
  videoCodec: 'h264', audioCodec: 'aac', width: 1920, height: 1080, addedAt: id, progress: null, art: null, titleId: show === 'Lost' ? 77 : 78,
  matchState: 'matched', needsReview: false, rating, ratings,
});
// Score100: Brazil 90, Heat 87, Cube 80, Alien 65, Dune none. By each source's own value instead:
// Alien 65, Heat 8.7, Cube 8, Brazil 4.5. A sort by value is told apart from one by score100.
const ITEMS = [
  movie(1, 'Heat', entry('imdb', 8.7, 10, { votes: 1_234_567 })),
  movie(2, 'Alien', entry('rt_critics', 65, 100, { state: 'fresh' })),
  movie(3, 'Brazil', entry('letterboxd', 4.5, 5)),
  movie(4, 'Cube', entry('imdb', 8.0, 10)),
  movie(5, 'Dune', null),
  episode(11, 'Lost', 1, 1, [entry('imdb', 8.9, 10, { votes: 5000, url: 'https://www.imdb.com/title/tt0636289/' })], entry('imdb', 9.0, 10)),
  episode(12, 'Lost', 1, 2, [], entry('imdb', 9.0, 10)),
  episode(21, 'Dark', 1, 1, [], entry('rt_audience', 85, 100)),
  episode(31, 'Fringe', 1, 1, [], null),
];
// Lost IMDb 9.0 (90), Dark RT audience 85% (85), Fringe none: by value Dark (85) would beat Lost (9).
const SHOWS = [
  { show: 'Dark', seasons: [1], episodeCount: 1, matchState: 'matched', needsReview: false, rating: entry('rt_audience', 85, 100) },
  { show: 'Fringe', seasons: [1], episodeCount: 1, matchState: 'matched', needsReview: false, rating: null },
  { show: 'Lost', seasons: [1], episodeCount: 2, matchState: 'matched', needsReview: false, rating: entry('imdb', 9.0, 10) },
];

// The Activity page's history (one finished ratings job), for the check of its own .panel.
const JOBS = [
  { id: 1, kind: 'ratings.imdb', key: 'imdb', title: 'Fetch IMDb ratings', priority: 40, state: 'done', user: false, done: 0, total: 0, attempts: 1, createdAt: ago(1), startedAt: ago(1), finishedAt: ago(1) },
];

// What the server sends (internal/api/ratings.go), word for word.
const IMDB_NOTICE = 'Information courtesy of IMDb (https://www.imdb.com). Used with permission.';
const MDBLIST_NOTICE = 'Ratings from Rotten Tomatoes, Metacritic, Letterboxd and Trakt provided by MDBList.';
const SOURCES = ['imdb', 'rt_critics', 'rt_audience', 'metacritic', 'letterboxd', 'trakt', 'tmdb'];
const settings = (over = {}) => ({
  tmdbKeySet: true, tmdbKeySource: 'settings', metadataEnabled: true, metadataForcedOff: false, language: 'en-US',
  maxStreams: 4, maxTranscodes: 2, maxStreamsSource: 'default', maxTranscodesSource: 'default',
  tvdbKeySet: false, tvdbPinSet: false, tvdbKeySource: '', tvdbAttribution: 'x', tvdbAttributionUrl: 'https://thetvdb.com',
  mdblistKeySet: false, mdblistKeySource: '', ratingsPrimary: 'imdb', ratingsShown: [...SOURCES], ratingsAvailable: ['imdb', 'tmdb'],
  imdbAttribution: IMDB_NOTICE, imdbAttributionUrl: 'https://www.imdb.com', mdblistAttribution: MDBLIST_NOTICE, mdblistAttributionUrl: 'https://mdblist.com',
  imdbDatasetUpdatedAt: '2026-11-03T06:00:00Z',
  ...over,
});

/**
 * A fresh page on the mocked server.
 *   settings   GET /api/settings (merged over the defaults above)
 *   put        (body, n) => [status, json] for the n-th PUT or PATCH of /api/settings
 *   putDelay   (body, n) => ms before the n-th PUT or PATCH of /api/settings is answered (what it
 *              stores is stored when it is answered, as a server that got it late would)
 *   hang       (body, n) => true for a PUT or PATCH of /api/settings that is never answered
 *   getDelay   (n) => ms before the n-th GET /api/settings is answered (with the settings as they
 *              were when it was asked)
 *   test       (provider, n) => json for the n-th POST /api/settings/test
 *   ratings    the film's ratings (title detail of item 1)
 *   width      the viewport's width
 * `calls` lists the non-GET requests; `hits` counts the GETs of each path; `server()` is what the
 * server has stored.
 */
async function open(browser, opts = {}) {
  const context = await browser.newContext({ viewport: { width: opts.width ?? 1280, height: 900 }, serviceWorkers: 'block' });
  const page = await context.newPage();
  const calls = [];
  const counts = {};
  const hits = {};
  const errors = [];
  let stored = settings(opts.settings);
  const store = (b) => {
    const next = { ...stored };
    if (b.mdblistKey !== undefined) {
      Object.assign(next, b.mdblistKey ? { mdblistKeySet: true, mdblistKeySource: 'settings' } : { mdblistKeySet: false, mdblistKeySource: '' });
      next.ratingsAvailable = b.mdblistKey ? [...SOURCES] : ['imdb', 'tmdb'];
    }
    if (b.ratingsPrimary !== undefined) next.ratingsPrimary = b.ratingsPrimary;
    if (b.ratingsShown !== undefined) next.ratingsShown = b.ratingsShown;
    stored = next;
    return { ...next, ...(b.mdblistKey ? { mdblistKeyChecked: !next.metadataForcedOff } : {}) };
  };
  page.on('pageerror', (e) => errors.push(e.message));
  const json = (route, body, st = 200) => route.fulfill({ status: st, contentType: 'application/json', body: JSON.stringify(body) });
  await page.route('**/api/**', async (route) => {
    const req = route.request();
    const p = new URL(req.url()).pathname;
    const method = req.method();
    const body = req.postData() ? JSON.parse(req.postData()) : null;
    if (method !== 'GET') {
      calls.push({ method, path: p, body });
      counts[`${method} ${p}`] = (counts[`${method} ${p}`] ?? 0) + 1;
    } else hits[p] = (hits[p] ?? 0) + 1;
    const n = method === 'GET' ? hits[p] : counts[`${method} ${p}`];
    if (p.startsWith('/api/images/')) return route.fulfill({ status: 404, body: '' });
    if (p === '/api/info') return json(route, { id: 'x', name: 'e2e', version: 'dev', authRequired: false });
    if (p === '/api/scan') return json(route, { running: false, itemCount: ITEMS.length, watching: [] });
    if (p === '/api/items') return routeItems(route, ITEMS);
    if (p === '/api/shows') return json(route, SHOWS);
    if (['/api/continue', '/api/upcoming', '/api/genres', '/api/folders', '/api/metadata/review'].includes(p)) return json(route, []);
    if (p === '/api/metadata/status') return json(route, { enabled: true, keySet: true, running: false, pending: 0, unmatched: 0, needsReview: 0, lastError: '' });
    if (p === '/api/jobs') return json(route, { paused: false, running: [], queued: [], history: JOBS, tier: { playback: {}, browsing: false, hold: { held: false } } });
    if (p === '/api/sessions') return json(route, []);
    if (p === '/api/events') return route.fulfill({ status: 503, body: '' });
    if (p === '/api/settings' && method === 'GET') {
      const then = stored; // as it is when asked
      const wait = opts.getDelay?.(n) ?? 0;
      if (wait) await sleep(wait);
      return json(route, then);
    }
    if (p === '/api/settings') {
      if (opts.hang?.(body, n)) return new Promise(() => {}); // the server never answers
      const wait = opts.putDelay?.(body, n) ?? 0;
      if (wait) await sleep(wait);
      const [st, out] = opts.put ? opts.put(body, n) : [200, store(body)];
      if (st === 200 && opts.put) store(body);
      return json(route, out, st);
    }
    if (p === '/api/settings/test') {
      // Only a test of MDBList's key gets MDBList's answer.
      const out = body?.provider === 'mdblist' ? (opts.test ? opts.test(n) : { ok: true }) : { ok: false, error: `asked about ${body?.provider}` };
      return json(route, out);
    }
    if (p === '/api/items/1') {
      return json(route, {
        ...ITEMS[0], path: '/m/Heat.mkv', size: 1, bitrate: 1, streams: [], subtitles: [],
        playback: { method: 'direct', videoAction: 'copy', audioAction: 'copy', audioIndex: 1, reasons: [], url: '', startSec: 0 },
        meta: {
          id: 101, kind: 'movie', title: 'Heat', year: 1995, itemId: 1, sortTitle: 'Heat', genres: [], cast: [], crew: [], source: 'tmdb', locked: false,
          matchState: 'matched', needsReview: false, rating: ITEMS[0].rating, ratings: opts.ratings ?? ALL(),
        },
      });
    }
    if (p === '/api/shows/Lost') {
      return json(route, {
        show: 'Lost', episodeCount: 2, seasons: [{ season: 1, name: 'Season 1', episodeCount: 2 }], rating: SHOWS[2].rating,
        meta: { id: 77, kind: 'series', title: 'Lost', year: 2004, sortTitle: 'Lost', genres: [], cast: [], crew: [], source: 'tmdb', locked: false, matchState: 'matched', needsReview: false, rating: SHOWS[2].rating, ratings: [entry('imdb', 9.0, 10, { votes: 600_000 })] },
      });
    }
    return json(route, { error: `unmocked ${p}` }, 404);
  });
  return { context, page, calls, hits, errors, server: () => stored };
}

const chips = (page) => page.locator('ul.ratings .score-chip');
// What a chip shows (its visible parts; the spoken label is separate).
const face = (loc) =>
  loc.evaluate((el) =>
    [...el.children]
      .filter((c) => c.getAttribute('aria-hidden') === 'true' && c.tagName.toLowerCase() !== 'svg')
      .map((c) => c.textContent.trim())
      .join(' '),
  );
const settingsCall = (calls) => calls.filter((c) => (c.method === 'PUT' || c.method === 'PATCH') && c.path === '/api/settings');
const card = (page) => page.locator('.ratings-card');
/** The MDBList key's card, in Settings → API keys. */
const keyCard = (page) => page.locator('.card.mdblist');
const cardTitles = (page) => page.locator('.grid .card .title').allTextContents();

const scenarios = {
  // Mutants caught: the chips in the server's order whatever ratingsShown says (orderShown
  // returning the list as it came), a source the settings hide still shown, the wrong format of a
  // source (IMDb's votes, a percentage), the link or its target, and the tooltip's votes and age.
  async 'Chips: the order of the settings, each source as it shows itself, linked'(browser) {
    const shown = ['metacritic', 'imdb', 'rt_critics', 'rt_audience', 'letterboxd', 'tmdb']; // Trakt hidden
    const { context, page, errors } = await open(browser, { settings: { ratingsShown: shown } });
    await page.goto(`${base}/#/item/1`);
    await chips(page).nth(5).waitFor();
    const order = await chips(page).evaluateAll((els) => els.map((e) => e.dataset.source));
    check('the chips follow ratingsShown, not the server', JSON.stringify(order) === JSON.stringify(shown), JSON.stringify(order));
    check('a hidden source has no chip', (await page.locator('ul.ratings [data-source="trakt"]').count()) === 0);
    const want = {
      imdb: 'IMDb 8.7 1.2M',
      rt_critics: 'Critics 93%',
      rt_audience: 'Audience 41%',
      metacritic: 'Metacritic 74',
      letterboxd: 'Letterboxd 4.1',
      tmdb: 'TMDB 7.8',
    };
    for (const [src, w] of Object.entries(want)) {
      const got = await face(page.locator(`ul.ratings [data-source="${src}"]`));
      check(`${src} reads "${w}"`, got === w, got);
    }
    check('the tomato is fresh', (await page.locator('[data-source="rt_critics"] svg').getAttribute('data-glyph')) === 'fresh');
    check('the popcorn is spilled', (await page.locator('[data-source="rt_audience"] svg').getAttribute('data-glyph')) === 'spilled');
    check('Letterboxd has its star', (await page.locator('[data-source="letterboxd"] svg').getAttribute('data-glyph')) === 'star');
    const spoken = await text(page.locator('[data-source="rt_critics"] .sr-only'));
    check('each chip names its source in words', spoken === 'Rotten Tomatoes critics 93%, fresh (opens in a new tab)', spoken);
    // Mutant caught: "out of 10" between IMDb's number and its votes (the name no longer holds
    // what the chip shows, in order: WCAG 2.5.3).
    const imdbSpoken = await text(page.locator('[data-source="imdb"] .sr-only'));
    check("IMDb's name holds what it shows, in order", imdbSpoken === 'IMDb 8.7, 1.2M votes (opens in a new tab)', imdbSpoken);
    const imdb = page.locator('ul.ratings [data-source="imdb"]');
    check('IMDb links to the title there', (await imdb.getAttribute('href')) === IMDB_URL);
    check('in a new tab, with no referrer', (await imdb.getAttribute('target')) === '_blank' && (await imdb.getAttribute('rel')) === 'noreferrer');
    const tip = await imdb.getAttribute('title');
    check('the tooltip has the votes in full and the age', tip === 'IMDb, 1,234,567 votes. Rated 3 days ago.', tip);
    const tmdb = page.locator('ul.ratings [data-source="tmdb"]');
    check('a rating with no page is not a link', (await tmdb.evaluate((e) => e.tagName)) === 'SPAN' && (await tmdb.getAttribute('href')) === null);
    // Positive control for "no chip": the film does have a Trakt rating, shown once the settings show it.
    await context.close();
    const again = await open(browser);
    await again.page.goto(`${base}/#/item/1`);
    await chips(again.page).nth(6).waitFor();
    check('with every source shown, Trakt is there too', (await face(again.page.locator('ul.ratings [data-source="trakt"]'))) === 'Trakt 84%');
    check('no page errors', !errors.length && !again.errors.length, [...errors, ...again.errors].join('; '));
    await again.context.close();
  },

  // Mutant caught: the band thresholds off by one (61 yellow, or 40 red, or 39 yellow, or 60 green).
  async "Chips: Metacritic's box is coloured by its band"(browser) {
    for (const [score, band] of [[61, 'good'], [60, 'mixed'], [40, 'mixed'], [39, 'bad']]) {
      const { context, page } = await open(browser, { ratings: ALL(score) });
      await page.goto(`${base}/#/item/1`);
      const box = page.locator('[data-source="metacritic"] .score-box');
      await box.waitFor();
      const got = await box.getAttribute('data-band');
      check(`a Metascore of ${score} is ${band}`, got === band, got);
      check(`${score}: the number is in text`, (await text(box)) === String(score));
      await context.close();
    }
  },

  // Mutants caught: cards without the primary rating (MediaCard not given it), the rating shown as
  // an object or not at all, and a card with no rating showing one anyway.
  async 'Cards: the primary rating, quietly'(browser) {
    const { context, page } = await open(browser);
    await page.goto(`${base}/#/movies`);
    await page.locator('.grid .card').nth(4).waitFor();
    const heat = page.locator('.grid .card', { hasText: 'Heat' });
    const r = heat.locator('.sub [data-source]');
    check('Heat shows its IMDb rating', (await face(r)) === '8.7', await face(r));
    check('Heat has an IMDb glyph', (await r.locator('svg.glyph[data-glyph="imdb"]').count()) === 1);
    check('quiet: no frame, no link inside the card', (await r.evaluate((e) => e.tagName)) === 'SPAN' && (await r.evaluate((e) => e.classList.contains('compact'))));
    const alien = await face(page.locator('.grid .card', { hasText: 'Alien' }).locator('.sub [data-source]'));
    check('Alien shows its Tomatometer', alien === '65%', alien);
    const spoken = await text(page.locator('.grid .card', { hasText: 'Alien' }).locator('.sub .sr-only'));
    check('and names it in words', spoken === 'Rotten Tomatoes critics 65%, fresh.', spoken);
    check('Dune, with no rating, shows none', (await page.locator('.grid .card', { hasText: 'Dune' }).locator('[data-source]').count()) === 0);
    await context.close();
  },

  // Mutants caught: the sort by each source's own value instead of score100 (Alien's 65 would beat
  // Heat's 8.7), a title with no rating not last, the minimum filter by value, a title with no
  // rating kept by a minimum, and the view not kept in the URL.
  async 'Movies and Shows: sort by rating and a minimum rating'(browser) {
    const { context, page } = await open(browser);
    await page.goto(`${base}/#/movies`);
    await page.locator('.grid .card').nth(4).waitFor();
    await page.locator('[role="radiogroup"][aria-label="Sort by"] a', { hasText: 'Rating' }).click();
    await page.waitForFunction(() => location.hash.includes('sort=rating'));
    await sleep(100);
    let t = await cardTitles(page);
    check('movies by rating: score100, best first, none last', JSON.stringify(t) === JSON.stringify(['Brazil', 'Heat', 'Cube', 'Alien', 'Dune']), JSON.stringify(t));
    await page.locator('[role="radiogroup"][aria-label="Minimum rating"] a', { hasText: '70+' }).click();
    await page.waitForFunction(() => location.hash.includes('min=70'));
    await sleep(100);
    t = await cardTitles(page);
    check('70+: only what reaches 70, no unrated', JSON.stringify(t) === JSON.stringify(['Brazil', 'Heat', 'Cube']), JSON.stringify(t));
    check('the URL keeps both', page.url().endsWith('#/movies?sort=rating&min=70'), page.url());
    check('70+ is the chosen one', (await page.locator('[aria-label="Minimum rating"] [aria-checked="true"]').allTextContents()).join() === '70+');
    await page.reload();
    await page.locator('.grid .card').nth(2).waitFor();
    t = await cardTitles(page);
    check('a reload keeps the view', JSON.stringify(t) === JSON.stringify(['Brazil', 'Heat', 'Cube']), JSON.stringify(t));
    await page.goto(`${base}/#/movies?min=80`);
    await sleep(200);
    t = await cardTitles(page);
    check('80+ in the A to Z order', JSON.stringify(t) === JSON.stringify(['Brazil', 'Cube', 'Heat']), JSON.stringify(t));

    await page.goto(`${base}/#/shows?sort=rating`);
    await page.locator('.grid .card').nth(2).waitFor();
    t = await cardTitles(page);
    check('shows by rating: Lost (90) before Dark (85%), none last', JSON.stringify(t) === JSON.stringify(['Lost', 'Dark', 'Fringe']), JSON.stringify(t));
    await page.goto(`${base}/#/shows?sort=rating&min=88`);
    await sleep(200);
    t = await cardTitles(page);
    check('shows, a minimum of 88: Lost only', JSON.stringify(t) === JSON.stringify(['Lost']), JSON.stringify(t));
    await page.goto(`${base}/#/shows?min=95`);
    await sleep(200);
    check('none left: said, with a way back', (await text(page.locator('.page p.muted'))).startsWith('No shows match these filters.'));
    await context.close();
  },

  // Mutants caught: the episode list without each episode's rating, and an episode with none
  // showing its show's.
  async "Episodes: each one's IMDb rating"(browser) {
    const { context, page } = await open(browser);
    await page.goto(`${base}/#/shows/Lost`);
    const eps = page.locator('ol.episodes li');
    await eps.nth(1).waitFor();
    const first = eps.nth(0).locator('[data-source="imdb"]');
    check('episode 1 shows IMDb 8.9', (await face(first)) === '8.9', await face(first));
    check('episode 1 has an IMDb glyph', (await first.locator('svg.glyph[data-glyph="imdb"]').count()) === 1);
    check('episode 2, with none, shows none', (await eps.nth(1).locator('[data-source]').count()) === 0);
    check("the show's chips are on the page too", (await face(page.locator('ul.ratings [data-source="imdb"]'))) === 'IMDb 9.0 600K');
    await context.close();
  },

  // Mutants caught: the key sent under another name, the server's refusal replaced by a generic
  // message, Remove offered for a key from the environment, and Test asking about another provider
  // (the mock gives MDBList's answer only to provider "mdblist").
  // Settings in tabs: the MDBList key has its own card in API keys.
  async 'Settings: the MDBList key, a refusal inline, Remove and Test'(browser) {
    let { context, page, calls } = await open(browser, {
      put: (body, n) => (n === 1 ? [400, { error: 'MDBList rejected the key' }] : [200, settings({ mdblistKeySet: true, mdblistKeySource: 'settings', mdblistKeyChecked: true, ratingsAvailable: SOURCES })]),
    });
    await page.goto(`${base}/#/settings?tab=keys`);
    await keyCard(page).waitFor();
    const input = page.locator('#mdblist-key');
    check('the key field is a password field', (await input.getAttribute('type')) === 'password');
    check('password managers are told to keep out', (await input.getAttribute('autocomplete')) === 'new-password' && (await input.getAttribute('data-1p-ignore')) !== null && (await input.getAttribute('data-lpignore')) === 'true' && (await input.getAttribute('data-bwignore')) !== null);
    await input.fill('fake-refused-key');
    await keyCard(page).locator('button:has-text("Save key")').click();
    const msg = keyCard(page).locator('p.key-message');
    await msg.waitFor();
    check('the refusal is said as the server said it', (await text(msg)) === 'MDBList rejected the key', await text(msg));
    check('it is an error, inline', await msg.evaluate((e) => e.classList.contains('error') && e.getAttribute('role') === 'status'));
    check('the key went as mdblistKey, and nothing else', JSON.stringify(settingsCall(calls)[0]?.body) === JSON.stringify({ mdblistKey: 'fake-refused-key' }), JSON.stringify(settingsCall(calls)[0]?.body));
    check('the refused key stays to be fixed', (await input.inputValue()) === 'fake-refused-key');
    await input.fill('fake-good-key');
    await keyCard(page).locator('button:has-text("Save key")').click();
    await keyCard(page).locator('p.key-message.ok').waitFor();
    check('a good key: saved, and said so', /^Key saved/.test(await text(keyCard(page).locator('p.key-message.ok'))));
    check('the field is emptied', (await input.inputValue()) === '');
    await context.close();

    ({ context, page, calls } = await open(browser, { settings: { mdblistKeySet: true, mdblistKeySource: 'settings' } }));
    await page.goto(`${base}/#/settings?tab=keys`);
    await keyCard(page).locator('button:has-text("Remove key")').click();
    await keyCard(page).locator('.confirm-remove button:has-text("Remove")').click(); // Remove asks first
    await keyCard(page).locator('p.key-message.ok').waitFor();
    check('Remove sends mdblistKey ""', JSON.stringify(settingsCall(calls).at(-1)?.body) === JSON.stringify({ mdblistKey: '' }));
    await context.close();

    ({ context, page } = await open(browser, { settings: { mdblistKeySet: true, mdblistKeySource: 'env' } }));
    await page.goto(`${base}/#/settings?tab=keys`);
    await keyCard(page).waitFor();
    check('a key from the environment: no Remove, no field', (await keyCard(page).locator('button:has-text("Remove")').count()) === 0 && (await page.locator('#mdblist-key').count()) === 0);
    check('and it says where it comes from', (await text(keyCard(page))).includes('A key is set, from the PLY_MDBLIST_KEY environment variable. Change it there.'));
    await context.close();

    for (const [name, answer, want] of [
      ['ok', { ok: true }, 'The MDBList key works.'],
      ['fail', { ok: false, error: 'MDBList rejected the key' }, 'MDBList rejected the key'],
    ]) {
      ({ context, page, calls } = await open(browser, { settings: { mdblistKeySet: true, mdblistKeySource: 'settings' }, test: () => answer }));
      await page.goto(`${base}/#/settings?tab=keys`);
      await keyCard(page).locator('button[aria-label="Test the MDBList key"]').click();
      const result = keyCard(page).locator('.test p.result');
      await page.waitForFunction(() => document.querySelector('.card.mdblist .test p.result')?.textContent?.trim());
      check(`Test ${name}: "${want}"`, (await text(result)) === want, await text(result));
      check(`Test ${name}: asked about MDBList`, JSON.stringify(calls.find((c) => c.path === '/api/settings/test')?.body) === JSON.stringify({ provider: 'mdblist' }));
      await context.close();
    }
  },

  // Mutants caught: the primary choice not sent (or sent as anything but ratingsPrimary), the
  // shown sources sent in the wrong order or not at all, a refusal leaving the page showing what
  // the server did not take, and the focus lost after a move.
  async 'Settings: the primary rating and the shown sources, saved as they change'(browser) {
    let { context, page, calls } = await open(browser, { settings: { mdblistKeySet: true, mdblistKeySource: 'settings', ratingsAvailable: SOURCES } });
    await page.goto(`${base}/#/settings?tab=ratings`);
    await card(page).waitFor();
    const opts = await page.locator('#ratings-primary option').evaluateAll((os) => os.map((o) => o.value));
    // Mutant caught: the hint described by the list, not the group of checkboxes it is about.
    check('the hint describes the group', (await card(page).locator('fieldset.shown').getAttribute('aria-describedby')) === 'ratings-shown-hint' && (await card(page).locator('fieldset.shown ul[aria-describedby]').count()) === 0);
    check('the primary choices are the sources this install has', JSON.stringify(opts) === JSON.stringify(['imdb', 'rt_critics', 'metacritic', 'letterboxd', 'trakt', 'tmdb']), JSON.stringify(opts));
    await page.selectOption('#ratings-primary', 'metacritic');
    const said = card(page).locator('p.choice-message');
    await page.waitForFunction(() => document.querySelector('.ratings-card p.choice-message')?.textContent?.trim());
    check('primary: sent as ratingsPrimary', JSON.stringify(settingsCall(calls).at(-1)?.body) === JSON.stringify({ ratingsPrimary: 'metacritic' }), JSON.stringify(settingsCall(calls).at(-1)));
    check('primary: said', (await text(said)) === 'Cards show the Metacritic rating.', await text(said));

    await page.locator('#rs-up-metacritic').click();
    await page.waitForFunction(() => document.querySelector('.ratings-card p.choice-message')?.textContent?.includes('moved'));
    const moved = settingsCall(calls).at(-1)?.body;
    check('Move up: the new order is sent', JSON.stringify(moved) === JSON.stringify({ ratingsShown: ['imdb', 'rt_critics', 'metacritic', 'rt_audience', 'letterboxd', 'trakt', 'tmdb'] }), JSON.stringify(moved));
    const rows = await card(page).locator('.shown li label').allTextContents();
    check('Move up: the list shows it', rows[2]?.trim().startsWith('Metacritic'), rows.join('|'));
    check('Move up: the focus stays on the button', await page.evaluate(() => document.activeElement?.id === 'rs-up-metacritic'), await page.evaluate(() => document.activeElement?.id));
    await page.locator('#rs-down-imdb').click();
    await page.waitForFunction(() => document.querySelector('.ratings-card p.choice-message')?.textContent?.includes('IMDb moved down'));
    check('Move down: sent', JSON.stringify(settingsCall(calls).at(-1)?.body?.ratingsShown?.slice(0, 2)) === JSON.stringify(['rt_critics', 'imdb']));
    check('the first one cannot move up', await page.locator('#rs-up-rt_critics').isDisabled());

    await page.locator('#rs-trakt').uncheck();
    await page.waitForFunction(() => document.querySelector('.ratings-card p.choice-message')?.textContent?.includes('hidden'));
    const hidden = settingsCall(calls).at(-1)?.body?.ratingsShown ?? [];
    check('unticking Trakt: sent without it', !hidden.includes('trakt') && hidden.length === 6, JSON.stringify(hidden));
    const last = await card(page).locator('.shown li').last().locator('label').textContent();
    check('an unticked source goes to the end', last?.trim() === 'Trakt', last);
    await context.close();

    // A refusal (400) is said inline, and the page goes back to what the server has.
    ({ context, page, calls } = await open(browser, { put: () => [400, { error: 'ratingsPrimary: "letterboxd" is not available' }], settings: { ratingsAvailable: ['imdb', 'letterboxd', 'tmdb'] } }));
    await page.goto(`${base}/#/settings?tab=ratings`);
    await card(page).waitFor();
    await page.selectOption('#ratings-primary', 'letterboxd');
    await page.waitForFunction(() => document.querySelector('.ratings-card p.choice-message.error'));
    check('a refused choice: the server\'s words, inline', (await text(card(page).locator('p.choice-message.error'))) === 'ratingsPrimary: "letterboxd" is not available');
    check('and the select is back to what is stored', (await page.inputValue('#ratings-primary')) === 'imdb');
    await context.close();
  },

  // Mutants caught: the notices reworded or without their links, and the dataset's date missing
  // (or "not downloaded yet" when there is one).
  async 'Settings: the notices of IMDb and MDBList, and the dataset date'(browser) {
    let { context, page } = await open(browser);
    await page.goto(`${base}/#/settings?tab=ratings`);
    await card(page).waitFor();
    check('the IMDb notice, word for word', (await text(card(page).locator('.imdb-notice'))) === IMDB_NOTICE, await text(card(page).locator('.imdb-notice')));
    check('with its link', (await card(page).locator('.imdb-notice a').getAttribute('href')) === 'https://www.imdb.com');
    check('the dataset date', (await text(card(page).locator('.dataset'))) === 'IMDb ratings updated 3 November 2026.', await text(card(page).locator('.dataset')));
    check('without an MDBList key, its sources say why they are not there', (await text(card(page).locator('li', { hasText: 'Metacritic' }))).includes('(needs an MDBList key)'));
    // MDBList's notice is with its key, in API keys.
    await page.goto(`${base}/#/settings?tab=keys`);
    await keyCard(page).waitFor();
    const mdb = await text(keyCard(page).locator('.mdblist-notice'));
    check('the MDBList notice, word for word, then its link', mdb === `${MDBLIST_NOTICE} mdblist.com`, mdb);
    check('linked to MDBList', (await keyCard(page).locator('.mdblist-notice a').getAttribute('href')) === 'https://mdblist.com');
    await context.close();
    ({ context, page } = await open(browser, { settings: { imdbDatasetUpdatedAt: null } }));
    await page.goto(`${base}/#/settings?tab=ratings`);
    await card(page).waitFor();
    check('no dataset yet: said', (await text(card(page).locator('.dataset'))) === 'IMDb ratings not downloaded yet.');
    await context.close();
  },

  // Mutant caught: a certified Tomatometer drawn as a plain fresh tomato.
  async 'Chips: a certified Tomatometer has its own mark'(browser) {
    const { context, page } = await open(browser, { ratings: [entry('rt_critics', 97, 100, { state: 'certified', url: RT_URL })] });
    await page.goto(`${base}/#/item/1`);
    const svg = page.locator('[data-source="rt_critics"] svg');
    await svg.waitFor();
    check('certified: its own glyph', (await svg.getAttribute('data-glyph')) === 'certified');
    check('certified: a ring the fresh tomato has not', (await svg.locator('circle.ring').count()) === 1);
    check('certified: said in words', (await text(page.locator('[data-source="rt_critics"] .sr-only'))).startsWith('Rotten Tomatoes critics 97%, certified fresh'));
    await context.close();
  },

  // Mutant caught: a global class of app.css named like a component's own (.panel): the search
  // palette got a padding, a border and a background, and Activity's lists a grid with a gap.
  async "Global classes: the palette and Activity keep their own .panel"(browser) {
    const { context, page } = await open(browser);
    await page.goto(`${base}/#/`);
    await page.locator('h1, .rail, main').first().waitFor();
    await sleep(300);
    await page.keyboard.press('/');
    const palette = page.locator('dialog .panel');
    await palette.waitFor();
    const pal = await palette.evaluate((e) => {
      const c = getComputedStyle(e);
      return { padding: c.paddingTop, border: c.borderTopWidth, bg: c.backgroundColor };
    });
    check('the palette: no padding, border or background of its own', pal.padding === '0px' && pal.border === '0px' && pal.bg === 'rgba(0, 0, 0, 0)', JSON.stringify(pal));
    await page.keyboard.press('Escape');
    await page.goto(`${base}/#/activity`);
    const list = page.locator('ul.panel.history');
    await list.waitFor();
    const act = await list.evaluate((e) => {
      const c = getComputedStyle(e);
      return { display: c.display, gap: c.rowGap, padding: c.paddingTop };
    });
    check("Activity's list: a list, not a grid with a gap", act.display === 'block' && act.gap === 'normal' && act.padding === '0px', JSON.stringify(act));
    // Positive control: the settings card has the look the shared class gives.
    await page.goto(`${base}/#/settings?tab=ratings`);
    const cardPad = await card(page).evaluate((e) => getComputedStyle(e).display);
    check('the Ratings card is still a raised grid', cardPad === 'grid');
    await context.close();
  },

  // Mutants caught: saves that cross (each sent at once, so a late answer to the first is what the
  // server keeps, or what the page takes); the cards' refresh skipped when a later choice is on its
  // way; and a GET of the settings that started before a save putting its old choice back.
  async 'Settings: choices made quickly are saved in order, and stay'(browser) {
    // 1. The first save is answered late; the second choice is made meanwhile.
    let { context, page, calls, server } = await open(browser, { putDelay: (_b, n) => (n === 1 ? 700 : 0) });
    await page.goto(`${base}/#/settings?tab=ratings`);
    await card(page).waitFor();
    await page.locator('#rs-trakt').uncheck();
    await page.locator('#rs-tmdb').uncheck();
    await page.waitForFunction(() => document.querySelector('.ratings-card p.choice-message')?.textContent?.includes('TMDB is hidden'));
    await sleep(1200); // past the late answer to the first save, and anything sent after it
    const ui = await page.locator('.shown li input:checked').evaluateAll((els) => els.map((e) => e.id.slice(3)));
    const kept = server().ratingsShown;
    check('crossing: what the server keeps is what the page shows', JSON.stringify(kept) === JSON.stringify(ui), `server ${JSON.stringify(kept)} page ${JSON.stringify(ui)}`);
    check('crossing: neither Trakt nor TMDB', !kept.includes('trakt') && !kept.includes('tmdb'), JSON.stringify(kept));
    check('crossing: one save at a time, the latest last', settingsCall(calls).length === 2, String(settingsCall(calls).length));
    await context.close();

    // 2. The primary is changed, and a box ticked before its answer: the cards still refresh.
    let hits;
    ({ context, page, calls, hits, server } = await open(browser, { putDelay: (_b, n) => (n === 1 ? 700 : 0) }));
    await page.goto(`${base}/#/settings?tab=ratings`);
    await card(page).waitFor();
    await sleep(300);
    // A confirmed primary bumps the library version: every page asks again (here, the settings).
    const before = hits['/api/settings'] ?? 0;
    await page.selectOption('#ratings-primary', 'tmdb');
    await page.locator('#rs-trakt').uncheck();
    await page.waitForFunction(() => document.querySelector('.ratings-card p.choice-message')?.textContent?.includes('Trakt is hidden'));
    await sleep(400);
    check('primary then a box: the server has both', server().ratingsPrimary === 'tmdb' && !server().ratingsShown.includes('trakt'), JSON.stringify(server().ratingsShown));
    check('primary then a box: the pages ask again (the cards refresh)', (hits['/api/settings'] ?? 0) > before, `${before} -> ${hits['/api/settings']}`);
    check('primary then a box: the select keeps TMDB', (await page.inputValue('#ratings-primary')) === 'tmdb');
    await context.close();

    // 3. A GET of the settings asked before a save, answered after it, does not undo the save.
    ({ context, page, server } = await open(browser, { getDelay: (n) => (n >= 2 ? 900 : 0) }));
    await page.goto(`${base}/#/settings?tab=ratings`);
    await card(page).waitFor();
    const asked = page.waitForRequest((r) => r.url().endsWith('/api/settings') && r.method() === 'GET');
    await page.selectOption('#ratings-primary', 'tmdb'); // confirmed: the library and the settings are asked for again
    await asked;
    await page.locator('#rs-trakt').uncheck(); // saved before that GET is answered
    await sleep(1300);
    check('a late GET: Trakt stays unticked', !(await page.locator('#rs-trakt').isChecked()));
    check('a late GET: the server agrees', !server().ratingsShown.includes('trakt'));
    check('a late GET: the select keeps TMDB', (await page.inputValue('#ratings-primary')) === 'tmdb');
    await context.close();
  },

  // Mutants caught: no focus after a move (the focus falls to the page while the save is on its
  // way), the twin not focused when the pressed button ends up disabled at the top or bottom
  // (atEnd always false), and the focus moved only after the answer.
  async 'Settings: the focus after a move'(browser) {
    const { context, page } = await open(browser, { settings: { ratingsAvailable: SOURCES }, putDelay: () => 600 });
    await page.goto(`${base}/#/settings?tab=ratings`);
    await card(page).waitFor();
    const focused = () => page.evaluate(() => document.activeElement?.id || document.activeElement?.tagName);
    const settled = (words) => page.waitForFunction((w) => document.querySelector('.ratings-card p.choice-message')?.textContent?.includes(w), words);
    await page.locator('#rs-up-rt_critics').click();
    await sleep(100); // the save is still on its way
    check('to the top: the focus is on its twin at once', (await focused()) === 'rs-down-rt_critics', await focused());
    await settled('Rotten Tomatoes critics moved up');
    check('to the top: still there after the answer', (await focused()) === 'rs-down-rt_critics', await focused());
    await page.locator('#rs-down-trakt').click();
    await sleep(100);
    check('to the bottom: the focus is on its twin at once', (await focused()) === 'rs-up-trakt', await focused());
    await settled('Trakt moved down');
    await page.locator('#rs-down-imdb').click();
    await sleep(100);
    check('a move down in the middle: the focus stays on the button', (await focused()) === 'rs-down-imdb', await focused());
    await settled('IMDb moved down');
    check('a move down in the middle: still there after the answer', (await focused()) === 'rs-down-imdb', await focused());
    await context.close();
  },

  // Mutant caught: `sending` cleared only after a save that worked (a refusal leaves the card
  // unable to save again).
  async 'Settings: after a refusal, the next choice is saved'(browser) {
    const { context, page, calls } = await open(browser, { put: (b, n) => (n === 1 ? [400, { error: 'nope' }] : [200, settings({ ratingsShown: b.ratingsShown ?? SOURCES })]) });
    await page.goto(`${base}/#/settings?tab=ratings`);
    await card(page).waitFor();
    await page.locator('#rs-trakt').uncheck();
    await page.waitForFunction(() => document.querySelector('.ratings-card p.choice-message.error'));
    await page.locator('#rs-tmdb').uncheck();
    await page.waitForFunction(() => document.querySelector('.ratings-card p.choice-message.ok'), null, { timeout: 5000 });
    check('a refusal, then a choice: two saves', settingsCall(calls).length === 2, String(settingsCall(calls).length));
    const second = settingsCall(calls)[1]?.body?.ratingsShown;
    check('the second has Trakt back (refused) and no TMDB', JSON.stringify(second) === JSON.stringify(['imdb', 'rt_critics', 'rt_audience', 'metacritic', 'letterboxd', 'trakt']), JSON.stringify(second));
    await context.close();
  },

  // Mutant caught: a refusal of an earlier choice reverting the page while a later one is pending
  // (the later one dropped without a word).
  async 'Settings: a refused choice with a later one pending sends the later one'(browser) {
    const { context, page, calls, server } = await open(browser, {
      putDelay: (_b, n) => (n === 1 ? 600 : 0),
      put: (b, n) => (n === 1 ? [400, { error: 'refused one' }] : [200, settings({ ratingsShown: b.ratingsShown })]),
    });
    await page.goto(`${base}/#/settings?tab=ratings`);
    await card(page).waitFor();
    await page.locator('#rs-trakt').uncheck();
    await page.locator('#rs-tmdb').uncheck(); // while the first is on its way
    await sleep(1500);
    const sc = settingsCall(calls);
    check('two saves', sc.length === 2, String(sc.length));
    check('the second carries both choices', JSON.stringify(sc[1]?.body?.ratingsShown) === JSON.stringify(['imdb', 'rt_critics', 'rt_audience', 'metacritic', 'letterboxd']), JSON.stringify(sc[1]?.body));
    const ui = await page.locator('.shown li input:checked').evaluateAll((els) => els.map((e) => e.id.slice(3)));
    check('the page shows what the server has', JSON.stringify(ui) === JSON.stringify(server().ratingsShown), `${JSON.stringify(ui)} vs ${JSON.stringify(server().ratingsShown)}`);
    await context.close();
  },

  // Mutant caught: no timeout on the save (a server that never answers holds every later choice).
  // Takes the 15 s of the timeout.
  async 'Settings: a save the server never answers is given up on'(browser) {
    const { context, page, calls, server } = await open(browser, { hang: (_b, n) => n === 1 });
    await page.goto(`${base}/#/settings?tab=ratings`);
    await card(page).waitFor();
    await page.locator('#rs-trakt').uncheck();
    await sleep(1000);
    check('while it waits: still unticked, nothing said yet', !(await page.locator('#rs-trakt').isChecked()) && (await text(card(page).locator('p.choice-message'))) === '');
    await page.waitForFunction(() => document.querySelector('.ratings-card p.choice-message.error'), null, { timeout: 20_000 });
    check('after the timeout: said', (await text(card(page).locator('p.choice-message.error'))) === 'The server didn’t answer.', await text(card(page).locator('p.choice-message.error')));
    check('after the timeout: put back', await page.locator('#rs-trakt').isChecked());
    await page.locator('#rs-tmdb').uncheck();
    await page.waitForFunction(() => document.querySelector('.ratings-card p.choice-message.ok'), null, { timeout: 5000 });
    check('the next choice is saved', settingsCall(calls).length === 2 && !server().ratingsShown.includes('tmdb'), JSON.stringify(server().ratingsShown));
    await context.close();
  },

  // Mutants caught: the whole card named "MDBList key" (it is the Ratings card), and the key's
  // card without a group of its own. Settings in tabs: the key's card is in API keys.
  async 'Settings: the card and its key part are named'(browser) {
    const { context, page } = await open(browser);
    await page.goto(`${base}/#/settings?tab=ratings`);
    await card(page).waitFor();
    check('the card is the group "Ratings"', (await page.getByRole('group', { name: 'Ratings', exact: true }).count()) === 1 && (await card(page).getAttribute('aria-label')) === 'Ratings');
    check('the key is not in it', (await card(page).locator('#mdblist-key').count()) === 0);
    await page.goto(`${base}/#/settings?tab=keys`);
    const key = page.getByRole('group', { name: 'MDBList key', exact: true });
    await key.waitFor();
    check('the key\'s card is the group "MDBList key"', (await key.count()) === 1 && (await key.locator('#mdblist-key').count()) === 1);
    check('and the choices are not in it', (await key.locator('#ratings-primary').count()) === 0);
    await context.close();
  },

  // Mutant caught: the page keeping what it asked for when the server kept something else.
  async "Settings: the page shows what the server kept"(browser) {
    const { context, page } = await open(browser, { put: (b) => [200, settings({ ratingsShown: ['imdb', 'metacritic'] })] });
    await page.goto(`${base}/#/settings?tab=ratings`);
    await card(page).waitFor();
    await page.locator('#rs-trakt').uncheck();
    await page.waitForFunction(() => document.querySelector('.ratings-card p.choice-message.ok'));
    const ui = await page.locator('.shown li input:checked').evaluateAll((els) => els.map((e) => e.id.slice(3)));
    check("the server's list, in its order", JSON.stringify(ui) === JSON.stringify(['imdb', 'metacritic']), JSON.stringify(ui));
    await context.close();
  },

  // Mutant caught: a refused change of the shown sources leaving the page as it was asked for, not
  // as the server has it.
  async 'Settings: a refused box is put back'(browser) {
    const { context, page } = await open(browser, { put: () => [500, { error: 'boom' }] });
    await page.goto(`${base}/#/settings?tab=ratings`);
    await card(page).waitFor();
    await page.locator('#rs-trakt').uncheck();
    await page.waitForFunction(() => document.querySelector('.ratings-card p.choice-message.error'));
    check('a refused untick: ticked again', await page.locator('#rs-trakt').isChecked());
    check("a refused untick: the server's words", (await text(card(page).locator('p.choice-message.error'))) === 'boom');
    await context.close();
  },

  // Not a check of behaviour: the chips, a card grid and the Settings card as a person sees them.
  async 'Screenshots'(browser) {
    if (!shots) return;
    for (const width of [1280, 360]) {
      let { context, page } = await open(browser, { width, settings: { mdblistKeySet: true, mdblistKeySource: 'settings', ratingsAvailable: SOURCES } });
      await page.goto(`${base}/#/item/1`);
      await chips(page).nth(6).waitFor();
      await sleep(300);
      await page.screenshot({ path: join(shots, `ratings-item-${width}.png`) });
      await page.locator('ul.ratings [data-source="imdb"]').focus();
      await page.locator('ul.ratings').screenshot({ path: join(shots, `ratings-chips-focus-${width}.png`) });
      check(`${width} px: the item page is no wider than the screen`, !(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth)));
      await page.goto(`${base}/#/movies?sort=rating`);
      await page.locator('.grid .card').nth(4).waitFor();
      await sleep(300);
      await page.screenshot({ path: join(shots, `ratings-movies-${width}.png`) });
      await page.goto(`${base}/#/shows/Lost`);
      await page.locator('ol.episodes li').nth(1).waitFor();
      await sleep(300);
      await page.locator('section.season').screenshot({ path: join(shots, `ratings-episodes-${width}.png`) });
      await page.goto(`${base}/#/settings?tab=ratings`);
      await card(page).waitFor();
      // The fixed bars would sit over the card in an element's screenshot.
      await page.addStyleTag({ content: 'header.bar, nav.tabs { visibility: hidden }' });
      await page.locator('#rs-up-metacritic').focus();
      await card(page).screenshot({ path: join(shots, `ratings-settings-${width}.png`) });
      check(`${width} px: settings no wider than the screen`, !(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth)));
      await context.close();
    }
  },
};

const browser = await chromium.launch({ channel: 'chrome', headless: true });
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
}
console.log(results.join('\n'));
const failed = results.filter((r) => r.startsWith('FAIL')).length;
console.log(`\n${results.length - failed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);
