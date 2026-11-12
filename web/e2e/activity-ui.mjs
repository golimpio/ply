// The Activity page and the header's indicator in a real browser, against a mocked API: the live
// feed (GET /api/events, answered as a short text/event-stream that EventSource reconnects to), the
// polling fallback, the tiers strip, and every action (cancel, run next, retry, pause and resume,
// stopping a play). The unit tests (lib/activity.test.ts) pin the event reducer; this pins the page.
//
// Self-contained: serves web/dist (build it first: `npm run build`; PLY_E2E_DIST picks another
// directory) and answers every /api/ request with page.route. Needs Google Chrome.
//
//   npm run build && node e2e/activity-ui.mjs
//
// PLY_E2E_ONLY=<text> runs the scenarios whose name contains it. Each scenario names the mutation it
// was confirmed to catch (build the mutant into another directory with
// `vite build --outDir ... --emptyOutDir` and point PLY_E2E_DIST at it).
import { createServer } from 'node:http';
import { existsSync, readFileSync } from 'node:fs';
import { dirname, extname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright-core';
import { eventStream, jobsSnapshot as snapshot, tierState as tier } from './mock-feed.mjs';

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

const ago = (sec) => new Date(Date.now() - sec * 1000).toISOString();
const job = (id, over = {}) => ({
  id, kind: 'meta.match', title: `Job ${id}`, priority: 20, state: 'queued', user: false, done: 0, total: 0, attempts: 0,
  createdAt: ago(600 - id), ...over,
});
const running = (id, over = {}) => job(id, { state: 'running', startedAt: ago(65), ...over });
const finished = (id, state, over = {}) => job(id, { state, startedAt: ago(400), finishedAt: ago(300), ...over });
const SYNTHETIC = { ...job(-1, { kind: 'scan.folders', title: 'Read library folders', state: 'running', priority: 0 }), startedAt: ago(20) };
const PLAY = { id: 'abc123', sessionId: 'abc123', itemId: 1, title: 'Alien', kind: 'transcode', client: 'Safari on Mac', startedAt: ago(900), lastSeenAt: ago(2), positionSec: 754, paused: false };

/**
 * A fresh page on the mocked server.
 *   snapshot   what GET /api/jobs and the event stream say (`st.snapshot` can be changed later)
 *   sessions   what GET /api/sessions lists
 *   events     false: GET /api/events answers 503 (the page must poll)
 *   feed       [type, data] events sent after the first snapshot only (session.started, ...)
 *   retry      the stream's retry: ms (default 300)
 *   refuse     { [id]: status } answers for job actions (default 204, retry 200)
 * `calls` holds the non-GET calls (not the browser registering itself, which `devices` holds);
 * `hits` counts the GETs of each path.
 */
async function open(browser, opts = {}, viewport = { width: 1280, height: 800 }) {
  const context = await browser.newContext({ viewport, serviceWorkers: 'block' });
  const page = await context.newPage();
  const st = { snapshot: opts.snapshot ?? snapshot(), sessions: opts.sessions ?? [] };
  const calls = [];
  const devices = [];
  const hits = {};
  const errors = [];
  let feed = opts.feed ?? [];
  page.on('pageerror', (e) => errors.push(e.message));
  const json = (route, body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  await page.route('**/api/**', async (route) => {
    const req = route.request();
    const p = new URL(req.url()).pathname;
    if (req.method() === 'PUT' && p.startsWith('/api/devices/')) {
      devices.push(p);
      return route.fulfill({ status: 204 });
    }
    if (req.method() !== 'GET') calls.push({ method: req.method(), path: p });
    else hits[p] = (hits[p] ?? 0) + 1;
    if (p === '/api/info') return json(route, { id: 'x', name: 'e2e', version: 'dev', authRequired: false });
    if (p === '/api/receivers') return json(route, []); // no AirPlay targets: the plain picker button
    if (p === '/api/scan') return json(route, { running: false, itemCount: 0, watching: [] });
    if (['/api/items', '/api/continue', '/api/shows', '/api/upcoming', '/api/genres'].includes(p)) return json(route, []);
    if (p === '/api/jobs') return json(route, st.snapshot);
    if (p === '/api/events') {
      if (opts.events === false) return json(route, { error: 'the background queue is not running' }, 503);
      // One snapshot, then the stream ends: EventSource reconnects after `retry` ms and gets the next.
      const answer = eventStream({ jobs: st.snapshot, sessions: st.sessions, retry: opts.retry ?? 300, events: feed });
      feed = [];
      return route.fulfill(answer);
    }
    if (p === '/api/sessions') return json(route, st.sessions);
    if (req.method() === 'DELETE' && p.startsWith('/api/sessions/')) {
      st.sessions = st.sessions.filter((s) => `/api/sessions/${s.id}` !== p);
      return route.fulfill({ status: 204 });
    }
    if (p === '/api/queue/pause' || p === '/api/queue/resume') {
      st.snapshot = { ...st.snapshot, paused: p.endsWith('pause') };
      return route.fulfill({ status: 204 });
    }
    const m = /^\/api\/jobs\/(-?\d+)\/(cancel|bump|retry)$/.exec(p);
    if (m) {
      const status = opts.refuse?.[m[1]];
      if (status) return json(route, { error: status === 409 ? 'not possible now: the job has moved on (reload the page)' : 'job not found' }, status);
      if (m[2] === 'retry') return json(route, job(900, { user: true, priority: 0 }));
      return route.fulfill({ status: 204 });
    }
    return json(route, { error: `unmocked ${p}` }, 404);
  });
  return { context, page, st, calls, devices, hits, errors };
}

const indicator = (page) => page.locator('header.bar a[href="#/activity"]');
const row = (page, id) => page.locator(`[data-job="${id}"]`);
const posted = (calls, path) => calls.some((c) => c.method === 'POST' && c.path === path);
async function waitFor(fn, timeout = 3000) {
  const end = Date.now() + timeout;
  while (Date.now() < end) {
    if (await fn()) return true;
    await sleep(50);
  }
  return false;
}

const scenarios = {
  // Mutant caught: the indicator's count taken from the queue instead of the running jobs, or its
  // state never leaving idle.
  async 'Indicator: quiet when idle, the count while jobs run, a click opens Activity'(browser) {
    {
      const { context, page } = await open(browser, { snapshot: snapshot() });
      await page.goto(`${base}/`);
      const a = indicator(page);
      await a.waitFor();
      await sleep(400); // the first snapshot has arrived
      check('idle: quiet', (await a.getAttribute('data-state')) === 'idle', await a.getAttribute('data-state'));
      check('idle: no count', (await a.locator('.n').count()) === 0);
      check('idle: it says so', (await a.getAttribute('aria-label')) === 'No background work', await a.getAttribute('aria-label'));
      await context.close();
    }
    const { context, page, hits } = await open(browser, {
      snapshot: snapshot({ running: [running(11), running(12)], queued: [job(21), job(22), job(23)] }),
    });
    await page.goto(`${base}/`);
    const a = indicator(page);
    const busy = await waitFor(async () => (await a.getAttribute('data-state')) === 'running');
    check('running: the busy state', busy, await a.getAttribute('data-state'));
    check('running: the count of running jobs', ((await a.locator('.n').textContent()) ?? '').trim() === '2', await a.locator('.n').textContent().catch(() => ''));
    const label = await a.getAttribute('aria-label');
    check('running: the label says what is going on', label === '2 background jobs running, 3 queued', label);
    check('running: the tooltip says the same', (await a.getAttribute('title')) === label);
    await a.click();
    await page.waitForSelector('h1:has-text("Activity")');
    check('a click opens #/activity', new URL(page.url()).hash === '#/activity', page.url());
    check('the indicator is marked as the current page', (await a.getAttribute('aria-current')) === 'page');
    check('it followed the event stream, not polling', (hits['/api/events'] ?? 0) >= 1 && !hits['/api/jobs'], JSON.stringify(hits));
    await context.close();
  },

  // Mutant caught: paused (or a recent failure) not shown in amber, i.e. data-state stays "running".
  async 'Indicator: amber when the queue is paused'(browser) {
    const { context, page } = await open(browser, { snapshot: snapshot({ paused: true, running: [running(11)], queued: [job(21)] }) });
    await page.goto(`${base}/`);
    const a = indicator(page);
    const ok = await waitFor(async () => (await a.getAttribute('data-state')) === 'paused');
    check('paused: amber', ok, await a.getAttribute('data-state'));
    check('paused: the label says so', (await a.getAttribute('aria-label'))?.startsWith('Background jobs paused'), await a.getAttribute('aria-label'));
    await context.close();
  },

  // Mutant caught: the page's markSeen effect removed (the failure light stays on after a visit).
  async 'Indicator: a recent failure lights it until Activity is visited'(browser) {
    const { context, page } = await open(browser, {
      snapshot: snapshot({ history: [finished(31, 'failed', { finishedAt: ago(60), error: 'TMDB timed out' })] }),
    });
    await page.goto(`${base}/`);
    const a = indicator(page);
    const lit = await waitFor(async () => (await a.getAttribute('data-state')) === 'failed');
    check('a failure a minute ago: lit', lit, await a.getAttribute('data-state'));
    await a.click();
    await page.waitForSelector('h1:has-text("Activity")');
    await page.goto(`${base}/#/`);
    await sleep(500);
    check('after the visit: no longer lit', (await a.getAttribute('data-state')) === 'idle', await a.getAttribute('data-state'));
    await context.close();
  },

  // Mutant caught: the held reason not rendered; Cancel posting for another id.
  async 'Running: the held reason is shown, and Cancel posts for that job'(browser) {
    const { context, page, calls } = await open(browser, {
      snapshot: snapshot({
        running: [running(11, { title: 'Scan the library', done: 120, total: 900, note: 'Movies/Alien (1979)' }), job(12, { title: 'Read new files in Shows', held: 'a video is playing from the same disk' })],
      }),
    });
    await page.goto(`${base}/#/activity`);
    await row(page, 12).waitFor();
    const hold = ((await row(page, 12).locator('.hold').textContent()) ?? '').trim();
    check('the held job says why it waits', hold === 'Waiting: a video is playing from the same disk', hold);
    check('the running one is not held', (await row(page, 11).locator('.hold').count()) === 0);
    const bar = row(page, 11).locator('[role="progressbar"]');
    check('a known total: a determinate bar', (await bar.getAttribute('aria-valuenow')) === '120' && (await bar.getAttribute('aria-valuemax')) === '900');
    check('done and total are shown', ((await row(page, 11).textContent()) ?? '').includes('120 of 900'));
    check('an unknown total: an indeterminate bar', (await row(page, 12).locator('[role="progressbar"]').getAttribute('aria-valuenow')) === null);
    check('the elapsed time is shown', /^1:0\d$/.test(((await row(page, 11).locator('.elapsed').textContent()) ?? '').trim()));
    await row(page, 12).locator('button:has-text("Cancel")').click();
    await waitFor(async () => calls.length > 0);
    check('Cancel posts to /api/jobs/12/cancel', posted(calls, '/api/jobs/12/cancel'), JSON.stringify(calls));
    check('and nothing else', calls.length === 1, JSON.stringify(calls));
    await context.close();
  },

  // Mutant caught: the isSynthetic guard dropped (work outside the queue gets a Cancel button).
  async 'Running: work outside the queue (a negative id) is shown, with no buttons'(browser) {
    const { context, page } = await open(browser, { snapshot: snapshot({ running: [running(11), SYNTHETIC] }) });
    await page.goto(`${base}/#/activity`);
    await row(page, -1).waitFor();
    check('it is listed', ((await row(page, -1).locator('.title').textContent()) ?? '').trim() === 'Read library folders');
    check('positive control: a queue job has Cancel', (await row(page, 11).locator('button:has-text("Cancel")').count()) === 1);
    check('it has no buttons', (await row(page, -1).locator('button').count()) === 0);
    await context.close();
  },

  // Mutant caught: Run next posting to cancel (or to the wrong id); the 409 message not shown inline.
  async 'Up next: Run next and Cancel, a retry time, and a refusal shown by the job'(browser) {
    const runAt = new Date(Date.now() + 30 * 60_000);
    const { context, page, calls } = await open(browser, {
      snapshot: snapshot({
        queued: [job(21), job(22), job(23, { attempts: 1, error: 'timeout', runAt: runAt.toISOString() }), job(24)],
      }),
      refuse: { 24: 409 },
    });
    await page.goto(`${base}/#/activity`);
    await row(page, 21).waitFor();
    check('the first has no "Run next" (it is next)', (await row(page, 21).locator('button:has-text("Run next")').count()) === 0);
    await row(page, 22).locator('button:has-text("Run next")').click();
    await waitFor(async () => calls.length > 0);
    check('Run next posts to /api/jobs/22/bump', posted(calls, '/api/jobs/22/bump'), JSON.stringify(calls));
    await row(page, 23).locator('button:has-text("Cancel")').click();
    await waitFor(async () => calls.length > 1);
    check('Cancel posts to /api/jobs/23/cancel', posted(calls, '/api/jobs/23/cancel'), JSON.stringify(calls));
    // The browser's own clock format: the page writes the time as the browser does.
    const hhmm = await page.evaluate((iso) => new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }), runAt.toISOString());
    const retry = ((await row(page, 23).locator('.retry').textContent()) ?? '').trim();
    check('a delayed retry says when', retry === `Retry at ${hhmm}`, retry);
    check('positive control: no retry time without one', (await row(page, 22).locator('.retry').count()) === 0);
    await row(page, 24).locator('button:has-text("Run next")').click();
    const refused = await row(page, 24).locator('[role="alert"]').waitFor({ timeout: 2000 }).then(() => true, () => false);
    check('a 409 is shown next to the job', refused && ((await row(page, 24).locator('[role="alert"]').textContent()) ?? '').includes('already started'));
    check('and only there', (await page.locator('[role="alert"]').count()) === 1);
    await context.close();
  },

  // Mutant caught: the filter ignored (Failed shows everything); Retry offered on a job that succeeded.
  async 'Recent: the failed filter, the error text, and Retry'(browser) {
    const { context, page, calls } = await open(browser, {
      snapshot: snapshot({
        history: [
          finished(31, 'done', { title: 'Scan the library' }),
          finished(32, 'failed', { title: 'Look up movie and show details', error: 'TMDB timed out' }),
          finished(33, 'cancelled', { title: 'Tidy the artwork and subtitle caches' }),
        ],
      }),
    });
    await page.goto(`${base}/#/activity`);
    await row(page, 31).waitFor();
    const rows = page.locator('.history > li');
    check('all: three', (await rows.count()) === 3);
    check('the error is shown', ((await row(page, 32).locator('.reason').textContent()) ?? '').trim() === 'TMDB timed out');
    check('the duration is shown', ((await row(page, 32).locator('.took').textContent()) ?? '').trim() === '2 min');
    check('done: no Retry', (await row(page, 31).locator('button:has-text("Retry")').count()) === 0);
    check('cancelled: Retry', (await row(page, 33).locator('button:has-text("Retry")').count()) === 1);
    await page.locator('[role="radio"]:has-text("Failed")').click();
    await sleep(100);
    const ids = await rows.evaluateAll((els) => els.map((e) => e.getAttribute('data-job')));
    check('failed: only the failure', ids.join(',') === '32', ids.join(','));
    await row(page, 32).locator('button:has-text("Retry")').click();
    await waitFor(async () => calls.length > 0);
    check('Retry posts to /api/jobs/32/retry', posted(calls, '/api/jobs/32/retry'), JSON.stringify(calls));
    await page.locator('[role="radio"]:has-text("All")').click();
    check('all again: three', (await rows.count()) === 3);
    await context.close();
  },

  // Mutant caught: the switch posting pause both ways.
  async 'Queue: Pause and Resume'(browser) {
    const { context, page, calls } = await open(browser, { snapshot: snapshot({ queued: [job(21)] }) });
    await page.goto(`${base}/#/activity`);
    const sw = page.locator('[role="switch"]');
    await waitFor(async () => (await sw.isEnabled()) && (await sw.getAttribute('aria-checked')) === 'true');
    check('on to start with', (await sw.getAttribute('aria-checked')) === 'true');
    await sw.click();
    await waitFor(async () => (await sw.getAttribute('aria-checked')) === 'false');
    check('Pause posts /api/queue/pause', posted(calls, '/api/queue/pause'), JSON.stringify(calls));
    check('it says what pausing means', ((await page.locator('.pause').textContent()) ?? '').includes('Paused: nothing new starts; running jobs finish.'));
    await sw.click();
    await waitFor(async () => (await sw.getAttribute('aria-checked')) === 'true');
    check('Resume posts /api/queue/resume', posted(calls, '/api/queue/resume'), JSON.stringify(calls));
    await context.close();
  },

  // Mutant caught: Stop deleting at once, with no confirmation.
  async 'Playing now: Stop asks first, then stops that play'(browser) {
    const { context, page, calls } = await open(browser, {
      snapshot: snapshot({ tier: tier({ Sessions: 1, Active: 1, Transcodes: 1, Software: 1 }, false, { held: true, reason: 'a video is being converted on the CPU' }) }),
      sessions: [PLAY],
    });
    await page.goto(`${base}/#/activity`);
    const play = page.locator('[data-session="abc123"]');
    await play.waitFor();
    check('the play is listed with its device and position', /Safari on Mac/.test((await play.textContent()) ?? '') && /At 12:34/.test((await play.textContent()) ?? ''));
    await play.locator('button:has-text("Stop")').click();
    await sleep(200);
    check('one click: nothing stopped yet', !calls.some((c) => c.method === 'DELETE'), JSON.stringify(calls));
    const confirm = play.locator('button:has-text("Stop playback")');
    check('it asks', (await confirm.count()) === 1);
    check('the confirmation has the focus', await confirm.evaluate((el) => el === document.activeElement));
    await play.locator('button:has-text("Keep playing")').click();
    await sleep(150);
    check('"Keep playing": nothing stopped', !calls.some((c) => c.method === 'DELETE') && (await confirm.count()) === 0);
    await play.locator('button:has-text("Stop")').click();
    await play.locator('button:has-text("Stop playback")').click();
    await waitFor(async () => calls.some((c) => c.method === 'DELETE'));
    check('confirmed: DELETE /api/sessions/abc123', calls.some((c) => c.method === 'DELETE' && c.path === '/api/sessions/abc123'), JSON.stringify(calls));
    const gone = await waitFor(async () => (await page.locator('[data-session="abc123"]').count()) === 0);
    check('it leaves the list', gone);
    await context.close();
  },

  // Mutants caught: the page polling GET /api/sessions while the feed is live; session events
  // not applied (the play would never show: the snapshot has none).
  async 'Playing now: follows the feed, with no polling'(browser) {
    // One stream, no reconnection within the test: the play can only come from its event.
    const { context, page, hits, devices } = await open(browser, { feed: [['session.started', { session: PLAY }]], retry: 600000 });
    await page.goto(`${base}/#/activity`);
    const shown = await page.locator('[data-session="abc123"]').waitFor({ timeout: 3000 }).then(() => true, () => false);
    check('a play told by session.started is listed', shown);
    await sleep(6000); // longer than the 5 s poll
    check('the feed was followed', (hits['/api/events'] ?? 0) >= 1, JSON.stringify(hits));
    // At most the one ask of a page that opens before the feed has said anything; no polling after.
    check('and GET /api/sessions not polled', (hits['/api/sessions'] ?? 0) <= 1, JSON.stringify(hits));
    check('the browser registered itself', devices.length >= 1 && /^\/api\/devices\/[A-Za-z0-9_-]{8,64}$/.test(devices[0]), JSON.stringify(devices));
    await context.close();
  },

  // Mutant caught: the headline not saying what the CPU is doing (describeTiers ignoring Software).
  async 'Tiers: the strip says plainly what has priority'(browser) {
    const { context, page } = await open(browser, {
      snapshot: snapshot({
        tier: tier({ Sessions: 1, Active: 1, Transcodes: 1, Software: 1 }, false, { held: true, reason: 'a video is being converted on the CPU' }),
        running: [job(12, { held: 'a video is being converted on the CPU' })],
      }),
    });
    await page.goto(`${base}/#/activity`);
    const headline = page.locator('.headline');
    await headline.waitFor();
    check('playing: the headline', ((await headline.textContent()) ?? '').trim() === 'Playing: 1 video (transcoding on the CPU). Background work waits.', await headline.textContent());
    const lanes = await page.locator('.lane').evaluateAll((els) => els.map((e) => `${e.getAttribute('data-lane')}:${e.className.match(/active|waiting|idle/)?.[0]}`));
    check('the lanes in priority order, with their states', lanes.join(' ') === 'playback:active browsing:idle queue:waiting', lanes.join(' '));
    await context.close();
    // Positive control: nothing playing.
    const idle = await open(browser, { snapshot: snapshot() });
    await idle.page.goto(`${base}/#/activity`);
    await idle.page.locator('.headline').waitFor();
    check('idle: the headline', ((await idle.page.locator('.headline').textContent()) ?? '').trim() === 'Nothing playing. Background work runs freely.');
    check('idle: the empty state gives direction', ((await idle.page.locator('.empty-note').first().textContent()) ?? '').includes('new files are picked up within seconds'));
    await idle.context.close();
  },

  // Mutant caught: no fallback when the stream is refused (the CLOSED branch of onerror removed).
  async 'Feed: polls GET /api/jobs when the event stream is refused'(browser) {
    const { context, page, st, hits } = await open(browser, { events: false, snapshot: snapshot({ running: [running(11, { title: 'Scan the library' })] }) });
    await page.goto(`${base}/#/activity`);
    const ok = await row(page, 11).waitFor({ timeout: 3000 }).then(() => true, () => false);
    check('the queue is shown from GET /api/jobs', ok);
    st.snapshot = snapshot({ running: [running(11), running(13, { title: 'Read new files in Movies' })] });
    const later = await row(page, 13).waitFor({ timeout: 4500 }).then(() => true, () => false);
    check('and kept up to date by polling', later && (hits['/api/jobs'] ?? 0) >= 2, `${hits['/api/jobs']} polls`);
    await context.close();
  },

  // Not a mutant: the page at phone width keeps every action reachable and nothing overflows.
  async 'Phone: 360 px wide, nothing overflows'(browser) {
    const { context, page } = await open(
      browser,
      {
        snapshot: snapshot({
          tier: tier({ Sessions: 1, Active: 1 }),
          running: [running(11, { title: 'Read new files in /Volumes/NAS/Media/Television/Some Very Long Show Name (2019)', total: 10, done: 3 }), SYNTHETIC],
          queued: [job(21), job(22)],
          history: [finished(32, 'failed', { error: 'open /Volumes/NAS/Media/x.mkv: input/output error' })],
        }),
        sessions: [PLAY],
      },
      { width: 360, height: 760 },
    );
    await page.goto(`${base}/#/activity`);
    await row(page, 32).waitFor();
    const wide = await page.evaluate(() => document.documentElement.scrollWidth);
    check('no sideways scroll', wide <= 360, `${wide}px`);
    check('Cancel is on screen', await row(page, 11).locator('button:has-text("Cancel")').isVisible());
    await context.close();
  },
};

// PLY_E2E_SHOTS=<dir>: screenshots of a busy Activity page, wide and at phone width, to look at.
if (process.env.PLY_E2E_SHOTS) {
  scenarios['Screenshots'] = async (browser) => {
    const busy = {
      snapshot: snapshot({
        // A CPU transcode holds CPU-heavy work (the folder read) but not light work (the scan's walk).
        tier: tier({ Sessions: 1, Active: 1, Transcodes: 1, Software: 1 }),
        running: [
          running(11, { kind: 'scan.full', title: 'Scan the library', done: 1840, total: 5210, note: 'Movies/Alien (1979)/Alien.mkv', priority: 30 }),
          job(12, { kind: 'scan.dirs', title: 'Read new files in Shows/Severance', held: 'a video is being converted on the CPU', priority: 10 }),
          SYNTHETIC,
        ],
        queued: [
          job(21, { title: 'Look up movie and show details', user: true, priority: 0 }),
          job(22, { kind: 'cache.prune', title: 'Tidy the artwork and subtitle caches', priority: 50, attempts: 1, error: 'disk busy', runAt: new Date(Date.now() + 9 * 60_000).toISOString() }),
          ...Array.from({ length: 14 }, (_, i) => job(30 + i, { title: `Look up details for file ${i + 1}`, priority: 40 })),
        ],
        history: [
          finished(51, 'done', { kind: 'scan.dirs', title: 'Read new files in Movies' }),
          finished(52, 'failed', { title: 'Look up movie and show details', error: 'TMDB did not answer in 10 s (network unreachable)' }),
          finished(53, 'cancelled', { kind: 'cache.prune', title: 'Tidy the artwork and subtitle caches' }),
          finished(54, 'done', { kind: 'scan.full', title: 'Scan the library', startedAt: ago(4000), finishedAt: ago(3700) }),
        ],
      }),
      sessions: [PLAY, { ...PLAY, id: 'def', title: 'Severance S02E03', kind: 'direct', client: 'Apple TV', positionSec: 1312, paused: true }],
    };
    for (const [name, viewport] of [['activity-1280', { width: 1280, height: 900 }], ['activity-390', { width: 390, height: 844 }]]) {
      const { context, page } = await open(browser, busy, viewport);
      await page.emulateMedia({ reducedMotion: 'reduce' });
      await page.goto(`${base}/#/activity`);
      await row(page, 54).waitFor();
      await sleep(300);
      await page.screenshot({ path: join(process.env.PLY_E2E_SHOTS, `${name}.png`), fullPage: true });
      await context.close();
    }
  };
}

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
