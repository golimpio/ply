<script lang="ts">
  import type { Snippet } from 'svelte';
  import Icon from './Icon.svelte';

  // A modal over the page (the native <dialog>: focus is kept inside, Escape
  // closes it). `open` is bound; closing by any means sets it false.
  let {
    open = $bindable(false),
    title,
    wide = false,
    children,
    footer,
  }: { open: boolean; title: string; wide?: boolean; children: Snippet; footer?: Snippet } = $props();

  let dialog = $state<HTMLDialogElement>();
  // A click on the backdrop closes it, but only one that also began there: a
  // text selection dragged out of an input and let go outside must not close
  // the dialog (and lose what was typed).
  let downOnBackdrop = false;

  $effect(() => {
    if (!dialog) return;
    if (open && !dialog.open) dialog.showModal();
    else if (!open && dialog.open) dialog.close();
  });
</script>

<dialog
  bind:this={dialog}
  class:wide
  aria-labelledby="dialog-title"
  onclose={() => (open = false)}
  onpointerdown={(e) => (downOnBackdrop = e.target === dialog)}
  onclick={(e) => {
    if (downOnBackdrop && e.target === dialog) open = false;
    downOnBackdrop = false;
  }}
>
  {#if open}
    <header>
      <h2 id="dialog-title">{title}</h2>
      <button class="btn icon" aria-label="Close" onclick={() => (open = false)}><Icon name="close" /></button>
    </header>
    <div class="body">{@render children()}</div>
    {#if footer}<footer>{@render footer()}</footer>{/if}
  {/if}
</dialog>

<style>
  dialog {
    width: min(32rem, calc(100vw - 2rem));
    max-height: min(44rem, 88dvh);
    margin: 8vh auto auto;
    padding: 0;
    color: var(--text);
    background: var(--surface-dialog);
    /* Unprefixed only: the build adds -webkit- itself, and drops this one when both are written. */
    backdrop-filter: var(--blur-dialog);
    border: 1px solid var(--line-overlay);
    border-radius: var(--radius-lg);
    box-shadow: var(--shadow-dialog);
  }
  dialog[open] {
    display: grid;
    grid-template-rows: auto 1fr auto;
  }
  dialog.wide {
    width: min(42rem, calc(100vw - 2rem));
  }
  dialog::backdrop {
    background: var(--scrim);
  }
  header {
    display: flex;
    align-items: center;
    gap: 1rem;
    padding: 0.9rem 0.9rem 0.6rem 1.25rem;
  }
  header .btn {
    margin-left: auto;
    background: transparent;
    border-color: transparent;
  }
  .body {
    padding: 0.25rem 1.25rem 1.25rem;
    overflow-y: auto;
    min-height: 0;
  }
  footer {
    display: flex;
    flex-wrap: wrap;
    justify-content: flex-end;
    gap: 0.6rem;
    padding: 0.9rem 1.25rem 1.1rem;
    border-top: 1px solid var(--line);
  }
  @media (max-width: 600px) {
    dialog {
      margin: auto auto 0;
      width: 100vw;
      max-width: 100vw;
      max-height: 92dvh;
      border-radius: var(--radius-lg) var(--radius-lg) 0 0;
      padding-bottom: env(safe-area-inset-bottom);
    }
  }
</style>
