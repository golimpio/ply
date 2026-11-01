<script lang="ts">
  import { api } from '../lib/api';
  import { href } from '../lib/router.svelte';
  import { resource } from '../lib/resource.svelte';
  import { app } from '../lib/state.svelte';
  import Tile from '../components/Tile.svelte';

  const shows = resource(() => {
    void app.libraryVersion;
    return api.shows();
  });

  const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`;
</script>

<h1>Shows {#if shows.data}<span class="count">{shows.data.length}</span>{/if}</h1>

{#if shows.error}
  <p class="error">{shows.error}</p>
{:else if !shows.data}
  <p class="muted">Loading…</p>
{:else if shows.data.length === 0}
  <p class="muted">No shows yet. Episodes named like <code>Show S01E02</code> appear here after a scan.</p>
{:else}
  <div class="grid">
    {#each shows.data as s (s.show)}
      <a class="show-card" href={href.show(s.show)} title={s.show}>
        <Tile title={s.show} />
        <div class="name">{s.show}</div>
        <div class="muted small">{plural(s.seasons.length, 'season')} · {plural(s.episodeCount, 'episode')}</div>
      </a>
    {/each}
  </div>
{/if}

<style>
  .show-card {
    display: block;
    color: inherit;
  }
  .show-card:hover {
    text-decoration: none;
  }
  .show-card:hover :global(.tile) {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
  }
  .name {
    margin-top: 0.5rem;
    font-weight: 600;
    line-height: 1.25;
  }
  .small {
    font-size: 0.85rem;
  }
</style>
