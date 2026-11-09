// The self-test of mock-items.mjs (vitest only picks up src/, so this is node's own runner):
//
//   node --test e2e/mock-items.test.mjs
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { answerItems, routeItems, serveItems, toCard } from './mock-items.mjs';

const cardGo = readFileSync(new URL('../../internal/api/card.go', import.meta.url), 'utf8');

// A movie with every field the full row (ItemSummary) can have.
const full = {
  id: 1, kind: 'movie', title: 'The Thing', sortTitle: 'Thing, The', year: 1982, durationSec: 6540, container: 'mkv',
  videoCodec: 'hevc', audioCodec: 'eac3', width: 3840, height: 2160, hdr: 'hdr10', addedAt: 1_700_000_000,
  progress: { positionSec: 120, watched: false }, art: { backdrop: '/api/items/1/art/backdrop', thumb: '/api/items/1/art/thumb', color: '#223344' },
  watched: false, watchedAt: null, playCount: 0, overview: 'Antarctica.', titleId: 77, poster: '/p.jpg', backdrop: '/b.jpg', logo: '/l.png',
  certification: 'R', genres: ['Horror'], matchState: 'matched', needsReview: false,
  rating: { source: 'rt_critics', value: 84, scale: 100, score100: 84, votes: 9, state: 'fresh', url: 'https://x', fetchedAt: '2026-01-01T00:00:00Z' },
};
// The card fields (docs: the cache plan; internal/api/card.go), every one present for `full`.
const CARD_KEYS = ['id', 'kind', 'title', 'sortTitle', 'year', 'titleId', 'addedAt', 'poster', 'art', 'backdrop', 'rating', 'watched', 'progress',
  'durationSec', 'width', 'height', 'hdr', 'container', 'genres', 'matchState', 'needsReview'];

const ep = (id, show) => ({ id, kind: 'episode', title: show, show, season: 1, episode: id, durationSec: 1, container: 'mkv', width: 1, height: 1,
  addedAt: id, progress: null, art: null, watched: false, watchedAt: null, playCount: 0, matchState: 'matched', needsReview: false, rating: null });
const sparse = { id: 2, kind: 'movie', title: 'Bare', durationSec: 1, width: 1, height: 1, addedAt: 2, progress: null, art: null,
  watched: true, watchedAt: 5, playCount: 1, matchState: 'unmatched', needsReview: false, rating: null };
const rows = [full, sparse, ep(10, 'Lost'), ep(11, 'Lost'), ep(12, 'The Wire')];

const ids = (a) => a.body.map((r) => r.id);

/** The json names of a Go struct's fields in src, in order ([] when there is no such struct). */
function jsonTags(src, struct) {
  const body = new RegExp(`^type ${struct} struct \\{\\n([\\s\\S]*?)^\\}`, 'm').exec(src)?.[1] ?? '';
  return [...body.matchAll(/`json:"([^",]+)/g)].map((m) => m[1]);
}

/** Where toCard of a full row and the structs of card.go (src) disagree: a line per struct that differs. */
function drift(src) {
  const card = toCard(full);
  const out = [];
  for (const [struct, obj] of [['ItemCard', card], ['CardArt', card.art], ['CardRating', card.rating]]) {
    const go = jsonTags(src, struct).sort();
    const js = Object.keys(obj).sort();
    if (go.join() !== js.join()) out.push(`${struct}: card.go [${go}] / toCard [${js}]`);
  }
  return out;
}

test('toCard has exactly the fields of the structs in card.go', () => {
  assert.ok(jsonTags(cardGo, 'ItemCard').length >= 20, 'positive control: the ItemCard tags were read');
  assert.ok(jsonTags(cardGo, 'CardArt').includes('thumb') && jsonTags(cardGo, 'CardRating').includes('score100'), 'positive control: the nested structs were read');
  assert.deepEqual(drift(cardGo), []);
});

test('the guard fails on a field card.go gains and the mirror lacks', () => {
  const added = cardGo.replace(/^(type ItemCard struct \{\n)/m, '$1\tLogo string `json:"logo,omitempty"`\n');
  assert.notEqual(added, cardGo, 'positive control: the copy was changed');
  assert.deepEqual(drift(added), [`ItemCard: card.go [${[...Object.keys(toCard(full)), 'logo'].sort()}] / toCard [${Object.keys(toCard(full)).sort()}]`]);
  const nested = cardGo.replace(/^(type CardRating struct \{\n)/m, '$1\tVotes *int `json:"votes,omitempty"`\n');
  assert.equal(drift(nested).length, 1);
  assert.match(drift(nested)[0], /^CardRating: /);
});

test('a plain /api/items is every row, the same array', () => {
  for (const q of [undefined, '', '/api/items', new URL('http://h/api/items'), new URLSearchParams()]) {
    const a = answerItems(rows, q);
    assert.equal(a.status, 200);
    assert.equal(a.body, rows, String(q));
  }
});

test('kind and show filter as the server does', () => {
  assert.deepEqual(ids(answerItems(rows, '/api/items?kind=movie')), [1, 2]);
  assert.deepEqual(ids(answerItems(rows, '/api/items?kind=episode')), [10, 11, 12]);
  assert.deepEqual(ids(answerItems(rows, '/api/items?kind=episode&show=lost')), [10, 11], 'show is case-insensitive');
  assert.deepEqual(ids(answerItems(rows, '/api/items?show=The+Wire')), [12]);
  assert.deepEqual(ids(answerItems(rows, '/api/items?kind=movie&show=Lost')), []);
  // Full rows still: the filter does not project.
  assert.equal(answerItems(rows, '/api/items?kind=movie').body[0], full);
});

test('fields=card sends cards of the movies', () => {
  const a = answerItems(rows, '/api/items?kind=movie&fields=card');
  assert.equal(a.status, 200);
  assert.deepEqual(ids(a), [1, 2]);
  const [card] = a.body;
  assert.ok('overview' in full, 'positive control: the full row has an overview');
  for (const k of ['overview', 'videoCodec', 'audioCodec', 'logo', 'certification', 'watchedAt', 'playCount']) assert.ok(!(k in card), `${k} dropped`);
  assert.deepEqual(Object.keys(card).sort(), [...CARD_KEYS].sort());
  for (const k of ['id', 'kind', 'title', 'sortTitle', 'year', 'titleId', 'addedAt', 'poster', 'backdrop', 'watched', 'progress',
    'durationSec', 'width', 'height', 'hdr', 'container', 'genres', 'matchState', 'needsReview']) assert.deepEqual(card[k], full[k], k);
  assert.deepEqual(card.art, { thumb: '/api/items/1/art/thumb', color: '#223344' }, 'art: thumb and colour, no backdrop');
  assert.deepEqual(card.rating, { source: 'rt_critics', value: 84, scale: 100, score100: 84, state: 'fresh' }, 'rating: no url, votes, fetchedAt');
});

test("a card leaves out what Go's omitempty does, and keeps the rest", () => {
  const c = toCard(sparse);
  for (const k of ['year', 'titleId', 'poster', 'backdrop', 'hdr', 'genres']) assert.ok(!(k in c), `${k} absent`);
  assert.equal(c.sortTitle, '');
  assert.equal(c.container, '');
  assert.equal(c.art, null);
  assert.equal(c.rating, null);
  assert.equal(c.progress, null);
  assert.equal(c.watched, true);
  assert.deepEqual(toCard({ ...full, art: { backdrop: '/b', thumb: '/t' } }).art, { thumb: '/t' }, 'no colour yet: the thumb alone');
  assert.ok(!('state' in toCard({ ...full, rating: { ...full.rating, state: '' } }).rating), 'no state: none on the card');
  assert.equal(toCard({ ...full, year: 0 }).year, 0, 'a *int of 0 is sent');
});

test('the 400s of handlers.go', () => {
  const cases = [
    ['/api/items?kind=show', 'kind must be "movie" or "episode"'],
    ['/api/items?kind=show&fields=card', 'kind must be "movie" or "episode"'],
    ['/api/items?fields=full', 'fields must be "card"'],
    ['/api/items?kind=movie&fields=full', 'fields must be "card"'],
    ['/api/items?fields=card', 'fields=card is for kind=movie'],
    ['/api/items?kind=episode&fields=card', 'fields=card is for kind=movie'],
  ];
  for (const [q, error] of cases) assert.deepEqual(answerItems(rows, q), { status: 400, body: { error } }, q);
});

test('routeItems fulfils a page.route', async () => {
  let got;
  const route = (url) => ({ request: () => ({ url: () => url }), fulfill: async (f) => (got = f) });
  await routeItems(route('http://127.0.0.1:9/api/items?kind=movie&fields=card'), rows);
  assert.equal(got.status, 200);
  assert.equal(got.contentType, 'application/json');
  assert.deepEqual(JSON.parse(got.body).map((r) => r.id), [1, 2]);
  await routeItems(route('http://127.0.0.1:9/api/items?fields=card'), rows);
  assert.equal(got.status, 400);
});

test('serveItems answers a node request, through the suite sender on a 200', () => {
  const res = () => ({ writeHead(s, h) { this.status = s; this.headers = h; return this; }, end(b) { this.body = b; return this; } });
  const sent = [];
  serveItems({ url: '/api/items' }, res(), rows, (body) => sent.push(body));
  assert.equal(sent[0], rows, 'the sender gets every row');
  const r = serveItems({ url: '/api/items?kind=episode&fields=card' }, res(), rows, (body) => sent.push(body));
  assert.equal(sent.length, 1, 'a 400 does not go through the sender');
  assert.equal(r.status, 400);
  assert.deepEqual(JSON.parse(r.body), { error: 'fields=card is for kind=movie' });
  const plain = serveItems({ url: '/api/items?kind=episode' }, res(), rows);
  assert.equal(plain.status, 200);
  assert.deepEqual(JSON.parse(plain.body).map((x) => x.id), [10, 11, 12]);
});
