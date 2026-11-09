// Small fuzzy matcher for the search palette: every query character must
// appear in order; matches at word starts and in runs score higher.

/** What the matcher reads of a row: a movie card, a full row, or a stand-in (a show, a genre). */
export interface Searchable {
  title: string;
  show?: string;
  episodeTitle?: string;
  year?: number;
}

/** Lower-case and strip accents, so "amelie" finds "Amélie" and "pokemon" "Pokémon". */
export function fold(s: string): string {
  return s.normalize('NFD').replace(/\p{M}/gu, '').toLowerCase();
}

// score takes the text already folded.
function score(query: string, t: string): number {
  if (!query) return 1;
  const exact = t.indexOf(query);
  if (exact >= 0) return 1000 - exact + (exact === 0 || /\W/.test(t[exact - 1]) ? 200 : 0);
  let s = 0;
  let run = 0;
  let from = 0;
  for (const ch of query) {
    const i = t.indexOf(ch, from);
    if (i < 0) return 0;
    run = i === from ? run + 1 : 0;
    s += 1 + run * 2 + (i === 0 || /\W/.test(t[i - 1]) ? 5 : 0);
    from = i + 1;
  }
  return s;
}

export function haystack(it: Searchable): string {
  return [it.title, it.show, it.episodeTitle, it.year].filter(Boolean).join(' ');
}

// The searched fields (title, show, episode title, year) of a row object never change: a rematch or
// a rename refetches the list. Watch state is still written in place today, but it is not searched,
// so a row's text is folded once, the first time a search meets it.
const folded = new WeakMap<Searchable, string>();

/** A row's haystack, folded: computed once per row object. */
export function foldedHaystack(it: Searchable): string {
  let t = folded.get(it);
  if (t === undefined) {
    t = fold(haystack(it));
    folded.set(it, t);
  }
  return t;
}

export function search<T extends Searchable>(items: T[], query: string, limit = 30): T[] {
  const q = fold(query.trim()).replace(/\s+/g, ' ');
  if (!q) return [];
  return items
    .map((it) => ({ it, s: score(q, foldedHaystack(it)) }))
    .filter((r) => r.s > 0)
    .sort((a, b) => b.s - a.s)
    .slice(0, limit)
    .map((r) => r.it);
}
