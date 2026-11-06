import { describe, expect, it } from 'vitest';
import { inView, scrollToShow } from './seasons';

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
