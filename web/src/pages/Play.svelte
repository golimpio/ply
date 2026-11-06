<script lang="ts">
  import { api } from '../lib/api';
  import { episodeLabel } from '../lib/format';
  import { backTarget, goBack } from '../lib/router.svelte';
  import { resource } from '../lib/resource.svelte';
  import Player from '../components/Player.svelte';

  let { id, startSec = 0 }: { id: number; startSec?: number } = $props();

  const detail = resource(() => api.item(id));

  // Back where the film was opened from (its page, Home's rails), named for where it goes: Back, or
  // from a deep link the film's page (Details).
  const to = $derived(backTarget());
  function back(e: MouseEvent) {
    if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    e.preventDefault();
    goBack();
  }
</script>

<p><a href={to.hash} onclick={back}>&larr; {to.label}</a></p>

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
