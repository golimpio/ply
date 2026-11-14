<script lang="ts">
  import { api, ApiError, mergedInto, type Person, type PersonFilmographyEntry } from '../lib/api';
  import {
    cardHref,
    countUniqueTitles,
    groupCredits,
    imdbPersonUrl,
    imdbTitleUrl,
    lifeSpan,
    splitCreditsByMedia,
    tmdbTitleUrl,
    wikipediaUrl,
  } from '../lib/credits';
  import { backdropOf, posterImg, prefersPosters, profileImg } from '../lib/meta';
  import { resource } from '../lib/resource.svelte';
  import { href, redirect } from '../lib/router.svelte';
  import { openSearch } from '../lib/palette.svelte';
  import { titleBadge } from '../lib/library.svelte';
  import BackButton from '../components/BackButton.svelte';
  import Dialog from '../components/Dialog.svelte';
  import MediaCard from '../components/MediaCard.svelte';
  import Picture from '../components/Picture.svelte';
  import Rating from '../components/Rating.svelte';

  let { id }: { id: number } = $props();

  // A person of TVDB that TMDB knows too is merged into the TMDB person the first time the page
  // is opened: that answer is the survivor's (another id), and after it the old id is a 404
  // {"error": "merged", "into": N}. Any other 404 is a person the library doesn't have.
  type Missing = { into: number };
  const detail = resource<Person | Missing>(() =>
    api.person(id).catch((e: unknown) => {
      if (e instanceof ApiError && e.status === 404) return { into: mergedInto(e) };
      throw e;
    }),
  );
  const person = $derived(detail.data && !('into' in detail.data) ? detail.data : undefined);
  const missing = $derived(detail.data && 'into' in detail.data ? detail.data : undefined);
  $effect(() => {
    // The page's address follows the merge, so that it can be kept and shared.
    const to = missing?.into || (person && person.id !== id ? person.id : 0);
    if (to && to !== id) redirect(href.person(to));
  });

  // Only credits that can be opened: a film with a file, a series with a show.
  const credits = $derived((person?.credits ?? []).filter((c) => cardHref(c.title)));
  const media = $derived(splitCreditsByMedia(credits));
  const filmSections = $derived(groupCredits(media.films));
  const seriesSections = $derived(groupCredits(media.series));
  const filmCount = $derived(countUniqueTitles(media.films));
  const seriesCount = $derived(countUniqueTitles(media.series));

  // Most of a person's titles without a poster (no TMDB key) read better as wide cards.
  const wide = $derived(!prefersPosters([...new Map(credits.map((c) => [c.title.id, c.title])).values()]));

  const life = $derived(lifeSpan(person?.birthday, person?.deathday));
  const knownFor = $derived(person?.knownFor ? `Known for ${person.knownFor.toLowerCase()}` : '');
  const wikiUrl = $derived(person ? wikipediaUrl(person.name, person.wikidataId) : '');
  const imdbUrl = $derived(person ? imdbPersonUrl(person.imdbId) : '');

  // External filmography (lazy-loaded in the background from local cache/TMDB).
  const filmography = resource<PersonFilmographyEntry[]>(() => api.personFilmography(id));
  const rawExternal = $derived(filmography.data ?? []);
  const libraryTitleIds = $derived(new Set(credits.map((c) => c.title.id)));
  const externalList = $derived(
    rawExternal.filter((e) => !e.inLibrary && (e.tmdbId ? !libraryTitleIds.has(e.tmdbId) : true)),
  );

  // A shelf per kind, so that a card needn't say which it is. The server calls a series 'series';
  // anything else is a film. A kind with nothing in it gets no shelf at all.
  const moreSections = $derived(
    [
      { key: 'films', label: 'films', entries: externalList.filter((e) => e.kind !== 'series') },
      { key: 'series', label: 'series', entries: externalList.filter((e) => e.kind === 'series') },
    ].filter((s) => s.entries.length > 0),
  );

  let selectedEntry = $state<PersonFilmographyEntry | null>(null);
  let dialogOpen = $state(false);
  function openDetails(entry: PersonFilmographyEntry) {
    selectedEntry = entry;
    dialogOpen = true;
  }

  // The biography stops at six lines until asked for more.
  let bioEl = $state<HTMLElement>();
  let expanded = $state(false);
  let clipped = $state(false);
  $effect(() => {
    const el = bioEl;
    if (!el) return;
    const measure = () => {
      if (!expanded) clipped = el.scrollHeight > el.clientHeight + 1;
    };
    measure();
    const watch = new ResizeObserver(measure);
    watch.observe(el);
    return () => watch.disconnect();
  });
</script>

{#if missing?.into}
  <BackButton over />
  <div class="page center">
    <p>This person was merged into another.</p>
    <p><a class="btn" href={href.person(missing.into)}>Open their page</a></p>
  </div>
{:else if missing}
  <BackButton over />
  <div class="page center">
    <p>This person isn’t in the library.</p>
    <p><button class="btn" onclick={openSearch}>Search for them</button></p>
  </div>
{:else if !person && detail.error}
  <BackButton over />
  <div class="page center">
    <p class="error">{detail.error}</p>
    <p><a href={href.home}>Back to the library</a></p>
  </div>
{:else if !person}
  <div class="page" aria-busy="true">
    <BackButton />
    <div class="head">
      <div class="skeleton portrait sk-portrait"></div>
      <div class="who">
        <div class="skeleton sk-name"></div>
        <div class="skeleton sk-line"></div>
        <div class="skeleton sk-line short"></div>
      </div>
    </div>
    <div class="poster-grid">
      {#each Array(6) as _, i (i)}<div class="skeleton sk-card"></div>{/each}
    </div>
  </div>
{:else}
  <div class="page">
    <BackButton />
    <header class="head">
      <div class="portrait">
        <Picture img={profileImg(person)} ratio="2 / 3" label={person.name} sizes="192px" eager />
      </div>
      <div class="who">
        <h1>{person.name}</h1>
        {#if life || person.placeOfBirth || knownFor}
          <div class="meta">
            {#if life}<span>{life}</span>{/if}
            {#if person.placeOfBirth}<span>{person.placeOfBirth}</span>{/if}
            {#if knownFor}<span>{knownFor}</span>{/if}
          </div>
        {/if}
        {#if wikiUrl || imdbUrl}
          <div class="links">
            {#if wikiUrl}
              <a class="chip ext-link" href={wikiUrl} target="_blank" rel="noopener noreferrer">
                <span>Wikipedia</span>
                <span class="arrow" aria-hidden="true">↗</span>
              </a>
            {/if}
            {#if imdbUrl}
              <a class="chip ext-link" href={imdbUrl} target="_blank" rel="noopener noreferrer">
                <span>IMDb</span>
                <span class="arrow" aria-hidden="true">↗</span>
              </a>
            {/if}
          </div>
        {/if}
        {#if person.biography}
          <div class="bio">
            <p id="bio" bind:this={bioEl} class:clamped={!expanded}>{person.biography}</p>
            {#if clipped}
              <button class="more link" aria-expanded={expanded} aria-controls="bio" onclick={() => (expanded = !expanded)}>
                {expanded ? 'Read less' : 'Read more'}
              </button>
            {/if}
          </div>
        {/if}
      </div>
    </header>

    {#if filmSections.length > 0}
      <section class="shelf" aria-labelledby="shelf-films">
        <h2 id="shelf-films" class="library">In your films <span class="n">{filmCount}</span></h2>
        {#each filmSections as section (section.group)}
          <section class="group" aria-labelledby="group-film-{section.group}">
            {#if filmSections.length > 1}
              <h3 id="group-film-{section.group}">{section.group}<span class="n">{section.entries.length}</span></h3>
            {/if}
            <div class={wide ? 'grid' : 'poster-grid'}>
              {#each section.entries as { card, detail: role } (card.id)}
                <MediaCard
                  shape={wide ? 'wide' : 'poster'}
                  img={wide ? backdropOf(card) : posterImg(card)}
                  href={cardHref(card) ?? href.home}
                  title={card.title}
                  sub={[card.year || undefined, role]}
                  rating={card.rating}
                  watch={titleBadge(card)}
                />
              {/each}
            </div>
          </section>
        {/each}
      </section>
    {/if}

    {#if seriesSections.length > 0}
      <section class="shelf {filmSections.length > 0 ? 'shelf-divider' : ''}" aria-labelledby="shelf-series">
        <h2 id="shelf-series" class="library">In your series <span class="n">{seriesCount}</span></h2>
        {#each seriesSections as section (section.group)}
          <section class="group" aria-labelledby="group-series-{section.group}">
            {#if seriesSections.length > 1}
              <h3 id="group-series-{section.group}">{section.group}<span class="n">{section.entries.length}</span></h3>
            {/if}
            <div class={wide ? 'grid' : 'poster-grid'}>
              {#each section.entries as { card, detail: role } (card.id)}
                <MediaCard
                  shape={wide ? 'wide' : 'poster'}
                  img={wide ? backdropOf(card) : posterImg(card)}
                  href={cardHref(card) ?? href.home}
                  title={card.title}
                  sub={[card.year || undefined, role]}
                  rating={card.rating}
                  watch={titleBadge(card)}
                />
              {/each}
            </div>
          </section>
        {/each}
      </section>
    {/if}

    {#if filmSections.length === 0 && seriesSections.length === 0}
      <p class="muted">None of their work is in your library right now.</p>
    {/if}

    {#each moreSections as section (section.key)}
      <section class="shelf shelf-divider" aria-labelledby="more-{section.key}">
        <h2 id="more-{section.key}" class="library">More {section.label} with {person.name} <span class="n">{section.entries.length}</span></h2>
        <div class="poster-grid">
          {#each section.entries as entry (entry.tmdbId || entry.tvdbId || entry.title)}
            <MediaCard
              shape="poster"
              img={posterImg(entry)}
              href={`#filmography-${entry.tmdbId || entry.tvdbId || entry.title}`}
              title={entry.title}
              sub={[entry.year || undefined, entry.character || entry.job || entry.department]}
              rating={entry.rating ? { source: 'tmdb', value: entry.rating, scale: 10, score100: Math.round(entry.rating * 10), url: entry.tmdbId ? tmdbTitleUrl(entry.kind, entry.tmdbId) : (entry.imdbId ? imdbTitleUrl(entry.imdbId) : '') } : null}
              onclick={() => openDetails(entry)}
            />
          {/each}
        </div>
      </section>
    {/each}
  </div>

  {#if selectedEntry}
    <Dialog bind:open={dialogOpen} title={selectedEntry.title}>
      <div class="entry-modal">
        <div class="entry-poster">
          <Picture img={posterImg(selectedEntry)} ratio="2 / 3" label={selectedEntry.title} sizes="160px" />
        </div>
        <div class="entry-info">
          <div class="entry-meta">
            <span class="badge">{selectedEntry.kind === 'series' ? 'Series' : 'Film'}</span>
            {#if selectedEntry.year}<span>{selectedEntry.year}</span>{/if}
            {#if selectedEntry.rating}
              <Rating entry={{ source: 'tmdb', value: selectedEntry.rating, scale: 10, score100: Math.round(selectedEntry.rating * 10), url: selectedEntry.tmdbId ? tmdbTitleUrl(selectedEntry.kind, selectedEntry.tmdbId) : (selectedEntry.imdbId ? imdbTitleUrl(selectedEntry.imdbId) : '') }} />
            {/if}
            {#if selectedEntry.episodeCount}
              <span>{selectedEntry.episodeCount} episodes</span>
            {/if}
          </div>
          {#if selectedEntry.character}
            <p class="entry-role"><strong>Role:</strong> {selectedEntry.character}</p>
          {:else if selectedEntry.job}
            <p class="entry-role"><strong>Role:</strong> {selectedEntry.job}</p>
          {/if}
          {#if selectedEntry.originalTitle && selectedEntry.originalTitle !== selectedEntry.title}
            <p class="entry-orig">Original title: <em>{selectedEntry.originalTitle}</em></p>
          {/if}
          <div class="entry-actions">
            {#if selectedEntry.imdbId}
              <a class="btn pill subtle" href={imdbTitleUrl(selectedEntry.imdbId)} target="_blank" rel="noopener noreferrer">
                <span>IMDb</span>
                <span class="arrow" aria-hidden="true">↗</span>
              </a>
            {/if}
            {#if selectedEntry.tmdbId}
              <a class="btn pill subtle" href={tmdbTitleUrl(selectedEntry.kind, selectedEntry.tmdbId)} target="_blank" rel="noopener noreferrer">
                <span>TMDB</span>
                <span class="arrow" aria-hidden="true">↗</span>
              </a>
            {/if}
            <button class="btn pill" onclick={() => { dialogOpen = false; openSearch(); }}>
              Search library
            </button>
          </div>
        </div>
      </div>
    </Dialog>
  {/if}
{/if}

<style>
  .head {
    display: flex;
    align-items: flex-start;
    gap: 1.5rem 2.5rem;
    margin-bottom: 2.75rem;
  }
  .portrait {
    flex: none;
    width: 12rem;
    border-radius: var(--radius);
    overflow: hidden;
    box-shadow: var(--ring-hairline);
  }
  .portrait :global(.tile) {
    font-size: var(--fs-4xl);
  }
  .who {
    min-width: 0;
    flex: 1;
    max-width: 46rem;
    display: grid;
    gap: 1rem;
    align-content: start;
  }
  .links {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
  }
  .ext-link {
    text-decoration: none;
    font-size: var(--fs-xs);
    min-height: 1.85rem;
    padding: 0.2rem 0.75rem;
  }
  .arrow {
    font-size: var(--fs-xs);
    opacity: 0.7;
  }
  .bio {
    display: grid;
    justify-items: start;
    gap: 0.5rem;
  }
  .bio p {
    margin: 0;
    max-width: 65ch;
    line-height: var(--lh-relaxed);
    white-space: pre-line;
  }
  .bio p.clamped {
    display: -webkit-box;
    -webkit-line-clamp: 6;
    line-clamp: 6;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
  .more {
    padding: 0;
    font: inherit;
    font-weight: var(--fw-semibold-2);
    background: none;
    border: 0;
    cursor: pointer;
  }
  .shelf-divider {
    border-top: 1px solid var(--line);
    padding-top: var(--shelf-pad-y);
    margin-top: var(--shelf-pad-y);
  }
  .library {
    margin-bottom: 1.5rem;
  }
  .group + .group {
    margin-top: 2.5rem;
  }
  h3 {
    margin-bottom: 1rem;
    font-size: var(--fs-lg);
    font-weight: var(--fw-semibold-2);
  }
  .n {
    margin-left: 0.6rem;
    font-weight: var(--fw-regular);
    color: var(--text-muted);
    font-variant-numeric: tabular-nums;
  }
  .sk-portrait {
    aspect-ratio: 2 / 3;
  }
  .sk-name {
    height: 2.6rem;
    width: min(18rem, 80%);
  }
  .sk-line {
    height: 1.1rem;
    max-width: 40rem;
  }
  .sk-line.short {
    width: 60%;
  }

  .entry-modal {
    display: flex;
    gap: 1.5rem;
    align-items: flex-start;
  }
  .entry-poster {
    flex: none;
    width: 9rem;
    border-radius: var(--radius);
    overflow: hidden;
    box-shadow: var(--ring-hairline);
  }
  .entry-info {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }
  .entry-meta {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.65rem;
    font-size: var(--fs-sm);
    color: var(--text-muted);
  }
  .entry-role,
  .entry-orig {
    margin: 0;
    font-size: var(--fs-sm);
    color: var(--text-muted);
  }
  .entry-role strong {
    color: var(--text);
  }
  .entry-actions {
    display: flex;
    flex-wrap: wrap;
    gap: 0.65rem;
    margin-top: 0.5rem;
  }

  @media (max-width: 700px) {
    .head {
      flex-direction: column;
      margin-bottom: 2.25rem;
    }
    .portrait {
      width: 9.5rem;
    }
    .who {
      width: 100%;
    }
    /* As the shared .poster-grid below 600 px, from 700 px here (the head stacks there too). */
    .poster-grid {
      grid-template-columns: repeat(auto-fill, minmax(min(140px, 100%), 1fr));
      gap: 1.25rem 0.75rem;
    }
  }

  @media (max-width: 600px) {
    .entry-modal {
      flex-direction: column;
      align-items: center;
      text-align: center;
    }
    .entry-actions {
      justify-content: center;
    }
  }
</style>
