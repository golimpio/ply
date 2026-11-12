import { afterEach, describe, expect, it } from 'vitest';
import { setToken, type ItemSummary, type MovieCard } from './api';
import { artOf, artSrc, progressOf } from './ui-types';

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

  it('reads a movie card as it reads a row', () => {
    const c: Pick<MovieCard, 'progress' | 'durationSec'> = { durationSec: 120, progress: { positionSec: 30, watched: false } };
    expect(progressOf(c)).toBe(0.25);
    expect(progressOf({ ...c, progress: null })).toBe(0);
  });
});

describe('artOf / artSrc', () => {
  const withArt: ItemSummary = { ...base, art: { thumb: '/api/items/1/art/thumb?v=5', backdrop: '/api/items/1/art/backdrop?v=5', color: '#123456' } };

  it('is absent until the server sends artwork', () => {
    expect(artOf(base)).toBeNull();
    expect(artOf({ ...base, art: null })).toBeNull();
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

  it("a movie card's artwork: its colour and thumb, never a backdrop", () => {
    const c: Pick<MovieCard, 'art'> = { art: { color: '#123456', thumb: '/api/items/1/art/thumb?v=5' } };
    expect(artOf(c)?.color).toBe('#123456');
    expect(new URL(artSrc(c, 'thumb')).pathname).toBe('/api/items/1/art/thumb');
    expect(artSrc(c, 'backdrop')).toBe(''); // not a URL made of "undefined"
    expect(artSrc({ art: null }, 'thumb')).toBe(''); // no artwork yet
  });
});
