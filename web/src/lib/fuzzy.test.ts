import { describe, expect, it } from 'vitest';
import type { ItemSummary } from './api';
import { fold, search } from './fuzzy';

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
const titles = (list: ItemSummary[]) => list.map((i) => i.title);

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
});

describe('fold', () => {
  it('strips combining marks and lower-cases', () => {
    expect(fold('Pokémon Ça Ñandú')).toBe('pokemon ca nandu');
  });
});
