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
  onclick={(e) => {
    if (e.target === dialog) open = false; // a click on the backdrop
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
    background: rgb(26 23 20 / 0.94);
    backdrop-filter: blur(24px);
    -webkit-backdrop-filter: blur(24px);
    border: 1px solid rgb(255 255 255 / 0.1);
    border-radius: var(--radius-lg);
    box-shadow: 0 30px 80px rgb(0 0 0 / 0.55);
  }
  dialog[open] {
    display: grid;
    grid-template-rows: auto 1fr auto;
  }
  dialog.wide {
    width: min(42rem, calc(100vw - 2rem));
  }
  dialog::backdrop {
    background: rgb(8 7 6 / 0.6);
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
