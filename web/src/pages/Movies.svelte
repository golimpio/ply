<script lang="ts">
  import type { MovieCard } from '../lib/api';
  import { byName, railLetters } from '../lib/az';
  import { formatDuration, hdrLabel, resolutionLabel } from '../lib/format';
  import { byAdded, ensureMovies, movies } from '../lib/library.svelte';
  import { filmPasses, groupByTitle, prefersPosters, type TitleGroup } from '../lib/meta';
  import { passesMinRating, ratingScore } from '../lib/ratings';
  import { href, type LibraryView } from '../lib/router.svelte';
  import { app } from '../lib/state.svelte';
  import JumpRail from '../components/JumpRail.svelte';
  import LibraryControls from '../components/LibraryControls.svelte';
  import MediaCard from '../components/MediaCard.svelte';
  import VirtualGrid from '../components/VirtualGrid.svelte';

  let { view }: { view: LibraryView } = $props();
  let grid = $state<VirtualGrid<TitleGroup<MovieCard>>>();

  $effect(() => {
    void app.libraryVersion;
    void ensureMovies();
  });

  const order: Record<LibraryView['sort'], (a: MovieCard, b: MovieCard) => number> = {
    title: byName, // the server's order of names (lib/az)
    added: byAdded,
    year: (a, b) => (b.year ?? 0) - (a.year ?? 0) || byName(a, b),
    duration: (a, b) => b.durationSec - a.durationSec,
    // score100, so that sources on other scales compare; no rating last.
    rating: (a, b) => ratingScore(b) - ratingScore(a) || byName(a, b),
  };

  // One card per film: its files (4K and 1080p, say) are versions of it.
  const all = $derived(groupByTitle(movies.rows.slice()));
  // See filmPasses: 4K or HDR when any version is, both when one version is; watched when any version was.
  const is4k = (i: MovieCard) => resolutionLabel(i.width, i.height) === '4K';
  const isHdr = (i: MovieCard) => hdrLabel(i.hdr) !== '';
  const shown = $derived(
    all
      .filter((g) => filmPasses(g, { is4k, isHdr, filters: view.filters }) && passesMinRating(g.item, view.minRating))
      .sort((a, b) => order[view.sort](a.item, b.item)),
  );
  const posters = $derived(prefersPosters(all.map((g) => g.item)));
  const letters = $derived(view.sort === 'title' ? railLetters(shown.map((g) => g.item.sortTitle || g.item.title)) : []);

  const sub = (g: TitleGroup<MovieCard>) => [
    g.item.year,
    formatDuration(g.item.durationSec),
    g.versions.length > 1 ? `${g.versions.length} versions` : '',
  ];
</script>

<div class="page">
  <h1>Films{#if movies.loaded}<span class="count">{all.length}</span>{/if}</h1>

  <LibraryControls
    {view}
    link={href.moviesWith}
    sorts={[
      { value: 'title', label: 'A to Z' },
      { value: 'added', label: 'Recently added' },
      { value: 'year', label: 'Newest' },
      { value: 'duration', label: 'Longest' },
      { value: 'rating', label: 'Rating' },
    ]}
    minRatings
    filters={[
      { value: '4k', label: '4K' },
      { value: 'hdr', label: 'HDR' },
      { value: 'unwatched', label: 'Unwatched' },
    ]}
  />

  {#if movies.error && !movies.loaded}
    <p class="error">{movies.error}</p>
  {:else if !movies.loaded}
    <div class="grid" aria-busy="true">
      {#each Array(8) as _, i (i)}<div class="skeleton sk"></div>{/each}
    </div>
  {:else if all.length === 0}
    <p class="muted">No movies yet. Point ply at a folder with <code>-media</code> and rescan.</p>
  {:else if shown.length === 0}
    <p class="muted">No movies match these filters. <a href={href.moviesWith({ sort: view.sort })}>Clear filters</a></p>
  {:else}
    <JumpRail entries={letters} onjump={(i, focus, moving) => grid?.scrollToIndex(i, focus, moving)} />
    <VirtualGrid bind:this={grid} items={shown} key={(g) => g.key} {posters} railed={letters.length > 0} ratio={posters ? 3 / 2 : 9 / 16}>
      {#snippet card(g, _i, priority, sizes, hold)}
        <MediaCard
          item={g.item}
          shape={posters ? 'poster' : 'wide'}
          href={href.item(g.item.id)}
          title={g.item.title}
          sub={sub(g)}
          rating={g.item.rating}
          {priority}
          {sizes}
          {hold}
        />
      {/snippet}
    </VirtualGrid>
  {/if}
</div>

<style>
  .sk {
    aspect-ratio: 16 / 9;
  }
</style>
