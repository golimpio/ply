<script lang="ts">
  import type { ItemSummary } from '../lib/api';
  import { formatDuration, hdrLabel, resolutionLabel } from '../lib/format';
  import { byAdded, byTitle, ensureLibrary, library } from '../lib/library.svelte';
  import { filmPasses, groupByTitle, prefersPosters, type TitleGroup } from '../lib/meta';
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

  // One card per film: its files (4K and 1080p, say) are versions of it.
  const all = $derived(groupByTitle(library.items.filter((i) => i.kind === 'movie')));
  // See filmPasses: 4K/HDR when any version is; watched when any version was.
  const is4k = (i: ItemSummary) => resolutionLabel(i.width, i.height) === '4K';
  const isHdr = (i: ItemSummary) => hdrLabel(i.hdr) !== '';
  const shown = $derived(
    all.filter((g) => filmPasses(g, { is4k, isHdr, filters: view.filters })).sort((a, b) => order[view.sort](a.item, b.item)),
  );
  const posters = $derived(prefersPosters(all.map((g) => g.item)));

  const sub = (g: TitleGroup) => [
    g.item.year,
    formatDuration(g.item.durationSec),
    g.versions.length > 1 ? `${g.versions.length} versions` : '',
  ];
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
    <div class="grid" class:posters>
      {#each shown as g (g.key)}
        <MediaCard item={g.item} shape={posters ? 'poster' : 'wide'} href={href.item(g.item.id)} title={g.item.title} sub={sub(g)} />
      {/each}
    </div>
  {/if}
</div>

<style>
  .sk {
    aspect-ratio: 16 / 9;
  }
  .grid.posters {
    grid-template-columns: repeat(auto-fill, minmax(min(160px, 42vw), 1fr));
    gap: 1.75rem 1.1rem;
  }
</style>
