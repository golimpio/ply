<script lang="ts">
  import { api } from '../lib/api';
  import { resource } from '../lib/resource.svelte';
  import { app } from '../lib/state.svelte';
  import ItemCard from '../components/ItemCard.svelte';

  const movies = resource(() => {
    void app.libraryVersion; // refetch after a rescan
    return api.items({ kind: 'movie' });
  });
</script>

<h1>Movies {#if movies.data}<span class="count">{movies.data.length}</span>{/if}</h1>

{#if movies.error}
  <p class="error">{movies.error}</p>
{:else if !movies.data}
  <p class="muted">Loading…</p>
{:else if movies.data.length === 0}
  <p class="muted">No movies yet. Point ply at a media folder (<code>-media</code>) and press Rescan.</p>
{:else}
  <div class="grid">
    {#each movies.data as item (item.id)}
      <ItemCard {item} />
    {/each}
  </div>
{/if}
