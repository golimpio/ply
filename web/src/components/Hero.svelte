<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { ItemSummary } from '../lib/api';
  import Art from './Art.svelte';

  // Full-bleed backdrop with the page's headline set over its lower-left
  // corner. `tall` is for the home page; detail pages use the shorter default
  // and are the landing point of the card morph (`landing`, see lib/nav).
  let {
    item,
    label = '',
    tall = false,
    landing = false,
    children,
  }: { item: ItemSummary | undefined; label?: string; tall?: boolean; landing?: boolean; children: Snippet } =
    $props();
</script>

<section class="hero" class:tall>
  <div class="backdrop" style:view-transition-name={landing ? 'hero-art' : undefined}>
    <Art {item} kind="backdrop" ratio="auto" {label} eager />
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
    z-index: -2;
    /* Dissolve into the room (and its projection light) instead of ending on an edge. */
    mask-image: linear-gradient(to bottom, #000 50%, transparent 100%);
    -webkit-mask-image: linear-gradient(to bottom, #000 50%, transparent 100%);
  }
  .backdrop :global(.art) {
    height: 100%;
    border-radius: 0;
  }
  .backdrop :global(.tile) {
    height: 100%;
  }
  .backdrop :global(.tile span) {
    display: none; /* initials read as noise behind a headline */
  }
  .scrim {
    position: absolute;
    inset: 0;
    z-index: -1;
    background:
      linear-gradient(to top, rgb(16 14 12 / 0.55) 0%, rgb(16 14 12 / 0.45) 30%, transparent 62%),
      linear-gradient(to right, rgb(16 14 12 / 0.85) 0%, rgb(16 14 12 / 0.35) 45%, transparent 75%);
  }
  .content {
    padding: calc(var(--bar-h) + 2rem) var(--gutter) clamp(1.5rem, 4vw, 3rem);
    max-width: min(46rem, 100%);
    display: grid;
    gap: 0.85rem;
  }
  .content :global(h1) {
    font-size: clamp(2.2rem, 5.2vw, 4.4rem);
    text-shadow: 0 2px 30px rgb(0 0 0 / 0.35);
  }
  @media (max-width: 600px) {
    .hero,
    .hero.tall {
      min-height: 72vh;
    }
    .scrim {
      background: linear-gradient(to top, rgb(16 14 12 / 0.6) 0%, rgb(16 14 12 / 0.55) 38%, transparent 75%);
    }
  }
</style>
