<script lang="ts">
  import { api } from '../lib/api';
  import { goBack } from '../lib/router.svelte';
  import { resource } from '../lib/resource.svelte';
  import Icon from '../components/Icon.svelte';
  import Player from '../components/Player.svelte';

  let { id, startSec = 0, minimized = false, wireless = $bindable(false), onStop }: {
    id: number;
    startSec?: number;
    minimized?: boolean;
    wireless?: boolean;
    onStop?: () => void;
  } = $props();

  const detail = resource(() => api.item(id));

  // Until the player is there (the item loading, or failing to), Escape and ✕ leave as the
  // player's do: a fetch that never answers must not leave a home-screen app with no way out.
  function onKey(e: KeyboardEvent) {
    if (minimized || (detail.data && !detail.error)) return; // the player's own
    if (e.key !== 'Escape' || e.metaKey || e.ctrlKey || e.altKey || e.repeat) return;
    if (document.querySelector('dialog[open]')) return;
    e.preventDefault();
    goBack();
  }
</script>

<svelte:window onkeydown={onKey} />

<!--
  The player fills the window (App leaves the header and the tab bar out): the picture on black,
  the title and the close button in its overlay. Close and Escape go back where it was opened from,
  or to the film's page.
-->
<div class="play" class:minimized>
  {#if detail.error || !detail.data}
    <div class="status" role={detail.error ? 'alert' : 'status'}>
      {#if detail.error}
        <p class="error">{detail.error}</p>
      {:else}
        <p class="muted">Loading…</p>
      {/if}
    </div>
    <button class="close" onclick={() => goBack()} aria-label="Close player"><Icon name="close" /></button>
  {:else}
    <Player item={detail.data} {startSec} active={!minimized} bind:wireless {onStop} />
  {/if}
</div>

<style>
  .play {
    position: fixed;
    inset: 0;
    z-index: var(--z-player);
    background: var(--surface-video);
  }
  .play.minimized {
    position: static;
    background: none;
  }
  .status {
    height: 100%;
    display: grid;
    place-content: center;
    padding: var(--gutter);
    text-align: center;
  }
  .status p {
    margin: 0;
  }
  /* Where the player's own ✕ is (PlayerControls: the top bar's padding, a 2.75rem control). */
  .close {
    position: absolute;
    top: max(0.75rem, env(safe-area-inset-top));
    right: max(var(--gutter-player), env(safe-area-inset-right));
    display: grid;
    place-items: center;
    width: 2.75rem;
    height: 2.75rem;
    padding: 0;
    color: var(--text);
    background: none;
    border: 0;
    border-radius: var(--radius-round);
    cursor: pointer;
  }
  .close:hover {
    background: var(--fill-strong);
  }
  .close :global(svg) {
    width: 1.5rem;
    height: 1.5rem;
  }
</style>
