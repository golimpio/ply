import { afterEach, describe, expect, it } from 'vitest';
import { setToken, type ItemSummary } from './api';
import { artOf, artSrc, progressOf, type WithArt } from './ui-types';

const base: ItemSummary = {
  id: 1,
  kind: 'movie',
  title: 'Film',
  durationSec: 120,
  container: 'mp4',
  videoCodec: 'h264',
  audioCodec: 'aac',
  width: 0,
  height: 0,
  addedAt: 0,
};

afterEach(() => setToken(''));

describe('progressOf', () => {
  it.each([
    ['no progress', {}, 0],
    ['part way', { progress: { positionSec: 30, watched: false } }, 0.25],
    ['watched', { progress: { positionSec: 110, watched: true } }, 0],
    ['past the end', { progress: { positionSec: 500, watched: false } }, 1],
    ['unknown duration', { durationSec: 0, progress: { positionSec: 30, watched: false } }, 0],
  ])('%s', (_name, extra, want) => {
    expect(progressOf({ ...base, ...extra })).toBe(want);
  });
});

describe('artOf / artSrc', () => {
  const withArt: WithArt = { ...base, art: { thumb: '/api/items/1/art/thumb?v=5', backdrop: '/api/items/1/art/backdrop?v=5', color: '#123456' } };

  it('is absent until the server sends artwork', () => {
    expect(artOf(base)).toBeNull();
    expect(artOf({ ...base, art: null } as WithArt)).toBeNull();
    expect(artOf(undefined)).toBeNull();
    expect(artSrc(base, 'thumb')).toBe('');
  });

  it('builds an absolute URL that keeps the version and adds the token once', () => {
    setToken('s3cret');
    const u = new URL(artSrc(withArt, 'thumb'));
    expect(u.pathname).toBe('/api/items/1/art/thumb');
    expect(u.searchParams.get('v')).toBe('5');
    expect(u.searchParams.getAll('token')).toEqual(['s3cret']);

    const already = { ...withArt, art: { ...withArt.art!, thumb: '/api/items/1/art/thumb?v=5&token=s3cret' } };
    expect(new URL(artSrc(already, 'thumb')).searchParams.getAll('token')).toEqual(['s3cret']);
  });
});
