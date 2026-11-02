<script lang="ts" generics="T">
  import type { Snippet } from 'svelte';
  import Icon from './Icon.svelte';

  // A horizontal shelf of cards. Arrow keys move between cards (left/right) and
  // between shelves (up/down), so the page works with a TV remote's D-pad.
  let {
    title,
    items,
    key,
    card,
    more,
    size = 'wide',
  }: {
    title: string;
    items: T[];
    key: (item: T) => string | number;
    card: Snippet<[T]>;
    more?: { href: string; label: string };
    /** Card width: wide 16:9 cards, 2:3 posters, or the smaller portraits of a cast. */
    size?: 'wide' | 'poster' | 'person';
  } = $props();

  let scroller = $state<HTMLUListElement>();
  let atStart = $state(true);
  let atEnd = $state(false);

  function updateEdges() {
    if (!scroller) return;
    atStart = scroller.scrollLeft < 8;
    atEnd = scroller.scrollLeft + scroller.clientWidth >= scroller.scrollWidth - 8;
  }

  $effect(() => {
    void items.length;
    updateEdges();
  });

  function page(dir: 1 | -1) {
    scroller?.scrollBy({ left: dir * scroller.clientWidth * 0.85, behavior: 'smooth' });
  }

  function cardsOf(list: Element | null): HTMLElement[] {
    return list ? [...list.querySelectorAll<HTMLElement>(':scope > li > a')] : [];
  }

  function onkeydown(e: KeyboardEvent) {
    const from = (e.target as Element).closest('a');
    if (!from || !scroller) return;
    const cards = cardsOf(scroller);
    const i = cards.indexOf(from as HTMLElement);
    if (i < 0) return;
    let target: HTMLElement | undefined;
    if (e.key === 'ArrowRight') target = cards[i + 1];
    else if (e.key === 'ArrowLeft') target = cards[i - 1];
    else if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      const rails = [...document.querySelectorAll('[data-rail]')];
      const next = rails[rails.indexOf(scroller) + (e.key === 'ArrowDown' ? 1 : -1)];
      const other = cardsOf(next ?? null);
      target = other[Math.min(i, other.length - 1)];
    }
    if (!target) return;
    e.preventDefault();
    target.focus();
    target.scrollIntoView({ block: 'nearest', inline: 'nearest', behavior: 'smooth' });
  }
</script>

{#if items.length}
  <section class="rail">
    <header>
      <h2>{title}</h2>
      {#if more}<a class="more link" href={more.href}>{more.label}</a>{/if}
      <div class="arrows">
        <button class="btn icon" aria-label="Scroll {title} back" disabled={atStart} onclick={() => page(-1)}>
          <Icon name="left" />
        </button>
        <button class="btn icon" aria-label="Scroll {title} forward" disabled={atEnd} onclick={() => page(1)}>
          <Icon name="right" />
        </button>
      </div>
    </header>
    <!-- Delegated arrow-key navigation between the focusable card links inside. -->
    <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
    <ul class={size} bind:this={scroller} data-rail onscroll={updateEdges} {onkeydown} aria-label={title}>
      {#each items as item (key(item))}
        <li>{@render card(item)}</li>
      {/each}
    </ul>
  </section>
{/if}

<style>
  .rail {
    margin-top: 2.25rem;
  }
  header {
    display: flex;
    align-items: baseline;
    gap: 1rem;
    padding: 0 var(--gutter);
    margin-bottom: 0.75rem;
  }
  .more {
    font-size: 0.9rem;
    color: var(--muted);
  }
  .arrows {
    margin-left: auto;
    display: flex;
    gap: 0.4rem;
    align-self: center;
  }
  .arrows .btn {
    min-height: 2rem;
    width: 2rem;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0.25rem var(--gutter) 0.5rem;
    display: grid;
    grid-auto-flow: column;
    grid-auto-columns: clamp(220px, 22vw, 320px);
    gap: 1.1rem;
    overflow-x: auto;
    overscroll-behavior-x: contain;
    scroll-snap-type: x mandatory;
    scroll-padding-inline: var(--gutter);
    scrollbar-width: none;
  }
  ul.poster {
    grid-auto-columns: clamp(140px, 13vw, 200px);
  }
  ul.person {
    grid-auto-columns: clamp(110px, 10vw, 150px);
  }
  ul::-webkit-scrollbar {
    display: none;
  }
  li {
    scroll-snap-align: start;
    min-width: 0;
  }
  @media (hover: none), (max-width: 600px) {
    .arrows {
      display: none;
    }
    ul {
      grid-auto-columns: min(68vw, 260px);
      gap: 0.8rem;
    }
    ul.poster {
      grid-auto-columns: min(38vw, 160px);
    }
    ul.person {
      grid-auto-columns: min(30vw, 120px);
    }
  }
</style>
