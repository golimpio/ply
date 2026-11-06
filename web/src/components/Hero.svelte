<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { ItemSummary } from '../lib/api';
  import type { Img } from '../lib/meta';
  import Art from './Art.svelte';

  // Full-bleed backdrop with the page's headline set over its lower-left
  // corner. `tall` is for the home page; detail pages use the shorter default
  // and are the landing point of the card morph (`landing`, see lib/nav).
  // `img` replaces the item's own backdrop (a show's, a collection's).
  let {
    item,
    img = undefined,
    label = '',
    tall = false,
    landing = false,
    children,
  }: {
    item: ItemSummary | undefined;
    img?: Img | null;
    label?: string;
    tall?: boolean;
    landing?: boolean;
    children: Snippet;
  } = $props();
</script>

<section class="hero" class:tall>
  <div class="backdrop" style:view-transition-name={landing ? 'hero-art' : undefined}>
    <Art {item} {img} kind="backdrop" ratio="auto" {label} eager />
  </div>
  <div class="scrim"></div>
  <div class="content">
    {@render children()}
  </div>
</section>

<style>
  .hero {
    position: relative;
    display: grid;
    align-items: end;
    min-height: min(68vh, 56vw);
    isolation: isolate;
  }
  .hero.tall {
    min-height: min(82vh, 64vw);
  }
  .backdrop {
    position: absolute;
    inset: 0;
    z-index: var(--z-backdrop);
    /* Dissolve into the room (and its projection light) instead of ending on an edge. */
    mask-image: linear-gradient(to bottom, var(--mask-opaque) 50%, transparent 100%);
    -webkit-mask-image: linear-gradient(to bottom, var(--mask-opaque) 50%, transparent 100%);
  }
  .backdrop :global(.picture) {
    height: 100%;
    border-radius: 0;
  }
  .backdrop :global(.picture img) {
    object-position: 50% 20%; /* faces sit high in a backdrop */
  }
  .backdrop :global(.tile span) {
    display: none; /* initials read as noise behind a headline */
  }
  .scrim {
    position: absolute;
    inset: 0;
    z-index: var(--z-under);
    /* Written top to bottom on purpose; don't turn it back into "to top, ... 0%, 30%, 62%". With
       literal colours the minifier rewrote that into this form (stops 38%, 70%, 100%), and Chrome
       rasterises the two forms one row apart (a 1-level difference around y=261 at 1280 px). With
       var() colours the minifier leaves a gradient alone, so this is the form that matches what
       was on screen before the tokens. The same holds for the narrow scrim below. */
    background:
      linear-gradient(transparent 38%, var(--scrim-hero-mid) 70%, var(--scrim-hero-bottom) 100%),
      linear-gradient(90deg, var(--scrim-hero-side) 0%, var(--scrim-hero-side-mid) 45%, transparent 75%);
  }
  .content {
    padding: calc(var(--bar-h) + 2rem) var(--gutter) clamp(1.5rem, 4vw, 3rem);
    max-width: min(46rem, 100%);
    display: grid;
    gap: 0.85rem;
  }
  .content :global(h1) {
    font-size: var(--fs-display);
    text-shadow: var(--text-shadow-hero);
  }
  /* A title's logo stands in for its h1 (the h1 stays, for screen readers). */
  .content :global(.logo) {
    display: block;
    max-width: min(28rem, 80%);
    max-height: clamp(5rem, 14vw, 9rem);
    width: auto;
    height: auto;
    object-fit: contain;
    object-position: left bottom;
    filter: var(--drop-shadow-logo);
  }
  @media (max-width: 600px) {
    .hero,
    .hero.tall {
      min-height: 72vh;
    }
    .scrim {
      background: linear-gradient(transparent 25%, var(--scrim-hero-narrow-mid) 62%, var(--scrim-hero-narrow) 100%);
    }
  }
</style>
