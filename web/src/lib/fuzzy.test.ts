import { describe, expect, it } from 'vitest';
import type { ItemSummary, MovieCard } from './api';
import { fold, foldedHaystack, search } from './fuzzy';

let nextId = 1;
const item = (title: string, extra: Partial<ItemSummary> = {}): ItemSummary => ({
  id: nextId++,
  kind: 'movie',
  title,
  durationSec: 0,
  container: 'mp4',
  videoCodec: 'h264',
  audioCodec: 'aac',
  width: 0,
  height: 0,
  addedAt: 0,
  ...extra,
});
// A movie as the card list sends it: none of a full row's other fields.
const card = (title: string, extra: Partial<MovieCard> = {}): MovieCard => ({
  id: nextId++,
  kind: 'movie',
  title,
  sortTitle: title,
  addedAt: 0,
  durationSec: 0,
  width: 0,
  height: 0,
  container: 'mp4',
  watched: false,
  progress: null,
  matchState: 'matched',
  needsReview: false,
  art: null,
  rating: null,
  ...extra,
});
const titles = (list: { title: string }[]) => list.map((i) => i.title);

describe('search', () => {
  const library = [item('Blade Runner'), item('Carrie'), item('Arrival'), item('Paterson')];

  it('returns nothing for an empty or blank query', () => {
    expect(search(library, '')).toEqual([]);
    expect(search(library, '   ')).toEqual([]);
  });

  it('ranks a match at a word start over one inside a word, and both over a scattered match', () => {
    expect(titles(search(library, 'ar'))).toEqual(['Arrival', 'Carrie', 'Blade Runner', 'Paterson']);
  });

  it('requires every character in order', () => {
    expect(search(library, 'xyz')).toEqual([]);
    expect(titles(search(library, 'lvr'))).toEqual([]); // l..v..r is not in order in "Arrival"
  });

  it('ignores accents and case in both directions', () => {
    const accented = [item('Amélie'), item('Pokémon')];
    expect(titles(search(accented, 'amelie'))).toEqual(['Amélie']);
    expect(titles(search(accented, 'POKEMON'))).toEqual(['Pokémon']);
    expect(titles(search([item('Amelie')], 'Amélie'))).toEqual(['Amelie']);
  });

  it('also matches the show, the episode title and the year', () => {
    const ep = item('S01E01', { kind: 'episode', show: 'Night Shift', episodeTitle: 'Pilot' });
    const film = item('Paterson', { year: 2016 });
    expect(search([ep, film], 'pilot')).toEqual([ep]);
    expect(search([ep, film], 'night')).toEqual([ep]);
    expect(search([ep, film], '2016')).toEqual([film]);
  });

  it('respects the limit', () => {
    const many = Array.from({ length: 50 }, (_, i) => item(`Movie ${i}`));
    expect(search(many, 'movie', 5)).toHaveLength(5);
  });

  it('finds movie cards as it finds full rows, in the same order, and gives the cards back', () => {
    const names = ['Blade Runner', 'Carrie', 'Arrival', 'Paterson', 'Amélie'];
    const rows = names.map((t) => item(t));
    const cards = names.map((t) => card(t, { year: 2001 }));
    for (const q of ['ar', 'amelie', 'xyz', 'lvr', 'pat']) {
      expect(titles(search(cards, q))).toEqual(titles(search(rows, q)));
    }
    const hit: MovieCard[] = search(cards, 'carrie');
    expect(hit).toEqual([cards[1]]);
    expect(hit[0]).toBe(cards[1]);
    expect(search(cards, '2001')).toHaveLength(names.length);
  });
});

describe('foldedHaystack', () => {
  it('folds a row once: the same row object gives the text it gave first, a new object fresh text', () => {
    const row = card('Amélie', { year: 2001 });
    expect(foldedHaystack(row)).toBe('amelie 2001');
    // Rows are replaced, never changed; changing one in place shows the text is kept, not refolded.
    row.title = 'Pokémon';
    expect(foldedHaystack(row)).toBe('amelie 2001');
    expect(search([row], 'pokemon')).toEqual([]);
    const replaced = { ...row };
    expect(foldedHaystack(replaced)).toBe('pokemon 2001');
    expect(search([replaced], 'pokemon')).toEqual([replaced]);
  });
});

describe('fold', () => {
  it('strips combining marks and lower-cases', () => {
    expect(fold('Pokémon Ça Ñandú')).toBe('pokemon ca nandu');
  });
});
