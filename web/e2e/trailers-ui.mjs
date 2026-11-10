// Trailers in a real browser, against a mocked API: the Trailer button of a film's and a show's
// hero, the player it opens (YouTube's or Vimeo's, in an <iframe> made on the click), the picker
// between several, and the pages that have none (an episode, a title with no trailer, lookups off,
// a failed request). The unit tests pin lib/trailers.ts; this pins what the pages do with it.
//
// Self-contained: serves web/dist (build it first: `npm run build`; PLY_E2E_DIST picks another
// directory), answers every /api/ request with page.route, and answers the players' own sites with
// a blank page (nothing leaves the machine). Needs Google Chrome.
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
const OFFICIAL = { site: 'youtube', key: 'vKQi3bBA1y8', name: 'Official Trailer', type: 'trailer', language: 'en', official: true, publishedAt: '1999-03-01' };
const TEASER = { site: 'vimeo', key: '76979871', type: 'teaser', official: false };
const SHOW_TRAILER = { site: 'youtube', key: 'HhesaQXLuRY', name: 'Season 1 Trailer', type: 'trailer', official: true };
const TRAILERS = { [FILM_TITLE]: [OFFICIAL, TEASER], [BARE_TITLE]: [], [SHOW_TITLE]: [SHOW_TRAILER] };
const YOUTUBE = 'https://www.youtube-nocookie.com/embed/';
const VIMEO = 'https://player.vimeo.com/video/';

/**
 * A fresh page on the mocked server.
 *   trailers  what GET /api/titles/{id}/trailers answers: { status, body } by title id, else TRAILERS
 * `asked` lists the trailer requests by title id; `sites` the requests to YouTube and Vimeo.
 */
async function open(browser, opts = {}) {
  const context = await browser.newContext({ viewport: { width: 1280, height: 800 }, serviceWorkers: 'block' });
  const page = await context.newPage();
  const asked = [];
  const sites = [];
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  const json = (route, body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  await context.route(/^https:\/\/(www\.youtube-nocookie\.com|player\.vimeo\.com)\//, (route) => {
    sites.push(route.request().url());
    return route.fulfill({ contentType: 'text/html', body: '<!doctype html><title>player</title><body style="background:#000"></body>' });
  });
  await page.route('**/api/**', async (route) => {
    const u = new URL(route.request().url());
    const p = u.pathname;
    if (p.startsWith('/api/images/')) return route.fulfill({ status: 404, body: '' });
    if (p === '/api/info') return json(route, { id: 'x', name: 'e2e', version: 'dev', authRequired: false });
    if (p === '/api/scan') return json(route, { running: false, itemCount: ITEMS.length, watching: [] });
    if (p === '/api/items') return routeItems(route, ITEMS);
    if (p === '/api/continue' || p === '/api/upcoming' || p === '/api/genres') return json(route, []);
    if (p === '/api/shows') return json(route, [{ show: SHOW, seasons: [1], episodeCount: 1, titleId: SHOW_TITLE, matchState: 'matched', needsReview: false }]);
    if (p === `/api/shows/${encodeURIComponent(SHOW)}`)
      return json(route, { show: SHOW, episodeCount: 1, meta: META[3], seasons: [{ season: 1, name: 'Season 1', episodeCount: 1 }] });
    const t = /^\/api\/titles\/(\d+)\/trailers$/.exec(p);
    if (t) {
      const id = Number(t[1]);
      asked.push(id);
      const custom = opts.trailers?.[id];
      if (custom) return json(route, custom.body, custom.status);
      return json(route, TRAILERS[id] ?? [], TRAILERS[id] ? 200 : 404);
    }
    const m = /^\/api\/items\/(\d+)$/.exec(p);
    if (m) {
      const it = ITEMS.find((i) => i.id === Number(m[1]));
      return it ? json(route, detail(it)) : json(route, { error: 'not found' }, 404);
    }
    return json(route, { error: `unmocked ${p}` }, 404);
  });
  return { context, page, asked, sites, errors };
}

// Exactly "Trailer": the dialog's chips ("Official Trailer") are inside the hero's actions too.
const trailerButton = (page) => page.locator('.hero-actions').getByRole('button', { name: 'Trailer', exact: true });
const dialog = (page) => page.locator('dialog[open]');
const iframeSrc = (page) => dialog(page).locator('iframe').getAttribute('src');
// The hero has painted from the detail: the play button and the title.
const heroReady = (page) => page.locator('.hero-actions a.btn.primary').waitFor();

const scenarios = {
  // Mutants caught: TrailerButton's {#if trailers.length} turned into {#if true} (a button with
  // nothing to play); the iframe's src built from the Vimeo branch for every site.
  async 'a film: the Trailer button opens the best trailer in YouTube’s privacy-enhanced player'(browser) {
    const { context, page, asked, sites, errors } = await open(browser);
    await page.goto(`${base}/#/item/1`);
    await trailerButton(page).waitFor();
    check('the film’s title was asked for its trailers', asked.includes(FILM_TITLE), asked.join());
    check('nothing reached YouTube before the click', sites.length === 0, sites.join());
    await trailerButton(page).click();
    await dialog(page).waitFor();
    const src = await iframeSrc(page);
    check('the first trailer plays, at once', src === `${YOUTUBE}${OFFICIAL.key}?autoplay=1&rel=0&playsinline=1`, src ?? 'no iframe');
    check('the dialog is named after it', (await dialog(page).locator('h2').textContent())?.trim() === 'Official Trailer');
    const frame = dialog(page).locator('iframe');
    check('the iframe sends the origin as referrer (YouTube refuses an embed without one)', (await frame.getAttribute('referrerpolicy')) === 'strict-origin-when-cross-origin');
    check('it may go full screen', (await frame.getAttribute('allowfullscreen')) !== null && /fullscreen/.test((await frame.getAttribute('allow')) ?? ''));
    check('the iframe says what it is', /Official Trailer – The Matrix \(YouTube\)/.test((await frame.getAttribute('title')) ?? ''), await frame.getAttribute('title'));
    await sleep(200);
    check('the player was loaded from YouTube once the dialog opened', sites.some((s) => s.startsWith(YOUTUBE + OFFICIAL.key)), sites.join());
    check('the button says it opens a dialog', (await trailerButton(page).getAttribute('aria-haspopup')) === 'dialog');
    // Closed with its ✕ (the positive control: the iframe is there until then).
    check('control: the iframe is there before closing', (await page.locator('iframe').count()) === 1);
    await dialog(page).locator('header button[aria-label="Close"]').click();
    await sleep(150);
    check('✕ closes the player and takes the iframe away', (await dialog(page).count()) === 0 && (await page.locator('iframe').count()) === 0);
    check('the focus is back on the Trailer button after ✕', await trailerButton(page).evaluate((b) => b === document.activeElement));
    check('no page error', errors.length === 0, errors.join('; '));
    await context.close();
  },

  // Mutants caught: the chips' onclick not changing `current`; the picker shown for one trailer.
  async 'a film with two: the picker switches to the other, and Escape closes the player'(browser) {
    const { context, page } = await open(browser);
    await page.goto(`${base}/#/item/1`);
    await trailerButton(page).click();
    await dialog(page).waitFor();
    const chips = dialog(page).locator('ul[aria-label="Trailers"] button');
    check('both trailers are offered', (await chips.count()) === 2 && (await chips.allTextContents()).map((s) => s.trim()).join('|') === 'Official Trailer|Teaser');
    check('the one playing is pressed', (await chips.nth(0).getAttribute('aria-pressed')) === 'true' && (await chips.nth(1).getAttribute('aria-pressed')) === 'false');
    await sleep(200); // the first player has loaded
    const entries = await page.evaluate(() => history.length);
    await chips.nth(1).click();
    await sleep(300);
    // Mutant caught: the iframe's src changed in place ({#key src} dropped), which Chrome adds to the history.
    check('switching adds no history entry (Back still leaves the page)', (await page.evaluate(() => history.length)) === entries, `${entries} → ${await page.evaluate(() => history.length)}`);
    const src = await iframeSrc(page);
    check('the teaser plays in Vimeo’s player, with Do Not Track', src === `${VIMEO}${TEASER.key}?autoplay=1&dnt=1`, src ?? 'no iframe');
    check('the dialog is named after it', (await dialog(page).locator('h2').textContent())?.trim() === 'Teaser');
    await page.keyboard.press('Escape');
    await sleep(150);
    check('Escape closes the player', (await dialog(page).count()) === 0);
    check('and takes the iframe away (the video stops)', (await page.locator('iframe').count()) === 0);
    check('the focus is back on the Trailer button', await trailerButton(page).evaluate((b) => b === document.activeElement));
    await context.close();
  },

  // Mutant caught: no max-height on .frame (the player and the chips taller than a short window).
  async 'a short window: the player stays 16:9 and wholly in view, with the picker under it'(browser) {
    const { context, page } = await open(browser);
    // 568x320, a small phone held sideways, is narrower than 600 px: the dialog is the window's width
    // there, and only the frame's own max-height keeps it in view.
    for (const viewport of [{ width: 568, height: 320 }, { width: 640, height: 320 }, { width: 844, height: 390 }, { width: 1280, height: 560 }, { width: 1280, height: 800 }, { width: 390, height: 844 }]) {
      await page.setViewportSize(viewport);
      await page.goto(`${base}/#/item/1`);
      await trailerButton(page).click();
      await dialog(page).waitFor();
      await sleep(150);
      const box = await dialog(page).locator('.frame').boundingBox();
      const chips = await dialog(page).locator('ul[aria-label="Trailers"]').boundingBox();
      const at = `${viewport.width}x${viewport.height}`;
      check(`${at}: control, the frame and the picker are there`, !!box && !!chips);
      if (box && chips) {
        check(`${at}: the player is 16:9`, Math.abs(box.width / box.height - 16 / 9) < 0.02, `${box.width.toFixed(0)}x${box.height.toFixed(0)}`);
        check(`${at}: the player and the picker are in the window`, box.y >= 0 && chips.y + chips.height <= viewport.height + 1, `frame ${box.y.toFixed(0)}, chips end ${(chips.y + chips.height).toFixed(0)}`);
      }
      await page.keyboard.press('Escape');
      await sleep(100);
    }
    await context.close();
  },

  // Mutant caught: Show.svelte not rendering TrailerButton.
  async 'a show: its hero has the series’ trailer'(browser) {
    const { context, page, asked } = await open(browser);
    await page.goto(`${base}/#/shows/${encodeURIComponent(SHOW)}`);
    await trailerButton(page).waitFor({ timeout: 5000 });
    check('the show’s title was asked', asked.includes(SHOW_TITLE), asked.join());
    await trailerButton(page).click();
    await dialog(page).waitFor();
    check('the show’s trailer plays', (await iframeSrc(page))?.startsWith(YOUTUBE + SHOW_TRAILER.key) ?? false, await iframeSrc(page));
    check('no picker for one trailer', (await dialog(page).locator('ul[aria-label="Trailers"]').count()) === 0);
    await context.close();
  },

  // Mutant caught: Item.svelte showing TrailerButton for an episode (its meta is its show's).
  async 'an episode has no Trailer button, and its show is not asked'(browser) {
    const { context, page, asked } = await open(browser);
    await page.goto(`${base}/#/item/3`);
    await heroReady(page);
    await page.locator('.hero .meta', { hasText: 'S01E01' }).waitFor(); // the detail is in: meta is the show's
    await sleep(300);
    check('control: the episode page is drawn', (await page.locator('.hero-actions a.btn.primary').count()) === 1);
    check('no Trailer button', (await trailerButton(page).count()) === 0);
    check('no trailers asked for', asked.length === 0, asked.join());
    await context.close();
  },

  // Mutant caught: playable() dropped from TrailerButton (a key that is not one gets a button).
  async 'no button for a title with none, with lookups off, an error, or only keys that are not one'(browser) {
    const cases = [
      ['none', { [BARE_TITLE]: { status: 200, body: [] } }],
      ['a failed request', { [BARE_TITLE]: { status: 502, body: { error: 'could not reach TMDB' } } }],
      ['no metadata subsystem', { [BARE_TITLE]: { status: 503, body: { error: 'metadata is not available' } } }],
      ['only keys that are not one', { [BARE_TITLE]: { status: 200, body: [{ site: 'youtube', key: '"><img src=x>', official: true }, { site: 'dailymotion', key: 'x7tgad0', official: true }] } }],
    ];
    for (const [name, trailers] of cases) {
      const { context, page, asked, errors } = await open(browser, { trailers });
      await page.goto(`${base}/#/item/2`);
      await heroReady(page);
      for (let i = 0; i < 40 && !asked.includes(BARE_TITLE); i++) await sleep(50);
      await sleep(200);
      check(`${name}: control, the trailers were asked for`, asked.includes(BARE_TITLE), asked.join());
      check(`${name}: no Trailer button`, (await trailerButton(page).count()) === 0);
      check(`${name}: no page error`, errors.length === 0, errors.join('; '));
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
