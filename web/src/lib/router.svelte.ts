export type Route =
  | { name: 'movies' }
  | { name: 'shows' }
  | { name: 'show'; show: string }
  | { name: 'item'; id: number }
  | { name: 'play'; id: number; startSec: number }
  | { name: 'notfound' };

/** Hash forms: #/movies, #/shows, #/shows/<name>, #/item/<id>, #/play/<id>[?t=<resume seconds>]. */
export function parseRoute(hash: string): Route {
  const [path, query = ''] = hash.replace(/^#\/?/, '').split('?');
  const [head = '', arg = ''] = path.split('/');
  try {
    switch (head) {
      case '':
      case 'movies':
        return { name: 'movies' };
      case 'shows':
        return arg ? { name: 'show', show: decodeURIComponent(arg) } : { name: 'shows' };
      case 'item':
      case 'play': {
        const id = Number(arg);
        if (!Number.isInteger(id) || id <= 0) break;
        if (head === 'item') return { name: 'item', id };
        const t = Number(new URLSearchParams(query).get('t'));
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
  router.route = parseRoute(location.hash);
  window.scrollTo(0, 0);
});

export const href = {
  movies: '#/movies',
  shows: '#/shows',
  show: (name: string) => `#/shows/${encodeURIComponent(name)}`,
  item: (id: number) => `#/item/${id}`,
  play: (id: number, startSec = 0) => `#/play/${id}${startSec > 0 ? `?t=${Math.floor(startSec)}` : ''}`,
};
