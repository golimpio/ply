<script lang="ts">
  import { onMount } from 'svelte';
  import { railOf, type LetterEntry } from '../lib/az';

  // The A to Z rail of a list sorted by name: the letters it has, down the right edge, faint until
  // a pointer is over it. A tap or a click jumps to the letter's first card; a drag along it jumps
  // as it goes, a touch one with the letter shown big beside the finger. Each letter is a 24 px
  // target at the least: on a screen too short for all of them, every other one (or fewer) is
  // shown, and a drag still goes through them all. From the keyboard it is one Tab stop (a roving
  // tabindex): ↑ ↓ Home End move between the letters, Enter jumps and takes the focus to that card.
  // `entries` from lib/az's railLetters (none on a short list: then there is no rail).
  let { entries, onjump }: {
    entries: LetterEntry[];
    /** `moving`: a drag along the rail, still going (the grid holds the posters until it rests). */
    onjump: (index: number, focus: boolean, moving: boolean) => void;
  } = $props();

  let nav = $state<HTMLElement>();
  let room = $state(0); // how many letters fit, from the nav's height
  let current = $state(''); // the letter that takes the Tab stop
  let drag = $state<{ letter: string; y: number; touch: boolean } | null>(null);
  /** Where the letters are, read once as a drag starts. */
  let span = { top: 0, bottom: 0 };

  const shown = $derived(railOf(entries, room));
  const tabStop = $derived(shown.some((e) => e.letter === current) ? current : shown[0]?.letter);

  const label = (l: string) => (l === '#' ? 'Titles starting with a digit or a symbol' : `Titles starting with ${l}`);

  /** The letter at y, through all of them (a collapsed rail too). */
  function pick(e: PointerEvent) {
    if (!entries.length || span.bottom <= span.top) return;
    const y = Math.min(Math.max(e.clientY, span.top), span.bottom - 1);
    const at = entries[Math.floor(((y - span.top) / (span.bottom - span.top)) * entries.length)];
    if (at.letter !== drag?.letter) onjump(at.index, false, drag !== null);
    drag = { letter: at.letter, y, touch: e.pointerType !== 'mouse' };
  }

  function down(e: PointerEvent) {
    if (e.button !== 0 || !nav) return;
    e.preventDefault(); // no focus ring from a pointer, no text selection
    nav.setPointerCapture(e.pointerId);
    const buttons = nav.querySelectorAll('button');
    span = { top: buttons[0]?.getBoundingClientRect().top ?? 0, bottom: buttons[buttons.length - 1]?.getBoundingClientRect().bottom ?? 0 };
    drag = null;
    pick(e);
  }
  const move = (e: PointerEvent) => drag && pick(e);
  const up = () => (drag = null);

  function onkeydown(e: KeyboardEvent) {
    const i = shown.findIndex((x) => x.letter === tabStop);
    const to = e.key === 'ArrowDown' ? i + 1 : e.key === 'ArrowUp' ? i - 1 : e.key === 'Home' ? 0 : e.key === 'End' ? shown.length - 1 : -2;
    if (to === -2) return;
    e.preventDefault();
    const next = shown[Math.max(0, Math.min(shown.length - 1, to))];
    current = next.letter;
    nav?.querySelector<HTMLElement>(`button[data-letter="${CSS.escape(next.letter)}"]`)?.focus();
  }

  onMount(() => {
    const fit = () => {
      if (!nav) return;
      const letter = parseFloat(getComputedStyle(nav).getPropertyValue('--rail-letter-h')) || 24;
      room = Math.max(2, Math.floor(nav.clientHeight / letter));
    };
    const observer = new ResizeObserver(fit);
    if (nav) observer.observe(nav);
    fit();
    return () => observer.disconnect();
  });
</script>

{#if entries.length > 1}
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions (a drag along it, the keys between its buttons) -->
  <nav
    class="az"
    class:active={drag}
    aria-label="Jump to a letter"
    bind:this={nav}
    onpointerdown={down}
    onpointermove={move}
    onpointerup={up}
    onpointercancel={up}
    {onkeydown}
  >
    {#each shown as e (e.letter)}
      <!-- A pointer jumps on pointerdown (above); a click from the keyboard has no detail. -->
      <button
        type="button"
        tabindex={e.letter === tabStop ? 0 : -1}
        data-letter={e.letter}
        data-index={e.index}
        aria-label={label(e.letter)}
        onfocus={() => (current = e.letter)}
        onclick={(ev) => ev.detail === 0 && onjump(e.index, true, false)}>{e.letter}</button
      >
    {/each}
  </nav>
  {#if drag?.touch}
    <div class="bubble" style:top="{drag.y}px" aria-hidden="true">{drag.letter}</div>
  {/if}
{/if}

<style>
  .az {
    position: fixed;
    z-index: var(--z-bar);
    top: calc(var(--bar-h) + env(safe-area-inset-top) + 1rem);
    bottom: 1rem;
    right: env(safe-area-inset-right);
    width: var(--rail-w);
    display: flex;
    flex-direction: column;
    justify-content: center;
    touch-action: none;
    user-select: none;
    -webkit-user-select: none;
    opacity: var(--opacity-rail-idle);
    transition: opacity var(--motion-hover);
  }
  .az:hover,
  .az:focus-within,
  .az.active {
    opacity: 1;
  }
  button {
    flex: none;
    width: var(--rail-w);
    height: var(--rail-letter-h);
    padding: 0;
    border: 0;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text-muted);
    font: inherit;
    font-size: var(--fs-3xs);
    font-weight: var(--fw-semibold);
    line-height: var(--lh-none);
    cursor: pointer;
  }
  button:hover {
    color: var(--text);
  }
  button:focus-visible {
    outline: none;
    color: var(--text);
    box-shadow: var(--ring-focus-inset);
  }
  .bubble {
    position: fixed;
    z-index: var(--z-bar);
    right: calc(var(--rail-w) + 1.5rem);
    width: var(--bubble-size);
    height: var(--bubble-size);
    margin-top: calc(var(--bubble-size) / -2);
    display: grid;
    place-items: center;
    border-radius: var(--radius-lg);
    background: var(--surface-dialog);
    backdrop-filter: var(--blur-menu);
    box-shadow: var(--shadow-menu);
    color: var(--text);
    font-size: var(--fs-4xl);
    font-weight: var(--fw-bold);
    pointer-events: none;
  }
  @media (max-width: 600px) {
    .az {
      bottom: calc(var(--tabbar-h) + env(safe-area-inset-bottom));
    }
  }
</style>
