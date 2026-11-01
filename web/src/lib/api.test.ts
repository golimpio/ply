import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

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
    stubFetch(new Response(null, { status: 204 }));
    expect(await m.api.deleteSession('s/1')).toBeUndefined();
  });

  it('escapes the session id in the URL', async () => {
    const fetchFn = stubFetch(new Response(null, { status: 204 }));
    await m.api.deleteSession('a/b c');
    expect((fetchFn.mock.calls[0] as unknown as FetchCall)[0]).toBe('/api/sessions/a%2Fb%20c');
    expect((fetchFn.mock.calls[0] as unknown as FetchCall)[1].method).toBe('DELETE');
  });
});
