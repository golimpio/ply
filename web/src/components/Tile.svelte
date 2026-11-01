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
    background: linear-gradient(145deg, hsl(var(--h) 50% 36%), hsl(calc(var(--h) + 45) 45% 15%));
    color: rgb(255 255 255 / 0.82);
    font-weight: 700;
    letter-spacing: 0.04em;
    font-size: clamp(1.4rem, 5vw, 2.4rem);
    user-select: none;
  }
</style>
