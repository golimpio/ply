// The File block of a film's and an episode's page, in a real browser against a mocked API: the
// facts of the file (its name, the folder that holds it, container, video, audio, duration, size,
// bitrate), what this browser will do with it and why, the streams and the subtitles. The folder is
// the server's (ItemDetail.folder, the directory of the file it read): a file whose `path` is a bare
// name still has one, and a file straight in a library folder has that folder, which the client
// cannot work out from the name.
//
// Self-contained: serves web/dist (build it first: `npm run build`; PLY_E2E_DIST picks another
// directory) and answers every /api/ request with page.route. Needs Google Chrome.
//
//   npm run build && node e2e/item-file-ui.mjs
//
// PLY_E2E_ONLY=<text> runs the scenarios whose name contains it. Each scenario names the mutants it
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
const FILM_FILE = 'Aquaman and the Lost Kingdom (2023) Bluray-1080p.mkv';
const FILM_DIR = '/Volumes/NAS/Movies/Aquaman and the Lost Kingdom (2023)';
const ROOT_FILE = 'Heat (1995) 2160p.mkv';
const ROOT_DIR = '/Volumes/NAS/Movies';
const EP_FILE = 'Cool Show S01E02 Pilot.mkv';
const EP_DIR = '/Volumes/NAS/TV/Cool Show/Season 1';

const row = { container: 'mkv', videoCodec: 'hevc', audioCodec: 'aac', width: 1920, height: 1080, progress: null, art: null, matchState: 'matched', needsReview: false };
const ITEMS = [
  { ...row, id: 1, kind: 'movie', title: 'Aquaman and the Lost Kingdom', year: 2023, durationSec: 7440, addedAt: 3 },
  { ...row, id: 2, kind: 'movie', title: 'Heat', year: 1995, durationSec: 10_680, addedAt: 2 },
  { ...row, id: 3, kind: 'episode', title: 'Pilot', episodeTitle: 'Pilot', show: SHOW, season: 1, episode: 2, durationSec: 2700, addedAt: 1 },
];

const VIDEO_1080 = { index: 0, type: 'video', codec: 'hevc', codecTag: 'hvc1', width: 1920, height: 1080, pixFmt: 'yuv420p', default: true };
const VIDEO_4K = { index: 0, type: 'video', codec: 'hevc', profile: 'Main 10', width: 3840, height: 2160, pixFmt: 'yuv420p10le', colorTransfer: 'smpte2084', default: true };
const AUDIO_AAC = { index: 1, type: 'audio', codec: 'aac', channels: 6, language: 'eng', title: 'Surround', default: true };
const AUDIO_TRUEHD = { index: 1, type: 'audio', codec: 'truehd', channels: 8, language: 'eng', title: 'TrueHD 7.1', default: true };
const SUB_SUBRIP = { index: 3, type: 'subtitle', codec: 'subrip', language: 'eng' };

const HEAT_REASONS = ['HEVC Main 10 with HDR is not played by this browser', 'TrueHD audio is not played by this browser'];
const DETAIL = {
  1: {
    path: FILM_FILE, folder: FILM_DIR, durationSec: 7440, size: 7_623_566_950, bitrate: 8_200_000,
    streams: [VIDEO_1080, AUDIO_AAC, { index: 2, type: 'audio', codec: 'ac3', channels: 2, language: 'fra' }, SUB_SUBRIP],
    subtitles: [{ key: '3', language: 'eng', title: 'English', format: 'subrip', image: false }],
    playback: { method: 'direct', videoAction: 'copy', audioAction: 'copy', audioIndex: 1, startSec: 0, url: '', reasons: [] },
  },
  2: {
    path: ROOT_FILE, folder: ROOT_DIR, durationSec: 10_680, size: 41_234_567_890, bitrate: 32_000_000,
    streams: [VIDEO_4K, AUDIO_TRUEHD, SUB_SUBRIP],
    subtitles: [],
    playback: { method: 'transcode', videoAction: 'transcode', audioAction: 'transcode', audioIndex: 1, startSec: 0, url: '', reasons: HEAT_REASONS },
  },
  3: {
    path: EP_FILE, folder: EP_DIR, durationSec: 2700, size: 2_147_483_648, bitrate: 3_000_000,
    streams: [VIDEO_1080, AUDIO_AAC, SUB_SUBRIP],
    subtitles: [{ key: '3', language: 'eng', title: 'English', format: 'subrip', image: false }],
    playback: { method: 'remux', videoAction: 'copy', audioAction: 'copy', audioIndex: 1, startSec: 0, url: '', reasons: ['container mkv is remuxed for this browser'] },
  },
};
const detail = (it) => ({ ...it, ...DETAIL[it.id] });

/** A fresh page on the mocked server; `errors` collects the page's own exceptions. */
async function open(browser) {
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 }, serviceWorkers: 'block' });
  const page = await context.newPage();
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  const json = (route, body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  await page.route('**/api/**', async (route) => {
    const p = new URL(route.request().url()).pathname;
    if (p.startsWith('/api/images/')) return route.fulfill({ status: 404, body: '' });
    if (p === '/api/info') return json(route, { id: 'x', name: 'e2e', version: 'dev', authRequired: false });
    if (p === '/api/scan') return json(route, { running: false, itemCount: ITEMS.length, watching: [] });
    if (p === '/api/items') return routeItems(route, ITEMS);
    if (p === '/api/continue' || p === '/api/upcoming' || p === '/api/genres') return json(route, []);
    if (p === '/api/shows') return json(route, [{ show: SHOW, seasons: [1], episodeCount: 1, matchState: 'matched', needsReview: false }]);
    const m = /^\/api\/items\/(\d+)$/.exec(p);
    if (m) {
      const it = ITEMS.find((i) => i.id === Number(m[1]));
      return it ? json(route, detail(it)) : json(route, { error: 'not found' }, 404);
    }
    return json(route, { error: `unmocked ${p}` }, 404);
  });
  return { context, page, errors };
}

// ---- reading the block ---------------------------------------------------------------------------

const NAME = 'Aquaman and the Lost Kingdom (2023) Bluray-1080p.mkv';
// "He says the name, the folder it is in, and what the file is": the labels, in the order they read.
const LABELS = ['Name', 'Folder', 'Container', 'Video', 'Audio', 'Duration', 'Size', 'Bitrate'];

const factsReady = (page) => page.locator('table.file-facts').waitFor({ timeout: 5000 });
const labels = (page) => page.$$eval('table.file-facts th', (ths) => ths.map((th) => th.textContent.trim()));
const facts = (page) =>
  page.$$eval('table.file-facts tr', (rows) =>
    Object.fromEntries(rows.map((r) => [r.querySelector('th')?.textContent.trim() ?? '', r.querySelector('td')?.textContent.trim() ?? ''])));
const block = (page, title) => page.locator('section.file-block').filter({ has: page.getByRole('heading', { name: title, exact: true, level: 3 }) });
const rowsOf = (page, title) => block(page, title).locator('tbody tr');
const cellsOf = (rowLocator) => rowLocator.locator('td').allTextContents().then((cells) => cells.map((c) => c.trim()));

const scenarios = {
  // Mutants caught: the Folder row bound to `path` (the name again, under another label); the row
  // hidden when it has no value ({#if folder}); the facts built from the item summary's codecs
  // rather than the detail's; the folder row moved below Container.
  async 'a film: its name, the folder it is in, and what the file is'(browser) {
    const { context, page, errors } = await open(browser);
    await page.goto(`${base}/#/item/1`);
    await factsReady(page);
    const f = await facts(page);
    check('the name is the file’s name', f.Name === NAME, f.Name);
    check('the folder is the one the server read', f.Folder === FILM_DIR, f.Folder);
    check('the folder is the line under the name', (await labels(page)).join('|') === LABELS.join('|'), (await labels(page)).join('|'));
    check('container', f.Container === 'mkv', f.Container);
    check('video: codec and resolution', f.Video === 'HEVC, 1920×1080', f.Video);
    check('audio: the codec', f.Audio === 'AAC', f.Audio);
    check('duration', f.Duration === '2h 4m', f.Duration);
    check('size', f.Size === '7.1 GB', f.Size);
    check('bitrate', f.Bitrate === '8.2 Mbps', f.Bitrate);
    check('no page error', errors.length === 0, errors.join('; '));
    await context.close();
  },

  // The reported case: the file sits in a folder of the library, so its name alone says nothing of
  // where it is. Mutants caught: the folder derived from `path` on the client; the row left out when
  // there is nothing to derive; the playback sentence built from the wrong actions.
  async 'a file straight in the library folder still shows the folder it is in'(browser) {
    const { context, page, errors } = await open(browser);
    await page.goto(`${base}/#/item/2`);
    await factsReady(page);
    const f = await facts(page);
    check('the name has no folder in it', f.Name === ROOT_FILE && !f.Name.includes('/'), f.Name);
    check('the folder is still the file’s folder', f.Folder === ROOT_DIR, f.Folder);
    check('the method is named in the header', (await page.locator('.file-head .badge').textContent())?.trim() === 'Transcode (HLS)');
    check('the method is said in a sentence', (await page.locator('.plays').textContent())?.trim() === 'Video is re-encoded, audio is re-encoded.');
    const reasons = await block(page, 'Why this playback method').locator('li').allTextContents();
    check('the reasons are listed', reasons.length === 2 && reasons[0] === HEAT_REASONS[0], reasons.join(' | '));
    const streams = await rowsOf(page, 'Streams').count();
    check('the streams are the video and audio ones', streams === 2, String(streams));
    const video = await cellsOf(rowsOf(page, 'Streams').nth(0));
    check('a video stream: codec, details, flags', video[2] === 'HEVC' && video[3] === '3840×2160, Main 10, yuv420p10le, PQ (HDR10)' && video[6] === 'default', video.join(' | '));
    const audio = await cellsOf(rowsOf(page, 'Streams').nth(1));
    check('an audio stream: channels as a word, its language and title', audio[3] === '7.1' && audio[4] === 'eng' && audio[5] === 'TrueHD 7.1', audio.join(' | '));
    const subs = await block(page, 'Subtitles').locator('.muted').allTextContents();
    check('no subtitles says so', subs.some((t) => t.trim() === 'No subtitles attached to this file.'), subs.join(' | '));
    check('no page error', errors.length === 0, errors.join('; '));
    await context.close();
  },

  // Mutants caught: the folder taken from the show's own path rather than the file's (a season's
  // folder is not the show's); an episode's page without the File block.
  async 'an episode: the folder of its own file, in its season'(browser) {
    const { context, page, errors } = await open(browser);
    await page.goto(`${base}/#/item/3`);
    await factsReady(page);
    const f = await facts(page);
    check('the name is the episode file’s', f.Name === EP_FILE, f.Name);
    check('the folder is the season’s folder', f.Folder === EP_DIR, f.Folder);
    check('the page is the episode', (await page.locator('.hero .meta').textContent())?.includes('S01E02'));
    check('the subtitles are listed', (await block(page, 'Subtitles').locator('li').count()) === 1);
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
