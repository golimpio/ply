<script lang="ts">
  import { api, type PersonFilmographyEntry, type TitlePreview } from '../lib/api';
  import { entryRating, imdbTitleUrl, tmdbTitleUrl } from '../lib/credits';
  import { CREDIT_LINES, posterImg, runtimeLabel } from '../lib/meta';
  import { openSearch } from '../lib/palette.svelte';
  import Dialog from './Dialog.svelte';
  import PersonCard from './PersonCard.svelte';
  import Picture from './Picture.svelte';
  import Rating from './Rating.svelte';

  // A person's credit outside the library: what the filmography says of it at once (poster, year,
  // the part), and more from TMDB as it comes in the background (plot, people, genres). The dialog
  // can be closed meanwhile, which drops the fetch; once the details are in, it grows to show them.
  let { entry, open = $bindable(false) }: { entry: PersonFilmographyEntry; open: boolean } = $props();

  let preview = $state<TitlePreview | null>(null);
  let loading = $state(false);
  let failed = $state(false);

  $effect(() => {
    const e = entry;
    // Nothing of the last credit is shown for this one, even one TMDB can't be asked about.
    preview = null;
    failed = false;
    loading = false;
    if (!open || !e.tmdbId) return;
    const stop = new AbortController();
    loading = true;
    api
      .titlePreview(e.kind === 'series' ? 'series' : 'movie', e.tmdbId, stop.signal)
      .then((p) => {
        if (!stop.signal.aborted) preview = p;
      })
      .catch(() => {
        if (!stop.signal.aborted) failed = true;
      })
      .finally(() => {
        if (!stop.signal.aborted) loading = false;
      });
    // Closed, or another credit opened: the answer is no longer wanted.
    return () => stop.abort();
  });

  const rating = $derived(entryRating(entry));
  const role = $derived(entry.character || entry.job || '');
  const imdbId = $derived(entry.imdbId || preview?.imdbId || '');
  const runtime = $derived(runtimeLabel(preview?.runtimeMin));
  // The credit lines of a title's page: "Director  Bernardo Bertolucci".
  const lines = $derived(
    CREDIT_LINES.map((l) => {
      const names = [...new Set((preview?.crew ?? []).filter((c) => c.role && l.jobs.includes(c.role)).map((c) => c.name))];
      return { label: names.length > 1 ? l.plural : l.label, names };
    }).filter((l) => l.names.length),
  );
  const more = $derived(!!preview && !!(preview.overview || preview.cast.length || lines.length));
</script>

<Dialog bind:open title={entry.title} wide={more}>
  {#snippet head()}
    <div class="head">
      <div class="poster">
        <Picture img={posterImg(entry)} ratio="2 / 3" label={entry.title} sizes="128px" eager />
      </div>
      <div class="who">
        <h2 id="dialog-title" class="title">{entry.title}</h2>
        {#if entry.originalTitle && entry.originalTitle !== entry.title}
          <p class="orig">{entry.originalTitle}</p>
        {/if}
        <div class="meta">
          <span class="badge">{entry.kind === 'series' ? 'Series' : 'Film'}</span>
          {#if entry.year}<span>{entry.year}</span>{/if}
          {#if runtime}<span>{runtime}</span>{/if}
          {#if preview?.certification}<span class="badge">{preview.certification}</span>{/if}
          {#if entry.episodeCount}<span>{entry.episodeCount} episodes</span>{/if}
          <!-- Only the score: the TMDB link is the button below. -->
          {#if rating}<Rating entry={rating} compact />{/if}
        </div>
        {#if role}<p class="role"><strong>{entry.character ? 'As' : 'Job'}</strong> {role}</p>{/if}
        <div class="actions">
          {#if imdbId}
            <a class="btn pill subtle" href={imdbTitleUrl(imdbId)} target="_blank" rel="noopener noreferrer">
              <span>IMDb</span>
              <span class="ext-arrow" aria-hidden="true">↗</span>
            </a>
          {/if}
          {#if entry.tmdbId}
            <a class="btn pill subtle" href={tmdbTitleUrl(entry.kind, entry.tmdbId)} target="_blank" rel="noopener noreferrer">
              <span>TMDB</span>
              <span class="ext-arrow" aria-hidden="true">↗</span>
            </a>
          {/if}
          <button
            class="btn pill"
            onclick={() => {
              open = false;
              openSearch();
            }}
          >
            Search library
          </button>
        </div>
      </div>
    </div>
  {/snippet}

  <div class="details">
    {#if loading}
      <p class="loading" role="status"><span class="spinner" aria-hidden="true"></span>Loading details…</p>
    {:else if failed}
      <p class="muted">More details couldn’t be loaded right now.</p>
    {/if}

    {#if preview}
      {#if preview.tagline}<p class="tagline">{preview.tagline}</p>{/if}
      {#if preview.overview}<p class="overview">{preview.overview}</p>{/if}
      {#if lines.length}
        <dl class="credits">
          {#each lines as line (line.label)}
            <dt>{line.label}</dt>
            <dd>{line.names.join(', ')}</dd>
          {/each}
        </dl>
      {/if}
      {#if preview.genres.length}
        <ul class="chips" aria-label="Genres">
          {#each preview.genres as g (g)}<li><span class="chip">{g}</span></li>{/each}
        </ul>
      {/if}
      {#if preview.cast.length}
        <section aria-labelledby="preview-cast">
          <h3 id="preview-cast">Cast</h3>
          <ul class="cast">
            {#each preview.cast as c, i (i)}
              <li><PersonCard name={c.name} profile={c.profile} role={c.role} /></li>
            {/each}
          </ul>
        </section>
      {/if}
    {/if}

  </div>
</Dialog>

<style>
  .head {
    flex: 1;
    min-width: 0;
    display: flex;
    align-items: flex-start;
    gap: var(--space-1250);
  }
  .poster {
    flex: none;
    width: 8rem;
    border-radius: var(--radius);
    overflow: hidden;
    box-shadow: var(--ring-hairline);
  }
  .who {
    min-width: 0;
    display: grid;
    gap: var(--space-0600);
    padding-top: var(--space-0250);
  }
  .title,
  .details p {
    margin: 0;
  }
  .orig {
    margin: 0;
    color: var(--text-muted);
    font-style: italic;
  }
  .meta,
  .role,
  .loading {
    font-size: var(--fs-sm);
    color: var(--text-muted);
  }
  .meta {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-0500) var(--space-0750);
  }
  .role {
    margin: 0;
  }
  .role strong {
    color: var(--text);
    margin-right: var(--space-0250);
  }
  .details {
    display: grid;
    /* The cast scrolls inside the column: its row of cards must not widen the dialog. */
    grid-template-columns: minmax(0, 1fr);
    gap: var(--space-1100);
    padding-top: var(--space-0500);
  }
  .loading {
    display: flex;
    align-items: center;
    gap: var(--space-0600);
  }
  .tagline {
    font-family: var(--font-display);
    font-style: italic;
    font-size: var(--fs-lg);
    color: var(--text-muted);
  }
  .overview {
    line-height: var(--lh-relaxed);
    max-width: 65ch;
  }
  .chips {
    margin: 0;
    padding: 0;
    list-style: none;
  }
  h3 {
    margin: 0 0 var(--space-0850);
    font-size: var(--fs-sm);
    font-weight: var(--fw-semibold-2);
  }
  .cast {
    list-style: none;
    margin: 0;
    padding: 0 0 var(--space-0250);
    display: grid;
    grid-auto-flow: column;
    grid-auto-columns: 5.5rem;
    gap: var(--space-1000);
    overflow-x: auto;
    scrollbar-width: thin;
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-0600);
    padding-top: var(--space-0250);
  }
  @media (max-width: 600px) {
    .poster {
      width: 6rem;
    }
  }
</style>
