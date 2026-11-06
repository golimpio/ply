<script lang="ts">
  import { backTarget, goBack, type BackTarget } from '../lib/router.svelte';
  import Icon from './Icon.svelte';

  // Back to the previous view, or with none (a deep link) to the page's parent, named on the button.
  // `over` floats it at the top of a page that opens with a full-bleed hero. `pending`: the page
  // doesn't know its parent yet (an episode, before the item is in); with no previous view either,
  // the button keeps its place but is neither seen nor reachable, rather than naming the wrong page.
  let {
    fallback = undefined,
    over = false,
    pending = false,
  }: { fallback?: BackTarget; over?: boolean; pending?: boolean } = $props();

  const to = $derived(backTarget(fallback));
  const unknown = $derived(pending && !to.previous);

  function back(e: MouseEvent) {
    if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return; // a new tab, etc.
    e.preventDefault();
    goBack(fallback);
  }
</script>

{#if unknown}
  <span class="btn back pending" class:over aria-hidden="true"><Icon name="left" /><span>Back</span></span>
{:else}
  <a class="btn back" class:over href={to.hash} aria-label={to.previous ? 'Back' : `Back to ${to.label}`} onclick={back}>
    <Icon name="left" /><span>{to.label}</span>
  </a>
{/if}

<style>
  .back {
    display: flex; /* on a line of its own above the title */
    min-height: 2.25rem;
    gap: 0.3rem;
    padding: 0.3rem 0.95rem 0.3rem 0.6rem;
    font-size: var(--fs-sm-3);
    width: fit-content;
    max-width: 100%;
    margin-bottom: 0.75rem;
  }
  .back span {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .pending {
    visibility: hidden;
  }
  /* On a picture: glass, like the badges and chips set on one. */
  .over {
    position: absolute;
    top: calc(env(safe-area-inset-top) + var(--bar-h) + 0.5rem);
    left: var(--gutter);
    z-index: var(--z-over);
    max-width: calc(100% - 2 * var(--gutter));
    margin: 0;
    background: var(--glass);
    border-color: var(--glass-line);
    backdrop-filter: var(--glass-blur);
  }
  .over:hover {
    border-color: var(--glass-line-hover);
  }
</style>
