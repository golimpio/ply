import { describe, expect, it } from 'vitest';
import { byName, letterIndex, letterOf, nameKey, RAIL_MIN_ITEMS, railLetters, railOf, type LetterEntry, type Named } from './az';

const t = (title: string, over: Partial<Named> = {}, id = 1): Named => ({ title, id, ...over });
const names = (list: Named[]) => list.map((x) => x.title);

describe('byName', () => {
  it('sorts by sort title: a leading article counts after the name', () => {
    const list = [t('The Matrix', { sortTitle: 'Matrix, The' }), t('Alien'), t('A Bug’s Life', { sortTitle: 'Bug’s Life, A' }), t('Zodiac')];
    expect(names(list.sort(byName))).toEqual(['Alien', 'A Bug’s Life', 'The Matrix', 'Zodiac']);
  });

  it('ignores case as SQLite’s LOWER() does: A to Z only', () => {
    const list = [t('alien'), t('Brazil'), t('ALIENS')];
    expect(names(list.sort(byName))).toEqual(['alien', 'ALIENS', 'Brazil']);
    // É is not folded: it sorts after every ASCII letter, as the server has it.
    expect(names([t('Élite'), t('Zodiac'), t('Easy A')].sort(byName))).toEqual(['Easy A', 'Zodiac', 'Élite']);
  });

  it('compares characters, not numbers: the server’s order, not a natural one', () => {
    expect(names([t('Film 9'), t('Film 10'), t('12 Monkeys'), t('2001')].sort(byName))).toEqual(['12 Monkeys', '2001', 'Film 10', 'Film 9']);
  });

  it('breaks a tie by year, then by id', () => {
    const list = [t('Dune', { year: 2021 }, 3), t('Dune', { year: 1984 }, 9), t('Dune', { year: 2021 }, 2)];
    expect(list.sort(byName).map((x) => `${x.year}/${x.id}`)).toEqual(['1984/9', '2021/2', '2021/3']);
  });

  it('takes the title when there is no sort title (an older server)', () => {
    expect(nameKey(t('The Thing'))).toBe('the thing');
    expect(nameKey(t('The Thing', { sortTitle: '' }))).toBe('the thing');
    expect(nameKey(t('The Thing', { sortTitle: 'Thing, The' }))).toBe('thing, the');
  });

  it('compares text ids (a show’s name) as text', () => {
    expect([t('Lost', { id: 'lost' }), t('Lost', { id: 'Lost' })].sort(byName).map((x) => x.id)).toEqual(['Lost', 'lost']);
  });
});

describe('letterOf', () => {
  it('is the first letter, upper case', () => {
    expect(letterOf('alien')).toBe('A');
    expect(letterOf('Matrix, The')).toBe('M');
    expect(letterOf('  zodiac')).toBe('Z');
  });

  it('drops accents', () => {
    expect(letterOf('Élite')).toBe('E');
    expect(letterOf('Ça va')).toBe('C');
    expect(letterOf('Ñandú')).toBe('N');
  });

  it('puts digits, symbols and other scripts under #', () => {
    for (const n of ['2001', '12 Monkeys', '(500) Days of Summer', '"Weird Al"', 'Ølstykke', '東京物語', '']) expect(letterOf(n)).toBe('#');
  });
});

describe('letterIndex', () => {
  it('gives each letter present its first card, # first', () => {
    const list = ['2001', '42', 'Alien', 'Aliens', 'Brazil', 'Matrix, The', 'Memento', 'Zodiac'];
    expect(letterIndex(list)).toEqual([
      { letter: '#', index: 0 },
      { letter: 'A', index: 2 },
      { letter: 'B', index: 4 },
      { letter: 'M', index: 5 },
      { letter: 'Z', index: 7 },
    ]);
  });

  it('lists an accented name under its letter, in alphabetical place, where it is first', () => {
    // In the server's order É comes after Z; its letter is E all the same.
    expect(letterIndex(['Alien', 'Zodiac', 'Élite'])).toEqual([
      { letter: 'A', index: 0 },
      { letter: 'E', index: 2 },
      { letter: 'Z', index: 1 },
    ]);
    // Where the letter has plain names too, the first of them.
    expect(letterIndex(['Easy A', 'Zodiac', 'Élite']).find((e) => e.letter === 'E')?.index).toBe(0);
  });
});

describe('railOf', () => {
  const abc = [...'#ABCDEFGHIJKLMNOPQRSTUVWXYZ'].map((letter, index): LetterEntry => ({ letter, index }));

  it('shows every letter when they fit, or before the room is known', () => {
    expect(railOf(abc, 27)).toBe(abc);
    expect(railOf(abc, 0)).toBe(abc);
  });

  it('spreads fewer evenly on a short screen, the first and the last among them', () => {
    const some = railOf(abc, 14);
    expect(some).toHaveLength(14);
    expect(some[0].letter).toBe('#');
    expect(some.at(-1)?.letter).toBe('Z');
    expect(some.map((e) => e.letter).join('')).toBe('#BDFHJLNPRTVXZ');
  });
});

describe('railLetters', () => {
  const many = (prefixes: string[]) => Array.from({ length: RAIL_MIN_ITEMS }, (_, i) => `${prefixes[i % prefixes.length]} ${i}`);

  it('is the letter index of a long list', () => {
    expect(railLetters(many(['Alpha', 'Bravo'])).map((e) => e.letter)).toEqual(['A', 'B']);
  });

  it('is none for a short list, or one with a single letter', () => {
    expect(railLetters(many(['Alpha', 'Bravo']).slice(1))).toEqual([]);
    expect(railLetters(many(['Film']))).toEqual([]);
  });
});
