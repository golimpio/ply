// Library order by name, and the A to Z rail over it (components/JumpRail.svelte).
//
// The server lists titles by their sort title (the title with a leading The, A or An moved to the
// end: "Matrix, The"), compared in ASCII lower case, then by year, then by id (store/meta_read.go,
// orderByName): GET /api/titles comes in that order. /api/items and /api/shows keep file order, so
// Movies and Shows sort here, the same way, from the sortTitle their rows carry (the title, for a
// server that sends none).

export interface Named {
  title: string;
  sortTitle?: string;
  year?: number;
  /** The last tie-break; a string (a show's name) compares as text. */
  id: number | string;
}

const keys = new WeakMap<object, string>();

/** What a row is listed under, as the server compares it: SQLite's LOWER() folds A to Z only. */
export function nameKey(x: Pick<Named, 'title' | 'sortTitle'>): string {
  let k = keys.get(x);
  if (k === undefined) {
    k = (x.sortTitle || x.title).replace(/[A-Z]/g, (c) => String.fromCharCode(c.charCodeAt(0) + 32));
    keys.set(x, k);
  }
  return k;
}

const cmp = <T extends string | number>(a: T, b: T) => (a < b ? -1 : a > b ? 1 : 0);

/**
 * By name, then year, then id: the server's order. (SQLite compares UTF-8 bytes, JavaScript UTF-16
 * units: they differ only past U+FFFF against U+E000 to U+FFFF, which no title starts with.)
 */
export function byName(a: Named, b: Named): number {
  const ids = typeof a.id === 'number' && typeof b.id === 'number' ? a.id - b.id : cmp(String(a.id), String(b.id));
  return cmp(nameKey(a), nameKey(b)) || (a.year ?? 0) - (b.year ?? 0) || ids;
}

/** The rail's letter for a name: A to Z with accents dropped ("Élite" is E), else # (digits, symbols). */
export function letterOf(name: string): string {
  const c = name.trimStart().normalize('NFD').charAt(0).toUpperCase();
  return c >= 'A' && c <= 'Z' ? c : '#';
}

export interface LetterEntry {
  letter: string;
  /** The first card under the letter, in the list's order. */
  index: number;
}

/**
 * The letters a list has, # first and then A to Z, each with its first card. In name order the
 * cards of a letter follow one another, except an accented name's: it sorts after Z (as the server
 * has it) but is listed under its letter, which then leads to the first of them.
 */
export function letterIndex(names: string[]): LetterEntry[] {
  const first = new Map<string, number>();
  names.forEach((n, i) => {
    const l = letterOf(n);
    if (!first.has(l)) first.set(l, i);
  });
  return [...first].map(([letter, index]) => ({ letter, index })).sort((a, b) => cmp(a.letter, b.letter));
}

/** A list shorter than this has no rail: it scrolls by hand in a moment. */
export const RAIL_MIN_ITEMS = 40;

/** The rail of a list in name order: its letters, or none for a short list or a single letter. */
export function railLetters(names: string[]): LetterEntry[] {
  if (names.length < RAIL_MIN_ITEMS) return [];
  const all = letterIndex(names);
  return all.length > 1 ? all : [];
}

/**
 * The letters a rail with room for `room` shows: all of them when they fit (or the room isn't
 * known yet), else `room` of them spread evenly, the first and the last always among them.
 */
export function railOf(entries: LetterEntry[], room: number): LetterEntry[] {
  if (room <= 0 || entries.length <= room) return entries;
  if (room === 1) return entries.slice(0, 1);
  const picked = new Set<number>();
  for (let k = 0; k < room; k++) picked.add(Math.round((k * (entries.length - 1)) / (room - 1)));
  return [...picked].map((i) => entries[i]);
}
