<script lang="ts">
  import type { CollectionCard } from '../lib/api';
  import { railLetters } from '../lib/az';
  import { plural } from '../lib/format';
  import { collections, ensureCollections } from '../lib/library.svelte';
  import { backdropOf, posterImg, prefersPosters } from '../lib/meta';
  import { href } from '../lib/router.svelte';
  import { app } from '../lib/state.svelte';
  import GridSkeleton from '../components/GridSkeleton.svelte';
  import JumpRail from '../components/JumpRail.svelte';
  import MediaCard from '../components/MediaCard.svelte';
  import VirtualGrid from '../components/VirtualGrid.svelte';

  let grid = $state<VirtualGrid<CollectionCard>>();

  $effect(() => {
    void app.libraryVersion;
    void ensureCollections();
  });

  // The server lists them by name, the order the cards read and the rail jumps in.
  const all = $derived([...collections.rows]);
  const posters = $derived(prefersPosters(all));
  const letters = $derived(railLetters(all.map((c) => c.name)));
</script>

<div class="page">
  <h1>Collections{#if collections.loaded}<span class="count">{all.length}</span>{/if}</h1>

  {#if collections.error && !collections.loaded}
    <p class="error">{collections.error}</p>
  {:else if !collections.loaded}
    <GridSkeleton />
  {:else if all.length === 0}
    <p class="muted">
      No collections yet. A film in a set — from TMDB, or an NFO's <code>&lt;set&gt;</code> — brings its
      collection here.
    </p>
  {:else}
    <JumpRail entries={letters} onjump={(i, focus, moving) => grid?.scrollToIndex(i, focus, moving)} />
    <VirtualGrid bind:this={grid} items={all} key={(c) => c.id} {posters} railed={letters.length > 0} ratio={posters ? 3 / 2 : 9 / 16}>
      {#snippet card(c, _i, priority, sizes, hold)}
        <MediaCard
          shape={posters ? 'poster' : 'wide'}
          img={posters ? posterImg(c) : backdropOf(c) ?? posterImg(c)}
          href={href.collection(c.id)}
          title={c.name}
          label={c.name}
          sub={[plural(c.count, 'title')]}
          {priority}
          {sizes}
          {hold}
        />
      {/snippet}
    </VirtualGrid>
  {/if}
</div>
