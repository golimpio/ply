<script lang="ts">
  import Icon from './Icon.svelte';

  // The player's stage while the picture is on a TV (docs/AIRPLAY.md): the video element is playing
  // somewhere else, so the screen here has nothing to show but where it went. The player's own
  // controls stay above it, and they are the way out (the AirPlay button, or Close).
  let { device = '' }: { device?: string } = $props();

  // Safari never says which target its own picker chose; only a target picked from ply's list is named.
  const where = $derived(
    device ? `This video is playing on “${device}”.` : 'This video is playing on another device.',
  );
</script>

<div class="airplay-screen">
  <span class="glyph"><Icon name="airplay" /></span>
  <h2>AirPlay</h2>
  <p class="where">{where}</p>
</div>

<style>
  /* A stage, not a panel: the picture is elsewhere, so the player's own controls stay above this and
     keep every tap. Nothing here is interactive. */
  .airplay-screen {
    position: absolute;
    inset: 0;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    padding: var(--gutter-player);
    text-align: center;
    background: var(--surface-video);
    pointer-events: none;
  }
  /* Room to breathe: the glyph stands well clear of the words, and the two lines are told apart. */
  .glyph {
    color: var(--text-dim);
  }
  .glyph :global(svg) {
    width: 4.5rem;
    height: 4.5rem;
  }
  h2 {
    margin: 2rem 0 0;
    font-size: var(--fs-2xl-2);
    font-weight: var(--fw-semibold-2);
    color: var(--text);
  }
  .where {
    max-width: 34ch;
    margin: 0.85rem 0 0;
    font-size: var(--fs-base);
    line-height: var(--lh-body-2);
    color: var(--text-lede);
  }
  @media (max-width: 600px) {
    .glyph :global(svg) {
      width: 3.25rem;
      height: 3.25rem;
    }
    h2 {
      margin-top: 1.5rem;
    }
  }
</style>
