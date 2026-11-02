import { describe, expect, it } from 'vitest';
import type { GenreCount } from './api';
import { Latest, matchGenres, peopleQuery } from './palette-search';

describe('peopleQuery', () => {
  it('asks the server from two characters on, with the query trimmed', () => {
    expect(peopleQuery('ke')).toBe('ke');
    expect(peopleQuery('  keanu ')).toBe('keanu');
  });

  it('is null while the query is too short, however much space is around it', () => {
    expect(peopleQuery('')).toBeNull();
    expect(peopleQuery('k')).toBeNull();
    expect(peopleQuery('  k  ')).toBeNull();
    expect(peopleQuery('     ')).toBeNull();
  });

  it('counts characters, not UTF-16 units', () => {
    expect(peopleQuery('😀')).toBeNull();
    expect(peopleQuery('😀😀')).toBe('😀😀');
  });
});

describe('Latest', () => {
  it('the ticket of the newest request is current', () => {
    const latest = new Latest();
    expect(latest.isCurrent(latest.begin())).toBe(true);
  });

  it('a newer request makes the older answer stale', () => {
    const latest = new Latest();
    const slow = latest.begin();
    const fast = latest.begin();
    expect(latest.isCurrent(slow)).toBe(false);
    expect(latest.isCurrent(fast)).toBe(true);
  });

  it('cancel makes every request in flight stale, and the next one starts fresh', () => {
    const latest = new Latest();
    const t = latest.begin();
    latest.cancel();
    expect(latest.isCurrent(t)).toBe(false);
    expect(latest.isCurrent(latest.begin())).toBe(true);
  });

  it('answers that arrive out of order: only the newest is used', async () => {
    const latest = new Latest();
    let shown: string[] = [];
    const ask = (q: string, ms: number) => {
      const ticket = latest.begin();
      return new Promise<void>((resolve) =>
        setTimeout(() => {
          if (latest.isCurrent(ticket)) shown = [q];
          resolve();
        }, ms),
      );
    };
    await Promise.all([ask('ke', 20), ask('kea', 1)]);
    expect(shown).toEqual(['kea']);
  });
});

describe('matchGenres', () => {
  const genres: GenreCount[] = [
    { id: 28, name: 'Action', count: 12 },
    { id: 18, name: 'Drama', count: 30 },
    { id: 878, name: 'Science Fiction', count: 4 },
    { id: 99, name: 'Documentary', count: 1 },
    { id: 10, name: 'Adventure', count: 7 },
  ];
  const names = (q: string, limit?: number) => matchGenres(genres, q, limit).map((g) => g.name);

  it('finds a genre by what is typed, ignoring case', () => {
    expect(names('drama')).toEqual(['Drama']);
    expect(names('SCIENCE')).toEqual(['Science Fiction']);
  });

  it('matches letters in order, the way titles do', () => {
    expect(names('drm')).toEqual(['Drama']);
    expect(names('fi')).toEqual(['Science Fiction']);
    expect(names('mrd')).toEqual([]);
  });

  it('finds nothing for a blank query or one that matches no genre', () => {
    expect(names('')).toEqual([]);
    expect(names('   ')).toEqual([]);
    expect(names('xyz')).toEqual([]);
  });

  it('returns the genres themselves, counts included, and honours the limit', () => {
    expect(matchGenres(genres, 'action')[0]).toBe(genres[0]);
    expect(names('a', 2)).toHaveLength(2);
  });
});

describe('matchGenres: limit and order', () => {
  const genres: GenreCount[] = [
    { id: 1, name: 'Animation', count: 3 },
    { id: 2, name: 'Action', count: 12 },
    { id: 3, name: 'Adventure', count: 7 },
    { id: 4, name: 'Family', count: 2 },
    { id: 5, name: 'Fantasy', count: 5 },
  ];
  it('gives at most four by default, best match first', () => {
    expect(matchGenres(genres, 'a')).toHaveLength(4);
    expect(matchGenres(genres, 'action')[0].name).toBe('Action');
    expect(matchGenres(genres, 'fan')[0].name).toBe('Fantasy');
  });
});
