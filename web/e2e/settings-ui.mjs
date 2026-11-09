// Settings in tabs, in a real browser, against a mocked API: the tabs (click, keyboard, ARIA), the
// tab in the URL (each tab's deep link, the player's older ?s=playback link, no history entries on
// a switch), only the open tab's requests, the API keys (a set key is a mask, never in the page; an
// empty save keeps the key; Remove; Test and its "last tested" line), and Settings and Activity
// centred on a wide screen. lib/settings-tabs.test.ts pins the parsing; this pins the page.
//
// Self-contained: serves web/dist (build it first: `npm run build`; PLY_E2E_DIST picks another
// directory) and answers every /api/ request with page.route. Needs Google Chrome.
//
//   npm run build && node e2e/settings-ui.mjs
//
// PLY_E2E_ONLY=<text> runs the scenarios whose name contains it. Each scenario names the mutation it
// was confirmed to catch (build the mutant into another directory with
// `vite build --outDir ... --emptyOutDir` and point PLY_E2E_DIST at it).
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

// Fake keys, as the server holds them. The page is never sent them: it must not show one either.
const TMDB_KEY = 'fake-tmdb-key-6c1f0e';
const TVDB_KEY = 'fake-tvdb-key-93ab42';
const NEW_KEY = 'fake-new-key-77d0aa';
const MASK = '••••••••';
const SOURCES = ['imdb', 'rt_critics', 'rt_audience', 'metacritic', 'letterboxd', 'trakt', 'tmdb'];
const now = () => Math.floor(Date.now() / 1000);

/** What GET /api/settings says of the server's state: whether each key is set, never the key. */
function view(st) {
  const keyed = (k) => (st.keys[k] ? { set: true, source: 'settings' } : { set: false, source: '' });
  const t = keyed('tmdb');
  const v = keyed('tvdb');
  const m = keyed('mdblist');
  return {
    tmdbKeySet: t.set, tmdbKeySource: t.source, metadataEnabled: true, metadataForcedOff: false, language: 'en-US',
    maxStreams: 4, maxTranscodes: 1, maxStreamsSource: 'default', maxTranscodesSource: 'default',
    tvdbKeySet: v.set, tvdbKeySource: v.source, tvdbPinSet: false, tvdbPinSource: '',
    tvdbAttribution: 'Metadata provided by TheTVDB. Please consider adding missing information or subscribing.', tvdbAttributionUrl: 'https://thetvdb.com',
    mdblistKeySet: m.set, mdblistKeySource: m.source, ratingsPrimary: 'imdb', ratingsShown: [...SOURCES], ratingsAvailable: ['imdb', 'tmdb'],
    imdbAttribution: 'Information courtesy of IMDb (https://www.imdb.com). Used with permission.', imdbAttributionUrl: 'https://www.imdb.com',
    mdblistAttribution: 'Ratings from Rotten Tomatoes, Metacritic, Letterboxd and Trakt provided by MDBList.', mdblistAttributionUrl: 'https://mdblist.com',
    imdbDatasetUpdatedAt: '2026-11-03T06:00:00Z',
    tmdbKeyTestedAt: st.tested.tmdb?.at ?? 0, tmdbKeyTestOk: st.tested.tmdb?.ok ?? false,
    tvdbKeyTestedAt: st.tested.tvdb?.at ?? 0, tvdbKeyTestOk: st.tested.tvdb?.ok ?? false,
    mdblistKeyTestedAt: st.tested.mdblist?.at ?? 0, mdblistKeyTestOk: st.tested.mdblist?.ok ?? false,
  };
}

const snapshot = { paused: false, running: [], queued: [], history: [], tier: { playback: { Sessions: 0, Active: 0, Transcodes: 0, Software: 0, Waiting: 0, Paths: null }, browsing: false, hold: { held: false } } };

/**
 * A fresh page on the mocked server.
 *   test     (provider) => json for POST /api/settings/test ({ ok: true } by default)
 *   keys     the keys the server starts with (TMDB and TVDB by default)
 *   items    what /api/items lists (none by default)
 *   status   fields over the default GET /api/metadata/status
 *   review   what /api/metadata/review lists (none by default)
 *   search   [status, json] for GET /api/metadata/search
 *   settingsFail  GET /api/settings answers 500
 *   width    the viewport's width (1280 by default)
 * `st` is the server's state (st.keys holds the keys themselves); `calls` lists the non-GET
 * requests; `gets` lists the GETs' paths, in order.
 */
async function open(browser, opts = {}) {
  const context = await browser.newContext({ viewport: { width: opts.width ?? 1280, height: 900 }, serviceWorkers: 'block' });
  const page = await context.newPage();
  const st = { keys: opts.keys ?? { tmdb: TMDB_KEY, tvdb: TVDB_KEY, mdblist: '' }, tested: { tmdb: { at: now() - 300, ok: true } } };
  const calls = [];
  const gets = [];
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  const json = (route, body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  await page.route('**/api/**', async (route) => {
    const req = route.request();
    const p = new URL(req.url()).pathname;
    const method = req.method();
    const body = req.postData() ? JSON.parse(req.postData()) : null;
    if (method === 'GET') gets.push(p);
    else calls.push({ method, path: p, body });
    if (p === '/api/info') return json(route, { id: 'x', name: 'e2e', version: 'dev', authRequired: false });
    if (p === '/api/scan') return json(route, { running: false, itemCount: 0, watching: [] });
    if (p === '/api/items') return routeItems(route, opts.items ?? []);
    if (['/api/continue', '/api/shows', '/api/upcoming', '/api/genres', '/api/sessions'].includes(p)) return json(route, []);
    if (p === '/api/folders') return json(route, [{ id: 1, path: '/Volumes/Media/Movies', kind: 'movies', itemCount: 12, missing: false }]);
    if (p === '/api/metadata/review') return json(route, opts.review ?? []);
    if (p === '/api/metadata/status')
      return json(route, { enabled: true, keySet: true, tvdbKeySet: true, running: false, pending: 0, unmatched: 0, needsReview: 0, lastError: '', showsLine: 'Shows: TVDB', ...opts.status });
    if (p === '/api/metadata/search' && opts.search) return json(route, opts.search[1], opts.search[0]);
    if (p === '/api/jobs') return json(route, snapshot);
    if (p === '/api/events') {
      const sse = `retry: 300\n\nid: 1\nevent: snapshot\ndata: ${JSON.stringify(snapshot)}\n\n`;
      return route.fulfill({ status: 200, headers: { 'content-type': 'text/event-stream', 'cache-control': 'no-store' }, body: sse });
    }
    if (p === '/api/settings' && method === 'GET') return opts.settingsFail ? json(route, { error: 'settings are not available' }, 500) : json(route, view(st));
    if (p === '/api/settings') {
      // As the server: "" removes a stored key; a new one is checked, and that check is a test.
      const out = {};
      for (const k of ['tmdb', 'tvdb', 'mdblist']) {
        const v = body?.[`${k}Key`];
        if (v === undefined) continue;
        st.keys[k] = v;
        st.tested[k] = v ? { at: now(), ok: true } : undefined;
        if (v) out[`${k}KeyChecked`] = true;
      }
      return json(route, { ...view(st), ...out });
    }
    if (p === '/api/settings/test') {
      const answer = opts.test?.(body?.provider) ?? { ok: true };
      st.tested[body?.provider] = { at: now(), ok: answer.ok };
      return json(route, answer);
    }
    return json(route, { error: `unmocked ${p}` }, 404);
  });
  return { context, page, st, calls, gets, errors };
}

const tab = (page, id) => page.locator(`#settings-tab-${id}`);
const selectedTab = (page) => page.locator('[role="tab"][aria-selected="true"]').getAttribute('id');
const hash = (page) => page.evaluate(() => location.hash);
const text = async (loc) => ((await loc.textContent({ timeout: 2000 }).catch(() => '')) ?? '').replace(/\s+/g, ' ').trim();
const settingsPuts = (calls) => calls.filter((c) => c.path === '/api/settings' && (c.method === 'PUT' || c.method === 'PATCH'));
async function until(fn, timeout = 3000) {
  const end = Date.now() + timeout;
  while (Date.now() < end) {
    if (await fn()) return true;
    await sleep(50);
  }
  return false;
}

// The heading each tab opens with, and the requests that are its own (the app's own, such as
// /api/scan and the activity feed, are made on every page).
const TABS = {
  library: { heading: '#folders-h', own: ['/api/folders', '/api/metadata/review', '/api/metadata/status'] },
  details: { heading: '#meta-h', own: ['/api/settings', '/api/metadata/status'] },
  ratings: { heading: '#ratings-h', own: ['/api/settings'] },
  playback: { heading: '#playback-h', own: ['/api/settings'] },
  keys: { heading: '#keys-h', own: ['/api/settings', '/api/metadata/status'] },
};
const TAB_PATHS = ['/api/settings', '/api/metadata/status', '/api/metadata/review', '/api/folders'];

const scenarios = {
  // Mutant caught: aria-selected or the roving tabindex left on the first tab; aria-controls
  // pointing at a panel that isn't there.
  async 'Tabs: roles, the selected tab, and its panel'(browser) {
    const { context, page } = await open(browser);
    await page.goto(`${base}/#/settings?tab=ratings`);
    await page.locator(TABS.ratings.heading).waitFor();
    const tabs = page.locator('[role="tablist"] [role="tab"]');
    check('a tablist named Settings, with five tabs', (await page.locator('[role="tablist"][aria-label="Settings"]').count()) === 1 && (await tabs.count()) === 5);
    const names = await tabs.allTextContents();
    check('the tabs, in order', names.map((n) => n.trim()).join('|') === 'Library|Details|Ratings|Playback|API keys', names.join('|'));
    const state = await tabs.evaluateAll((ts) => ts.map((t) => [t.id, t.getAttribute('aria-selected'), t.getAttribute('tabindex'), t.getAttribute('aria-controls')]));
    const sel = state.filter(([, s]) => s === 'true');
    check('one tab selected: Ratings', sel.length === 1 && sel[0][0] === 'settings-tab-ratings', JSON.stringify(state));
    check('only the selected tab is in the Tab order', state.every(([id, , ti]) => ti === (id === 'settings-tab-ratings' ? '0' : '-1')), JSON.stringify(state));
    const panel = page.locator('[role="tabpanel"]');
    check('one panel', (await panel.count()) === 1);
    const controls = sel[0][3];
    check('the tab controls the panel there is', !!controls && (await panel.getAttribute('id')) === controls, `${controls} vs ${await panel.getAttribute('id')}`);
    check('the panel is labelled by its tab', (await panel.getAttribute('aria-labelledby')) === 'settings-tab-ratings');
    check('the other tabs point at no missing panel', state.filter(([, s]) => s !== 'true').every(([, , , c]) => c === null));
    await context.close();
  },

  // Mutants caught: a tab switch through navigate() (a history entry per tab); the URL not
  // following the tab.
  async 'Tabs: a click switches, the URL follows, and no history entry is added'(browser) {
    const { context, page } = await open(browser);
    await page.goto(`${base}/#/`);
    await page.locator('header a[href="#/settings"]').click();
    await page.locator(TABS.library.heading).waitFor();
    const entries = await page.evaluate(() => history.length);
    for (const id of ['details', 'ratings', 'playback', 'keys', 'library']) {
      await tab(page, id).click();
      await page.locator(TABS[id].heading).waitFor({ timeout: 3000 }).catch(() => {});
      check(`${id}: selected`, (await selectedTab(page)) === `settings-tab-${id}`);
      const want = id === 'library' ? '#/settings' : `#/settings?tab=${id}`;
      check(`${id}: the URL says ${want}`, (await hash(page)) === want, await hash(page));
      check(`${id}: its panel`, (await page.locator(`[role="tabpanel"] ${TABS[id].heading}`).count()) === 1);
    }
    check('five switches: no new history entry', (await page.evaluate(() => history.length)) === entries, `${entries} then ${await page.evaluate(() => history.length)}`);
    // Back leaves Settings for the page before it, past every tab seen.
    await page.locator('main .back').click();
    await until(async () => (await hash(page)) === '#/');
    check('Back: to the page before Settings', (await hash(page)) === '#/', await hash(page));
    // Positive control: a link to another page does add one.
    await page.goto(`${base}/#/settings?tab=keys`);
    await page.locator(TABS.keys.heading).waitFor();
    const before = await page.evaluate(() => history.length);
    await page.locator('header nav a[href="#/movies"]').first().click();
    await until(async () => (await hash(page)) === '#/movies');
    check('positive control: a link to Movies adds an entry', (await page.evaluate(() => history.length)) === before + 1);
    await page.goBack();
    await page.locator(TABS.keys.heading).waitFor({ timeout: 3000 }).catch(() => {});
    check('and the browser’s Back returns to the tab it left', (await selectedTab(page)) === 'settings-tab-keys');
    await context.close();
  },

  // Mutants caught: no arrow keys (or the wrong way round), no Home and End, the focus left on the
  // tab that was.
  async 'Tabs: the keyboard'(browser) {
    const { context, page } = await open(browser);
    await page.goto(`${base}/#/settings`);
    await page.locator(TABS.library.heading).waitFor();
    const entries = await page.evaluate(() => history.length);
    await tab(page, 'library').focus();
    const focused = () => page.evaluate(() => document.activeElement?.id ?? '');
    const press = async (key, want) => {
      await page.keyboard.press(key);
      await page.locator(TABS[want].heading).waitFor({ timeout: 3000 }).catch(() => {});
      check(`${key}: ${want} selected, with the focus`, (await selectedTab(page)) === `settings-tab-${want}` && (await focused()) === `settings-tab-${want}`, `${await selectedTab(page)} / ${await focused()}`);
    };
    await press('ArrowDown', 'details');
    check('ArrowDown: the URL follows', (await hash(page)) === '#/settings?tab=details', await hash(page));
    await press('ArrowRight', 'ratings');
    await press('ArrowUp', 'details');
    await press('ArrowLeft', 'library');
    await press('ArrowUp', 'keys'); // from the first, round to the last
    await press('ArrowDown', 'library'); // and back
    await press('End', 'keys');
    await press('Home', 'library');
    check('keys: no new history entry either', (await page.evaluate(() => history.length)) === entries);
    // Tab leaves the tablist for the panel's first control: the other tabs, and the panel itself,
    // are not stops.
    await page.keyboard.press('Tab');
    const into = await page.evaluate(() => {
      const a = document.activeElement;
      return { inPanel: !!a?.closest('[role="tabpanel"]'), isPanel: a?.getAttribute('role') === 'tabpanel', tab: a?.getAttribute('role') === 'tab' };
    });
    check('Tab: from the tab into its panel, to a control in it', into.inPanel && !into.isPanel && !into.tab, JSON.stringify(into));
    await context.close();
  },

  // Mutants caught: an unknown tab, or the older link, not opening the right tab.
  async 'Deep links: each tab, and the older ?s=playback'(browser) {
    const { context, page } = await open(browser);
    for (const id of Object.keys(TABS)) {
      await page.goto(`${base}/#/settings${id === 'library' ? '' : `?tab=${id}`}`);
      await page.reload(); // a fresh load of that address
      await page.locator(TABS[id].heading).waitFor({ timeout: 3000 }).catch(() => {});
      check(`#/settings?tab=${id}: that tab`, (await selectedTab(page)) === `settings-tab-${id}` && (await page.locator(TABS[id].heading).count()) === 1, await selectedTab(page));
    }
    await page.goto(`${base}/#/settings?tab=nonsense`);
    await page.reload();
    await page.locator(TABS.library.heading).waitFor({ timeout: 3000 }).catch(() => {});
    check('an unknown tab: Library', (await selectedTab(page)) === 'settings-tab-library');
    // The player's "too many videos" panel links to #/settings?s=playback.
    await page.goto(`${base}/#/settings?s=playback`);
    await page.reload();
    await page.locator('#max-streams').waitFor({ timeout: 3000 }).catch(() => {});
    check('?s=playback: the Playback tab', (await selectedTab(page)) === 'settings-tab-playback');
    check('?s=playback: with the limits', (await page.getByLabel('Videos playing at once').count()) === 1);
    await context.close();
  },

  // Mutant caught: every panel mounted (hidden), so each tab asks for what the others show.
  async 'Requests: only the open tab’s'(browser) {
    for (const [id, { heading, own }] of Object.entries(TABS)) {
      const { context, page, gets } = await open(browser);
      await page.goto(`${base}/#/settings${id === 'library' ? '' : `?tab=${id}`}`);
      await page.locator(heading).waitFor();
      await sleep(600);
      const asked = TAB_PATHS.filter((p) => gets.includes(p));
      // Positive control: what the tab shows was asked for.
      check(`${id}: asks for what it shows`, own.every((p) => gets.includes(p)), asked.join(', '));
      check(`${id}: and nothing the other tabs show`, asked.every((p) => own.includes(p)), asked.join(', '));
      await context.close();
    }
    // A switch asks for the new tab's alone.
    const { context, page, gets } = await open(browser);
    await page.goto(`${base}/#/settings?tab=playback`);
    await page.locator(TABS.playback.heading).waitFor();
    await sleep(300);
    const from = gets.length;
    await tab(page, 'library').click();
    await page.locator(TABS.library.heading).waitFor();
    await sleep(600);
    const asked = [...new Set(gets.slice(from).filter((p) => TAB_PATHS.includes(p)))];
    check('Playback → Library: Library’s requests, no settings', asked.length > 0 && !asked.includes('/api/settings'), asked.join(', '));
    await context.close();
  },

  // Mutants caught: a key kept in the field after its save (the typed key stays in the page); the
  // mask missing for a key that is set.
  async 'API keys: a set key is a mask, and never in the page'(browser) {
    const { context, page, st } = await open(browser);
    await page.goto(`${base}/#/settings?tab=keys`);
    await page.locator('#tmdb-key').waitFor();
    await sleep(300);
    // Positive controls: the server has the keys, and the fields show that keys are set.
    check('the server holds the TMDB and TVDB keys', st.keys.tmdb === TMDB_KEY && st.keys.tvdb === TVDB_KEY);
    check('TMDB: the mask', (await page.locator('#tmdb-key').getAttribute('placeholder')) === MASK);
    check('TVDB: the mask', (await page.locator('#tvdb-key').getAttribute('placeholder')) === MASK);
    check('MDBList, with no key: no mask', (await page.locator('#mdblist-key').getAttribute('placeholder')) === 'Paste your key');
    check('the fields are empty', (await page.locator('#tmdb-key').inputValue()) === '' && (await page.locator('#tvdb-key').inputValue()) === '');
    /** Every piece of the page a key could be in: the markup, its attributes, the fields' values. */
    const pageHas = (secret) =>
      page.evaluate((k) => {
        const all = [document.documentElement.outerHTML, ...[...document.querySelectorAll('input, textarea')].map((e) => e.value)];
        for (const el of document.querySelectorAll('*')) for (const a of el.attributes) all.push(a.value);
        return all.some((s) => s.includes(k));
      }, secret);
    check('the TMDB key is nowhere in the page', !(await pageHas(TMDB_KEY)));
    check('nor the TVDB key', !(await pageHas(TVDB_KEY)));
    // A key typed and saved leaves the page with its save.
    await page.fill('#tmdb-key', NEW_KEY);
    check('positive control: the typed key is in the field', await pageHas(NEW_KEY));
    await page.locator('.card.tmdb button:has-text("Save key")').click();
    await page.locator('.card.tmdb p[role="status"].ok').waitFor();
    check('saved: the server has it', st.keys.tmdb === NEW_KEY);
    await sleep(300);
    check('saved: the page no longer has it', !(await pageHas(NEW_KEY)));
    check('saved: the mask again', (await page.locator('#tmdb-key').getAttribute('placeholder')) === MASK);
    await context.close();
  },

  // Mutants caught: a save with the field empty sent as tmdbKey "" (which removes the key); a Remove
  // that sends at the first click; Keep that removes, or loses the focus.
  async 'API keys: an empty save keeps the key; Remove removes it'(browser) {
    const { context, page, st, calls } = await open(browser);
    await page.goto(`${base}/#/settings?tab=keys`);
    const field = page.locator('#tmdb-key');
    await field.waitFor();
    await field.focus();
    await page.keyboard.press('Enter');
    await field.fill('   ');
    await page.keyboard.press('Enter');
    await page.locator('#tvdb-key').focus();
    await page.keyboard.press('Enter');
    await sleep(400);
    check('Enter in an empty field, or in spaces: nothing is sent', settingsPuts(calls).length === 0, JSON.stringify(settingsPuts(calls)));
    check('the keys are still there', st.keys.tmdb === TMDB_KEY && st.keys.tvdb === TVDB_KEY);
    check('Save is off with nothing to save', await page.locator('.card.tmdb button:has-text("Save key")').isDisabled());
    // Remove is the way to remove one, and it asks first.
    const card = page.locator('.card.tmdb');
    await card.locator('button:has-text("Remove key")').click();
    const ask = card.locator('.confirm-remove');
    await ask.waitFor();
    await sleep(300);
    check('Remove: it asks first', (await text(ask)).startsWith('Remove the TMDB key?'), await text(ask));
    check('Remove: nothing sent at the first click', settingsPuts(calls).length === 0, JSON.stringify(settingsPuts(calls)));
    check('Remove: the focus on Keep', await page.evaluate(() => document.activeElement?.textContent?.trim() === 'Keep'));
    await page.keyboard.press('Enter'); // Keep
    await sleep(300);
    check('Keep: nothing sent, the key kept', settingsPuts(calls).length === 0 && st.keys.tmdb === TMDB_KEY);
    check('Keep: the focus back on Remove key', await page.evaluate(() => document.activeElement?.textContent?.trim() === 'Remove key'));
    await card.locator('button:has-text("Remove key")').click();
    await ask.locator('button:has-text("Remove")').click();
    await card.locator('p[role="status"].ok').waitFor();
    check('Remove: tmdbKey "" is sent, alone', JSON.stringify(settingsPuts(calls).at(-1)?.body) === JSON.stringify({ tmdbKey: '' }), JSON.stringify(settingsPuts(calls).at(-1)?.body));
    check('Remove: the server has no TMDB key', st.keys.tmdb === '');
    await until(async () => (await field.getAttribute('placeholder')) !== MASK);
    check('Remove: no mask', (await field.getAttribute('placeholder')) === 'Paste your key', await field.getAttribute('placeholder'));
    check('Remove: the TVDB key is untouched', st.keys.tvdb === TVDB_KEY);
    await context.close();
    // The other two ask too.
    const o = await open(browser, { keys: { tmdb: TMDB_KEY, tvdb: TVDB_KEY, mdblist: 'fake-mdblist-key-1d2e' } });
    await o.page.goto(`${base}/#/settings?tab=keys`);
    await o.page.locator('#mdblist-key').waitFor();
    for (const [cls, name] of [
      ['tvdb', 'TVDB'],
      ['mdblist', 'MDBList'],
    ]) {
      await o.page.locator(`.card.${cls} button:has-text("Remove key")`).click();
      check(`${name}: asks first`, (await text(o.page.locator(`.card.${cls} .confirm-remove`))).startsWith(`Remove the ${name} key?`));
      check(`${name}: nothing sent yet`, settingsPuts(o.calls).length === 0);
      await o.page.locator(`.card.${cls} .confirm-remove button:has-text("Keep")`).click();
    }
    await o.context.close();
  },

  // Mutant caught: Test with a key typed and not saved (it would test the saved key, and say so of
  // the one typed).
  async 'API keys: no Test of a key that is typed and not saved'(browser) {
    const { context, page, calls } = await open(browser);
    await page.goto(`${base}/#/settings?tab=keys`);
    const test = page.locator('button[aria-label="Test the TMDB key"]');
    await test.waitFor();
    check('nothing typed: Test is on', await test.isEnabled());
    await page.fill('#tmdb-key', 'fake-typed-not-saved');
    check('a key typed: Test is off', await test.isDisabled());
    check('a key typed: it says why', (await text(page.locator('.card.tmdb .test .why'))) === 'Save the key to test it.');
    await test.click({ force: true }).catch(() => {});
    await sleep(300);
    check('a key typed: nothing asked of the server', !calls.some((c) => c.path === '/api/settings/test'));
    await page.fill('#tvdb-pin', '1234');
    check('TVDB, a PIN typed: Test is off', await page.locator('button[aria-label="Test the TVDB key"]').isDisabled());
    await page.fill('#tmdb-key', '');
    check('the field emptied: Test is on again', await test.isEnabled());
    await context.close();
  },

  // Mutant caught: the result of a Test made here kept when the key changes (a failed Test, then a
  // good key saved: the line went on saying "Failed").
  async 'API keys: the last-tested line follows a new key'(browser) {
    const { context, page } = await open(browser, { test: () => ({ ok: false, error: 'TMDB said the key is not valid (401).' }) });
    await page.goto(`${base}/#/settings?tab=keys`);
    const line = page.locator('.card.tmdb .tested');
    await page.locator('button[aria-label="Test the TMDB key"]').click();
    await until(async () => (await text(line)) === 'Failed when tested just now.');
    check('a failed Test: "Failed when tested just now."', (await text(line)) === 'Failed when tested just now.', await text(line));
    await page.fill('#tmdb-key', NEW_KEY);
    await page.locator('.card.tmdb button:has-text("Save key")').click(); // the save checks it: a test that passed
    await page.locator('.card.tmdb p[role="status"].ok').waitFor();
    await until(async () => (await text(line)) === 'Worked when tested just now.');
    check('a good key saved: "Worked when tested just now."', (await text(line)) === 'Worked when tested just now.', await text(line));
    await context.close();
  },

  // Mutant caught: the server's lastError not shown (API keys, Details).
  async 'The server’s last error, in API keys and Details'(browser) {
    const { context, page } = await open(browser, { status: { lastError: 'TMDB rejected the key' } });
    await page.goto(`${base}/#/settings?tab=keys`);
    const keys = page.locator('.key-status .error');
    await keys.waitFor({ timeout: 3000 }).catch(() => {});
    check('API keys: the error, as the server says it', (await text(keys)) === 'TMDB rejected the key', await text(keys));
    await tab(page, 'details').click();
    const details = page.locator('.status .error');
    await details.waitFor({ timeout: 3000 }).catch(() => {});
    check('Details: the error too', (await text(details)) === 'TMDB rejected the key', await text(details));
    await context.close();
    // Positive control for its absence: no error, no line.
    const ok = await open(browser);
    await ok.page.goto(`${base}/#/settings?tab=keys`);
    await ok.page.locator('#tmdb-key').waitFor();
    await sleep(300);
    check('no error: no line', (await ok.page.locator('.key-status').count()) === 0);
    await ok.context.close();
  },

  // Mutant caught: the TMDB notice only in the TMDB card (not where the lookups are switched), or
  // gone when the settings fail to load.
  async 'Details: the TMDB notice, even when the settings fail'(browser) {
    const { context, page } = await open(browser, { settingsFail: true });
    await page.goto(`${base}/#/settings?tab=details`);
    await page.locator('[role="tabpanel"] .error').waitFor();
    check('the settings failed: said', (await text(page.locator('[role="tabpanel"] .error').first())).includes('settings are not available'));
    check('the notice is there all the same', (await text(page.locator('[role="tabpanel"] .tmdb-notice'))).startsWith('This product uses TMDB'));
    await context.close();
  },

  // Mutant caught: "Open settings" in the fix dialog going to API keys (or Library) when it is the
  // lookups switch, in Details, that is off.
  async 'Fix match: lookups off opens Details'(browser) {
    const { context, page } = await open(browser, {
      review: [{ kind: 'movie', itemId: 1, title: 'Slow Film', year: 2001, matchState: 'unmatched', needsReview: false, score: 0 }],
      search: [409, { error: 'online metadata lookups are off: switch them on in Settings' }],
    });
    await page.goto(`${base}/#/settings`);
    await page.locator('.review li button:has-text("Fix")').click();
    const link = page.locator('dialog[open] .error a:has-text("Open settings")');
    await link.waitFor({ timeout: 3000 }).catch(() => {});
    check('lookups off: it says so', (await text(page.locator('dialog[open] .error'))).startsWith('Online lookups are off'));
    check('lookups off: "Open settings" opens Details', (await link.getAttribute('href').catch(() => '')) === '#/settings?tab=details');
    await context.close();
  },

  // Mutants caught: the last-tested line always "Worked"; the line left out; a Test's outcome not
  // shown in it until the settings are fetched again.
  async 'API keys: Test, and when it was last tested'(browser) {
    const { context, page, calls } = await open(browser, { test: (p) => (p === 'tvdb' ? { ok: false, error: 'TVDB rejected the key or PIN' } : { ok: true }) });
    await page.goto(`${base}/#/settings?tab=keys`);
    await page.locator('#tmdb-key').waitFor();
    const tmdbLine = page.locator('.card.tmdb .tested');
    const tvdbLine = page.locator('.card.tvdb .tested');
    await tmdbLine.waitFor({ timeout: 3000 }).catch(() => {});
    check('TMDB, tested 5 minutes ago and worked: said', (await text(tmdbLine)) === 'Worked when tested 5 minutes ago.', await text(tmdbLine));
    check('TVDB, never tested: no line', (await tvdbLine.count()) === 0);
    check('every key has a Test', (await page.locator('button[aria-label="Test the TMDB key"]').count()) === 1 && (await page.locator('button[aria-label="Test the TVDB key"]').count()) === 1 && (await page.locator('button[aria-label="Test the MDBList key"]').count()) === 1);
    await page.locator('button[aria-label="Test the TVDB key"]').click();
    await page.locator('.card.tvdb .test [role="status"].error').waitFor();
    check('TVDB Test: asks about TVDB', JSON.stringify(calls.filter((c) => c.path === '/api/settings/test').at(-1)?.body) === JSON.stringify({ provider: 'tvdb' }));
    check('TVDB Test: the reason', (await text(page.locator('.card.tvdb .test [role="status"]'))) === 'TVDB rejected the key or PIN');
    check('TVDB Test: "Failed when tested just now."', (await text(tvdbLine)) === 'Failed when tested just now.', await text(tvdbLine));
    await page.locator('button[aria-label="Test the TMDB key"]').click();
    await until(async () => (await text(tmdbLine)) === 'Worked when tested just now.');
    check('TMDB Test: "Worked when tested just now."', (await text(tmdbLine)) === 'Worked when tested just now.', await text(tmdbLine));
    // What the server says is what a fresh load shows.
    await page.reload();
    await page.locator('.card.tvdb .tested').waitFor({ timeout: 3000 }).catch(() => {});
    check('a reload: the server’s record of the failure', (await text(page.locator('.card.tvdb .tested'))) === 'Failed when tested just now.');
    await context.close();
  },

  // Mutant caught: the Ratings tab without its way to the MDBList key.
  async 'Ratings: the way to the MDBList key'(browser) {
    const { context, page } = await open(browser);
    await page.goto(`${base}/#/settings?tab=ratings`);
    const link = page.locator('[role="tabpanel"] a', { hasText: 'API keys' });
    await link.waitFor();
    check('it says what the key is for', (await text(page.locator('.keys-note'))).includes('need an MDBList key'));
    const entries = await page.evaluate(() => history.length);
    await link.click();
    await page.locator('#mdblist-key').waitFor({ timeout: 3000 }).catch(() => {});
    check('the link opens API keys, at the MDBList key', (await selectedTab(page)) === 'settings-tab-keys' && (await page.locator('#mdblist-key').count()) === 1);
    check('as a tab does: no history entry', (await page.evaluate(() => history.length)) === entries);
    check('the focus on the tab, not lost', await page.evaluate(() => document.activeElement?.id === 'settings-tab-keys'));
    await context.close();
  },

  // Mutant caught: Home's "Set it up" (no TMDB key) opening Library, where there is no key to set.
  async 'Links into a tab: Home’s TMDB setup card'(browser) {
    const film = {
      id: 1, kind: 'movie', title: 'Heat', year: 1995, durationSec: 7000, container: 'mkv', videoCodec: 'h264', audioCodec: 'aac',
      width: 1920, height: 1080, addedAt: 1, progress: null, art: null, matchState: 'unmatched', needsReview: false,
    };
    const { context, page } = await open(browser, { keys: { tmdb: '', tvdb: '', mdblist: '' }, items: [film] });
    await page.goto(`${base}/#/`);
    const setUp = page.locator('a:has-text("Set it up")');
    await setUp.waitFor();
    check('"Set it up" links to API keys', (await setUp.getAttribute('href')) === '#/settings?tab=keys', await setUp.getAttribute('href'));
    await setUp.click();
    await page.locator('#tmdb-key').waitFor({ timeout: 3000 }).catch(() => {});
    check('and opens it, at the TMDB key', (await selectedTab(page)) === 'settings-tab-keys' && (await page.locator('#tmdb-key').count()) === 1);
    await context.close();
  },

  // Mutants caught: the page container not centred (or as wide as the screen); a side column of
  // tabs on a phone, or a row that wraps instead of scrolling.
  async 'Layout: centred at 1920 px; a column of tabs on a wide screen, a row on a phone'(browser) {
    for (const [name, at, ready, block] of [
      ['Settings', '#/settings', TABS.library.heading, '.settings'],
      ['Activity', '#/activity', 'h1', '.activity'],
    ]) {
      const { context, page } = await open(browser, { width: 1920 });
      await page.goto(`${base}/${at}`);
      await page.locator(ready).first().waitFor();
      await sleep(300);
      const box = await page.locator(block).first().boundingBox();
      const centre = box.x + box.width / 2;
      check(`${name} at 1920: narrower than the screen`, box.width < 1920 - 400, `${Math.round(box.width)} px`);
      check(`${name} at 1920: centred`, Math.abs(centre - 960) <= 3, `centre at ${Math.round(centre)}`);
      // What is in it, not only the box: the title has moved in from the gutter (48 px) it had.
      const h1 = await page.locator('main h1').first().boundingBox();
      check(`${name} at 1920: the title is in from the left edge`, h1.x > 300, `h1 at ${Math.round(h1.x)}`);
      await context.close();
    }
    {
      const { context, page } = await open(browser, { width: 1440 });
      await page.goto(`${base}/#/settings`);
      await page.locator(TABS.library.heading).waitFor();
      const list = await page.locator('[role="tablist"]').boundingBox();
      const panel = await page.locator('[role="tabpanel"]').boundingBox();
      check('1440: the tabs are a column left of the panel', list.x + list.width <= panel.x && list.height > list.width, JSON.stringify({ list, panel }));
      check('1440: aria-orientation vertical', (await page.locator('[role="tablist"]').getAttribute('aria-orientation')) === 'vertical');
      // The selected tab reads as navigation: a quiet fill and the accent's bar, not a white pill.
      const look = await tab(page, 'library').evaluate((t) => {
        const c = getComputedStyle(t);
        return { bg: c.backgroundColor, color: c.color, shadow: c.boxShadow, text: getComputedStyle(document.body).color };
      });
      const alpha = Number(/rgba\([^)]*,\s*([\d.]+)\)/.exec(look.bg)?.[1] ?? 1);
      check('1440: the selected tab is not filled with the text colour', look.bg !== look.text && alpha < 0.3, JSON.stringify(look));
      check('1440: it is marked by the accent bar', look.shadow.includes('inset'), look.shadow);
      check('1440: no fade on the column', (await page.locator('[role="tablist"]').evaluate((e) => getComputedStyle(e).maskImage)) === 'none');
      await context.close();
    }
    {
      const { context, page } = await open(browser, { width: 375 });
      await page.goto(`${base}/#/settings?tab=keys`);
      await page.locator(TABS.keys.heading).waitFor();
      await sleep(300);
      const list = page.locator('[role="tablist"]');
      const m = await list.evaluate((e) => ({ sw: e.scrollWidth, cw: e.clientWidth, sl: e.scrollLeft, h: e.getBoundingClientRect().height }));
      const tops = await page.locator('[role="tab"]').evaluateAll((ts) => new Set(ts.map((t) => Math.round(t.getBoundingClientRect().top))).size);
      check('375: the tabs are one row', tops === 1, `${tops} rows`);
      check('375: the row scrolls sideways', m.sw > m.cw, JSON.stringify(m));
      check('375: the page itself does not', !(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth)));
      const last = await tab(page, 'keys').boundingBox();
      check('375: a deep link to the last tab shows it', last.x >= 0 && last.x + last.width <= 375, JSON.stringify(last));
      check('375: aria-orientation horizontal', (await list.getAttribute('aria-orientation')) === 'horizontal');
      // At the end of the row: a fade before it (more to scroll back to), none after.
      const fades = await list.evaluate((e) => ({ before: e.classList.contains('fade-before'), after: e.classList.contains('fade-after'), mask: getComputedStyle(e).maskImage }));
      check('375, the last tab: a fade before, none after', fades.before && !fades.after && fades.mask !== 'none', JSON.stringify(fades));
      await context.close();
    }
    {
      // A tab in the middle of the row is centred in it, with a fade at both ends.
      const { context, page } = await open(browser, { width: 375 });
      await page.goto(`${base}/#/settings?tab=ratings`);
      await page.locator(TABS.ratings.heading).waitFor();
      await sleep(300);
      const list = page.locator('[role="tablist"]');
      const row = await list.boundingBox();
      const t = await tab(page, 'ratings').boundingBox();
      const off = t.x + t.width / 2 - (row.x + row.width / 2);
      check('375, Ratings: centred in the row', Math.abs(off) <= 3, `${Math.round(off)} px off the middle`);
      const fades = await list.evaluate((e) => ({ before: e.classList.contains('fade-before'), after: e.classList.contains('fade-after') }));
      check('375, Ratings: a fade at both ends', fades.before && fades.after, JSON.stringify(fades));
      // Scrolled to the start: the fade before goes.
      await list.evaluate((e) => (e.scrollLeft = 0));
      await sleep(150);
      check('375, scrolled to the start: no fade before', !(await list.evaluate((e) => e.classList.contains('fade-before'))));
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
