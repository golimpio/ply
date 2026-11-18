<script lang="ts">
  import Icon from './Icon.svelte';

  // A "more actions" button and the short list it opens. Arrow keys move
  // through the items, Escape or a click elsewhere closes it.
  let { label = 'More actions', items }: { label?: string; items: { label: string; run: () => void; hidden?: boolean }[] } =
    $props();

  let open = $state(false);
  let root = $state<HTMLDivElement>();
  let button = $state<HTMLButtonElement>();
  const shown = $derived(items.filter((i) => !i.hidden));

  function buttons(): HTMLButtonElement[] {
    return root ? [...root.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')] : [];
  }

  function toggle() {
    open = !open;
    if (open) queueMicrotask(() => buttons()[0]?.focus());
  }

  function close(refocus = true) {
    open = false;
    if (refocus) button?.focus();
  }

  function onkeydown(e: KeyboardEvent) {
    if (!open) return;
    const list = buttons();
    const i = list.indexOf(document.activeElement as HTMLButtonElement);
    if (e.key === 'Escape') {
      e.preventDefault();
      close();
    } else if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      const n = list.length;
      list[(i + (e.key === 'ArrowDown' ? 1 : n - 1) + n) % n]?.focus();
    } else if (e.key === 'Tab') {
      close(false);
    }
  }
</script>

<svelte:window
  onpointerdown={(e) => {
    if (open && root && !root.contains(e.target as Node)) close(false);
  }}
/>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="menu" bind:this={root} {onkeydown}>
  <button
    bind:this={button}
    class="btn icon"
    aria-label={label}
    aria-haspopup="menu"
    aria-expanded={open}
    onclick={toggle}
  >
    <Icon name="more" />
  </button>
  {#if open}
    <div class="list panel-card" role="menu" aria-label={label}>
      {#each shown as item (item.label)}
        <button
          role="menuitem"
          onclick={() => {
            close();
            item.run();
          }}>{item.label}</button
        >
      {/each}
    </div>
  {/if}
</div>

<style>
  .menu {
    position: relative;
    display: inline-block;
  }
  .list {
    position: absolute;
    z-index: var(--z-bar);
    top: calc(100% + 0.4rem);
    left: 0;
    min-width: 13rem;
    padding: var(--space-0350);
    display: grid;
    box-shadow: var(--shadow-menu);
  }
  [role='menuitem'] {
    padding: var(--space-0600) var(--space-0800);
    font: inherit;
    text-align: left;
    color: var(--text);
    background: transparent;
    border: 0;
    border-radius: var(--radius-item);
    cursor: pointer;
    transition: background var(--motion-hover) var(--ease);
  }
  [role='menuitem']:hover,
  [role='menuitem']:focus-visible {
    background: var(--fill-hover);
    outline: none;
  }
</style>
