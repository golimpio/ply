<script lang="ts">
  import { api } from '../lib/api';
  import { episodeLabel } from '../lib/format';
  import { href } from '../lib/router.svelte';
  import { resource } from '../lib/resource.svelte';
  import Player from '../components/Player.svelte';

  let { id, startSec = 0 }: { id: number; startSec?: number } = $props();

  const detail = resource(() => api.item(id));
</script>

<p><a href={href.item(id)}>&larr; Details</a></p>

{#if detail.error}
  <p class="error">{detail.error}</p>
{:else if !detail.data}
  <p class="muted">Loading…</p>
{:else}
  {@const item = detail.data}
  <h1>
    {#if item.kind === 'episode'}
      <span class="muted">{item.show} · {episodeLabel(item.season, item.episode)}</span><br />
      {item.episodeTitle || item.title}
    {:else}
      {item.title}{#if item.year}&nbsp;<span class="muted">({item.year})</span>{/if}
    {/if}
  </h1>
  <Player {item} {startSec} />
{/if}
