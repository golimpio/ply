import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { MovieCard } from './api';

// The token lives in module state, read from localStorage once: import a fresh copy for each test.
let m: typeof import('./api');
beforeEach(async () => {
  vi.resetModules();
  localStorage.clear();
  m = await import('./api');
});
afterEach(() => {
  vi.unstubAllGlobals();
});

type FetchCall = [string, RequestInit];

/** Replaces fetch with a stub answering `res` (or rejecting with it when it is an Error). */
function stubFetch(res: Response | Error) {
  const fn = vi.fn(async (_path: string, _init: RequestInit) => {
    if (res instanceof Error) throw res;
    return res.clone();
  });
  vi.stubGlobal('fetch', fn);
  return fn;
}

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

const headersOf = (fn: ReturnType<typeof stubFetch>): Record<string, string> =>
  (fn.mock.calls[0] as unknown as FetchCall)[1].headers as Record<string, string>;

describe('mediaUrl', () => {
  it('resolves a server-relative URL against the page origin', () => {
    expect(m.mediaUrl('/api/items/5/file')).toBe(`${location.origin}/api/items/5/file`);
    expect(m.mediaUrl('/api/sessions/s1/index.m3u8')).toBe(`${location.origin}/api/sessions/s1/index.m3u8`);
  });

  it('adds nothing without a token', () => {
    expect(m.mediaUrl('/api/items/5/file')).not.toContain('token');
    expect(m.mediaUrl('/a.m3u8?v=1')).toBe(`${location.origin}/a.m3u8?v=1`);
  });

  it('puts the token after ? when there is no query and after & when there is one', () => {
    m.setToken('abc');
    expect(m.mediaUrl('/api/items/5/file')).toBe(`${location.origin}/api/items/5/file?token=abc`);
    expect(m.mediaUrl('/api/sessions/s/index.m3u8?v=1')).toBe(`${location.origin}/api/sessions/s/index.m3u8?v=1&token=abc`);
  });

  it('keeps a token the URL already carries (the server adds it to what it returns)', () => {
    m.setToken('abc');
    expect(m.mediaUrl('/api/items/5/file?token=abc')).toBe(`${location.origin}/api/items/5/file?token=abc`);
    expect(m.mediaUrl('/x?token=other')).toBe(`${location.origin}/x?token=other`);
  });

  it('escapes the token', () => {
    m.setToken('a b&c=d+e');
    const u = new URL(m.mediaUrl('/f'));
    expect(u.searchParams.get('token')).toBe('a b&c=d+e');
    expect([...u.searchParams.keys()]).toEqual(['token']);
  });

  it('follows the token as it changes', () => {
    m.setToken('one');
    expect(m.mediaUrl('/f')).toContain('token=one');
    m.clearToken();
    expect(m.mediaUrl('/f')).not.toContain('token');
  });
});

describe('token storage', () => {
  it('is read from localStorage when the module loads', async () => {
    localStorage.setItem('ply.token', 'saved');
    vi.resetModules();
    m = await import('./api');
    expect(m.getToken()).toBe('saved');
  });

  it('setToken stores it, clearToken removes it', () => {
    m.setToken('t');
    expect(localStorage.getItem('ply.token')).toBe('t');
    expect(m.getToken()).toBe('t');
    m.clearToken();
    expect(localStorage.getItem('ply.token')).toBeNull();
    expect(m.getToken()).toBe('');
  });

  it('keeps working in memory when storage is unavailable', () => {
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('quota');
    });
    m.setToken('mem');
    expect(m.getToken()).toBe('mem');
  });
});

describe('request headers', () => {
  it('sends no Authorization header without a token', async () => {
    const fetchFn = stubFetch(json({ id: 'x' }));
    await m.api.info();
    expect(headersOf(fetchFn)).not.toHaveProperty('Authorization');
  });

  it('sends a Bearer header with one, and only then', async () => {
    m.setToken('s3cret');
    const fetchFn = stubFetch(json({ id: 'x' }));
    await m.api.info();
    expect(headersOf(fetchFn).Authorization).toBe('Bearer s3cret');

    m.clearToken();
    const again = stubFetch(json({ id: 'x' }));
    await m.api.info();
    expect(headersOf(again)).not.toHaveProperty('Authorization');
  });

  it('declares JSON only for requests with a body', async () => {
    const get = stubFetch(json([]));
    await m.api.items();
    expect(headersOf(get)).not.toHaveProperty('Content-Type');

    const post = stubFetch(json({ method: 'direct' }));
    await m.api.playback(7, { profile: 'browser', startSec: 12 });
    const [path, init] = post.mock.calls[0] as unknown as FetchCall;
    expect(path).toBe('/api/items/7/playback');
    expect(init.method).toBe('POST');
    expect((init.headers as Record<string, string>)['Content-Type']).toBe('application/json');
    expect(JSON.parse(init.body as string)).toEqual({ profile: 'browser', startSec: 12 });
  });

  it('builds query strings from the parameters that are set', async () => {
    const fetchFn = stubFetch(json([]));
    await m.api.items();
    await m.api.items({ kind: 'episode', show: 'Breaking Bad & Co' });
    await m.api.items({ show: '' });
    expect(fetchFn.mock.calls.map((c) => c[0])).toEqual([
      '/api/items',
      '/api/items?kind=episode&show=Breaking+Bad+%26+Co',
      '/api/items',
    ]);
  });
});

describe('errors', () => {
  it('takes the message from the JSON error body', async () => {
    stubFetch(json({ error: 'no audio stream with index 9' }, 400));
    const err = await m.api.playback(1, { audioIndex: 9 }).catch((e) => e);
    expect(err).toBeInstanceOf(m.ApiError);
    expect(err).toMatchObject({ name: 'ApiError', status: 400, message: 'no audio stream with index 9' });
  });

  it('falls back to the status text when the body is not the error JSON', async () => {
    stubFetch(new Response('<html>oops</html>', { status: 502, statusText: 'Bad Gateway' }));
    expect(await m.api.info().catch((e) => e)).toMatchObject({ status: 502, message: 'Bad Gateway' });

    stubFetch(json({ detail: 'no error field' }, 500));
    // Response.statusText is empty for a constructed 500: the message names the status instead.
    expect(await m.api.info().catch((e) => e)).toMatchObject({ status: 500, message: 'HTTP 500' });

    stubFetch(json({ error: '' }, 500));
    expect(await m.api.info().catch((e) => e)).toMatchObject({ message: 'HTTP 500' });

    stubFetch(json(null, 500));
    expect(await m.api.info().catch((e) => e)).toMatchObject({ message: 'HTTP 500' });
  });

  it('reports a server that cannot be reached as status 0', async () => {
    stubFetch(new TypeError('Failed to fetch'));
    expect(await m.api.info().catch((e) => e)).toMatchObject({ status: 0, message: 'Cannot reach the ply server' });
  });

  it('a 401 forgets the token and tells the app, then still throws', async () => {
    m.setToken('stale');
    const seen = vi.fn();
    m.onUnauthorized(seen);
    stubFetch(json({ error: 'unauthorized' }, 401));
    const err = await m.api.items().catch((e) => e);
    expect(err).toMatchObject({ status: 401, message: 'unauthorized' });
    expect(seen).toHaveBeenCalledTimes(1);
    expect(m.getToken()).toBe('');
    expect(localStorage.getItem('ply.token')).toBeNull();
  });

  it('other failures leave the token alone', async () => {
    m.setToken('keep');
    stubFetch(json({ error: 'boom' }, 500));
    await m.api.items().catch(() => {});
    expect(m.getToken()).toBe('keep');
  });

  it('a scan already running is a state, not an error', async () => {
    stubFetch(json({ error: 'scan already running' }, 409));
    expect(await m.api.startScan()).toBe('running');
    stubFetch(json({ started: true }, 202));
    expect(await m.api.startScan()).toBe('started');
    stubFetch(json({ error: 'boom' }, 500));
    await expect(m.api.startScan()).rejects.toMatchObject({ status: 500 });
  });
});

describe('response shapes', () => {
  it('turns the nulls Go sends for empty lists into arrays', async () => {
    stubFetch(json(null));
    expect(await m.api.items()).toEqual([]);
    expect(await m.api.continueWatching()).toEqual([]);
    stubFetch(json([{ show: 'A', seasons: null, episodeCount: 0 }]));
    expect(await m.api.shows()).toEqual([{ show: 'A', seasons: [], episodeCount: 0 }]);
  });

  it('answers undefined for 204', async () => {
    // Through a call that does not swallow errors: an empty body must not reach res.json().
    stubFetch(new Response(null, { status: 204 }));
    await expect(m.api.putProgress(1, 5, 60)).resolves.toBeUndefined();
  });

  it('answers null where the server does', async () => {
    stubFetch(json(null));
    await expect(m.api.putProgress(1, 5, 60)).resolves.toBeNull();
  });

  it('escapes the session id in the URL', async () => {
    const fetchFn = stubFetch(new Response(null, { status: 204 }));
    await m.api.deleteSession('a/b c');
    expect((fetchFn.mock.calls[0] as unknown as FetchCall)[0]).toBe('/api/sessions/a%2Fb%20c');
    expect((fetchFn.mock.calls[0] as unknown as FetchCall)[1].method).toBe('DELETE');
  });
});

describe('movieCards', () => {
  // A card as the server sends one with no artwork, no rating and not started (internal/api/card.go).
  const bare = {
    id: 1, kind: 'movie', title: 'Heat', sortTitle: 'Heat', addedAt: 5, art: null, rating: null, watched: false, progress: null,
    durationSec: 60, container: 'mkv', width: 1920, height: 1080, matchState: 'matched', needsReview: false,
  } satisfies MovieCard;
  const card = (over: Record<string, unknown> = {}) => ({ ...bare, ...over });
  const imdb = { source: 'imdb', value: 8.3, scale: 10, score100: 83 };

  /** Answers `rows` as the parsed body itself, so the test can see which objects come back unchanged. */
  function answer(rows: unknown) {
    const fn = vi.fn(async (_path: string, _init: RequestInit) => ({ ok: true, status: 200, json: async () => rows }) as unknown as Response);
    vi.stubGlobal('fetch', fn);
    return fn;
  }

  it('always asks for the card list of the movies, with a GET', async () => {
    const fetchFn = stubFetch(json([]));
    await m.api.movieCards();
    const [path, init] = fetchFn.mock.calls[0] as unknown as FetchCall;
    expect(path).toBe('/api/items?kind=movie&fields=card');
    expect(init.method).toBe('GET');
  });

  it('turns the null Go sends for no movies into []', async () => {
    stubFetch(json(null));
    expect(await m.api.movieCards()).toEqual([]);
  });

  it('hands back the rows that need nothing as the very objects it was given', async () => {
    const none = card({ id: 1 });
    const rt = { source: 'rt_critics', value: 94, scale: 100, score100: 94, state: 'certified' };
    const known = card({ id: 2, art: { thumb: '/api/items/2/thumb?v=7', color: '#112233' }, rating: rt, backdrop: '/b.jpg' });
    answer([none, known]);
    const [a, b] = await m.api.movieCards();
    expect(a).toBe(none);
    expect(b).toBe(known);
    expect(b.rating).toEqual(rt);
  });

  it('makes a rating the page cannot show null, as for the full rows, and keeps the rest of the row', async () => {
    const odd = card({ id: 3, rating: { ...imdb, source: 'myanimelist' } });
    const junk = card({ id: 4, rating: 'imdb' });
    answer([odd, junk]);
    const [a, b] = await m.api.movieCards();
    expect(a).toEqual({ ...odd, rating: null });
    expect(b).toEqual({ ...junk, rating: null });
    expect(odd.rating).toMatchObject({ source: 'myanimelist' }); // the server's object is not changed
  });

  it("throws the server's error (fields=card without a kind is a 400)", async () => {
    stubFetch(json({ error: 'fields=card needs kind=movie or kind=episode' }, 400)); // internal/api/handlers.go
    await expect(m.api.episodeCards()).rejects.toMatchObject({ name: 'ApiError', status: 400, message: 'fields=card needs kind=movie or kind=episode' });
  });

  it('asks for the episode cards, not the full episode rows', async () => {
    const fetchFn = stubFetch(json([]));
    await m.api.episodeCards();
    expect(fetchFn.mock.calls[0][0]).toContain('/api/items?kind=episode&fields=card');
  });
});

describe('watched', () => {
  it('puts {watched} to the item, the title, the show and the season (the name escaped, 0 kept)', async () => {
    const item = { id: 3, watched: true, watchedAt: 9, playCount: 1, progress: { positionSec: 0, watched: true } };
    const fetchFn = stubFetch(json({ watched: true, items: [item] }));
    expect(await m.api.setItemWatched(3, true)).toEqual({ watched: true, items: [item] });
    await m.api.setTitleWatched(7, false);
    await m.api.setShowWatched('Law & Order', true);
    await m.api.setSeasonWatched('Law & Order', 0, true);
    const calls = fetchFn.mock.calls as unknown as FetchCall[];
    expect(calls.map(([path, init]) => `${init.method} ${path} ${init.body}`)).toEqual([
      'PUT /api/items/3/watched {"watched":true}',
      'PUT /api/titles/7/watched {"watched":false}',
      'PUT /api/shows/Law%20%26%20Order/watched {"watched":true}',
      'PUT /api/shows/Law%20%26%20Order/seasons/0/watched {"watched":true}',
    ]);
  });

  it('reads a null list of items as none', async () => {
    stubFetch(json({ watched: false, items: null }));
    expect(await m.api.setShowWatched('Lost', false)).toEqual({ watched: false, items: [] });
  });
});

describe('putProgress', () => {
  it('sends the position and the duration to the item', async () => {
    const fetchFn = stubFetch(json({ positionSec: 42.5, watched: false }));
    const res = await m.api.putProgress(7, 42.5, 3600);
    const [path, init] = fetchFn.mock.calls[0] as unknown as FetchCall;
    expect(path).toBe('/api/items/7/progress');
    expect(init.method).toBe('PUT');
    expect(JSON.parse(init.body as string)).toEqual({ positionSec: 42.5, durationSec: 3600 });
    expect((init.headers as Record<string, string>)['Content-Type']).toBe('application/json');
    expect(res).toEqual({ positionSec: 42.5, watched: false });
  });

  it('asks for keepalive only when told to (a report made while the page unloads)', async () => {
    const fetchFn = stubFetch(json(null));
    await m.api.putProgress(7, 1, 2);
    await m.api.putProgress(7, 1, 2, false);
    await m.api.putProgress(7, 1, 2, true);
    const keepalive = fetchFn.mock.calls.map((c) => (c[1] as RequestInit).keepalive);
    expect(keepalive).toEqual([false, false, true]);
  });

  it('sends the bearer token like any other call', async () => {
    m.setToken('s3cret');
    const fetchFn = stubFetch(json(null));
    await m.api.putProgress(7, 1, 2);
    expect(headersOf(fetchFn).Authorization).toBe('Bearer s3cret');
  });
});

describe('deleteSession', () => {
  it('outlives the page: keepalive', async () => {
    const fetchFn = stubFetch(new Response(null, { status: 204 }));
    await m.api.deleteSession('s1');
    expect((fetchFn.mock.calls[0] as unknown as FetchCall)[1].keepalive).toBe(true);
  });

  it('is best effort: a failure is not the caller\'s problem', async () => {
    stubFetch(new TypeError('Failed to fetch'));
    await expect(m.api.deleteSession('s1')).resolves.toBeUndefined();
    stubFetch(json({ error: 'no such session' }, 404));
    await expect(m.api.deleteSession('s1')).resolves.toBeUndefined();
  });
});

describe('item', () => {
  const bare = { id: 5, title: 'T', playback: { method: 'remux', reasons: ['container'], url: '', startSec: 0 } };

  it('asks for the item, with the playback target when given', async () => {
    const fetchFn = stubFetch(json(bare));
    await m.api.item(5);
    await m.api.item(5, 'browser');
    expect(fetchFn.mock.calls.map((c) => c[0])).toEqual(['/api/items/5', '/api/items/5?target=browser']);
  });

  it('turns the nulls Go sends for empty lists into arrays', async () => {
    stubFetch(json({ ...bare, streams: null, subtitles: null, playback: { ...bare.playback, reasons: null } }));
    const d = await m.api.item(5);
    expect(d.streams).toEqual([]);
    expect(d.subtitles).toEqual([]);
    expect(d.playback.reasons).toEqual([]);
  });

  it('keeps what the server sent', async () => {
    const streams = [{ index: 1, type: 'audio', codec: 'aac' }];
    const subtitles = [{ key: '2', format: 'subrip', image: false }];
    stubFetch(json({ ...bare, streams, subtitles }));
    expect(await m.api.item(5)).toEqual({ ...bare, streams, subtitles });
  });

  it('copes with an answer that has no playback decision', async () => {
    stubFetch(json({ id: 5, streams: null, subtitles: null }));
    const d = await m.api.item(5);
    expect(d.playback).toBeUndefined();
    expect(d.streams).toEqual([]);
  });

  it('releases a session the server opened just to describe the decision', async () => {
    const fetchFn = stubFetch(json({ ...bare, playback: { ...bare.playback, sessionId: 'oops/1' } }));
    await m.api.item(5);
    const calls = fetchFn.mock.calls as unknown as FetchCall[];
    expect(calls.map((c) => [c[1].method ?? 'GET', c[0]])).toEqual([
      ['GET', '/api/items/5'],
      ['DELETE', '/api/sessions/oops%2F1'],
    ]);
    expect(calls[1][1].keepalive).toBe(true);
  });

  it('asks for nothing more when there is no session', async () => {
    const fetchFn = stubFetch(json(bare));
    await m.api.item(5);
    expect(fetchFn).toHaveBeenCalledTimes(1);
  });
});

describe('playback', () => {
  const decision = { method: 'direct', videoAction: 'copy', audioAction: 'copy', audioIndex: 1 };

  it('fills in what Go leaves out: no reasons, no url, no start', async () => {
    stubFetch(json({ ...decision, reasons: null, url: null }));
    expect(await m.api.playback(1, {})).toEqual({ ...decision, reasons: [], url: '', startSec: 0 });
    stubFetch(json(decision));
    expect(await m.api.playback(1, {})).toEqual({ ...decision, reasons: [], url: '', startSec: 0 });
  });

  it('keeps what the server did send', async () => {
    const full = { ...decision, reasons: ['why'], url: '/api/sessions/s1/index.m3u8', sessionId: 's1', startSec: 12.5 };
    stubFetch(json(full));
    expect(await m.api.playback(1, { startSec: 12.5 })).toEqual(full);
  });
});

describe('library folders', () => {
  const call = (fn: ReturnType<typeof stubFetch>, i = 0) => fn.mock.calls[i] as unknown as FetchCall;

  it('lists the folders, an empty list for the null Go sends', async () => {
    stubFetch(json(null));
    expect(await m.api.folders()).toEqual([]);
    const f = { id: 1, path: '/m', kind: 'movies', guessed: false, source: 'ui', kindLocked: false, items: 3, lastScanAt: 0, state: 'ok', error: '' };
    const fn = stubFetch(json([f]));
    expect(await m.api.folders()).toEqual([f]);
    expect(call(fn)[0]).toBe('/api/folders');
  });

  it('adds a folder with its path and type', async () => {
    const fn = stubFetch(json({ id: 2, path: '/Volumes/NAS/TV' }, 201));
    await m.api.addFolder('/Volumes/NAS/TV', 'shows');
    expect(call(fn)[0]).toBe('/api/folders');
    expect(call(fn)[1].method).toBe('POST');
    expect(JSON.parse(call(fn)[1].body as string)).toEqual({ path: '/Volumes/NAS/TV', kind: 'shows' });
  });

  it("passes on the server's reason for refusing a folder", async () => {
    stubFetch(json({ error: '/a/b overlaps the library folder /a' }, 400));
    await expect(m.api.addFolder('/a/b', 'movies')).rejects.toMatchObject({ status: 400, message: '/a/b overlaps the library folder /a' });
  });

  it('changes a type with a PATCH of the folder', async () => {
    const fn = stubFetch(json({ id: 4, kind: 'movies' }));
    await m.api.setFolderKind(4, 'movies');
    expect(call(fn)[0]).toBe('/api/folders/4');
    expect(call(fn)[1].method).toBe('PATCH');
    expect(JSON.parse(call(fn)[1].body as string)).toEqual({ kind: 'movies' });
  });

  it('asks to forget only when told to', async () => {
    const fn = stubFetch(new Response(null, { status: 204 }));
    await m.api.removeFolder(5);
    await m.api.removeFolder(5, true);
    expect(call(fn, 0)[0]).toBe('/api/folders/5');
    expect(call(fn, 1)[0]).toBe('/api/folders/5?forget=1');
    expect(call(fn, 1)[1].method).toBe('DELETE');
  });

  it('a removal refused while a scan is busy is an error that says when to retry', async () => {
    const busy = new Response(JSON.stringify({ error: 'a scan of the library is still running' }), {
      status: 503,
      headers: { 'Content-Type': 'application/json', 'Retry-After': '30' },
    });
    stubFetch(busy);
    await expect(m.api.removeFolder(5)).rejects.toMatchObject({ status: 503, retryAfter: 30 });
  });

  it('reads Retry-After only when it is a number of seconds', async () => {
    const res = (h?: string) =>
      new Response('{}', { status: 503, headers: h === undefined ? {} : { 'Retry-After': h } });
    for (const [h, want] of [[undefined, undefined], ['', undefined], ['Wed, 21 Oct 2026 07:28:00 GMT', undefined], [' 5 ', 5], ['0', 0]] as const) {
      stubFetch(res(h));
      const err = await m.api.removeFolder(1).catch((e: unknown) => e);
      expect((err as InstanceType<typeof m.ApiError>).retryAfter, String(h)).toBe(want);
    }
  });

  it('escapes every character a folder name can hold', async () => {
    const path = '/Volumes/A&B #1 100%+x?=/é';
    const fn = stubFetch(json({ path, parent: '/Volumes', dirs: [] }));
    await m.api.listDirs(path);
    const url = new URL(call(fn)[0], location.origin);
    expect(url.pathname).toBe('/api/fs');
    expect([...url.searchParams.keys()]).toEqual(['path']);
    expect(url.searchParams.get('path')).toBe(path);
  });

  it('lists directories: the places with no path, an escaped path otherwise', async () => {
    const fn = stubFetch(json({ path: '', parent: '', dirs: null, places: [{ name: 'Home', path: '/Users/me' }] }));
    const top = await m.api.listDirs();
    expect(call(fn)[0]).toBe('/api/fs');
    expect(top.dirs).toEqual([]);
    expect(top.places).toEqual([{ name: 'Home', path: '/Users/me' }]);

    const fn2 = stubFetch(json({ path: '/Volumes/My Disk', parent: '/Volumes', dirs: [{ name: 'A&B', path: '/Volumes/My Disk/A&B' }] }));
    const d = await m.api.listDirs('/Volumes/My Disk');
    expect(new URL(call(fn2)[0], location.origin).searchParams.get('path')).toBe('/Volumes/My Disk');
    expect(d.parent).toBe('/Volumes');
    expect(d.places).toBeUndefined();
  });
});
