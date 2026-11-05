// The M8 TVDB UI in a real browser, against a mocked API: the Settings TVDB card (key, PIN, Remove,
// Test, the attribution, the status line), the Show page's episode order, Fix match with TVDB
// references and results, the source badges, and a merged person. The unit tests pin the helpers
// (parseMatchInput, candidateRef, the api calls); this pins what the components do with them.
//
// Self-contained: serves web/dist (build it first: `npm run build`; PLY_E2E_DIST picks another
// directory) and answers every /api/ request with page.route. Needs Google Chrome.
//
//   npm run build && node e2e/tvdb-ui.mjs
//
// PLY_E2E_ONLY=<text> runs the scenarios whose name contains it. PLY_E2E_SHOTS=<dir> also saves
// screenshots of the TVDB card and the episode order (wide and at 360 px). Each scenario names the
// mutation it was confirmed to catch (build the mutant into another directory with
// `vite build --outDir ... --emptyOutDir` and point PLY_E2E_DIST at it).
//
// Every key and PIN here is an obvious fake: the repository is public.
import { createServer } from 'node:http';
import { existsSync, mkdirSync, readFileSync } from 'node:fs';
import { dirname, extname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright-core';

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

// What the server sends (internal/api/meta_types.go). The page must show it as it is.
const ATTRIBUTION = 'Metadata provided by TheTVDB. Please consider adding missing information or subscribing.';
const ATTRIBUTION_URL = 'https://thetvdb.com';
const SHOW = 'Breaking Bad';
const TITLE_ID = 77;

const film = { id: 1, kind: 'movie', title: 'Heat', year: 1995, durationSec: 9000, container: 'mkv', videoCodec: 'h264', audioCodec: 'aac', width: 1920, height: 1080, addedAt: 1, progress: null, art: null, titleId: 101, matchState: 'matched', needsReview: false };
const episode = (id, season, ep) => ({
  id, kind: 'episode', title: SHOW, show: SHOW, season, episode: ep, episodeTitle: `Episode ${ep}`, durationSec: 2800, container: 'mkv',
  videoCodec: 'h264', audioCodec: 'aac', width: 1920, height: 1080, addedAt: id, progress: null, art: null, titleId: TITLE_ID, matchState: 'matched', needsReview: false,
});
const ITEMS = [film, episode(11, 1, 1), episode(12, 1, 2)];

const titleDetail = (over) => ({
  id: TITLE_ID, kind: 'series', title: SHOW, year: 2008, sortTitle: SHOW, genres: [], cast: [], crew: [], locked: false, matchState: 'matched', needsReview: false, ...over,
});

const settings = (over = {}) => ({
  tmdbKeySet: true, tmdbKeySource: 'settings', metadataEnabled: true, metadataForcedOff: false, language: 'en-US',
  maxStreams: 4, maxTranscodes: 2, maxStreamsSource: 'default', maxTranscodesSource: 'default',
  tvdbKeySet: false, tvdbPinSet: false, tvdbKeySource: '', tvdbAttribution: ATTRIBUTION, tvdbAttributionUrl: ATTRIBUTION_URL,
  ...over,
});
const status = (over = {}) => ({
  enabled: true, keySet: true, tvdbKeySet: true, shows: 'tvdb', showsLine: 'Shows: TVDB', running: false, pending: 0, unmatched: 0, needsReview: 0, lastError: '', ...over,
});

const CANDIDATES = [
  { source: 'tvdb', id: 81189, tvdbId: 81189, title: SHOW, year: 2008, overview: 'From TVDB.', score: 0.99 },
  { source: 'tmdb', id: 1396, tmdbId: 1396, title: SHOW, year: 2008, overview: 'From TMDB.', score: 0.95 },
];

/**
 * A fresh page on the mocked server.
 *   settings     GET /api/settings (merged over the defaults above)
 *   status       GET /api/metadata/status (merged over the defaults above): an object, or a
 *                function of how many times it was asked for
 *   put          (body, n) => [status, json] for the n-th PUT /api/settings
 *   putDelay     ms before a PUT /api/settings is answered
 *   search       (n) => [status, json] for the n-th metadata search
 *   match        (body, n) => [status, json] for the n-th PUT of the show's match
 *   showDelay    (n) => ms before the n-th GET of the show is answered
 *   test         (provider, n) => json for the n-th POST /api/settings/test
 *   testDelay    (provider, n) => ms before the n-th POST /api/settings/test is answered
 *   source       the show's source ('tvdb' by default); order its episodeOrder
 *   patch        (body, n) => [status, json] for the n-th PATCH /api/titles/77
 *   width        the viewport's width
 * `calls` lists the non-GET requests; `hits` counts the GETs of each path.
 */
async function open(browser, opts = {}) {
  const context = await browser.newContext({ viewport: { width: opts.width ?? 1280, height: 900 }, serviceWorkers: 'block' });
  const page = await context.newPage();
  const calls = [];
  const hits = {};
  const counts = {};
  const errors = [];
  const show = { source: opts.source ?? 'tvdb', order: opts.order ?? 'aired', tvdbId: 81189 };
  // The settings as the server keeps them: a PUT changes what the next GET says.
  let stored = settings(opts.settings);
  const store = (b) => {
    const next = { ...stored };
    if (b.tvdbKey !== undefined) Object.assign(next, b.tvdbKey ? { tvdbKeySet: true, tvdbKeySource: 'settings' } : { tvdbKeySet: false, tvdbKeySource: '' });
    if (b.tvdbPin !== undefined) Object.assign(next, { tvdbPinSet: !!b.tvdbPin, ...('tvdbPinSource' in stored ? { tvdbPinSource: b.tvdbPin ? 'settings' : '' } : {}) });
    if (b.tmdbKey !== undefined) Object.assign(next, b.tmdbKey ? { tmdbKeySet: true, tmdbKeySource: 'settings' } : { tmdbKeySet: false, tmdbKeySource: '' });
    stored = next;
    return { ...next, ...(b.tvdbKey ? { tvdbKeyChecked: !next.metadataForcedOff } : {}), ...(b.tmdbKey ? { tmdbKeyChecked: !next.metadataForcedOff } : {}) };
  };
  page.on('pageerror', (e) => errors.push(e.message));
  const json = (route, body, st = 200) => route.fulfill({ status: st, contentType: 'application/json', body: JSON.stringify(body) });
  await page.route('**/api/**', async (route) => {
    const req = route.request();
    const u = new URL(req.url());
    const p = u.pathname;
    const method = req.method();
    const body = req.postData() ? JSON.parse(req.postData()) : null;
    if (method !== 'GET') {
      calls.push({ method, path: p, body });
      counts[`${method} ${p}`] = (counts[`${method} ${p}`] ?? 0) + 1;
    } else hits[p] = (hits[p] ?? 0) + 1;
    const n = counts[`${method} ${p}`];
    if (p.startsWith('/api/images/')) return route.fulfill({ status: 404, body: '' });
    if (p === '/api/info') return json(route, { id: 'x', name: 'e2e', version: 'dev', authRequired: false });
    if (p === '/api/scan') return json(route, { running: false, itemCount: ITEMS.length, watching: [] });
    if (p === '/api/items') return json(route, ITEMS);
    if (p === '/api/shows') return json(route, [{ show: SHOW, seasons: [1], episodeCount: 2, matchState: 'matched', needsReview: false }]);
    if (p === '/api/continue' || p === '/api/upcoming' || p === '/api/genres' || p === '/api/folders') return json(route, []);
    if (p === '/api/metadata/review') return json(route, []);
    if (p === '/api/settings' && method === 'GET') return json(route, stored);
    if (p === '/api/settings') {
      const [st, out] = opts.put ? opts.put(body, n) : [200, store(body)];
      if (opts.putDelay) await sleep(opts.putDelay);
      return json(route, out, st);
    }
    if (p === '/api/settings/test') {
      const out = opts.test ? opts.test(body?.provider, n) : { ok: true };
      const wait = opts.testDelay?.(body?.provider, n) ?? 0;
      if (wait) await sleep(wait);
      return json(route, out);
    }
    if (p === '/api/metadata/status') return json(route, status(typeof opts.status === 'function' ? opts.status(hits[p]) : opts.status));
    if (p === '/api/metadata/search') {
      if (opts.search) {
        const [st, out] = opts.search(hits[p]);
        return json(route, out, st);
      }
      return json(route, u.searchParams.get('kind') === 'series' ? CANDIDATES : []);
    }
    if (p === `/api/titles/${TITLE_ID}` && method === 'PATCH') {
      const [st, out] = opts.patch
        ? opts.patch(body, n)
        : [200, titleDetail({ source: 'tvdb', episodeOrder: body?.episodeOrder, unmapped: 0 })];
      if (st === 200 && body?.episodeOrder) show.order = body.episodeOrder;
      return json(route, st === 200 ? { tvdbId: show.tvdbId, ...out } : out, st);
    }
    if (p === `/api/shows/${encodeURIComponent(SHOW)}/match`) {
      const [st, out] = opts.match ? opts.match(body, n) : [200, { show: SHOW, episodeCount: 2, seasons: [] }];
      if (st === 200) {
        // As the server: the title keeps its row; a new match starts at the aired order (store/meta.go).
        const tvdb = body?.tvdbId ?? (/^tvdb:(\d+)$/.exec(body?.tvdb ?? '')?.[1] && Number(/^tvdb:(\d+)$/.exec(body.tvdb)[1]));
        if (tvdb) Object.assign(show, { source: 'tvdb', tvdbId: tvdb, order: 'aired' });
        else if (body?.tmdbId) Object.assign(show, { source: 'tmdb', order: 'aired' });
      }
      return json(route, out, st);
    }
    if (p === `/api/shows/${encodeURIComponent(SHOW)}`) {
      const order = show.order; // what the server has when the request comes, however long the answer takes
      const wait = opts.showDelay?.(hits[p]) ?? 0;
      if (wait) await sleep(wait);
      const meta = titleDetail({ source: show.source, ...(show.source === 'tvdb' ? { tvdbId: show.tvdbId, episodeOrder: order } : { tmdbId: 1396 }) });
      return json(route, { show: SHOW, episodeCount: 2, seasons: [{ season: 1, name: 'Season 1', episodeCount: 2 }], meta });
    }
    const item = /^\/api\/items\/(\d+)$/.exec(p);
    if (item && Number(item[1]) === film.id) {
      return json(route, {
        ...film, path: '/m/Heat.mkv', size: 1, bitrate: 1, streams: [], subtitles: [],
        playback: { method: 'direct', videoAction: 'copy', audioAction: 'copy', audioIndex: 1, reasons: [], url: '', startSec: 0 },
        meta: { id: 101, kind: 'movie', title: 'Heat', year: 1995, itemId: 1, sortTitle: 'Heat', genres: [], cast: [], crew: [], source: opts.itemSource ?? 'tmdb', locked: false, matchState: 'matched', needsReview: false },
      });
    }
    const person = /^\/api\/people\/(\d+)$/.exec(p);
    if (person) {
      const id = Number(person[1]);
      // 5 was merged into 8 (the server's answer for the old id); 6 was never in the library.
      if (id === 5) return json(route, { error: 'merged', into: 8 }, 404);
      if (id === 6) return json(route, { error: 'person not found' }, 404);
      // 7 was merged into 8 just now: the answer is 8's.
      return json(route, { id: id === 7 ? 8 : id, name: 'Bryan Cranston', tmdbId: 17419, tvdbId: 251010, credits: [] });
    }
    return json(route, { error: `unmocked ${p}` }, 404);
  });
  return { context, page, calls, hits, errors, show };
}

const card = (page) => page.locator('.card.tvdb');
const lastCall = (calls, method, path) => calls.filter((c) => c.method === method && c.path === path).at(-1);
const dialog = (page) => page.locator('dialog[open]');

const scenarios = {
  // Mutants caught: the key sent as tmdbKey (or under any other name), and the 400's message
  // replaced by a generic one: the server's words are what the person needs.
  async 'TVDB card: a refused key is said inline, then a good one is saved'(browser) {
    const { context, page, calls } = await open(browser, {
      put: (body, n) =>
        n === 1 ? [400, { error: 'TVDB rejected the key or PIN' }] : [200, settings({ tvdbKeySet: true, tvdbKeySource: 'settings', tvdbKeyChecked: true })],
    });
    await page.goto(`${base}/#/settings`);
    await card(page).waitFor();
    check('the key field is a password field', (await page.locator('#tvdb-key').getAttribute('type')) === 'password');
    await page.fill('#tvdb-key', 'fake-refused-key');
    await card(page).locator('button:has-text("Save")').click();
    const msg = card(page).locator('p[role="status"]').first();
    await msg.waitFor();
    check('the refusal is said as the server said it', (await text(msg)) === 'TVDB rejected the key or PIN', await text(msg));
    check('it is an error', await msg.evaluate((e) => e.classList.contains('error')));
    const first = lastCall(calls, 'PUT', '/api/settings');
    check('the key went as tvdbKey, and nothing else', JSON.stringify(first?.body) === JSON.stringify({ tvdbKey: 'fake-refused-key' }), JSON.stringify(first?.body));
    check('the refused key stays in the field to be fixed', (await page.inputValue('#tvdb-key')) === 'fake-refused-key');
    await page.fill('#tvdb-key', 'fake-good-key');
    await card(page).locator('button:has-text("Save")').click();
    await card(page).locator('p[role="status"].ok').waitFor();
    const ok = await text(card(page).locator('p[role="status"].ok'));
    check('a good key: saved, and said so', /^Key saved/.test(ok), ok);
    check('the field is emptied', (await page.inputValue('#tvdb-key')) === '');
    await context.close();
  },

  // Mutant caught: the PIN sent as tvdbKey (the server would check it as a key and refuse it), or
  // the key field's empty value sent along with it ("" removes the stored key).
  async 'TVDB card: a PIN alone is sent alone'(browser) {
    const { context, page, calls } = await open(browser, { settings: { tvdbKeySet: true, tvdbKeySource: 'settings' } });
    await page.goto(`${base}/#/settings`);
    await card(page).waitFor();
    check('the PIN field is a password field', (await page.locator('#tvdb-pin').getAttribute('type')) === 'password');
    check('the PIN is explained', /subscription/.test(await text(card(page).locator('#tvdb-pin-hint'))));
    await page.fill('#tvdb-pin', '0000-fake');
    await card(page).locator('button:has-text("Save")').click();
    await card(page).locator('p[role="status"].ok').waitFor();
    const put = lastCall(calls, 'PUT', '/api/settings');
    check('only the PIN was sent', JSON.stringify(put?.body) === JSON.stringify({ tvdbPin: '0000-fake' }), JSON.stringify(put?.body));
    check('it says the PIN was saved', (await text(card(page).locator('p[role="status"].ok'))) === 'PIN saved.');
    await context.close();
  },

  // Mutants caught: Remove key offered for a key from the environment (removing the stored one
  // would change nothing), and Remove sending anything but "".
  async 'TVDB card: Remove, and the wording for a key set elsewhere'(browser) {
    let { context, page, calls } = await open(browser, { settings: { tvdbKeySet: true, tvdbKeySource: 'settings', tvdbPinSet: true } });
    await page.goto(`${base}/#/settings`);
    await card(page).waitFor();
    check('from this page: it says so', /A key is set, from this page\./.test(await text(card(page))));
    await card(page).locator('button:has-text("Remove key")').click();
    await card(page).locator('p[role="status"].ok').waitFor();
    check('Remove key sends tvdbKey ""', JSON.stringify(lastCall(calls, 'PUT', '/api/settings')?.body) === JSON.stringify({ tvdbKey: '' }));
    // Mutant caught: no focus() after a save (Remove goes away, and the focus falls to the page).
    await card(page).locator('button:has-text("Remove key")').waitFor({ state: 'detached' });
    await sleep(200);
    check('Remove key: the focus is on what happened', await page.evaluate(() => document.activeElement?.matches('.card.tvdb p[role="status"]') ?? false), await page.evaluate(() => document.activeElement?.outerHTML.slice(0, 80)));
    await card(page).locator('button:has-text("Remove PIN")').click();
    await page.waitForFunction(() => document.querySelector('.card.tvdb p[role="status"].ok')?.textContent?.includes('PIN removed'));
    check('Remove PIN sends tvdbPin ""', JSON.stringify(lastCall(calls, 'PUT', '/api/settings')?.body) === JSON.stringify({ tvdbPin: '' }));
    await context.close();

    // The TMDB card's Remove keeps the focus too.
    ({ context, page, calls } = await open(browser));
    await page.goto(`${base}/#/settings`);
    await page.locator('form.key button:has-text("Remove")').click();
    await page.locator('.card p[role="status"].ok').first().waitFor();
    await sleep(300);
    check('TMDB Remove: the focus is on what happened', await page.evaluate(() => document.activeElement?.textContent?.trim() === 'Key removed.'));
    await context.close();

    // Mutant caught: Remove PIN offered whatever tvdbPinSource says (removing the stored PIN leaves
    // the flag's, and a typed PIN would be "saved" while the flag's still wins).
    for (const source of ['flag', 'env']) {
      ({ context, page, calls } = await open(browser, { settings: { tvdbKeySet: true, tvdbKeySource: 'settings', tvdbPinSet: true, tvdbPinSource: source } }));
      await page.goto(`${base}/#/settings`);
      await card(page).waitFor();
      check(`PIN from ${source}: no Remove PIN, no PIN field`, (await card(page).locator('button:has-text("Remove PIN")').count()) === 0 && (await page.locator('#tvdb-pin').count()) === 0);
      check(`PIN from ${source}: says where to change it`, (await text(card(page))).includes('The PIN comes from -tvdb-pin or PLY_TVDB_PIN; change it there.'));
      await context.close();
    }
    // Positive control: a PIN stored here can be removed.
    ({ context, page } = await open(browser, { settings: { tvdbKeySet: true, tvdbKeySource: 'settings', tvdbPinSet: true, tvdbPinSource: 'settings' } }));
    await page.goto(`${base}/#/settings`);
    await card(page).waitFor();
    check('PIN from this page: Remove PIN is there', (await card(page).locator('button:has-text("Remove PIN")').count()) === 1);
    await context.close();

    // Mutant caught: "PIN removed." whatever the answer says. A server without tvdbPinSource: the PIN
    // is still set after the remove (a flag's), or the remove is refused (409).
    for (const [name, put] of [
      ['still set after it', () => [200, settings({ tvdbKeySet: true, tvdbKeySource: 'settings', tvdbPinSet: true })]],
      ['a 409', () => [409, { error: 'tvdbPin is set by -tvdb-pin and cannot be removed here' }]], // the server's words
    ]) {
      ({ context, page } = await open(browser, { settings: { tvdbKeySet: true, tvdbKeySource: 'settings', tvdbPinSet: true }, put }));
      await page.goto(`${base}/#/settings`);
      await card(page).locator('button:has-text("Remove PIN")').click();
      const msg = card(page).locator('p[role="status"]').first();
      await msg.waitFor();
      check(`Remove PIN, ${name}: says the PIN comes from elsewhere`, (await text(msg)) === 'The PIN comes from -tvdb-pin or PLY_TVDB_PIN; change it there.', await text(msg));
      await context.close();
    }

    // Mutant caught: "PIN saved." with -metadata=false, where nothing was checked.
    ({ context, page } = await open(browser, { settings: { tvdbKeySet: true, tvdbKeySource: 'settings', metadataEnabled: false, metadataForcedOff: true } }));
    await page.goto(`${base}/#/settings`);
    await page.fill('#tvdb-pin', '0000-fake');
    await card(page).locator('button:has-text("Save")').click();
    await card(page).locator('p[role="status"].ok').waitFor();
    check('a PIN saved with lookups forced off: not checked', /^PIN saved, not checked/.test(await text(card(page).locator('p[role="status"].ok'))));
    await context.close();

    for (const [source, words] of [
      ['env', 'the PLY_TVDB_KEY environment variable'],
      ['flag', 'the -tvdb-key flag'],
    ]) {
      ({ context, page, calls } = await open(browser, { settings: { tvdbKeySet: true, tvdbKeySource: source } }));
      await page.goto(`${base}/#/settings`);
      await card(page).waitFor();
      const said = await text(card(page));
      check(`from ${source}: it says where, and to change it there`, said.includes(`A key is set, from ${words}. Change it there.`), said);
      check(`from ${source}: no key field and no Remove key`, (await page.locator('#tvdb-key').count()) === 0 && (await card(page).locator('button:has-text("Remove key")').count()) === 0);
      check(`from ${source}: the PIN can still be set`, (await page.locator('#tvdb-pin').count()) === 1);
      await context.close();
    }
  },

  // Mutant caught: the Test button calling the wrong provider (TVDB's card testing TMDB's key, or the
  // other way round); a failed test shown as a success.
  async 'Test: each card tests its own key, and says how it went'(browser) {
    const { context, page, calls } = await open(browser, {
      settings: { tvdbKeySet: true, tvdbKeySource: 'settings' },
      test: (provider, n) => (provider === 'tvdb' && n === 2 ? { ok: false, error: 'TVDB rejected the key or PIN' } : { ok: true }),
    });
    await page.goto(`${base}/#/settings`);
    await card(page).waitFor();
    const tvdbTest = card(page).locator('button[aria-label="Test the TVDB key"]');
    await tvdbTest.click();
    const tvdbResult = card(page).locator('.test [role="status"]');
    await page.waitForFunction(() => document.querySelector('.card.tvdb .test [role="status"]')?.textContent?.trim());
    check('TVDB Test sends provider tvdb', JSON.stringify(lastCall(calls, 'POST', '/api/settings/test')?.body) === JSON.stringify({ provider: 'tvdb' }));
    check('it works: said so', (await text(tvdbResult)) === 'The TVDB key works.', await text(tvdbResult));
    await tvdbTest.click();
    await page.waitForFunction(() => document.querySelector('.card.tvdb .test [role="status"]')?.textContent?.includes('rejected'));
    check('it fails: the reason, as an error', (await text(tvdbResult)) === 'TVDB rejected the key or PIN' && (await tvdbResult.evaluate((e) => e.classList.contains('error'))));
    const tmdbTest = page.locator('button[aria-label="Test the TMDB key"]');
    await tmdbTest.click();
    await sleep(300);
    check('TMDB Test sends provider tmdb', JSON.stringify(lastCall(calls, 'POST', '/api/settings/test')?.body) === JSON.stringify({ provider: 'tmdb' }));
    check('the TMDB result is in the TMDB card', (await text(page.locator('.key-test [role="status"]'))) === 'The TMDB key works.');
    await context.close();
    const off = await open(browser, { settings: { tvdbKeySet: false } });
    await off.page.goto(`${base}/#/settings`);
    await card(off.page).waitFor();
    check('no TVDB key: its Test is disabled', await card(off.page).locator('button[aria-label="Test the TVDB key"]').isDisabled());
    check('no TVDB key: it says why', (await text(card(off.page).locator('.test .why'))) === 'Set a key to test it.');
    await off.context.close();
  },

  // Mutants caught: the result kept when the settings change (a red result after a good key is
  // saved, "works" after Remove); Test enabled while a save runs, or with lookups off.
  async 'Test: no stale result, and no Test while it can’t work'(browser) {
    const { context, page } = await open(browser, {
      settings: { tvdbKeySet: true, tvdbKeySource: 'settings' },
      test: (provider) => (provider === 'tvdb' ? { ok: false, error: 'TVDB rejected the key or PIN' } : { ok: true }),
      putDelay: 600,
    });
    await page.goto(`${base}/#/settings`);
    await card(page).waitFor();
    const result = card(page).locator('.test [role="status"]');
    const tvdbTest = card(page).locator('button[aria-label="Test the TVDB key"]');
    await tvdbTest.click();
    await page.waitForFunction(() => document.querySelector('.card.tvdb .test [role="status"]')?.textContent?.includes('rejected'));
    check('a failed test is shown', (await text(result)) === 'TVDB rejected the key or PIN');
    await page.fill('#tvdb-key', 'fake-good-key');
    await card(page).locator('button:has-text("Save")').click();
    await sleep(150);
    check('while saving: Test is off', await tvdbTest.isDisabled());
    await card(page).locator('p[role="status"].ok').waitFor();
    await sleep(300);
    check('a new key saved: the old result is gone', (await text(result)) === '', await text(result));
    check('and Test is on again', await tvdbTest.isEnabled());
    // TMDB: "works", then Remove: gone.
    const tmdbTest = page.locator('button[aria-label="Test the TMDB key"]');
    await tmdbTest.click();
    await page.waitForFunction(() => document.querySelector('.key-test [role="status"]')?.textContent?.includes('works'));
    await page.locator('form.key button:has-text("Remove")').click();
    await page.locator('form.key button:has-text("Remove")').waitFor({ state: 'detached' });
    await sleep(300);
    check('TMDB key removed: "works" is gone', (await text(page.locator('.key-test [role="status"]'))) === '');
    check('and its Test is off, saying why', (await tmdbTest.isDisabled()) && (await text(page.locator('.key-test .why'))) === 'Set a key to test it.');
    await context.close();

    // Lookups off: nothing can be asked, so no Test, and the reason. Positive control: the same keys with lookups on.
    for (const [name, over, off] of [
      ['lookups off', { metadataEnabled: false }, true],
      ['forced off', { metadataEnabled: false, metadataForcedOff: true }, true],
      ['lookups on', {}, false],
    ]) {
      const o = await open(browser, { settings: { tvdbKeySet: true, tvdbKeySource: 'settings', ...over } });
      await o.page.goto(`${base}/#/settings`);
      await card(o.page).waitFor();
      const both = [o.page.locator('button[aria-label="Test the TMDB key"]'), card(o.page).locator('button[aria-label="Test the TVDB key"]')];
      const disabled = await Promise.all(both.map((b) => b.isDisabled()));
      check(`${name}: Test is ${off ? 'off in both cards' : 'on in both cards'}`, disabled.every((d) => d === off), JSON.stringify(disabled));
      if (off) check(`${name}: it says why`, (await text(card(o.page).locator('.test .why'))) === 'Online lookups are off.');
      await o.context.close();
    }
  },

  // Mutant caught: no refetch of the status after a Test that works (the server clears "TVDB rejected
  // the key or PIN" at once, and the line would keep saying it).
  async 'Test: a key that works brings the status line up to date'(browser) {
    let cleared = false;
    const { context, page, hits } = await open(browser, {
      settings: { tvdbKeySet: true, tvdbKeySource: 'settings' },
      status: () => status({ showsLine: cleared ? 'Shows: TVDB' : 'TVDB rejected the key or PIN' }),
      test: (provider, n) => {
        if (n === 1) return { ok: false, error: 'TVDB rejected the key or PIN' };
        cleared = true;
        return { ok: true };
      },
    });
    await page.goto(`${base}/#/settings`);
    const line = page.locator('.status .shows-line');
    await line.getByText('TVDB rejected the key or PIN').waitFor();
    const tvdbTest = card(page).locator('button[aria-label="Test the TVDB key"]');
    await tvdbTest.click();
    await page.waitForFunction(() => document.querySelector('.card.tvdb .test [role="status"]')?.textContent?.includes('rejected'));
    await sleep(300);
    check('a failed Test: the status is not asked for again', hits['/api/metadata/status'] === 1, `${hits['/api/metadata/status']}x`);
    await tvdbTest.click();
    await line.getByText('Shows: TVDB').waitFor({ timeout: 3000 }).catch(() => {});
    check('a Test that works: the status line is fetched again', (await text(line)) === 'Shows: TVDB', `${await text(line)}, asked ${hits['/api/metadata/status']}x`);
    check('and the Test result stays', (await text(card(page).locator('.test [role="status"]'))) === 'The TVDB key works.');
    await context.close();
  },

  // Mutant caught: an answer still on its way kept when the key changes (no `asked++` in the reset):
  // a Test of the old key, answered after a new key is saved, would be shown against the new one.
  async 'Test: an answer about the old key never shows'(browser) {
    const { context, page } = await open(browser, {
      settings: { tvdbKeySet: true, tvdbKeySource: 'settings' },
      testDelay: (provider, n) => (n === 1 ? 1500 : 0),
    });
    await page.goto(`${base}/#/settings`);
    await card(page).waitFor();
    const result = card(page).locator('.test [role="status"]');
    await card(page).locator('button[aria-label="Test the TVDB key"]').click(); // answered in 1.5 s
    await sleep(200);
    await page.fill('#tvdb-key', 'fake-new-key');
    await card(page).locator('button:has-text("Save")').click();
    await card(page).locator('p[role="status"].ok').waitFor();
    await sleep(2000); // the old answer has come by now
    check('the old key’s answer is not shown', (await text(result)) === '', await text(result));
    // Positive control: a Test of the key there is now is shown.
    await card(page).locator('button[aria-label="Test the TVDB key"]').click();
    await page.waitForFunction(() => document.querySelector('.card.tvdb .test [role="status"]')?.textContent?.includes('works'), null, { timeout: 3000 }).catch(() => {});
    check('a Test of the new key is shown', (await text(result)) === 'The TVDB key works.');
    await context.close();
  },

  // Mutant caught: the result cleared by any new copy of the settings (a scan or a matching pass
  // ending fetches them again, unchanged), not only by a change the Test depends on.
  async 'Test: the same settings fetched again keep the result'(browser) {
    // The pass ends when the test says so, not at the nth status request: the page may ask for the
    // status more than once as it starts, and a pass that ended then would refetch the settings
    // before the test counts them (the flake this replaced: "2x, then 2x").
    let ended = false;
    const { context, page, hits } = await open(browser, {
      settings: { tvdbKeySet: true, tvdbKeySource: 'settings' },
      status: () => (ended ? status() : status({ running: true, pending: 2 })),
    });
    await page.clock.install();
    await page.goto(`${base}/#/settings`);
    await card(page).waitFor();
    const result = card(page).locator('.test [role="status"]');
    await sleep(1000); // the page settles (it may fetch the settings again as it starts)
    await card(page).locator('button[aria-label="Test the TVDB key"]').click();
    await page.waitForFunction(() => document.querySelector('.card.tvdb .test [role="status"]')?.textContent?.includes('works'));
    const before = hits['/api/settings'];
    ended = true; // the pass ends: at its next poll the page fetches the settings again
    const refetched = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/settings' && r.request().method() === 'GET', { timeout: 10000 });
    await page.clock.runFor(3000);
    await refetched.catch(() => {});
    await sleep(100); // the page applies the answer
    check('the settings were fetched again', hits['/api/settings'] > before, `${before}x, then ${hits['/api/settings']}x`);
    check('the result is still there', (await text(result)) === 'The TVDB key works.', await text(result));
    await context.close();
  },

  // Mutant caught: a key or PIN field a password manager may fill (with ply's own token, which the
  // sign-in prompt saves as current-password; Save would send it to TVDB or TMDB).
  async 'Key and PIN fields: password managers stay out'(browser) {
    const { context, page } = await open(browser);
    await page.goto(`${base}/#/settings`);
    await card(page).waitFor();
    for (const id of ['tmdb-key', 'tvdb-key', 'tvdb-pin']) {
      const attrs = await page.locator(`#${id}`).evaluate((el) => ({
        type: el.getAttribute('type'),
        autocomplete: el.getAttribute('autocomplete'),
        onePassword: el.hasAttribute('data-1p-ignore'),
        lastPass: el.getAttribute('data-lpignore'),
        bitwarden: el.hasAttribute('data-bwignore'),
      }));
      check(
        `#${id}: new-password and the managers' opt-outs`,
        attrs.type === 'password' && attrs.autocomplete === 'new-password' && attrs.onePassword && attrs.lastPass === 'true' && attrs.bitwarden,
        JSON.stringify(attrs),
      );
    }
    await context.close();
  },

  // Mutant caught: isMatching without the TVDB case (nothing polls while shows match on TVDB alone),
  // or with pending (the movies that wait for TMDB would make it poll for ever).
  async 'Status: with only a TVDB key, it polls while a pass runs, and stops'(browser) {
    const tvdbOnly = { keySet: false, tvdbKeySet: true, pending: 4 }; // 4 movies wait for a TMDB key
    const { context, page, hits } = await open(browser, {
      status: (n) => (n <= 2 ? status({ ...tvdbOnly, running: true }) : status(tvdbOnly)),
    });
    await page.clock.install();
    await page.goto(`${base}/#/settings`);
    const line = page.locator('.status p').first();
    await line.getByText('Looking up shows on TVDB…').waitFor();
    await page.clock.runFor(3000);
    await sleep(200);
    await page.clock.runFor(3000);
    await line.getByText('Shows are looked up on TVDB.').waitFor();
    check('the status was asked for again while the pass ran', hits['/api/metadata/status'] >= 3, `asked ${hits['/api/metadata/status']}x`);
    await sleep(300);
    const asked = hits['/api/metadata/status'];
    await page.clock.runFor(20000);
    await sleep(300);
    check('then it stops, though movies are pending', hits['/api/metadata/status'] === asked, `asked ${asked}x, then ${hits['/api/metadata/status']}x`);
    await context.close();
  },

  // Mutant caught: the attribution missing, reworded, or without its link to thetvdb.com.
  async 'TVDB card: the attribution TVDB asks for, as the server gives it'(browser) {
    const { context, page } = await open(browser);
    await page.goto(`${base}/#/settings`);
    await card(page).waitFor();
    const note = card(page).locator('.attribution');
    check('the attribution is there', (await note.count()) === 1);
    const said = await text(note);
    check('word for word', said.startsWith(ATTRIBUTION), said);
    check('it links to tvdbAttributionUrl', (await note.locator(`a[href="${ATTRIBUTION_URL}"]`).count()) === 1);
    // Positive control for the text coming from the server: another text is shown as it is.
    await context.close();
    const other = await open(browser, { settings: { tvdbAttribution: 'Fake notice from the server.', tvdbAttributionUrl: 'https://thetvdb.com/' } });
    await other.page.goto(`${base}/#/settings`);
    await card(other.page).waitFor();
    check('the server’s text, not one of the page’s', (await text(card(other.page).locator('.attribution'))).startsWith('Fake notice from the server.'));
    await other.context.close();
  },

  // Mutants caught: showsLine not shown (or reworded); the "No TMDB key" sentence that ignores
  // tvdbKeySet, and Refresh all details off when only TVDB has a key.
  async 'Status: showsLine as given, and a TVDB key counts as a key'(browser) {
    for (const line of ['Shows: TVDB', 'Shows: TMDB (no TVDB key)', 'TVDB rejected the key or PIN']) {
      const { context, page } = await open(browser, { status: { showsLine: line } });
      await page.goto(`${base}/#/settings`);
      const el = page.locator('.status .shows-line');
      await el.waitFor();
      check(`"${line}" is shown as it is`, (await text(el)) === line, await text(el));
      await context.close();
    }
    const cases = [
      ['TVDB only', { keySet: false, tvdbKeySet: true }, /^No TMDB key, so movies wait for one\. Shows are looked up on TVDB\./, true],
      ['no key at all', { keySet: false, tvdbKeySet: false, shows: '', showsLine: '' }, /^No TMDB key and no TVDB key/, false],
      ['both keys', { keySet: true, tvdbKeySet: true }, /^Everything has been looked up/, true], // positive control
    ];
    for (const [name, st, want, refresh] of cases) {
      const { context, page } = await open(browser, { status: st });
      await page.goto(`${base}/#/settings`);
      const line = page.locator('.status p').first();
      await line.waitFor();
      check(`${name}: what it says`, want.test(await text(line)), await text(line));
      check(`${name}: Refresh all details is ${refresh ? 'on' : 'off'}`, (await page.locator('button:has-text("Refresh all details")').isEnabled()) === refresh);
      await context.close();
    }
  },

  // Mutants caught: the wrong PATCH field ({order}, or the label "DVD" for "dvd"); the unmapped note
  // missing; a 409 replaced by a generic message; a 502 shown raw; the picker on a TMDB show.
  async 'Show: the episode order picker'(browser) {
    const { context, page, calls, hits } = await open(browser, {
      patch: (body, n) =>
        n === 1
          ? [200, titleDetail({ source: 'tvdb', episodeOrder: body.episodeOrder, unmapped: 2 })]
          : n === 2
            ? [409, { error: 'TVDB has no episodes for this show in that order' }]
            : [502, { error: 'could not reach TVDB: dial tcp: i/o timeout' }],
    });
    await page.goto(`${base}/#/shows/${encodeURIComponent(SHOW)}`);
    const select = page.locator('#episode-order');
    await select.waitFor();
    check('it is labelled', (await text(page.locator('label[for="episode-order"]'))) === 'Episode order');
    check('it starts at the show’s order', (await select.inputValue()) === 'aired');
    check('the choices', (await select.locator('option').allTextContents()).join('|') === 'Aired|DVD|Absolute');
    const shows = hits[`/api/shows/${encodeURIComponent(SHOW)}`] ?? 0;
    await select.selectOption('dvd');
    await page.locator('.order-msg[role="status"]').waitFor();
    const patch = lastCall(calls, 'PATCH', `/api/titles/${TITLE_ID}`);
    check('PATCH /api/titles/77 {episodeOrder: "dvd"}', JSON.stringify(patch?.body) === JSON.stringify({ episodeOrder: 'dvd' }), JSON.stringify(patch?.body));
    check('the unmapped note', (await text(page.locator('.order-msg[role="status"]'))) === '2 episodes don’t fit this order.');
    await sleep(300);
    check('the page is fetched again', (hits[`/api/shows/${encodeURIComponent(SHOW)}`] ?? 0) > shows, `${shows} then ${hits[`/api/shows/${encodeURIComponent(SHOW)}`]}`);
    check('it now reads DVD', (await select.inputValue()) === 'dvd');

    await select.selectOption('absolute');
    await page.locator('.order-msg[role="alert"]').waitFor();
    check('a 409: the server’s words', (await text(page.locator('.order-msg[role="alert"]'))) === 'TVDB has no episodes for this show in that order');
    check('a 409: the picker goes back to the order the show has', (await select.inputValue()) === 'dvd');
    check('a 409: the unmapped note is gone', (await page.locator('.order-msg[role="status"]').count()) === 0);

    await select.selectOption('aired');
    await page.waitForFunction(() => document.querySelector('.order-msg[role="alert"]')?.textContent?.includes('reached'));
    check('a 502: TVDB can’t be reached', (await text(page.locator('.order-msg[role="alert"]'))) === 'TVDB can’t be reached right now.');
    if (shots) {
      await page.locator('.order').scrollIntoViewIfNeeded();
      await page.screenshot({ path: join(shots, 'show-episode-order.png') });
    }
    await context.close();

    // Positive control for the absence: a show from TMDB has no order to change.
    const tmdb = await open(browser, { source: 'tmdb' });
    await tmdb.page.goto(`${base}/#/shows/${encodeURIComponent(SHOW)}`);
    await tmdb.page.locator('.meta .badge.source').waitFor();
    check('a TMDB show: no picker', (await tmdb.page.locator('#episode-order').count()) === 0);
    await tmdb.context.close();

    if (shots) {
      const narrow = await open(browser, { width: 360, patch: (body) => [200, titleDetail({ source: 'tvdb', episodeOrder: body.episodeOrder, unmapped: 1 })] });
      await narrow.page.goto(`${base}/#/shows/${encodeURIComponent(SHOW)}`);
      await narrow.page.locator('#episode-order').selectOption('absolute');
      await narrow.page.locator('.order-msg').waitFor();
      await narrow.page.locator('.order').scrollIntoViewIfNeeded();
      await narrow.page.screenshot({ path: join(shots, 'show-episode-order-360.png') });
      await narrow.context.close();
    }
  },

  // Mutants caught: a TVDB result sent as tmdbId; a typed "tvdb:81189" sent as anything but
  // {tvdb: "tvdb:81189"}; the results without their source badge.
  async 'Fix match: TVDB references and results'(browser) {
    const { context, page, calls } = await open(browser);
    await page.goto(`${base}/#/shows/${encodeURIComponent(SHOW)}`);
    await page.click('[aria-haspopup="menu"]');
    await page.click('[role="menuitem"]:has-text("Fix the match")');
    await page.waitForSelector('dialog[open] .result');
    const rows = dialog(page).locator('.result');
    const badges = await rows.locator('.badge.source').allTextContents();
    check('each result says where it is from, TVDB first', badges.join('|') === 'TVDB|TMDB', badges.join('|'));
    const matchPath = `/api/shows/${encodeURIComponent(SHOW)}/match`;
    await rows.nth(0).click();
    await sleep(250);
    check('the TVDB result sends {tvdbId}', JSON.stringify(lastCall(calls, 'PUT', matchPath)?.body) === JSON.stringify({ tvdbId: 81189 }), JSON.stringify(lastCall(calls, 'PUT', matchPath)?.body));

    await page.click('[aria-haspopup="menu"]');
    await page.click('[role="menuitem"]:has-text("Fix the match")');
    await page.waitForSelector('dialog[open] .result');
    await rows.nth(1).click();
    await sleep(250);
    check('the TMDB result still sends {tmdbId}', JSON.stringify(lastCall(calls, 'PUT', matchPath)?.body) === JSON.stringify({ tmdbId: 1396 }));

    await page.click('[aria-haspopup="menu"]');
    await page.click('[role="menuitem"]:has-text("Fix the match")');
    await dialog(page).waitFor();
    const box = dialog(page).locator('input[type="search"]');
    check('the box offers TVDB links for a show', /TVDB/.test((await box.getAttribute('placeholder')) ?? ''));
    await box.fill('tvdb:81189');
    check('it says what was typed', (await text(dialog(page).locator('.pasted p'))) === 'TVDB show 81189');
    await dialog(page).locator('button:has-text("Use this show")').click();
    await sleep(250);
    check('"tvdb:81189" sends {tvdb: "tvdb:81189"}', JSON.stringify(lastCall(calls, 'PUT', matchPath)?.body) === JSON.stringify({ tvdb: 'tvdb:81189' }), JSON.stringify(lastCall(calls, 'PUT', matchPath)?.body));

    await page.click('[aria-haspopup="menu"]');
    await page.click('[role="menuitem"]:has-text("Fix the match")');
    await dialog(page).waitFor();
    await box.fill('https://thetvdb.com/series/breaking-bad');
    await dialog(page).locator('button:has-text("Use this show")').click();
    await sleep(250);
    check('a thetvdb.com address is sent as typed', JSON.stringify(lastCall(calls, 'PUT', matchPath)?.body) === JSON.stringify({ tvdb: 'https://thetvdb.com/series/breaking-bad' }));
    await context.close();

    // A film's dialog: TVDB has no films.
    const movie = await open(browser);
    await movie.page.goto(`${base}/#/item/1`);
    await movie.page.click('[aria-haspopup="menu"]');
    await movie.page.click('[role="menuitem"]:has-text("Fix the match")');
    await dialog(movie.page).waitFor();
    await dialog(movie.page).locator('input[type="search"]').fill('tvdb:81189');
    check('in a film’s dialog, a TVDB id is refused', (await dialog(movie.page).locator('text=TVDB has shows only').count()) === 1);
    check('nothing to use', (await dialog(movie.page).locator('text=Use this film').count()) === 0);
    await movie.context.close();
  },

  // Mutant caught: the badge left out, or the labels mixed up.
  async 'Source badges on the Item and Show pages'(browser) {
    for (const [source, want] of [
      ['tmdb', 'TMDB'],
      ['nfo', 'From .nfo'],
      ['tvdb', 'TVDB'],
    ]) {
      const { context, page } = await open(browser, { itemSource: source });
      await page.goto(`${base}/#/item/1`);
      const badge = page.locator('.meta .badge.source');
      await badge.waitFor();
      check(`item (${source}): "${want}"`, (await text(badge)) === want, await text(badge));
      await context.close();
    }
    const { context, page } = await open(browser);
    await page.goto(`${base}/#/shows/${encodeURIComponent(SHOW)}`);
    const badge = page.locator('.meta .badge.source');
    await badge.waitFor();
    check('show from TVDB: "TVDB"', (await text(badge)) === 'TVDB');
    await context.close();
  },

  // Mutants caught: every 404 read as a merge (or none); the merged answer's `into` not followed;
  // the address not following an answer for another id.
  async 'People: a merged person, and one who isn’t there'(browser) {
    const { context, page, hits } = await open(browser);
    await page.goto(`${base}/#/person/5`); // the server: 404 {"error": "merged", "into": 8}
    await page.waitForFunction(() => location.hash === '#/person/8', null, { timeout: 3000 }).catch(() => {});
    check('a merged person: the address follows `into`', (await page.evaluate(() => location.hash)) === '#/person/8', await page.evaluate(() => location.hash));
    await page.locator('h1:has-text("Bryan Cranston")').waitFor({ timeout: 3000 }).catch(() => {});
    check('and the person it was merged into is shown', (await page.locator('h1:has-text("Bryan Cranston")').count()) === 1 && (hits['/api/people/8'] ?? 0) >= 1);

    await page.goto(`${base}/#/person/6`); // a plain 404
    await page.locator('text=This person isn’t in the library.').waitFor({ timeout: 3000 }).catch(() => {});
    check('a plain 404: not in the library', (await page.locator('text=This person isn’t in the library.').count()) === 1);
    check('a plain 404: not called a merge', (await page.locator('text=merged').count()) === 0);
    check('with a way to search', (await page.locator('button:has-text("Search for them")').count()) === 1);
    await page.locator('button:has-text("Search for them")').click();
    check('which opens the search', (await page.locator('dialog[open], [role="dialog"]').count()) >= 1);
    await page.keyboard.press('Escape');

    // An answer for another id (merged by this very request) moves the address too.
    await page.goto(`${base}/#/person/7`);
    await page.waitForFunction(() => location.hash === '#/person/8', null, { timeout: 3000 }).catch(() => {});
    check('the address follows the answer', (await page.evaluate(() => location.hash)) === '#/person/8', await page.evaluate(() => location.hash));
    await context.close();
  },

  // Mutants caught: "TVDB" anywhere in a 409 read as "TVDB is not set up" (the server's lookups-off
  // text names TVDB too); "TVDB refused the key" (a 502) said as "could not be reached".
  async 'Fix match: the server’s error texts'(browser) {
    const answers = [
      [409, { error: 'online metadata lookups are off: set a TMDB or TVDB key and turn them on in the settings' }],
      [502, { error: 'TVDB refused the key' }],
      [502, { error: 'could not reach TVDB: dial tcp: i/o timeout' }],
      [502, { error: 'TMDB refused the key' }],
    ];
    const { context, page } = await open(browser, {
      search: (n) => answers[n - 1] ?? [200, CANDIDATES],
      match: () => [409, { error: 'TheTVDB is not set up: add a TVDB key in the settings' }],
    });
    await page.goto(`${base}/#/shows/${encodeURIComponent(SHOW)}`);
    await page.click('[aria-haspopup="menu"]');
    await page.click('[role="menuitem"]:has-text("Fix the match")');
    const err = dialog(page).locator('.error');
    const want = [
      'Online lookups are off, so there is nothing to search.',
      'TVDB refused the key. Check it in the settings.',
      'TVDB could not be reached. Try again in a moment.',
      'TMDB refused the key. Check it in the settings.',
    ];
    for (let i = 0; i < want.length; i++) {
      if (i) await dialog(page).locator('button:has-text("Search")').click();
      await page.waitForFunction((w) => document.querySelector('dialog[open] .error')?.textContent?.includes(w), want[i], { timeout: 3000 }).catch(() => {});
      const said = await text(err);
      check(`${answers[i][0]} "${answers[i][1].error}": ${want[i]}`, said.startsWith(want[i]), said);
    }
    check('a refused key: the settings are offered', (await dialog(page).locator('.error a:has-text("Open settings")').count()) === 1);
    // TVDB not set up (the server's 409 for tvdb:… with no TVDB key) is said as the server says it.
    await dialog(page).locator('input[type="search"]').fill('tvdb:81189');
    await dialog(page).locator('button:has-text("Use this show")').click();
    await page.waitForFunction(() => document.querySelector('dialog[open] .error')?.textContent?.includes('not set up'), null, { timeout: 3000 }).catch(() => {});
    check('409 TVDB not set up: as the server says', (await text(err)).startsWith('TheTVDB is not set up: add a TVDB key in the settings'), await text(err));
    await context.close();
  },

  // Mutant caught: the order kept from a PATCH outliving a new match. The server keeps the title's
  // row and resets its order to aired; the picker must say so, and DVD must be sendable again.
  async 'Show: a new match starts the picker from the server’s order'(browser) {
    const { context, page, calls } = await open(browser);
    await page.goto(`${base}/#/shows/${encodeURIComponent(SHOW)}`);
    const select = page.locator('#episode-order');
    await select.waitFor();
    await select.selectOption('dvd');
    await page.waitForFunction(() => !document.querySelector('#episode-order')?.disabled);
    await sleep(300);
    check('DVD to start with', (await select.inputValue()) === 'dvd');
    const fix = async (typed) => {
      await page.click('[aria-haspopup="menu"]');
      await page.click('[role="menuitem"]:has-text("Fix the match")');
      await dialog(page).waitFor();
      await dialog(page).locator('input[type="search"]').fill(typed);
      await dialog(page).locator('button:has-text("Use this show")').click();
      await dialog(page).waitFor({ state: 'detached' });
      await sleep(400);
    };
    await fix('tvdb:12345'); // another TVDB show
    check('another TVDB show: the picker says Aired', (await select.inputValue()) === 'aired', await select.inputValue());
    await select.selectOption('dvd');
    await sleep(300);
    const patches = calls.filter((c) => c.method === 'PATCH').map((c) => c.body.episodeOrder);
    check('and DVD can be chosen again', patches.join(',') === 'dvd,dvd', patches.join(','));
    await page.waitForFunction(() => !document.querySelector('#episode-order')?.disabled);

    // TMDB and back: the same TVDB id and the same title id as before the trip.
    await fix('https://www.themoviedb.org/tv/1396-breaking-bad');
    check('matched at TMDB: no picker', (await page.locator('#episode-order').count()) === 0);
    await fix('tvdb:12345');
    await select.waitFor();
    check('back at TVDB: the picker says Aired', (await select.inputValue()) === 'aired', await select.inputValue());
    await context.close();
  },

  // Mutant caught: the picker's order read from the page only, so picking the old order again before
  // the page is fetched again is taken for no change, and nothing is sent.
  async 'Show: picking the order back before the page catches up'(browser) {
    const { context, page, calls } = await open(browser, { showDelay: (n) => (n >= 2 ? 1500 : 0) });
    await page.goto(`${base}/#/shows/${encodeURIComponent(SHOW)}`);
    const select = page.locator('#episode-order');
    await select.waitFor();
    await select.selectOption('dvd');
    await page.waitForFunction(() => !document.querySelector('#episode-order')?.disabled);
    await select.selectOption('aired'); // the page still says "aired": its fetch takes 1.5 s
    await sleep(400);
    const patches = calls.filter((c) => c.method === 'PATCH').map((c) => c.body.episodeOrder);
    check('both changes were sent', patches.join(',') === 'dvd,aired', patches.join(','));
    await sleep(2000);
    check('and the picker says aired after the page is fetched again', (await select.inputValue()) === 'aired');
    await context.close();
  },

  // Not a check of behaviour: the TVDB card as a person sees it, wide and at 360 px.
  async 'Screenshots: the TVDB card'(browser) {
    if (!shots) return;
    for (const width of [1280, 360]) {
      const { context, page } = await open(browser, {
        width,
        settings: { tvdbKeySet: true, tvdbKeySource: 'settings', tvdbPinSet: true },
        test: () => ({ ok: false, error: 'TVDB rejected the key or PIN' }),
      });
      await page.goto(`${base}/#/settings`);
      await card(page).waitFor();
      await card(page).locator('button[aria-label="Test the TVDB key"]').click();
      await sleep(300);
      await page.locator('#tvdb-pin').focus();
      await card(page).screenshot({ path: join(shots, `settings-tvdb-card-${width}.png`) });
      await page.locator('.card.status').screenshot({ path: join(shots, `settings-status-${width}.png`) });
      await page.locator('.card').first().screenshot({ path: join(shots, `settings-tmdb-card-${width}.png`) });
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth);
      check(`${width} px: nothing wider than the screen`, !overflow);
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
