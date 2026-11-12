import { flushSync, mount, unmount } from 'svelte';
import { describe, expect, it, vi } from 'vitest';
import SearchPalette from '../components/SearchPalette.svelte';
import { api, type EpisodeCard, type GenreCount, type MovieCard } from './api';
import { search } from './fuzzy';
import { applyProgress, ensureLibrary, episodes } from './library.svelte';
import { palette } from './palette.svelte';
import { Latest, matchGenres, peopleQuery } from './palette-search';

// The real matcher, counted: the palette's searching is what must stop while it is closed.
vi.mock('./fuzzy', async (importOriginal) => {
  const real = await importOriginal<typeof import('./fuzzy')>();
  return { ...real, search: vi.fn(real.search) };
});

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

  it('searches the name alone: neither the id nor the count is matched', () => {
    expect(names('action')).toEqual(['Action']);
    expect(names('28')).toEqual([]);
    expect(names('12')).toEqual([]);
  });

  it('returns the genres themselves, counts included, and honours the limit', () => {
    expect(matchGenres(genres, 'action')[0]).toBe(genres[0]);
    expect(names('a', 2)).toHaveLength(2);
  });
});

describe('SearchPalette', () => {
  const dune: MovieCard = {
    id: 1, kind: 'movie', title: 'Dune', sortTitle: 'Dune', addedAt: 0, durationSec: 0, container: 'mkv',
    width: 0, height: 0, art: null, rating: null, watched: false, progress: null, matchState: 'matched', needsReview: false,
  };
  const pilot: EpisodeCard = {
    id: 2, kind: 'episode', title: '', show: 'Lost', season: 1, episode: 1, episodeTitle: 'Pilot', durationSec: 0,
    width: 0, height: 0, addedAt: 0,
  };

  it('does no searching once closed, however often a progress report patches the lists', async () => {
    vi.spyOn(api, 'movieCards').mockResolvedValue([dune]);
    vi.spyOn(api, 'episodeCards').mockResolvedValue([pilot]);
    vi.spyOn(api, 'shows').mockResolvedValue([]);
    vi.spyOn(api, 'genres').mockResolvedValue([]);
    vi.spyOn(api, 'people').mockResolvedValue([]);
    const target = document.body.appendChild(document.createElement('div'));
    const cmp = mount(SearchPalette, { target });
    try {
      palette.open = true;
      flushSync();
      await ensureLibrary();
      expect(episodes.get(pilot.id)).toBeDefined(); // positive control: the episode is in the lists
      const input = target.querySelector('input')!;
      input.value = 'dune';
      input.dispatchEvent(new Event('input', { bubbles: true }));
      flushSync();
      expect(target.querySelector('li[role="option"] .title')?.textContent).toBe('Dune');

      const searches = () => vi.mocked(search).mock.calls.length;
      let before = searches();
      applyProgress(dune.id, { positionSec: 10, watched: false });
      flushSync();
      expect(searches()).toBeGreaterThan(before); // positive control: open, a patch searches again
      before = searches();
      applyProgress(pilot.id, { positionSec: 10, watched: false });
      flushSync();
      expect(searches()).toBeGreaterThan(before); // and one of an episode

      // Closed, before the close event (the browser fires it in a later task): the query is still typed in.
      const dialog = target.querySelector('dialog')!;
      const closeEvent = new Promise((r) => dialog.addEventListener('close', r, { once: true }));
      palette.open = false;
      flushSync();
      expect(dialog.hasAttribute('open')).toBe(false);
      expect(input.value).toBe('dune');
      before = searches();
      applyProgress(dune.id, { positionSec: 20, watched: false });
      flushSync();
      expect(searches()).toBe(before);

      // After it, the query is gone too, and still nothing is searched.
      await closeEvent;
      flushSync();
      expect(input.value).toBe('');
      applyProgress(dune.id, { positionSec: 30, watched: false });
      applyProgress(pilot.id, { positionSec: 40, watched: false });
      flushSync();
      expect(searches()).toBe(before);
    } finally {
      unmount(cmp);
      target.remove();
      palette.open = false;
    }
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

  it('puts a word that starts with the query before one that holds it later, whatever the given order', () => {
    const g = (id: number, name: string) => ({ id, name, count: 1 });
    expect(matchGenres([g(1, 'Science Fiction'), g(2, 'Fiction')], 'fic').map((x) => x.name)).toEqual(['Fiction', 'Science Fiction']);
  });

  it('keeps the given order (the biggest genre first) between equal matches', () => {
    const g = (id: number, name: string) => ({ id, name, count: 1 });
    expect(matchGenres([g(2, 'Dramedy'), g(1, 'Drama')], 'dram').map((x) => x.name)).toEqual(['Dramedy', 'Drama']);
    expect(matchGenres([g(1, 'Drama'), g(2, 'Dramedy')], 'dram').map((x) => x.name)).toEqual(['Drama', 'Dramedy']);
  });
});
