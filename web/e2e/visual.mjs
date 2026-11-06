// Screenshots of the web UI, for comparing two builds pixel for pixel: a fixed set of pages and
// states (the library pages, a film, a show, Settings, Activity, the dialogs, the search palette,
// the player and its cap panel, a few focus rings), each at 360 and 1280 px wide, against a mocked
// API. Nothing here checks behaviour; the other harnesses do that. The point is that a change meant
// to look the same (a CSS refactor) gives byte-identical PNGs.
//
// Self-contained: serves web/dist (PLY_E2E_DIST picks another directory), answers every /api/
// request with page.route, draws its artwork as SVG and builds a 10 s clip with ffmpeg. Needs Google
// Chrome and ffmpeg on PATH.
//
//   npm run build && PLY_VISUAL_OUT=/tmp/shots node e2e/visual.mjs
//
// PLY_VISUAL_OUT (required) is where the PNGs go, one per shot and width (home-360.png, ...).
// PLY_E2E_ONLY=<text>[,<text>...] takes only the shots whose name contains one of them.
//
// Deterministic by construction: a fake clock (page.clock, so Date and the timers only move when
// this script moves them), reduced motion, animations stopped and carets hidden for each shot, a
// fixed locale and time zone, every image loaded and decoded first, and a fresh context per shot.
// Chrome runs in software, and backdrop-filter is off in the pictures (see unblur). Two runs on one
// build must give the same bytes; compare before/after with `cmp` (the .glass.txt files too).
import { createServer } from 'node:http';
import { execFileSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, extname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright-core';

const here = dirname(fileURLToPath(import.meta.url));
const dist = process.env.PLY_E2E_DIST ?? join(here, '..', 'dist');
if (!existsSync(join(dist, 'index.html'))) {
  console.error(`No build in ${dist}: run \`npm run build\` first.`);
  process.exit(2);
}
const out = process.env.PLY_VISUAL_OUT;
if (!out) {
  console.error('Set PLY_VISUAL_OUT to the directory for the screenshots.');
  process.exit(2);
}
mkdirSync(out, { recursive: true });

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function until(what, fn, ms = 10000) {
  const end = Date.now() + ms;
  for (;;) {
    if (await fn()) return;
    if (Date.now() > end) throw new Error(`timed out waiting for ${what}`);
    await sleep(40);
  }
}

// ---- media: a 10 s clip of one flat colour (decodes to the same pixels every time) ------------------

const work = mkdtempSync(join(tmpdir(), 'ply-visual-'));
const clipFile = join(work, 'clip.mp4');
execFileSync('ffmpeg', ['-v', 'error', '-y', '-f', 'lavfi', '-i', 'color=c=0x2b3a4a:s=320x180:r=10:d=10', '-f', 'lavfi', '-i', 'sine=f=440:d=10',
  '-c:v', 'libx264', '-preset', 'ultrafast', '-pix_fmt', 'yuv420p', '-c:a', 'aac', '-b:a', '32k', '-movflags', '+faststart', clipFile]);
const clip = readFileSync(clipFile);

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

// ---- artwork: an SVG per name, its colours from the name ----------------------------------------------

const hue = (s) => [...s].reduce((h, c) => (h * 31 + c.charCodeAt(0)) % 360, 7);
function artwork(name) {
  const [w, h] = /-(poster|profile)$/.test(name) ? [300, 450] : /-logo$/.test(name) ? [400, 120] : [1280, 720];
  const a = hue(name);
  if (/-logo$/.test(name)) {
    return `<svg xmlns="http://www.w3.org/2000/svg" width="${w}" height="${h}"><rect x="10" y="30" width="380" height="60" rx="12" fill="hsl(${a} 70% 70%)"/></svg>`;
  }
  return `<svg xmlns="http://www.w3.org/2000/svg" width="${w}" height="${h}" viewBox="0 0 ${w} ${h}">
<defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="hsl(${a} 55% 42%)"/><stop offset="1" stop-color="hsl(${(a + 60) % 360} 45% 16%)"/></linearGradient></defs>
<rect width="${w}" height="${h}" fill="url(#g)"/><circle cx="${w * 0.7}" cy="${h * 0.35}" r="${Math.min(w, h) * 0.22}" fill="hsl(${(a + 180) % 360} 60% 60%)" opacity="0.55"/>
<rect x="${w * 0.08}" y="${h * 0.7}" width="${w * 0.5}" height="${h * 0.12}" fill="hsl(${a} 30% 85%)" opacity="0.35"/></svg>`;
}
const img = (name, size = 'w342') => `/api/images/tmdb/${size}/${name}.jpg`;

// ---- the mocked server: one library for every page ---------------------------------------------------

const FIXED = Date.parse('2026-11-06T09:30:00Z');
const ago = (sec) => new Date(FIXED - sec * 1000).toISOString();
const DAY = 86_400;
const entry = (source, value, scale, over = {}) => ({ source, value, scale, score100: Math.round((value / scale) * 100), url: '', fetchedAt: ago(3 * DAY), ...over });
const RATINGS = [
  entry('imdb', 8.3, 10, { votes: 712_000, url: 'https://www.imdb.com/title/tt0113277/' }),
  entry('rt_critics', 88, 100, { state: 'certified', url: 'https://www.rottentomatoes.com/m/heat_1995' }),
  entry('rt_audience', 94, 100, { state: 'upright', votes: 250_000, url: 'https://www.rottentomatoes.com/m/heat_1995' }),
  entry('metacritic', 76, 100, { url: 'https://www.metacritic.com/movie/heat-1995/' }),
  entry('letterboxd', 4.2, 5, { votes: 300_000, url: 'https://letterboxd.com/film/heat-1995/' }),
  entry('trakt', 84, 100, { votes: 40_000, url: 'https://trakt.tv/movies/heat-1995' }),
  entry('tmdb', 7.9, 10, { votes: 7000 }),
];

const movie = (id, title, year, over = {}) => ({
  id, kind: 'movie', title, year, durationSec: 6000 + id * 431, container: 'mkv', videoCodec: 'h264', audioCodec: 'aac',
  width: 1920, height: 1080, addedAt: 1_780_000_000 + id, progress: null, art: null, titleId: 100 + id, matchState: 'matched', needsReview: false,
  genres: ['Crime', 'Drama'], rating: null, ...over,
});
const episode = (id, show, season, ep, over = {}) => ({
  id, kind: 'episode', title: show, show, season, episode: ep, episodeTitle: `Episode title ${ep}`, durationSec: 2700 + ep * 60, container: 'mkv',
  videoCodec: 'h264', audioCodec: 'aac', width: 1920, height: 1080, addedAt: 1_780_000_100 + id, progress: null, art: null,
  titleId: show === 'Lost' ? 77 : 78, matchState: 'matched', needsReview: false, overview: `What happens in ${show} ${season}x${ep}.`,
  still: img(`${show}-${season}-${ep}-still`, 'w500'), ratings: [], ...over,
});
const ITEMS = [
  movie(1, 'Heat', 1995, { width: 3840, height: 2160, hdr: 'hdr10', poster: img('heat-poster'), backdrop: img('heat-backdrop', 'w1280'), logo: img('heat-logo', 'w500'),
    certification: 'R', art: { backdrop: img('heat-backdrop', 'w1280'), thumb: img('heat-backdrop', 'w500'), color: '#b5652e' }, rating: RATINGS[0] }),
  movie(6, 'Heat', 1995, { titleId: 101, poster: img('heat-poster'), backdrop: img('heat-backdrop', 'w1280'), rating: RATINGS[0] }),
  movie(2, 'Alien', 1979, { poster: img('alien-poster'), backdrop: img('alien-backdrop', 'w1280'), genres: ['Horror', 'Science Fiction'], rating: entry('rt_critics', 98, 100, { state: 'certified' }) }),
  movie(3, 'Brazil', 1985, { poster: img('brazil-poster'), backdrop: img('brazil-backdrop', 'w1280'), genres: ['Drama'], rating: entry('letterboxd', 4.1, 5) }),
  movie(4, 'Cube', 1997, { poster: img('cube-poster'), genres: ['Science Fiction'], rating: entry('imdb', 7.2, 10), needsReview: true }),
  movie(5, 'Dune', 2021, { poster: img('dune-poster'), backdrop: img('dune-backdrop', 'w1280'), progress: { positionSec: 2400, watched: false } }),
  movie(7, 'A Film With No Artwork At All', 2003, { genres: [] }),
  movie(8, 'Gigli', 2003, { poster: img('gigli-poster'), genres: ['Comedy'], rating: entry('rt_critics', 6, 100, { state: 'rotten' }) }),
  episode(11, 'Lost', 1, 1, { progress: { positionSec: 2700, watched: true }, ratings: [entry('imdb', 9.1, 10, { votes: 30_000 })], rating: entry('imdb', 9.1, 10) }),
  episode(12, 'Lost', 1, 2, { progress: { positionSec: 900, watched: false } }),
  episode(13, 'Lost', 1, 3),
  episode(14, 'Lost', 2, 1),
  episode(21, 'Dark', 1, 1, { still: undefined }),
];
const SHOWS = [
  { show: 'Dark', seasons: [1], episodeCount: 1, matchState: 'matched', needsReview: false, poster: img('dark-poster'), backdrop: img('dark-backdrop', 'w1280'), rating: entry('rt_audience', 85, 100), year: 2017 },
  { show: 'Lost', seasons: [1, 2], episodeCount: 4, matchState: 'matched', needsReview: false, poster: img('lost-poster'), backdrop: img('lost-backdrop', 'w1280'), rating: entry('imdb', 8.3, 10), year: 2004 },
];
const card = (it, over = {}) => ({ id: it.titleId, kind: 'movie', title: it.title, year: it.year, poster: it.poster, backdrop: it.backdrop, rating: it.rating, itemId: it.id, ...over });
const CAST = [
  { personId: 31, name: 'Al Pacino', character: 'Vincent Hanna', profile: img('pacino-profile', 'w185') },
  { personId: 32, name: 'Robert De Niro', character: 'Neil McCauley', profile: img('deniro-profile', 'w185') },
  { personId: 33, name: 'Val Kilmer', character: 'Chris Shiherlis' },
  { personId: 34, name: 'Ashley Judd', character: 'Charlene Shiherlis', profile: img('judd-profile', 'w185') },
];
const CREW = [
  { personId: 40, name: 'Michael Mann', job: 'Director' },
  { personId: 40, name: 'Michael Mann', job: 'Screenplay' },
  { personId: 41, name: 'Art Linson', job: 'Producer' },
];
const HEAT_META = {
  id: 101, kind: 'movie', title: 'Heat', year: 1995, itemId: 1, sortTitle: 'Heat', poster: img('heat-poster'), backdrop: img('heat-backdrop', 'w1280'),
  logo: img('heat-logo', 'w500'), tagline: 'A Los Angeles crime saga.', releaseDate: '1995-12-15', runtimeMin: 170, certification: 'R',
  overview: 'Obsessive master thief Neil McCauley leads a top-notch crew on various daring heists throughout Los Angeles while a mentally unstable detective, Vincent Hanna, pursues him without rest.',
  genres: [{ id: 80, name: 'Crime' }, { id: 18, name: 'Drama' }], collection: { id: 9, name: 'The Mann Collection' }, cast: CAST, crew: CREW,
  source: 'tmdb', locked: false, matchState: 'matched', needsReview: false, rating: RATINGS[0], ratings: RATINGS, tmdbId: 949, imdbId: 'tt0113277',
};
const HEAT_DETAIL = {
  ...ITEMS[0], path: '/Volumes/NAS/Movies/Heat (1995)/Heat (1995) 2160p.mkv', size: 41_234_567_890, bitrate: 32_000_000,
  streams: [
    { index: 0, type: 'video', codec: 'hevc', codecTag: 'hvc1', profile: 'Main 10', width: 3840, height: 2160, pixFmt: 'yuv420p10le', colorTransfer: 'smpte2084', default: true },
    { index: 1, type: 'audio', codec: 'truehd', channels: 8, language: 'eng', title: 'TrueHD 7.1', default: true },
    { index: 2, type: 'audio', codec: 'ac3', channels: 6, language: 'fra' },
    { index: 3, type: 'subtitle', codec: 'subrip', language: 'eng', title: 'English' },
    { index: 4, type: 'subtitle', codec: 'hdmv_pgs_subtitle', language: 'eng', forced: true },
  ],
  subtitles: [
    { key: '3', language: 'eng', title: 'English', format: 'subrip', image: false, default: false },
    { key: '4', language: 'eng', format: 'pgs', image: true, forced: true },
  ],
  playback: { method: 'transcode', videoAction: 'transcode', audioAction: 'transcode', audioIndex: 1, startSec: 0, url: '',
    reasons: ['HEVC Main 10 with HDR is not played by this browser', 'TrueHD audio is not played by this browser'] },
  meta: HEAT_META,
};
const PLAYER_DETAIL = {
  ...HEAT_DETAIL, durationSec: 10, width: 320, height: 180, videoCodec: 'h264', audioCodec: 'aac', container: 'mp4',
  streams: [
    { index: 0, type: 'video', codec: 'h264', width: 320, height: 180 },
    { index: 1, type: 'audio', codec: 'aac', channels: 2, language: 'eng', default: true },
    { index: 2, type: 'audio', codec: 'aac', channels: 2, language: 'fra' },
  ],
  subtitles: [{ key: '3', language: 'eng', title: 'English', format: 'subrip', image: false, url: '/api/items/1/subtitles/3.vtt' }],
  playback: { method: 'direct', videoAction: 'copy', audioAction: 'copy', audioIndex: 1, reasons: [], url: '', startSec: 0 },
};
const LOST_META = {
  id: 77, kind: 'series', title: 'Lost', year: 2004, endDate: '2010-05-23', status: 'Ended', sortTitle: 'Lost', poster: img('lost-poster'), backdrop: img('lost-backdrop', 'w1280'),
  overview: 'Stripped of everything, the survivors of a horrific plane crash must work together to stay alive.', genres: [{ id: 18, name: 'Drama' }, { id: 9648, name: 'Mystery' }],
  cast: CAST.slice(2), crew: [{ personId: 50, name: 'J. J. Abrams', job: 'Creator' }], source: 'tvdb', tvdbId: 73739, episodeOrder: 'aired', locked: false,
  matchState: 'matched', needsReview: false, rating: entry('imdb', 8.3, 10), ratings: [entry('imdb', 8.3, 10, { votes: 600_000 }), entry('tmdb', 7.9, 10, { votes: 4000 })],
};
const LOST_DETAIL = {
  show: 'Lost', episodeCount: 4, year: 2004, poster: img('lost-poster'), backdrop: img('lost-backdrop', 'w1280'), rating: LOST_META.rating, meta: LOST_META,
  seasons: [
    { season: 1, name: 'Season 1', overview: 'The survivors find their footing on the island.', poster: img('lost-s1-poster'), airDate: '2004-09-22', episodeCount: 3 },
    { season: 2, name: 'Season 2', poster: img('lost-s2-poster'), airDate: '2005-09-21', episodeCount: 1 },
  ],
};
const GENRES = [{ id: 18, name: 'Drama', count: 5 }, { id: 80, name: 'Crime', count: 4 }, { id: 878, name: 'Science Fiction', count: 2 }];
const byId = Object.fromEntries(ITEMS.map((i) => [i.id, i]));
const GENRE_TITLES = {
  18: [card(byId[1]), card(byId[3]), card(byId[5]), { id: 77, kind: 'series', title: 'Lost', year: 2004, poster: img('lost-poster'), show: 'Lost', rating: LOST_META.rating }, card(byId[7])],
  80: [card(byId[1]), card(byId[2]), card(byId[4]), card(byId[5])],
};
const COLLECTION = {
  id: 9, name: 'The Mann Collection', overview: 'Crime films by Michael Mann, in the order they came out.', poster: img('mann-poster'), backdrop: img('mann-backdrop', 'w1280'),
  titles: [card(byId[1]), { id: 120, kind: 'movie', title: 'Collateral', year: 2004, poster: img('collateral-poster') }, card(byId[3], { title: 'Thief', year: 1981 })],
};
const PERSON = {
  id: 31, name: 'Al Pacino', profile: img('pacino-profile', 'w185'), knownFor: 'Acting', birthday: '1940-04-25', placeOfBirth: 'New York City, New York, USA', tmdbId: 1158,
  biography: 'Alfredo James Pacino is an American actor. Considered one of the greatest actors of the twentieth century, he has received many awards.\n\nHe began his career on stage in the 1960s and moved to film soon after. '.repeat(3),
  credits: [
    { titleId: 101, kind: 'cast', character: 'Vincent Hanna', title: card(byId[1]) },
    { titleId: 103, kind: 'cast', character: 'Sam Lowry', title: card(byId[3]) },
    { titleId: 77, kind: 'crew', job: 'Producer', title: { id: 77, kind: 'series', title: 'Lost', year: 2004, poster: img('lost-poster'), show: 'Lost' } },
  ],
};
const PEOPLE = [{ id: 31, name: 'Al Pacino', profile: img('pacino-profile', 'w185'), knownFor: 'Acting' }, { id: 33, name: 'Val Kilmer', knownFor: 'Acting' }];
const UPCOMING = [{ titleId: 78, show: 'Dark', poster: img('dark-poster'), backdrop: img('dark-backdrop', 'w1280'), nextAirDate: '2026-11-08', nextEpisode: { season: 2, episode: 1, name: 'Beginnings and Endings' } }];
const CANDIDATES = [
  { source: 'tmdb', id: 949, tmdbId: 949, title: 'Heat', year: 1995, overview: 'Obsessive master thief Neil McCauley leads a top-notch crew.', poster: img('heat-poster', 'w92'), score: 0.98 },
  { source: 'tmdb', id: 11_843, tmdbId: 11_843, title: 'Heat', originalTitle: 'Heat', year: 1986, overview: 'A Las Vegas bodyguard with a gambling problem.', score: 0.71 },
  { source: 'tmdb', id: 3_001, tmdbId: 3_001, title: 'The Heat', year: 2013, score: 0.52 },
];
const REVIEW = [
  { kind: 'movie', itemId: 7, title: 'A Film With No Artwork At All', year: 2003, matchState: 'unmatched', needsReview: false, score: 0 },
  { kind: 'movie', itemId: 4, title: 'Cube', year: 1997, matchState: 'matched', needsReview: true, score: 0.62, candidate: { id: 104, kind: 'movie', title: 'Cube Zero', year: 2004, poster: img('cube-poster') } },
  { kind: 'series', show: 'Dark', title: 'Dark', year: 2017, matchState: 'matched', needsReview: false, score: 0.9,
    episodes: [{ id: 21, path: '/Volumes/NAS/TV/Dark/Dark - Secrets.mkv' }] },
];
const folder = (id, path, kind, extra = {}) => ({ id, path, kind, guessed: false, source: 'ui', kindLocked: false, items: 0, lastScanAt: 0, state: 'ok', error: '', ...extra });
const FOLDERS = [
  folder(1, '/srv/films', 'movies', { source: 'flag', kindLocked: true, items: 40, lastScanAt: FIXED / 1000 - 3600 }),
  folder(2, '/Volumes/NAS/Movies', 'movies', { guessed: true, items: 263, lastScanAt: FIXED / 1000 - 7200 }),
  folder(0, '/srv/mixed', '', { source: 'flag' }),
  folder(3, '/Volumes/Gone/TV', 'shows', { state: 'missing', items: 12 }),
];
const TREE = { '/Users/me': ['Movies', 'TV', 'Documents and other things'], '/Users/me/Movies': [], '/Users/me/TV': ['Lost (2004)'], '/Volumes/USB': ['Kids'] };
const SOURCES = ['imdb', 'rt_critics', 'rt_audience', 'metacritic', 'letterboxd', 'trakt', 'tmdb'];
const SETTINGS = {
  tmdbKeySet: true, tmdbKeySource: 'settings', metadataEnabled: true, metadataForcedOff: false, language: 'en-US',
  maxStreams: 4, maxTranscodes: 2, maxStreamsSource: 'settings', maxTranscodesSource: 'flag',
  tvdbKeySet: true, tvdbPinSet: false, tvdbKeySource: 'env', tvdbPinSource: '', tvdbAttribution: 'Metadata provided by TheTVDB. Please consider adding missing information or subscribing.', tvdbAttributionUrl: 'https://thetvdb.com',
  mdblistKeySet: true, mdblistKeySource: 'settings', ratingsPrimary: 'imdb', ratingsShown: ['imdb', 'rt_critics', 'rt_audience', 'metacritic', 'letterboxd', 'tmdb'], ratingsAvailable: SOURCES,
  imdbAttribution: 'Information courtesy of IMDb (https://www.imdb.com). Used with permission.', imdbAttributionUrl: 'https://www.imdb.com',
  mdblistAttribution: 'Ratings from Rotten Tomatoes, Metacritic, Letterboxd and Trakt provided by MDBList.', mdblistAttributionUrl: 'https://mdblist.com',
  imdbDatasetUpdatedAt: '2026-11-03T06:00:00Z',
};
const STATUS = { enabled: true, keySet: true, tvdbKeySet: true, shows: 'tvdb', showsLine: 'Shows: TVDB', running: false, pending: 0, unmatched: 1, needsReview: 2, lastRefresh: ago(2 * DAY), lastError: '' };
const job = (id, over = {}) => ({ id, kind: 'meta.match', title: `Match job ${id}`, priority: 20, state: 'queued', user: false, done: 0, total: 0, attempts: 0, createdAt: ago(600 - id), ...over });
const PLAY = { id: 'abc123', sessionId: 'abc123', itemId: 2, title: 'Alien', kind: 'transcode', client: '192.168.1.20 · Safari', startedAt: ago(900), lastSeenAt: ago(2), positionSec: 754, paused: false };
const SNAPSHOT = {
  paused: false,
  running: [
    job(3, { kind: 'scan.folder', title: 'Scan /Volumes/NAS/Movies', priority: 30, state: 'running', startedAt: ago(65), done: 120, total: 263, note: 'Heat (1995)' }),
    job(4, { kind: 'images.fetch', title: 'Fetch artwork', priority: 50, state: 'running', startedAt: ago(30), held: 'waits while something plays' }),
    { ...job(-1, { kind: 'scan.folders', title: 'Read library folders', state: 'running', priority: 0 }), startedAt: ago(20) },
  ],
  queued: [
    job(5, { title: 'Match Brazil (1985)' }),
    job(6, { title: 'Match Cube (1997)', attempts: 2, runAt: ago(-300), error: 'TMDB answered 429' }),
    job(7, { kind: 'ratings.imdb', title: 'Fetch IMDb ratings', priority: 40 }),
  ],
  history: [
    job(2, { kind: 'ratings.imdb', title: 'Fetch IMDb ratings', state: 'done', attempts: 1, startedAt: ago(4000), finishedAt: ago(3900) }),
    job(1, { title: 'Match Dune (2021)', state: 'failed', attempts: 3, error: 'TMDB could not be reached: dial tcp: i/o timeout', startedAt: ago(5000), finishedAt: ago(4990) }),
    job(0, { title: 'Match Alien (1979)', state: 'cancelled', startedAt: ago(6000), finishedAt: ago(5990) }),
  ],
  tier: { playback: { Sessions: 1, Active: 1, Transcodes: 1, Software: 0, Waiting: 0, Paths: null }, browsing: false, hold: { held: true, reason: 'playback' } },
};
const CAP = {
  error: '2 of 2 plays in use', limit: 'streams', max: 2, inUse: 2,
  playing: [PLAY, { id: 'def', sessionId: 'def', itemId: 3, title: 'Brazil', kind: 'direct', client: '192.168.1.31 · Chrome', startedAt: ago(300), lastSeenAt: ago(1), positionSec: 3725, paused: true }],
};

/**
 * A fresh context and page on the mocked server, the clock stopped at FIXED.
 *   width     the viewport's width (height 900, or 780 under 600 px)
 *   settings  merged over SETTINGS
 *   items     what /api/items lists
 *   auth      /api/info asks for a token
 *   cap       POST /playback answers 503 at the limit
 *   file      GET of the player's file answers this status (410: stopped from elsewhere)
 *   putError, testError, searchError   PUT /api/settings, the key test and the metadata search
 *             answer with this error
 *   motion    motion is not reduced
 */
async function open(browser, width, opts = {}) {
  const context = await browser.newContext({
    viewport: { width, height: width < 600 ? 780 : width > 1280 ? 1080 : 900 }, deviceScaleFactor: 1,
    reducedMotion: opts.motion ? 'no-preference' : 'reduce', colorScheme: 'dark',
    locale: 'en-US', timezoneId: 'UTC', serviceWorkers: 'block',
  });
  const page = await context.newPage();
  await page.clock.install({ time: FIXED });
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  const settings = { ...SETTINGS, ...opts.settings };
  const json = (route, body, status = 200, headers = {}) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body), headers });
  await page.route('**/api/**', async (route) => {
    const req = route.request();
    const u = new URL(req.url());
    const p = decodeURIComponent(u.pathname);
    const m = req.method();
    if (p.startsWith('/api/images/')) {
      const name = p.split('/').pop().replace(/\.\w+$/, '');
      return route.fulfill({ contentType: 'image/svg+xml', body: artwork(name) });
    }
    if (p === '/api/info') return json(route, { id: 'x', name: 'Living room', version: 'dev', authRequired: !!opts.auth });
    if (p === '/api/scan') return json(route, { running: false, itemCount: ITEMS.length, watching: [{ root: '/Volumes/NAS/Movies', watching: true, dirs: 263 }] });
    if (p === '/api/items') return json(route, opts.items ?? ITEMS);
    if (p === '/api/shows') return json(route, opts.items ? [] : SHOWS);
    if (p === '/api/continue') return json(route, opts.items ? [] : [byId[5], ITEMS.find((i) => i.id === 12)]);
    if (p === '/api/upcoming') return json(route, opts.items ? [] : UPCOMING);
    if (p === '/api/genres') return json(route, opts.items ? [] : GENRES);
    if (p === '/api/titles') return json(route, GENRE_TITLES[u.searchParams.get('genre')] ?? []);
    if (p === '/api/collections/9') return json(route, COLLECTION);
    if (p === '/api/people') return json(route, PEOPLE);
    if (p === '/api/people/31') return json(route, PERSON);
    if (p === '/api/settings' && m === 'GET') return json(route, settings);
    if (p === '/api/settings' && opts.putError) return json(route, { error: opts.putError }, 400);
    if (p === '/api/settings/test') return json(route, opts.testError ? { ok: false, error: opts.testError } : { ok: true });
    if (p === '/api/metadata/status') return json(route, STATUS);
    if (p === '/api/metadata/review') return json(route, REVIEW);
    if (p === '/api/metadata/search') {
      if (opts.searchError) return json(route, { error: opts.searchError }, 502);
      return json(route, u.searchParams.get('kind') === 'movie' ? CANDIDATES : []);
    }
    if (p === '/api/folders') return json(route, FOLDERS);
    if (p === '/api/fs') {
      const path = u.searchParams.get('path');
      if (!path) return json(route, { path: '', parent: '', dirs: [], places: [{ name: 'Home', path: '/Users/me' }, { name: 'USB', path: '/Volumes/USB' }] });
      return json(route, { path, parent: path.slice(0, path.lastIndexOf('/')) || '/', dirs: (TREE[path] ?? []).map((name) => ({ name, path: `${path}/${name}` })) });
    }
    if (p === '/api/jobs') return json(route, SNAPSHOT);
    // One snapshot, and no reconnection while the shot is taken.
    if (p === '/api/events') return route.fulfill({ status: 200, headers: { 'content-type': 'text/event-stream', 'cache-control': 'no-store' }, body: `retry: 600000\n\nid: 1\nevent: snapshot\ndata: ${JSON.stringify(SNAPSHOT)}\n\n` });
    if (p === '/api/sessions') return json(route, [PLAY]);
    if (p === '/api/shows/Lost') return json(route, LOST_DETAIL);
    if (p === '/api/items/1') return json(route, opts.player ? PLAYER_DETAIL : HEAT_DETAIL);
    if (p === '/api/items/1/playback' && m === 'POST') {
      if (opts.cap) return json(route, CAP, 503, { 'Retry-After': '30' });
      return json(route, { method: 'direct', videoAction: 'copy', audioAction: 'copy', audioIndex: 1, reasons: [], url: '/api/items/1/file', startSec: 0 });
    }
    if (p === '/api/items/1/file' && opts.file === 410) return json(route, { error: 'stopped', stoppedBy: 'another device' }, 410);
    if (p === '/api/items/1/file' && opts.file) return json(route, { error: 'the file is gone: /m/Heat.mkv' }, opts.file);
    if (p === '/api/items/1/file') {
      const range = /bytes=(\d*)-(\d*)/.exec(req.headers().range ?? '');
      const start = range?.[1] ? Number(range[1]) : 0;
      const end = range?.[2] ? Math.min(Number(range[2]), clip.length - 1) : clip.length - 1;
      return route.fulfill({
        status: range ? 206 : 200,
        headers: { 'content-type': 'video/mp4', 'accept-ranges': 'bytes', 'content-range': `bytes ${start}-${end}/${clip.length}` },
        body: clip.subarray(start, end + 1),
      });
    }
    if (p === '/api/items/1/subtitles/3.vtt') return route.fulfill({ contentType: 'text/vtt', body: 'WEBVTT\n' });
    if (p === '/api/items/1/progress' || p.endsWith('/heartbeat')) return route.fulfill({ status: 204 });
    // A rotten Tomatometer and a spilled audience, beside the fresh and upright ones of Heat.
    if (p === '/api/items/8') {
      const it = byId[8];
      const ratings = [entry('rt_critics', 6, 100, { state: 'rotten', url: 'https://www.rottentomatoes.com/m/gigli' }), entry('rt_audience', 13, 100, { state: 'spilled', votes: 20_000 }), entry('imdb', 2.6, 10, { votes: 50_000 })];
      return json(route, { ...it, path: '/m/Gigli.mkv', size: 1, bitrate: 1, streams: [], subtitles: [], playback: HEAT_DETAIL.playback,
        meta: { ...HEAT_META, id: 108, title: 'Gigli', year: 2003, itemId: 8, logo: undefined, collection: undefined, tagline: undefined, rating: ratings[0], ratings } });
    }
    const item = /^\/api\/items\/(\d+)$/.exec(p);
    if (item && byId[item[1]]) {
      const it = byId[item[1]];
      return json(route, { ...it, path: `/m/${it.title}.mkv`, size: 1, bitrate: 1, streams: [], subtitles: [], playback: HEAT_DETAIL.playback });
    }
    return json(route, { error: `unmocked ${m} ${p}` }, 404);
  });
  return { context, page, errors };
}

/** Lets the app's timers run (on the fake clock), then waits for every image to load and decode. */
async function settle(page) {
  // Cards skip rendering off screen (content-visibility: auto), and a full-page shot is mostly off
  // screen: render them all.
  await page.addStyleTag({ content: '* { content-visibility: visible !important; }' });
  for (let i = 0; i < 3; i++) await page.clock.runFor(100);
  await page.evaluate(() => {
    for (const el of document.images) if (el.loading === 'lazy') el.loading = 'eager';
  });
  await until('the images', () => page.evaluate(() => [...document.images].every((el) => el.complete)));
  await page.evaluate(async () => {
    await document.fonts.ready;
    await Promise.all([...document.images].map((el) => el.decode().catch(() => {})));
  });
  await sleep(150); // the load events' state changes (Picture's "loaded" class)
  await page.clock.runFor(100); // the fake clock drives requestAnimationFrame too
}

/**
 * Chrome does not blur the same way twice: a backdrop-filter (the glass of badges and chips) differs
 * by a few pixels from run to run. Its computed value per element is written beside the shot (and
 * compared like it), then it is switched off for the picture.
 */
async function unblur(page, file) {
  const glass = await page.evaluate(() =>
    [...document.querySelectorAll('*')]
      .map((el) => [el, getComputedStyle(el).backdropFilter])
      .filter(([, v]) => v && v !== 'none')
      .map(([el, v]) => `${el.tagName.toLowerCase()}${[...el.classList].filter((c) => !c.startsWith('svelte-')).map((c) => `.${c}`).join('')} ${v}`)
      .join('\n'),
  );
  writeFileSync(file, `${glass}\n`);
  if (!keepBlur) await page.addStyleTag({ content: '* { backdrop-filter: none !important; -webkit-backdrop-filter: none !important; }' });
}

/**
 * Pixels can't show motion: the shots run with reduced motion and stop every animation. So the
 * motion of each element that has some (transition-* and animation-*, as computed with motion NOT
 * reduced) is written beside the shot, once per distinct element and value.
 */
async function motion(page, file, reduced) {
  await page.emulateMedia({ reducedMotion: 'no-preference' });
  const lines = await page.evaluate(() => {
    // What an element is: its tag and its classes but Svelte's hash.
    const name = (el) => el.tagName.toLowerCase() + [...el.classList].filter((c) => !c.startsWith('svelte-')).map((c) => `.${c}`).join('');
    const seen = new Set();
    for (const el of document.querySelectorAll('*')) {
      const s = getComputedStyle(el);
      const moves = s.transitionDuration.split(',').some((d) => parseFloat(d) > 0) || s.animationName !== 'none';
      if (!moves) continue;
      seen.add(
        `${name(el)} | transition: ${s.transitionProperty} ${s.transitionDuration} ${s.transitionTimingFunction} ${s.transitionDelay}` +
          ` | animation: ${s.animationName} ${s.animationDuration} ${s.animationTimingFunction} ${s.animationDelay} ${s.animationIterationCount}`,
      );
    }
    return [...seen].join('\n');
  });
  writeFileSync(file, `${lines}\n`);
  await page.emulateMedia({ reducedMotion: reduced ? 'reduce' : 'no-preference' });
}

const go = (page, hash) => page.goto(`${base}/${hash}`);
const visible = (page, selector) => page.locator(selector).first().waitFor({ state: 'visible', timeout: 10000 });
/** Something typed first, so that a programmatic focus shows the ring as a keyboard one would. */
const keyboardFocus = async (page, selector) => {
  await page.keyboard.press('Shift');
  await page.locator(selector).first().focus();
};
/** The player paused at 0:00 with the whole clip buffered (the scrubber's bars then never vary). */
async function pausedAtStart(page) {
  await page.evaluate(async () => {
    const v = document.querySelector('video');
    v.pause();
    if (v.readyState < 2) await new Promise((r) => v.addEventListener('canplay', r, { once: true }));
    if (v.currentTime !== 0) {
      v.currentTime = 0;
      await new Promise((r) => v.addEventListener('seeked', r, { once: true }));
    }
  });
  await until('the clip buffered whole', () =>
    page.evaluate(() => {
      const v = document.querySelector('video');
      return v.paused && v.currentTime === 0 && v.buffered.length === 1 && v.buffered.end(0) >= v.duration - 0.05 && !document.querySelector('.overlay');
    }),
  );
  // Whether Chrome sent a "waiting" on the way is a matter of timing, and a paused element never
  // sends the "playing" that takes the buffering spinner down: send it, so that it never shows.
  await page.evaluate(() => document.querySelector('video').dispatchEvent(new Event('playing')));
}
async function openMenu(page) {
  await page.click('[aria-haspopup="menu"]');
  await visible(page, '[role="menuitem"]');
}

// Each shot: what to open, what to wait for, and whether the whole page or just the window is taken.
const SHOTS = [
  { name: 'home', hash: '#/', ready: 'text=Recently added', opts: { settings: { tmdbKeySet: false, tmdbKeySource: '' } }, full: true },
  { name: 'home-empty', hash: '#/', ready: 'text=Nothing to watch yet', opts: { items: [] } },
  { name: 'token', hash: '#/', ready: 'input[type="password"]', opts: { auth: true } },
  { name: 'movies', hash: '#/movies', ready: '.grid a.card, .posters a.card', full: true },
  { name: 'movies-sorted', hash: '#/movies?sort=rating&min=70', ready: 'a.card', full: true },
  { name: 'shows', hash: '#/shows', ready: 'a.card', full: true },
  {
    name: 'item', hash: '#/item/1', ready: 'ul.ratings .score-chip', full: true,
    act: async (page) => {
      await page.click('details.tech summary');
      await visible(page, 'details.tech table');
    },
  },
  { name: 'item-version', hash: '#/item/6', ready: 'text=Versions', full: true },
  { name: 'item-review', hash: '#/item/4', ready: '[role="note"]', full: true }, // "might not be the right film"
  { name: 'show', hash: '#/shows/Lost', ready: 'ol.episodes li', full: true },
  { name: 'show-season2', hash: '#/shows/Lost?s=2', ready: 'ol.episodes li', full: true },
  { name: 'episode', hash: '#/item/12', ready: 'nav.siblings', full: true },
  { name: 'genre', hash: '#/genre/18', ready: 'h1 .count', full: true },
  { name: 'collection', hash: '#/collection/9', ready: 'a.card', full: true },
  { name: 'person', hash: '#/person/31', ready: 'h2.library', full: true },
  // Settings, a shot per tab (#/settings?tab=...; Library is the bare #/settings).
  ...[
    ['library', 'ul[aria-label="Library folders"] li'],
    ['details', '.status p'],
    ['ratings', '.ratings-card'],
    ['playback', '#max-streams'],
    ['keys', '#tmdb-key'],
  ].map(([tab, ready]) => ({
    name: `settings-${tab}`, hash: tab === 'library' ? '#/settings' : `#/settings?tab=${tab}`, ready, full: true,
    act: (page) => until('no skeletons', () => page.evaluate(() => !document.querySelector('.skeleton'))),
  })),
  { name: 'activity', hash: '#/activity', ready: '[data-job="3"]', full: true },
  {
    name: 'folders-picker', hash: '#/settings', ready: 'ul[aria-label="Library folders"] li',
    act: async (page) => {
      await page.click('button:has-text("Add a folder")');
      await page.locator('dialog[open] .dir:has-text("Home")').click();
      await visible(page, 'dialog[open] ul[aria-label="Folders in /Users/me"]');
      await page.locator('dialog[open] label:has-text("TV shows")').click();
    },
  },
  {
    name: 'fixmatch', hash: '#/item/1', ready: 'ul.ratings .score-chip',
    act: async (page) => {
      await openMenu(page);
      await page.click('[role="menuitem"]:has-text("Fix the match")');
      await visible(page, 'dialog[open] .result');
    },
  },
  {
    name: 'edit-details', hash: '#/item/1', ready: 'ul.ratings .score-chip',
    act: async (page) => {
      await openMenu(page);
      await page.click('[role="menuitem"]:has-text("Edit details")');
      await visible(page, 'dialog[open] input');
    },
  },
  { name: 'menu', hash: '#/item/1', ready: 'ul.ratings .score-chip', act: openMenu },
  {
    name: 'palette', hash: '#/movies', ready: 'a.card',
    act: async (page) => {
      await page.keyboard.press('/');
      await visible(page, 'dialog[open] input[type="search"]');
      await page.keyboard.type('al');
      await page.clock.runFor(1000); // the people lookup's debounce
      await visible(page, '#search-hits li[role="option"] >> text=Al Pacino');
    },
  },
  { name: 'player', hash: '#/play/1', ready: '.controls .row button', opts: { player: true }, act: pausedAtStart },
  { name: 'cap', hash: '#/play/1', ready: '.overlay.limit li[data-session]', opts: { player: true, cap: true } },
  // Focus rings.
  { name: 'focus-header', hash: '#/', ready: 'text=Recently added', act: (page) => page.keyboard.press('Tab') },
  { name: 'focus-card', hash: '#/movies', ready: 'a.card', act: (page) => keyboardFocus(page, 'a.card') },
  { name: 'focus-chip', hash: '#/item/1', ready: 'ul.ratings .score-chip', act: (page) => keyboardFocus(page, 'ul.ratings a.score-chip') },
  { name: 'focus-input', hash: '#/settings?tab=keys', ready: '#tmdb-key', act: async (page) => {
    await page.locator('#tmdb-key').scrollIntoViewIfNeeded();
    await keyboardFocus(page, '#tmdb-key');
  } },
  { name: 'focus-chips', hash: '#/movies', ready: 'a.card', act: (page) => keyboardFocus(page, '[role="radiogroup"] a, .chips .chip') },
  { name: 'focus-player', hash: '#/play/1', ready: '.controls .row button', opts: { player: true }, act: async (page) => {
    await pausedAtStart(page);
    await keyboardFocus(page, 'button[aria-label="Play"]');
  } },
  // Hover.
  { name: 'hover-card', hash: '#/movies', ready: 'a.card', act: (page) => page.locator('a.card').first().hover() },
  { name: 'hover-btn', hash: '#/', ready: 'text=Recently added', act: (page) => page.locator('a.btn:has-text("Details")').hover() },
  { name: 'hover-primary', hash: '#/', ready: 'text=Recently added', act: (page) => page.locator('a.btn.primary:has-text("Play")').first().hover() },
  { name: 'hover-menu', hash: '#/item/1', ready: 'ul.ratings .score-chip', act: async (page) => {
    await openMenu(page);
    await page.locator('[role="menuitem"]').nth(1).hover();
  } },
  { name: 'hover-person', hash: '#/item/1', ready: 'a.person', act: async (page) => {
    await page.locator('a.person').first().scrollIntoViewIfNeeded();
    await page.locator('a.person').first().hover();
  } },
  // The header once the page scrolls under it.
  { name: 'header-scrolled', hash: '#/settings', ready: 'ul[aria-label="Library folders"] li', act: async (page) => {
    await page.evaluate(() => window.scrollTo(0, 600));
    await visible(page, 'header.bar.scrolled');
  } },
  // The player: the time over the scrubber, and an open track menu.
  { name: 'player-tip', hash: '#/play/1', ready: '.controls .row button', opts: { player: true }, act: async (page) => {
    await pausedAtStart(page);
    const box = await page.locator('.scrubber').boundingBox();
    await page.mouse.move(Math.round(box.x + box.width / 2), Math.round(box.y + box.height / 2));
    await visible(page, '.scrubber .tip');
  } },
  { name: 'player-menu', hash: '#/play/1', ready: '.controls .row button', opts: { player: true }, act: async (page) => {
    await pausedAtStart(page);
    await page.click('button[aria-label="Audio track"]');
    await visible(page, '.menu[role="menu"]');
  } },
  // A rotten Tomatometer and a spilled audience.
  { name: 'item-rotten', hash: '#/item/8', ready: 'ul.ratings [data-source="rt_audience"]', full: true },
  // Errors.
  { name: 'keytest-error', hash: '#/settings?tab=keys', ready: '#tmdb-key', opts: { testError: 'TMDB said the key is not valid (401).' }, act: async (page) => {
    await page.click('button[aria-label="Test the TMDB key"]');
    await visible(page, '.test .result.error');
    await page.locator('.test .result.error').first().scrollIntoViewIfNeeded();
  } },
  { name: 'tvdb-error', hash: '#/settings?tab=keys', ready: '#tvdb-key', opts: { putError: 'TVDB rejected the key or PIN', settings: { tvdbKeySet: false, tvdbKeySource: '' } }, act: async (page) => {
    await page.fill('#tvdb-key', 'fake-refused-key');
    await page.locator('.card.tvdb button:has-text("Save")').click();
    await visible(page, '.card.tvdb p[role="status"].error');
    await page.locator('.card.tvdb p[role="status"].error').scrollIntoViewIfNeeded();
  } },
  { name: 'fixmatch-error', hash: '#/item/1', ready: 'ul.ratings .score-chip', opts: { searchError: 'TMDB refused the key' }, act: async (page) => {
    await openMenu(page);
    await page.click('[role="menuitem"]:has-text("Fix the match")');
    await visible(page, 'dialog[open] .error');
  } },
  { name: 'player-error', hash: '#/play/1', ready: '.overlay[role="alert"]', opts: { player: true, file: 404 } },
  { name: 'player-stopped', hash: '#/play/1', ready: '.overlay.stopped', opts: { player: true, file: 410 } },
  // A key's Remove asks first.
  { name: 'remove-confirm', hash: '#/settings?tab=keys', ready: '#tmdb-key', act: async (page) => {
    await page.click('.card.tmdb button:has-text("Remove key")');
    await visible(page, '.card.tmdb .confirm-remove');
  } },
  { name: 'folders-forget', hash: '#/settings', ready: 'ul[aria-label="Library folders"] li', act: async (page) => {
    await page.click('button[aria-label="Remove /Volumes/NAS/Movies"]');
    await page.locator('dialog[open] input[type="checkbox"]').check();
    await visible(page, 'dialog[open] .forget');
  } },
  // With motion: the activity indicator's turning ring (its first frame) instead of the still one.
  { name: 'indicator-motion', hash: '#/movies', ready: 'a.card', opts: { motion: true } },
];

// PLY_VISUAL_BLUR=1 keeps backdrop-filter in the pictures (to look at a blur; not repeatable).
const keepBlur = process.env.PLY_VISUAL_BLUR === '1';
const WIDTHS = [360, 700, 1280, 1920]; // 700: Person's own breakpoint

const browser = await chromium.launch({ channel: 'chrome', headless: true, args: ['--mute-audio', '--autoplay-policy=user-gesture-required', '--force-color-profile=srgb', '--disable-gpu',
  '--disable-font-subpixel-positioning', '--disable-lcd-text', '--run-all-compositor-stages-before-draw', '--disable-partial-raster'] });
const only = process.env.PLY_E2E_ONLY?.split(',');
const failed = [];
let taken = 0;
try {
  for (const shot of SHOTS) {
    if (only && !only.some((o) => shot.name.includes(o))) continue;
    for (const width of WIDTHS) {
      const { context, page, errors } = await open(browser, width, shot.opts);
      try {
        await go(page, shot.hash);
        await page.clock.runFor(100);
        await visible(page, shot.ready);
        await settle(page);
        if (shot.act) await shot.act(page);
        if (shot.full) await page.evaluate(() => window.scrollTo(0, 0)); // a click may have scrolled
        await settle(page);
        await motion(page, join(out, `${shot.name}-${width}.motion.txt`), !shot.opts?.motion);
        await page.clock.runFor(100);
        await unblur(page, join(out, `${shot.name}-${width}.glass.txt`));
        await page.clock.runFor(100);
        await page.screenshot({ path: join(out, `${shot.name}-${width}.png`), fullPage: !!shot.full, animations: 'disabled', caret: 'hide' });
        taken++;
        if (errors.length) failed.push(`${shot.name}-${width}: page errors: ${errors.join('; ')}`);
      } catch (e) {
        failed.push(`${shot.name}-${width}: ${e instanceof Error ? e.message.split('\n')[0] : String(e)}`);
      } finally {
        await context.close();
      }
    }
  }
} finally {
  await browser.close();
  server.close();
  rmSync(work, { recursive: true, force: true });
}
console.log(`${taken} screenshots in ${out}`);
if (failed.length) {
  console.log(failed.map((f) => `FAIL  ${f}`).join('\n'));
  process.exit(1);
}
