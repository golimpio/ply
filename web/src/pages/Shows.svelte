<script lang="ts">
  import { byName, railLetters } from '../lib/az';
  import { ensureLibrary, library, showsOf, type ShowEntry } from '../lib/library.svelte';
  import { posterImg, prefersPosters, showWideImg } from '../lib/meta';
  import { passesMinRating, ratingScore } from '../lib/ratings';
  import { href, type LibraryView } from '../lib/router.svelte';
  import { app } from '../lib/state.svelte';
  import { countsBadge } from '../lib/watched';
  import JumpRail from '../components/JumpRail.svelte';
  import LibraryControls from '../components/LibraryControls.svelte';
  import MediaCard from '../components/MediaCard.svelte';
  import VirtualGrid from '../components/VirtualGrid.svelte';

  let { view }: { view: LibraryView } = $props();
  let grid = $state<VirtualGrid<ShowEntry>>();

  $effect(() => {
    void app.libraryVersion;
    void ensureLibrary();
  });

  const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`;
  const nameOf = (s: ShowEntry) => s.meta?.sortTitle || s.show;
  // By name as the server lists names (lib/az): its sort title, year, then the show's own name.
  const all = $derived(
    showsOf(library.items, library.shows)
      .map((s) => ({ s, n: { title: s.show, sortTitle: s.meta?.sortTitle, year: s.meta?.year, id: s.show } }))
      .sort((a, b) => byName(a.n, b.n))
      .map((x) => x.s),
  );
  const order: Partial<Record<LibraryView['sort'], (a: ShowEntry, b: ShowEntry) => number>> = {
    added: (a, b) => b.latestAddedAt - a.latestAddedAt,
    // score100, so that sources on other scales compare; no rating last. Ties keep the list's
    // order, which is by name (Array.prototype.sort is stable).
    rating: (a, b) => ratingScore(b.meta) - ratingScore(a.meta),
  };
  const shown = $derived.by(() => {
    const list = all.filter((s) => passesMinRating(s.meta, view.minRating));
    const by = order[view.sort];
    return by ? list.sort(by) : list;
  });
  const posters = $derived(prefersPosters(all.map((s) => s.meta ?? {})));
  const letters = $derived(view.sort === 'title' ? railLetters(shown.map(nameOf)) : []);
</script>

<div class="page">
  <h1>Shows{#if library.loaded}<span class="count">{all.length}</span>{/if}</h1>

  <LibraryControls
    {view}
    link={href.showsWith}
    sorts={[
      { value: 'title', label: 'A to Z' },
      { value: 'added', label: 'Recently updated' },
      { value: 'rating', label: 'Rating' },
    ]}
    minRatings
  />

  {#if library.error && !library.loaded}
    <p class="error">{library.error}</p>
  {:else if !library.loaded}
    <div class="grid" aria-busy="true">
      {#each Array(8) as _, i (i)}<div class="skeleton sk"></div>{/each}
    </div>
  {:else if all.length === 0}
    <p class="muted">No shows yet. Episodes named like <code>Show S01E02</code> appear here after a scan.</p>
  {:else if shown.length === 0}
    <p class="muted">No shows match these filters. <a href={href.showsWith({ sort: view.sort })}>Clear filters</a></p>
  {:else}
    <JumpRail entries={letters} onjump={(i, focus, moving) => grid?.scrollToIndex(i, focus, moving)} />
    <VirtualGrid bind:this={grid} items={shown} key={(s) => s.show} {posters} railed={letters.length > 0} ratio={posters ? 3 / 2 : 9 / 16}>
      {#snippet card(s, _i, priority, sizes, hold)}
        <MediaCard
          shape={posters ? 'poster' : 'wide'}
          img={posters ? posterImg(s.meta) : showWideImg(s.meta, s.cover)}
          href={href.show(s.show)}
          title={s.show}
          label={s.show}
          sub={[s.meta?.year, plural(s.seasons.length, 'season'), plural(s.counts.episodeCount, 'episode')]}
          rating={s.meta?.rating}
          watch={countsBadge(s.counts)}
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
