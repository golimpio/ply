<script lang="ts">
  import { untrack } from 'svelte';
  import type { Img } from '../lib/meta';
  import Tile from './Tile.svelte';

  // An image in a fixed-ratio frame that fades in once loaded, with the
  // generated title tile standing in when there is none or it fails.
  // `priority` (a virtual grid's): high loads at once, before the rest; low
  // lazily, after it. `eager` is high. `hold` (a virtual grid's new row): not
  // asked for yet; once released it loads, and a later hold changes nothing.
  let {
    img,
    ratio,
    label = '',
    sizes = undefined,
    eager = false,
    priority = undefined,
    hold = false,
    fit = 'cover',
  }: {
    img: Img | null;
    ratio: string;
    label?: string;
    sizes?: string;
    eager?: boolean;
    priority?: 'high' | 'low';
    hold?: boolean;
    fit?: 'cover' | 'contain';
  } = $props();
  const urgent = $derived(eager || priority === 'high');
  let released = $state(false);
  $effect.pre(() => {
    if (!hold) released = true;
  });
  const asked = $derived(released || !hold);

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
  // Dropped before it has loaded (a virtual grid's row scrolled away): let go of the request.
  $effect(() => {
    const node = el;
    return () => {
      if (!node || node.complete) return;
      node.removeAttribute('srcset');
      node.removeAttribute('src');
    };
  });
</script>

<div class="picture" style:aspect-ratio={ratio} data-art>
  {#if !img?.src || failed}
    <Tile title={label} {ratio} />
  {:else}
    <img
      bind:this={el}
      src={asked ? img.src : undefined}
      srcset={asked ? img.srcset : undefined}
      {sizes}
      alt=""
      loading={urgent ? 'eager' : 'lazy'}
      fetchpriority={urgent ? 'high' : (priority ?? 'auto')}
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
    background: var(--surface-raised);
  }
  img {
    width: 100%;
    height: 100%;
    opacity: 0;
    transition: opacity var(--motion-image) var(--ease);
  }
  img.loaded {
    opacity: 1;
  }
  .picture :global(.tile) {
    height: 100%;
    border-radius: inherit;
  }
</style>
