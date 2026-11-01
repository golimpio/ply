// Small fuzzy matcher for the search palette: every query character must
// appear in order; matches at word starts and in runs score higher.
import type { ItemSummary } from './api';

function score(query: string, text: string): number {
  if (!query) return 1;
  const t = text.toLowerCase();
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

export function haystack(it: ItemSummary): string {
  return [it.title, it.show, it.episodeTitle, it.year].filter(Boolean).join(' ');
}

export function search<T extends ItemSummary>(items: T[], query: string, limit = 30): T[] {
  const q = query.trim().toLowerCase().replace(/\s+/g, ' ');
  if (!q) return [];
  return items
    .map((it) => ({ it, s: score(q, haystack(it)) }))
    .filter((r) => r.s > 0)
    .sort((a, b) => b.s - a.s)
    .slice(0, limit)
    .map((r) => r.it);
}
