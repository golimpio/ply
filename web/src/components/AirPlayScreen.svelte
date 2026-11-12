<script lang="ts">
  import Icon from './Icon.svelte';

  // The player's stage while the picture is on a TV (docs/AIRPLAY.md): the video element is playing
  // somewhere else, so the screen here has nothing to show but where it went.
  let { device = '', onStop }: { device?: string; onStop?: () => void } = $props();

  // Safari never says which target its own picker chose; only a target picked from ply's list is named.
  const where = $derived(
    device ? `This video is playing on “${device}”.` : 'This video is playing on another device.',
  );
</script>

<div class="airplay-screen">
  <span class="glyph"><Icon name="airplay" /></span>
  <h2>AirPlay</h2>
  <p class="where">{where}</p>
  {#if onStop}
    <button class="btn stop" onclick={onStop}>Stop AirPlay</button>
  {/if}
</div>

<style>
  /* A stage, not a panel: the picture is elsewhere, so the player's own controls stay above this and
     keep their taps. Only the button takes a pointer. */
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
    width: 3.25rem;
    height: 3.25rem;
  }
  h2 {
    margin: 1.75rem 0 0;
    font-size: var(--fs-2xl);
    font-weight: var(--fw-semibold-2);
    color: var(--text);
  }
  .where {
    max-width: 34ch;
    margin: 0.75rem 0 0;
    font-size: var(--fs-md-2);
    line-height: var(--lh-body-2);
    color: var(--text-lede);
  }
  .stop {
    margin-top: 1.9rem;
    pointer-events: auto;
  }
  @media (max-width: 600px) {
    .glyph :global(svg) {
      width: 2.75rem;
      height: 2.75rem;
    }
  }
</style>
