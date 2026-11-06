// The arithmetic of a virtual grid (components/VirtualGrid.svelte): rows of equal height, `cols`
// cards each, one every `pitch` pixels (a card and the gap under it). Positions are from the top of
// the grid. Pure, for the tests.

/** Rows from first to last, inclusive; empty when last < first. */
export interface Rows {
  first: number;
  last: number;
}

export const rowCount = (n: number, cols: number): number => (n > 0 && cols > 0 ? Math.ceil(n / cols) : 0);

/** The grid's height: every row, and the gaps between them (none after the last). */
export const gridHeight = (rows: number, pitch: number, gap: number): number => (rows > 0 ? rows * pitch - gap : 0);

/**
 * The rows on screen when its top is `top` pixels below the grid's top (negative while the grid
 * starts lower down) and it is `height` pixels tall.
 */
export function visibleRows(top: number, height: number, pitch: number, rows: number): Rows {
  if (rows <= 0 || pitch <= 0 || top + height <= 0 || top >= rows * pitch) return { first: 0, last: -1 };
  const first = Math.max(0, Math.floor(top / pitch));
  return { first, last: Math.min(rows - 1, Math.max(first, Math.ceil((top + height) / pitch) - 1)) };
}

/**
 * The rows to mount for a screen at `top`: those on it and a screen's worth more above and below
 * (their posters load at low priority), so that a fling or a Page Down finds cards drawn.
 */
export const nearRows = (top: number, height: number, pitch: number, rows: number): Rows => visibleRows(top - height, height * 3, pitch, rows);

/** `r` and `extra` more rows on each side, within the grid (the focused card's neighbours). */
export function withOverscan(r: Rows, extra: number, rows: number): Rows {
  if (r.last < r.first) return r;
  return { first: Math.max(0, r.first - extra), last: Math.min(rows - 1, r.last + extra) };
}

/** The rows of `ranges` together, each once, in order (the ones on screen and the focused card's). */
export function rowsOf(...ranges: Rows[]): number[] {
  const set = new Set<number>();
  for (const r of ranges) for (let i = r.first; i <= r.last; i++) set.add(i);
  return [...set].sort((a, b) => a - b);
}

/**
 * Where the screen is in the grid: the first card of the first row wholly on it (its top at `top`
 * or below), and how far above that row's top the screen starts (`offset`, 0 or less). What a Back
 * finds the grid by again, with as many columns or another number: that card in the first row.
 */
export function firstVisible(top: number, pitch: number, cols: number): { index: number; offset: number } {
  if (pitch <= 0 || cols <= 0) return { index: 0, offset: top };
  const row = Math.max(0, Math.ceil(top / pitch));
  return { index: row * cols, offset: top - row * pitch };
}

/** The inverse of firstVisible: how far below the grid's top the screen's top goes. */
export const topOf = (index: number, offset: number, pitch: number, cols: number): number =>
  cols > 0 ? Math.floor(Math.max(0, index) / cols) * pitch + offset : offset;

/** The card an arrow key moves the focus to from card `i`, or -1 for none (an edge). */
export function arrowTarget(key: string, i: number, cols: number, n: number): number {
  const to = key === 'ArrowLeft' ? i - 1 : key === 'ArrowRight' ? i + 1 : key === 'ArrowUp' ? i - cols : key === 'ArrowDown' ? i + cols : -1;
  if (key === 'ArrowDown' && to >= n && Math.floor(i / cols) < rowCount(n, cols) - 1) return n - 1; // the short last row
  return to >= 0 && to < n && to !== i ? to : -1;
}
