import { describe, expect, it } from 'vitest';
import type { CardRating, RatingEntry, RatingSource } from './api';
import {
  linkParts,
  metacriticBand,
  normaliseRatings,
  orderShown,
  passesMinRating,
  ratedAgo,
  ratingAriaLabel,
  ratingScore,
  ratingTooltip,
  ratingValue,
  RATING_SOURCES,
  safeRatingUrl,
  shortVotes,
  titleRatings,
  tomatoState,
  type RatingLike,
} from './ratings';

const at = '2026-11-02T10:00:00Z';
const entry = (source: RatingSource, value: number, scale: number, over: Partial<RatingEntry> = {}): RatingEntry => ({
  source,
  value,
  scale,
  score100: Math.round((value / scale) * 100),
  url: '',
  fetchedAt: at,
  ...over,
});

// true only when A and B are the same type (not merely assignable).
type Same<A, B> = (<T>() => T extends A ? 1 : 2) extends <T>() => T extends B ? 1 : 2 ? true : false;

// A rating as a movie card has it: no link, votes or fetch time, and `state` only from a newer server.
const cardRating = (source: RatingSource, value: number, scale: number, state?: string): CardRating => ({
  source,
  value,
  scale,
  score100: Math.round((value / scale) * 100),
  ...(state ? { state } : {}),
});

describe('shortVotes', () => {
  it('counts in short form, as the sites do', () => {
    expect(shortVotes(999)).toBe('999');
    expect(shortVotes(1000)).toBe('1K');
    expect(shortVotes(1540)).toBe('1.5K');
    expect(shortVotes(12_345)).toBe('12K');
    expect(shortVotes(999_400)).toBe('999K');
    expect(shortVotes(999_600)).toBe('1M'); // not "1000K"
    expect(shortVotes(1_234_567)).toBe('1.2M');
    expect(shortVotes(2_000_000)).toBe('2M');
    expect(shortVotes(12_600_000)).toBe('13M');
  });

  it('is empty for none', () => {
    expect(shortVotes(0)).toBe('');
    expect(shortVotes(undefined)).toBe('');
    expect(shortVotes(null)).toBe('');
    expect(shortVotes(-4)).toBe('');
  });
});

describe('ratingValue', () => {
  it('shows each source as it shows itself', () => {
    expect(ratingValue(entry('imdb', 8.66, 10))).toBe('8.7');
    expect(ratingValue(entry('imdb', 8, 10))).toBe('8.0');
    expect(ratingValue(entry('tmdb', 7.84, 10))).toBe('7.8');
    expect(ratingValue(entry('letterboxd', 4.08, 5))).toBe('4.1');
    expect(ratingValue(entry('rt_critics', 93, 100))).toBe('93%');
    expect(ratingValue(entry('rt_audience', 91, 100))).toBe('91%');
    expect(ratingValue(entry('trakt', 84, 100))).toBe('84%');
    expect(ratingValue(entry('metacritic', 74, 100))).toBe('74');
  });
});

describe('metacriticBand', () => {
  it('is green from 61, yellow from 40 to 60, red under 40', () => {
    expect(metacriticBand(100)).toBe('good');
    expect(metacriticBand(61)).toBe('good');
    expect(metacriticBand(60)).toBe('mixed');
    expect(metacriticBand(40)).toBe('mixed');
    expect(metacriticBand(39)).toBe('bad');
    expect(metacriticBand(0)).toBe('bad');
  });
});

describe('tomatoState', () => {
  it("takes the server's word, else the 60% line", () => {
    expect(tomatoState(entry('rt_critics', 60, 100))).toBe('fresh');
    expect(tomatoState(entry('rt_critics', 59, 100))).toBe('rotten');
    expect(tomatoState(entry('rt_critics', 97, 100, { state: 'certified' }))).toBe('certified');
    expect(tomatoState(entry('rt_audience', 60, 100))).toBe('upright');
    expect(tomatoState(entry('rt_audience', 59, 100))).toBe('spilled');
    expect(tomatoState(entry('rt_audience', 95, 100, { state: 'verified_hot' }))).toBe('verified_hot');
    // A word of the other score's vocabulary is not taken.
    expect(tomatoState(entry('rt_critics', 40, 100, { state: 'upright' }))).toBe('rotten');
    expect(tomatoState(entry('imdb', 9, 10, { state: 'fresh' }))).toBe('');
  });

  it("a card's rating: the server's word when it sends one, else the 60% line", () => {
    expect(tomatoState(cardRating('rt_critics', 97, 100, 'certified'))).toBe('certified');
    expect(tomatoState(cardRating('rt_critics', 97, 100))).toBe('fresh');
    expect(tomatoState(cardRating('rt_audience', 41, 100))).toBe('spilled');
  });
});

describe('labels and tooltips', () => {
  it('say the source in full, the scale and the votes', () => {
    // IMDb's chip shows "IMDb 8.7 1.2M": the label has that, in that order (label in name).
    expect(ratingAriaLabel(entry('imdb', 8.7, 10, { votes: 1_234_567 }))).toBe('IMDb 8.7, 1.2M votes');
    expect(ratingAriaLabel(entry('imdb', 8.7, 10))).toBe('IMDb 8.7 out of 10');
    expect(ratingAriaLabel(entry('rt_critics', 93, 100))).toBe('Rotten Tomatoes critics 93%, fresh');
    expect(ratingAriaLabel(entry('rt_audience', 41, 100))).toBe('Rotten Tomatoes audience 41%, spilled');
    expect(ratingAriaLabel(entry('metacritic', 74, 100))).toBe('Metacritic 74 out of 100');
    expect(ratingAriaLabel(entry('letterboxd', 4.1, 5))).toBe('Letterboxd 4.1 out of 5');
  });

  it('the tooltip has the votes in full and the age of the number', () => {
    const now = new Date('2026-11-05T09:00:00Z');
    expect(ratingTooltip(entry('imdb', 8.7, 10, { votes: 1_234_567 }), now)).toBe('IMDb, 1,234,567 votes. Rated 2 days ago.');
    expect(ratingTooltip(entry('tmdb', 7.8, 10, { votes: 1 }), now)).toBe('TMDB, 1 vote. Rated 2 days ago.');
    expect(ratingTooltip(entry('metacritic', 74, 100, { fetchedAt: '' }), now)).toBe('Metacritic.');
  });

  it("a card's rating has no votes or age: its label and tooltip leave them out", () => {
    const now = new Date('2026-11-05T09:00:00Z');
    expect(ratingTooltip(cardRating('imdb', 8.7, 10), now)).toBe('IMDb.');
    expect(ratingAriaLabel(cardRating('imdb', 8.7, 10))).toBe('IMDb 8.7 out of 10');
    expect(ratingAriaLabel(cardRating('rt_critics', 97, 100, 'certified'))).toBe('Rotten Tomatoes critics 97%, certified fresh');
    expect(ratingAriaLabel(cardRating('rt_critics', 93, 100))).toBe('Rotten Tomatoes critics 93%, fresh');
  });

  it('ratedAgo counts whole days', () => {
    const now = new Date('2026-11-05T09:00:00Z');
    expect(ratedAgo('2026-11-05T01:00:00Z', now)).toBe('rated today');
    expect(ratedAgo('2026-11-04T08:00:00Z', now)).toBe('rated 1 day ago');
    expect(ratedAgo('2026-10-22T09:00:00Z', now)).toBe('rated 14 days ago');
    expect(ratedAgo('2026-11-06T09:00:00Z', now)).toBe('rated today'); // a clock ahead is not "in the future"
    expect(ratedAgo('soon', now)).toBe('');
    expect(ratedAgo(undefined, now)).toBe('');
  });
});

describe('orderShown', () => {
  const all = RATING_SOURCES.map((s) => entry(s, s === 'letterboxd' ? 4 : s === 'imdb' || s === 'tmdb' ? 8 : 80, s === 'letterboxd' ? 5 : s === 'imdb' || s === 'tmdb' ? 10 : 100));
  const sources = (l: RatingEntry[]) => l.map((e) => e.source);

  it('follows the settings, in their order, leaving out what they hide', () => {
    expect(sources(orderShown(all, ['metacritic', 'imdb', 'rt_critics']))).toEqual(['metacritic', 'imdb', 'rt_critics']);
    expect(sources(orderShown(all, []))).toEqual([]);
  });

  it('a shown source the title has no rating from is skipped', () => {
    expect(sources(orderShown([all[0]], ['trakt', 'imdb']))).toEqual(['imdb']);
  });

  it('with no settings, everything in the order the server sent', () => {
    expect(sources(orderShown(all, null))).toEqual(RATING_SOURCES);
    expect(sources(orderShown([all[3], all[0]], undefined))).toEqual(['metacritic', 'imdb']);
  });
});

describe('titleRatings', () => {
  const imdb = entry('imdb', 8.7, 10);
  const rt = entry('rt_critics', 93, 100);

  it("is the detail's list once it has come", () => {
    expect(titleRatings({ ratings: [imdb, rt], rating: imdb }, { rating: imdb })).toEqual([imdb, rt]);
    expect(titleRatings({ ratings: [] }, { rating: imdb })).toEqual([]); // the title has none: none
  });

  it("before that, the card's one rating (an entry, as the server now sends it)", () => {
    expect(titleRatings(undefined, { rating: rt })).toEqual([rt]);
    expect(titleRatings({ rating: imdb }, undefined)).toEqual([imdb]);
    expect(titleRatings(undefined, { rating: null })).toEqual([]);
    expect(titleRatings(undefined, undefined)).toEqual([]);
  });

  it("a card's rating stands in until the detail comes", () => {
    const one = cardRating('imdb', 8.7, 10);
    expect(titleRatings(undefined, { rating: one })).toEqual([one]);
    expect(titleRatings({ ratings: [imdb, rt] }, { rating: one })).toEqual([imdb, rt]);
    // A detail with no list (an older server) and a card: the card's entry, and it orders.
    const mixed = titleRatings({ rating: null }, { rating: one });
    expect(mixed).toEqual([one]);
    expect(orderShown(mixed, ['rt_critics', 'imdb'])).toEqual([one]);
  });

  it('types the entries as either side has them (checked by npm run check)', () => {
    const one = cardRating('imdb', 8.7, 10);
    // Both sides full: full entries, as Ratings and orderShown take them today.
    const full: RatingEntry[] = titleRatings({ ratings: [imdb, rt] }, { rating: imdb });
    // A card summary with a full detail: a card's entry may come back, so not RatingEntry[]...
    const mixed = titleRatings({ ratings: [imdb, rt] }, { rating: one }) satisfies RatingLike[];
    // @ts-expect-error a card's entry has no url or fetchedAt
    const notFull: RatingEntry[] = mixed;
    // ...but each entry is still either a full one or a card's, not narrowed to the card's fields.
    const either: Same<typeof mixed, (RatingEntry | CardRating)[]> = true;
    const shown = orderShown(mixed, null);
    const ordered: Same<typeof shown, typeof mixed> = true; // orderShown keeps the entries' type
    expect([full.length, notFull.length, mixed.length]).toEqual([2, 2, 2]);
    expect([either, ordered]).toEqual([true, true]);
  });
});

describe('sorting and filtering by rating', () => {
  it('goes by score100, whatever the scale, and no rating is last', () => {
    expect(ratingScore({ rating: entry('imdb', 8, 10) })).toBe(80);
    expect(ratingScore({ rating: entry('rt_critics', 75, 100) })).toBe(75);
    expect(ratingScore({ rating: entry('letterboxd', 4.5, 5) })).toBe(90);
    expect(ratingScore({ rating: null })).toBe(-1);
    expect(ratingScore({})).toBe(-1);
    expect(ratingScore(undefined)).toBe(-1);
  });

  it('a minimum keeps what reaches it and drops what has no rating', () => {
    const seventy = { rating: entry('rt_critics', 70, 100) };
    expect(passesMinRating(seventy, 70)).toBe(true);
    expect(passesMinRating(seventy, 71)).toBe(false);
    expect(passesMinRating({ rating: entry('imdb', 8.1, 10) }, 80)).toBe(true); // 81, not 8.1
    expect(passesMinRating({ rating: null }, 60)).toBe(false);
    expect(passesMinRating({ rating: null }, 0)).toBe(true);
  });

  it("a card's rating sorts and filters by the same score100, with or without a state", () => {
    expect(ratingScore({ rating: cardRating('imdb', 8, 10) })).toBe(80);
    expect(ratingScore({ rating: cardRating('rt_critics', 75, 100, 'fresh') })).toBe(75);
    expect(ratingScore({ rating: cardRating('letterboxd', 4.5, 5) })).toBe(90);
    expect(passesMinRating({ rating: cardRating('rt_critics', 70, 100) }, 70)).toBe(true);
    expect(passesMinRating({ rating: cardRating('rt_critics', 70, 100, 'fresh') }, 71)).toBe(false);
    expect(passesMinRating({ rating: cardRating('imdb', 8.1, 10) }, 80)).toBe(true);
  });
});

describe('normaliseRatings', () => {
  it("turns Go's null into [] and drops what is not an entry of a known source", () => {
    expect(normaliseRatings(null)).toEqual([]);
    expect(normaliseRatings(undefined)).toEqual([]);
    const imdb = entry('imdb', 8, 10);
    const odd = { ...imdb, source: 'myanimelist' } as unknown as RatingEntry;
    expect(normaliseRatings([imdb, null, odd])).toEqual([imdb]);
  });
});

describe('linkParts', () => {
  it("links the URL where the server's words have it", () => {
    expect(linkParts('Information courtesy of IMDb (https://www.imdb.com). Used with permission.', 'https://www.imdb.com')).toEqual({
      before: 'Information courtesy of IMDb (',
      link: 'https://www.imdb.com',
      after: '). Used with permission.',
    });
    expect(linkParts('See https://a.example for more.', 'https://a.example/').link).toBe('https://a.example');
  });

  it('else adds the host as the link after the words, which stay whole', () => {
    const p = linkParts('Ratings provided by MDBList.', 'https://mdblist.com');
    expect(p).toEqual({ before: 'Ratings provided by MDBList. ', link: 'mdblist.com', after: '' });
    expect(p.before + p.link + p.after).toContain('Ratings provided by MDBList.');
    expect(linkParts('Words only.', '')).toEqual({ before: 'Words only.', link: '', after: '' });
  });
});

describe('safeRatingUrl', () => {
  it('keeps https links only', () => {
    expect(safeRatingUrl('https://www.imdb.com/title/tt0133093/')).toBe('https://www.imdb.com/title/tt0133093/');
    expect(safeRatingUrl('http://www.imdb.com/title/tt0133093/')).toBe('');
    expect(safeRatingUrl('javascript:alert(1)')).toBe('');
    expect(safeRatingUrl('')).toBe('');
    expect(safeRatingUrl('/relative')).toBe('');
  });
});
