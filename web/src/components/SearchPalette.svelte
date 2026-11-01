<script lang="ts">
  import type { ItemSummary } from '../lib/api';
  import { episodeLabel, formatDuration } from '../lib/format';
  import { search } from '../lib/fuzzy';
  import { ensureLibrary, library, showsOf } from '../lib/library.svelte';
  import { go } from '../lib/nav';
  import { palette } from '../lib/palette.svelte';
  import { href } from '../lib/router.svelte';
  import Art from './Art.svelte';

  // Cmd/Ctrl-K or "/" anywhere outside a text field opens it.
  let dialog = $state<HTMLDialogElement>();
  let input = $state<HTMLInputElement>();
  let query = $state('');
  let active = $state(0);

  interface Hit {
    id: string;
    href: string;
    title: string;
    sub: (string | number | undefined)[];
    item: ItemSummary;
    label: string; // fallback-tile initials source
  }

  const hits = $derived.by((): Hit[] => {
    if (!query.trim()) return [];
    const shows = showsOf(library.items);
    const showHits = search(
      shows.map((s) => ({ ...s.cover, title: s.show, episodeTitle: undefined, year: undefined })),
      query,
      5,
    ).map((cover) => {
      const s = shows.find((x) => x.show === cover.title)!;
      return {
        id: `show:${s.show}`,
        href: href.show(s.show),
        title: s.show,
        sub: ['Show', `${s.episodeCount} episode${s.episodeCount === 1 ? '' : 's'}`],
        item: s.cover,
        label: s.show,
      };
    });
    const itemHits = search(library.items, query, 25).map((it) => ({
      id: `item:${it.id}`,
      href: href.item(it.id),
      title: it.kind === 'episode' ? it.episodeTitle || `Episode ${it.episode ?? ''}`.trim() : it.title,
      sub:
        it.kind === 'episode'
          ? [it.show, episodeLabel(it.season, it.episode)]
          : ['Movie', it.year, formatDuration(it.durationSec)],
      item: it,
      label: it.show || it.title,
    }));
    return [...showHits, ...itemHits];
  });

  $effect(() => {
    void query;
    active = 0;
  });

  $effect(() => {
    if (!dialog) return;
    if (palette.open && !dialog.open) {
      void ensureLibrary();
      query = '';
      dialog.showModal();
      input?.focus();
    } else if (!palette.open && dialog.open) {
      dialog.close();
    }
  });

  function choose(hit: Hit | undefined) {
    if (!hit) return;
    palette.open = false;
    go(hit.href);
  }

  function onInputKey(e: KeyboardEvent) {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      const n = hits.length;
      if (n) active = (active + (e.key === 'ArrowDown' ? 1 : n - 1)) % n;
      document.getElementById(`hit-${active}`)?.scrollIntoView({ block: 'nearest' });
    } else if (e.key === 'Enter') {
      e.preventDefault();
      choose(hits[active]);
    }
  }

  function onWindowKey(e: KeyboardEvent) {
    const t = e.target as HTMLElement;
    const typing = t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName);
    if ((e.key === 'k' && (e.metaKey || e.ctrlKey)) || (e.key === '/' && !typing && !palette.open)) {
      e.preventDefault();
      palette.open = !palette.open;
    }
  }
</script>

<svelte:window onkeydown={onWindowKey} />

<dialog
  bind:this={dialog}
  aria-label="Search the library"
  onclose={() => (palette.open = false)}
  onclick={(e) => {
    if (e.target === dialog) palette.open = false; // click on the backdrop
  }}
>
  <div class="panel">
    <input
      bind:this={input}
      bind:value={query}
      onkeydown={onInputKey}
      type="search"
      placeholder="Search movies, shows and episodes"
      role="combobox"
      aria-expanded={hits.length > 0}
      aria-controls="search-hits"
      aria-activedescendant={hits.length ? `hit-${active}` : undefined}
      autocomplete="off"
      spellcheck="false"
    />
    {#if query.trim()}
      <ul id="search-hits" role="listbox" aria-label="Results">
        {#each hits as hit, i (hit.id)}
          <!-- Keyboard selection is the combobox input's job (aria-activedescendant); the click is for pointers. -->
          <!-- svelte-ignore a11y_click_events_have_key_events -->
          <li
            id="hit-{i}"
            role="option"
            aria-selected={i === active}
            onpointermove={() => (active = i)}
            onclick={() => choose(hit)}
          >
            <div class="thumb"><Art item={hit.item} label={hit.label} /></div>
            <div class="text">
              <div class="title">{hit.title}</div>
              <div class="sub">
                {#each hit.sub.filter((p) => p !== '' && p != null) as p, j (j)}<span>{p}</span>{/each}
              </div>
            </div>
          </li>
        {:else}
          <li class="empty" role="presentation">
            {library.loaded ? `Nothing matches “${query.trim()}”.` : 'Loading the library…'}
          </li>
        {/each}
      </ul>
    {:else}
      <p class="tip">Type a title, a show or a year. Use ↑ ↓ to move and Enter to open.</p>
    {/if}
  </div>
</dialog>

<style>
  dialog {
    width: min(40rem, calc(100vw - 2rem));
    max-height: min(36rem, 80dvh);
    margin: 12vh auto auto;
    padding: 0;
    color: var(--text);
    background: rgb(26 23 20 / 0.92);
    backdrop-filter: blur(24px);
    -webkit-backdrop-filter: blur(24px);
    border: 1px solid rgb(255 255 255 / 0.1);
    border-radius: var(--radius-lg);
    box-shadow: 0 30px 80px rgb(0 0 0 / 0.55);
    overflow: hidden;
  }
  dialog::backdrop {
    background: rgb(8 7 6 / 0.6);
  }
  .panel {
    display: grid;
    grid-template-rows: auto 1fr;
    max-height: inherit;
  }
  input {
    width: 100%;
    padding: 1.1rem 1.25rem;
    font: inherit;
    font-size: 1.15rem;
    color: var(--text);
    background: transparent;
    border: 0;
    border-bottom: 1px solid var(--line);
    outline: none;
  }
  input::-webkit-search-cancel-button {
    display: none;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0.4rem;
    overflow-y: auto;
  }
  li[role='option'] {
    display: grid;
    grid-template-columns: 6.5rem 1fr;
    align-items: center;
    gap: 0.9rem;
    padding: 0.45rem;
    border-radius: var(--radius);
    cursor: pointer;
  }
  li[aria-selected='true'] {
    background: rgb(255 255 255 / 0.08);
  }
  .thumb {
    border-radius: 6px;
    overflow: hidden;
  }
  .thumb :global(.tile) {
    font-size: 1rem;
  }
  .text {
    min-width: 0;
  }
  .title {
    font-weight: 650;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .sub {
    display: flex;
    gap: 0.75rem;
  }
  .sub,
  .empty,
  .tip {
    color: var(--muted);
    font-size: 0.875rem;
  }
  .empty,
  .tip {
    margin: 0;
    padding: 1rem 1.25rem;
  }
</style>
