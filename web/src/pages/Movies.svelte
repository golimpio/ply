<script lang="ts">
  import type { ItemSummary } from '../lib/api';
  import { formatDuration, hdrLabel, resolutionLabel } from '../lib/format';
  import { byAdded, byTitle, ensureLibrary, library } from '../lib/library.svelte';
  import { href, type LibraryView } from '../lib/router.svelte';
  import { app } from '../lib/state.svelte';
  import LibraryControls from '../components/LibraryControls.svelte';
  import MediaCard from '../components/MediaCard.svelte';

  let { view }: { view: LibraryView } = $props();

  $effect(() => {
    void app.libraryVersion;
    void ensureLibrary();
  });

  const order: Record<LibraryView['sort'], (a: ItemSummary, b: ItemSummary) => number> = {
    title: byTitle,
    added: byAdded,
    year: (a, b) => (b.year ?? 0) - (a.year ?? 0) || byTitle(a, b),
    duration: (a, b) => b.durationSec - a.durationSec,
  };

  const all = $derived(library.items.filter((i) => i.kind === 'movie'));
  const shown = $derived(
    all
      .filter(
        (i) =>
          (!view.filters.includes('4k') || resolutionLabel(i.width, i.height) === '4K') &&
          (!view.filters.includes('hdr') || hdrLabel(i.hdr) !== '') &&
          (!view.filters.includes('unwatched') || !i.progress?.watched),
      )
      .sort(order[view.sort]),
  );
</script>

<div class="page">
  <h1>Movies{#if library.loaded}<span class="count">{all.length}</span>{/if}</h1>

  <LibraryControls
    {view}
    link={href.moviesWith}
    sorts={[
      { value: 'title', label: 'A to Z' },
      { value: 'added', label: 'Recently added' },
      { value: 'year', label: 'Newest' },
      { value: 'duration', label: 'Longest' },
    ]}
    filters={[
      { value: '4k', label: '4K' },
      { value: 'hdr', label: 'HDR' },
      { value: 'unwatched', label: 'Unwatched' },
    ]}
  />

  {#if library.error && !library.loaded}
    <p class="error">{library.error}</p>
  {:else if !library.loaded}
    <div class="grid" aria-busy="true">
      {#each Array(8) as _, i (i)}<div class="skeleton sk"></div>{/each}
    </div>
  {:else if all.length === 0}
    <p class="muted">No movies yet. Point ply at a folder with <code>-media</code> and rescan.</p>
  {:else if shown.length === 0}
    <p class="muted">No movies match these filters. <a href={href.moviesWith({ sort: view.sort })}>Clear filters</a></p>
  {:else}
    <div class="grid">
      {#each shown as item (item.id)}
        <MediaCard
          {item}
          href={href.item(item.id)}
          title={item.title}
          sub={[item.year, formatDuration(item.durationSec)]}
        />
      {/each}
    </div>
  {/if}
</div>

<style>
  .sk {
    aspect-ratio: 16 / 9;
  }
</style>
