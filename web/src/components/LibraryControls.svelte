<script lang="ts">
  import type { Filter, LibraryView, Sort } from '../lib/router.svelte';

  // Sort (one of) and filters (any of) for a library page; state lives in the
  // URL hash, so a view can be bookmarked and Back undoes a change.
  let {
    view,
    sorts,
    filters = [],
    link,
  }: {
    view: LibraryView;
    sorts: { value: Sort; label: string }[];
    filters?: { value: Filter; label: string }[];
    link: (v: Partial<LibraryView>) => string;
  } = $props();

  const toggled = (f: Filter) =>
    view.filters.includes(f) ? view.filters.filter((x) => x !== f) : [...view.filters, f];
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
</div>

<style>
  .controls {
    display: flex;
    flex-wrap: wrap;
    gap: 0.75rem 2rem;
    margin: 1.25rem 0 1.75rem;
  }
  .chip {
    display: inline-flex;
    align-items: center;
  }
</style>
