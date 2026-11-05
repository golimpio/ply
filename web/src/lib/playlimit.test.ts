import { afterEach, describe, expect, it, vi } from 'vitest';
import { api, ApiError } from './api';
import { limitMessage, limitOf, parseLimit, probeMedia, type PlayLimit } from './playlimit';

afterEach(() => {
  vi.unstubAllGlobals();
});

const PLAY = {
  id: 'abc', sessionId: 'abc', itemId: 7, title: 'Alien', kind: 'transcode', client: '192.168.1.20 · Safari',
  startedAt: '2026-11-05T01:00:00Z', lastSeenAt: '2026-11-05T01:10:00Z', positionSec: 754.4, paused: true,
};
const BODY = { error: '2 of 2 plays in use', limit: 'streams', max: 2, inUse: 2, playing: [PLAY, { ...PLAY, id: 'def', sessionId: 'def', paused: false }] };

const json = (body: unknown, status: number, headers: Record<string, string> = {}) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } });

describe('parseLimit', () => {
  it('reads the body of the 503 at a limit', () => {
    const l = parseLimit(BODY, 5);
    expect(l).toEqual({ limit: 'streams', max: 2, inUse: 2, playing: [BODY.playing[0], BODY.playing[1]], retryAfter: 5 });
  });

  it('is null for a 503 that is not about a limit', () => {
    expect(parseLimit({ error: 'streaming is disabled' })).toBeNull();
    expect(parseLimit({ ...BODY, limit: 'cpu' })).toBeNull();
    expect(parseLimit(null)).toBeNull();
    expect(parseLimit('busy')).toBeNull();
  });

  it('fills in what Go or a cross-site answer leaves out', () => {
    const l = parseLimit({ limit: 'transcodes', max: 1, inUse: 1, playing: null })!;
    expect(l.playing).toEqual([]);
    expect(l.retryAfter).toBe(5); // no Retry-After header
    const odd = parseLimit({ limit: 'streams', max: -1, playing: [{ id: '' }, 'x', { id: 'a', positionSec: -4 }] })!;
    expect(odd.max).toBe(0);
    expect(odd.inUse).toBe(0);
    expect(odd.playing).toEqual([{ id: 'a', title: 'Untitled', client: '', positionSec: 0, paused: false }]);
  });
});

describe('limitOf', () => {
  it('finds the limit in what api.playback throws for a 503 at the cap', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => json(BODY, 503, { 'Retry-After': '7' })));
    const err = await api.playback(1, {}).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).message).toBe('2 of 2 plays in use');
    const l = limitOf(err)!;
    expect(l.limit).toBe('streams');
    expect(l.retryAfter).toBe(7);
    expect(l.playing.map((p) => p.id)).toEqual(['abc', 'def']);
  });

  it('is null for other errors', () => {
    expect(limitOf(new ApiError(500, 'x', undefined, BODY))).toBeNull();
    expect(limitOf(new ApiError(503, 'busy', 5, { error: 'busy' }))).toBeNull();
    expect(limitOf(new Error('x'))).toBeNull();
  });
});

describe('probeMedia', () => {
  it('reads a play refused at the limit, with its Retry-After', async () => {
    const fetch = vi.fn(async () => json({ ...BODY, limit: 'streams' }, 503, { 'Retry-After': '7' }));
    vi.stubGlobal('fetch', fetch);
    const r = await probeMedia('http://x/api/items/1/file?token=t');
    expect(r?.kind).toBe('limit');
    const l = r?.kind === 'limit' ? r.limit : null;
    expect(l?.inUse).toBe(2);
    expect(l?.retryAfter).toBe(7);
    expect((fetch.mock.calls[0] as unknown as [string, RequestInit])[1].headers).toEqual({ Range: 'bytes=0-0' });
  });

  it('reads a play stopped on purpose (410)', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => json({ error: 'stopped', stoppedBy: 'another device' }, 410)));
    expect(await probeMedia('http://x/api/items/1/file')).toEqual({ kind: 'stopped' });
  });

  it('is null for a file that is served, a 503 for another reason, or a server out of reach', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('x', { status: 206 })));
    expect(await probeMedia('http://x/f')).toBeNull();
    vi.stubGlobal('fetch', vi.fn(async () => json({ error: 'busy' }, 503)));
    expect(await probeMedia('http://x/f')).toBeNull();
    vi.stubGlobal('fetch', vi.fn(async () => json({ error: 'session not found' }, 404)));
    expect(await probeMedia('http://x/f')).toBeNull();
    vi.stubGlobal('fetch', vi.fn(async () => Promise.reject(new TypeError('offline'))));
    expect(await probeMedia('http://x/f')).toBeNull();
  });
});

describe('limitMessage', () => {
  const l = (over: Partial<PlayLimit>): PlayLimit => ({ limit: 'streams', max: 2, inUse: 2, playing: [], retryAfter: 5, ...over });
  it('says what is in use, in plain words', () => {
    expect(limitMessage(l({}))).toBe('2 videos are already playing, the most this server allows.');
    expect(limitMessage(l({ max: 1, inUse: 1 }))).toBe('1 video is already playing, the most this server allows.');
    expect(limitMessage(l({ limit: 'transcodes', max: 1, inUse: 1 }))).toBe('ply is already converting 1 video, the most it allows.');
    expect(limitMessage(l({ limit: 'transcodes', max: 3, inUse: 3 }))).toBe('ply is already converting 3 videos, the most it allows.');
  });
  it('after the limit was lowered under what is playing', () => {
    expect(limitMessage(l({ max: 2, inUse: 4 }))).toBe('4 videos are already playing, more than the 2 this server allows.');
  });
});
