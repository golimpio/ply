<script lang="ts">
  import type { ItemSummary } from '../lib/api';
  import { artSrc } from '../lib/ui-types';
  import Tile from './Tile.svelte';

  // Artwork for an item: the server's frame grab, falling back to the
  // generated title tile when there is none (yet) or it fails to load.
  let {
    item,
    kind = 'thumb',
    ratio = '16 / 9',
    label = '',
    eager = false,
  }: {
    item: ItemSummary | undefined;
    kind?: 'thumb' | 'backdrop';
    ratio?: string;
    label?: string;
    eager?: boolean;
  } = $props();

  const src = $derived(artSrc(item, kind));
  let img = $state<HTMLImageElement>();
  let loaded = $state(false);
  let failed = $state(false);

  $effect(() => {
    void src;
    loaded = false;
    failed = false;
  });
  // A cached image can finish before the load listener is attached.
  $effect(() => {
    if (img?.complete && img.naturalWidth > 0) loaded = true;
  });
</script>

<div class="art" style:aspect-ratio={ratio} data-art>
  {#if !src || failed}
    <Tile title={label || item?.show || item?.title || ''} {ratio} />
  {:else}
    <img
      bind:this={img}
      {src}
      alt=""
      loading={eager ? 'eager' : 'lazy'}
      fetchpriority={eager ? 'high' : 'auto'}
      decoding="async"
      class:loaded
      onload={() => (loaded = true)}
      onerror={() => (failed = true)}
    />
  {/if}
</div>

<style>
  .art {
    position: relative;
    width: 100%;
    overflow: hidden;
    border-radius: inherit;
    background: var(--bg-2);
  }
  img {
    width: 100%;
    height: 100%;
    object-fit: cover;
    opacity: 0;
    transition: opacity 0.35s var(--ease);
  }
  img.loaded {
    opacity: 1;
  }
  .art :global(.tile) {
    border-radius: inherit;
  }
</style>
