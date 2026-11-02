<script lang="ts">
  // A page's title over its hero: the title's logo once it has loaded, the
  // text until then, and the text for good when the logo cannot be had
  // (TMDB down, offline, lookups off). The h1 is always there for screen
  // readers; it is only hidden from sight while the logo stands in for it.
  let { title, logo = '' }: { title: string; logo?: string } = $props();

  let el = $state<HTMLImageElement>();
  let loaded = $state(false);
  let failed = $state(false);

  const src = $derived(logo);
  $effect(() => {
    void src;
    failed = false;
    loaded = false;
  });
  // A cached logo can finish before the load listener is attached.
  $effect(() => {
    if (el?.complete && el.naturalWidth > 0) loaded = true;
  });
  const showLogo = $derived(!!src && loaded && !failed);
</script>

<h1 class:sr-only={showLogo}>{title}</h1>
{#if src && !failed}
  <img
    bind:this={el}
    class="logo"
    class:pending={!loaded}
    {src}
    alt=""
    onload={() => (loaded = true)}
    onerror={() => (failed = true)}
  />
{/if}

<style>
  /* Loaded out of sight: the h1 holds the place until the logo is ready. */
  img.pending {
    position: absolute;
    width: 1px;
    height: 1px;
    opacity: 0;
    pointer-events: none;
  }
</style>
