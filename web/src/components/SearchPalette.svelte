<script lang="ts">
  import { api, type GenreCount, type ItemSummary, type PersonCard } from '../lib/api';
  import { episodeLabel, formatDuration } from '../lib/format';
  import { search } from '../lib/fuzzy';
  import { ensureLibrary, library, showsOf } from '../lib/library.svelte';
  import { profileImg, type Img } from '../lib/meta';
  import { go } from '../lib/nav';
  import { palette } from '../lib/palette.svelte';
  import { Latest, matchGenres, PEOPLE_DEBOUNCE_MS, peopleQuery } from '../lib/palette-search';
  import { href } from '../lib/router.svelte';
  import Art from './Art.svelte';
  import Picture from './Picture.svelte';

  // Cmd/Ctrl-K or "/" anywhere outside a text field opens it.
  let dialog = $state<HTMLDialogElement>();
  let input = $state<HTMLInputElement>();
  let query = $state('');
  let active = $state(0);

  // A hit is a title (artwork from `item`), a person (`portrait`, round) or a genre (a title tile).
  interface Hit {
    id: string;
    href: string;
    title: string;
    sub: (string | number | undefined)[];
    item?: ItemSummary;
    portrait?: Img | null;
    label: string; // fallback-tile initials source
  }

  // People are asked of the server as the query settles; the genres are fetched
  // once, the first time the palette opens, and matched here.
  let people = $state<PersonCard[]>([]);
  let peoplePending = $state(false); // a lookup is waiting for typing to pause, or for its answer
  let genres = $state<GenreCount[]>([]);
  let genresRequested = false;
  const latest = new Latest();

  function loadGenres() {
    if (genresRequested) return;
    genresRequested = true;
    api.genres().then(
      (list) => (genres = list),
      () => (genresRequested = false), // try again the next time it opens
    );
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
    const personHits = people.slice(0, 8).map((p) => ({
      id: `person:${p.id}`,
      href: href.person(p.id),
      title: p.name,
      sub: [p.knownFor || 'Person'],
      portrait: profileImg(p),
      label: p.name,
    }));
    const genreHits = matchGenres(genres, query).map((g) => ({
      id: `genre:${g.id}`,
      href: href.genre(g.id),
      title: g.name,
      sub: ['Genre', `${g.count} title${g.count === 1 ? '' : 's'}`],
      label: g.name,
    }));
    return [...showHits, ...itemHits, ...personHits, ...genreHits];
  });

  $effect(() => {
    void query;
    active = 0;
  });

  // Look people up once typing pauses. An answer is used only if no newer query
  // (or a query too short to ask) has been typed since the request left.
  $effect(() => {
    const q = peopleQuery(query);
    const ticket = latest.begin();
    if (!q) {
      people = [];
      peoplePending = false;
      return;
    }
    peoplePending = true;
    const answer = (list: PersonCard[]) => {
      if (!latest.isCurrent(ticket)) return;
      people = list;
      peoplePending = false;
    };
    const timer = setTimeout(() => {
      api.people(q).then(answer, () => answer([]));
    }, PEOPLE_DEBOUNCE_MS);
    return () => {
      clearTimeout(timer);
      latest.cancel();
    };
  });

  $effect(() => {
    if (!dialog) return;
    if (palette.open && !dialog.open) {
      void ensureLibrary();
      loadGenres();
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
      placeholder="Search movies, shows, people and genres"
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
            {#if hit.portrait !== undefined}
              <div class="thumb round">
                <Picture img={hit.portrait} ratio="1 / 1" label={hit.label} sizes="52px" />
              </div>
            {:else}
              <div class="thumb"><Art item={hit.item} label={hit.label} /></div>
            {/if}
            <div class="text">
              <div class="title">{hit.title}</div>
              <div class="sub">
                {#each hit.sub.filter((p) => p !== '' && p != null) as p, j (j)}<span>{p}</span>{/each}
              </div>
            </div>
          </li>
        {:else}
          <li class="empty" role="presentation">
            {#if !library.loaded}Loading the library…{:else if peoplePending}Searching…{:else}Nothing matches “{query.trim()}”.{/if}
          </li>
        {/each}
      </ul>
    {:else}
      <p class="tip">Type a title, a name, a genre or a year. Use ↑ ↓ to move and Enter to open.</p>
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
    background: var(--surface-palette);
    /* Unprefixed only: the build adds -webkit- itself, and drops this one when both are written. */
    backdrop-filter: var(--blur-dialog);
    border: 1px solid var(--line-overlay);
    border-radius: var(--radius-lg);
    box-shadow: var(--shadow-dialog);
    overflow: hidden;
  }
  dialog::backdrop {
    background: var(--scrim);
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
    font-size: var(--fs-xl);
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
    background: var(--fill);
  }
  .thumb {
    border-radius: var(--radius-item);
    overflow: hidden;
  }
  .thumb.round {
    width: 3.25rem;
    justify-self: center; /* centred in the artwork column, so the text lines up with the other rows */
    border-radius: var(--radius-round);
  }
  .thumb :global(.tile) {
    font-size: var(--fs-base);
  }
  .text {
    min-width: 0;
  }
  .title {
    font-weight: var(--fw-semibold-2);
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
    color: var(--text-muted);
    font-size: var(--fs-sm);
  }
  .empty,
  .tip {
    margin: 0;
    padding: 1rem 1.25rem;
  }
</style>
