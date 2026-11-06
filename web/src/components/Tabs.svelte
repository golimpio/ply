<script lang="ts" generics="T extends string">
  import type { Snippet } from 'svelte';
  import { MediaQuery } from 'svelte/reactivity';

  // Tabs (the WAI-ARIA tabs pattern): a tablist of tabs and the selected one's tabpanel. Only that
  // panel is mounted, so a tab fetches nothing until it is opened. The tab with the focus is the
  // selected one (roving tabindex: the others are out of the Tab order), and the arrow keys, Home
  // and End select another, as a click does. The owner keeps `selected` (Settings: in the URL).
  // From WIDE up the tabs are a column beside the panel; below, a row that scrolls sideways.
  let {
    id,
    label,
    tabs,
    selected,
    onselect,
    panel,
  }: {
    /** Prefixes the ids of the tabs and the panel. */
    id: string;
    /** The tablist's accessible name. */
    label: string;
    tabs: readonly { id: T; label: string }[];
    selected: T;
    onselect: (tab: T) => void;
    panel: Snippet<[T]>;
  } = $props();

  // Keep in step with the media query in the styles below.
  const wide = new MediaQuery('min-width: 900px');

  let list = $state<HTMLElement>();
  const tabId = (t: T) => `${id}-tab-${t}`;
  const panelId = (t: T) => `${id}-panel-${t}`;

  function choose(t: T, focus: boolean) {
    if (t !== selected) onselect(t);
    if (focus) document.getElementById(tabId(t))?.focus();
  }

  function onkeydown(e: KeyboardEvent) {
    const n = tabs.length;
    const i = Math.max(0, tabs.findIndex((t) => t.id === selected));
    let j: number;
    switch (e.key) {
      case 'ArrowDown':
      case 'ArrowRight':
        j = (i + 1) % n;
        break;
      case 'ArrowUp':
      case 'ArrowLeft':
        j = (i - 1 + n) % n;
        break;
      case 'Home':
        j = 0;
        break;
      case 'End':
        j = n - 1;
        break;
      default:
        return;
    }
    e.preventDefault();
    choose(tabs[j].id, true);
  }

  // In the row, the selected tab sits in the middle (a deep link to the last one, on a phone), with
  // its neighbours either side to show there is more; set directly, not with scrollIntoView, which
  // would scroll the page too.
  $effect(() => {
    const el = list?.querySelector<HTMLElement>(`#${CSS.escape(tabId(selected))}`);
    if (!list || !el || wide.current) return;
    const middle = el.offsetLeft - list.offsetLeft + el.offsetWidth / 2;
    list.scrollLeft = middle - list.clientWidth / 2; // the browser keeps it within the row
    edges();
  });

  // The row fades out at an edge with more past it.
  let more = $state({ before: false, after: false });
  function edges() {
    if (!list) return;
    more = { before: list.scrollLeft > 1, after: list.scrollLeft + list.clientWidth < list.scrollWidth - 1 };
  }
  $effect(() => {
    if (!list) return;
    const ro = new ResizeObserver(edges);
    ro.observe(list);
    return () => ro.disconnect();
  });
</script>

<div class="tabs">
  <!-- The tabs take the focus, one at a time; the list only hears their keys. -->
  <!-- svelte-ignore a11y_interactive_supports_focus -->
  <div
    class="tablist"
    role="tablist"
    aria-label={label}
    aria-orientation={wide.current ? 'vertical' : 'horizontal'}
    class:fade-before={more.before}
    class:fade-after={more.after}
    bind:this={list}
    {onkeydown}
    onscroll={edges}
  >
    {#each tabs as t (t.id)}
      {@const on = t.id === selected}
      <button
        type="button"
        class="chip tab"
        role="tab"
        id={tabId(t.id)}
        aria-selected={on}
        aria-controls={on ? panelId(t.id) : undefined}
        tabindex={on ? 0 : -1}
        onclick={() => choose(t.id, false)}>{t.label}</button
      >
    {/each}
  </div>
  {#key selected}
    <!-- Not a stop of its own: every panel has controls, and Tab goes on to its first. -->
    <div class="tabpanel" role="tabpanel" id={panelId(selected)} aria-labelledby={tabId(selected)}>
      {@render panel(selected)}
    </div>
  {/key}
</div>

<style>
  .tabs {
    display: grid;
    gap: 1.75rem;
    min-width: 0;
  }
  .tablist {
    display: flex;
    gap: 0.5rem;
    min-width: 0;
    overflow-x: auto;
    scrollbar-width: none;
    /* Room for the focus ring, which the scroll box would clip. */
    margin: calc(-1 * var(--focus-offset) - 2px);
    padding: calc(var(--focus-offset) + 2px);
  }
  .tablist::-webkit-scrollbar {
    display: none;
  }
  .tab {
    flex: none;
    white-space: nowrap;
  }
  .tabpanel {
    min-width: 0;
  }
  .fade-before {
    mask-image: linear-gradient(to left, var(--mask-solid) calc(100% - var(--fade-edge)), transparent);
  }
  .fade-after {
    mask-image: linear-gradient(to right, var(--mask-solid) calc(100% - var(--fade-edge)), transparent);
  }
  .fade-before.fade-after {
    mask-image: linear-gradient(
      to right,
      transparent,
      var(--mask-solid) var(--fade-edge),
      var(--mask-solid) calc(100% - var(--fade-edge)),
      transparent
    );
  }
  @media (min-width: 900px) {
    .tabs {
      grid-template-columns: var(--side-nav-w) minmax(0, 1fr);
      align-items: start;
      gap: 2.5rem;
    }
    .tablist {
      position: sticky;
      top: calc(var(--bar-h) + 1.25rem);
      flex-direction: column;
      gap: 0.25rem;
      overflow: visible;
    }
    .tablist.fade-before,
    .tablist.fade-after {
      mask-image: none;
    }
    /* A column of navigation: the selected tab marked by a fill and the accent's bar, quieter
       than the white of a selected chip. */
    .tab {
      text-align: start;
      border-color: transparent;
      border-radius: var(--radius-row);
    }
    .tab:hover {
      background: var(--fill-subtle);
    }
    .tab[aria-selected='true'] {
      color: var(--text);
      background: var(--fill);
      border-color: transparent;
      box-shadow: inset 3px 0 0 var(--accent);
      font-weight: var(--fw-semibold);
    }
  }
</style>
