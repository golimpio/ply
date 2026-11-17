// Trailers in a real browser, against a mocked API (docs/M13-TRAILERS.md, "Clients"): the Trailer
// button of a film's and a show's hero plays the trailer file in ply's own player; while the server
// downloads one it spins, and the job's end on the live feed makes it playable (or takes it away);
// an episode has none. Nothing reaches YouTube or Vimeo. The unit tests pin lib/trailers.ts; this
// pins what the pages do with it.
//
// Self-contained: serves web/dist (build it first: `npm run build`; PLY_E2E_DIST picks another
// directory) and answers every /api/ request with page.route. Needs Google Chrome.
//
//   npm run build && node e2e/trailers-ui.mjs
//
// PLY_E2E_ONLY=<text> runs the scenarios whose name contains it. Each scenario names the mutation it
// was confirmed to catch.
import { createServer } from 'node:http';
import { existsSync, readFileSync } from 'node:fs';
import { dirname, extname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright-core';
import { eventStream, jobsSnapshot } from './mock-feed.mjs';
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

const SHOW = 'Cool Show';
const FILM_TITLE = 603;
const BARE_TITLE = 604;
const SHOW_TITLE = 900;
const row = { durationSec: 6000, container: 'mp4', videoCodec: 'h264', audioCodec: 'aac', width: 1920, height: 1080, progress: null, art: null, matchState: 'matched', needsReview: false };
const ITEMS = [
  { ...row, id: 1, kind: 'movie', title: 'The Matrix', year: 1999, addedAt: 3, titleId: FILM_TITLE },
  { ...row, id: 2, kind: 'movie', title: 'No Trailer', year: 2001, addedAt: 2, titleId: BARE_TITLE },
  { ...row, id: 3, kind: 'episode', title: 'Pilot', episodeTitle: 'Pilot', show: SHOW, season: 1, episode: 1, addedAt: 1 },
];
const titleMeta = (id, kind, title) => ({ id, kind, title, year: 1999, sortTitle: title, genres: [], cast: [], crew: [], source: 'tmdb', locked: false, matchState: 'matched', needsReview: false });
const META = { 1: titleMeta(FILM_TITLE, 'movie', 'The Matrix'), 2: titleMeta(BARE_TITLE, 'movie', 'No Trailer'), 3: titleMeta(SHOW_TITLE, 'series', SHOW) };
const detail = (it) => ({
  ...it, path: `/m/${it.id}.mp4`, size: 1, bitrate: 1, streams: [], subtitles: [],
  playback: { method: 'direct', videoAction: 'copy', audioAction: 'copy', audioIndex: 1, reasons: [], url: '', startSec: 0 },
  meta: META[it.id],
});
const TRAILER = { ...row, id: 50, kind: 'trailer', title: 'The Matrix', year: 1999, addedAt: 0, durationSec: 120 };
const TEASER = { ...row, id: 51, kind: 'trailer', title: 'The Matrix', year: 1999, addedAt: 0, durationSec: 60 };
const local = (it, name) => ({ itemId: it.id, name, durationSec: it.durationSec });
const TRAILERS = {
  [FILM_TITLE]: { local: [local(TRAILER, 'The Matrix (1999)-trailer')] },
  [BARE_TITLE]: { local: [] },
  [SHOW_TITLE]: { local: [local(TEASER, 'Cool Show-trailer')] },
};
const job = (id, state) => ({ id, kind: 'trailer.download', key: `title:${BARE_TITLE}`, title: 'Trailer: No Trailer (2001)', priority: 0, state, user: true, done: 0, total: 0, attempts: 1, createdAt: '2026-11-17T00:00:00Z' });

/**
 * A fresh page on the mocked server.
 *   trailers  what GET /api/titles/{id}/trailers answers, by title id (a function of how many times it
 *             was asked, or a body), else TRAILERS
 *   feed      the events of GET /api/events after its snapshot ([type, data] pairs)
 * `asked` lists the trailer requests by title id; `sites` the requests to YouTube and Vimeo; `items`
 * the item details asked for.
 */
async function open(browser, opts = {}) {
  const context = await browser.newContext({ viewport: { width: 1280, height: 800 }, serviceWorkers: 'block' });
  const page = await context.newPage();
  const asked = [];
  const sites = [];
  const items = [];
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  const json = (route, body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  await context.route(/^https:\/\/([a-z0-9-]+\.)*(youtube\.com|youtube-nocookie\.com|vimeo\.com)\//, (route) => {
    sites.push(route.request().url());
    return route.abort();
  });
  await page.route('**/api/**', async (route) => {
    const u = new URL(route.request().url());
    const p = u.pathname;
    if (p.startsWith('/api/images/')) return route.fulfill({ status: 404, body: '' });
    if (p === '/api/info') return json(route, { id: 'x', name: 'e2e', version: 'dev', authRequired: false });
    if (p === '/api/scan') return json(route, { running: false, itemCount: ITEMS.length, watching: [] });
    if (p === '/api/items') return routeItems(route, ITEMS);
    if (p === '/api/events') return route.fulfill(eventStream({ jobs: jobsSnapshot(), retry: 60_000, events: opts.feed ?? [] }));
    if (p === '/api/continue' || p === '/api/upcoming' || p === '/api/genres') return json(route, []);
    if (p === '/api/shows') return json(route, [{ show: SHOW, seasons: [1], episodeCount: 1, titleId: SHOW_TITLE, matchState: 'matched', needsReview: false }]);
    if (p === `/api/shows/${encodeURIComponent(SHOW)}`)
      return json(route, { show: SHOW, episodeCount: 1, meta: META[3], seasons: [{ season: 1, name: 'Season 1', episodeCount: 1 }] });
    const t = /^\/api\/titles\/(\d+)\/trailers$/.exec(p);
    if (t) {
      const id = Number(t[1]);
      asked.push(id);
      const custom = opts.trailers?.[id];
      const body = typeof custom === 'function' ? custom(asked.filter((a) => a === id).length) : (custom ?? TRAILERS[id]);
      return body ? json(route, body) : json(route, { error: 'title not found' }, 404);
    }
    const m = /^\/api\/items\/(\d+)$/.exec(p);
    if (m) {
      items.push(Number(m[1]));
      const it = [...ITEMS, TRAILER, TEASER].find((i) => i.id === Number(m[1]));
      return it ? json(route, detail(it)) : json(route, { error: 'not found' }, 404);
    }
    return json(route, { error: `unmocked ${p}` }, 404);
  });
  return { context, page, asked, sites, items, errors };
}

const hero = (page) => page.locator('.hero-actions');
const trailerLink = (page) => hero(page).getByRole('link', { name: 'Trailer', exact: true });
const spinning = (page) => hero(page).getByRole('button', { name: 'Downloading trailer' });
const heroReady = (page) => page.locator('.hero-actions a.btn.primary').waitFor();

const scenarios = {
  // Mutants caught: TrailerButton's play branch dropped (no link); href.play(itemId) of the film instead
  // of the trailer's; Player.svelte not saying "Trailer".
  async 'a film: the Trailer link plays the trailer file in ply’s player'(browser) {
    const { context, page, asked, sites, items, errors } = await open(browser);
    await page.goto(`${base}/#/item/1`);
    await trailerLink(page).waitFor();
    check('the film’s title was asked for its trailers', asked.includes(FILM_TITLE), asked.join());
    check('the link plays the trailer item', (await trailerLink(page).getAttribute('href')) === `#/play/${TRAILER.id}`, await trailerLink(page).getAttribute('href'));
    await trailerLink(page).click();
    await page.waitForFunction((id) => location.hash === `#/play/${id}`, TRAILER.id);
    for (let i = 0; i < 50 && !items.includes(TRAILER.id); i++) await sleep(50);
    check('the player asks for the trailer item', items.includes(TRAILER.id), items.join());
    await page.getByText('Trailer', { exact: true }).first().waitFor({ timeout: 5000 });
    check('nothing reached YouTube or Vimeo', sites.length === 0, sites.join());
    check('no page error', errors.length === 0, errors.join('; '));
    await context.close();
  },

  // Mutant caught: the Menu of the other trailers shown for one, or not for two.
  async 'several trailers: the others are in a menu'(browser) {
    const two = { local: [local(TRAILER, 'The Matrix (1999)-trailer'), local(TEASER, 'Teaser-trailer')] };
    const { context, page } = await open(browser, { trailers: { [FILM_TITLE]: two } });
    await page.goto(`${base}/#/item/1`);
    await trailerLink(page).waitFor();
    await hero(page).getByRole('button', { name: 'More trailers' }).click();
    const entry = page.getByRole('menuitem', { name: 'Teaser-trailer' });
    check('the second trailer is offered', (await entry.count()) === 1);
    await entry.click();
    await page.waitForFunction((id) => location.hash === `#/play/${id}`, TEASER.id);
    check('picking it plays it', true);
    await context.close();
    const one = await open(browser);
    await one.page.goto(`${base}/#/item/1`);
    await trailerLink(one.page).waitFor();
    check('no menu for one', (await hero(one.page).getByRole('button', { name: 'More trailers' }).count()) === 0);
    await one.context.close();
  },

  // Mutants caught: the downloading branch dropped (no spinner); the job.done not refetching; a
  // failed download leaving the spinner.
  async 'a download: the button spins, then plays the trailer once the job is done'(browser) {
    const { context, page, asked, sites } = await open(browser, {
      trailers: { [BARE_TITLE]: (n) => (n === 1 ? { local: [], download: { state: 'running', jobId: 77 } } : { local: [local(TRAILER, 'x-trailer')] }) },
      feed: [['job.done', { seq: 2, type: 'job.done', job: job(77, 'done') }]],
    });
    await page.goto(`${base}/#/item/2`);
    await heroReady(page);
    await trailerLink(page).waitFor({ timeout: 8000 });
    check('it was asked again after the job ended', asked.filter((a) => a === BARE_TITLE).length >= 2, asked.join());
    check('nothing reached YouTube or Vimeo', sites.length === 0, sites.join());
    await context.close();

    const spin = await open(browser, { trailers: { [BARE_TITLE]: { local: [], download: { state: 'queued', jobId: 78 } } } });
    await spin.page.goto(`${base}/#/item/2`);
    await spinning(spin.page).waitFor();
    check('queued: a disabled button with a spinner', (await spinning(spin.page).isDisabled()) && (await spinning(spin.page).locator('.spinner').count()) === 1);
    await spin.context.close();

    const failed = await open(browser, {
      trailers: { [BARE_TITLE]: (n) => (n === 1 ? { local: [], download: { state: 'running', jobId: 79 } } : { local: [], download: { state: 'failed', jobId: 79, error: 'YouTube: Sign in' } }) },
      feed: [['job.failed', { seq: 2, type: 'job.failed', job: job(79, 'failed') }]],
    });
    await failed.page.goto(`${base}/#/item/2`);
    await heroReady(failed.page);
    for (let i = 0; i < 100 && failed.asked.filter((a) => a === BARE_TITLE).length < 2; i++) await sleep(50);
    await sleep(200);
    check('failed: asked again, and no button', failed.asked.filter((a) => a === BARE_TITLE).length >= 2 &&
      (await spinning(failed.page).count()) === 0 && (await trailerLink(failed.page).count()) === 0, failed.asked.join());
    await failed.context.close();
  },

  // Mutant caught: Show.svelte not rendering TrailerButton.
  async 'a show: its hero has the series’ trailer'(browser) {
    const { context, page } = await open(browser);
    await page.goto(`${base}/#/shows/${encodeURIComponent(SHOW)}`);
    await trailerLink(page).waitFor({ timeout: 5000 });
    check('the show’s trailer plays', (await trailerLink(page).getAttribute('href')) === `#/play/${TEASER.id}`);
    await context.close();
  },

  // Mutant caught: Item.svelte showing TrailerButton for an episode (its meta is its show's).
  async 'an episode has no Trailer button, and its show is not asked'(browser) {
    const { context, page, asked } = await open(browser);
    await page.goto(`${base}/#/item/3`);
    await heroReady(page);
    await sleep(300);
    check('no Trailer button', (await trailerLink(page).count()) === 0 && (await spinning(page).count()) === 0);
    check('no trailers asked for', asked.length === 0, asked.join());
    await context.close();
  },

  // Mutant caught: a button for a title with none (the play branch taken for local: []).
  async 'no button for a title with none'(browser) {
    const { context, page, asked, errors } = await open(browser);
    await page.goto(`${base}/#/item/2`);
    await heroReady(page);
    for (let i = 0; i < 50 && !asked.includes(BARE_TITLE); i++) await sleep(50);
    await sleep(200);
    check('control: the trailers were asked for', asked.includes(BARE_TITLE), asked.join());
    check('no Trailer button', (await trailerLink(page).count()) === 0 && (await spinning(page).count()) === 0);
    check('no page error', errors.length === 0, errors.join('; '));
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
