// The M6 metadata UI in a real browser, against a mocked API: the fix-match dialog, the dialog's
// backdrop, the hero's logo fallback, the settings page's review list and matching status, and how
// Home and Movies fold the versions of one film into a tile. The unit tests pin the helpers
// (lib/meta.ts, parseMatchInput); this pins what the components do with them.
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

// Two files of one film (a 4K and a 1080p version), for the pages that fold them into one tile.
const FOUR_K = { width: 3840, height: 2160 };
const twin = (id, extra = {}) => film(id, 'Twin Film', 2021, { titleId: 500, ...extra });
const playing = { positionSec: 600, watched: false };
const status = (over = {}) => ({ enabled: true, keySet: true, running: false, pending: 0, unmatched: 0, needsReview: 0, lastError: '', ...over });

/**
 * A fresh page on the mocked server.
 *   slowMovieSearch  the movie search answers after this many ms (the show search at once)
 *   keyChecked       what PUT /api/settings says about the key
 *   items            what /api/items lists (the item pages still know ITEMS only)
 *   continueItems    what /api/continue lists
 *   status           the metadata status: an object, or a function of how many times it was asked for
 *   review           the review list: an array, or a function of how many times it was asked for
 * `hits` counts the GETs of each path.
 */
async function open(browser, opts = {}) {
  const context = await browser.newContext({ viewport: { width: 1280, height: 800 }, serviceWorkers: 'block' });
  const page = await context.newPage();
  const calls = []; // non-GET calls: { method, path, body }
  const searches = []; // the q of each metadata search
  const hits = {}; // GETs by path
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  const json = (route, body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  await page.route('**/api/**', async (route) => {
    const req = route.request();
    const u = new URL(req.url());
    const p = u.pathname;
    if (req.method() !== 'GET') calls.push({ method: req.method(), path: p, body: req.postData() ? JSON.parse(req.postData()) : null });
    else hits[p] = (hits[p] ?? 0) + 1;
    const answer = (v) => (typeof v === 'function' ? v(hits[p]) : v);
    if (p === LOGO_OK) return route.fulfill({ contentType: 'image/svg+xml', body: svg });
    if (p.startsWith('/api/images/')) return route.fulfill({ status: 404, body: '' });
    if (p === '/api/info') return json(route, { id: 'x', name: 'e2e', version: 'dev', authRequired: false });
    if (p === '/api/scan') return json(route, { running: false, itemCount: ITEMS.length, watching: [] });
    if (p === '/api/items') return json(route, opts.items ?? ITEMS);
    if (p === '/api/continue') return json(route, opts.continueItems ?? []);
    if (p === '/api/shows' || p === '/api/upcoming' || p === '/api/genres') return json(route, []);
    if (p === '/api/settings' && req.method() === 'PUT')
      return json(route, { tmdbKeySet: true, tmdbKeySource: 'settings', metadataEnabled: false, metadataForcedOff: true, language: 'en-US', tmdbKeyChecked: opts.keyChecked ?? true });
    if (p === '/api/settings') return json(route, { tmdbKeySet: false, tmdbKeySource: '', metadataEnabled: false, metadataForcedOff: true, language: 'en-US' });
    if (p === '/api/metadata/status')
      return json(route, answer(opts.status) ?? { enabled: true, keySet: true, running: false, pending: 0, unmatched: 1, needsReview: 1, lastError: '' });
    if (p === '/api/metadata/review') return json(route, answer(opts.review) ?? REVIEW);
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
  return { context, page, calls, searches, hits, errors };
}

const dialog = (page) => page.locator('dialog[open]');
async function openFix(page) {
  await page.click('[aria-haspopup="menu"]');
  await page.click('[role="menuitem"]:has-text("Fix the match")');
  await dialog(page).waitFor();
}

// The titles of the cards on the Movies page for a query string ('' or '?f=4k,hdr').
async function movieTitles(page, query) {
  await page.goto(`${base}/#/movies${query}`);
  await page.waitForSelector('h1 .count'); // the library has loaded
  await sleep(250);
  return page.locator('.grid a.card .title').allTextContents();
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

  // Mutant caught: Escape left to the search box (Chrome clears a type="search" box with the first press).
  async 'Fix match: one Escape closes the dialog, with a title in the search box'(browser) {
    const { context, page } = await open(browser);
    await page.goto(`${base}/#/item/1`);
    await openFix(page);
    await page.waitForFunction(() => document.activeElement?.matches('dialog input[type="search"]'));
    const box = dialog(page).locator('input[type="search"]');
    check('the box has the title in it', (await box.inputValue()) === '1917');
    await page.keyboard.press('Escape');
    await sleep(150);
    const closed = (await dialog(page).count()) === 0;
    check('one press closes the dialog', closed, closed ? '' : `still open, box now: "${await box.inputValue()}"`);
    if (!closed) await dialog(page).locator('button[aria-label="Close"]').click();
    // Mutant caught: closeOnEscape without the isComposing guard. An Escape that cancels an IME
    // composition (Japanese, Chinese) belongs to the input method, not to the dialog. Positive
    // control: the plain press above, through the same handler, closed it.
    await openFix(page);
    await box.evaluate((el) => el.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', isComposing: true, bubbles: true, cancelable: true })));
    await sleep(150);
    const stillOpen = (await dialog(page).count()) === 1;
    check('an Escape while composing text leaves it open', stillOpen);
    if (stillOpen) await dialog(page).locator('button[aria-label="Close"]').click();
    // Positive control: with the box empty one press has always closed it.
    await openFix(page);
    await box.fill('');
    await page.keyboard.press('Escape');
    await sleep(150);
    check('an empty box: one press closes it', (await dialog(page).count()) === 0);
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

  // Mutant caught: `matching` ignoring running and pending (polls for ever); no app.libraryVersion
  // bump when matching ends (the review list and the rest of the page keep what they had).
  async 'Settings: polling stops when matching ends, and the page refetches what changed'(browser) {
    const { context, page, hits } = await open(browser, {
      // A pass under way, then one waiting for its retry, then done.
      status: (n) => (n === 1 ? status({ running: true, pending: 2 }) : n === 2 ? status({ pending: 1 }) : status()),
      review: (n) => (n === 1 ? REVIEW : REVIEW.slice(1)), // the matcher settled the first row
    });
    await page.clock.install(); // the poll is every 3 s: wind it on by hand
    await page.goto(`${base}/#/settings`);
    const line = page.locator('.status p').first();
    await line.getByText('Matching 2 titles…').waitFor();
    check('the review list starts with both rows', (await page.locator('.review li').count()) === 2);
    await page.clock.runFor(3000);
    await line.getByText('Matching 1 title…').waitFor();
    await page.clock.runFor(3000);
    await line.getByText('Everything has been looked up.').waitFor();
    check('the status was asked for again while it ran', hits['/api/metadata/status'] >= 3, `asked ${hits['/api/metadata/status']}x`);
    // Matching ended: app.libraryVersion went up, so the page asks again for what may have changed.
    const one = await page.waitForFunction(() => document.querySelectorAll('.review li').length === 1, null, { polling: 100, timeout: 3000 }).then(
      () => true,
      () => false,
    );
    check('the review list was fetched again', one && hits['/api/metadata/review'] === 2, `${hits['/api/metadata/review']} fetches`);
    check('the settings were fetched again', hits['/api/settings'] === 2, `${hits['/api/settings']} fetches`);
    await sleep(300);
    const asked = hits['/api/metadata/status'];
    await page.clock.runFor(20000);
    await sleep(300);
    check('the polling has stopped', hits['/api/metadata/status'] === asked, `asked ${asked}x, then ${hits['/api/metadata/status']}x`);
    check('the review list was not fetched again for it', hits['/api/metadata/review'] === 2, `${hits['/api/metadata/review']} fetches`);
    await context.close();
  },

  // Mutant caught: `matching` without keySet (a server with no key reports its queue for ever).
  async 'Settings: with no key, or lookups off, it is not "Matching" and does not poll'(browser) {
    const cases = [
      ['no key', status({ keySet: false, running: true, pending: 5 }), /^No TMDB key/, false],
      ['lookups off', status({ enabled: false, running: true, pending: 5 }), /^Matching is off/, false],
      // Positive control: the same queue with a key and lookups on is being matched, and polled.
      ['a key and lookups on', status({ running: true, pending: 5 }), /^Matching 5 titles/, true],
    ];
    for (const [name, st, text, polls] of cases) {
      const { context, page, hits } = await open(browser, { status: st });
      await page.clock.install();
      await page.goto(`${base}/#/settings`);
      const line = page.locator('.status p').first();
      await line.waitFor();
      const said = ((await line.textContent()) ?? '').trim();
      check(`${name}: what it says`, text.test(said), said);
      await page.clock.runFor(10000);
      await sleep(300);
      const asked = hits['/api/metadata/status'];
      check(`${name}: ${polls ? 'the status is asked for again' : 'the status is not asked for again'}`, polls ? asked > 1 : asked === 1, `asked ${asked}x`);
      await context.close();
    }
  },

  // Mutant caught: "Refresh all details" enabled with no key. The server accepts it, but with no key it
  // only re-reads the local files of unmatched titles, while the page says every title is being refreshed.
  async 'Settings: "Refresh all details" needs a key'(browser) {
    const cases = [
      ['no key', status({ keySet: false }), false],
      ['lookups off', status({ enabled: false }), false],
      ['a key and lookups on', status(), true], // positive control
    ];
    for (const [name, st, enabled] of cases) {
      const { context, page } = await open(browser, { status: st });
      await page.goto(`${base}/#/settings`);
      const button = page.locator('button:has-text("Refresh all details")');
      await button.waitFor();
      check(`${name}: Refresh all details is ${enabled ? 'enabled' : 'disabled'}`, (await button.isEnabled()) === enabled);
      await context.close();
    }
  },

  // Mutant caught: the notice reworded (the TMDB API terms, paragraph 3, give it word for word).
  async 'Settings: the TMDB notice is the one the terms require'(browser) {
    const { context, page } = await open(browser);
    await page.goto(`${base}/#/settings`);
    const notice = page.locator('footer.attribution');
    await notice.waitFor();
    const said = ((await notice.textContent()) ?? '').replace(/\s+/g, ' ').trim();
    const required = 'This product uses TMDB and the TMDB APIs but is not endorsed, certified, or otherwise approved by TMDB.';
    check('the notice is word for word', said.includes(required), said);
    check('TMDB links to themoviedb.org', (await notice.locator('a[href="https://www.themoviedb.org/"]').count()) === 1);
    await context.close();
  },

  // Mutant caught: onePerTitle returning the list as it is.
  async 'Home: two versions of one film are one tile, in Recently added and in Continue watching'(browser) {
    const { context, page } = await open(browser, {
      items: [...ITEMS, twin(3, FOUR_K), twin(4)],
      // The server lists the most recent first: the 1080p copy was played last.
      continueItems: [twin(4, { progress: playing }), twin(3, { ...FOUR_K, progress: playing })],
    });
    await page.goto(`${base}/`);
    const rail = (name) => page.locator(`ul[aria-label="${name}"] > li`);
    await rail('Continue watching').first().waitFor();
    const recent = await rail('Recently added').locator('.title').allTextContents();
    check('Recently added: the film once, beside the other two', recent.length === 3 && recent.filter((t) => t === 'Twin Film').length === 1, recent.join(' | '));
    check('Recently added: its newest version', (await rail('Recently added').locator('a[href="#/item/4"]').count()) === 1);
    const resume = await rail('Continue watching').locator('a').evaluateAll((as) => as.map((a) => a.getAttribute('href')));
    check('Continue watching: one tile, the version played last', resume.length === 1 && resume[0].startsWith('#/play/4'), resume.join(' | '));
    await context.close();
  },

  // Mutant caught: filmPasses asking for 4K and for HDR of any version, not of one.
  async 'Movies: with 4K and HDR on, one version must be both'(browser) {
    const { context, page } = await open(browser, {
      items: [
        ...ITEMS, // two 1080p SDR films
        film(3, 'Split Film', 2021, { titleId: 600, ...FOUR_K }), // 4K, SDR
        film(4, 'Split Film', 2021, { titleId: 600, hdr: 'hdr10' }), // 1080p, HDR
        film(5, 'Both Film', 2022, { ...FOUR_K, hdr: 'hdr10' }),
      ],
    });
    const all = await movieTitles(page, '');
    check('no filter: the two files are one card', all.length === 4 && all.filter((t) => t === 'Split Film').length === 1, all.join(' | '));
    // Positive controls: either filter alone takes a film when any version fits.
    check('4K alone: any version', (await movieTitles(page, '?f=4k')).join(' | ') === 'Both Film | Split Film');
    check('HDR alone: any version', (await movieTitles(page, '?f=hdr')).join(' | ') === 'Both Film | Split Film');
    const both = await movieTitles(page, '?f=4k,hdr');
    check('4K and HDR: only the film with a version that is both', both.join(' | ') === 'Both Film', both.join(' | '));
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
