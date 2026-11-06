import { describe, expect, it } from 'vitest';
import { arrowTarget, firstVisible, gridHeight, nearRows, rowCount, rowsOf, topOf, visibleRows, withOverscan } from './vgrid';

describe('rows', () => {
  it('counts a short last row as a row', () => {
    expect(rowCount(5000, 7)).toBe(715);
    expect(rowCount(14, 7)).toBe(2);
    expect(rowCount(0, 7)).toBe(0);
  });

  it('is as tall as its rows and the gaps between them', () => {
    expect(gridHeight(3, 300, 28)).toBe(872);
    expect(gridHeight(0, 300, 28)).toBe(0);
  });
});

describe('visibleRows', () => {
  const pitch = 300; // a 272 px card and a 28 px gap

  it('is the rows the screen overlaps', () => {
    expect(visibleRows(0, 900, pitch, 100)).toEqual({ first: 0, last: 2 });
    expect(visibleRows(299, 900, pitch, 100)).toEqual({ first: 0, last: 3 });
    expect(visibleRows(300, 900, pitch, 100)).toEqual({ first: 1, last: 3 });
  });

  it('starts at the first row while the grid is lower down the page', () => {
    expect(visibleRows(-250, 900, pitch, 100)).toEqual({ first: 0, last: 2 });
  });

  it('is none when the grid is below or above the screen', () => {
    expect(visibleRows(-900, 900, pitch, 100)).toEqual({ first: 0, last: -1 });
    expect(visibleRows(30_000, 900, pitch, 100)).toEqual({ first: 0, last: -1 });
  });

  it('stops at the last row', () => {
    expect(visibleRows(29_500, 900, pitch, 100)).toEqual({ first: 98, last: 99 });
  });

  it('stays bounded however long the list', () => {
    const r = nearRows(500_000, 900, pitch, rowCount(50_000, 7));
    expect(r.last - r.first + 1).toBeLessThanOrEqual(11);
  });
});

describe('nearRows', () => {
  it('is the rows on screen and a screen more above and below', () => {
    expect(nearRows(3000, 900, 300, 100)).toEqual({ first: 7, last: 15 }); // on screen: 10 to 12
  });

  it('stops at the ends of the grid', () => {
    expect(nearRows(0, 900, 300, 100)).toEqual({ first: 0, last: 5 });
    expect(nearRows(29_500, 900, 300, 100)).toEqual({ first: 95, last: 99 });
  });

  it('mounts the first rows of a grid that starts less than a screen below', () => {
    expect(visibleRows(-1000, 900, 300, 100)).toEqual({ first: 0, last: -1 });
    expect(nearRows(-1000, 900, 300, 100)).toEqual({ first: 0, last: 2 });
  });
});

describe('withOverscan', () => {
  it('adds rows on each side, within the grid', () => {
    expect(withOverscan({ first: 5, last: 8 }, 2, 100)).toEqual({ first: 3, last: 10 });
    expect(withOverscan({ first: 0, last: 2 }, 2, 100)).toEqual({ first: 0, last: 4 });
    expect(withOverscan({ first: 97, last: 99 }, 2, 100)).toEqual({ first: 95, last: 99 });
  });

  it('adds none to an empty range', () => {
    expect(withOverscan({ first: 0, last: -1 }, 2, 100)).toEqual({ first: 0, last: -1 });
  });
});

describe('rowsOf', () => {
  it('joins the rows on screen and the focused card’s, each once, in order', () => {
    expect(rowsOf({ first: 10, last: 13 }, { first: 2, last: 4 })).toEqual([2, 3, 4, 10, 11, 12, 13]);
    expect(rowsOf({ first: 3, last: 5 }, { first: 4, last: 6 })).toEqual([3, 4, 5, 6]);
    expect(rowsOf({ first: 0, last: -1 })).toEqual([]);
  });
});

describe('the first card on screen', () => {
  it('is the first of the first row wholly on screen', () => {
    expect(firstVisible(4321, 300, 7)).toEqual({ index: 15 * 7, offset: -179 }); // row 14 is cut off
    expect(firstVisible(4500, 300, 7)).toEqual({ index: 15 * 7, offset: 0 });
  });

  it('is found again with as many columns, at the same place', () => {
    const at = firstVisible(4321, 300, 7);
    expect(topOf(at.index, at.offset, 300, 7)).toBe(4321);
  });

  it('is found again in the first row with other columns (a wider window)', () => {
    const at = firstVisible(4321, 300, 7); // card 105
    expect(topOf(at.index, at.offset, 300, 10)).toBe(10 * 300 - 179); // card 105 is in row 10 of 10
  });

  it('keeps a grid that starts lower down where it was', () => {
    expect(firstVisible(-200, 300, 7)).toEqual({ index: 0, offset: -200 });
    expect(topOf(0, -200, 300, 7)).toBe(-200);
  });
});

describe('arrowTarget', () => {
  // 10 cards in rows of 4: 0-3, 4-7, 8-9.
  it('moves along a row and between rows', () => {
    expect(arrowTarget('ArrowRight', 1, 4, 10)).toBe(2);
    expect(arrowTarget('ArrowLeft', 4, 4, 10)).toBe(3);
    expect(arrowTarget('ArrowDown', 1, 4, 10)).toBe(5);
    expect(arrowTarget('ArrowUp', 5, 4, 10)).toBe(1);
  });

  it('goes down to the short last row’s last card', () => {
    expect(arrowTarget('ArrowDown', 7, 4, 10)).toBe(9);
  });

  it('stops at the edges, and ignores other keys', () => {
    expect(arrowTarget('ArrowLeft', 0, 4, 10)).toBe(-1);
    expect(arrowTarget('ArrowUp', 2, 4, 10)).toBe(-1);
    expect(arrowTarget('ArrowRight', 9, 4, 10)).toBe(-1);
    expect(arrowTarget('ArrowDown', 9, 4, 10)).toBe(-1);
    expect(arrowTarget('Enter', 3, 4, 10)).toBe(-1);
  });
});
