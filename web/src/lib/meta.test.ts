import { describe, expect, it } from 'vitest';
import type { ItemSummary, MetadataStatus } from './api';
import {
  airLabel,
  backdropImg,
  CREDIT_LINES,
  crewWith,
  filmPasses,
  groupByTitle,
  isMatching,
  logoSrc,
  onePerTitle,
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
    expect(titleHref({ kind: 'movie', title: 'X' })).toBe('#/movies'); // no file to open: the list
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

  it('versions change name at 700, 1000, 1400 and 2000 lines', () => {
    const label = (height: number) => versionLabel(item({ height, hdr: false, container: '' }));
    expect([699, 700, 999, 1000, 1399, 1400, 1999, 2000].map(label)).toEqual([
      '699p', '720p', '720p', '1080p', '1080p', '1440p', '1440p', '4K',
    ]);
    expect(label(0)).toBe('');
  });

  it("a show's years: the release date wins over the fallback year", () => {
    expect(yearSpan({ releaseDate: '2001-05-01', status: '' }, 1999)).toBe('2001');
  });

  it('a logo is asked for at w500', () => {
    expect(logoSrc({ logo: '/api/images/tmdb/original/l.png' })).toContain('/w500/l.png');
    expect(logoSrc({})).toBe('');
  });

  it('air dates count calendar days, across a daylight-saving change', () => {
    // Whatever the zone, the day before a 23- or 25-hour day is still "Tomorrow" and two days on
    // is not; the dates span both the March and the October/November changes.
    for (const [from, next, after] of [
      [new Date(2026, 2, 7, 23, 50), '2026-03-08', '2026-03-09'],
      [new Date(2026, 9, 3, 23, 50), '2026-10-04', '2026-10-05'],
      [new Date(2026, 9, 24, 23, 50), '2026-10-25', '2026-10-26'],
      [new Date(2026, 10, 0, 23, 50), '2026-11-01', '2026-11-02'],
    ] as const) {
      expect(airLabel(next, from)).toBe('Tomorrow');
      expect(airLabel(after, from)).not.toBe('Tomorrow');
      expect(airLabel(after, from)).not.toBe('Today');
    }
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
  it('a novel the film is based on is a writing credit', () => {
    const writers = CREDIT_LINES.find((l) => l.label === 'Writer')!;
    expect(crewWith({ crew: [{ personId: 9, name: 'P. K. Dick', job: 'Novel' }] }, writers.jobs).map((c) => c.personId)).toEqual([9]);
  });

  it('lists each person once, in the order of the jobs asked for', () => {
    expect(crewWith(meta, ['Screenplay', 'Writer']).map((c) => c.personId)).toEqual([2, 1]);
    expect(crewWith(meta, ['Director']).map((c) => c.name)).toEqual(['C']);
    expect(crewWith(undefined, ['Director'])).toEqual([]);
  });
});

describe('onePerTitle', () => {
  it('keeps the first file of each film, and every episode and unmatched file', () => {
    const list = [
      item({ id: 1, titleId: 7 }),
      item({ id: 2, kind: 'episode', titleId: 7 }),
      item({ id: 3, titleId: 7 }),
      item({ id: 4 }),
      item({ id: 5 }),
      item({ id: 6, titleId: 8 }),
    ];
    expect(onePerTitle(list).map((i) => i.id)).toEqual([1, 2, 4, 5, 6]);
  });
});

describe('isMatching', () => {
  const st = (over: Partial<MetadataStatus> = {}) => ({ enabled: true, keySet: true, running: false, pending: 0, ...over });

  it('is on while a pass runs or titles wait, with lookups on and a key', () => {
    expect(isMatching(st({ running: true }))).toBe(true);
    expect(isMatching(st({ pending: 3 }))).toBe(true);
    expect(isMatching(st())).toBe(false);
  });

  it('is off with no key, however long the queue: it never drains, and a pass only reads local files', () => {
    expect(isMatching(st({ keySet: false, pending: 3 }))).toBe(false);
    expect(isMatching(st({ keySet: false, running: true }))).toBe(false);
  });

  it('with only a TVDB key: on while a pass runs, never for the movies that wait for TMDB', () => {
    expect(isMatching(st({ keySet: false, tvdbKeySet: true, running: true, pending: 3 }))).toBe(true);
    expect(isMatching(st({ keySet: false, tvdbKeySet: true, pending: 3 }))).toBe(false);
    expect(isMatching(st({ enabled: false, keySet: false, tvdbKeySet: true, running: true }))).toBe(false);
  });

  it('is off with lookups off, and before the status has arrived', () => {
    expect(isMatching(st({ enabled: false, running: true, pending: 3 }))).toBe(false);
    expect(isMatching(undefined)).toBe(false);
    expect(isMatching(null)).toBe(false);
  });
});

describe('filmPasses', () => {
  const is4k = (i: ItemSummary) => i.height >= 2000;
  const isHdr = (i: ItemSummary) => !!i.hdr;
  const film = (...v: Partial<ItemSummary>[]) => ({ key: 'k', item: item(v[0]), versions: v.map((o) => item(o)) });
  const passes = (g: ReturnType<typeof film>, ...filters: string[]) => filmPasses(g, { is4k, isHdr, filters });

  it('4K and HDR pass when any version is', () => {
    const g = film({ height: 1080 }, { height: 2160, hdr: 'hdr10' });
    expect(passes(g, '4k')).toBe(true);
    expect(passes(g, 'hdr')).toBe(true);
    expect(passes(film({ height: 1080 }), '4k')).toBe(false);
    expect(passes(film({ height: 1080 }), 'hdr')).toBe(false);
    expect(passes(film({ height: 1080 }))).toBe(true);
  });

  it('4K and HDR together need one version that is both', () => {
    const split = film({ height: 2160 }, { height: 1080, hdr: 'hdr10' }); // 4K SDR beside 1080p HDR
    expect(passes(split, '4k')).toBe(true);
    expect(passes(split, 'hdr')).toBe(true);
    expect(passes(split, '4k', 'hdr')).toBe(false);
    expect(passes(film({ height: 1080 }, { height: 2160, hdr: 'hdr10' }), '4k', 'hdr')).toBe(true);
    expect(passes(film({ height: 2160, hdr: 'dv' }), '4k', 'hdr')).toBe(true);
    expect(passes(film({ height: 1080, hdr: 'hdr10' }), '4k', 'hdr')).toBe(false);
  });

  it('the filters are all asked of a film: 4K and HDR from one version, Unwatched from none', () => {
    const watched = { positionSec: 7000, watched: true };
    const both = film({ height: 2160, hdr: 'hdr10' }, { height: 1080, progress: watched });
    expect(passes(both, '4k', 'hdr')).toBe(true);
    expect(passes(both, '4k', 'hdr', 'unwatched')).toBe(false);
    expect(passes(film({ height: 2160, hdr: 'hdr10' }), '4k', 'hdr', 'unwatched')).toBe(true);
  });

  it('a film with any version watched to the end is not unwatched', () => {
    const seen = film({ height: 1080, progress: { positionSec: 6000, watched: true } }, { height: 2160 });
    expect(passes(seen, 'unwatched')).toBe(false);
    expect(passes(film({ height: 2160, progress: { positionSec: 60, watched: false } }), 'unwatched')).toBe(true);
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

  it('between equal versions the first file is the one a card opens, and versions are listed by id', () => {
    const later = item({ id: 5, titleId: 9, height: 1080 });
    const earlier = item({ id: 2, titleId: 9, height: 1080 });
    const g = groupByTitle([later, earlier])[0];
    expect(g.item.id).toBe(5);
    expect(g.versions.map((v) => v.id)).toEqual([2, 5]);
  });

  it('of two started versions the sharper one is the one a card opens', () => {
    const started = { positionSec: 300, watched: false };
    const a = item({ id: 1, titleId: 9, height: 1080, progress: started });
    const b = item({ id: 2, titleId: 9, height: 2160, progress: started });
    expect(groupByTitle([a, b])[0].item.id).toBe(2);
    expect(groupByTitle([b, a])[0].item.id).toBe(2);
  });

  it('a version at 0 seconds is not started', () => {
    const a = item({ id: 1, titleId: 9, height: 2160 });
    const b = item({ id: 2, titleId: 9, height: 1080, progress: { positionSec: 0, watched: false } });
    expect(groupByTitle([a, b])[0].item.id).toBe(1);
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
