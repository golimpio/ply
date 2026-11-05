import { describe, expect, it } from 'vitest';
import { href, parseRoute, type LibraryView } from './router.svelte';

const notfound = { name: 'notfound' };

describe('parseRoute', () => {
  it.each([
    ['', { name: 'home' }],
    ['#/', { name: 'home' }],
    ['#/movies', { name: 'movies', sort: 'title', filters: [], minRating: 0 }],
    ['#/movies?sort=added&f=4k,hdr,bogus', { name: 'movies', sort: 'added', filters: ['4k', 'hdr'], minRating: 0 }],
    ['#/movies?sort=nonsense', { name: 'movies', sort: 'title', filters: [], minRating: 0 }],
    ['#/shows?sort=added', { name: 'shows', sort: 'added', filters: [], minRating: 0 }],
    ['#/movies?sort=rating&min=70', { name: 'movies', sort: 'rating', filters: [], minRating: 70 }],
    ['#/shows?sort=rating&min=80', { name: 'shows', sort: 'rating', filters: [], minRating: 80 }],
    ['#/movies?min=101', { name: 'movies', sort: 'title', filters: [], minRating: 0 }],
    ['#/movies?min=-5', { name: 'movies', sort: 'title', filters: [], minRating: 0 }],
    ['#/movies?min=7.5', { name: 'movies', sort: 'title', filters: [], minRating: 0 }],
    ['#/item/12', { name: 'item', id: 12 }],
    ['#/item/0', notfound],
    ['#/item/1.5', notfound],
    ['#/item/abc', notfound],
    ['#/play/3?t=125', { name: 'play', id: 3, startSec: 125 }],
    ['#/play/3?t=-5', { name: 'play', id: 3, startSec: 0 }],
    ['#/play/3?t=soon', { name: 'play', id: 3, startSec: 0 }],
    ['#/nowhere', notfound],
    ['#/person/31', { name: 'person', id: 31 }],
    ['#/genre/18', { name: 'genre', id: 18 }],
    ['#/collection/2344', { name: 'collection', id: 2344 }],
    ['#/person/0', notfound],
    ['#/genre/-3', notfound],
    ['#/collection/x', notfound],
    ['#/settings', { name: 'settings' }],
    ['#/settings/extra', notfound],
    ['#/activity', { name: 'activity' }],
    ['#/activity/7', notfound],
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
    const view: LibraryView = { sort: 'year', filters: ['hdr', 'unwatched'], minRating: 0 };
    expect(parseRoute(href.moviesWith(view))).toEqual({ name: 'movies', ...view });
    const rated: LibraryView = { sort: 'rating', filters: ['4k'], minRating: 60 };
    expect(parseRoute(href.showsWith(rated))).toEqual({ name: 'shows', ...rated });
    expect(href.moviesWith({ sort: 'title', filters: [], minRating: 0 })).toBe('#/movies');
    expect(href.moviesWith({ sort: 'rating', minRating: 70 })).toBe('#/movies?sort=rating&min=70');
    expect(href.showsWith({ sort: 'added' })).toBe('#/shows?sort=added');
  });

  it('floors the resume time and omits a zero one', () => {
    expect(href.play(5)).toBe('#/play/5');
    expect(href.play(5, 0.4)).toBe('#/play/5');
    expect(href.play(5, 61.9)).toBe('#/play/5?t=61');
  });

  it('round-trips the metadata pages', () => {
    expect(parseRoute(href.person(7))).toEqual({ name: 'person', id: 7 });
    expect(parseRoute(href.genre(18))).toEqual({ name: 'genre', id: 18 });
    expect(parseRoute(href.collection(9))).toEqual({ name: 'collection', id: 9 });
    expect(parseRoute(href.settings)).toEqual({ name: 'settings' });
    expect(parseRoute(href.activity)).toEqual({ name: 'activity' });
  });
});
