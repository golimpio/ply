<script lang="ts">
  import { untrack } from 'svelte';
  import { api, ApiError, imageUrl, parseMatchInput, type MatchCandidate, type MatchRef, type TitleKind } from '../lib/api';
  import { href } from '../lib/router.svelte';
  import { app } from '../lib/state.svelte';
  import Dialog from './Dialog.svelte';
  import Picture from './Picture.svelte';

  // "Fix match": search TMDB for the right film or show (or paste its TMDB or
  // IMDb link) and link the file, or the whole show, to it. A match made here
  // is never replaced by the automatic matcher.
  let {
    open = $bindable(false),
    kind,
    itemId = undefined,
    show = undefined,
    query: initialQuery,
    year: initialYear = undefined,
    matched = false,
  }: {
    open: boolean;
    kind: TitleKind;
    itemId?: number;
    show?: string;
    query: string;
    year?: number;
    /** It has a match now: offer to remove it. */
    matched?: boolean;
  } = $props();

  let query = $state('');
  let year = $state('');
  let results = $state<MatchCandidate[]>([]);
  let searched = $state(false);
  let busy = $state(false);
  let error = $state('');
  let offline = $state(false);
  let input = $state<HTMLInputElement>();
  // Each search takes a number; an answer is shown only while its number is
  // the latest, so a slow answer for an earlier query (or an earlier opening
  // of the dialog, for another film) can never be clicked.
  let searchSeq = 0;

  // Each opening starts from what the file says, and searches for it. Only
  // `open` is a dependency: typing, or a library refresh, must not re-run it.
  $effect(() => {
    if (!open) {
      searchSeq++; // answers still on their way belong to a closed dialog
      return;
    }
    untrack(() => {
      query = initialQuery;
      year = initialYear ? String(initialYear) : '';
      results = [];
      searched = false;
      error = '';
      offline = false;
      busy = false; // a search cut off by closing never cleared it
      void search();
    });
    // The search box, not Close, takes the focus (after showModal has placed it).
    const t = setTimeout(() => input?.focus(), 0);
    return () => clearTimeout(t);
  });

  const pasted = $derived(parseMatchInput(query));
  const what = $derived(kind === 'movie' ? 'film' : 'show');
  // A themoviedb.org link to the other kind of title (a show's link in a film's dialog).
  const wrongKind = $derived(!!pasted?.kind && pasted.kind !== kind);
  // A bare number is searched as a title ("1917"); it may also be a TMDB id.
  const numeric = $derived(/^\d{1,10}$/.test(query.trim()) && Number(query.trim()) > 0 ? Number(query.trim()) : 0);

  function explain(e: unknown): string {
    if (e instanceof ApiError && e.status === 409) {
      offline = true;
      return 'Online lookups are off, so there is nothing to search.';
    }
    if (e instanceof ApiError && e.status === 502) return 'TMDB could not be reached. Try again in a moment.';
    return e instanceof Error ? e.message : String(e);
  }

  async function search(e?: Event) {
    e?.preventDefault();
    const q = query.trim();
    if (!q || pasted) return;
    const mine = ++searchSeq;
    busy = true;
    error = '';
    results = [];
    try {
      const y = Number(year);
      const found = await api.searchMetadata(kind, q, Number.isInteger(y) && y > 1800 ? y : undefined);
      if (mine !== searchSeq) return;
      results = found;
      searched = true;
    } catch (err) {
      if (mine !== searchSeq) return;
      error = explain(err);
      results = [];
    } finally {
      if (mine === searchSeq) busy = false;
    }
  }

  // Chrome clears a type="search" box with the first Escape, and only the second reaches the
  // dialog: take the key before the box does, so one press closes. An Escape that cancels an
  // input-method composition (Japanese, Chinese) is the input method's.
  function closeOnEscape(e: KeyboardEvent) {
    if (e.key !== 'Escape' || e.isComposing) return;
    e.preventDefault();
    open = false;
  }

  async function apply(ref: MatchRef) {
    busy = true;
    error = '';
    try {
      if (kind === 'movie' && itemId) await api.matchItem(itemId, ref);
      else if (show) await api.matchShow(show, ref);
      app.libraryVersion++; // every page shows the new metadata
      open = false;
    } catch (err) {
      error = explain(err);
    } finally {
      busy = false;
    }
  }

  async function unmatch() {
    busy = true;
    error = '';
    try {
      if (kind === 'movie' && itemId) await api.unmatchItem(itemId);
      else if (show) await api.unmatchShow(show);
      app.libraryVersion++;
      open = false;
    } catch (err) {
      error = explain(err);
    } finally {
      busy = false;
    }
  }
</script>

<Dialog bind:open title={kind === 'movie' ? 'Fix the match' : 'Fix the show’s match'} wide>
  <form class="search" onsubmit={search}>
    <label class="q">
      <span class="sr-only">Title, or a TMDB or IMDb link</span>
      <input
        bind:this={input}
        bind:value={query}
        type="search"
        placeholder="Title, or a TMDB or IMDb link"
        autocomplete="off"
        onkeydown={closeOnEscape}
      />
    </label>
    <label class="y">
      <span class="sr-only">Year</span>
      <input bind:value={year} inputmode="numeric" maxlength="4" placeholder="Year" />
    </label>
    <button class="btn" disabled={busy || !query.trim() || !!pasted}>Search</button>
  </form>

  {#if pasted && wrongKind}
    <p class="error" role="alert">
      That is a link to a {pasted.kind === 'series' ? 'show' : 'film'}; this needs a {what}.
    </p>
  {:else if pasted}
    <div class="pasted">
      <p>
        {'imdbId' in pasted.ref ? `IMDb ${pasted.ref.imdbId}` : `TMDB ${what} ${pasted.ref.tmdbId}`}
      </p>
      <button class="btn primary" disabled={busy} onclick={() => apply(pasted.ref)}>Use this {what}</button>
    </div>
  {/if}

  {#if error}
    <p class="error" role="alert">
      {error}
      {#if offline}<a class="link" href={href.settings} onclick={() => (open = false)}>Open settings</a>{/if}
    </p>
  {/if}

  {#if !pasted}
    <ul class="results" aria-busy={busy}>
      {#each results as r (r.tmdbId)}
        <li>
          <button class="result" disabled={busy} onclick={() => apply({ tmdbId: r.tmdbId })}>
            <div class="poster">
              <Picture img={r.poster ? { src: imageUrl(r.poster, 'w185') } : null} ratio="2 / 3" label={r.title} />
            </div>
            <div class="text">
              <div class="title">{r.title}{#if r.year}<span class="year">{r.year}</span>{/if}</div>
              {#if r.originalTitle && r.originalTitle !== r.title}<div class="muted orig">{r.originalTitle}</div>{/if}
              {#if r.overview}<p class="overview">{r.overview}</p>{/if}
            </div>
          </button>
        </li>
      {:else}
        {#if searched && !busy && !error}
          <li class="none muted">Nothing found. Try the original title, or paste the {what}’s TMDB link.</li>
        {/if}
      {/each}
    </ul>
    {#if numeric && searched && !busy}
      <p class="by-id muted">
        Or is {numeric} the {what}’s TMDB id?
        <button class="link-btn" disabled={busy} onclick={() => apply({ tmdbId: numeric })}>Use TMDB id {numeric}</button>
      </p>
    {/if}
  {/if}

  {#snippet footer()}
    {#if matched}
      <button class="btn" disabled={busy} onclick={unmatch}>Remove the match</button>
    {/if}
    <button class="btn" onclick={() => (open = false)}>Cancel</button>
  {/snippet}
</Dialog>

<style>
  .search {
    display: grid;
    grid-template-columns: 1fr 5.5rem auto;
    gap: 0.5rem;
  }
  input {
    width: 100%;
    min-height: 2.5rem;
    padding: 0.5rem 0.85rem;
    font: inherit;
    color: var(--text);
    background: var(--bg);
    border: 1px solid var(--line);
    border-radius: var(--radius);
  }
  input:focus-visible {
    border-color: var(--text);
    outline: none;
  }
  .pasted {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    margin-top: 1rem;
    padding: 0.75rem 0.9rem;
    background: var(--bg-2);
    border-radius: var(--radius);
  }
  .pasted p {
    margin: 0;
    font-weight: 600;
  }
  .error {
    margin: 1rem 0 0;
  }
  .error .link {
    margin-left: 0.5rem;
  }
  .results {
    list-style: none;
    margin: 1rem 0 0;
    padding: 0;
    display: grid;
    gap: 0.25rem;
  }
  .result {
    display: grid;
    grid-template-columns: 3.4rem 1fr;
    gap: 0.9rem;
    width: 100%;
    padding: 0.5rem;
    font: inherit;
    text-align: left;
    color: inherit;
    background: transparent;
    border: 0;
    border-radius: var(--radius);
    cursor: pointer;
  }
  .result:hover:not(:disabled),
  .result:focus-visible {
    background: rgb(255 255 255 / 0.07);
  }
  .poster {
    border-radius: 4px;
    overflow: hidden;
  }
  .poster :global(.tile) {
    font-size: 0.8rem;
  }
  .text {
    min-width: 0;
  }
  .title {
    font-weight: 650;
  }
  .year {
    margin-left: 0.5rem;
    color: var(--muted);
    font-weight: 500;
    font-variant-numeric: tabular-nums;
  }
  .orig {
    font-size: 0.85rem;
  }
  .overview {
    margin: 0.2rem 0 0;
    color: var(--muted);
    font-size: 0.875rem;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
  .none {
    padding: 1rem 0.5rem;
  }
  .by-id {
    margin: 0.75rem 0.5rem 0;
    font-size: 0.9rem;
  }
  .link-btn {
    padding: 0;
    font: inherit;
    color: var(--text);
    background: none;
    border: 0;
    text-decoration: underline;
    text-decoration-color: color-mix(in oklab, var(--tint) 70%, transparent);
    text-underline-offset: 0.2em;
    cursor: pointer;
  }
  @media (max-width: 600px) {
    .search {
      grid-template-columns: 1fr 4.5rem;
    }
    .search .btn {
      grid-column: 1 / -1;
    }
  }
</style>
