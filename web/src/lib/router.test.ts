import { describe, expect, it } from 'vitest';
import { href, parseRoute, type LibraryView } from './router.svelte';

const notfound = { name: 'notfound' };

describe('parseRoute', () => {
  it.each([
    ['', { name: 'home' }],
    ['#/', { name: 'home' }],
    ['#/movies', { name: 'movies', sort: 'title', filters: [] }],
    ['#/movies?sort=added&f=4k,hdr,bogus', { name: 'movies', sort: 'added', filters: ['4k', 'hdr'] }],
    ['#/movies?sort=nonsense', { name: 'movies', sort: 'title', filters: [] }],
    ['#/shows?sort=added', { name: 'shows', sort: 'added', filters: [] }],
    ['#/item/12', { name: 'item', id: 12 }],
    ['#/item/0', notfound],
    ['#/item/1.5', notfound],
    ['#/item/abc', notfound],
    ['#/play/3?t=125', { name: 'play', id: 3, startSec: 125 }],
    ['#/play/3?t=-5', { name: 'play', id: 3, startSec: 0 }],
    ['#/play/3?t=soon', { name: 'play', id: 3, startSec: 0 }],
    ['#/nowhere', notfound],
  ])('%s', (hash, want) => {
    expect(parseRoute(hash)).toEqual(want);
  });

  it.each([
    ['#/shows/Lost', undefined],
    ['#/shows/Lost?s=', undefined], // regression: Number('') is 0, which would mean Specials
    ['#/shows/Lost?s=%20', undefined],
    ['#/shows/Lost?s=0', 0],
    ['#/shows/Lost?s=2', 2],
    ['#/shows/Lost?s=1.5', undefined],
    ['#/shows/Lost?s=two', undefined],
  ])('season of %s', (hash, season) => {
    expect(parseRoute(hash)).toEqual({ name: 'show', show: 'Lost', season });
  });

  it('treats malformed percent-encoding as not found instead of throwing', () => {
    expect(parseRoute('#/shows/%E0%A4%A')).toEqual(notfound);
  });
});

describe('href', () => {
  it.each(["Who's [Next]? & 100% #1", 'a/b', 'C++ + more', 'Amélie', '?', '%41'])(
    'round-trips the show name %s',
    (name) => {
      expect(parseRoute(href.show(name))).toEqual({ name: 'show', show: name, season: undefined });
      expect(parseRoute(href.show(name, 0))).toEqual({ name: 'show', show: name, season: 0 });
    },
  );

  it('round-trips library views and leaves the default sort out of the URL', () => {
    const view: LibraryView = { sort: 'year', filters: ['hdr', 'unwatched'] };
    expect(parseRoute(href.moviesWith(view))).toEqual({ name: 'movies', ...view });
    expect(href.moviesWith({ sort: 'title', filters: [] })).toBe('#/movies');
    expect(href.showsWith({ sort: 'added' })).toBe('#/shows?sort=added');
  });

  it('floors the resume time and omits a zero one', () => {
    expect(href.play(5)).toBe('#/play/5');
    expect(href.play(5, 0.4)).toBe('#/play/5');
    expect(href.play(5, 61.9)).toBe('#/play/5?t=61');
  });
});
