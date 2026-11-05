export type Sort = 'title' | 'added' | 'year' | 'duration';
export type Filter = '4k' | 'hdr' | 'unwatched';

export interface LibraryView {
  sort: Sort;
  filters: Filter[];
}

export type Route =
  | { name: 'home' }
  | ({ name: 'movies' } & LibraryView)
  | ({ name: 'shows' } & LibraryView)
  | { name: 'show'; show: string; season?: number }
  | { name: 'item'; id: number }
  | { name: 'play'; id: number; startSec: number }
  | { name: 'person'; id: number }
  | { name: 'genre'; id: number }
  | { name: 'collection'; id: number }
  | { name: 'settings' }
  | { name: 'activity' }
  | { name: 'notfound' };

const SORTS: Sort[] = ['title', 'added', 'year', 'duration'];
const FILTERS: Filter[] = ['4k', 'hdr', 'unwatched'];

function libraryView(q: URLSearchParams, fallback: Sort): LibraryView {
  const s = q.get('sort') as Sort;
  const filters = (q.get('f') ?? '').split(',').filter((f): f is Filter => FILTERS.includes(f as Filter));
  return { sort: SORTS.includes(s) ? s : fallback, filters };
}

/**
 * Hash forms: #/ (home), #/movies[?sort=&f=], #/shows[?sort=&f=], #/shows/<name>[?s=<season>],
 * #/item/<id>, #/play/<id>[?t=<resume seconds>], #/person/<id>, #/genre/<id>,
 * #/collection/<id>, #/settings, #/activity.
 */
export function parseRoute(hash: string): Route {
  const [path, query = ''] = hash.replace(/^#\/?/, '').split('?');
  const [head = '', arg = ''] = path.split('/');
  const q = new URLSearchParams(query);
  try {
    switch (head) {
      case '':
        return { name: 'home' };
      case 'movies':
        return { name: 'movies', ...libraryView(q, 'title') };
      case 'shows': {
        if (!arg) return { name: 'shows', ...libraryView(q, 'title') };
        const s = q.get('s') ?? '';
        const season = s.trim() === '' ? NaN : Number(s); // Number('') would be 0, i.e. Specials
        return { name: 'show', show: decodeURIComponent(arg), season: Number.isInteger(season) ? season : undefined };
      }
      case 'settings':
      case 'activity':
        if (!arg) return { name: head };
        break;
      case 'person':
      case 'genre':
      case 'collection': {
        const id = Number(arg);
        if (!Number.isInteger(id) || id <= 0) break;
        return { name: head, id };
      }
      case 'item':
      case 'play': {
        const id = Number(arg);
        if (!Number.isInteger(id) || id <= 0) break;
        if (head === 'item') return { name: 'item', id };
        const t = Number(q.get('t'));
        return { name: 'play', id, startSec: Number.isFinite(t) && t > 0 ? t : 0 };
      }
    }
  } catch {
    // malformed percent-encoding in the show name
  }
  return { name: 'notfound' };
}

export const router = $state<{ route: Route }>({ route: parseRoute(location.hash) });

window.addEventListener('hashchange', () => {
  const next = parseRoute(location.hash);
  // Sort/filter/season changes re-render in place; only real navigation scrolls to the top.
  const cur = router.route;
  const samePage =
    ((next.name === 'movies' || next.name === 'shows') && next.name === cur.name) ||
    (next.name === 'show' && cur.name === 'show' && next.show === cur.show);
  router.route = next;
  if (!samePage) window.scrollTo(0, 0);
});

function viewQuery(v: Partial<LibraryView>, fallback: Sort): string {
  const p = new URLSearchParams();
  if (v.sort && v.sort !== fallback) p.set('sort', v.sort);
  if (v.filters?.length) p.set('f', v.filters.join(','));
  const s = p.toString();
  return s ? `?${s}` : '';
}

export const href = {
  home: '#/',
  movies: '#/movies',
  shows: '#/shows',
  moviesWith: (v: Partial<LibraryView>) => `#/movies${viewQuery(v, 'title')}`,
  showsWith: (v: Partial<LibraryView>) => `#/shows${viewQuery(v, 'title')}`,
  show: (name: string, season?: number) =>
    `#/shows/${encodeURIComponent(name)}${season === undefined ? '' : `?s=${season}`}`,
  item: (id: number) => `#/item/${id}`,
  play: (id: number, startSec = 0) => {
    const t = Math.floor(startSec);
    return `#/play/${id}${t > 0 ? `?t=${t}` : ''}`;
  },
  person: (id: number) => `#/person/${id}`,
  genre: (id: number) => `#/genre/${id}`,
  collection: (id: number) => `#/collection/${id}`,
  settings: '#/settings',
  activity: '#/activity',
};
