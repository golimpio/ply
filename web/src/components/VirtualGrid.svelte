<script lang="ts" generics="T">
  import { onMount, tick, untrack, type Snippet } from 'svelte';
  import { claimRestore, recall, remember } from '../lib/router.svelte';
  import { arrowTarget, firstVisible, gridHeight, nearRows, rowCount, rowsOf, topOf, visibleRows, withOverscan, type Rows } from '../lib/vgrid';

  // A library grid that keeps in the page only the rows on screen, about a screen more above and
  // below (nearRows) and the focused card's: thousands of films cost a few screens of DOM. The
  // columns are the CSS grid's own (.grid, .grid.posters), read when the grid is resized; every card
  // is as tall as the next (MediaCard's line under the title is one line, always), measured by a
  // ResizeObserver, so row r sits at r × pitch. The window scrolls (the app scrolls the document):
  // one read of where the grid is per frame scrolled, in requestAnimationFrame. When the pitch or
  // the columns change (a resize, a zoom, the fonts) the first card on screen stays where it was.
  //
  // On a return to its view (Back, Forward, a reload) it puts itself back from that first card,
  // which it remember()s as it scrolls (claimRestore: the router's anchor link may not be mounted,
  // so the router can't). The cards on screen load first (priority high), the others after; a
  // poster dropped with its row before it loaded lets go of its request (Picture).
  let {
    items,
    key,
    ratio,
    posters = false,
    railed = false,
    card,
  }: {
    items: T[];
    key: (item: T) => string | number;
    /** A card's picture, height over width (1.5 for a 2:3 poster): the estimate until one is measured. */
    ratio: number;
    posters?: boolean;
    /** The A to Z rail is on the right edge: keep the cards clear of it where the gutter is narrower. */
    railed?: boolean;
    /** `hold`: the card's row is new, its picture not to be asked for yet (see HOLD_MS). */
    card: Snippet<[item: T, index: number, priority: 'high' | 'low', sizes: string | undefined, hold: boolean]>;
  } = $props();

  type First = { index: number; offset: number };
  const STATE = 'grid.first';
  /** A card's title and line under it, until a row is measured. */
  const TEXT_ESTIMATE = 52;
  const NONE: Rows = { first: 0, last: -1 };

  let el = $state<HTMLDivElement>();
  let cols = $state(1);
  let colWidth = $state(0);
  let gap = $state(0);
  let cardHeight = $state(0);
  /** Where the fixed header ends: the first card on screen is the first under it. */
  let under = 0;
  /** The rows on screen, and those mounted around them. */
  let shown = $state<Rows>(NONE);
  let near = $state<Rows>(NONE);
  let focusRow = $state(-1);
  /** The first card on screen as last seen, for a change of pitch or columns to keep in place. */
  let first: First | null = null;
  let restoring = false;

  const pitch = $derived((cardHeight || colWidth * ratio + TEXT_ESTIMATE) + gap);
  const rows = $derived(rowCount(items.length, cols));
  const mounted = $derived(rowsOf(near, focusRow >= 0 && focusRow < rows ? withOverscan({ first: focusRow, last: focusRow }, 1, rows) : NONE));
  const sizes = $derived(colWidth > 0 ? `${Math.ceil(colWidth)}px` : undefined);

  /** The columns and the gap between rows, from the grid's computed style (after a resize). */
  function measure() {
    if (!el) return;
    under = document.querySelector('header')?.getBoundingClientRect().bottom ?? 0;
    const css = getComputedStyle(el);
    const tracks = css.gridTemplateColumns.split(' ').filter((t) => t.endsWith('px'));
    cols = Math.max(1, tracks.length);
    const width = tracks.length ? parseFloat(tracks[0]) : el.clientWidth;
    if (width !== colWidth) cardHeight = 0; // the rows say their new height as they resize
    colWidth = width;
    gap = parseFloat(css.rowGap) || 0;
  }

  /** Where the screen's top is, from the grid's top. */
  const screenTop = () => (el ? -el.getBoundingClientRect().top : 0);

  /** The rows for a screen whose top is `top` below the grid's. */
  function place(top: number) {
    const r = visibleRows(top, innerHeight, pitch, rows);
    const n = nearRows(top, innerHeight, pitch, rows);
    if (r.first !== shown.first || r.last !== shown.last) shown = r;
    if (n.first !== near.first || n.last !== near.last) near = n;
  }

  let frame = 0;
  function schedule() {
    if (!frame) frame = requestAnimationFrame(update);
  }
  function update() {
    cancelAnimationFrame(frame);
    frame = 0;
    if (!el) return;
    const top = screenTop();
    place(top);
    first = firstVisible(top + under, pitch, cols);
    remember(STATE, first);
  }

  /** Where the page scrolls to for `at` to be the first card on screen. */
  const scrollFor = (at: First, gridTop: number) => gridTop + topOf(at.index, at.offset, pitch, cols) - under;

  /** Back where its view was left: the first card that was on screen, under the header, again. */
  async function restore(saved: { scroll: number; anchor?: { href: string }; refocus: boolean }) {
    if (!el) return;
    restoring = true;
    const at = recall<First | null>(STATE, null);
    measure();
    const gridTop = el.getBoundingClientRect().top + scrollY;
    const target = () => (at ? scrollFor(at, gridTop) : saved.scroll);
    loadNow();
    place(target() - gridTop);
    await tick();
    // The rows are there: measure them, so that the pitch is the real one before the jump.
    for (const row of el?.querySelectorAll<HTMLElement>('.row') ?? []) {
      cardHeight = Math.max(cardHeight, row.getBoundingClientRect().height);
    }
    loadNow();
    place(target() - gridTop);
    await tick();
    window.scrollTo(0, target());
    restoring = false;
    update();
    if (saved.refocus && saved.anchor) {
      for (const a of el?.querySelectorAll<HTMLElement>('a[href]') ?? []) {
        if (a.getAttribute('href') === saved.anchor.href) {
          a.focus({ preventScroll: true });
          break;
        }
      }
    }
  }

  /** The card element of index i, when its row is mounted. */
  const cardAt = (i: number) => el?.querySelector<HTMLElement>(`.cell[data-index="${i}"] > *`) ?? undefined;
  /** The index of the card an event happened in, or -1. */
  function indexOf(target: EventTarget | null): number {
    const cell = target instanceof Element ? target.closest<HTMLElement>('.cell') : null;
    return cell && el?.contains(cell) ? Number(cell.dataset.index) : -1;
  }

  /** Moves the focus to card i, mounting its row first. */
  export async function focusIndex(i: number) {
    focusRow = Math.floor(i / cols);
    await tick();
    const c = cardAt(i);
    c?.focus({ preventScroll: true });
    c?.scrollIntoView({ block: 'nearest' });
  }

  /**
   * Scrolls so that card i's row is the first under the header (the A to Z rail). Its posters load
   * at once, unless `moving` (a drag along the rail, still going: they wait as a fling's do).
   */
  export function scrollToIndex(i: number, focus = false, moving = false) {
    if (!el) return;
    const y = el.getBoundingClientRect().top + scrollY + Math.floor(i / cols) * pitch - under - gap;
    window.scrollTo(0, Math.max(0, y));
    if (!moving) loadNow();
    update(); // mount the rows there now, not a frame later (a drag along the rail)
    if (focus) void focusIndex(i);
  }

  function onkeydown(e: KeyboardEvent) {
    if (e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;
    const i = indexOf(e.target);
    const to = i < 0 ? -1 : arrowTarget(e.key, i, cols, items.length);
    if (to < 0) return;
    e.preventDefault();
    void focusIndex(to);
  }
  function onfocusin(e: FocusEvent) {
    const i = indexOf(e.target);
    if (i >= 0) focusRow = Math.floor(i / cols);
  }
  function onfocusout(e: FocusEvent) {
    if (!(e.relatedTarget instanceof Node && el?.contains(e.relatedTarget))) focusRow = -1;
  }

  // Rows are measured as they mount and whenever they change size (fonts, a resize). Every card is
  // as tall as the next; should one not be, the pitch is the tallest row's, so that rows never
  // overlap.
  let rowObserver: ResizeObserver | undefined;
  function observe(node: HTMLElement) {
    rowObserver?.observe(node);
    return () => rowObserver?.unobserve(node);
  }

  onMount(() => {
    const gridObserver = new ResizeObserver(() => {
      measure();
      schedule();
    });
    rowObserver = new ResizeObserver((entries) => {
      for (const e of entries) {
        const h = e.borderBoxSize?.[0]?.blockSize ?? e.contentRect.height;
        if (h > cardHeight + 0.5) cardHeight = h;
      }
    });
    if (el) gridObserver.observe(el);
    for (const row of el?.querySelectorAll<HTMLElement>('.row') ?? []) rowObserver.observe(row);
    measure();
    // Before the first update: it would remember where the screen is now (the page left's scroll).
    const saved = claimRestore();
    if (saved) void restore(saved);
    else {
      loadNow();
      update();
    }
    return () => {
      clearTimeout(holdTimer);
      gridObserver.disconnect();
      rowObserver?.disconnect();
      cancelAnimationFrame(frame);
    };
  });

  // A return to this view as its items change under it (Back from one sort of Movies to another;
  // onMount takes one as the grid mounts). Otherwise the rows follow the list, its columns and its
  // pitch.
  let listed: T[] | undefined;
  $effect(() => {
    const now = items;
    untrack(() => {
      const changed = listed !== undefined && listed !== now;
      listed = now;
      const saved = changed ? claimRestore() : undefined;
      if (saved) void restore(saved);
    });
  });
  $effect(() => {
    void items;
    void cols;
    void pitch;
    untrack(schedule);
  });

  // A row's posters are asked for once it has been in the page HOLD_MS: a fling mounts and drops
  // rows every frame and asks for none of them (each request is the server's work, a NAS read or an
  // upstream fetch, aborted or not); a scroll at reading speed mounts rows a screen ahead (nearRows),
  // ready long before they come on screen. One timer, for the row due first. The rows of a first
  // mount, a Back and a rail jump load at once (loadNow).
  const HOLD_MS = 100;
  const born = new Map<number, number>();
  let ready = $state<ReadonlySet<number>>(new Set());
  let holdTimer: ReturnType<typeof setTimeout> | undefined;
  let immediate = false;
  /** The rows mounted by the next change load at once. */
  function loadNow() {
    immediate = true;
    void tick().then(() => (immediate = false));
  }
  function release() {
    clearTimeout(holdTimer);
    const now = performance.now();
    let due = Infinity;
    const next = new Set<number>();
    for (const [r, at] of born) {
      if (now - at >= HOLD_MS) next.add(r);
      else due = Math.min(due, at + HOLD_MS);
    }
    if (next.size !== ready.size || [...next].some((r) => !ready.has(r))) ready = next;
    holdTimer = due < Infinity ? setTimeout(release, due - now + 1) : undefined;
  }
  $effect(() => {
    const rowsNow = mounted;
    untrack(() => {
      const now = immediate ? -Infinity : performance.now();
      for (const r of rowsNow) if (!born.has(r)) born.set(r, now);
      for (const r of [...born.keys()]) if (!rowsNow.includes(r)) born.delete(r);
      release();
    });
  });

  // A new pitch or number of columns moves every row: scroll so that the first card on screen
  // stays the first, where it was.
  let layout = '';
  $effect(() => {
    const now = `${cols} ${pitch}`;
    untrack(() => {
      const was = layout;
      layout = now;
      if (!was || was === now || restoring || !first || !el) return;
      const y = scrollFor(first, el.getBoundingClientRect().top + scrollY);
      if (Math.abs(y - scrollY) >= 1) window.scrollTo(0, y);
    });
  });
</script>

<svelte:window onscroll={schedule} onresize={schedule} />

<!-- svelte-ignore a11y_no_noninteractive_element_interactions (the keys move between the links inside) -->
<div
  bind:this={el}
  class="grid vgrid"
  class:posters
  class:railed
  role="list"
  style:height="{gridHeight(rows, pitch, gap)}px"
  {onkeydown}
  {onfocusin}
  {onfocusout}
>
  {#each mounted as r (r)}
    <div class="row" role="none" data-row={r} style:top="{r * pitch}px" {@attach observe}>
      {#each items.slice(r * cols, r * cols + cols) as item, j (key(item))}
        {@const i = r * cols + j}
        <div class="cell" role="listitem" data-index={i} aria-setsize={items.length} aria-posinset={i + 1}>
          {@render card(item, i, r >= shown.first && r <= shown.last ? 'high' : 'low', sizes, !ready.has(r))}
        </div>
      {/each}
    </div>
  {/each}
</div>

<style>
  .vgrid {
    position: relative;
  }
  .grid.posters {
    grid-template-columns: repeat(auto-fill, minmax(min(160px, 42vw), 1fr));
    gap: 1.75rem 1.1rem;
  }
  /* Where the gutter is narrower than the rail (phones, 601 to 700 px), the cards make room. */
  .vgrid.railed {
    margin-right: max(0px, calc(var(--rail-w) - var(--gutter)));
  }
  /* Each row is a grid of the same columns, at its own place; a cell is only its card's list item. */
  .row {
    position: absolute;
    inset-inline: 0;
    display: grid;
    grid-template-columns: inherit;
    column-gap: inherit;
  }
  .cell {
    display: contents;
  }
  /* Cards draw at once: content-visibility's estimate (MediaCard) would be measured as a row's
     height. A card the keys move to clears the header and the tab bar. */
  .vgrid .cell > :global(*) {
    content-visibility: visible;
    scroll-margin: calc(var(--bar-h) + 1rem) 0 1rem;
  }
  @media (max-width: 600px) {
    .vgrid .cell > :global(*) {
      scroll-margin-bottom: calc(var(--tabbar-h) + env(safe-area-inset-bottom));
    }
  }
</style>
