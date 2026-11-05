import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// The token lives in module state: import a fresh copy for each test.
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

const call = (fn: ReturnType<typeof stubFetch>): FetchCall => fn.mock.calls[0] as unknown as FetchCall;
const sent = (fn: ReturnType<typeof stubFetch>) => JSON.parse(String(call(fn)[1].body));

describe('imageUrl', () => {
  it('is empty for nothing', () => {
    expect(m.imageUrl(undefined)).toBe('');
    expect(m.imageUrl(null)).toBe('');
    expect(m.imageUrl('')).toBe('');
    expect(m.imageUrl('', 'w500')).toBe('');
  });

  it('makes an absolute URL and appends the token like other media', () => {
    expect(m.imageUrl('/api/images/tmdb/w342/abc.jpg')).toBe(`${location.origin}/api/images/tmdb/w342/abc.jpg`);
    m.setToken('s3cret');
    expect(m.imageUrl('/api/images/tmdb/w342/abc.jpg')).toBe(`${location.origin}/api/images/tmdb/w342/abc.jpg?token=s3cret`);
    expect(m.imageUrl('/api/images/local/item/12/poster')).toBe(`${location.origin}/api/images/local/item/12/poster?token=s3cret`);
  });

  it('keeps a token the server already put in the URL', () => {
    m.setToken('s3cret');
    expect(m.imageUrl('/api/images/tmdb/w342/abc.jpg?token=s3cret')).toBe(
      `${location.origin}/api/images/tmdb/w342/abc.jpg?token=s3cret`,
    );
  });

  it('switches the size of a TMDB image, keeping the query', () => {
    m.setToken('t');
    expect(m.imageUrl('/api/images/tmdb/w342/abc.jpg?token=t', 'w500')).toBe(`${location.origin}/api/images/tmdb/w500/abc.jpg?token=t`);
    expect(m.imageUrl('/api/images/tmdb/w342/abc.jpg', 'original')).toBe(`${location.origin}/api/images/tmdb/original/abc.jpg?token=t`);
    expect(m.imageUrl('/api/images/tmdb/w1280/b.png', 'w780')).toContain('/w780/b.png');
  });

  it('leaves other URLs at their size', () => {
    expect(m.imageUrl('/api/images/local/item/12/poster', 'w500')).toBe(`${location.origin}/api/images/local/item/12/poster`);
    expect(m.imageUrl('/api/items/5/art/thumb?v=1', 'w500')).toBe(`${location.origin}/api/items/5/art/thumb?v=1`);
  });

  it('switches the size of a TVDB image, whose path has directories', () => {
    m.setToken('t');
    expect(m.imageUrl('/api/images/tvdb/w342/banners/posters/81189-1.jpg?token=t', 'w780')).toBe(
      `${location.origin}/api/images/tvdb/w780/banners/posters/81189-1.jpg?token=t`,
    );
    expect(m.imageUrl('/api/images/tvdb/w1280/banners/v4/series/81189/backdrops/5f8c.jpeg', 'w500')).toContain(
      '/api/images/tvdb/w500/banners/v4/series/81189/backdrops/5f8c.jpeg',
    );
    expect(m.imageSrcset('/api/images/tvdb/w342/banners/p/1.png', ['w185', 'w500'])).toBe(
      `${location.origin}/api/images/tvdb/w185/banners/p/1.png?token=t 185w, ${location.origin}/api/images/tvdb/w500/banners/p/1.png?token=t 500w`,
    );
    // What the server's TVDBImagePath takes: dots inside names, an upper-case extension.
    expect(m.imageUrl('/api/images/tvdb/w342/banners/v4.1/x.y.JPG', 'w500')).toContain('/api/images/tvdb/w500/banners/v4.1/x.y.JPG');
    // a traversal anywhere, a "." segment and a size off the list stay as they are
    expect(m.imageUrl('/api/images/tvdb/w342/banners/../x.jpg', 'w500')).not.toContain('/w500/');
    expect(m.imageUrl('/api/images/tvdb/w342/banners/a..b.jpg', 'w500')).not.toContain('/w500/');
    expect(m.imageUrl('/api/images/tvdb/w342/banners/./x.jpg', 'w500')).not.toContain('/w500/');
    expect(m.imageUrl('/api/images/tvdb/w100/banners/x.jpg', 'w500')).not.toContain('/w500/');
    expect(m.imageUrl('/api/images/tvdb/w342/banners/x.gif', 'w500')).not.toContain('/w500/');
  });

  it('does not rewrite what is not a TMDB image path', () => {
    // a size outside the allowlist, a traversal and an odd name stay as they are
    expect(m.imageUrl('/api/images/tmdb/w100/abc.jpg', 'w500')).toBe(`${location.origin}/api/images/tmdb/w100/abc.jpg`);
    expect(m.imageUrl('/api/images/tmdb/w342/../../x.jpg', 'w500')).not.toContain('/w500/');
  });
});

describe('imageSrcset', () => {
  it('lists the sizes with their widths', () => {
    m.setToken('t');
    expect(m.imageSrcset('/api/images/tmdb/w342/abc.jpg?token=t')).toBe(
      [185, 342, 500].map((w) => `${location.origin}/api/images/tmdb/w${w}/abc.jpg?token=t ${w}w`).join(', '),
    );
    expect(m.imageSrcset('/api/images/tmdb/w342/abc.jpg', ['w92', 'w185'])).toBe(
      `${location.origin}/api/images/tmdb/w92/abc.jpg?token=t 92w, ${location.origin}/api/images/tmdb/w185/abc.jpg?token=t 185w`,
    );
  });

  it('is one candidate for a sidecar image and empty for nothing', () => {
    expect(m.imageSrcset('/api/images/local/item/1/poster')).toBe(`${location.origin}/api/images/local/item/1/poster`);
    expect(m.imageSrcset(undefined)).toBe('');
  });
});

describe('parseMatchRef', () => {
  const cases: [string, unknown][] = [
    // A bare number is a title ("1917", "300", "24"), searched like any other: never an id.
    ['603', null],
    ['1917', null],
    ['  24  ', null],
    ['tmdb:603', { tmdbId: 603 }],
    ['TMDB: 603', { tmdbId: 603 }],
    ['tmdb 603', { tmdbId: 603 }],
    ['tmdb:0', null],
    ['tmdb:', null],
    ['tmdb603', null],
    ['my tmdb 603', null],
    ['https://www.themoviedb.org/movie/603', { tmdbId: 603 }],
    ['https://www.themoviedb.org/movie/603-the-matrix?language=en-US', { tmdbId: 603 }],
    ['themoviedb.org/tv/1396-breaking-bad', { tmdbId: 1396 }],
    ['https://www.imdb.com/title/tt0133093/', { imdbId: 'tt0133093' }],
    ['https://m.imdb.com/title/tt0133093/?ref_=x', { imdbId: 'tt0133093' }],
    ['tt0133093', { imdbId: 'tt0133093' }],
    ['TT0133093', { imdbId: 'tt0133093' }],
    ['tt12345678', { imdbId: 'tt12345678' }],
    ['', null],
    ['   ', null],
    ['0', null],
    ['-5', null],
    ['6.5', null],
    ['tt123456', null],
    ['tt0133093x', null],
    ['the matrix', null],
    ['https://www.themoviedb.org/person/6384', null],
    ['https://example.com/movie/603', null],
    ['12345678901', null],
    // An IMDb id inside other text counts only on imdb.com (or alone).
    ['https://example.com/title/tt0133093/', null],
    ['see tt0133093 there', null],
    // TVDB (M8): sent for the server to read; an id said as such always as "tvdb:N", an address as it
    // was typed. The forms ParseTVDBRef takes: "tvdb:N", "TVDB:N", "tvdb N", "tvdb : N", "tvdbN".
    ['tvdb:81189', { tvdb: 'tvdb:81189' }],
    ['TVDB: 81189', { tvdb: 'tvdb:81189' }],
    ['tvdb 81189', { tvdb: 'tvdb:81189' }],
    ['  tvdb:81189  ', { tvdb: 'tvdb:81189' }],
    ['tvdb:0', null],
    ['tvdb:', null],
    ['tvdb81189', { tvdb: 'tvdb:81189' }],
    ['TVDB:81189', { tvdb: 'tvdb:81189' }],
    ['tvdb : 81189', { tvdb: 'tvdb:81189' }],
    ['tvdb:81189x', null],
    ['tvdb::81189', null],
    ['tvdbx81189', null],
    ['my tvdb 81189', null],
    ['https://thetvdb.com/series/breaking-bad', { tvdb: 'https://thetvdb.com/series/breaking-bad' }],
    ['thetvdb.com/series/breaking-bad', { tvdb: 'thetvdb.com/series/breaking-bad' }],
    ['https://www.thetvdb.com/series/breaking-bad/allseasons/official', { tvdb: 'https://www.thetvdb.com/series/breaking-bad/allseasons/official' }],
    ['https://thetvdb.com/series/81189', { tvdb: 'https://thetvdb.com/series/81189' }],
    ['https://thetvdb.com/dereferrer/series/81189', { tvdb: 'https://thetvdb.com/dereferrer/series/81189' }],
    ['https://thetvdb.com/?tab=series&id=81189', { tvdb: 'https://thetvdb.com/?tab=series&id=81189' }],
    ['https://www.thetvdb.com/index.php?tab=series&id=81189&lid=7', { tvdb: 'https://www.thetvdb.com/index.php?tab=series&id=81189&lid=7' }],
    ['https://thetvdb.com/?tab=series&id=0', null],
    ['https://thetvdb.com/?id=81189', null],
    ['https://thetvdb.com/movies/the-matrix', null],
    ['https://thetvdb.com/series/', null],
    ['https://thetvdb.com/series/-bad', null],
    ['https://thetvdb.com.evil.com/series/breaking-bad', null],
    ['https://evil.com/thetvdb.com/series/breaking-bad', null],
    ['https://user@thetvdb.com/series/breaking-bad', null],
    ['https://thetvdb.com:8080/series/breaking-bad', null],
    ['ftp://thetvdb.com/series/breaking-bad', null],
    ['thetvdb.com/series/breaking bad', null],
    // As ParseTVDBRef: any port is refused, the default one too; the path is read decoded; an id
    // in an address may be longer than ten digits.
    ['https://thetvdb.com:443/series/breaking-bad', null],
    ['thetvdb.com:443/series/breaking-bad', null],
    ['https://thetvdb.com/series/breaking%2Dbad', { tvdb: 'https://thetvdb.com/series/breaking%2Dbad' }],
    ['https://thetvdb.com/series/breaking%20bad', null],
    ['https://thetvdb.com/series/%E0%A4%A', null],
    ['https://thetvdb.com/series/123456789012', { tvdb: 'https://thetvdb.com/series/123456789012' }],
  ];
  for (const [input, want] of cases) {
    it(`reads ${JSON.stringify(input)}`, () => {
      expect(m.parseMatchRef(input)).toEqual(want);
    });
  }

  it('says which kind of title a themoviedb.org link is, and nothing for the others', () => {
    expect(m.parseMatchInput('https://www.themoviedb.org/movie/603')).toEqual({ ref: { tmdbId: 603 }, kind: 'movie' });
    expect(m.parseMatchInput('themoviedb.org/TV/1396-breaking-bad')).toEqual({ ref: { tmdbId: 1396 }, kind: 'series' });
    expect(m.parseMatchInput('tmdb:603')).toEqual({ ref: { tmdbId: 603 } });
    expect(m.parseMatchInput('tt0133093')).toEqual({ ref: { imdbId: 'tt0133093' } });
    expect(m.parseMatchInput('1917')).toBeNull();
  });

  it('says a TVDB reference is a show, and what it names', () => {
    expect(m.parseMatchInput('tvdb:81189')).toEqual({ ref: { tvdb: 'tvdb:81189' }, kind: 'series', tvdb: { id: 81189, slug: '' } });
    expect(m.parseMatchInput('https://thetvdb.com/series/Breaking-Bad')).toEqual({
      ref: { tvdb: 'https://thetvdb.com/series/Breaking-Bad' },
      kind: 'series',
      tvdb: { id: 0, slug: 'breaking-bad' },
    });
    expect(m.parseTvdbRef('https://thetvdb.com/?tab=series&id=81189')).toEqual({ id: 81189, slug: '' });
    // Positive control for the hosts refused above: the same path at thetvdb.com is read.
    expect(m.parseTvdbRef('https://thetvdb.com/series/breaking-bad')).toEqual({ id: 0, slug: 'breaking-bad' });
    expect(m.parseTvdbRef('https://thetvdb.com/series/breaking%2Dbad')).toEqual({ id: 0, slug: 'breaking-bad' });
    expect(m.parseTvdbRef('https://thetvdb.com/series/123456789012')).toEqual({ id: 123456789012, slug: '' });
  });
});

describe('mergedInto', () => {
  it('reads the 404 of a merged person, and nothing else', () => {
    expect(m.mergedInto(new m.ApiError(404, 'merged', undefined, { error: 'merged', into: 8 }))).toBe(8);
    expect(m.mergedInto(new m.ApiError(404, 'person not found', undefined, { error: 'person not found' }))).toBe(0);
    expect(m.mergedInto(new m.ApiError(404, 'merged', undefined, { error: 'merged', into: 0 }))).toBe(0);
    expect(m.mergedInto(new m.ApiError(404, 'merged', undefined, { error: 'merged', into: '8' }))).toBe(0);
    expect(m.mergedInto(new m.ApiError(500, 'merged', undefined, { error: 'merged', into: 8 }))).toBe(0);
    expect(m.mergedInto(new Error('merged'))).toBe(0);
  });

  it('gets the body through the request', async () => {
    stubFetch(json({ error: 'merged', into: 8 }, 404));
    const e = await m.api.person(5).catch((x: unknown) => x);
    expect(m.mergedInto(e)).toBe(8);
  });
});

describe('candidateRef', () => {
  it('sends the id of the result’s own source', () => {
    expect(m.candidateRef({ source: 'tvdb', id: 81189, tvdbId: 81189 })).toEqual({ tvdbId: 81189 });
    expect(m.candidateRef({ source: 'tmdb', id: 1396, tmdbId: 1396 })).toEqual({ tmdbId: 1396 });
    // Only id given: still its source's field.
    expect(m.candidateRef({ source: 'tvdb', id: 81189 })).toEqual({ tvdbId: 81189 });
  });
});

describe('metadata requests', () => {
  it('lists people by name', async () => {
    m.setToken('tok');
    const fn = stubFetch(json([{ id: 1, name: 'Keanu Reeves' }]));
    expect(await m.api.people('kea nu')).toEqual([{ id: 1, name: 'Keanu Reeves' }]);
    const [path, init] = call(fn);
    expect(path).toBe('/api/people?q=kea+nu');
    expect(init.method).toBe('GET');
    expect((init.headers as Record<string, string>).Authorization).toBe('Bearer tok');
  });

  it('turns the nulls Go sends for empty lists into lists', async () => {
    stubFetch(json(null));
    expect(await m.api.people('x')).toEqual([]);
    stubFetch(json(null));
    expect(await m.api.genres()).toEqual([]);
    stubFetch(json(null));
    expect(await m.api.titles()).toEqual([]);
    stubFetch(json(null));
    expect(await m.api.review()).toEqual([]);
    stubFetch(json(null));
    expect(await m.api.searchMetadata('movie', 'x')).toEqual([]);
    stubFetch(json({ id: 1, name: 'A', titles: null }));
    expect((await m.api.collection(1)).titles).toEqual([]);
    stubFetch(json({ id: 1, name: 'A', credits: null }));
    expect((await m.api.person(1)).credits).toEqual([]);
    stubFetch(json({ show: 'S', episodeCount: 1, seasons: null }));
    expect((await m.api.show('S')).seasons).toEqual([]);
  });

  it('fetches a person, a collection, genres', async () => {
    let fn = stubFetch(json({ id: 7, name: 'P', credits: [] }));
    await m.api.person(7);
    expect(call(fn)[0]).toBe('/api/people/7');
    fn = stubFetch(json({ id: 3, name: 'C', titles: [] }));
    await m.api.collection(3);
    expect(call(fn)[0]).toBe('/api/collections/3');
    fn = stubFetch(json([{ id: 28, name: 'Action', count: 4 }]));
    expect(await m.api.genres()).toEqual([{ id: 28, name: 'Action', count: 4 }]);
    expect(call(fn)[0]).toBe('/api/genres');
  });

  it('filters titles by genre, collection and person, and only by what is given', async () => {
    let fn = stubFetch(json([]));
    await m.api.titles({ genre: 28 });
    expect(call(fn)[0]).toBe('/api/titles?genre=28');
    fn = stubFetch(json([]));
    await m.api.titles({ genre: 28, collection: 5, person: 6384 });
    expect(call(fn)[0]).toBe('/api/titles?genre=28&collection=5&person=6384');
    fn = stubFetch(json([]));
    await m.api.titles();
    expect(call(fn)[0]).toBe('/api/titles');
    fn = stubFetch(json([]));
    await m.api.titles({ genre: 0 });
    expect(call(fn)[0]).toBe('/api/titles'); // 0 is no id: it is left out
  });

  it('lists the upcoming episodes of the shows in the library', async () => {
    m.setToken('tok');
    const list = [
      {
        titleId: 9,
        show: 'Cool Show',
        poster: '/api/images/tmdb/w342/p.jpg?token=tok',
        backdrop: '/api/images/tmdb/w1280/b.jpg?token=tok',
        nextAirDate: '2026-11-08',
        nextEpisode: { season: 3, episode: 4, name: 'Please Hold to My Hand' },
      },
      { titleId: 10, show: 'Other', nextAirDate: '2026-11-09' },
    ];
    const fn = stubFetch(json(list));
    const got = await m.api.upcoming();
    expect(got).toEqual(list);
    expect(got[0].nextEpisode?.episode).toBe(4);
    expect(got[1].nextEpisode).toBeUndefined();
    const [path, init] = call(fn);
    expect(path).toBe('/api/upcoming');
    expect(init.method).toBe('GET');
    expect((init.headers as Record<string, string>).Authorization).toBe('Bearer tok');

    // Go sends null for a list with nothing in it.
    stubFetch(json(null));
    expect(await m.api.upcoming()).toEqual([]);
    stubFetch(json([]));
    expect(await m.api.upcoming()).toEqual([]);
  });

  it('carries the next air date on a card and in a title', async () => {
    stubFetch(json([{ id: 1, kind: 'series', title: 'S', year: 2020, show: 'S', nextAirDate: '2026-11-08' }]));
    const [card] = await m.api.titles();
    expect(card.nextAirDate).toBe('2026-11-08');
    stubFetch(json({ show: 'S', episodeCount: 1, seasons: [], meta: { id: 1, nextAirDate: '2026-11-08' } }));
    expect((await m.api.show('S')).meta?.nextAirDate).toBe('2026-11-08');
  });

  it('encodes a show name as one path segment', async () => {
    const fn = stubFetch(json({ show: 'AC/DC: 100% Live', episodeCount: 1, seasons: [] }));
    await m.api.show('AC/DC: 100% Live');
    expect(call(fn)[0]).toBe('/api/shows/AC%2FDC%3A%20100%25%20Live');
    for (const [fnName, method, suffix] of [
      ['matchShow', 'PUT', '/match'],
      ['unmatchShow', 'DELETE', '/match'],
      ['refreshShow', 'POST', '/metadata/refresh'],
    ] as const) {
      const f = stubFetch(json({ started: true, show: 'x', episodeCount: 0, seasons: [] }));
      await (m.api[fnName] as (n: string, r?: { tmdbId: number }) => Promise<unknown>)('Show / One', { tmdbId: 1 });
      expect(call(f)[0]).toBe(`/api/shows/Show%20%2F%20One${suffix}`);
      expect(call(f)[1].method).toBe(method);
    }
  });

  it('keeps each search result’s source and id, and reads an older server’s as TMDB’s', async () => {
    stubFetch(
      json([
        { source: 'tvdb', id: 81189, tvdbId: 81189, title: 'Breaking Bad', year: 2008, score: 0.98 },
        { source: 'tmdb', id: 1396, tmdbId: 1396, title: 'Breaking Bad', year: 2008, score: 0.95 },
        { tmdbId: 603, title: 'The Matrix', year: 1999, score: 0.9 }, // no source: a server older than M8
      ]),
    );
    const res = await m.api.searchMetadata('series', 'Breaking Bad');
    expect(res.map((r) => [r.source, r.id])).toEqual([
      ['tvdb', 81189],
      ['tmdb', 1396],
      ['tmdb', 603],
    ]);
    expect(res.map(m.candidateRef)).toEqual([{ tvdbId: 81189 }, { tmdbId: 1396 }, { tmdbId: 603 }]);
  });

  it('changes a TVDB show’s episode order, and reads the unmapped count and Go’s nulls', async () => {
    const fn = stubFetch(json({ id: 9, source: 'tvdb', episodeOrder: 'dvd', unmapped: 3, genres: null, cast: null, crew: null }));
    const t = await m.api.patchTitle(9, { episodeOrder: 'dvd' });
    expect(call(fn)[0]).toBe('/api/titles/9');
    expect(call(fn)[1].method).toBe('PATCH');
    expect(sent(fn)).toEqual({ episodeOrder: 'dvd' });
    expect(t.unmapped).toBe(3);
    expect(t.episodeOrder).toBe('dvd');
    expect([t.genres, t.cast, t.crew]).toEqual([[], [], []]);
    stubFetch(json({ error: 'TVDB has no episodes for this show in that order' }, 409));
    await expect(m.api.patchTitle(9, { episodeOrder: 'absolute' })).rejects.toMatchObject({
      status: 409,
      message: 'TVDB has no episodes for this show in that order',
    });
  });

  it('sends a TVDB match as the server takes it', async () => {
    let fn = stubFetch(json({ show: 'Breaking Bad', episodeCount: 1, seasons: [] }));
    await m.api.matchShow('Breaking Bad', { tvdbId: 81189 });
    expect(sent(fn)).toEqual({ tvdbId: 81189 });
    fn = stubFetch(json({ show: 'Breaking Bad', episodeCount: 1, seasons: [] }));
    await m.api.matchShow('Breaking Bad', { tvdb: 'https://thetvdb.com/series/breaking-bad' });
    expect(sent(fn)).toEqual({ tvdb: 'https://thetvdb.com/series/breaking-bad' });
  });

  it('tests a key by provider, never sending one', async () => {
    let fn = stubFetch(json({ ok: true }));
    expect(await m.api.testKey('tvdb')).toEqual({ ok: true });
    expect(call(fn)[0]).toBe('/api/settings/test');
    expect(call(fn)[1].method).toBe('POST');
    expect(sent(fn)).toEqual({ provider: 'tvdb' });
    fn = stubFetch(json({ ok: false, error: 'TVDB rejected the key or PIN' }));
    expect(await m.api.testKey('tmdb')).toEqual({ ok: false, error: 'TVDB rejected the key or PIN' });
    expect(sent(fn)).toEqual({ provider: 'tmdb' });
    stubFetch(json(null));
    expect(await m.api.testKey('tvdb')).toEqual({ ok: false });
  });

  it('searches TMDB with the kind, the text and the year', async () => {
    let fn = stubFetch(json([{ tmdbId: 603, title: 'The Matrix', year: 1999, score: 1 }]));
    const res = await m.api.searchMetadata('movie', 'The Matrix', 1999);
    expect(res[0].tmdbId).toBe(603);
    expect(call(fn)[0]).toBe('/api/metadata/search?kind=movie&q=The+Matrix&year=1999');
    fn = stubFetch(json([]));
    await m.api.searchMetadata('series', 'Breaking Bad');
    expect(call(fn)[0]).toBe('/api/metadata/search?kind=series&q=Breaking+Bad');
  });

  it('fixes and undoes a match', async () => {
    let fn = stubFetch(json({ id: 5, meta: { id: 9, title: 'Inception' } }));
    const d = await m.api.matchItem(5, { tmdbId: 27205 });
    expect(d.meta?.title).toBe('Inception');
    expect(call(fn)[0]).toBe('/api/items/5/match');
    expect(call(fn)[1].method).toBe('PUT');
    expect(sent(fn)).toEqual({ tmdbId: 27205 });
    expect((call(fn)[1].headers as Record<string, string>)['Content-Type']).toBe('application/json');

    fn = stubFetch(json({ id: 5 }));
    await m.api.matchItem(5, { imdbId: 'tt1375666' });
    expect(sent(fn)).toEqual({ imdbId: 'tt1375666' });

    fn = stubFetch(json({ id: 5 }));
    await m.api.unmatchItem(5);
    expect(call(fn)[0]).toBe('/api/items/5/match');
    expect(call(fn)[1].method).toBe('DELETE');
    expect(call(fn)[1].body).toBeUndefined();
  });

  it('edits a title', async () => {
    const fn = stubFetch(json({ id: 9, title: 'Mine', locked: true }));
    const t = await m.api.patchTitle(9, { title: 'Mine', year: 2000 });
    expect(t.locked).toBe(true);
    expect(call(fn)[0]).toBe('/api/titles/9');
    expect(call(fn)[1].method).toBe('PATCH');
    expect(sent(fn)).toEqual({ title: 'Mine', year: 2000 });
  });

  it('asks for refreshes, which answer with nothing the caller needs', async () => {
    let fn = stubFetch(json({ started: true }, 202));
    await expect(m.api.refreshItem(4)).resolves.toBeUndefined();
    expect(call(fn)[0]).toBe('/api/items/4/metadata/refresh');
    expect(call(fn)[1].method).toBe('POST');
    fn = stubFetch(json({ started: true }, 202));
    await expect(m.api.refreshAll()).resolves.toBeUndefined();
    expect(call(fn)[0]).toBe('/api/metadata/refresh');
  });

  it('reads the status and the review list', async () => {
    let fn = stubFetch(json({ enabled: true, keySet: true, running: false, pending: 2, unmatched: 1, needsReview: 0, lastError: '' }));
    expect((await m.api.metadataStatus()).pending).toBe(2);
    expect(call(fn)[0]).toBe('/api/metadata/status');
    fn = stubFetch(json([{ kind: 'movie', itemId: 3, title: 'X', year: 2001, matchState: 'unmatched', needsReview: false, score: 0.2 }]));
    expect((await m.api.review())[0].itemId).toBe(3);
    expect(call(fn)[0]).toBe('/api/metadata/review');
  });

  it('reads and saves the settings, sending the key only when given', async () => {
    let fn = stubFetch(json({ tmdbKeySet: true, tmdbKeySource: 'settings', metadataEnabled: true, metadataForcedOff: false, language: 'en-US' }));
    const s = await m.api.settings();
    expect(s.tmdbKeySource).toBe('settings');
    expect(Object.keys(s)).not.toContain('tmdbKey');
    expect(call(fn)[0]).toBe('/api/settings');

    fn = stubFetch(json(s));
    await m.api.saveSettings({ metadataEnabled: false });
    expect(call(fn)[1].method).toBe('PUT');
    expect(sent(fn)).toEqual({ metadataEnabled: false });
    fn = stubFetch(json(s));
    await m.api.saveSettings({ tmdbKey: 'abc', language: 'pt-BR' });
    expect(sent(fn)).toEqual({ tmdbKey: 'abc', language: 'pt-BR' });

    // With -metadata=false a key is stored without being checked, and the answer says so.
    stubFetch(json({ ...s, metadataForcedOff: true, tmdbKeyChecked: false }));
    expect((await m.api.saveSettings({ tmdbKey: 'abc' })).tmdbKeyChecked).toBe(false);
    stubFetch(json(s));
    expect((await m.api.saveSettings({ language: 'pt-BR' })).tmdbKeyChecked).toBeUndefined();
  });

  it('reads the TVDB settings and saves a TVDB key and PIN', async () => {
    const tvdb = {
      tmdbKeySet: false,
      tmdbKeySource: '',
      metadataEnabled: true,
      metadataForcedOff: false,
      language: 'en-US',
      tvdbKeySet: true,
      tvdbPinSet: false,
      tvdbKeySource: 'env',
      tvdbAttribution: 'Metadata provided by TheTVDB. Please consider adding missing information or subscribing.',
      tvdbAttributionUrl: 'https://thetvdb.com',
    };
    stubFetch(json(tvdb));
    const s = await m.api.settings();
    expect([s.tvdbKeySet, s.tvdbPinSet, s.tvdbKeySource]).toEqual([true, false, 'env']);
    expect(s.tvdbAttributionUrl).toBe('https://thetvdb.com');
    const fn = stubFetch(json({ ...tvdb, tvdbKeySource: 'settings', tvdbKeyChecked: true }));
    const after = await m.api.saveSettings({ tvdbKey: 'fake-key', tvdbPin: 'fake-pin' });
    expect(sent(fn)).toEqual({ tvdbKey: 'fake-key', tvdbPin: 'fake-pin' });
    expect(after.tvdbKeyChecked).toBe(true);
    stubFetch(json({ error: 'TVDB rejected the key or PIN' }, 400));
    await expect(m.api.saveSettings({ tvdbKey: 'bad' })).rejects.toMatchObject({ status: 400, message: 'TVDB rejected the key or PIN' });
  });

  it('reads the TVDB part of the status', async () => {
    stubFetch(json({ enabled: true, keySet: false, tvdbKeySet: true, shows: 'tvdb', showsLine: 'Shows: TVDB', running: false, pending: 0, unmatched: 0, needsReview: 0, lastError: '' }));
    const st = await m.api.metadataStatus();
    expect([st.keySet, st.tvdbKeySet, st.shows, st.showsLine]).toEqual([false, true, 'tvdb', 'Shows: TVDB']);
  });

  it('reads a person’s TMDB and TVDB ids', async () => {
    stubFetch(json({ id: 4, name: 'Bryan Cranston', tmdbId: 17419, tvdbId: 251010, credits: null }));
    const p = await m.api.person(4);
    expect([p.tmdbId, p.tvdbId, p.credits]).toEqual([17419, 251010, []]);
  });
});

describe('metadata errors', () => {
  it('turn the server message into an ApiError', async () => {
    stubFetch(json({ error: 'TMDB rejected the key: use a v3 API key' }, 400));
    await expect(m.api.saveSettings({ tmdbKey: 'bad' })).rejects.toMatchObject({
      name: 'ApiError',
      status: 400,
      message: 'TMDB rejected the key: use a v3 API key',
    });
    stubFetch(json({ error: 'online metadata lookups are off' }, 409));
    await expect(m.api.searchMetadata('movie', 'x')).rejects.toMatchObject({ status: 409 });
    stubFetch(json({ error: 'could not reach TMDB' }, 502));
    await expect(m.api.matchItem(1, { tmdbId: 1 })).rejects.toMatchObject({ status: 502, message: 'could not reach TMDB' });
    stubFetch(json({ error: 'show not found' }, 404));
    await expect(m.api.show('Nope')).rejects.toMatchObject({ status: 404 });
  });

  it('a 401 clears the token and calls the handler, as for every request', async () => {
    m.setToken('old');
    const handler = vi.fn();
    m.onUnauthorized(handler);
    stubFetch(json({ error: 'unauthorized' }, 401));
    await expect(m.api.settings()).rejects.toMatchObject({ status: 401 });
    expect(m.getToken()).toBe('');
    expect(handler).toHaveBeenCalledOnce();
  });

  it('an unreachable server is an ApiError with status 0', async () => {
    stubFetch(new TypeError('network'));
    await expect(m.api.genres()).rejects.toMatchObject({ status: 0 });
  });
});

describe('the scan status', () => {
  it('may list the watched roots', async () => {
    stubFetch(json({ running: false, itemCount: 3, watching: [{ root: '/media', watching: true, dirs: 12 }] }));
    const st = await m.api.scanStatus();
    expect(st.watching?.[0]).toEqual({ root: '/media', watching: true, dirs: 12 });
  });
});
