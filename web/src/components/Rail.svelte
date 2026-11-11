<script lang="ts" generics="T">
  import type { Snippet } from 'svelte';
  import Icon from './Icon.svelte';

  // A horizontal shelf of cards. Arrow keys move between cards (left/right) and
  // between shelves (up/down), so the page works with a TV remote's D-pad.
  let {
    title,
    index = '',
    subtitle = '',
    items,
    key,
    card,
    subhead,
    more,
    size = 'wide',
  }: {
    title: string;
    /** Optional numbered chapter prefix (e.g. "01 / NEW ARRIVALS"). */
    index?: string;
    /** Optional editorial note beside the heading. */
    subtitle?: string;
    items: T[];
    key: (item: T) => string | number;
    card: Snippet<[T]>;
    subhead?: Snippet;
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
      {#if index}<span class="chapter-idx tabular">{index}</span>{/if}
      <h2>{title}</h2>
      {#if subtitle}<span class="chapter-sub">{subtitle}</span>{/if}
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
    {#if subhead}
      <div class="subhead">{@render subhead()}</div>
    {/if}
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
    margin: 0;
    padding-top: 2.25rem;
    padding-bottom: 2.25rem;
    border-top: 1px solid var(--line);
  }
  header {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 0.5rem 1rem;
    padding: 0 var(--gutter);
    margin-bottom: 1rem;
  }
  .chapter-idx {
    font-family: var(--font-mono);
    font-size: var(--fs-2xs-2);
    letter-spacing: var(--tracking-widest);
    text-transform: uppercase;
    color: var(--text-faint);
  }
  .chapter-sub {
    font-family: var(--font-display);
    font-style: italic;
    font-size: var(--fs-lg-2);
    color: var(--text-muted);
  }
  .subhead {
    padding: 0 var(--gutter) 1rem;
  }
  .more {
    font-family: var(--font-mono);
    font-size: var(--fs-xs);
    color: var(--text-muted);
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
    padding: 0 var(--gutter);
    display: grid;
    grid-auto-flow: column;
    grid-auto-columns: clamp(230px, 22vw, 320px);
    gap: 1.35rem;
    overflow-x: auto;
    overscroll-behavior-x: contain;
    scroll-snap-type: x mandatory;
    scroll-padding-inline: var(--gutter);
    scrollbar-width: none;
  }
  ul.poster {
    grid-auto-columns: clamp(190px, 18.5vw, 275px);
    gap: clamp(16px, 2vw, 26px);
  }
  ul.person {
    grid-auto-columns: clamp(110px, 10vw, 150px);
  }
  ul::-webkit-scrollbar {
    display: none;
  }
  @media (max-width: 600px) {
    .chapter-sub {
      display: none;
    }
  }
</style>
