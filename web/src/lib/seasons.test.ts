import { describe, expect, it } from 'vitest';
import { inView, scrollEdges, scrollToShow } from './seasons';

describe('scrollEdges', () => {
  it('a row that fits has nothing more, and is at both ends', () => {
    expect(scrollEdges(0, 300, 300)).toEqual({ more: false, atStart: true, atEnd: true });
    expect(scrollEdges(0, 300, 200)).toEqual({ more: false, atStart: true, atEnd: true });
  });

  it('past the right edge there is more, and only that edge is ahead', () => {
    expect(scrollEdges(0, 300, 800)).toEqual({ more: true, atStart: true, atEnd: false });
  });

  it('in the middle there is something either way', () => {
    expect(scrollEdges(250, 300, 800)).toEqual({ more: true, atStart: false, atEnd: false });
  });

  it('at the right end the start is behind, the end reached', () => {
    expect(scrollEdges(500, 300, 800)).toEqual({ more: true, atStart: false, atEnd: true });
  });

  it('a fraction of a pixel off an edge counts as it', () => {
    expect(scrollEdges(0.5, 300, 800).atStart).toBe(true);
    expect(scrollEdges(499.5, 300, 800).atEnd).toBe(true);
  });

  it('a row a pixel wider than it shows has nothing to page to', () => {
    expect(scrollEdges(0, 300, 301).more).toBe(false);
  });
});

describe('inView', () => {
  // Cards 100 wide, 10 apart: 0-100, 110-210, 220-320, 330-430, 440-540.
  const boxes = Array.from({ length: 5 }, (_, i) => ({ left: i * 110, width: 100 }));

  it('takes every card the box shows some of', () => {
    expect([...inView(boxes, 0, 250)]).toEqual([0, 1, 2]);
    expect([...inView(boxes, 200, 150)]).toEqual([1, 2, 3]);
  });

  it('leaves out a card that only touches an edge', () => {
    expect([...inView(boxes, 0, 220)]).toEqual([0, 1]);
    expect([...inView(boxes, 210, 100)]).toEqual([2]);
  });

  it('takes them all when they fit', () => {
    expect(inView(boxes, 0, 1000).size).toBe(5);
  });
});

describe('scrollToShow', () => {
  it('leaves a card wholly in view where it is', () => {
    expect(scrollToShow(110, 100, 0, 300)).toBe(0);
    expect(scrollToShow(0, 100, 0, 300)).toBe(0);
  });

  it('puts a card out of view, or cut by an edge, in the middle', () => {
    expect(scrollToShow(440, 100, 0, 300)).toBe(340);
    expect(scrollToShow(220, 100, 0, 300)).toBe(120);
    expect(scrollToShow(0, 100, 50, 300)).toBe(-100); // the browser keeps it at 0
  });
});
