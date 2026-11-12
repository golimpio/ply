// The M7 "Library folders" settings in a real browser, against a mocked API that keeps state:
// the list (startup folders, a guessed type, a folder still to be guessed, a missing disk), adding a
// folder through the picker, changing a type and removing a folder, each behind its confirmation,
// and what happens when the server is slow, busy or says no.
//
// Self-contained like metadata-ui.mjs: serves web/dist (build it first) and answers /api/ with
// page.route. Needs Google Chrome.
//
//   npm run build && node e2e/folders-ui.mjs
//
// PLY_E2E_ONLY=<text> runs the scenarios whose name contains it; PLY_E2E_DIST=<dir> serves another
// build (for trying a mutant without touching web/dist). Each scenario names the mutation it was
// confirmed to catch.
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
/** Polls `fn` until it is truthy (true) or `ms` pass (false). */
const until = async (fn, ms = 3000) => {
  const end = Date.now() + ms;
  while (Date.now() < end) {
    if (await fn()) return true;
    await sleep(40);
  }
  return false;
};

const types = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.json': 'application/json', '.svg': 'image/svg+xml' };
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

const folder = (id, path, kind, extra = {}) => ({
  id, path, kind, guessed: false, source: 'ui', kindLocked: false, items: 0, lastScanAt: 0, state: 'ok', error: '', ...extra,
});
const startFolders = () => [
  folder(1, '/srv/films', 'movies', { source: 'flag', kindLocked: true, items: 40, lastScanAt: 1790000000 }),
  folder(2, '/Volumes/NAS/Movies-3', 'movies', { guessed: true, items: 263, lastScanAt: 1790000000 }),
  folder(0, '/srv/mixed', '', { source: 'flag' }),
  folder(3, '/Volumes/Gone/TV', 'shows', { state: 'missing', items: 12 }),
];
// The server's disks: a tree of directory names.
const TREE = {
  '/Users/me': ['Movies', 'TV'],
  '/Users/me/Movies': [],
  '/Users/me/TV': ['Lost (2004)'],
  '/Users/me/TV/Lost (2004)': ['Season 1'],
  '/Volumes/USB': ['Kids'],
  '/Volumes/USB/Kids': [],
};
// Listings the server refuses (403).
const FORBIDDEN = { '/Users/me/.config': 'permission denied: /Users/me/.config' };
const parentOf = (p) => (p.lastIndexOf('/') > 0 ? p.slice(0, p.lastIndexOf('/')) : '/');

/**
 * A fresh page on the mocked server.
 *   post, patch, del  script one kind of request: (n) => step, with n counting from 1. A step is
 *                     {status, error, retryAfter, delay}: a status of 400 or more answers with {error}
 *                     (and a Retry-After header), a delay holds the answer back that many ms, no step
 *                     (or no status) succeeds. A 404 on DELETE also drops the folder, as if someone
 *                     else removed it; a 409 on PATCH also fixes its type, as if set at startup.
 *   slowDir           a path whose listing answers after `slowMs` (a 404 if it is not in TREE)
 *   scanAfterAdd      after POST /api/folders, GET /api/scan answers running:true that many times;
 *                     when it turns false the new folder has 5 items and a scan time
 *   review            the review list
 * Returns `calls` (the non-GET requests: {method, path, body}) and `gets` (how many GET /api/folders
 * and GET /api/scan were answered).
 */
async function open(browser, opts = {}) {
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 }, serviceWorkers: 'block' });
  const page = await context.newPage();
  const calls = [];
  const errors = [];
  const gets = { folders: 0, scan: 0 };
  const seen = { post: 0, patch: 0, del: 0 };
  let folders = startFolders();
  let nextId = 10;
  let scanLeft = 0; // GET /api/scan still answers running:true this many times
  let scanned = null; // the folder that gets its items when that scan ends
  page.on('pageerror', (e) => errors.push(e.message));
  // A scenario may end (and close the page) while an answer is still being held back.
  const reply = (route, status, body, headers = {}) =>
    route.fulfill({ status, headers, contentType: 'application/json', body: JSON.stringify(body) }).catch(() => {});
  const json = (route, body, status = 200) => reply(route, status, body);
  const refuse = (route, step) =>
    reply(route, step.status, { error: step.error }, step.retryAfter === undefined ? {} : { 'Retry-After': String(step.retryAfter) });
  /** Waits out the scripted delay; returns the step when it refuses. */
  const scripted = async (script, n) => {
    const step = script?.(n);
    if (step?.delay) await sleep(step.delay);
    return step?.status >= 400 ? step : null;
  };
  await page.route('**/api/**', async (route) => {
    const req = route.request();
    const u = new URL(req.url());
    const p = u.pathname;
    const body = req.postData() ? JSON.parse(req.postData()) : null;
    if (req.method() !== 'GET') calls.push({ method: req.method(), path: p + u.search, body });
    if (p === '/api/info') return json(route, { id: 'x', name: 'e2e', version: 'dev', authRequired: false });
    if (p === '/api/scan') {
      gets.scan++;
      const running = scanLeft > 0;
      if (running) scanLeft--;
      else if (scanned) {
        Object.assign(scanned, { items: 5, lastScanAt: 1790000500 });
        scanned = null;
      }
      return json(route, { running, itemCount: 315, watching: [{ root: '/Volumes/NAS/Movies-3', watching: true, dirs: 263 }] });
    }
    if (p === '/api/items' || p === '/api/continue' || p === '/api/shows' || p === '/api/upcoming' || p === '/api/genres')
      return json(route, []);
    if (p === '/api/settings') return json(route, { tmdbKeySet: false, tmdbKeySource: '', metadataEnabled: false, metadataForcedOff: true, language: 'en-US' });
    if (p === '/api/metadata/status') return json(route, { enabled: false, keySet: false, running: false, pending: 0, unmatched: 0, needsReview: 0, lastError: '' });
    if (p === '/api/metadata/review') return json(route, opts.review ?? []);
    if (p === '/api/folders' && req.method() === 'GET') {
      gets.folders++;
      return json(route, folders);
    }
    if (p === '/api/folders' && req.method() === 'POST') {
      const refused = await scripted(opts.post, ++seen.post);
      if (refused) return refuse(route, refused);
      const f = folder(nextId++, body.path, body.kind);
      folders = [...folders, f];
      if (opts.scanAfterAdd) {
        scanLeft = opts.scanAfterAdd;
        scanned = f;
      }
      return json(route, f, 201);
    }
    const m = /^\/api\/folders\/(\d+)$/.exec(p);
    if (m && req.method() === 'PATCH') {
      const f = folders.find((x) => x.id === Number(m[1]));
      const refused = await scripted(opts.patch, ++seen.patch);
      if (refused) {
        if (refused.status === 409) Object.assign(f, { source: 'flag', kindLocked: true });
        return refuse(route, refused);
      }
      Object.assign(f, { kind: body.kind, guessed: false });
      return json(route, f);
    }
    if (m && req.method() === 'DELETE') {
      const refused = await scripted(opts.del, ++seen.del);
      if (refused) {
        if (refused.status === 404) folders = folders.filter((x) => x.id !== Number(m[1]));
        return refuse(route, refused);
      }
      folders = folders.filter((x) => x.id !== Number(m[1]));
      return route.fulfill({ status: 204, body: '' }).catch(() => {});
    }
    const mScan = /^\/api\/folders\/(\d+)\/scan$/.exec(p);
    if (mScan && req.method() === 'POST') {
      return json(route, { started: true }, 202);
    }
    if (p === '/api/fs') {
      const path = u.searchParams.get('path');
      if (!path)
        return json(route, { path: '', parent: '', dirs: [], places: [{ name: 'Home', path: '/Users/me' }, { name: 'USB', path: '/Volumes/USB' }] });
      if (opts.slowDir === path) await sleep(opts.slowMs ?? 800);
      if (FORBIDDEN[path]) return json(route, { error: FORBIDDEN[path] }, 403);
      if (!TREE[path]) return json(route, { error: `no such directory: ${path}` }, 404);
      const parent = path === '/' ? '' : parentOf(path);
      return json(route, { path, parent, dirs: TREE[path].map((name) => ({ name, path: `${path}/${name}` })) });
    }
    return json(route, { error: `unmocked ${p}` }, 404);
  });
  await page.goto(`${base}/#/settings`);
  await page.locator('ul[aria-label="Library folders"]').waitFor();
  return { context, page, calls, errors, gets };
}

const row = (page, path) => page.locator('ul[aria-label="Library folders"] > li', { has: page.locator(`code:text-is("${path}")`) });
const dialog = (page) => page.locator('dialog[open]');
const alerts = (page) => dialog(page).locator('[role="alert"]');
const listed = (page) => dialog(page).locator('.list .dir span:first-of-type').allTextContents();
/** The line under the list that says what just happened. */
const notice = (page) => page.locator('.add [role="status"]');
const noticeText = async (page) => ((await notice(page).textContent()) ?? '').trim();
/** The trimmed text, or '' when the element is not there (so a missing one fails a check instead of timing out). */
const textOf = (loc) => loc.textContent({ timeout: 1500 }).then((t) => (t ?? '').trim(), () => '');
const count = (calls, method) => calls.filter((c) => c.method === method).length;
/** True once the element with the focus matches `selector` (and holds `text`). */
const focusIs = (page, selector, text = '') =>
  page
    .waitForFunction(([s, t]) => document.activeElement?.matches(s) && document.activeElement.textContent.includes(t), [selector, text], { timeout: 2500 })
    .then(() => true, () => false);
/** Two clicks in one task: the second lands before Svelte has redrawn the first one's button. */
async function twice(button) {
  await until(() => button.isEnabled());
  await button.evaluate((b) => {
    b.click();
    b.click();
  });
}
/** The picker, opened and walked to /Users/me/TV with "TV shows" chosen. */
async function pickTV(page) {
  await page.click('button:has-text("Add a folder")');
  await dialog(page).locator('.dir:has-text("Home")').click();
  await dialog(page).locator('.dir:has-text("TV")').click();
  await dialog(page).locator('ul[aria-label="Folders in /Users/me/TV"]').waitFor();
  await dialog(page).locator('label:has-text("TV shows")').click();
}
const removeNAS = async (page) => {
  await row(page, '/Volumes/NAS/Movies-3').locator('button:has-text("Remove")').click();
  await dialog(page).waitFor();
};
const retypeNAS = async (page) => {
  await row(page, '/Volumes/NAS/Movies-3').locator('.seg button:has-text("TV shows")').click();
  await dialog(page).waitFor();
};
const BUSY = 'a scan of the library is still running; the folder was not removed, try again in a moment'; // the server's words

const scenarios = {
  // Mutants caught: Remove shown for a startup folder; the type buttons enabled for a locked type; the
  // startup notes dropped (the page no longer says why there is no Remove); "No items yet." replaced by
  // "0 items."; the Remove button without its aria-label; the notice region rendered only once it has text.
  async 'the list: startup folders, guessed and unknown types, a missing disk'(browser) {
    const { context, page, errors } = await open(browser);
    const flag = row(page, '/srv/films');
    check('a startup folder says so', (await flag.locator('.tag').textContent())?.trim() === 'Set at startup');
    check('a startup folder has no Remove', (await flag.locator('button:has-text("Remove")').count()) === 0);
    check('a type set at startup is locked', await flag.locator('.seg button[aria-pressed="true"]').isDisabled());
    check('…and says why, in words on the page', (await flag.textContent()).includes("Set at startup with -movies: the type is fixed and the folder can't be removed here."));

    const nas = row(page, '/Volumes/NAS/Movies-3');
    // Positive controls for the above: a folder added here can be removed and retyped.
    check('a folder added here has Remove', (await nas.locator('button:has-text("Remove")').count()) === 1);
    check('Remove names its folder', (await nas.locator('button[aria-label="Remove /Volumes/NAS/Movies-3"]').count()) === 1);
    check('its type can be changed', await nas.locator('.seg button:has-text("TV shows")').isEnabled());
    check('Movies is the pressed type', (await nas.locator('.seg button[aria-pressed="true"]').textContent()) === 'Movies');
    check('a guessed type says so', (await nas.textContent()).includes('Guessed from the file names'));
    check('the watcher state is shown', (await nas.textContent()).includes('New files show up within seconds'));
    check('the item count is shown', (await nas.textContent()).includes('263 items'));
    check('…and no startup note', !(await nas.textContent()).includes("can't be removed here"));
    check('…and not "No items yet."', !(await nas.textContent()).includes('No items yet.'));

    const unknown = row(page, '/srv/mixed');
    check('a type not guessed yet has no buttons', (await unknown.locator('.seg').count()) === 0);
    check('…and says when it will be known', (await unknown.textContent()).includes('guessed from the file names after the first scan'));
    check('a folder with id 0 has no Remove', (await unknown.locator('button:has-text("Remove")').count()) === 0);
    check('a startup folder of unknown type says it stays', (await unknown.textContent()).includes("Set at startup, so it can't be removed here."));
    check('no items reads "No items yet."', (await unknown.textContent()).includes('No items yet.'));

    check('a missing disk is flagged', (await row(page, '/Volumes/Gone/TV').textContent()).includes("Can't be found"));
    check('the notice is there before it has anything to say', (await notice(page).count()) === 1 && (await noticeText(page)) === '');
    check('no page errors', errors.length === 0, errors.join('; '));
    await context.close();
  },

  // Mutants caught: kind defaulting to 'movies'; the POST sending the folder the list was opened at; the
  // path box not focused on opening; no focus moved into the new list; the legend without the full path;
  // the screen-reader status dropped or without the count; the footer button not saying "Add this folder";
  // the navigation without its label; a new folder listed with a made-up item count.
  async 'adding: browse, choose the type, add'(browser) {
    const { context, page, calls } = await open(browser);
    await page.click('button:has-text("Add a folder")');
    await dialog(page).locator('ul[aria-label="Places"]').waitFor();
    const addBtn = dialog(page).locator('footer .btn.primary');
    const said = dialog(page).locator('p[role="status"]');
    check('it starts at the places', (await listed(page)).join('|') === 'Home|USB', (await listed(page)).join('|'));
    check('Up is off at the places', await dialog(page).locator('button:has-text("Up")').isDisabled());
    check('the navigation is labelled', (await dialog(page).locator('nav[aria-label="Folder navigation"]').count()) === 1);
    check('the path box has the focus on opening', await focusIs(page, '#folder-path'));
    check('a screen reader hears the places', await until(async () => (await textOf(said)) === '2 places'), await textOf(said));
    check('at the places, Add asks to open a folder', (await textOf(addBtn)) === 'Open a folder to add it' && (await addBtn.isDisabled()));

    await dialog(page).locator('.dir:has-text("Home")').click();
    await dialog(page).locator('ul[aria-label="Folders in /Users/me"]').waitFor();
    check('going down puts the focus on the first folder', await focusIs(page, '.list .dir', 'Movies'));
    check('a screen reader hears how many folders', await until(async () => (await textOf(said)) === '2 folders in /Users/me'), await textOf(said));
    await dialog(page).locator('.dir:has-text("TV")').click();
    await dialog(page).locator('ul[aria-label="Folders in /Users/me/TV"]').waitFor();
    check('the path box follows', (await dialog(page).locator('#folder-path').inputValue()) === '/Users/me/TV');
    check('one folder is "1 folder"', await until(async () => (await textOf(said)) === '1 folder in /Users/me/TV'), await textOf(said));

    check('no type is chosen for you', (await dialog(page).locator('input[name="folder-kind"]:checked').count()) === 0);
    check('Add waits for a type', await addBtn.isDisabled());
    check('Add says what it adds', (await textOf(addBtn)) === 'Add this folder', await textOf(addBtn));
    check('the legend has the full path', (await textOf(dialog(page).locator('fieldset.kind legend'))) === "What's in /Users/me/TV?");

    await dialog(page).locator('label:has-text("TV shows")').click();
    check('with a type, Add is on', await addBtn.isEnabled());
    await addBtn.click();
    await row(page, '/Users/me/TV').waitFor();
    const post = calls.find((c) => c.method === 'POST');
    check('the folder and type were sent', post?.body?.path === '/Users/me/TV' && post?.body?.kind === 'shows', JSON.stringify(post?.body));
    check('the dialog closed', (await dialog(page).count()) === 0);
    check('the new folder is listed as TV shows', (await row(page, '/Users/me/TV').locator('.seg button[aria-pressed="true"]').textContent()) === 'TV shows');
    check('a new folder has no items yet', (await row(page, '/Users/me/TV').textContent()).includes('No items yet.'));
    check('a note says it is being read', (await noticeText(page)).includes('TV was added'));

    // Opened again: back at the places, nothing chosen.
    await page.click('button:has-text("Add a folder")');
    await dialog(page).locator('ul[aria-label="Places"]').waitFor();
    check('re-opened: no type chosen', (await dialog(page).locator('input[name="folder-kind"]:checked').count()) === 0);
    check('re-opened: the path box has the focus', await focusIs(page, '#folder-path'));
    await context.close();
  },

  // Mutants caught: Up going to the places instead of the parent; a typed path ignored; the error not
  // saying where the picker still is; an empty folder leaving the focus behind the dialog.
  async 'adding: Up, Places and a typed path'(browser) {
    const { context, page } = await open(browser);
    await page.click('button:has-text("Add a folder")');
    await dialog(page).locator('ul[aria-label="Places"]').waitFor();
    await dialog(page).locator('#folder-path').fill('/Users/me/TV/Lost (2004)');
    await dialog(page).locator('#folder-path').press('Enter');
    await dialog(page).locator('ul[aria-label="Folders in /Users/me/TV/Lost (2004)"]').waitFor();
    check('a typed path is opened', (await listed(page)).join('|') === 'Season 1');
    await dialog(page).locator('button:has-text("Up")').click();
    await dialog(page).locator('ul[aria-label="Folders in /Users/me/TV"]').waitFor();
    check('Up opens the parent', (await listed(page)).join('|') === 'Lost (2004)');
    check('…and the focus follows', await focusIs(page, '.list .dir', 'Lost (2004)'));
    await dialog(page).locator('#folder-path').fill('/nowhere');
    await dialog(page).locator('#folder-path').press('Enter');
    await alerts(page).waitFor();
    const said = await textOf(alerts(page));
    check('a missing path says so', said.includes('no such directory'), said);
    check('…and where the picker still is', said.includes('Still in /Users/me/TV.'), said);
    check('…and keeps the last listing', (await listed(page)).join('|') === 'Lost (2004)');
    await dialog(page).locator('button:has-text("Places")').click();
    await dialog(page).locator('ul[aria-label="Places"]').waitFor();
    check('Places goes back to the start', (await listed(page)).join('|') === 'Home|USB');

    // A folder with nothing in it: the focus goes to the (empty) list, not to the page behind.
    await dialog(page).locator('#folder-path').fill('/Users/me/Movies');
    await dialog(page).locator('#folder-path').press('Enter');
    await dialog(page).locator('text=No folders in here.').waitFor();
    check('an empty folder takes the focus to the list', await focusIs(page, '.list'));
    await context.close();
  },

  // Mutant caught: go() without the ticket check (a slow, older listing wins).
  async 'adding: a slow listing does not replace a newer one'(browser) {
    const { context, page } = await open(browser, { slowDir: '/Users/me', slowMs: 900 });
    await page.click('button:has-text("Add a folder")');
    await dialog(page).locator('ul[aria-label="Places"]').waitFor();
    await dialog(page).locator('.dir:has-text("Home")').click(); // slow
    await dialog(page).locator('#folder-path').fill('/Volumes/USB');
    await dialog(page).locator('#folder-path').press('Enter'); // fast
    await dialog(page).locator('ul[aria-label="Folders in /Volumes/USB"]').waitFor();
    await sleep(1200); // the slow answer arrives meanwhile
    check('the newer listing stays', (await listed(page)).join('|') === 'Kids', (await listed(page)).join('|'));
    check('the path box too', (await dialog(page).locator('#folder-path').inputValue()) === '/Volumes/USB');
    await context.close();
  },

  // Mutant caught (A): go()'s catch without the stale-ticket check (an old, slow failure puts its error
  // over the newer, good listing).
  async 'adding: a slow failure does not replace a newer listing'(browser) {
    const { context, page } = await open(browser, { slowDir: '/nowhere', slowMs: 900 });
    await page.click('button:has-text("Add a folder")');
    await dialog(page).locator('ul[aria-label="Places"]').waitFor();
    await dialog(page).locator('#folder-path').fill('/nowhere');
    await dialog(page).locator('#folder-path').press('Enter'); // a slow 404
    await dialog(page).locator('#folder-path').fill('/Volumes/USB');
    await dialog(page).locator('#folder-path').press('Enter'); // fast, and good
    await dialog(page).locator('ul[aria-label="Folders in /Volumes/USB"]').waitFor();
    await sleep(1300); // the slow 404 arrives meanwhile
    check('the older failure shows no error', (await alerts(page).count()) === 0, await alerts(page).allTextContents().then((t) => t.join('|')));
    check('the newer listing stays', (await listed(page)).join('|') === 'Kids', (await listed(page)).join('|'));
    check('the path box too', (await dialog(page).locator('#folder-path').inputValue()) === '/Volumes/USB');

    // Positive control: the same slow 404, with nothing newer after it, is shown.
    await dialog(page).locator('#folder-path').fill('/nowhere');
    await dialog(page).locator('#folder-path').press('Enter');
    check('the same failure, not stale, is shown', await alerts(page).waitFor({ timeout: 3000 }).then(() => true, () => false));
    check('…with the reason', (await textOf(alerts(page))).includes('no such directory: /nowhere'));
    await context.close();
  },

  // Mutants caught: the error swallowed (dialog closing as if added); Add left off (adding never reset)
  // after a refusal.
  async 'adding: a refusal is shown in the dialog'(browser) {
    const refusals = [
      [400, '/Users/me/TV overlaps the library folder /Users/me: a folder cannot hold or sit inside another'],
      [409, '/Users/me/TV is already a library folder'],
    ];
    for (const [status, error] of refusals) {
      const { context, page, calls } = await open(browser, { post: () => ({ status, error }) });
      await pickTV(page);
      check(`${status}: no error before Add`, (await alerts(page).count()) === 0);
      await dialog(page).locator('footer .btn.primary').click();
      await alerts(page).waitFor();
      check(`${status}: the server's reason is shown`, (await textOf(alerts(page))) === error);
      check(`${status}: the dialog stays open`, (await dialog(page).count()) === 1);
      check(`${status}: the request was made`, count(calls, 'POST') === 1);
      check(`${status}: Add can be tried again`, await dialog(page).locator('footer .btn.primary').isEnabled());
      await context.close();
    }
  },

  // Mutant caught: a refused listing (403) shown as an empty folder, or not at all.
  async 'adding: a folder the server may not read says so'(browser) {
    const { context, page } = await open(browser);
    await page.click('button:has-text("Add a folder")');
    await dialog(page).locator('.dir:has-text("Home")').click();
    await dialog(page).locator('ul[aria-label="Folders in /Users/me"]').waitFor();
    check('no error while browsing a readable folder', (await alerts(page).count()) === 0); // control
    await dialog(page).locator('#folder-path').fill('/Users/me/.config');
    await dialog(page).locator('#folder-path').press('Enter');
    await alerts(page).waitFor();
    const said = await textOf(alerts(page));
    check('the server\'s reason is shown', said.includes('permission denied: /Users/me/.config'), said);
    check('…with where the picker still is', said.includes('Still in /Users/me.'), said);
    check('…and it still lists that folder', (await listed(page)).join('|') === 'Movies|TV');
    await context.close();
  },

  // Mutants caught: the PATCH sent on the click itself; a cancelled change left showing; the second
  // paragraph of the dialog (what happens to watch progress and fixed matches) dropped.
  async 'changing a type asks first'(browser) {
    const { context, page, calls } = await open(browser);
    const nas = row(page, '/Volumes/NAS/Movies-3');
    await nas.locator('.seg button:has-text("TV shows")').click();
    await dialog(page).waitFor();
    check('it asks', ((await dialog(page).locator('h2').textContent()) ?? '').includes('Read Movies-3 as TV shows?'));
    check('…saying the files are read again', (await dialog(page).textContent()).includes('263 files'));
    const words = (await dialog(page).textContent()).replace(/\s+/g, ' '); // the source wraps its lines
    check('…that watch progress is kept', words.includes('Watch progress is kept'));
    check('…and that a fixed match may be put aside', words.includes('put aside'));
    check('nothing sent before the answer', !calls.some((c) => c.method === 'PATCH'));
    await dialog(page).locator('footer button:has-text("Cancel")').click();
    check('cancelled: still Movies', (await nas.locator('.seg button[aria-pressed="true"]').textContent()) === 'Movies');
    check('cancelled: nothing sent', !calls.some((c) => c.method === 'PATCH'));

    await nas.locator('.seg button:has-text("TV shows")').click();
    await dialog(page).locator('footer button:has-text("Read as TV shows")').click();
    await page.waitForFunction(() => !document.querySelector('dialog[open]'));
    const patch = calls.find((c) => c.method === 'PATCH');
    check('confirmed: the new type is sent', patch?.path === '/api/folders/2' && patch?.body?.kind === 'shows', JSON.stringify(patch));
    await nas.locator('.seg button[aria-pressed="true"]:has-text("TV shows")').waitFor();
    check('the guess note is gone', !(await nas.textContent()).includes('Guessed from the file names'));
    check('pressing the current type asks nothing', await (async () => {
      await nas.locator('.seg button:has-text("TV shows")').click();
      await sleep(200);
      return (await dialog(page).count()) === 0;
    })());
    await context.close();
  },

  // Mutants caught: forget always sent (or never); the dialog button not saying what it will do when
  // forgetting is on.
  async 'removing: progress kept unless asked'(browser) {
    const { context, page, calls } = await open(browser);
    await removeNAS(page);
    check('it says the files stay', (await dialog(page).textContent()).includes("The files on disk aren't touched"));
    check('forgetting is off by default', !(await dialog(page).locator('input[type="checkbox"]').isChecked()));
    check('the button says Remove folder', (await textOf(dialog(page).locator('footer .btn.danger'))) === 'Remove folder');
    await dialog(page).locator('footer button:has-text("Remove folder")').click();
    await page.waitForFunction(() => !document.querySelector('ul[aria-label="Library folders"]')?.textContent?.includes('Movies-3'));
    const del = calls.find((c) => c.method === 'DELETE');
    check('kept: no forget', del?.path === '/api/folders/2', del?.path);
    check('the notice says it was removed', (await noticeText(page)).includes('Movies-3 was removed from the library'));

    await row(page, '/Volumes/Gone/TV').locator('button:has-text("Remove")').click();
    await dialog(page).locator('input[type="checkbox"]').check();
    check('the box says what forgetting means', (await dialog(page).textContent()).includes('starts from scratch'));
    check('with forgetting on, the button says so', (await textOf(dialog(page).locator('footer .btn.danger'))) === 'Remove and forget');
    await dialog(page).locator('footer button:has-text("Remove and forget")').click();
    await page.waitForFunction(() => !document.querySelector('dialog[open]'));
    const del2 = calls.filter((c) => c.method === 'DELETE')[1];
    check('forget: ?forget=1', del2?.path === '/api/folders/3?forget=1', del2?.path);
    await context.close();
  },

  // Mutants caught: no retry of a 503 (the folder stays until the user clicks again); the server's raw
  // words shown instead of ours; Retry-After ignored or not capped to 1..30 s; the retry limit gone
  // (MAX_RETRIES); closing the dialog not giving up the retry.
  async 'removing: a busy scan is retried by itself'(browser) {
    {
      // One 503 with Retry-After: 1, then it works: no second click.
      const { context, page, calls } = await open(browser, { del: (n) => (n === 1 ? { status: 503, error: BUSY, retryAfter: 1 } : null) });
      await removeNAS(page);
      await dialog(page).locator('footer button:has-text("Remove folder")').click();
      await alerts(page).waitFor();
      const said = await textOf(alerts(page));
      check('busy: our own words, not the server\'s', said.includes('A scan is still running, so nothing was removed.') && !said.includes('a scan of the library'), said);
      check('busy: it says it will try again', /Retrying in 1 s…/.test(said), said);
      check('busy: the dialog stays', (await dialog(page).count()) === 1);
      check('busy: the folder is still listed', (await row(page, '/Volumes/NAS/Movies-3').count()) === 1); // control for the next
      check('busy: then the folder goes, with no click', await until(async () => (await row(page, '/Volumes/NAS/Movies-3').count()) === 0, 6000));
      check('busy: two DELETEs, the second by itself', count(calls, 'DELETE') === 2, `${count(calls, 'DELETE')}`);
      check('busy: the dialog closed', await until(async () => (await dialog(page).count()) === 0));
      check('busy: the notice says it was removed', (await noticeText(page)).includes('was removed from the library'));
      await context.close();
    }
    {
      // Always busy: the first try and MAX_RETRIES (3) more, then it stops and says so.
      const { context, page, calls } = await open(browser, { del: () => ({ status: 503, error: BUSY, retryAfter: 1 }) });
      await removeNAS(page);
      await dialog(page).locator('footer button:has-text("Remove folder")').click();
      const gaveUp = await until(async () => (await alerts(page).allTextContents()).join('').includes('Try again in a moment'), 9000);
      check('always busy: it stops and says to try later', gaveUp, (await alerts(page).allTextContents()).join('|'));
      check('always busy: 4 DELETEs, not more', count(calls, 'DELETE') === 4, `${count(calls, 'DELETE')}`);
      await sleep(1600);
      check('always busy: it stays stopped', count(calls, 'DELETE') === 4, `${count(calls, 'DELETE')}`);
      check('always busy: no countdown any more', !(await textOf(alerts(page))).includes('Retrying'));
      check('always busy: the dialog stays and the folder too', (await dialog(page).count()) === 1 && (await row(page, '/Volumes/NAS/Movies-3').count()) === 1);
      await context.close();
    }
    for (const [header, want] of [[999, 30], [0, 1]]) {
      // Retry-After is kept within 1..30 s.
      const { context, page } = await open(browser, { del: () => ({ status: 503, error: BUSY, retryAfter: header }) });
      await removeNAS(page);
      await dialog(page).locator('footer button:has-text("Remove folder")').click();
      await alerts(page).waitFor();
      const said = await textOf(alerts(page));
      check(`Retry-After: ${header} waits ${want} s`, said.includes(`Retrying in ${want} s…`), said);
      await context.close();
    }
    {
      // Closing the dialog gives up the retry.
      const { context, page, calls } = await open(browser, { del: () => ({ status: 503, error: BUSY, retryAfter: 1 }) });
      await removeNAS(page);
      await dialog(page).locator('footer button:has-text("Remove folder")').click();
      await dialog(page).locator('text=Retrying in').waitFor();
      await dialog(page).locator('footer button:has-text("Cancel")').click();
      await sleep(2300);
      check('cancelled: no retry after the dialog closed', count(calls, 'DELETE') === 1, `${count(calls, 'DELETE')}`);
      check('cancelled: the folder is still listed', (await row(page, '/Volumes/NAS/Movies-3').count()) === 1);
      await context.close();
    }
  },

  // Mutant caught: waitingLong never set (a removal held up by a scan looks frozen).
  async 'removing: a long wait says the scan is being waited for'(browser) {
    const { context, page } = await open(browser, { del: () => ({ delay: 2600 }) });
    await removeNAS(page);
    const waiting = dialog(page).locator('p[role="status"]:has-text("Waiting for the scan to finish…")');
    await dialog(page).locator('footer button:has-text("Remove folder")').click();
    check('removing: the button shows it is working', await until(async () => (await textOf(dialog(page).locator('footer .btn.danger'))) === 'Removing…'));
    await sleep(700);
    check('not at once: nothing about a scan yet', (await waiting.count()) === 0);
    check('after 1.5 s it says it is waiting for the scan', await waiting.waitFor({ timeout: 3000 }).then(() => true, () => false));
    check('…and the folder is still listed', (await row(page, '/Volumes/NAS/Movies-3').count()) === 1);
    check('then the folder goes', await until(async () => (await row(page, '/Volumes/NAS/Movies-3').count()) === 0, 5000));
    check('…and the dialog closes', await until(async () => (await dialog(page).count()) === 0));
    await context.close();
  },

  // Mutants caught: a failure after the dialog was closed lost (no notice); a late 503 retried although
  // nobody is looking; the notice for a retype not saying it was not changed.
  async 'a failure after the dialog was closed is reported in the notice'(browser) {
    {
      const { context, page, calls } = await open(browser, { del: () => ({ delay: 1200, status: 500, error: 'the disk is on fire' }) });
      await removeNAS(page);
      await dialog(page).locator('footer button:has-text("Remove folder")').click();
      await until(() => count(calls, 'DELETE') === 1);
      await dialog(page).locator('footer button:has-text("Cancel")').click();
      check('remove: nothing in the notice yet', (await noticeText(page)) === ''); // control for the next
      check('remove: the dialog is gone while it is in flight', (await dialog(page).count()) === 0);
      check('remove: the late failure is reported', await until(async () => (await noticeText(page)).includes('was not removed'), 4000), await noticeText(page));
      check('remove: …with the reason', (await noticeText(page)).includes('the disk is on fire'), await noticeText(page));
      check('remove: the folder is still listed', (await row(page, '/Volumes/NAS/Movies-3').count()) === 1);
      await context.close();
    }
    {
      const { context, page, calls } = await open(browser, { patch: () => ({ delay: 1200, status: 500, error: 'the disk is on fire' }) });
      await retypeNAS(page);
      await dialog(page).locator('footer button:has-text("Read as TV shows")').click();
      await until(() => count(calls, 'PATCH') === 1);
      await dialog(page).locator('footer button:has-text("Cancel")').click();
      check('retype: nothing in the notice yet', (await noticeText(page)) === '');
      check('retype: the late failure is reported', await until(async () => (await noticeText(page)).includes('Movies-3 was not changed'), 4000), await noticeText(page));
      check('retype: …with the reason', (await noticeText(page)).includes('the disk is on fire'), await noticeText(page));
      await context.close();
    }
    {
      // A busy scan answered after the dialog closed: reported, and not retried behind the user's back.
      const { context, page, calls } = await open(browser, { del: () => ({ delay: 900, status: 503, error: BUSY, retryAfter: 1 }) });
      await removeNAS(page);
      await dialog(page).locator('footer button:has-text("Remove folder")').click();
      await until(() => count(calls, 'DELETE') === 1);
      await dialog(page).locator('footer button:has-text("Cancel")').click();
      const said = await until(async () => (await noticeText(page)).includes('was not removed'), 4000);
      check('late 503: the notice says it was not removed', said, await noticeText(page));
      check('late 503: …to try again later', (await noticeText(page)).includes('Try again in a moment'), await noticeText(page));
      await sleep(2000);
      check('late 503: no retry', count(calls, 'DELETE') === 1, `${count(calls, 'DELETE')}`);
      await context.close();
    }
  },

  // Mutants caught: refetchIfStale dropped, or refetching on every error; the refreshed list not shown
  // (a folder gone, a type now fixed). The success is the positive control for the GET counter.
  async 'a stale list is fetched again after a 404 or a 409'(browser) {
    const settled = async (gets, was) => {
      await until(() => gets.folders > was, 3000);
      await sleep(500); // and no more than that
      return gets.folders - was;
    };
    {
      // Retype: a 500 refetches nothing, then the same dialog succeeds and the list is fetched again.
      const { context, page, gets, calls } = await open(browser, { patch: (n) => (n === 1 ? { status: 500, error: 'try later' } : null) });
      await sleep(300);
      const was = gets.folders;
      await retypeNAS(page);
      await dialog(page).locator('footer button:has-text("Read as TV shows")').click();
      await alerts(page).waitFor();
      check('retype 500: the reason is shown', (await textOf(alerts(page))) === 'try later');
      check('retype 500: the list is not fetched again', (await settled(gets, was)) === 0, `${gets.folders - was}`);
      await dialog(page).locator('footer button:has-text("Read as TV shows")').click();
      await page.waitForFunction(() => !document.querySelector('dialog[open]'));
      check('retype ok: the list is fetched again', (await settled(gets, was)) === 1, `${gets.folders - was}`); // control
      check('retype ok: two PATCHes in all', count(calls, 'PATCH') === 2);
      await context.close();
    }
    {
      // Retype: a 409 (the type was fixed at startup meanwhile) fetches the list again, and it shows.
      const { context, page, gets } = await open(browser, { patch: () => ({ status: 409, error: 'the type of /Volumes/NAS/Movies-3 is set at startup' }) });
      await sleep(300);
      const was = gets.folders;
      const nas = row(page, '/Volumes/NAS/Movies-3');
      check('retype 409: the type can be changed before', await nas.locator('.seg button:has-text("TV shows")').isEnabled()); // control
      await retypeNAS(page);
      await dialog(page).locator('footer button:has-text("Read as TV shows")').click();
      await alerts(page).waitFor();
      check('retype 409: the reason is shown', (await textOf(alerts(page))).includes('is set at startup'));
      check('retype 409: the list is fetched again, once', (await settled(gets, was)) === 1, `${gets.folders - was}`);
      await dialog(page).locator('footer button:has-text("Cancel")').click();
      check('retype 409: the type is now fixed', await nas.locator('.seg button[aria-pressed="true"]').isDisabled());
      check('retype 409: …and Remove is gone', (await nas.locator('button:has-text("Remove")').count()) === 0);
      await context.close();
    }
    {
      // Remove: a 500 refetches nothing.
      const { context, page, gets } = await open(browser, { del: () => ({ status: 500, error: 'the disk is on fire' }) });
      await sleep(300);
      const was = gets.folders;
      await removeNAS(page);
      await dialog(page).locator('footer button:has-text("Remove folder")').click();
      await alerts(page).waitFor();
      check('remove 500: the list is not fetched again', (await settled(gets, was)) === 0, `${gets.folders - was}`);
      await context.close();
    }
    {
      // Remove: a 404 (someone else removed it) fetches the list again, and the folder is gone from it.
      const { context, page, gets } = await open(browser, { del: () => ({ status: 404, error: 'no such folder' }) });
      await sleep(300);
      const was = gets.folders;
      check('remove 404: the folder is listed before', (await row(page, '/Volumes/NAS/Movies-3').count()) === 1); // control
      await removeNAS(page);
      await dialog(page).locator('footer button:has-text("Remove folder")').click();
      await alerts(page).waitFor();
      check('remove 404: the reason is shown', (await textOf(alerts(page))) === 'no such folder');
      check('remove 404: the list is fetched again, once', (await settled(gets, was)) === 1, `${gets.folders - was}`);
      check('remove 404: the folder is gone from the list', await until(async () => (await row(page, '/Volumes/NAS/Movies-3').count()) === 0));
      await context.close();
    }
  },

  // Mutant caught: the 8 s timer of the notice dropped (it stays for ever) or shortened.
  async 'the notice goes away by itself after 8 s'(browser) {
    const { context, page } = await open(browser);
    await retypeNAS(page);
    await dialog(page).locator('footer button:has-text("Read as TV shows")').click();
    check('a notice appears', await until(async () => (await noticeText(page)).includes('Movies-3 is read as TV shows now.')), await noticeText(page));
    const t0 = Date.now();
    await sleep(6500);
    check('…and is still there after 6.5 s', (await noticeText(page)) !== '', await noticeText(page));
    const cleared = await until(async () => (await noticeText(page)) === '', 4000);
    const took = Date.now() - t0;
    check('…and goes by itself, at about 8 s', cleared && took > 7000 && took < 10500, `${took} ms`);
    await context.close();
  },

  // Mutants caught (C): the in-flight guard of add, of retype or of remove removed, whether or not the
  // button's disabled attribute stays (a double click sends two requests). "one task" is two clicks before
  // Svelte has redrawn, so only the guard in the function can stop the second.
  async 'in flight: a double click sends one request'(browser) {
    for (const [how, press] of [['one task', twice], ['dblclick', (b) => b.dblclick()]]) {
      {
        const { context, page, calls } = await open(browser, { post: () => ({ delay: 800 }) });
        await pickTV(page);
        await press(dialog(page).locator('footer .btn.primary'));
        await sleep(1300); // the 800 ms answer, and any second request
        check(`add, ${how}: one POST`, count(calls, 'POST') === 1, `${count(calls, 'POST')}`);
        check(`add, ${how}: and the folder was added`, (await row(page, '/Users/me/TV').count()) === 1); // control: a request was made
        await context.close();
      }
      {
        const { context, page, calls } = await open(browser, { patch: () => ({ delay: 800 }) });
        await retypeNAS(page);
        await press(dialog(page).locator('footer button:has-text("Read as TV shows")'));
        await sleep(1300);
        check(`retype, ${how}: one PATCH`, count(calls, 'PATCH') === 1, `${count(calls, 'PATCH')}`);
        check(`retype, ${how}: and the dialog closed`, (await dialog(page).count()) === 0);
        await context.close();
      }
      {
        const { context, page, calls } = await open(browser, { del: () => ({ delay: 800 }) });
        await removeNAS(page);
        await press(dialog(page).locator('footer button:has-text("Remove folder")'));
        await sleep(1300);
        check(`remove, ${how}: one DELETE`, count(calls, 'DELETE') === 1, `${count(calls, 'DELETE')}`);
        check(`remove, ${how}: and the folder went`, (await row(page, '/Volumes/NAS/Movies-3').count()) === 0);
        await context.close();
      }
    }
  },

  // Mutants caught (B1): attachToRunningScan left out of changed() (the folder stays at "No items yet."
  // until a reload); follow() not bumping libraryVersion when the scan ends.
  async 'adding: the scan it starts is followed and the card refreshes by itself'(browser) {
    // The add's own status calls take two of the running answers; follow() polls once more, a second later.
    const { context, page, gets } = await open(browser, { scanAfterAdd: 3 });
    await page.evaluate(() => (window.__sameDocument = true));
    await pickTV(page);
    await dialog(page).locator('footer .btn.primary').click();
    await row(page, '/Users/me/TV').waitFor();
    const scansBefore = gets.scan;
    check('at first the new folder has no items', (await row(page, '/Users/me/TV').textContent()).includes('No items yet.')); // control
    const refreshed = await page
      .waitForFunction(
        (path) => {
          const li = [...document.querySelectorAll('ul[aria-label="Library folders"] > li')].find((x) => x.querySelector('code')?.textContent === path);
          return /(^|\D)5 items\./.test(li?.textContent ?? '');
        },
        '/Users/me/TV',
        { timeout: 8000 },
      )
      .then(() => true, () => false);
    check('then the card shows 5 items, with no reload', refreshed);
    check('…and a scan time', refreshed && !(await row(page, '/Users/me/TV').textContent()).includes('Not scanned yet'));
    check('the page was not reloaded', await page.evaluate(() => window.__sameDocument === true));
    check('the status was polled until the scan ended', gets.scan >= scansBefore + 1, `${gets.scan - scansBefore} more calls`);
    await context.close();
  },

  // Mutant caught: ReviewEntry.episodes not shown.
  async 'review: a show lists its episodes with no number'(browser) {
    const review = [
      {
        kind: 'series', show: 'Lost', title: 'Lost', year: 0, matchState: 'matched', needsReview: true, score: 0.9,
        candidate: { id: 9, kind: 'series', title: 'Lost', year: 2004 },
        episodes: [{ id: 41, path: '/tv/Lost/Season 1/pilot.mkv' }, { id: 42, path: '/tv/Lost/Season 1/finale.mkv' }],
      },
      { kind: 'movie', itemId: 7, title: 'Plain', year: 2001, matchState: 'unmatched', needsReview: false, score: 0 },
    ];
    const { context, page } = await open(browser, { review });
    const lost = page.locator('ul.review > li', { has: page.locator('a.title:has-text("Lost")') });
    await lost.waitFor();
    check('the count of episodes', ((await lost.locator('summary').textContent()) ?? '').includes('2 episodes with no episode number'));
    await lost.locator('summary').click();
    const links = await lost.locator('details a').evaluateAll((as) => as.map((a) => `${a.getAttribute('href')} ${a.textContent}`));
    check('each episode links to its item', links.join('|') === '#/item/41 pilot.mkv|#/item/42 finale.mkv', links.join('|'));
    check('the match is not called unsure for it', !(await lost.textContent()).includes('not for sure'));
    const plain = page.locator('ul.review > li', { has: page.locator('a.title:has-text("Plain")') });
    check('a movie has no episode list', (await plain.locator('details').count()) === 0); // positive control above
    await context.close();
  },

  // Mutant caught: Scan button on folder absent; scan library row not on same row; explanatory text still present.
  async 'folder scan and scan library row layout'(browser) {
    const { context, page, calls } = await open(browser);
    const nas = row(page, '/Volumes/NAS/Movies-3');
    const scanBtn = nas.locator('button:has-text("Scan")');
    check('each folder has a Scan button', (await scanBtn.count()) === 1);
    const removeBtn = nas.locator('button:has-text("Remove")');
    check('Remove button is present', (await removeBtn.count()) === 1);

    const libActions = page.locator('.library-actions');
    check('library actions container is present', (await libActions.count()) === 1);
    check('Add a folder is inside library actions', (await libActions.locator('button:has-text("Add a folder")').count()) === 1);
    check('Scan library now is inside library actions before scan', (await libActions.locator('button:has-text("Scan library now")').count()) === 1);
    check('old explanatory text is gone', (await page.locator('text=Reads every folder again').count()) === 0);

    await scanBtn.click();
    await until(() => calls.some((c) => c.method === 'POST' && c.path === '/api/folders/2/scan'));
    check('clicking folder Scan calls POST /api/folders/2/scan', calls.some((c) => c.method === 'POST' && c.path === '/api/folders/2/scan'));
    check('Scan library now turns to Scanning… while scan is running', (await libActions.locator('button:has-text("Scanning…")').count()) === 1);

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
