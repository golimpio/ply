import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { ItemSummary } from './api';

vi.mock('./api', () => ({ api: { items: vi.fn(), shows: vi.fn(() => Promise.resolve([])) } }));

// Fresh module state (fetched version, in-flight fetch) for every test.
let api: { items: ReturnType<typeof vi.fn>; shows: ReturnType<typeof vi.fn> };
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
