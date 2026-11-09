// Back in a real browser, against a mocked API: the Back button of every page that isn't a
// top-level section, the view stack behind it (lib/router.svelte.ts), the scroll position and the
// sort and filters it returns to, the parents a deep link falls back to, and the browser's own Back
// and Forward staying in step with it. The unit tests pin the router alone; this pins the pages.
//
// Self-contained: serves web/dist (build it first: `npm run build`; PLY_E2E_DIST picks another
// directory) and answers every /api/ request with page.route. Needs Google Chrome.
//
//   npm run build && node e2e/navigation.mjs
//
// PLY_E2E_ONLY=<text> runs the scenarios whose name contains it. Each scenario names the mutants it
// was confirmed to catch (build the mutant into another directory with `vite build --outDir ...
// --emptyOutDir` and point PLY_E2E_DIST at it).
import { createServer } from 'node:http';
import { existsSync, readFileSync } from 'node:fs';
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

// 120 films, a third of them watched: a grid long enough to scroll far down, which Newest and
// Unwatched reorder and thin out (so a Back that dropped either lands on other cards).
const movie = (id) => ({
  id, kind: 'movie', title: `Film ${String(id).padStart(3, '0')}`, year: 1950 + ((id * 37) % 70), durationSec: 6000 + id, container: 'mkv',
  videoCodec: 'h264', audioCodec: 'aac', width: 1920, height: 1080, addedAt: id, art: null, titleId: 1000 + id, matchState: 'matched',
  needsReview: false, rating: null, watched: id % 3 === 0, progress: id % 3 === 0 ? { positionSec: 0, watched: true } : null,
});
const MOVIES = Array.from({ length: 120 }, (_, i) => movie(i + 1));
const episode = (id, ep) => ({
  id, kind: 'episode', title: 'Lost', show: 'Lost', season: 1, episode: ep, episodeTitle: `Episode ${ep}`, durationSec: 2800, container: 'mkv',
  videoCodec: 'h264', audioCodec: 'aac', width: 1920, height: 1080, addedAt: id, progress: null, art: null, titleId: 77, matchState: 'matched', needsReview: false,
});
const ITEMS = [...MOVIES, episode(2001, 1), episode(2002, 2)];
const SHOWS = [{ show: 'Lost', seasons: [1], episodeCount: 2, matchState: 'matched', needsReview: false, rating: null }];
const LOST = {
  show: 'Lost', episodeCount: 2, year: 2004, seasons: [{ season: 1, name: 'Season 1', episodeCount: 2 }],
  meta: {
    id: 77, kind: 'series', title: 'Lost', year: 2004, sortTitle: 'Lost', genres: [], cast: [{ personId: 31, name: 'Al Pacino', character: 'Jack' }], crew: [],
    source: 'tmdb', locked: false, matchState: 'matched', needsReview: false, ratings: [],
  },
};
const PERSON = {
  id: 31, name: 'Al Pacino', knownFor: 'Acting',
  credits: [{ titleId: 77, kind: 'cast', character: 'Jack', title: { id: 77, kind: 'series', title: 'Lost', year: 2004, show: 'Lost' } }],
};
// The genre's titles come from the server on every visit (resource, no cache), and late: Back to
// it finds a skeleton first, the page a scroll restore must wait for.
const GENRE_TITLES = MOVIES.slice(0, 80).map((m) => ({ id: m.titleId, kind: 'movie', title: m.title, year: m.year, itemId: m.id, rating: null }));
const TITLES_DELAY = 700;
const detail = (it) => ({
  ...it, path: `/m/${it.title}.mkv`, size: 1, bitrate: 1, streams: [], subtitles: [],
  playback: { method: 'direct', videoAction: 'copy', audioAction: 'copy', audioIndex: 1, reasons: [], url: '', startSec: 0 },
});
const settings = {
  tmdbKeySet: true, tmdbKeySource: 'settings', metadataEnabled: true, metadataForcedOff: false, language: 'en-US',
  maxStreams: 4, maxTranscodes: 2, maxStreamsSource: 'default', maxTranscodesSource: 'default',
  tvdbKeySet: false, tvdbPinSet: false, tvdbKeySource: '', tvdbAttribution: 'x', tvdbAttributionUrl: 'https://thetvdb.com',
  mdblistKeySet: false, mdblistKeySource: '', ratingsPrimary: 'imdb', ratingsShown: ['imdb', 'tmdb'], ratingsAvailable: ['imdb', 'tmdb'],
  imdbAttribution: 'x', imdbAttributionUrl: 'https://www.imdb.com', mdblistAttribution: 'x', mdblistAttributionUrl: 'https://mdblist.com',
};

/**
 * A fresh page on the mocked server (about:blank first: a deep link has an entry before it).
 *   slowItems  ms before the library and the items' details are answered
 *   shrink     titles the genre has lost by its second fetch, from the top (the library changed)
 *   many       a library of this many films instead (no episodes)
 */
async function open(browser, opts = {}) {
  const items = opts.many ? Array.from({ length: opts.many }, (_, i) => movie(i + 1)) : ITEMS;
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 }, serviceWorkers: 'block' });
  const page = await context.newPage();
  const errors = [];
  let titleFetches = 0;
  page.on('pageerror', (e) => errors.push(e.message));
  const json = (route, body, st = 200) => route.fulfill({ status: st, contentType: 'application/json', body: JSON.stringify(body) });
  await page.route('**/api/**', async (route) => {
    const req = route.request();
    const u = new URL(req.url());
    const p = u.pathname;
    if (p.startsWith('/api/images/')) return route.fulfill({ status: 404, body: '' });
    if (p === '/api/info') return json(route, { id: 'x', name: 'e2e', version: 'dev', authRequired: false });
    if (p === '/api/scan') return json(route, { running: false, itemCount: items.length, watching: [] });
    if (opts.slowItems && p.startsWith('/api/items')) await sleep(opts.slowItems);
    if (p === '/api/items') return routeItems(route, items);
    if (p === '/api/shows') return json(route, SHOWS);
    if (['/api/continue', '/api/upcoming', '/api/folders', '/api/metadata/review', '/api/sessions'].includes(p)) return json(route, []);
    if (p === '/api/genres') return json(route, [{ id: 18, name: 'Drama', count: GENRE_TITLES.length }]);
    if (p === '/api/titles') {
      const n = (titleFetches += 1);
      await sleep(TITLES_DELAY);
      const titles = n > 1 && opts.shrink ? GENRE_TITLES.slice(opts.shrink) : GENRE_TITLES;
      return json(route, u.searchParams.get('genre') === '18' ? titles : []);
    }
    if (p === '/api/metadata/status') return json(route, { enabled: true, keySet: true, running: false, pending: 0, unmatched: 0, needsReview: 0, lastError: '' });
    if (p === '/api/jobs') return json(route, { paused: false, running: [], queued: [], history: [], tier: { playback: {}, browsing: false, hold: { held: false } } });
    if (p === '/api/events') return route.fulfill({ status: 503, body: '' });
    if (p === '/api/settings') return json(route, settings);
    if (p === '/api/shows/Lost') return json(route, LOST);
    if (p === '/api/people/31') return json(route, PERSON);
    if (p === '/api/collections/9') return json(route, { id: 9, name: 'The Mann Collection', titles: [] });
    const m = p.match(/^\/api\/items\/(\d+)$/);
    const it = m && items.find((i) => i.id === Number(m[1]));
    if (it) return json(route, detail(it));
    return json(route, { error: `unmocked ${p}` }, 404);
  });
  await page.goto('about:blank');
  return { context, page, errors };
}

const back = (page) => page.locator('a.back');
/** A load of the page itself (a bookmark, a new tab): page.goto to another hash alone stays in the document. */
async function fresh(page, h) {
  await page.goto('about:blank');
  await page.goto(`${base}/${h}`);
}
const hash = (page) => page.evaluate(() => location.hash);
const scrollY = (page) => page.evaluate(() => window.scrollY);
const entries = (page) => page.evaluate(() => history.length);
async function until(fn, ms = 4000) {
  const end = Date.now() + ms;
  while (Date.now() < end) {
    if (await fn()) return true;
    await sleep(40);
  }
  return false;
}
// How far the restored scrollY may be from the one left, while the same card is back in the same
// place (checked to 4 px): the cards of a grid skip rendering off screen (content-visibility), and
// the page left may have had rows above it at the estimated size, rows it never drew (scrolled past
// in one jump); the page restored has them at their real size. A few rows' worth.
const ROWS = 300;
/** Opens a card and waits for its page (the page left can have a Back button of its own). */
async function follow(page, h) {
  await page.locator(`a.card[href="${h}"]`).click();
  await until(async () => (await hash(page)) === h);
  await back(page).waitFor();
}
/** The card nearest the middle of the screen: its link and where it sits. */
const middleCard = (page) =>
  page.evaluate(() => {
    let best = null;
    let far = Infinity;
    for (const a of document.querySelectorAll('a.card')) {
      const r = a.getBoundingClientRect();
      const d = Math.hypot(r.left + r.width / 2 - innerWidth / 2, r.top + r.height / 2 - innerHeight / 2);
      if (d < far) [best, far] = [{ href: a.getAttribute('href'), top: Math.round(r.top) }, d];
    }
    return best;
  });
/** Scrolls far down, then waits for the scroll position to stay put (it settles after restores). */
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

const scenarios = {
  // Mutants caught: the scroll not restored (restoreScroll doing nothing: Movies comes back at the
  // top, another card in the middle); the filters dropped (goBack opening the previous view's page
  // without its query: the sort and the Unwatched filter gone, so the hash and the card differ);
  // the jump not corrected to the card that was in the middle (content-visibility's estimates above
  // put another card there); the focus not moved back to that card; goBack pushing a new entry
  // instead of history.back() (history.length grows).
  async 'Movies, sorted, filtered and far down → a film → Back: the same view, at the same card'(browser) {
    const { context, page, errors } = await open(browser);
    await page.goto(`${base}/#/`);
    await page.locator('header nav a[href="#/movies"]').click();
    await page.locator('.grid .card').nth(12).waitFor(); // a virtual grid: a screenful of cards, not all 120
    await page.locator('a.chip', { hasText: 'Newest' }).click();
    await page.locator('a.chip', { hasText: 'Unwatched' }).click();
    const view = await hash(page);
    check('the sort and the filter are in the hash', view === '#/movies?sort=year&f=unwatched', view);
    check('Movies, a top-level section, has no Back button', (await back(page).count()) === 0);
    await page.evaluate(() => window.scrollTo(0, 3000));
    const y0 = await settle(page);
    const card = await middleCard(page);
    check('scrolled far down, a card in the middle', y0 >= 2500 && !!card, `${y0} ${JSON.stringify(card)}`);
    const before = await entries(page);
    await follow(page, card.href);
    check('the film page opens at the top', (await scrollY(page)) === 0);
    check('its Back button says Back (there is a previous view)', (await text(back(page))) === 'Back', await text(back(page)));
    await back(page).click();
    await until(async () => (await hash(page)).startsWith('#/movies'));
    const y1 = await settle(page);
    const after = await middleCard(page);
    check('Back returns to the exact hash', (await hash(page)) === view, await hash(page));
    check('the sort is still Newest', (await page.locator('a.chip[aria-checked="true"]').first().textContent())?.trim() === 'Newest');
    check('the scroll position is restored', Math.abs(y1 - y0) <= ROWS, `${y0} → ${y1}`);
    check('the same card is in the middle, in the same place', after?.href === card.href && Math.abs(after.top - card.top) <= 4, `${JSON.stringify(card)} → ${JSON.stringify(after)}`);
    check('the focus is back on the card that was opened', (await page.evaluate(() => document.activeElement?.getAttribute('href'))) === card.href, await page.evaluate(() => `${document.activeElement?.tagName} ${document.activeElement?.getAttribute('href')}`));
    check('Back went back in the history (no new entry)', (await entries(page)) === before + 1, `${before} → ${await entries(page)}`);
    check('no page errors', errors.length === 0, errors.join('; '));
    await context.close();
  },

  // Mutant caught: the grid not claiming the restore (the router's own scroll lands, but the card
  // isn't mounted yet when it looks for it: the focus goes to the title).
  async 'Movies with 5,000 films, far down → a film → Back: the same card, in the same place'(browser) {
    const { context, page, errors } = await open(browser, { many: 5000 });
    await page.goto(`${base}/#/`);
    await page.locator('header nav a[href="#/movies"]').click();
    await page.locator('.grid .card').nth(12).waitFor();
    check('5,000 films: a screenful of cards in the page', (await page.locator('a.card').count()) < 200, String(await page.locator('a.card').count()));
    await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight * 0.7));
    const y0 = await settle(page);
    const card = await middleCard(page);
    await follow(page, card.href);
    await back(page).click();
    await until(async () => (await hash(page)) === '#/movies');
    const y1 = await settle(page);
    const after = await middleCard(page);
    check('5,000 films: the same card in the middle, in the same place', after?.href === card.href && Math.abs(after.top - card.top) <= 4, `${JSON.stringify(card)} → ${JSON.stringify(after)}`);
    check('5,000 films: the scroll position is restored', Math.abs(y1 - y0) <= ROWS, `${y0} → ${y1}`);
    check('5,000 films: the focus is back on the card', (await page.evaluate(() => document.activeElement?.getAttribute('href'))) === card.href);
    check('5,000 films: no page errors', errors.length === 0, errors.join('; '));
    await context.close();
  },

  // Mutants caught: the scroll restored before the page is there (no wait: the genre's skeleton is
  // too short, so it lands part way); the sort not kept with the view (remember/recall: back to
  // Title, other cards).
  async 'A genre, sorted and scrolled → a film → Back: waits for the late grid, then the same card'(browser) {
    const { context, page } = await open(browser);
    await page.goto(`${base}/#/genre/18`);
    await page.locator('.grid .card, .poster-grid .card').nth(40).waitFor();
    check('a deep link to a genre: Back to Home', (await text(back(page))) === 'Home', await text(back(page)));
    await page.locator('button.chip', { hasText: 'Year' }).click();
    await page.evaluate(() => window.scrollTo(0, 2500));
    const y0 = await settle(page);
    const card = await middleCard(page);
    await follow(page, card.href);
    await back(page).click();
    const skeleton = await until(async () => (await page.locator('main [aria-busy="true"]').count()) > 0, 1000);
    check('the genre comes back as a skeleton first (the titles are late)', skeleton);
    await page.locator('.card').nth(40).waitFor();
    const y1 = await settle(page);
    const after = await middleCard(page);
    check('the sort is still Year', (await page.locator('button.chip[aria-pressed="true"]').textContent())?.trim() === 'Year');
    check('the scroll position is restored once the grid is there', Math.abs(y1 - y0) <= ROWS, `${y0} → ${y1}`);
    check('the same card is in the middle', after?.href === card.href && Math.abs(after.top - card.top) <= 4, `${JSON.stringify(card)} → ${JSON.stringify(after)}`);
    await context.close();
  },

  // Mutants caught: Back not shown on a show or a person page; goBack going to the parent although
  // there is a previous view (the person's Back would open Home, the show's Shows only by luck).
  async 'Shows → a show → a person → Back → Back'(browser) {
    const { context, page } = await open(browser);
    await page.goto(`${base}/#/shows`);
    await page.locator('a.card[href="#/shows/Lost"]').click();
    await page.locator('a.person[href="#/person/31"]').click();
    await page.locator('h1', { hasText: 'Al Pacino' }).waitFor();
    check('the person page has a Back button that says Back', (await text(back(page))) === 'Back', await text(back(page)));
    await back(page).click();
    await until(async () => (await hash(page)) === '#/shows/Lost');
    check('Back returns to the show', (await hash(page)) === '#/shows/Lost', await hash(page));
    await back(page).waitFor();
    check('the show has a Back button', (await text(back(page))) === 'Back');
    await back(page).click();
    await until(async () => (await hash(page)) === '#/shows');
    check('Back again returns to Shows', (await hash(page)) === '#/shows', await hash(page));
    await page.locator('a.card[href="#/shows/Lost"]').waitFor();
    check('Shows, a top-level section, has no Back button', (await back(page).count()) === 0);
    await context.close();
  },

  // Mutants caught: the fallback map ignored (parentOf answering Home for every page: a film's
  // Back says Home and goes there, so does a show's); an episode's own parent ignored (Back to
  // Movies instead of its show); the fallback pushed instead of replacing the entry.
  async 'A deep link has no previous view: Back goes to the parent and replaces the entry'(browser) {
    const { context, page } = await open(browser);
    const want = [
      ['#/item/5', 'Movies', '#/movies'],
      ['#/item/2001', 'Lost', '#/shows/Lost?s=1'],
      ['#/shows/Lost', 'Shows', '#/shows'],
      ['#/person/31', 'Home', '#/'],
      ['#/collection/9', 'Home', '#/'],
      ['#/genre/18', 'Home', '#/'],
    ];
    for (const [from, label, to] of want) {
      await fresh(page, from);
      await back(page).waitFor();
      if (from === '#/item/2001') await until(async () => (await text(back(page))) === label); // once the episode is known
      check(`${from}: Back says ${label}`, (await text(back(page))) === label, await text(back(page)));
      check(`${from}: and is named for it`, (await back(page).getAttribute('aria-label')) === `Back to ${label}`);
      const n = await entries(page);
      await back(page).click();
      await until(async () => (await hash(page)) === to);
      check(`${from}: Back goes to ${to}`, (await hash(page)) === to, await hash(page));
      check(`${from}: in place of the deep link (no new entry)`, (await entries(page)) === n);
    }
    await context.close();
  },

  // Mutants caught: Back missing from Activity or Settings; a Back that a keyboard can't reach or
  // press (a div with a click handler).
  async 'Activity and Settings have Back, by mouse and by keyboard'(browser) {
    const { context, page } = await open(browser);
    await page.goto(`${base}/#/`);
    await page.locator('header a[href="#/settings"]').click();
    await page.locator('h1', { hasText: 'Settings' }).waitFor();
    check('Settings: Back says Back', (await text(back(page))) === 'Back');
    await back(page).click();
    await until(async () => (await hash(page)) === '#/');
    check('Settings: Back returns Home', (await hash(page)) === '#/', await hash(page));
    await fresh(page, '#/activity');
    await page.locator('h1', { hasText: 'Activity' }).waitFor();
    check('Activity (a deep link): Back says Home', (await text(back(page))) === 'Home', await text(back(page)));
    await back(page).focus();
    check('the Back button takes the focus', await back(page).evaluate((el) => el === document.activeElement));
    await page.keyboard.press('Enter');
    await until(async () => (await hash(page)) === '#/');
    check('Enter on it goes Home', (await hash(page)) === '#/', await hash(page));
    await context.close();
  },

  // Mutants caught: goBack pushing the previous view as a new entry (the browser's Back would then
  // return to the film: a loop); the deep link's fallback pushed (the browser's Back returns to the
  // deep link); the player's Details link or its close ignoring goBack.
  async "The browser's Back after goBack doesn't loop"(browser) {
    const { context, page } = await open(browser);
    await page.goto(`${base}/#/`);
    await page.locator('header nav a[href="#/movies"]').click();
    await page.locator('a.card[href="#/item/1"]').click();
    await back(page).waitFor();
    await back(page).click();
    await until(async () => (await hash(page)) === '#/movies');
    check('goBack: Movies', (await hash(page)) === '#/movies', await hash(page));
    await page.goBack();
    await until(async () => (await hash(page)) === '#/');
    check("the browser's Back then goes Home, not to the film", (await hash(page)) === '#/', await hash(page));
    await page.goForward();
    await page.goForward();
    await until(async () => (await hash(page)) === '#/item/1');
    check('Forward twice reaches the film again', (await hash(page)) === '#/item/1', await hash(page));
    check('whose Back still says Back', (await text(back(page))) === 'Back');

    // The player's close (✕, Player.svelte's onClose): back to where it was opened from, through
    // the history, not a new entry. Mutant caught: onClose setting location.hash to the film (the
    // browser's Back then reopens the player).
    const close = 'button[aria-label="Close player"]';
    const closePlayer = () => page.evaluate((sel) => document.querySelector(sel).click(), close); // the controls hide
    await page.locator('a.btn.primary[href="#/play/1"]').click();
    await page.locator(close).waitFor({ state: 'attached' });
    const n = await entries(page);
    await closePlayer();
    await until(async () => (await hash(page)) === '#/item/1');
    check("the player's close button goes back to the film", (await hash(page)) === '#/item/1', await hash(page));
    await page.goBack();
    await until(async () => (await hash(page)) === '#/movies');
    check("and the browser's Back then goes to Movies, not the player", (await hash(page)) === '#/movies', await hash(page));
    check('no entry was added', (await entries(page)) === n);

    // Escape closes it the same way.
    await page.goForward();
    await until(async () => (await hash(page)) === '#/item/1');
    await page.locator('a.btn.primary[href="#/play/1"]').click();
    await page.locator(close).waitFor({ state: 'attached' });
    await page.keyboard.press('Escape');
    await until(async () => (await hash(page)) === '#/item/1');
    check('Escape goes back to the film', (await hash(page)) === '#/item/1', await hash(page));
    await page.goBack();
    await until(async () => !(await hash(page)).startsWith('#/item/1'));
    check("after Escape, the browser's Back goes to Movies, not the player", (await hash(page)) === '#/movies', await hash(page));

    // A deep link to the player: its close goes to the film's page, in place of the player.
    await fresh(page, '#/play/1');
    await page.locator(close).waitFor({ state: 'attached' });
    await closePlayer();
    await until(async () => (await hash(page)) === '#/item/1');
    check("a deep link's close goes to the film's page", (await hash(page)) === '#/item/1', await hash(page));

    // A deep link: its fallback replaces it, so the browser's Back leaves the app.
    await fresh(page, '#/item/7');
    await back(page).click();
    await until(async () => (await hash(page)) === '#/movies');
    await page.goBack();
    await until(async () => !page.url().includes('#/movies'));
    check("after a deep link's Back, the browser's Back doesn't return to it", !page.url().includes('#/item/7'), page.url());
    await context.close();
  },

  // Mutant caught: a restore that waits for room as far down as the page was left (a grid left at
  // its very bottom comes back shorter, here by five rows of titles gone meanwhile, more than the
  // estimated sizes of a new grid add: it stayed at the top for 8 s, then jumped).
  async 'A genre left at its very bottom → a film → Back: lands at once on the same card'(browser) {
    const { context, page } = await open(browser, { shrink: 20 });
    await page.goto(`${base}/#/genre/18`);
    await page.locator('main .card').nth(40).waitFor();
    await page.evaluate(() => window.scrollTo(0, 99_999));
    const y0 = await settle(page);
    const card = await middleCard(page);
    await follow(page, card.href);
    // Timed in the page, frame by frame: from the click, when the grid is there, when the card is.
    await page.evaluate((href) => {
      const w = window;
      w.__t = { click: performance.now() };
      const watch = () => {
        const now = performance.now();
        if (!w.__t.grid && document.querySelectorAll('main .card').length > 40) w.__t.grid = now;
        const a = document.querySelector(`main a.card[href="${href}"]`)?.getBoundingClientRect();
        if (w.__t.grid && scrollY > 0 && a && a.top < innerHeight && a.bottom > 0) w.__t.card = now;
        else if (now - w.__t.click < 5000) requestAnimationFrame(watch);
      };
      requestAnimationFrame(watch);
    }, card.href);
    await back(page).click();
    await until(async () => (await page.evaluate(() => !!window.__t.card)), 5000);
    const t = await page.evaluate(() => window.__t);
    await settle(page);
    const after = await middleCard(page);
    const total = Math.round((t.card ?? Infinity) - t.click);
    const wait = Math.round((t.card ?? Infinity) - (t.grid ?? 0));
    // About 1.5 s: the titles alone take 700 ms, and the page a varying while to draw them; the
    // restore's own share is the check below, after the grid.
    check(`back on the same card within 2 s (titles ${TITLES_DELAY} ms late)`, total <= 2000, `${total} ms ${JSON.stringify(t)}`);
    check('as soon as the grid is drawn (no wait for room that never comes)', wait <= 100, `${wait} ms after the grid`);
    // At the very bottom the page can come back a little shorter (see ROWS): the card is on screen,
    // as low as the page lets it.
    check('on screen', after?.href === card.href || (await page.evaluate((h) => { const r = document.querySelector(`main a.card[href="${h}"]`)?.getBoundingClientRect(); return !!r && r.top >= 0 && r.bottom <= innerHeight; }, card.href)), `${JSON.stringify(card)} → ${JSON.stringify(after)} (scroll ${y0} → ${await scrollY(page)})`);
    await context.close();
  },

  // Mutants caught: the views not written down on pagehide (a reload loses the previous view and the
  // scroll position); the entry left by navigate() not given its scroll position (Back after a
  // reload comes to Movies at the top).
  async 'A reload keeps the previous views and their scroll positions'(browser) {
    const { context, page } = await open(browser);
    await page.goto(`${base}/#/`);
    await page.locator('header nav a[href="#/movies"]').click();
    await page.locator('.grid .card').nth(12).waitFor(); // a virtual grid: a screenful of cards, not all 120
    await page.evaluate(() => window.scrollTo(0, 3000));
    const y0 = await settle(page);
    const card = await middleCard(page);
    await page.reload();
    await page.locator('.grid .card').nth(12).waitFor(); // a virtual grid: a screenful of cards, not all 120
    await until(async () => (await scrollY(page)) > 0);
    const y1 = await settle(page);
    const again = await middleCard(page);
    check('Movies comes back where it was, at the same card', again?.href === card.href && Math.abs(again.top - card.top) <= 4 && Math.abs(y1 - y0) <= ROWS, `${y0} → ${y1} ${JSON.stringify(card)} → ${JSON.stringify(again)}`);
    await follow(page, card.href);
    await page.reload();
    await back(page).waitFor();
    check('the film, reloaded, still has a previous view: Back says Back', (await text(back(page))) === 'Back', await text(back(page)));
    await back(page).click();
    await until(async () => (await hash(page)) === '#/movies');
    await until(async () => (await scrollY(page)) > 0);
    await settle(page);
    const after = await middleCard(page);
    check('and Back returns to Movies at the same card', after?.href === card.href && Math.abs(after.top - card.top) <= 4, `${JSON.stringify(card)} → ${JSON.stringify(after)}`);
    await context.close();
  },

  // The card → detail morph (lib/nav) in Chrome. A show's hero is the landing point of the morph
  // (hero-art) and its episodes are cards that morph: two elements of one name abort the transition
  // ("Transition was aborted because of invalid state. Snapshot capture failed", a page error). A
  // second card before the first is drawn skips the first ("Transition was skipped. New
  // ViewTransition started", an AbortError, expected). Mutants caught: go() leaving the show's hero
  // named while the episode is captured (InvalidStateError outcomes and their page errors); go() not
  // handling ready (the skip as a page error); a second go() navigating without a transition while
  // the first is pending (no skip: the two cards' outcomes differ).
  async 'The morph: from a show to its episodes and back, and two cards at once, without page errors'(browser) {
    const { context, page, errors } = await open(browser);
    // What became of each transition's ready: "ready" once it animates, or the rejection's name. The
    // app gets a relay of ready that settles the same way, so a rejection it leaves unhandled is
    // still a page error (observing the real one marks that one handled).
    await page.addInitScript(() => {
      const start = document.startViewTransition?.bind(document);
      if (!start) return;
      window.__vt = [];
      document.startViewTransition = (cb) => {
        const vt = start(cb);
        const ready = vt.ready;
        const i = window.__vt.push('pending') - 1;
        ready.then(() => (window.__vt[i] = 'ready'), (e) => (window.__vt[i] = e.name));
        Object.defineProperty(vt, 'ready', { value: new Promise((ok, fail) => ready.then(ok, fail)) });
        return vt;
      };
    });
    const outcomes = () => page.evaluate(() => window.__vt ?? null);
    /** The outcomes of the transitions after the first `from`, once none is pending. */
    const since = async (from, count) => {
      await until(async () => {
        const all = (await outcomes()) ?? [];
        return all.length >= from + count && !all.slice(from).includes('pending');
      }, 2000);
      return ((await outcomes()) ?? []).slice(from);
    };
    await page.goto(`${base}/#/shows/Lost`);
    for (const id of [2001, 2002]) {
      const ep = page.locator(`a.ep[href="#/item/${id}"]`);
      await ep.waitFor();
      check(`episode ${id}: the show's hero holds the morph's name`, await page.evaluate(() => [...document.querySelectorAll('.hero .backdrop')].some((el) => el.style.viewTransitionName === 'hero-art')));
      const n = (await outcomes())?.length ?? -1;
      await ep.click();
      await until(async () => (await hash(page)) === `#/item/${id}`);
      check(`episode ${id}: its page opens`, (await hash(page)) === `#/item/${id}`, await hash(page));
      const got = await since(n, 1);
      check(`episode ${id}: the morph ran`, JSON.stringify(got) === '["ready"]', JSON.stringify(got));
      await back(page).waitFor();
      await back(page).click();
      await until(async () => (await hash(page)) === '#/shows/Lost');
      check(`episode ${id}: Back returns to the show`, (await hash(page)) === '#/shows/Lost', await hash(page));
    }
    await page.goto(`${base}/#/movies`);
    await page.locator('a.card[href="#/item/2"]').waitFor();
    const n = (await outcomes())?.length ?? -1;
    await page.evaluate(() => ['#/item/1', '#/item/2'].forEach((h) => document.querySelector(`a.card[href="${h}"]`).click()));
    await until(async () => (await hash(page)) === '#/item/2');
    const got = await since(n, 2);
    check('two cards at once: the first transition skipped (AbortError), the second animated', JSON.stringify(got) === '["AbortError","ready"]', JSON.stringify(got));
    check('two cards at once: the second one opens', (await hash(page)) === '#/item/2', await hash(page));
    await sleep(600); // past the morph
    check('the morph: no page errors', errors.length === 0, errors.join('; '));
    await context.close();
  },

  // Mutant caught: the Back button of an episode reached by a deep link naming Movies (the film
  // fallback) until the item is in, then its show.
  async "An episode's deep link: Back names its show, never Movies"(browser) {
    const { context, page } = await open(browser, { slowItems: 800 });
    await fresh(page, '#/item/2001');
    const labels = new Set();
    const boxes = [];
    let sawPending = false; // the positive control: there was a time the item wasn't known
    const end = Date.now() + 2500;
    while (Date.now() < end) {
      const box = await page.evaluate(() => {
        const b = document.querySelector('.back')?.getBoundingClientRect();
        return b ? `${b.left},${b.top},${b.height}` : null;
      });
      if (box && boxes.at(-1) !== box) boxes.push(box);
      sawPending ||= (await page.locator('span.back.pending').count()) > 0;
      if (await back(page).count()) labels.add(await text(back(page)));
      if (labels.has('Lost')) break;
      await sleep(30);
    }
    check('the episode was unknown for a while (slow details)', sawPending);
    const placeKept = boxes.length === 1 && (await page.locator('span.back.pending').count()) === 0;
    check('Back only ever says Lost', JSON.stringify([...labels]) === '["Lost"]', JSON.stringify([...labels]));
    check('its place was kept while unknown (no shift)', placeKept, `${JSON.stringify(boxes)}`);
    await context.close();
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
