<script lang="ts">
  import { ensureLibrary, library, showsOf, type ShowEntry } from '../lib/library.svelte';
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
  const all = $derived(showsOf(library.items));
  const shown = $derived(
    view.sort === 'added' ? [...all].sort((a: ShowEntry, b: ShowEntry) => b.latestAddedAt - a.latestAddedAt) : all,
  );
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
    <div class="grid">
      {#each shown as s (s.show)}
        <MediaCard
          item={s.cover}
          href={href.show(s.show)}
          title={s.show}
          sub={[plural(s.seasons.length, 'season'), plural(s.episodeCount, 'episode')]}
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
