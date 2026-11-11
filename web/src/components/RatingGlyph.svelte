<script lang="ts" module>
  import type { RatingEntry } from '../lib/api';

  /** Whether a source has a glyph of its own (the others are named in text). */
  export const hasGlyph = (source: RatingEntry['source']) =>
    source === 'rt_critics' || source === 'rt_audience' || source === 'letterboxd' || source === 'imdb' || source === 'tmdb';
</script>

<script lang="ts">
  import { tomatoState } from '../lib/ratings';

  // Small drawings of our own, not the sources' logos: a tomato (fresh) or a splat (rotten) for
  // the critics, a bucket of popcorn upright or spilled for the audience, a star for Letterboxd,
  // a badge for IMDb, and an icon for TMDB.
  let { entry }: { entry: Pick<RatingEntry, 'source' | 'score100' | 'state'> } = $props();

  const state = $derived(tomatoState(entry));
</script>

{#if entry.source === 'rt_critics'}
  <svg class="glyph" viewBox="0 0 24 24" aria-hidden="true" data-glyph={state === 'rotten' || state === 'certified' ? state : 'fresh'}>
    {#if state === 'rotten'}
      <path class="splat" d="M12 3.5l1.9 4.2 4.3-1.8-1.4 4.5 4.2 1.9-4.3 1.8 1.5 4.4-4.4-1.7L12 21l-1.8-4.2-4.4 1.7 1.5-4.4-4.3-1.8 4.2-1.9-1.4-4.5 4.3 1.8z" />
    {:else}
      {#if state === 'certified'}
        <!-- Certified: a smaller tomato inside a ring of our own (not Rotten Tomatoes' badge). -->
        <circle class="ring" cx="12" cy="13" r="10" />
        <circle class="tomato" cx="12" cy="14" r="6" />
      {:else}
        <circle class="tomato" cx="12" cy="14" r="8.5" />
      {/if}
      <path class="leaf" d="M12 7.2c-1.2-1.9-3.4-2.6-5.2-2 1 1.6 2.9 2.3 5.2 2zm0 0c1.2-1.9 3.4-2.6 5.2-2-1 1.6-2.9 2.3-5.2 2zm0 0V4" />
    {/if}
  </svg>
{:else if entry.source === 'rt_audience'}
  <svg class="glyph" viewBox="0 0 24 24" aria-hidden="true" data-glyph={state === 'spilled' ? 'spilled' : 'upright'}>
    {#if state === 'spilled'}
      <!-- The bucket on its side, the popcorn out of it. -->
      <path class="bucket" d="M2 11l11-2.5v12L2 18z" />
      <circle class="kernel" cx="16.5" cy="17.5" r="2.4" />
      <circle class="kernel" cx="20.5" cy="19.5" r="2" />
      <circle class="kernel" cx="18.5" cy="13" r="2" />
    {:else}
      <circle class="kernel" cx="8.5" cy="8.5" r="2.6" />
      <circle class="kernel" cx="12" cy="6.8" r="3" />
      <circle class="kernel" cx="15.5" cy="8.5" r="2.6" />
      <path class="bucket" d="M5.5 10h13l-1.7 11H7.2z" />
    {/if}
  </svg>
{:else if entry.source === 'letterboxd'}
  <svg class="glyph" viewBox="0 0 24 24" aria-hidden="true" data-glyph="star">
    <path class="star" d="M12 4l2.4 5 5.4.6-4 3.7 1.1 5.3L12 16l-4.9 2.6 1.1-5.3-4-3.7 5.4-.6z" />
  </svg>
{:else if entry.source === 'imdb'}
  <svg class="glyph imdb" viewBox="0 0 24 24" aria-hidden="true" data-glyph="imdb">
    <polygon class="imdb-star" points="12 2 15.09 8.26 22 9.27 17 14.14 18.18 21.02 12 17.77 5.82 21.02 7 14.14 2 9.27 8.91 8.26 12 2" />
  </svg>
{:else if entry.source === 'tmdb'}
  <svg class="glyph tmdb" viewBox="0 0 24 24" aria-hidden="true" data-glyph="tmdb" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round">
    <rect x="3" y="4" width="18" height="16" rx="3" />
    <path d="M7 8h10M7 12h10M7 16h6" />
  </svg>
{/if}

<style>
  .glyph {
    width: 1.3em;
    height: 1.3em;
    flex: none;
    vertical-align: middle;
  }
  .tomato {
    fill: var(--rating-fresh);
  }
  .leaf {
    fill: var(--rating-leaf);
    stroke: var(--rating-leaf);
    stroke-width: 1.2;
    stroke-linecap: round;
  }
  .ring {
    fill: none;
    stroke: var(--rating-certified);
    stroke-width: 2;
  }
  .splat {
    fill: var(--rating-rotten);
  }
  .bucket {
    fill: var(--rating-bucket);
  }
  .kernel {
    fill: var(--rating-kernel);
  }
  .star,
  .imdb-star {
    fill: var(--rating-star);
  }

  /* Compact chips and feature scores keep glyph colors legible */
  :global(.score-chip.compact) .glyph .star,
  :global(.score-chip.compact) .glyph .imdb-star,
  :global(.feature-scores) .glyph .star,
  :global(.feature-scores) .glyph .imdb-star {
    fill: var(--rating-star);
  }
</style>
