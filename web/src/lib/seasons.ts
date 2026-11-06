// The row of season posters on a show's page (components/SeasonRow): which cards of the row are on
// screen (those load first), and where to scroll it to show one.

/**
 * The indices of the boxes that a scroll box shows any of, laid out along x: `left` and `width` of
 * each from the start of the scrolled content, the box scrolled to `scroll` and `width` wide.
 */
export function inView(boxes: readonly { left: number; width: number }[], scroll: number, width: number): Set<number> {
  const out = new Set<number>();
  boxes.forEach((b, i) => {
    if (b.left + b.width > scroll && b.left < scroll + width) out.add(i);
  });
  return out;
}

/**
 * Where to scroll a row so that the box at `left`, `width` wide, is wholly in view: unchanged when it
 * already is, else with the box in the middle (the browser keeps it within the row).
 */
export function scrollToShow(left: number, width: number, scroll: number, view: number): number {
  if (left >= scroll && left + width <= scroll + view) return scroll;
  return left + width / 2 - view / 2;
}
