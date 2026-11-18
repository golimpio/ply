<script lang="ts">
  import { MIN_RATINGS } from '../lib/ratings';
  import type { Filter, LibraryView, Sort } from '../lib/router.svelte';

  // Sort (one of), filters (any of) and a minimum rating (one of) for a library page; state lives
  // in the URL hash, so a view can be bookmarked and Back undoes a change.
  let {
    view,
    sorts,
    filters = [],
    minRatings = false,
    link,
  }: {
    view: LibraryView;
    sorts: { value: Sort; label: string }[];
    filters?: { value: Filter; label: string }[];
    /** Offer Any, 60+, 70+ and 80+ (on score100: every source on one scale). */
    minRatings?: boolean;
    link: (v: Partial<LibraryView>) => string;
  } = $props();

  const toggled = (f: Filter) =>
    view.filters.includes(f) ? view.filters.filter((x) => x !== f) : [...view.filters, f];
  const mins = [0, ...MIN_RATINGS];
</script>

<div class="controls">
  <div class="chips" role="radiogroup" aria-label="Sort by">
    {#each sorts as s (s.value)}
      <a class="chip" role="radio" aria-checked={view.sort === s.value} href={link({ ...view, sort: s.value })}>{s.label}</a>
    {/each}
  </div>
  {#if filters.length}
    <div class="chips" aria-label="Show only">
      {#each filters as f (f.value)}
        <a class="chip" role="button" aria-pressed={view.filters.includes(f.value)} href={link({ ...view, filters: toggled(f.value) })}>
          {f.label}
        </a>
      {/each}
    </div>
  {/if}
  {#if minRatings}
    <div class="chips" role="radiogroup" aria-label="Minimum rating">
      {#each mins as m (m)}
        <a class="chip" role="radio" aria-checked={view.minRating === m} href={link({ ...view, minRating: m })}>{m ? `${m}+` : 'Any rating'}</a>
      {/each}
    </div>
  {/if}
</div>

<style>
  .controls {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-0750) var(--space-2000);
    margin: var(--space-1250) 0 var(--space-1750);
  }
</style>
