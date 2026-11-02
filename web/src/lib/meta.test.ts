import { describe, expect, it } from 'vitest';
import type { ItemSummary } from './api';
import {
  airLabel,
  backdropImg,
  crewWith,
  groupByTitle,
  posterImg,
  prefersPosters,
  ratingLabel,
  runtimeLabel,
  showWideImg,
  titleHref,
  versionLabel,
  wideImg,
  yearOf,
  yearSpan,
} from './meta';

const T = (size: string, file = 'x') => `/api/images/tmdb/${size}/${file}.jpg`;

function item(over: Partial<ItemSummary>): ItemSummary {
  return {
    id: 1,
    kind: 'movie',
    title: 'Film',
    durationSec: 6000,
    container: 'mkv',
    videoCodec: 'h264',
    audioCodec: 'aac',
    width: 1920,
    height: 1080,
    addedAt: 0,
    ...over,
  };
}

describe('pictures', () => {
  const art = { backdrop: '/api/items/1/art/backdrop', thumb: '/api/items/1/art/thumb' };

  it('posters come at card sizes with a srcset, and are null when absent', () => {
    const p = posterImg({ poster: T('w342', 'p') })!;
    expect(p.src).toContain('/w342/p.jpg');
    expect(p.srcset).toContain('/w185/p.jpg 185w');
    expect(p.srcset).toContain('/w500/p.jpg 500w');
    expect(posterImg({})).toBeNull();
    expect(posterImg(null)).toBeNull();
  });

  it('a sidecar (local) poster has no srcset', () => {
    const p = posterImg({ poster: '/api/images/local/item/3/poster' })!;
    expect(p.src).toContain('/api/images/local/item/3/poster');
    expect(p.srcset).toBeUndefined();
  });

  it('wide cards: an episode shows its still, a movie its backdrop, else the frame grab', () => {
    expect(wideImg(item({ kind: 'episode', still: T('w780', 's'), backdrop: T('w1280', 'b'), art }))!.src).toContain('/w500/s.jpg');
    expect(wideImg(item({ kind: 'episode', backdrop: T('w1280', 'b'), art }))!.src).toContain('/art/thumb');
    expect(wideImg(item({ backdrop: T('w1280', 'b'), art }))!.src).toContain('/w500/b.jpg');
    expect(wideImg(item({ art }))!.src).toContain('/art/thumb');
    expect(wideImg(item({}))).toBeNull();
  });

  it('backdrops: an episode its still, then the show backdrop, then the frame grab', () => {
    expect(backdropImg(item({ kind: 'episode', still: T('w780', 's'), backdrop: T('w1280', 'b'), art }))!.src).toContain('/w1280/s.jpg');
    expect(backdropImg(item({ kind: 'episode', backdrop: T('w1280', 'b'), art }))!.src).toContain('/w1280/b.jpg');
    expect(backdropImg(item({ art }))!.src).toContain('/art/backdrop');
  });

  it("a show's wide card is its backdrop, else its first episode's frame grab", () => {
    const cover = item({ kind: 'episode', still: T('w780', 's'), art });
    expect(showWideImg({ backdrop: T('w1280', 'b') }, cover)!.src).toContain('/w500/b.jpg');
    expect(showWideImg({}, cover)!.src).toContain('/art/thumb'); // not the episode's still
    expect(showWideImg(undefined, undefined)).toBeNull();
  });

  it('title cards lead to the film or the show', () => {
    expect(titleHref({ kind: 'movie', itemId: 4, title: 'X' })).toBe('#/item/4');
    expect(titleHref({ kind: 'series', show: 'Lost', title: 'LOST' })).toBe('#/shows/Lost');
    expect(titleHref({ kind: 'series', title: 'Dark' })).toBe('#/shows/Dark');
  });

  it('lists turn to posters only when most of them have one', () => {
    expect(prefersPosters([])).toBe(false);
    expect(prefersPosters([{ poster: 'a' }, {}])).toBe(true);
    expect(prefersPosters([{ poster: 'a' }, {}, {}])).toBe(false);
    expect(prefersPosters([{}, {}])).toBe(false);
  });
});

describe('labels', () => {
  it('ratings have one decimal and vanish when unknown', () => {
    expect(ratingLabel(7.84)).toBe('7.8');
    expect(ratingLabel(8)).toBe('8.0');
    expect(ratingLabel(0)).toBe('');
    expect(ratingLabel(undefined)).toBe('');
  });

  it('air dates in words, as local calendar days', () => {
    const now = new Date(2026, 10, 2, 23, 30); // Monday 2 Nov 2026, late evening
    expect(airLabel('2026-11-02', now)).toBe('Today');
    expect(airLabel('2026-11-03', now)).toBe('Tomorrow');
    const fri = airLabel('2026-11-06', now);
    expect(fri).toBe(new Date(2026, 10, 6).toLocaleDateString(undefined, { weekday: 'long' }));
    expect(airLabel('2026-11-09', now)).not.toBe(new Date(2026, 10, 9).toLocaleDateString(undefined, { weekday: 'long' })); // a week on: a date
    expect(airLabel('2026-11-09', now)).not.toMatch(/2026/);
    expect(airLabel('2027-01-15', now)).toMatch(/2027/);
    expect(airLabel('nonsense', now)).toBe('');
  });

  it('runtimes in hours and minutes', () => {
    expect(runtimeLabel(136)).toBe('2h 16m');
    expect(runtimeLabel(120)).toBe('2h');
    expect(runtimeLabel(45)).toBe('45m');
    expect(runtimeLabel(0)).toBe('');
  });

  it('the year from the year, else the release date', () => {
    expect(yearOf({ year: 1999, releaseDate: '2001-01-01' })).toBe(1999);
    expect(yearOf({ year: 0, releaseDate: '1982-06-25' })).toBe(1982);
    expect(yearOf({ year: 0, releaseDate: '' })).toBeUndefined();
    expect(yearOf(undefined)).toBeUndefined();
  });

  it("a show's years: a span once ended, open while running", () => {
    expect(yearSpan({ releaseDate: '2008-01-20', endDate: '2013-09-29', status: 'Ended' })).toBe('2008–2013');
    expect(yearSpan({ releaseDate: '2019-03-01', endDate: '2024-05-01', status: 'Returning Series' })).toBe('2019–');
    expect(yearSpan({ releaseDate: '2010-06-01', endDate: '2010-08-01', status: 'Canceled' })).toBe('2010');
    expect(yearSpan({ releaseDate: '', status: '' }, 2004)).toBe('2004');
    expect(yearSpan(undefined)).toBe('');
  });

  it('versions say resolution, HDR and container', () => {
    expect(versionLabel(item({ height: 2160, hdr: 'hdr10', container: 'mkv' }))).toBe('4K HDR10, MKV');
    expect(versionLabel(item({ height: 1080, hdr: false, container: 'mp4' }))).toBe('1080p, MP4');
    expect(versionLabel(item({ height: 720, hdr: true, container: 'mkv' }))).toBe('720p HDR, MKV');
  });
});

describe('crewWith', () => {
  const meta = {
    crew: [
      { personId: 1, name: 'A', job: 'Writer' },
      { personId: 2, name: 'B', job: 'Screenplay' },
      { personId: 1, name: 'A', job: 'Screenplay' },
      { personId: 3, name: 'C', job: 'Director' },
    ],
  };
  it('lists each person once, in the order of the jobs asked for', () => {
    expect(crewWith(meta, ['Screenplay', 'Writer']).map((c) => c.personId)).toEqual([2, 1]);
    expect(crewWith(meta, ['Director']).map((c) => c.name)).toEqual(['C']);
    expect(crewWith(undefined, ['Director'])).toEqual([]);
  });
});

describe('groupByTitle', () => {
  it('groups the files of one matched film, and leaves unmatched ones alone', () => {
    const a = item({ id: 1, titleId: 9, height: 1080 });
    const b = item({ id: 2, titleId: 9, height: 2160 });
    const c = item({ id: 3, height: 1080 }); // unmatched
    const d = item({ id: 4, height: 1080 }); // unmatched, same title text: still separate
    const e = item({ id: 5, kind: 'episode', titleId: 9 }); // episodes are never grouped
    const g = groupByTitle([a, b, c, d, e]);
    expect(g.map((x) => x.versions.map((v) => v.id))).toEqual([[2, 1], [3], [4], [5]]);
    expect(g[0].item.id).toBe(2); // the sharpest
  });

  it('a started version is the one a card opens', () => {
    const a = item({ id: 1, titleId: 9, height: 2160 });
    const b = item({ id: 2, titleId: 9, height: 1080, progress: { positionSec: 300, watched: false } });
    expect(groupByTitle([a, b])[0].item.id).toBe(2);
    const w = item({ id: 3, titleId: 8, height: 1080, progress: { positionSec: 300, watched: true } });
    const x = item({ id: 4, titleId: 8, height: 2160 });
    expect(groupByTitle([w, x])[0].item.id).toBe(4); // watched does not count as started
  });
});
