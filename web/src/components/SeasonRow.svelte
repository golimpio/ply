<script lang="ts" module>
  import type { Img } from '../lib/meta';
  import type { WatchBadge as Watch } from '../lib/watched';

  /** A season's card. `fallback`: the picture is the show's poster, and the card's name is over it. */
  export interface SeasonCard {
    season: number;
    name: string;
    poster: Img | null;
    fallback: boolean;
    episodes: number;
    badge: Watch | null;
  }
</script>

<script lang="ts">
  import { inView, scrollToShow } from '../lib/seasons';
  import Picture from './Picture.svelte';
  import WatchBadge from './WatchBadge.svelte';

  // A show's seasons as a row of posters that scrolls sideways (with snap points) and never wraps.
  // Several seasons are tabs (the WAI-ARIA tabs pattern, as components/Tabs): the row is one stop
  // of the Tab key, the arrow keys, Home and End select another season, as a click does, and the
  // episodes below are the selected one's tabpanel (`panel`). One season is a card on its own.
  // The posters in view load first (fetchpriority high), the rest lazily; none is asked for until
  // `ready` (the show's details, with the seasons' own posters, are in) and the row knows which
  // cards it shows.
  let {
    seasons,
    selected,
    onselect,
    panel,
    label,
    ready,
  }: {
    seasons: SeasonCard[];
    selected: number;
    onselect: (season: number) => void;
    /** The id of the tabpanel the tabs control. */
    panel: string;
    /** The show's name: the generated tile of a season with no picture at all. */
    label: string;
    ready: boolean;
  } = $props();

  // Keep in step with --season-w and --season-w-narrow (tokens.css) and the media query below.
  const SIZES = '(max-width: 600px) 7rem, 8.5rem';
  const tabId = (n: number) => `season-tab-${n}`;
  const plural = (n: number) => `${n} episode${n === 1 ? '' : 's'}`;
  const tabs = $derived(seasons.length > 1);

  let row = $state<HTMLElement>();
  let visible = $state<Set<number>>();
  // The seasons the measure was taken for: another list (the details' order, a rescan) measures again.
  let measured = '';
  const order = $derived(seasons.map((s) => s.season).join());

  function cards(): HTMLElement[] {
    return row ? [...row.querySelectorAll<HTMLElement>(':scope > .season')] : [];
  }

  /** Scroll the row (only the row, not the page) so that the selected card is wholly in view. */
  function reveal() {
    const el = row?.querySelector<HTMLElement>(`#${tabId(selected)}`);
    if (!row || !el) return;
    row.scrollLeft = scrollToShow(el.offsetLeft - row.offsetLeft, el.offsetWidth, row.scrollLeft, row.clientWidth);
  }

  $effect(() => {
    void selected;
    reveal();
    // Measured with the selected card in view: what is on screen when the pictures are asked for.
    if (row && measured !== order) {
      measured = order;
      const boxes = cards().map((c) => ({ left: c.offsetLeft - row!.offsetLeft, width: c.offsetWidth }));
      visible = inView(boxes, row.scrollLeft, row.clientWidth);
    }
  });

  function choose(n: number, focus: boolean) {
    if (n !== selected) onselect(n);
    if (focus) document.getElementById(tabId(n))?.focus({ preventScroll: true });
  }

  function onkeydown(e: KeyboardEvent) {
    const n = seasons.length;
    const i = Math.max(0, seasons.findIndex((s) => s.season === selected));
    let j: number;
    if (e.key === 'ArrowRight') j = (i + 1) % n;
    else if (e.key === 'ArrowLeft') j = (i - 1 + n) % n;
    else if (e.key === 'Home') j = 0;
    else if (e.key === 'End') j = n - 1;
    else return;
    e.preventDefault();
    choose(seasons[j].season, true);
  }
</script>

{#snippet card(s: SeasonCard, i: number)}
  <div class="frame">
    <Picture
      img={s.poster}
      ratio="2 / 3"
      sizes={SIZES}
      {label}
      priority={visible?.has(i) ? 'high' : 'low'}
      hold={!ready || !visible}
    />
    {#if s.fallback}<span class="over" aria-hidden="true">{s.name}</span>{/if}
    <!-- Its words are said after the name (below): "Season 1, 3 episodes, Watched". -->
    {#if s.badge}<span class="corner" aria-hidden="true"><WatchBadge badge={s.badge} /></span>{/if}
  </div>
  <span class="name">{s.name}</span>
  <span class="eps">{plural(s.episodes)}</span>
  {#if s.badge}<span class="sr-only">{s.badge.label}</span>{/if}
{/snippet}

{#if tabs}
  <!-- The tabs take the focus, one at a time; the row only hears their keys. -->
  <!-- svelte-ignore a11y_interactive_supports_focus -->
  <div class="row" role="tablist" aria-label="Seasons" aria-orientation="horizontal" bind:this={row} {onkeydown}>
    {#each seasons as s, i (s.season)}
      {@const on = s.season === selected}
      <button
        type="button"
        class="season"
        role="tab"
        id={tabId(s.season)}
        aria-selected={on}
        aria-controls={on ? panel : undefined}
        tabindex={on ? 0 : -1}
        onclick={() => choose(s.season, false)}
      >
        {@render card(s, i)}
      </button>
    {/each}
  </div>
{:else}
  <div class="row solo" bind:this={row}>
    {#each seasons as s, i (s.season)}
      <div class="season" id={tabId(s.season)}>{@render card(s, i)}</div>
    {/each}
  </div>
{/if}

<style>
  .row {
    display: flex;
    flex-wrap: nowrap;
    gap: 1rem;
    min-width: 0;
    overflow-x: auto;
    overscroll-behavior-x: contain;
    scroll-snap-type: x mandatory;
    scrollbar-width: none;
    /* Room for the selected ring and the focus ring, which the scroll box would clip. */
    margin: calc(-1 * var(--focus-offset) - 4px);
    padding: calc(var(--focus-offset) + 4px);
    scroll-padding-inline: calc(var(--focus-offset) + 4px);
  }
  .row::-webkit-scrollbar {
    display: none;
  }
  .season {
    position: relative; /* holds its sr-only words */
    flex: none;
    display: grid;
    gap: 0.15rem;
    align-content: start;
    width: var(--season-w);
    padding: 0;
    font: inherit;
    text-align: start;
    color: var(--text-muted);
    background: none;
    border: 0;
    scroll-snap-align: start;
  }
  button.season {
    cursor: pointer;
  }
  .frame {
    position: relative;
    margin-bottom: 0.45rem;
    border-radius: var(--radius);
    box-shadow: var(--ring-hairline);
    transition: box-shadow var(--motion-hover);
  }
  button.season:hover .frame {
    box-shadow: var(--ring-hover);
  }
  .season[aria-selected='true'] {
    color: var(--text);
  }
  .season[aria-selected='true'] .frame {
    box-shadow: var(--ring-selected);
  }
  .season:focus-visible {
    outline: none;
  }
  .season:focus-visible .frame {
    outline: var(--focus-ring);
    outline-offset: var(--focus-offset);
  }
  .over {
    position: absolute;
    inset: auto 0 0;
    padding: 1.6rem 0.5rem 0.5rem;
    font-weight: var(--fw-bold);
    font-size: var(--fs-sm);
    line-height: var(--lh-tight);
    color: var(--text);
    background: linear-gradient(to top, var(--scrim-season-label), transparent);
    border-radius: 0 0 var(--radius) var(--radius);
  }
  .corner {
    position: absolute;
    top: 6px;
    right: 6px;
  }
  .name {
    font-weight: var(--fw-semibold-2);
    line-height: var(--lh-snug);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .season[aria-selected='true'] .name {
    color: var(--text);
  }
  /* One season: the page names it beside the card. */
  .solo .name,
  .solo .eps {
    display: none;
  }
  .solo .frame {
    margin-bottom: 0;
  }
  .eps {
    font-size: var(--fs-sm);
    font-variant-numeric: tabular-nums;
  }
  @media (max-width: 600px) {
    .season {
      width: var(--season-w-narrow);
    }
  }
</style>
