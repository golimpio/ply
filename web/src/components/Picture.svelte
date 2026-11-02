<script lang="ts">
  import { untrack } from 'svelte';
  import type { Img } from '../lib/meta';
  import Tile from './Tile.svelte';

  // An image in a fixed-ratio frame that fades in once loaded, with the
  // generated title tile standing in when there is none or it fails.
  let {
    img,
    ratio,
    label = '',
    sizes = undefined,
    eager = false,
    fit = 'cover',
  }: {
    img: Img | null;
    ratio: string;
    label?: string;
    sizes?: string;
    eager?: boolean;
    fit?: 'cover' | 'contain';
  } = $props();

  let el = $state<HTMLImageElement>();
  let loaded = $state(false);
  let failed = $state(false);

  // Reset only when the address changes: a new Img object with the same src
  // (the page swapping a cached summary for the full detail) loads nothing
  // new, so no load event would come to show it again.
  const src = $derived(img?.src ?? '');
  $effect(() => {
    void src;
    failed = false;
    // A cached image can finish before the load listener is attached.
    const node = untrack(() => el);
    loaded = !!node?.complete && node.naturalWidth > 0;
  });
  $effect(() => {
    if (el?.complete && el.naturalWidth > 0) loaded = true;
  });
</script>

<div class="picture" style:aspect-ratio={ratio} data-art>
  {#if !img?.src || failed}
    <Tile title={label} {ratio} />
  {:else}
    <img
      bind:this={el}
      src={img.src}
      srcset={img.srcset}
      {sizes}
      alt=""
      loading={eager ? 'eager' : 'lazy'}
      fetchpriority={eager ? 'high' : 'auto'}
      decoding="async"
      style:object-fit={fit}
      class:loaded
      onload={() => (loaded = true)}
      onerror={() => (failed = true)}
    />
  {/if}
</div>

<style>
  .picture {
    position: relative;
    width: 100%;
    overflow: hidden;
    border-radius: inherit;
    background: var(--bg-2);
  }
  img {
    width: 100%;
    height: 100%;
    opacity: 0;
    transition: opacity 0.35s var(--ease);
  }
  img.loaded {
    opacity: 1;
  }
  .picture :global(.tile) {
    height: 100%;
    border-radius: inherit;
  }
</style>
