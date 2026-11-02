// The M6 metadata UI in a real browser, against a mocked API: the fix-match dialog, the dialog's
// backdrop, the hero's logo fallback and the settings page's review list. The unit tests pin the
// helpers (lib/meta.ts, parseMatchInput); this pins what the components do with them.
//
// Self-contained: serves web/dist (build it first: `npm run build`; PLY_E2E_DIST picks another
// directory) and answers every /api/ request with page.route. Needs Google Chrome.
//
//   npm run build && node e2e/metadata-ui.mjs
//
// PLY_E2E_ONLY=<text> runs the scenarios whose name contains it. Each scenario names the mutation it
// was confirmed to catch (build the mutant into another directory with
// `vite build --outDir ... --emptyOutDir` and point PLY_E2E_DIST at it).
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

const LOGO_OK = '/api/images/tmdb/w500/logo-ok.svg';
const LOGO_BROKEN = '/api/images/tmdb/w500/logo-broken.svg';
const film = (id, title, year, extra = {}) => ({
  id, kind: 'movie', title, year, durationSec: 7000, container: 'mkv', videoCodec: 'h264', audioCodec: 'aac',
  width: 1920, height: 1080, addedAt: id, progress: null, art: null, titleId: 100 + id, matchState: 'matched', needsReview: false,
  ...extra,
});
const ITEMS = [film(1, '1917', 2019, { logo: LOGO_BROKEN }), film(2, 'Logo Film', 2020, { logo: LOGO_OK })];
const meta = (it) => ({
  id: it.titleId, kind: 'movie', title: it.title, year: it.year, itemId: it.id, sortTitle: it.title, genres: [], cast: [], crew: [],
  source: 'tmdb', locked: false, matchState: 'matched', needsReview: false, logo: it.logo,
});
const detail = (it) => ({
  ...it, path: `/m/${it.title}.mkv`, size: 1, bitrate: 1, streams: [], subtitles: [],
  playback: { method: 'direct', videoAction: 'copy', audioAction: 'copy', audioIndex: 1, reasons: [], url: '', startSec: 0 },
  meta: meta(it),
});
const CANDIDATES = {
  movie: [{ tmdbId: 530915, title: '1917', year: 2019, overview: 'Two soldiers race against time.', score: 0.97 }],
  series: [{ tmdbId: 1396, title: 'Breaking Bad', year: 2008, overview: 'A chemistry teacher turns to crime.', score: 0.95 }],
};
const REVIEW = [
  { kind: 'movie', itemId: 1, title: 'Slow Film', year: 2001, matchState: 'unmatched', needsReview: false, score: 0 },
  { kind: 'series', show: 'Breaking Bad', title: 'Breaking Bad', year: 2008, matchState: 'matched', needsReview: true, score: 0.7 },
];
const svg = '<svg xmlns="http://www.w3.org/2000/svg" width="400" height="120"><rect width="400" height="120" fill="#f0b64e"/></svg>';

/**
 * A fresh page on the mocked server.
 *   slowMovieSearch  the movie search answers after this many ms (the show search at once)
 *   keyChecked       what PUT /api/settings says about the key
 */
async function open(browser, opts = {}) {
  const context = await browser.newContext({ viewport: { width: 1280, height: 800 }, serviceWorkers: 'block' });
  const page = await context.newPage();
  const calls = []; // non-GET calls: { method, path, body }
  const searches = []; // the q of each metadata search
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  const json = (route, body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  await page.route('**/api/**', async (route) => {
    const req = route.request();
    const u = new URL(req.url());
    const p = u.pathname;
    if (req.method() !== 'GET') calls.push({ method: req.method(), path: p, body: req.postData() ? JSON.parse(req.postData()) : null });
    if (p === LOGO_OK) return route.fulfill({ contentType: 'image/svg+xml', body: svg });
    if (p.startsWith('/api/images/')) return route.fulfill({ status: 404, body: '' });
    if (p === '/api/info') return json(route, { id: 'x', name: 'e2e', version: 'dev', authRequired: false });
    if (p === '/api/scan') return json(route, { running: false, itemCount: ITEMS.length, watching: [] });
    if (p === '/api/items') return json(route, ITEMS);
    if (p === '/api/shows' || p === '/api/continue' || p === '/api/upcoming' || p === '/api/genres') return json(route, []);
    if (p === '/api/settings' && req.method() === 'PUT')
      return json(route, { tmdbKeySet: true, tmdbKeySource: 'settings', metadataEnabled: false, metadataForcedOff: true, language: 'en-US', tmdbKeyChecked: opts.keyChecked ?? true });
    if (p === '/api/settings') return json(route, { tmdbKeySet: false, tmdbKeySource: '', metadataEnabled: false, metadataForcedOff: true, language: 'en-US' });
    if (p === '/api/metadata/status') return json(route, { enabled: true, keySet: true, running: false, pending: 0, unmatched: 1, needsReview: 1, lastError: '' });
    if (p === '/api/metadata/review') return json(route, REVIEW);
    if (p === '/api/metadata/search') {
      const kind = u.searchParams.get('kind');
      searches.push(u.searchParams.get('q'));
      if (kind === 'movie' && opts.slowMovieSearch) await sleep(opts.slowMovieSearch);
      return json(route, CANDIDATES[kind] ?? []);
    }
    const m = /^\/api\/items\/(\d+)(\/match)?$/.exec(p);
    if (m) {
      const it = ITEMS.find((i) => i.id === Number(m[1]));
      return it ? json(route, detail(it)) : json(route, { error: 'not found' }, 404);
    }
    if (p.startsWith('/api/shows/')) return json(route, { show: 'Breaking Bad', episodeCount: 0, seasons: [] });
    return json(route, { error: `unmocked ${p}` }, 404);
  });
  return { context, page, calls, searches, errors };
}

const dialog = (page) => page.locator('dialog[open]');
async function openFix(page) {
  await page.click('[aria-haspopup="menu"]');
  await page.click('[role="menuitem"]:has-text("Fix the match")');
  await dialog(page).waitFor();
}

const scenarios = {
  // Mutant caught: a bare number read as a TMDB id again (parseMatchInput).
  async 'B1: a numeric title is searched, and a result is what gets matched'(browser) {
    const { context, page, calls, searches } = await open(browser);
    await page.goto(`${base}/#/item/1`);
    await openFix(page);
    const box = dialog(page).locator('input[type="search"]');
    await page.waitForSelector('dialog[open] .result');
    check('the box is prefilled with the title', (await box.inputValue()) === '1917');
    check('the title was searched for', searches.includes('1917'), `searches: ${searches.join(', ')}`);
    check('Search stays usable', await dialog(page).locator('button:has-text("Search")').isEnabled());
    check('no "Use this film" for a bare number', (await dialog(page).locator('text=Use this film').count()) === 0);
    check('the number is offered as an id, separately', (await dialog(page).locator('text=Use TMDB id 1917').count()) === 1);
    await dialog(page).locator('.result').first().click();
    await sleep(200);
    const put = calls.find((c) => c.method === 'PUT' && c.path === '/api/items/1/match');
    check('choosing the result matches that result', put?.body?.tmdbId === 530915, JSON.stringify(put?.body));
    // Positive control: an explicit TMDB id is used as one.
    await openFix(page);
    await box.fill('tmdb:603');
    await dialog(page).locator('text=Use this film').click();
    await sleep(200);
    const put2 = calls.filter((c) => c.method === 'PUT').at(-1);
    check('"tmdb:603" is used as an id', put2?.body?.tmdbId === 603, JSON.stringify(put2?.body));
    await context.close();
  },

  // Mutant caught: no focus call (showModal leaves the focus on Close).
  async 'Fix match: the search box has the focus'(browser) {
    const { context, page } = await open(browser);
    await page.goto(`${base}/#/item/1`);
    await openFix(page);
    await sleep(100);
    check('focus is in the search box', await page.evaluate(() => document.activeElement?.matches('dialog input[type="search"]') ?? false));
    await context.close();
  },

  // Mutant caught: wrongKind always false (a show's link offered as "Use this film").
  async 'Fix match: a link to the other kind of title is refused'(browser) {
    const { context, page, calls } = await open(browser);
    await page.goto(`${base}/#/item/1`);
    await openFix(page);
    await dialog(page).locator('input[type="search"]').fill('https://www.themoviedb.org/tv/1396-breaking-bad');
    check('it says why', (await dialog(page).locator('text=a link to a show').count()) === 1);
    check('nothing to click', (await dialog(page).locator('text=Use this film').count()) === 0);
    // Positive control: a film's link is offered.
    await dialog(page).locator('input[type="search"]').fill('https://www.themoviedb.org/movie/530915-1917');
    check('a film link is offered', (await dialog(page).locator('text=Use this film').count()) === 1);
    check('nothing was matched', !calls.some((c) => c.method === 'PUT'));
    await context.close();
  },

  // Mutant caught: the answer of a search is used without checking it is still the latest.
  async 'Fix match: a slow answer for an earlier dialog never shows'(browser) {
    const { context, page } = await open(browser, { slowMovieSearch: 1200 });
    await page.goto(`${base}/#/settings`);
    await page.waitForSelector('.review li');
    await page.locator('.review li', { hasText: 'Slow Film' }).locator('button:has-text("Fix")').click();
    await dialog(page).waitFor();
    await page.keyboard.press('Escape');
    await page.locator('.review li', { hasText: 'Breaking Bad' }).locator('button:has-text("Fix")').click();
    await page.waitForSelector('dialog[open] .result');
    await sleep(1600); // the film's answer has arrived by now
    const titles = await dialog(page).locator('.result .title').allTextContents();
    check("the show's dialog shows only the show's candidates", titles.length === 1 && titles[0].startsWith('Breaking Bad'), titles.join(' | '));
    await context.close();
  },

  // Mutant caught: a click whose target is the backdrop closes, wherever the press began.
  async 'Dialog: dragging a selection out onto the backdrop keeps it open'(browser) {
    const { context, page } = await open(browser);
    await page.goto(`${base}/#/item/1`);
    await openFix(page);
    const box = dialog(page).locator('input[type="search"]');
    await box.fill('the thing I typed');
    const b = await box.boundingBox();
    await page.mouse.move(b.x + b.width - 5, b.y + b.height / 2);
    await page.mouse.down();
    await page.mouse.move(b.x + b.width / 2, 790, { steps: 8 }); // out of the dialog, onto the backdrop
    await page.mouse.up();
    await sleep(150);
    check('still open', (await dialog(page).count()) === 1);
    check('what was typed is kept', (await box.inputValue()) === 'the thing I typed');
    // Positive control: a real click on the backdrop closes it.
    await page.mouse.click(30, 790);
    await sleep(150);
    check('a click on the backdrop closes it', (await dialog(page).count()) === 0);
    await context.close();
  },

  // Mutant caught: the h1 hidden whenever a logo URL exists (no load/error handling).
  async 'Hero: the title stays when the logo cannot load, and gives way when it does'(browser) {
    const { context, page } = await open(browser);
    await page.goto(`${base}/#/item/1`); // its logo answers 404
    await page.waitForSelector('h1');
    await sleep(400);
    const h1 = page.locator('h1');
    check('a broken logo: the title is visible', (await h1.isVisible()) && !(await h1.evaluate((e) => e.classList.contains('sr-only'))));
    check('a broken logo: the text is right', (await h1.textContent())?.trim() === '1917');
    // Positive control: a logo that loads takes the title's place (the h1 stays for screen readers).
    await page.goto(`${base}/#/item/2`);
    await page.waitForSelector('img.logo:not(.pending)');
    check('a working logo: the h1 is only for screen readers', await page.locator('h1').evaluate((e) => e.classList.contains('sr-only')));
    await context.close();
  },

  // Mutant caught: `matched` passed for every row.
  async 'Settings: "Remove the match" only for a row that has one'(browser) {
    const { context, page } = await open(browser);
    await page.goto(`${base}/#/settings`);
    await page.waitForSelector('.review li');
    await page.locator('.review li', { hasText: 'Slow Film' }).locator('button:has-text("Fix")').click();
    await dialog(page).waitFor();
    check('an unmatched film: no "Remove the match"', (await dialog(page).locator('text=Remove the match').count()) === 0);
    await page.keyboard.press('Escape');
    await page.locator('.review li', { hasText: 'Breaking Bad' }).locator('button:has-text("Fix")').click();
    await dialog(page).waitFor();
    check('a show matched but unsure: "Remove the match" is there', (await dialog(page).locator('text=Remove the match').count()) === 1);
    await context.close();
  },

  // Mutant caught: the "key saved" message whatever tmdbKeyChecked says.
  async 'Settings: a key saved while lookups are off says it was not checked'(browser) {
    for (const checked of [false, true]) {
      const { context, page } = await open(browser, { keyChecked: checked });
      await page.goto(`${base}/#/settings`);
      await page.fill('#tmdb-key', 'abc123');
      await page.click('button:has-text("Save key")');
      await page.waitForSelector('[role="status"]');
      const text = (await page.locator('.card [role="status"]').first().textContent()) ?? '';
      check(`tmdbKeyChecked=${checked}: the message says so`, checked ? !/not checked/.test(text) : /not checked/.test(text), text.trim());
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
