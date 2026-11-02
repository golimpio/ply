<script lang="ts">
  import { ensureLibrary, library, showsOf, type ShowEntry } from '../lib/library.svelte';
  import { posterImg, prefersPosters, showWideImg } from '../lib/meta';
  import { href, type LibraryView } from '../lib/router.svelte';
  import { app } from '../lib/state.svelte';
  import LibraryControls from '../components/LibraryControls.svelte';
  import MediaCard from '../components/MediaCard.svelte';

  let { view }: { view: LibraryView } = $props();

  $effect(() => {
    void app.libraryVersion;
    void ensureLibrary();
  });

  const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`;
  const all = $derived(showsOf(library.items, library.shows));
  const shown = $derived(
    view.sort === 'added' ? [...all].sort((a: ShowEntry, b: ShowEntry) => b.latestAddedAt - a.latestAddedAt) : all,
  );
  const posters = $derived(prefersPosters(all.map((s) => s.meta ?? {})));
</script>

<div class="page">
  <h1>Shows{#if library.loaded}<span class="count">{all.length}</span>{/if}</h1>

  <LibraryControls
    {view}
    link={href.showsWith}
    sorts={[
      { value: 'title', label: 'A to Z' },
      { value: 'added', label: 'Recently updated' },
    ]}
  />

  {#if library.error && !library.loaded}
    <p class="error">{library.error}</p>
  {:else if !library.loaded}
    <div class="grid" aria-busy="true">
      {#each Array(8) as _, i (i)}<div class="skeleton sk"></div>{/each}
    </div>
  {:else if all.length === 0}
    <p class="muted">No shows yet. Episodes named like <code>Show S01E02</code> appear here after a scan.</p>
  {:else}
    <div class="grid" class:posters>
      {#each shown as s (s.show)}
        <MediaCard
          shape={posters ? 'poster' : 'wide'}
          img={posters ? posterImg(s.meta) : showWideImg(s.meta, s.cover)}
          href={href.show(s.show)}
          title={s.show}
          label={s.show}
          sub={[s.meta?.year, plural(s.seasons.length, 'season'), plural(s.episodeCount, 'episode')]}
        />
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
