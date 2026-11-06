<script lang="ts">
  import { initials, titleHue } from '../lib/format';

  // Placeholder artwork: a gradient seeded by the title plus its initials.
  let { title, ratio = '2 / 3' }: { title: string; ratio?: string } = $props();
</script>

<div class="tile" style:aspect-ratio={ratio} style:--h={titleHue(title)} aria-hidden="true">
  <span>{initials(title)}</span>
</div>

<style>
  .tile {
    display: grid;
    place-items: center;
    width: 100%;
    border-radius: var(--radius);
    /* --h, the title's hue, is set inline; the rest of the colour is the theme's. */
    background: linear-gradient(145deg, hsl(var(--h) var(--tile-from)), hsl(calc(var(--h) + var(--tile-hue-shift)) var(--tile-to)));
    color: var(--text-on-art);
    font-weight: var(--fw-bold);
    letter-spacing: var(--tracking-widest);
    font-size: var(--fs-tile);
    user-select: none;
  }
</style>
