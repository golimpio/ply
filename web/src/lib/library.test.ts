import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { ItemSummary } from './api';

vi.mock('./api', () => ({
  api: {
    items: vi.fn(),
    shows: vi.fn(() => Promise.resolve([])),
    setItemWatched: vi.fn(),
    setTitleWatched: vi.fn(),
    setShowWatched: vi.fn(),
    setSeasonWatched: vi.fn(),
  },
}));

type Fn = ReturnType<typeof vi.fn>;
// Fresh module state (fetched version, in-flight fetch) for every test.
let api: { items: Fn; shows: Fn; setItemWatched: Fn; setTitleWatched: Fn; setShowWatched: Fn; setSeasonWatched: Fn };
let app: { libraryVersion: number };
let lib: typeof import('./library.svelte');
beforeEach(async () => {
  vi.resetModules();
  api = (await import('./api')).api as unknown as typeof api;
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
const movie = (title: string): ItemSummary => ({ ...ep('', 0, 0), kind: 'movie', show: undefined, title });

function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}

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
    const b = { ...movie('B'), addedAt: 30 };
    const c = { ...movie('C'), addedAt: 20 };
    expect([a, b, c].sort(lib.byAdded).map((m) => m.title)).toEqual(['B', 'C', 'A']);
  });

  it('writes a progress report into the cached item, and only that one', async () => {
    const a = movie('A');
    const b = movie('B');
    api.items.mockResolvedValueOnce([a, b]);
    await lib.ensureLibrary();
    lib.applyProgress(a.id, { positionSec: 42, watched: false });
    expect(lib.findItem(a.id)?.progress).toEqual({ positionSec: 42, watched: false });
    expect(lib.findItem(b.id)?.progress).toBeUndefined();
    lib.applyProgress(9999, { positionSec: 1, watched: false }); // not in the library: nothing happens
  });

  it('a film with a show name is still not a show', () => {
    const odd = { ...movie('Odd'), show: 'Odd' };
    expect(lib.showsOf([odd, ep('Real', 1, 1)]).map((s) => s.show)).toEqual(['Real']);
  });
});

describe('watch state in the cached list', () => {
  const seen = { positionSec: 0, watched: true };
  /** The cached list holds these; what findItem gives back is the cached copy. */
  async function cache(...items: ItemSummary[]) {
    api.items.mockResolvedValueOnce(items);
    await lib.ensureLibrary();
  }
  const film = (title: string, titleId?: number, o: Partial<ItemSummary> = {}) => ({ ...movie(title), titleId, ...o });

  it('applyWatch writes each file of the answer, and nothing else', async () => {
    const a = ep('Lost', 1, 1);
    const b = ep('Lost', 1, 2);
    await cache(a, b);
    lib.applyWatch([{ id: a.id, watched: true, watchedAt: 1700, playCount: 1, progress: seen }, { id: 9999, watched: true, watchedAt: 1, playCount: 1, progress: seen }]);
    expect(lib.findItem(a.id)).toMatchObject({ watched: true, watchedAt: 1700, playCount: 1, progress: seen });
    expect(lib.findItem(b.id)?.watched).toBeUndefined();
  });

  it("applyWatch gives every version of a film the film's state", async () => {
    const hd = film('Dune', 7);
    const uhd = film('Dune', 7);
    const other = film('Other', 8);
    await cache(hd, uhd, other);
    lib.applyWatch([{ id: hd.id, watched: true, watchedAt: 1800, playCount: 2, progress: seen }]);
    expect(lib.findItem(uhd.id)).toMatchObject({ watched: true, watchedAt: 1800, playCount: 2 });
    expect(lib.findItem(other.id)?.watched).toBeUndefined();
  });

  it('applyProgress makes an item watched when the answer says so, its film in all versions', async () => {
    const hd = film('Dune', 7);
    const uhd = film('Dune', 7);
    await cache(hd, uhd);
    lib.applyProgress(hd.id, { positionSec: 6000, watched: false });
    expect(lib.findItem(hd.id)?.watched).toBeFalsy(); // positive control: a report short of the end is not watched
    lib.applyProgress(hd.id, { positionSec: 6900, watched: true });
    expect(lib.findItem(hd.id)).toMatchObject({ watched: true, playCount: 1 });
    expect(lib.findItem(hd.id)?.watchedAt).toBeGreaterThan(0);
    expect(lib.findItem(uhd.id)?.watched).toBe(true);
  });

  it('applyProgress leaves a watched item watched when it is played again', async () => {
    const a = film('Dune', undefined, { watched: true, watchedAt: 100, playCount: 3, progress: seen });
    await cache(a);
    lib.applyProgress(a.id, { positionSec: 120, watched: false });
    expect(lib.findItem(a.id)).toMatchObject({ watched: true, watchedAt: 100, playCount: 3, progress: { positionSec: 120, watched: false } });
  });

  it('markWatched sends a film with a title to the title, others to the item, a season and a show to theirs', async () => {
    const titled = film('Dune', 7);
    const loose = film('Home video');
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
    const a = film('Dune', 7);
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
    const card = { kind: 'series' as const, show: 'Lost', episodeCount: 4, watchedEpisodes: 1, inProgress: 0 };
    expect(lib.titleBadge(card)?.text).toBe('3 left'); // not one show's "1 left"
    api.setItemWatched.mockResolvedValueOnce({ watched: true, items: [{ id: a2.id, watched: true, watchedAt: 1, playCount: 1, progress: seen }] });
    await lib.markWatched({ item: a2 }, true);
    expect(lib.titleBadge(card)?.text).toBe('2 left'); // the mark in Lost, over both shows
    expect(lib.titleBadge({ kind: 'series', show: 'Fringe', episodeCount: 5, watchedEpisodes: 5, inProgress: 0 })?.text).toBe('✓');
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
});

describe('byTitle', () => {
  it('sorts numbers by value and ignores case', () => {
    const sorted = [movie('Movie 10'), movie('Movie 2'), movie('movie 1')].sort(lib.byTitle);
    expect(sorted.map((m) => m.title)).toEqual(['movie 1', 'Movie 2', 'Movie 10']);
  });
});

describe('ensureLibrary', () => {
  it('shares one fetch per library version and caches the result', async () => {
    const a = movie('A');
    api.items.mockResolvedValue([a]);
    await Promise.all([lib.ensureLibrary(), lib.ensureLibrary()]);
    expect(api.items).toHaveBeenCalledTimes(1);
    expect(lib.library.items).toEqual([a]);
    expect(lib.library.loaded).toBe(true);
    await lib.ensureLibrary();
    expect(api.items).toHaveBeenCalledTimes(1);
  });

  it('fetches again after a scan bumps the library version', async () => {
    api.items.mockResolvedValue([]);
    await lib.ensureLibrary();
    app.libraryVersion++;
    await lib.ensureLibrary();
    expect(api.items).toHaveBeenCalledTimes(2);
  });

  it('does not reuse a fetch from before a scan, and a late old response cannot overwrite the new one', async () => {
    const before = deferred<ItemSummary[]>();
    const after = deferred<ItemSummary[]>();
    api.items.mockReturnValueOnce(before.promise).mockReturnValueOnce(after.promise);
    const oldItem = movie('Old');
    const newItem = movie('New');

    const p1 = lib.ensureLibrary();
    app.libraryVersion++; // a scan finished while the first fetch was in flight
    const p2 = lib.ensureLibrary();
    expect(api.items).toHaveBeenCalledTimes(2);

    after.resolve([newItem]);
    await p2;
    expect(lib.library.items).toEqual([newItem]);
    before.resolve([oldItem]);
    await p1;
    expect(lib.library.items).toEqual([newItem]);
  });

  it('reports a failure and keeps what it had', async () => {
    const a = movie('A');
    api.items.mockResolvedValueOnce([a]).mockRejectedValueOnce(new Error('Cannot reach the ply server'));
    await lib.ensureLibrary();
    app.libraryVersion++;
    await lib.ensureLibrary();
    expect(lib.library.error).toBe('Cannot reach the ply server');
    expect(lib.library.items).toEqual([a]);
  });

  it('loads the show list with the items, and a failing show list costs only the metadata', async () => {
    const a = ep('A', 1, 1);
    const meta = { show: 'A', seasons: [1], episodeCount: 1, poster: '/p.jpg', matchState: 'matched', needsReview: false };
    api.items.mockResolvedValue([a]);
    api.shows.mockResolvedValueOnce([meta]).mockRejectedValueOnce(new Error('HTTP 500'));
    await lib.ensureLibrary();
    expect(lib.library.shows).toEqual([meta]);
    expect(lib.showMeta('A')).toEqual(meta);
    app.libraryVersion++;
    await lib.ensureLibrary();
    expect(lib.library.error).toBe('');
    expect(lib.library.loaded).toBe(true);
    expect(lib.library.items).toEqual([a]);
    expect(lib.library.shows).toEqual([]);
  });
});
