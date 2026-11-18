<script lang="ts">
  import { backTarget, goBack, type BackTarget } from '../lib/router.svelte';
  import Icon from './Icon.svelte';

  // Back to the previous view, or with none (a deep link) to the page's parent, named on the button.
  // `over` floats it at the top of a page that opens with a full-bleed hero.
  let { fallback, over = false }: { fallback?: BackTarget; over?: boolean } = $props();

  const to = $derived(backTarget(fallback));

  function back(e: MouseEvent) {
    if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return; // a new tab, etc.
    e.preventDefault();
    goBack(fallback);
  }
</script>

<a class="btn back" class:over href={to.hash} aria-label={to.previous ? 'Back' : `Back to ${to.label}`} onclick={back}>
  <Icon name="left" /><span class="truncate">{to.label}</span>
</a>

<style>
  .back {
    display: inline-flex;
    align-items: center;
    min-height: 2.25rem;
    gap: var(--space-0350);
    padding: var(--space-0350) var(--space-0950) var(--space-0350) var(--space-0650);
    font-size: var(--fs-sm-3);
    font-weight: var(--fw-medium);
    width: fit-content;
    max-width: 100%;
    margin-bottom: var(--space-0850);
    background: var(--glass);
    border: 1px solid var(--glass-line);
    backdrop-filter: var(--glass-blur);
    border-radius: var(--radius-pill);
    color: var(--text);
    transition:
      background var(--motion-hover),
      border-color var(--motion-hover),
      color var(--motion-hover);
  }
  .back:hover {
    background: var(--fill-hover);
    border-color: var(--glass-line-hover);
    color: var(--text);
  }
  /* On a picture: glass, like the badges and chips set on one. */
  .over {
    position: absolute;
    top: calc(env(safe-area-inset-top) + var(--bar-h) + var(--space-0500));
    left: var(--gutter);
    z-index: var(--z-over);
    max-width: calc(100% - 2 * var(--gutter));
    margin: 0;
  }
</style>
