import { describe, expect, it } from 'vitest';
import type { PersonCredit, TitleCard } from './api';
import { cardHref, formatDate, groupCredits, groupOf, lifeSpan, sortCards, yearSpan } from './credits';

let nextId = 1;
const card = (over: Partial<TitleCard> = {}): TitleCard => {
  const id = nextId++;
  return { id, kind: 'movie', title: `Film ${id}`, year: 2000, itemId: id * 10, ...over };
};
const cast = (title: TitleCard, character = ''): PersonCredit => ({ titleId: title.id, kind: 'cast', character, title });
const crew = (title: TitleCard, job: string): PersonCredit => ({ titleId: title.id, kind: 'crew', job, title });
const ids = (list: { id: number }[]) => list.map((c) => c.id);

describe('groupOf', () => {
  it('cast is Acting, whatever else the credit carries', () => {
    expect(groupOf({ kind: 'cast' })).toBe('Acting');
    expect(groupOf({ kind: 'cast', job: 'Director' })).toBe('Acting');
  });

  it('Director is Directing', () => {
    expect(groupOf({ kind: 'crew', job: 'Director' })).toBe('Directing');
  });

  it.each(['Writer', 'Screenplay', 'Story', 'Novel', 'Creator'])('%s is Writing', (job) => {
    expect(groupOf({ kind: 'crew', job })).toBe('Writing');
  });

  it('every other crew job is Other, including no job and near misses', () => {
    for (const job of ['Producer', 'Executive Producer', 'Director of Photography', 'Original Music Composer', 'Co-Director', '']) {
      expect(groupOf({ kind: 'crew', job })).toBe('Other');
    }
    expect(groupOf({ kind: 'crew' })).toBe('Other');
  });

  it('ignores case and stray spaces in the job', () => {
    expect(groupOf({ kind: 'crew', job: ' director ' })).toBe('Directing');
    expect(groupOf({ kind: 'crew', job: 'SCREENPLAY' })).toBe('Writing');
  });
});

describe('groupCredits', () => {
  it('lists the groups as Acting, Directing, Writing, Other and leaves empty ones out', () => {
    const a = card();
    const b = card();
    const c = card();
    const d = card();
    const sections = groupCredits([crew(a, 'Producer'), crew(b, 'Screenplay'), crew(c, 'Director'), cast(d, 'Rick')]);
    expect(sections.map((s) => s.group)).toEqual(['Acting', 'Directing', 'Writing', 'Other']);
    expect(groupCredits([crew(a, 'Director')]).map((s) => s.group)).toEqual(['Directing']);
    expect(groupCredits([])).toEqual([]);
  });

  it('shows a title once in a group however many roles the person had in it', () => {
    const film = card();
    const sections = groupCredits([crew(film, 'Writer'), crew(film, 'Screenplay'), crew(film, 'Writer')]);
    expect(sections).toHaveLength(1);
    expect(ids(sections[0].entries.map((e) => e.card))).toEqual([film.id]);
    expect(sections[0].entries[0].detail).toBe('Writer, Screenplay');
  });

  it('keeps one title that is in two groups in both', () => {
    const film = card();
    const sections = groupCredits([cast(film, 'Himself'), crew(film, 'Director')]);
    expect(sections.map((s) => [s.group, s.entries.length])).toEqual([
      ['Acting', 1],
      ['Directing', 1],
    ]);
  });

  it('the detail is the character for cast and the job for crew', () => {
    const a = card();
    const b = card();
    const sections = groupCredits([cast(a, 'Rick Deckard'), crew(b, 'Executive Producer')]);
    expect(sections[0].entries[0].detail).toBe('Rick Deckard');
    expect(sections[1].entries[0].detail).toBe('Executive Producer');
  });

  it('two characters in one title read together, a blank one adds nothing', () => {
    const film = card();
    const sections = groupCredits([cast(film, 'Fry'), cast(film, ''), cast(film, 'Zoidberg'), cast(film, 'Fry')]);
    expect(sections[0].entries[0].detail).toBe('Fry, Zoidberg');
    expect(groupCredits([cast(card(), '')])[0].entries[0].detail).toBe('');
  });

  it('puts the newest title first and a title with no year last', () => {
    const old = card({ year: 1982 });
    const none = card({ year: 0 });
    const recent = card({ year: 2017 });
    const mid = card({ year: 1999 });
    const sections = groupCredits([cast(old), cast(none), cast(recent), cast(mid)]);
    expect(ids(sections[0].entries.map((e) => e.card))).toEqual([recent.id, mid.id, old.id, none.id]);
  });

  it('keeps the server order for titles of the same year', () => {
    const a = card({ year: 2001 });
    const b = card({ year: 2001 });
    const c = card({ year: 2001 });
    const sections = groupCredits([cast(b), cast(c), cast(a)]);
    expect(ids(sections[0].entries.map((e) => e.card))).toEqual([b.id, c.id, a.id]);
  });
});

describe('cardHref', () => {
  it('a film opens its file, a series its show', () => {
    expect(cardHref({ kind: 'movie', itemId: 7 })).toBe('#/item/7');
    expect(cardHref({ kind: 'series', show: 'The Wire' })).toBe('#/shows/The%20Wire');
  });

  it('a card with nothing to open has no link', () => {
    expect(cardHref({ kind: 'movie' })).toBeUndefined();
    expect(cardHref({ kind: 'series' })).toBeUndefined();
    expect(cardHref({ kind: 'series', itemId: 7 })).toBeUndefined();
    expect(cardHref({ kind: 'movie', show: 'The Wire' })).toBeUndefined();
  });
});

describe('sortCards', () => {
  const a = card({ title: 'Alien', year: 1979, rating: 8.4 });
  const b = card({ title: 'Blade Runner', year: 1982, rating: 7.9 });
  const c = card({ title: 'Cube', year: 0 });
  const d = card({ title: 'Dune', year: 2021, rating: 8.4 });
  const list = [a, b, c, d];
  const titles = (l: TitleCard[]) => l.map((x) => x.title);

  it('title keeps the order of the list, and sorting does not change the list itself', () => {
    expect(titles(sortCards([d, a], 'title'))).toEqual(['Dune', 'Alien']);
    const copy = [...list];
    sortCards(list, 'year');
    expect(list).toEqual(copy);
  });

  it('year puts the newest first and an unknown year last', () => {
    expect(titles(sortCards(list, 'year'))).toEqual(['Dune', 'Blade Runner', 'Alien', 'Cube']);
  });

  it('rating puts the best first, unrated last, and ties keep the list order', () => {
    expect(titles(sortCards(list, 'rating'))).toEqual(['Alien', 'Dune', 'Blade Runner', 'Cube']);
  });
});

describe('yearSpan', () => {
  it('is one year, a span, or nothing', () => {
    expect(yearSpan([{ year: 1999 }])).toBe('1999');
    expect(yearSpan([{ year: 2003 }, { year: 1999 }, { year: 2001 }])).toBe('1999–2003');
    expect(yearSpan([{ year: 0 }, { year: 2001 }])).toBe('2001');
    expect(yearSpan([{ year: 0 }])).toBe('');
    expect(yearSpan([])).toBe('');
  });
});

describe('formatDate', () => {
  it('writes day, month name and year', () => {
    expect(formatDate('1961-03-12')).toBe('12 March 1961');
    expect(formatDate('1999-01-01')).toBe('1 January 1999');
    expect(formatDate('2023-12-31')).toBe('31 December 2023');
  });

  it('uses the date as written: a timestamp cannot slip into the next day', () => {
    expect(formatDate('1961-03-12T00:00:00Z')).toBe('12 March 1961');
    expect(formatDate('1961-03-12T23:59:59-08:00')).toBe('12 March 1961');
  });

  it('copes with a partial date', () => {
    expect(formatDate('1961-03')).toBe('March 1961');
    expect(formatDate('1961')).toBe('1961');
    expect(formatDate('1961-00-00')).toBe('1961');
    expect(formatDate('1961-03-00')).toBe('March 1961');
  });

  it('is empty for a missing or impossible date', () => {
    for (const bad of [undefined, null, '', ' ', 'unknown', '0000-00-00', '1961-13-01', '1961-02-30', '1961-3-12', '12/03/1961']) {
      expect(formatDate(bad)).toBe('');
    }
  });

  it('knows leap years', () => {
    expect(formatDate('2000-02-29')).toBe('29 February 2000');
    expect(formatDate('1900-02-29')).toBe('');
    expect(formatDate('2023-02-29')).toBe('');
    expect(formatDate('2024-04-31')).toBe('');
  });
});

describe('lifeSpan', () => {
  it('a living person has a birth date', () => {
    expect(lifeSpan('1961-03-12', undefined)).toBe('Born 12 March 1961');
    expect(lifeSpan('1961-03-12', '')).toBe('Born 12 March 1961');
  });

  it('a death date turns it into the years of a life', () => {
    expect(lifeSpan('1961-03-12', '2023-07-04')).toBe('1961–2023');
  });

  it('copes with one of the two missing', () => {
    expect(lifeSpan(undefined, '2023-07-04')).toBe('Died 4 July 2023');
    expect(lifeSpan('not a date', '2023-07-04')).toBe('Died 4 July 2023');
    expect(lifeSpan('', '')).toBe('');
    expect(lifeSpan(undefined, undefined)).toBe('');
  });
});

describe('dates: month lengths', () => {
  it('30-day months end on the 30th, and February follows the leap rule', () => {
    expect(formatDate('2023-04-30')).toBe('30 April 2023');
    expect(formatDate('2023-04-31')).toBe(''); // April has 30 days
    expect(formatDate('2023-06-31')).toBe('');
    expect(formatDate('2023-09-31')).toBe('');
    expect(formatDate('2023-11-31')).toBe('');
    expect(formatDate('2023-05-31')).toBe('31 May 2023');
    expect(formatDate('2024-02-29')).toBe('29 February 2024');
    expect(formatDate('2023-02-29')).toBe('');
  });
});
