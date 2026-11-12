import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { ItemSummary, MovieCard } from './api';

vi.mock('./api', () => ({
  api: {
    items: vi.fn(),
    movieCards: vi.fn(),
    episodeCards: vi.fn(),
    shows: vi.fn(),
    setItemWatched: vi.fn(),
    setTitleWatched: vi.fn(),
    setShowWatched: vi.fn(),
    setSeasonWatched: vi.fn(),
  },
}));

type Fn = ReturnType<typeof vi.fn>;
type Api = Record<'items' | 'movieCards' | 'episodeCards' | 'shows' | 'setItemWatched' | 'setTitleWatched' | 'setShowWatched' | 'setSeasonWatched', Fn>;
// Fresh module state (the two lists, the stamps) for every test.
let api: Api;
let app: { libraryVersion: number };
let lib: typeof import('./library.svelte');
beforeEach(async () => {
  vi.resetModules();
  api = (await import('./api')).api as unknown as Api;
  // The mock outlives resetModules: every test starts from empty lists and no answers queued.
  for (const f of Object.values(api)) f.mockReset();
  api.episodeCards.mockResolvedValue([]);
  api.movieCards.mockResolvedValue([]);
  api.shows.mockResolvedValue([]);
  app = (await import('./state.svelte')).app;
  lib = await import('./library.svelte');
});

let nextId = 1;
const ep = (show: string, season: number, episode: number, addedAt = 0): ItemSummary => ({
  id: nextId++,
  kind: 'episode',
  title: '',
  show,
  season,
  episode,
  durationSec: 0,
  container: 'mp4',
  videoCodec: 'h264',
  audioCodec: 'aac',
  width: 0,
  height: 0,
  addedAt,
});
/** A full movie row, for the helpers that take any row. */
const movie = (title: string): ItemSummary => ({ ...ep('', 0, 0), kind: 'movie', show: undefined, title });
/** A movie as the card list sends it. */
const card = (title: string, titleId?: number, o: Partial<MovieCard> = {}): MovieCard => ({
  id: nextId++,
  kind: 'movie',
  title,
  sortTitle: title,
  titleId,
  addedAt: 0,
  durationSec: 0,
  container: 'mp4',
  width: 0,
  height: 0,
  art: null,
  rating: null,
  watched: false,
  progress: null,
  matchState: 'matched',
  needsReview: false,
  ...o,
});

function deferred<T>() {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => ((resolve = res), (reject = rej)));
  return { promise, resolve, reject };
}

/** The lists hold these (the movies as cards, the episodes); what findItem gives back is the cached row. */
async function cache(...rows: (MovieCard | ItemSummary)[]) {
  api.movieCards.mockResolvedValueOnce(rows.filter((r) => r.kind === 'movie'));
  api.episodeCards.mockResolvedValueOnce(rows.filter((r) => r.kind === 'episode'));
  await lib.ensureLibrary();
}

const seen = { positionSec: 0, watched: true };

describe('showsOf', () => {
  it('groups episodes, uses the first regular episode as the cover and counts seasons', () => {
    const special = ep('B', 0, 1, 50);
    const first = ep('B', 1, 1, 10);
    const items = [ep('B', 1, 2, 20), special, ep('B', 2, 1, 30), first, ep('A', 1, 1, 5), movie('Film')];
    const shows = lib.showsOf(items);
    expect(shows.map((s) => s.show)).toEqual(['A', 'B']);
    const b = shows[1];
    expect(b.cover).toBe(first); // not the special, though S00 sorts first numerically
    expect(b.seasons).toEqual([0, 1, 2]);
    expect(b.episodeCount).toBe(4);
    expect(b.latestAddedAt).toBe(50);
  });

  it('counts an episode with no season as season 0, and covers it like a special', () => {
    const loose = { ...ep('C', 1, 1), season: undefined };
    const first = ep('C', 2, 1);
    const c = lib.showsOf([loose, first])[0];
    expect(c.seasons).toEqual([0, 2]);
    expect(c.cover).toBe(first);
  });

  it('uses a special as the cover when a show has nothing else', () => {
    const only = ep('Extras', 0, 3);
    expect(lib.showsOf([ep('Extras', 0, 4), only])[0].cover).toBe(only);
  });

  it("attaches each show's metadata row, and only to its own show", () => {
    const meta = { show: 'B', seasons: [1], episodeCount: 1, poster: '/p.jpg', matchState: 'matched' as const, needsReview: false };
    const shows = lib.showsOf([ep('A', 1, 1), ep('B', 1, 1)], [meta]);
    expect(shows.find((s) => s.show === 'B')?.meta).toBe(meta);
    expect(shows.find((s) => s.show === 'A')?.meta).toBeUndefined();
  });
});

describe('byAdded, applyProgress, and what counts as a show', () => {
  it('sorts newest first', () => {
    const a = { ...movie('A'), addedAt: 10 };
    const b = { ...card('B'), addedAt: 30 };
    const c = { ...movie('C'), addedAt: 20 };
    expect([a, b, c].sort(lib.byAdded).map((m) => m.title)).toEqual(['B', 'C', 'A']);
  });

  it('writes a progress report into the cached card, and only that one', async () => {
    const a = card('A');
    const b = card('B');
    await cache(a, b);
    lib.applyProgress(a.id, { positionSec: 42, watched: false });
    expect(lib.findItem(a.id)?.progress).toEqual({ positionSec: 42, watched: false });
    expect(lib.findItem(b.id)?.progress).toBeNull();
    expect(a.progress).toBeNull(); // the fetched row is replaced, not changed
    lib.applyProgress(9999, { positionSec: 1, watched: false }); // not in the library: nothing happens
    expect(lib.findItem(9999)).toBeUndefined();
    expect(lib.movies.rows).toHaveLength(2);
  });

  it('a film with a show name is still not a show', () => {
    const odd = { ...movie('Odd'), show: 'Odd' };
    expect(lib.showsOf([odd, ep('Real', 1, 1)]).map((s) => s.show)).toEqual(['Real']);
  });
});

describe('watch state in the cached lists', () => {
  it('applyWatch writes each file of the answer, and nothing else', async () => {
    const a = ep('Lost', 1, 1);
    const b = ep('Lost', 1, 2);
    await cache(a, b);
    lib.applyWatch([{ id: a.id, watched: true, watchedAt: 1700, playCount: 1, progress: seen }, { id: 9999, watched: true, watchedAt: 1, playCount: 1, progress: seen }]);
    expect(lib.findItem(a.id)).toMatchObject({ watched: true, watchedAt: 1700, playCount: 1, progress: seen });
    expect(lib.findItem(b.id)?.watched).toBeUndefined();
    expect(lib.findItem(9999)).toBeUndefined();
  });

  it("applyWatch gives every version of a film the film's state", async () => {
    const hd = card('Dune', 7);
    const uhd = card('Dune', 7);
    const other = card('Other', 8);
    await cache(hd, uhd, other);
    lib.applyWatch([{ id: hd.id, watched: true, watchedAt: 1800, playCount: 2, progress: seen }]);
    expect(lib.findItem(hd.id)).toMatchObject({ watched: true, progress: seen });
    expect(lib.findItem(uhd.id)?.watched).toBe(true); // a card has no time or count to follow
    expect(lib.findItem(other.id)?.watched).toBe(false);
  });

  it('applyProgress makes a card watched when the answer says so, its film in all versions', async () => {
    const hd = card('Dune', 7);
    const uhd = card('Dune', 7);
    const other = card('Other', 8);
    await cache(hd, uhd, other);
    lib.applyProgress(hd.id, { positionSec: 6000, watched: false });
    expect(lib.findItem(hd.id)?.watched).toBe(false); // positive control: a report short of the end is not watched
    expect(lib.findItem(uhd.id)?.watched).toBe(false);
    lib.applyProgress(hd.id, { positionSec: 6900, watched: true });
    expect(lib.findItem(hd.id)).toMatchObject({ watched: true, progress: { positionSec: 6900, watched: true } });
    expect(lib.findItem(uhd.id)?.watched).toBe(true);
    expect(lib.findItem(other.id)?.watched).toBe(false);
  });

  it('applyProgress gives an episode watched to the end its time and count', async () => {
    const e = ep('Lost', 1, 1);
    await cache(e);
    lib.applyProgress(e.id, { positionSec: 2600, watched: true });
    expect(lib.findItem(e.id)).toMatchObject({ watched: true, playCount: 1 });
    expect((lib.findItem(e.id) as ItemSummary).watchedAt).toBeGreaterThan(0);
  });

  it('applyProgress leaves a watched item watched when it is played again', async () => {
    const e = { ...ep('Lost', 1, 1), watched: true, watchedAt: 100, playCount: 3, progress: seen };
    const c = card('Dune', undefined, { watched: true, progress: seen });
    await cache(e, c);
    lib.applyProgress(e.id, { positionSec: 120, watched: false });
    expect(lib.findItem(e.id)).toMatchObject({ watched: true, watchedAt: 100, playCount: 3, progress: { positionSec: 120, watched: false } });
    lib.applyProgress(c.id, { positionSec: 60, watched: false });
    expect(lib.findItem(c.id)).toMatchObject({ watched: true, progress: { positionSec: 60, watched: false } });
  });

  it('markWatched sends a film with a title to the title, others to the item, a season and a show to theirs', async () => {
    const titled = card('Dune', 7);
    const loose = card('Home video');
    const e = ep('Lost', 0, 1);
    await cache(titled, loose, e);
    const ok = { watched: true, items: [] };
    for (const f of [api.setItemWatched, api.setTitleWatched, api.setShowWatched, api.setSeasonWatched]) f.mockResolvedValue(ok);
    await lib.markWatched({ item: titled }, true);
    expect(api.setTitleWatched).toHaveBeenLastCalledWith(7, true);
    await lib.markWatched({ item: loose }, false);
    expect(api.setItemWatched).toHaveBeenLastCalledWith(loose.id, false);
    await lib.markWatched({ item: e }, true);
    expect(api.setItemWatched).toHaveBeenLastCalledWith(e.id, true);
    await lib.markWatched({ show: 'Lost', season: 0 }, true); // the specials
    expect(api.setSeasonWatched).toHaveBeenLastCalledWith('Lost', 0, true);
    await lib.markWatched({ show: 'Lost' }, false);
    expect(api.setShowWatched).toHaveBeenLastCalledWith('Lost', false);
    expect(api.setTitleWatched).toHaveBeenCalledTimes(1);
  });

  it('markWatched changes every version of a film at once, and puts them all back on an error', async () => {
    const hd = card('Dune', 7);
    const uhd = card('Dune', 7);
    const other = card('Other', 8);
    await cache(hd, uhd, other);
    const answer = deferred<never>();
    api.setTitleWatched.mockReturnValueOnce(answer.promise);
    const done = lib.markWatched({ item: hd }, true);
    expect(lib.findItem(hd.id)).toMatchObject({ watched: true, progress: seen });
    expect(lib.findItem(uhd.id)).toMatchObject({ watched: true, progress: seen });
    expect(lib.findItem(other.id)?.watched).toBe(false);
    answer.reject(new Error('HTTP 500'));
    await expect(done).rejects.toThrow('HTTP 500');
    expect(lib.findItem(hd.id)).toMatchObject({ watched: false, progress: null });
    expect(lib.findItem(uhd.id)).toMatchObject({ watched: false, progress: null });
  });

  it('markWatched changes the list before the answer, then takes the answer', async () => {
    const a = ep('Lost', 1, 1, 0);
    const b = ep('Lost', 1, 2, 0);
    const other = ep('Lost', 2, 1, 0);
    await cache(a, b, other);
    const answer = deferred<{ watched: boolean; items: unknown[] }>();
    api.setSeasonWatched.mockReturnValueOnce(answer.promise);
    const done = lib.markWatched({ show: 'Lost', season: 1 }, true);
    expect(lib.findItem(a.id)).toMatchObject({ watched: true, progress: seen });
    expect(lib.findItem(b.id)?.watched).toBe(true);
    expect(lib.findItem(other.id)?.watched).toBeUndefined(); // another season
    answer.resolve({ watched: true, items: [{ id: a.id, watched: true, watchedAt: 5, playCount: 4, progress: seen }] });
    await done;
    expect(lib.findItem(a.id)).toMatchObject({ watchedAt: 5, playCount: 4 });
  });

  it('markWatched puts the list back as it was when the server fails, and throws', async () => {
    const started = { positionSec: 600, watched: false };
    const a = ep('Lost', 1, 1);
    const b = { ...ep('Lost', 1, 2), watched: true, watchedAt: 50, playCount: 2, progress: seen };
    const c = { ...ep('Lost', 1, 3), progress: started };
    await cache(a, b, c);
    let fail!: (e: Error) => void;
    api.setShowWatched.mockReturnValueOnce(new Promise((_, reject) => (fail = reject)));
    const done = lib.markWatched({ show: 'Lost' }, false);
    expect(lib.findItem(b.id)).toMatchObject({ watched: false, progress: null }); // positive control: it did change
    fail(new Error('HTTP 500'));
    await expect(done).rejects.toThrow('HTTP 500');
    expect(lib.findItem(a.id)?.watched).toBeUndefined();
    expect(lib.findItem(b.id)).toMatchObject({ watched: true, watchedAt: 50, playCount: 2, progress: seen });
    expect(lib.findItem(c.id)?.progress).toEqual(started);
  });

  it('showsOf counts how far through its episodes a show is, from the cached items', () => {
    const counts = lib.showsOf([
      { ...ep('Lost', 1, 1), watched: true },
      { ...ep('Lost', 1, 2), progress: { positionSec: 300, watched: false } },
      ep('Lost', 1, 3),
      { ...ep('Lost', 0, 1), watched: true }, // a special: not in the show's counts
    ])[0].counts;
    expect(counts).toEqual({ episodeCount: 3, watchedEpisodes: 1, inProgress: 1 });
  });

  it("titleBadge reads a film card's state from the cached list, else from the card", async () => {
    const a = card('Dune', 7);
    await cache(a);
    expect(lib.titleBadge({ kind: 'movie', itemId: a.id, watched: false })).toBeNull();
    lib.applyWatch([{ id: a.id, watched: true, watchedAt: 1, playCount: 1, progress: seen }]);
    expect(lib.titleBadge({ kind: 'movie', itemId: a.id, watched: false })?.label).toBe('Watched');
    expect(lib.titleBadge({ kind: 'movie', itemId: 4242, watched: true })?.label).toBe('Watched'); // not cached: the card's
  });

  it("titleBadge counts a series over all its linked shows: the card's counts, plus what changed here since", async () => {
    // One title, two shows in the library ("Lost" and "Lost (2004)"): the server counts 4 episodes, 1 watched.
    const a1 = { ...ep('Lost', 1, 1), watched: true };
    const a2 = ep('Lost', 1, 2);
    const b1 = ep('Lost (2004)', 1, 3);
    const b2 = ep('Lost (2004)', 1, 4);
    await cache(a1, a2, b1, b2);
    const series = { kind: 'series' as const, show: 'Lost', episodeCount: 4, watchedEpisodes: 1, inProgress: 0 };
    expect(lib.titleBadge(series)?.text).toBe('3 left'); // not one show's "1 left"
    api.setItemWatched.mockResolvedValueOnce({ watched: true, items: [{ id: a2.id, watched: true, watchedAt: 1, playCount: 1, progress: seen }] });
    await lib.markWatched({ item: a2 }, true);
    // The mark in Lost, over both shows. The mark made a new episode array: a snapshot keyed on the
    // array (not on the list's epoch) would be gone, and this would still say "3 left".
    expect(lib.titleBadge(series)?.text).toBe('2 left');
    expect(lib.titleBadge({ kind: 'series', show: 'Fringe', episodeCount: 5, watchedEpisodes: 5, inProgress: 0 })?.text).toBe('✓');
  });

  it("titleBadge drops what changed here once a new episode list has come: the card's counts have it", async () => {
    const a1 = { ...ep('Lost', 1, 1), watched: true };
    const a2 = ep('Lost', 1, 2);
    const a3 = ep('Lost', 1, 3);
    await cache(a1, a2, a3);
    api.setItemWatched.mockResolvedValueOnce({ watched: true, items: [{ id: a2.id, watched: true, watchedAt: 1, playCount: 1, progress: seen }] });
    await lib.markWatched({ item: a2 }, true);
    expect(lib.titleBadge({ kind: 'series', show: 'Lost', episodeCount: 3, watchedEpisodes: 1, inProgress: 0 })?.text).toBe('1 left'); // positive control
    app.libraryVersion++;
    await cache(a1, { ...a2, watched: true }, a3); // the server has the mark
    // Fresh counts (2 watched) with nothing added: a snapshot kept from the old list would add the
    // mark again and say "✓".
    expect(lib.titleBadge({ kind: 'series', show: 'Lost', episodeCount: 3, watchedEpisodes: 2, inProgress: 0 })?.text).toBe('1 left');
  });

  it('episodesOf (and findItem) follow a new list after a rescan', async () => {
    await cache(ep('Lost', 1, 1));
    expect(lib.episodesOf('Lost')).toHaveLength(1);
    app.libraryVersion++;
    const b = ep('Lost', 1, 2);
    await cache(ep('Lost', 1, 1), b);
    expect(lib.episodesOf('Lost')).toHaveLength(2);
    expect(lib.findItem(b.id)?.id).toBe(b.id);
  });

  it('a rollback leaves alone a file a later mark changed (a season mark failing after an episode unmark)', async () => {
    const a = { ...ep('Lost', 1, 1), watched: true, watchedAt: 9, playCount: 1, progress: seen };
    const b = ep('Lost', 1, 2);
    await cache(a, b);
    let fail!: (e: Error) => void;
    api.setSeasonWatched.mockReturnValueOnce(new Promise((_, reject) => (fail = reject)));
    const season = lib.markWatched({ show: 'Lost', season: 1 }, true);
    api.setItemWatched.mockResolvedValueOnce({ watched: false, items: [{ id: a.id, watched: false, watchedAt: null, playCount: 0, progress: null }] });
    await lib.markWatched({ item: a }, false);
    expect(lib.findItem(b.id)?.watched).toBe(true); // positive control: the season mark is still showing
    fail(new Error('HTTP 500'));
    await expect(season).rejects.toThrow('HTTP 500');
    expect(lib.findItem(b.id)?.watched).toBeFalsy(); // the season's own change undone
    expect(lib.findItem(a.id)).toMatchObject({ watched: false, progress: null }); // the later unmark kept
  });

  it('a late answer does not overwrite a newer toggle', async () => {
    const a = ep('Lost', 1, 1);
    await cache(a);
    let first!: (r: unknown) => void;
    api.setItemWatched.mockReturnValueOnce(new Promise((r) => (first = r)));
    const mark = lib.markWatched({ item: a }, true);
    api.setItemWatched.mockResolvedValueOnce({ watched: false, items: [{ id: a.id, watched: false, watchedAt: null, playCount: 0, progress: null }] });
    await lib.markWatched({ item: a }, false);
    expect(lib.findItem(a.id)?.watched).toBe(false);
    first({ watched: true, items: [{ id: a.id, watched: true, watchedAt: 5, playCount: 1, progress: seen }] });
    await mark;
    expect(lib.findItem(a.id)).toMatchObject({ watched: false, progress: null });
  });

  it("a progress report made while a mark is out is not overwritten by the mark's answer", async () => {
    const a = ep('Lost', 1, 1);
    await cache(a);
    const answer = deferred<{ watched: boolean; items: unknown[] }>();
    api.setItemWatched.mockReturnValueOnce(answer.promise);
    const mark = lib.markWatched({ item: a }, true);
    expect(lib.findItem(a.id)?.progress).toEqual(seen); // positive control: the mark shows
    lib.applyProgress(a.id, { positionSec: 300, watched: false }); // played again meanwhile
    answer.resolve({ watched: true, items: [{ id: a.id, watched: true, watchedAt: 5, playCount: 1, progress: seen }] });
    await mark;
    expect(lib.findItem(a.id)?.progress).toEqual({ positionSec: 300, watched: false });
  });

  it("a progress report made while a mark is out is not undone by the mark's rollback", async () => {
    const a = ep('Lost', 1, 1);
    await cache(a);
    const answer = deferred<never>();
    api.setItemWatched.mockReturnValueOnce(answer.promise);
    const mark = lib.markWatched({ item: a }, true);
    expect(lib.findItem(a.id)?.watched).toBe(true); // positive control: the mark shows
    lib.applyProgress(a.id, { positionSec: 300, watched: false });
    answer.reject(new Error('HTTP 500'));
    await expect(mark).rejects.toThrow('HTTP 500');
    expect(lib.findItem(a.id)?.progress).toEqual({ positionSec: 300, watched: false });
  });

  it('markWatched gives the files their time and count before the answer: kept when watched, cleared on unmark', async () => {
    const fresh = ep('Lost', 1, 1);
    // Watched, but started again: marked, it keeps its time and count and goes back to position 0.
    const rewatching = { ...ep('Lost', 1, 2), watched: true, watchedAt: 50, playCount: 2, progress: { positionSec: 300, watched: false } };
    await cache(fresh, rewatching);
    const start = Math.floor(Date.now() / 1000);
    const marked = deferred<{ watched: boolean; items: unknown[] }>();
    api.setSeasonWatched.mockReturnValueOnce(marked.promise);
    const mark = lib.markWatched({ show: 'Lost', season: 1 }, true);
    expect(lib.findItem(fresh.id)).toMatchObject({ watched: true, playCount: 1, progress: seen });
    expect((lib.findItem(fresh.id) as ItemSummary).watchedAt).toBeGreaterThanOrEqual(start);
    expect(lib.findItem(rewatching.id)).toMatchObject({ watched: true, watchedAt: 50, playCount: 2, progress: seen });
    marked.resolve({ watched: true, items: [] });
    await mark;

    const unmarked = deferred<{ watched: boolean; items: unknown[] }>();
    api.setSeasonWatched.mockReturnValueOnce(unmarked.promise);
    const unmark = lib.markWatched({ show: 'Lost', season: 1 }, false);
    for (const id of [fresh.id, rewatching.id]) {
      expect(lib.findItem(id)).toMatchObject({ watched: false, watchedAt: null, playCount: 0, progress: null });
    }
    unmarked.resolve({ watched: false, items: [] });
    await unmark;
  });

  it('titleBadge follows a progress report in a series, and its count of episodes in progress', async () => {
    const a1 = { ...ep('Lost', 1, 1), watched: true };
    const a2 = ep('Lost', 1, 2);
    const a3 = ep('Lost', 1, 3);
    await cache(a1, a2, a3);
    const series = { kind: 'series' as const, show: 'Lost', episodeCount: 3, watchedEpisodes: 1, inProgress: 0 };
    expect(lib.titleBadge(series)?.label).toBe('2 episodes left'); // positive control: nothing started
    lib.applyProgress(a2.id, { positionSec: 300, watched: false });
    expect(lib.titleBadge(series)).toMatchObject({ text: '2 left', label: '2 episodes left, 1 in progress', started: true });
    lib.applyProgress(a2.id, { positionSec: 2600, watched: true });
    expect(lib.titleBadge(series)).toMatchObject({ text: '1 left', label: '1 episode left', started: false });
  });

  it("titleBadge takes an episode the card counts in progress out of them once it is watched here", async () => {
    const a1 = { ...ep('Lost', 1, 1), watched: true };
    const a2 = { ...ep('Lost', 1, 2), progress: { positionSec: 300, watched: false } };
    const a3 = ep('Lost', 1, 3);
    await cache(a1, a2, a3);
    const series = { kind: 'series' as const, show: 'Lost', episodeCount: 3, watchedEpisodes: 1, inProgress: 1 };
    expect(lib.titleBadge(series)?.label).toBe('2 episodes left, 1 in progress'); // positive control
    api.setItemWatched.mockResolvedValueOnce({ watched: true, items: [{ id: a2.id, watched: true, watchedAt: 1, playCount: 1, progress: seen }] });
    await lib.markWatched({ item: a2 }, true);
    expect(lib.titleBadge(series)).toMatchObject({ text: '1 left', label: '1 episode left', started: false });
  });
});

describe('byTitle', () => {
  it('sorts numbers by value and ignores case', () => {
    const sorted = [movie('Movie 10'), card('Movie 2'), movie('movie 1')].sort(lib.byTitle);
    expect(sorted.map((m) => m.title)).toEqual(['movie 1', 'Movie 2', 'Movie 10']);
  });
});

describe('ensureLibrary, ensureMovies, ensureEpisodes', () => {
  afterEach(() => vi.unstubAllGlobals());

  /** Answer the list calls with the real client over a stubbed fetch (every answer []); returns the URLs asked for. */
  async function realClient() {
    const real = (await vi.importActual<typeof import('./api')>('./api')).api;
    api.episodeCards.mockImplementation(real.episodeCards);
    api.movieCards.mockImplementation(real.movieCards);
    api.shows.mockImplementation(real.shows);
    const fetch = vi.fn((_url: string) => Promise.resolve(new Response('[]', { headers: { 'Content-Type': 'application/json' } })));
    vi.stubGlobal('fetch', fetch);
    return () => fetch.mock.calls.map((c) => c[0]);
  }

  it('ensureMovies asks for the movie cards and nothing else', async () => {
    const urls = await realClient();
    await lib.ensureMovies();
    expect(urls()).toEqual(['/api/items?kind=movie&fields=card']);
    expect(lib.movies.loaded).toBe(true);
    expect(lib.episodes.loaded).toBe(false);
  });

  it('ensureEpisodes asks for the episode cards and the show list, never the movies', async () => {
    const urls = await realClient();
    await lib.ensureEpisodes();
    expect(urls().sort()).toEqual(['/api/items?kind=episode&fields=card', '/api/shows']);
    expect(lib.episodes.loaded).toBe(true);
    expect(lib.movies.loaded).toBe(false);
  });

  it('ensureLibrary loads both lists, each once per library version, shared between callers', async () => {
    const a = card('A');
    const e = ep('Lost', 1, 1);
    api.movieCards.mockResolvedValue([a]);
    api.episodeCards.mockResolvedValue([e]);
    expect(lib.bothLists.loaded).toBe(false);
    await Promise.all([lib.ensureLibrary(), lib.ensureMovies(), lib.ensureEpisodes(), lib.ensureLibrary()]);
    expect(api.movieCards).toHaveBeenCalledTimes(1);
    expect(api.episodeCards).toHaveBeenCalledTimes(1);
    expect(api.episodeCards).toHaveBeenCalledWith();
    expect(lib.movies.rows).toEqual([a]);
    expect(lib.episodes.rows).toEqual([e]);
    expect(lib.bothLists.loaded).toBe(true);
    await lib.ensureLibrary();
    expect(api.movieCards).toHaveBeenCalledTimes(1);
    expect(api.episodeCards).toHaveBeenCalledTimes(1);
  });

  it('fetches both again after a scan bumps the library version', async () => {
    await lib.ensureLibrary();
    app.libraryVersion++;
    await lib.ensureLibrary();
    expect(api.movieCards).toHaveBeenCalledTimes(2);
    expect(api.episodeCards).toHaveBeenCalledTimes(2);
  });

  it('does not reuse a fetch from before a scan, and a late old response cannot overwrite the new one', async () => {
    const before = deferred<MovieCard[]>();
    const after = deferred<MovieCard[]>();
    api.movieCards.mockReturnValueOnce(before.promise).mockReturnValueOnce(after.promise);
    const oldCard = card('Old');
    const newCard = card('New');

    const p1 = lib.ensureMovies();
    app.libraryVersion++; // a scan finished while the first fetch was in flight
    const p2 = lib.ensureMovies();
    expect(api.movieCards).toHaveBeenCalledTimes(2);

    after.resolve([newCard]);
    await p2;
    expect(lib.movies.rows).toEqual([newCard]);
    before.resolve([oldCard]);
    await p1;
    expect(lib.movies.rows).toEqual([newCard]);
  });

  it('reports a failure and keeps what it had', async () => {
    const a = card('A');
    api.movieCards.mockResolvedValueOnce([a]).mockRejectedValueOnce(new Error('Cannot reach the ply server'));
    await lib.ensureMovies();
    app.libraryVersion++;
    await lib.ensureMovies();
    expect(lib.movies.error).toBe('Cannot reach the ply server');
    expect(lib.movies.rows).toEqual([a]);
  });

  it('the two lists fail on their own', async () => {
    const e = ep('Lost', 1, 1);
    api.movieCards.mockRejectedValueOnce(new Error('HTTP 500'));
    api.episodeCards.mockResolvedValueOnce([e]);
    await lib.ensureLibrary();
    expect(lib.movies.error).toBe('HTTP 500');
    expect(lib.movies.loaded).toBe(false);
    expect(lib.episodes.error).toBe('');
    expect(lib.episodes.loaded).toBe(true);
    expect(lib.findItem(e.id)).toEqual(e);
    expect(lib.bothLists.loaded).toBe(false);
    expect(lib.bothLists.error).toBe('HTTP 500');

    // And the other way round, after a scan: the episodes fail, keeping theirs.
    app.libraryVersion++;
    const a = card('A');
    api.movieCards.mockResolvedValueOnce([a]);
    api.episodeCards.mockRejectedValueOnce(new Error('Cannot reach the ply server'));
    await lib.ensureLibrary();
    expect(lib.movies.error).toBe('');
    expect(lib.movies.loaded).toBe(true);
    expect(lib.findItem(a.id)).toEqual(a);
    expect(lib.episodes.error).toBe('Cannot reach the ply server');
    expect(lib.episodes.rows).toEqual([e]);
  });

  it('loads the show list with the episodes, and a failing show list costs only the metadata', async () => {
    const a = ep('A', 1, 1);
    const meta = { show: 'A', seasons: [1], episodeCount: 1, poster: '/p.jpg', matchState: 'matched', needsReview: false };
    api.episodeCards.mockResolvedValue([a]);
    api.shows.mockResolvedValueOnce([meta]).mockRejectedValueOnce(new Error('HTTP 500'));
    await lib.ensureEpisodes();
    expect(lib.episodes.extra).toEqual([meta]);
    expect(lib.showMeta('A')).toEqual(meta);
    expect(lib.showsOf(lib.episodes.rows)[0].meta).toEqual(meta); // by default, with the cached show list
    app.libraryVersion++;
    await lib.ensureEpisodes();
    expect(api.episodeCards).toHaveBeenCalledTimes(2); // positive control: the second load did run
    expect(lib.episodes.error).toBe('');
    expect(lib.episodes.loaded).toBe(true);
    expect(lib.episodes.rows).toEqual([a]);
    expect(lib.episodes.extra).toEqual([]);
    expect(lib.showMeta('A')).toBeUndefined();
  });
});

describe('writes while a list loads', () => {
  it('a progress report answered while the card list is in flight survives its arrival', async () => {
    const a = card('A');
    await cache(a);
    app.libraryVersion++;
    const next = deferred<MovieCard[]>();
    api.movieCards.mockReturnValueOnce(next.promise);
    const loading = lib.ensureMovies();
    lib.applyProgress(a.id, { positionSec: 42, watched: false });
    expect(lib.findItem(a.id)?.progress).toEqual({ positionSec: 42, watched: false }); // the list held shows it at once
    next.resolve([{ ...a, title: 'A (rescanned)' }]); // read by the server before the report
    await loading;
    expect(lib.findItem(a.id)?.title).toBe('A (rescanned)'); // positive control: the new list landed
    expect(lib.findItem(a.id)?.progress).toEqual({ positionSec: 42, watched: false });
  });

  it('a progress report made before the first lists arrive is kept, in the list that has the row', async () => {
    const a = card('A');
    const e = ep('Lost', 1, 1);
    const cards = deferred<MovieCard[]>();
    const eps = deferred<ItemSummary[]>();
    api.movieCards.mockReturnValueOnce(cards.promise);
    api.episodeCards.mockReturnValueOnce(eps.promise);
    const loading = lib.ensureLibrary();
    expect(lib.findItem(a.id)).toBeUndefined();
    lib.applyProgress(a.id, { positionSec: 42, watched: false });
    lib.applyProgress(e.id, { positionSec: 2600, watched: true });
    cards.resolve([a]);
    eps.resolve([e]);
    await loading;
    expect(lib.findItem(a.id)?.progress).toEqual({ positionSec: 42, watched: false });
    expect(lib.findItem(e.id)).toMatchObject({ watched: true, progress: { positionSec: 2600, watched: true } });
    // Each row is in its own list only (findItem found both above).
    expect(lib.movies.get(e.id)).toBeUndefined();
    expect(lib.episodes.get(a.id)).toBeUndefined();
  });

  it('the answer to a mark made before the list arrives is kept', async () => {
    const e = ep('Lost', 1, 1);
    const eps = deferred<ItemSummary[]>();
    api.episodeCards.mockReturnValueOnce(eps.promise);
    const loading = lib.ensureEpisodes();
    lib.applyWatch([{ id: e.id, watched: true, watchedAt: 5, playCount: 2, progress: seen }]);
    eps.resolve([e]);
    await loading;
    expect(lib.findItem(e.id)).toMatchObject({ watched: true, watchedAt: 5, playCount: 2, progress: seen });
  });

  it('a rollback after a refetch writes the saved fields onto the new row, not the old one', async () => {
    const a = card('Dune', 7);
    await cache(a);
    const answer = deferred<never>();
    api.setTitleWatched.mockReturnValueOnce(answer.promise);
    const mark = lib.markWatched({ item: a }, true);
    expect(lib.findItem(a.id)?.watched).toBe(true); // positive control: the mark shows
    app.libraryVersion++; // a scan lands while the mark is out; the server already has the mark
    api.movieCards.mockResolvedValueOnce([{ ...a, title: 'Dune: Part One', watched: true, progress: seen }]);
    await lib.ensureMovies();
    expect(lib.findItem(a.id)?.title).toBe('Dune: Part One');
    answer.reject(new Error('HTTP 500'));
    await expect(mark).rejects.toThrow('HTTP 500');
    expect(lib.findItem(a.id)).toMatchObject({ title: 'Dune: Part One', watched: false, progress: null });
    expect(a).toMatchObject({ title: 'Dune', watched: false }); // the old row is not changed either
  });
});

describe('the rows the helpers hand out', () => {
  it('versionsOf lists the cards of a film as they are now', async () => {
    const hd = card('Dune', 7, { height: 1080 });
    const uhd = card('Dune', 7, { height: 2160 });
    const other = card('Other', 8);
    const loose = card('Home video');
    await cache(hd, uhd, other, loose);
    expect(lib.versionsOf(7).map((c) => c.id)).toEqual([hd.id, uhd.id]);
    expect(lib.versionsOf(8)).toEqual([other]);
    const dune = lib.versionsOf(7);
    expect(lib.versionsOf(7)).toBe(dune); // the same array until a patch
    lib.applyProgress(uhd.id, { positionSec: 5, watched: false });
    expect(lib.versionsOf(7)).not.toBe(dune);
    expect(lib.versionsOf(7)[1].progress).toEqual({ positionSec: 5, watched: false }); // the patched row, not the fetched one
    expect(lib.versionsOf(7)).toBe(lib.versionsOf(7));
  });

  it('episodesOf gives the episodes of a show as they are now', async () => {
    const a = ep('Lost', 1, 1);
    const b = ep('Lost', 1, 2);
    await cache(a, b, ep('Fringe', 1, 1), card('Lost'));
    expect(lib.episodesOf('Lost').map((e) => e.id)).toEqual([a.id, b.id]);
    const lost = lib.episodesOf('Lost');
    expect(lib.episodesOf('Lost')).toBe(lost); // the same array until a patch
    api.setItemWatched.mockResolvedValueOnce({ watched: true, items: [{ id: b.id, watched: true, watchedAt: 3, playCount: 1, progress: seen }] });
    await lib.markWatched({ item: b }, true);
    expect(lib.episodesOf('Lost')).not.toBe(lost);
    expect(lib.episodesOf('Lost')[1]).toMatchObject({ id: b.id, watched: true, watchedAt: 3 });
    expect(lib.episodesOf('Lost')).toBe(lib.episodesOf('Lost'));
  });

  it('libraryRows is both lists, the same array until one of them changes', async () => {
    const a = card('A');
    const e = ep('Lost', 1, 1);
    await cache(a, e);
    const rows = lib.libraryRows();
    expect(rows).toEqual([a, e]);
    expect(lib.libraryRows()).toBe(rows);
    lib.applyProgress(e.id, { positionSec: 9, watched: false });
    expect(lib.libraryRows()).not.toBe(rows);
    expect(lib.libraryRows()[1].progress).toEqual({ positionSec: 9, watched: false });
  });
});

describe('reactivity', () => {
  // Read through svelte/store's toStore, which runs `get` in an effect: imported after resetModules, it
  // is the same Svelte instance as the library. (A rune in this file would compile against the
  // instance loaded before the reset, which the re-imported library does not share.)
  async function watch<T>(get: () => T) {
    const { toStore } = await import('svelte/store');
    const { flushSync } = await import('svelte');
    const seen: T[] = [];
    const stop = toStore(get).subscribe((v) => seen.push(v));
    return { seen, flush: () => flushSync(), stop };
  }

  it('findItem wakes a reader when the list with its row lands', async () => {
    const a = card('A');
    const e = ep('Lost', 1, 1);
    const cards = deferred<MovieCard[]>();
    const eps = deferred<ItemSummary[]>();
    api.movieCards.mockReturnValueOnce(cards.promise);
    api.episodeCards.mockReturnValueOnce(eps.promise);
    const loading = lib.ensureLibrary();
    const film = await watch(() => lib.findItem(a.id));
    const episode = await watch(() => lib.findItem(e.id));
    expect(film.seen).toEqual([undefined]);

    cards.resolve([a]);
    await lib.ensureMovies(); // the card list's own fetch, in flight
    film.flush();
    expect(film.seen).toEqual([undefined, a]);
    expect(episode.seen).toEqual([undefined]); // its list is still out

    eps.resolve([e]);
    await loading;
    episode.flush();
    expect(episode.seen).toEqual([undefined, e]);

    lib.applyProgress(a.id, { positionSec: 3, watched: false });
    film.flush();
    expect(film.seen.at(-1)?.progress).toEqual({ positionSec: 3, watched: false });
    film.stop();
    episode.stop();
  });

  it('a series badge drawn before the first change to its list hears a mark and a progress report', async () => {
    const a1 = { ...ep('Lost', 1, 1), watched: true };
    const a2 = ep('Lost', 1, 2);
    const a3 = ep('Lost', 1, 3);
    await cache(a1, a2, a3);
    const series = { kind: 'series' as const, show: 'Lost', episodeCount: 3, watchedEpisodes: 1, inProgress: 0 };
    const badge = await watch(() => lib.titleBadge(series)?.label);
    expect(badge.seen).toEqual(['2 episodes left']);
    api.setItemWatched.mockResolvedValueOnce({ watched: true, items: [{ id: a2.id, watched: true, watchedAt: 1, playCount: 1, progress: seen }] });
    await lib.markWatched({ item: a2 }, true);
    badge.flush();
    expect(badge.seen.at(-1)).toBe('1 episode left');
    lib.applyProgress(a3.id, { positionSec: 300, watched: false });
    badge.flush();
    expect(badge.seen.at(-1)).toBe('1 episode left, 1 in progress');
    badge.stop();
  });
});
