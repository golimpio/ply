// GET /api/items for the mocked e2e suites, as internal/api/handlers.go (handleItems) answers it:
// kind and show filter the rows, fields=card sends each movie as an ItemCard and each episode as an
// EpisodeCard (internal/api/card.go), limit cuts the list after the filters, and the same queries are
// a 400 with the same message. A plain /api/items is every row, unchanged, so a suite that hands its
// rows to this sees what it saw before.
//
// Not here: sort=rating and minRating (the client filters and sorts by rating itself, and sends
// neither), and the ETag. A suite that needs a 304 keeps its own sender: pass it to serveItems.
//
//   node server:  if (p === '/api/items') return serveItems(req, res, items, (body) => list(req, res, body));
//   page.route:   if (p === '/api/items') return routeItems(route, items);

const KIND_MOVIE = 'movie';
const KIND_EPISODE = 'episode';
const FIELDS_CARD = 'card';

/** SQLite's COLLATE NOCASE (store.ListItems filters show with it): ASCII letters only. */
const nocase = (s) => s.replace(/[A-Z]/g, (c) => c.toLowerCase());

/** Sets out[key] = v unless Go's omitempty leaves it out: a nil pointer or slice, "", []. (A *int of 0 stays.) */
function omitEmpty(out, key, v) {
  if (v === undefined || v === null || v === '' || (Array.isArray(v) && v.length === 0)) return;
  out[key] = v;
}

/** The card of a full row (ItemSummary): itemCard in internal/api/card.go, field by field. Keep the two in step. */
export function toCard(row) {
  const c = { id: row.id, kind: row.kind, title: row.title, sortTitle: row.sortTitle ?? '' };
  omitEmpty(c, 'year', row.year);
  omitEmpty(c, 'titleId', row.titleId);
  c.addedAt = row.addedAt ?? 0;
  omitEmpty(c, 'poster', row.poster);
  omitEmpty(c, 'backdrop', row.backdrop);
  // The frame grab without its backdrop; null when the row has none.
  c.art = row.art ? { thumb: row.art.thumb ?? '' } : null;
  if (row.art?.color) c.art.color = row.art.color;
  const r = row.rating;
  c.rating = r ? { source: r.source, value: r.value, scale: r.scale, score100: r.score100 } : null;
  if (r?.state) c.rating.state = r.state;
  c.watched = !!row.watched;
  c.progress = row.progress ?? null;
  c.durationSec = row.durationSec ?? 0;
  c.width = row.width ?? 0;
  c.height = row.height ?? 0;
  omitEmpty(c, 'hdr', row.hdr);
  c.container = row.container ?? '';
  omitEmpty(c, 'genres', row.genres);
  c.matchState = row.matchState ?? '';
  c.needsReview = !!row.needsReview;
  return c;
}

const bad = (error) => ({ status: 400, body: { error } });

/** The card of a full episode row (ItemSummary): episodeCard in internal/api/card.go, field by field. Keep the two in step. */
export function toEpisodeCard(row) {
  const c = { id: row.id, kind: row.kind, title: row.title };
  omitEmpty(c, 'year', row.year);
  omitEmpty(c, 'show', row.show);
  omitEmpty(c, 'season', row.season);
  omitEmpty(c, 'episode', row.episode);
  omitEmpty(c, 'episodeTitle', row.episodeTitle);
  c.addedAt = row.addedAt ?? 0;
  c.durationSec = row.durationSec ?? 0;
  omitEmpty(c, 'still', row.still);
  omitEmpty(c, 'overview', row.overview);
  // The frame grab without its backdrop; null when the row has none.
  c.art = row.art ? { thumb: row.art.thumb ?? '' } : null;
  if (row.art?.color) c.art.color = row.art.color;
  if (row.ratings?.length) c.ratings = row.ratings.map(cardRating);
  c.watched = !!row.watched;
  c.watchedAt = row.watchedAt ?? null;
  c.playCount = row.playCount ?? 0;
  c.progress = row.progress ?? null;
  c.width = row.width ?? 0;
  c.height = row.height ?? 0;
  omitEmpty(c, 'hdr', row.hdr);
  return c;
}

/** A CardRating: the entry without its link, votes and fetch time; null for none. */
function cardRating(r) {
  if (!r) return null;
  const out = { source: r.source, value: r.value, scale: r.scale, score100: r.score100 };
  if (r.state) out.state = r.state;
  return out;
}

/**
 * The answer to GET /api/items for these rows: { status, body }. query is the request's URL (a URL,
 * or a string such as '/api/items?kind=movie') or its URLSearchParams.
 */
export function answerItems(rows, query = '') {
  const q = query instanceof URLSearchParams ? query : query instanceof URL ? query.searchParams : new URL(String(query), 'http://x').searchParams;
  const kind = q.get('kind') ?? '';
  if (kind !== '' && kind !== KIND_MOVIE && kind !== KIND_EPISODE) return bad('kind must be "movie" or "episode"');
	const fields = q.get('fields') ?? '';
  if (fields !== '' && fields !== FIELDS_CARD) return bad('fields must be "card"');
  if (fields === FIELDS_CARD && kind === '') return bad('fields=card needs kind=movie or kind=episode');
  const limit = q.get('limit') ?? '';
  let n = 0;
  if (limit !== '') {
    if (!/^\+?[0-9]+$/.test(limit) || Number(limit) < 1 || Number(limit) > 500) return bad('limit must be a number from 1 to 500');
    n = Number(limit);
  }
  const show = q.get('show') ?? '';
  // The whole list is cut by the limit too, as handleItems cuts it after the filters.
  let out =
    kind === '' && show === ''
      ? rows
      : rows.filter((r) => (kind === '' || r.kind === kind) && (show === '' || nocase(r.show ?? '') === nocase(show)));
  if (n > 0 && out.length > n) out = out.slice(0, n);
  if (fields !== FIELDS_CARD) return { status: 200, body: out };
  return { status: 200, body: out.map(kind === KIND_EPISODE ? toEpisodeCard : toCard) };
}

/** page.route: fulfils the request to /api/items with answerItems. */
export function routeItems(route, rows) {
  const { status, body } = answerItems(rows, new URL(route.request().url()));
  return route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
}

/**
 * A node http server: answers req with answerItems. ok(body), when given, sends a 200 instead (a
 * suite's own sender, with its ETag and its log); a 400 is always sent here.
 */
export function serveItems(req, res, rows, ok) {
  const { status, body } = answerItems(rows, new URL(req.url, 'http://x'));
  if (status === 200 && ok) return ok(body);
  return res.writeHead(status, { 'content-type': 'application/json', 'cache-control': 'no-store' }).end(JSON.stringify(body));
}
