<script lang="ts">
  import { tick } from 'svelte';

  // A Remove that asks first, in place: the button becomes "Remove the TMDB key?" with Remove and
  // Keep, the focus on Keep (the safe answer). Nothing is sent until the second Remove.
  let {
    label,
    question,
    disabled = false,
    onconfirm,
  }: {
    /** The button's words: "Remove key", "Remove PIN". */
    label: string;
    /** What is asked: "Remove the TMDB key?" */
    question: string;
    disabled?: boolean;
    onconfirm: () => void;
  } = $props();

  let asking = $state(false);
  let first = $state<HTMLButtonElement>();

  async function keep() {
    asking = false;
    await tick();
    first?.focus();
  }
  function confirm() {
    asking = false;
    onconfirm();
  }
  const focus = (el: HTMLElement) => el.focus();
</script>

{#if asking}
  <span class="confirm-remove" role="group" aria-label={question}>
    <span class="question">{question}</span>
    <button type="button" class="btn danger" {disabled} onclick={confirm}>Remove</button>
    <button type="button" class="btn" onclick={keep} {@attach focus}>Keep</button>
  </span>
{:else}
  <button type="button" class="btn" {disabled} bind:this={first} onclick={() => (asking = true)}>{label}</button>
{/if}

<style>
  .confirm-remove {
    display: inline-flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.5rem 0.6rem;
  }
  .question {
    font-size: var(--fs-sm);
  }
  .danger {
    color: var(--text-inverse);
    background: var(--danger);
    border-color: var(--danger);
  }
  .danger:hover:not(:disabled) {
    background: color-mix(in oklab, var(--danger) 85%, var(--lighten));
  }
</style>
