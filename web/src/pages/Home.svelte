<script lang="ts">
  import {
    api,
    type GenreCount,
    type ItemDetail,
    type ItemSummary,
    type MovieCard,
    type Settings,
    type TitleCard,
    type UpcomingShow,
  } from '../lib/api';
  import { useAmbient } from '../lib/ambient.svelte';
  import { detail } from '../lib/details.svelte';
  import { episodeLabel, formatDuration } from '../lib/format';
  import { byName } from '../lib/az';
  import {
    bothLists,
    byAdded,
    ensureLibrary,
    episodesOf,
    findItem,
    libraryRows,
    movies,
    titleBadge,
    type LibraryRow,
  } from '../lib/library.svelte';
  import {
    airLabel,
    crewWith,
    groupByTitle,
    logoSrc,
    onePerTitle,
    posterImg,
    prefersPosters,
    showWideImg,
    titleHref,
  } from '../lib/meta';
  import { ratingScore, ratingValue, titleRatings } from '../lib/ratings';
  import { morph } from '../lib/nav';
  import { href } from '../lib/router.svelte';
  import { app, rescan, scan } from '../lib/state.svelte';
  import { artOf, progressOf } from '../lib/ui-types';
  import Art, { type ArtItem } from '../components/Art.svelte';
  import Badges from '../components/Badges.svelte';
  import HeroTitle from '../components/HeroTitle.svelte';
  import Icon from '../components/Icon.svelte';
  import MediaCard from '../components/MediaCard.svelte';
  import Rail from '../components/Rail.svelte';

  let continueItems = $state<ItemSummary[]>([]);
  let upcoming = $state<UpcomingShow[]>([]);
  let genreRails = $state<{ genre: GenreCount; titles: TitleCard[] }[]>([]);
  let settings = $state<Settings>();

  // Interactive Evening Lens & Curated Focus state (Variation II: Living Repertory & Moods)
  type MoodLens = 'weeknight' | 'double' | 'noir' | 'epic' | 'unwatched' | 'series';
  let moodLens = $state<MoodLens>('weeknight');
  let heroOffset = $state(0);
  let focusFilter = $state('All');

  const LENSES: [MoodLens, string, string][] = [
    ['weeknight', 'Tonight (≤100m)', 'Tonight’s Weeknight Pick · Under 100m'],
    ['double', 'Double Feature', 'Thematic Double Feature · Feature I of II'],
    ['noir', 'Late Night Noir & Sci-Fi', 'Late Night Lens · Nocturnal & Sci-Fi'],
    ['epic', 'Sunday Epics (2h 20m+)', 'Sunday Matinee · Wide-Canvas Feature'],
    ['unwatched', 'Unwatched Gems', 'Unwatched Archive Gem · Critics’ Choice'],
    ['series', 'Series Spotlight (≥30m)', 'Films & Series · Serial Spotlight (≥30m)'],
  ];

  $effect(() => {
    void app.libraryVersion;
    void ensureLibrary();
    api.continueWatching().then(
      (list) => (continueItems = list),
      () => (continueItems = []),
    );
    api.upcoming().then(
      (list) => (upcoming = list),
      () => (upcoming = []), // the metadata is extra: the page works without it
    );
  });

  // Curated genre showcases mixing both films and series.
  $effect(() => {
    void app.libraryVersion;
    let stale = false;
    api
      .genres()
      .then(async (genres) => {
        const top = genres.filter((g) => g.count >= 4).sort((a, b) => b.count - a.count).slice(0, 3);
        const rails = await Promise.all(
          top.map(async (genre) => ({ genre, titles: uniqueTitles(await api.titles({ genre: genre.id })) })),
        );
        if (!stale) genreRails = rails.filter((r) => r.titles.length);
      })
      .catch(() => {
        if (!stale) genreRails = []; // the metadata is extra: the page works without it
      });
    return () => {
      stale = true;
    };
  });

  // One card per title: a repeated key would stop the whole page rendering.
  function uniqueTitles(list: TitleCard[]): TitleCard[] {
    const seen = new Set<string>();
    return list.filter((t) => !seen.has(`${t.kind}:${t.id}`) && !!seen.add(`${t.kind}:${t.id}`));
  }

  // The "set up metadata" card, until someone sets a key or waves it away.
  const DISMISSED = 'ply.setup-dismissed';
  let dismissed = $state(readDismissed());
  function readDismissed(): boolean {
    try {
      return localStorage.getItem(DISMISSED) === '1';
    } catch {
      return false;
    }
  }
  function dismiss() {
    dismissed = true;
    try {
      localStorage.setItem(DISMISSED, '1');
    } catch {
      // storage unavailable: hidden for this visit only
    }
  }
  $effect(() => {
    void app.libraryVersion;
    if (dismissed) return;
    api.settings().then(
      (s) => (settings = s),
      () => (settings = undefined),
    );
  });
  const showSetup = $derived(
    !dismissed && !!settings && !settings.tmdbKeySet && settings.metadataEnabled && !settings.metadataForcedOff,
  );

  const rows = $derived(libraryRows());
  // One card per film: its newest version in Recently added, and in Continue
  // the version played last (the server lists the most recent first).
  const recent = $derived(onePerTitle([...rows].sort(byAdded)).slice(0, 24));
  // A file watched since the list came (marked, here or on another page, or played to the end): gone
  // from it at once, as the server leaves it out from then on.
  const continuing = $derived(
    onePerTitle(continueItems.filter((i) => !((findItem(i.id) ?? i).progress?.watched ?? false))),
  );
  const films = $derived(groupByTitle([...movies.rows]).sort((a, b) => byName(a.item, b.item)));
  const moviePosters = $derived(prefersPosters(films.map((g) => g.item)));

  // Cultural & Language preference scoring (European languages & Australian English)
  const AUS_EURO_GEO =
    /australi|melbourne|sydney|brisbane|outback|queensland|perth|italy|italia|chiara|france|french|germany|deutschland|spain|españa|europe|nordic|scandi/i;

  function scorePreference(item: LibraryRow): number {
    let score = 0;
    // In-progress items from continue watching
    if (item.progress && !item.progress.watched && item.progress.positionSec > 60) score += 50;
    // Unwatched items preferred over fully watched
    if (!item.watched) score += 20;
    // Ratings quality boost
    if ('rating' in item && item.rating?.score100) score += item.rating.score100 / 10;
    else if ('ratings' in item && item.ratings?.[0]?.score100) score += item.ratings[0].score100 / 10;
    // European & Australian preference
    const text = `${item.title} ${'show' in item ? item.show : ''} ${'episodeTitle' in item ? item.episodeTitle : ''} ${'overview' in item ? item.overview : ''}`;
    if (AUS_EURO_GEO.test(text)) score += 35;
    if ('genres' in item && item.genres?.some((g: string) => /world|foreign|european|drama|independent/i.test(g))) score += 25;
    return score;
  }

  // Candidate pool for the Evening Lens hero rotation.
  const lensPool = $derived.by((): LibraryRow[] => {
    const allMovieRows: LibraryRow[] = movies.rows.length ? [...movies.rows] : rows.filter((i) => i.kind === 'movie');
    const movieCandidates = allMovieRows.filter((i) => i.durationSec >= 3600);
    const moviePool = movieCandidates.length ? movieCandidates : allMovieRows;

    const allEpisodeRows = rows.filter((i) => i.kind === 'episode');
    const episodeCandidates = allEpisodeRows.filter((i) => i.durationSec >= 1800);

    if (moodLens === 'series') {
      const candidates = episodeCandidates.length ? episodeCandidates : allEpisodeRows;
      if (!candidates.length) return [];
      return [...candidates].sort((a, b) => scorePreference(b) - scorePreference(a) || b.addedAt - a.addedAt);
    }

    if (!moviePool.length) return recent.filter((i) => i.kind === 'movie');

    let filtered: LibraryRow[] = [];
    if (moodLens === 'weeknight') {
      // Feature films under 100 minutes (>= 60m and <= 100m)
      filtered = moviePool.filter((i) => i.durationSec >= 3600 && i.durationSec <= 6000);
      if (!filtered.length) filtered = moviePool.filter((i) => i.durationSec > 0 && i.durationSec <= 6100);
    } else if (moodLens === 'noir') {
      filtered = moviePool.filter(
        (i) => 'genres' in i && i.genres?.some((g: string) => /sci-fi|science fiction|thriller|crime|mystery|noir/i.test(g)),
      );
    } else if (moodLens === 'epic') {
      filtered = moviePool.filter((i) => i.durationSec >= 7800);
    } else if (moodLens === 'unwatched') {
      filtered = moviePool.filter((i) => !i.watched);
    } else if (moodLens === 'double') {
      filtered = moviePool;
    }

    const pool = filtered.length ? filtered : moviePool;
    return [...pool].sort((a, b) => scorePreference(b) - scorePreference(a) || b.addedAt - a.addedAt);
  });

  const lead = $derived.by((): LibraryRow | undefined => {
    if (lensPool.length > 0) {
      return lensPool[heroOffset % lensPool.length];
    }
    if (moodLens === 'series') {
      const allEpisodeRows = rows.filter((i) => i.kind === 'episode');
      return allEpisodeRows[0] ?? recent.find((i) => i.kind === 'episode');
    }
    const allMovieRows: LibraryRow[] = movies.rows.length ? [...movies.rows] : rows.filter((i) => i.kind === 'movie');
    return (
      allMovieRows.find((i) => 'backdrop' in i && i.backdrop) ??
      allMovieRows[0] ??
      recent.find((i) => i.kind === 'movie')
    );
  });

  /** What the hero reads: a full row, or a film's card when its detail could not be had. */
  type HeroItem = ArtItem &
    Pick<
      ItemSummary,
      | 'id'
      | 'kind'
      | 'title'
      | 'show'
      | 'year'
      | 'season'
      | 'episode'
      | 'episodeTitle'
      | 'certification'
      | 'logo'
      | 'genres'
      | 'durationSec'
      | 'width'
      | 'height'
      | 'hdr'
      | 'progress'
      | 'watched'
    >;

  const leadId = $derived(lead?.id);
  let full = $state<{ id: number; item: ItemDetail | null }>();
  $effect(() => {
    const id = leadId;
    if (id === undefined) return;
    const ctl = new AbortController();
    const d = detail(id, ctl.signal);
    if (d.cached) full = { id, item: d.cached };
    d.fresh.then(
      (item) => {
        if (!ctl.signal.aborted) full = { id, item };
      },
      () => {
        if (!ctl.signal.aborted && full?.id !== id) full = { id, item: null };
      },
    );
    return () => ctl.abort();
  });
  const feature = $derived.by((): HeroItem | undefined => {
    if (!lead) return undefined;
    if (full?.id !== lead.id) return undefined;
    return full.item ? { ...full.item, watched: lead.watched, progress: lead.progress } : lead;
  });
  const featureDetail = $derived(full?.id === lead?.id ? full?.item : null);
  const featureLogo = $derived(feature?.kind === 'movie' ? logoSrc(feature) : '');
  const featureDirector = $derived.by(() => {
    if (feature?.kind === 'episode') return '';
    const dirs = crewWith(featureDetail?.meta, ['Director']);
    return dirs.length ? dirs.slice(0, 2).map((d) => d.name).join(', ') : '';
  });
  const featureTagline = $derived(featureDetail?.meta?.tagline || '');
  const featureScores = $derived.by(() => {
    const fallback = lead && 'rating' in lead ? lead : null;
    const list = titleRatings(featureDetail?.meta, fallback);
    const labels: Record<string, string> = {
      imdb: 'IMDb',
      rt_critics: 'RT',
      rt_audience: 'Audience',
      metacritic: 'MC',
      letterboxd: 'LB',
      tmdb: 'TMDB',
      trakt: 'Trakt',
    };
    return list.slice(0, 3).map((r) => ({
      val: ratingValue(r),
      label: labels[r.source] ?? r.source.toUpperCase(),
    }));
  });

  // Companion film for the borderless Double Bill Pairing footnote.
  const companion = $derived.by((): MovieCard | undefined => {
    if (!feature) return undefined;
    const allFilms = films.map((g) => g.item).filter((m) => m.id !== feature.id);
    if (!allFilms.length) return undefined;
    const fGenres = new Set(feature.genres ?? []);
    const byGenre = allFilms.find((m) => m.genres?.some((g) => fGenres.has(g)));
    return byGenre ?? allFilms[0];
  });

  const lensKicker = $derived(LENSES.find(([k]) => k === moodLens)?.[2] ?? LENSES[0][2]);

  function endsAtLabel(sec: number): string {
    if (!sec || sec <= 0) return '';
    const end = new Date(Date.now() + sec * 1000);
    const hh = String(end.getHours()).padStart(2, '0');
    const mm = String(end.getMinutes()).padStart(2, '0');
    return `Ends ${hh}:${mm}`;
  }

  const featureRemainingSec = $derived.by(() => {
    if (!feature?.durationSec) return 0;
    const pos = resumeAt(feature);
    return Math.max(feature.durationSec - pos, 60);
  });
  const featureEndsAt = $derived(endsAtLabel(featureRemainingSec));
  const companionEndsAt = $derived.by(() => {
    if (!companion) return '';
    const combined = (featureRemainingSec || 0) + (companion.durationSec || 0);
    return combined > 0 ? `Combined ${endsAtLabel(combined).toLowerCase()}` : formatDuration(companion.durationSec);
  });
  const companionNote = $derived.by(() => {
    if (!companion || !feature) return '';
    const shared = companion.genres?.find((g) => feature.genres?.includes(g));
    return shared ? `Paired ${shared.toLowerCase()} study from your archive` : 'Complementary screening from your archive';
  });

  // Curated Chapter 01: Auteur & Critics Focus with interactive genre/theme pivot pills
  const focusOptions = $derived.by((): string[] => {
    const counts = new Map<string, number>();
    for (const g of films) {
      for (const name of g.item.genres ?? []) {
        counts.set(name, (counts.get(name) ?? 0) + 1);
      }
    }
    const top = [...counts.entries()]
      .sort((a, b) => b[1] - a[1])
      .slice(0, 5)
      .map(([name]) => name);
    return ['All', ...top];
  });

  const acclaimedFilms = $derived.by(() => {
    const base = films.map((g) => g.item);
    const filtered =
      focusFilter === 'All'
        ? base
        : [...base.filter((m) => m.genres?.includes(focusFilter))].sort((a, b) => ratingScore(b) - ratingScore(a) || byName(a, b));
    return (filtered.length ? filtered : base).slice(0, 30);
  });

  // Curated Chapter 02: Acclaimed & Under 100 Minutes (Weeknight Duration)
  const shortFeatures = $derived.by(() => {
    const under100 = films
      .map((g) => g.item)
      .filter((m) => m.durationSec > 0 && m.durationSec <= 6100);
    return under100.slice(0, 16);
  });

  useAmbient(() => artOf(lead)?.color);

  const isEpisode = (i: Pick<ItemSummary, 'kind'>) => i.kind === 'episode';
  const cardTitle = (i: Pick<ItemSummary, 'kind' | 'title' | 'show'>) => (isEpisode(i) ? i.show || i.title : i.title);
  const cardSub = (i: Pick<ItemSummary, 'kind' | 'season' | 'episode' | 'episodeTitle' | 'year' | 'durationSec'>) =>
    isEpisode(i) ? [episodeLabel(i.season, i.episode), i.episodeTitle] : [i.year, formatDuration(i.durationSec)];
  const resumeAt = (i: Pick<ItemSummary, 'progress' | 'durationSec'>) => (progressOf(i) > 0 ? (i.progress?.positionSec ?? 0) : 0);

  function leftLabel(i: Pick<ItemSummary, 'durationSec' | 'progress'>): string {
    const rem = Math.max((i.durationSec || 0) - (i.progress?.positionSec ?? 0), 0);
    return rem > 0 ? `${formatDuration(rem)} remaining` : 'In progress';
  }
</script>

{#if bothLists.error && !bothLists.loaded}
  <div class="page center">
    <p class="error">{bothLists.error}</p>
    <button class="btn" onclick={() => ensureLibrary()}>Try again</button>
  </div>
{:else if !bothLists.loaded}
  <div class="loading" aria-busy="true" aria-label="Loading the library">
    <div class="skeleton hero-sk"></div>
    <div class="row-sk">
      {#each Array(5) as _, i (i)}<div class="skeleton card-sk"></div>{/each}
    </div>
  </div>
{:else if rows.length === 0}
  <div class="page center empty">
    <h1>Nothing to watch yet</h1>
    <p class="muted">
      Start ply with <code>-media /path/to/movies</code>, then rescan. Episodes named like
      <code>Show S01E02</code> are grouped into shows.
    </p>
    <button class="btn primary" onclick={rescan} disabled={scan.running}>
      {scan.running ? 'Scanning…' : 'Rescan the library'}
    </button>
  </div>
{:else}
  <div class="home-stage">
    <!-- Interactive Evening Programming Lens Ribbon -->
    <div class="lens-bar" role="region" aria-label="Evening programming lens">
      <div class="lens-pills">
        <span class="lens-label">Films & Series:</span>
        {#each LENSES as [id, label] (id)}
          <button
            type="button"
            class="lens-pill"
            class:active={moodLens === id}
            aria-pressed={moodLens === id}
            onclick={() => {
              moodLens = id;
              heroOffset = 0;
            }}
          >
            {label}
          </button>
        {/each}
      </div>
      <button type="button" class="shuffle-btn" onclick={() => heroOffset++}>
        ↻ Shuffle Screening Suggestion
      </button>
    </div>

    <!-- Top Asymmetric Editorial Spread: Featured Screening + Editorial Resume Desk -->
    <section class="spread hero">
      <div class="spread-feature">
        {#if feature}
          <div class="kicker tabular">
            <span class="kicker-accent">● {lensKicker}</span>
            {#if featureEndsAt}
              <span>·</span>
              <span>Starts Now · {featureEndsAt}</span>
            {/if}
          </div>

          <div class="feature-grid">
            <a class="feature-art" href={href.item(feature.id)} onclick={morph}>
              <Art item={feature} kind="backdrop" ratio="16 / 10" label={cardTitle(feature)} eager />
            </a>

            <div class="feature-copy">
              <HeroTitle title={cardTitle(feature)} logo={featureLogo} />

              <div class="meta">
                {#if isEpisode(feature)}<span>{episodeLabel(feature.season, feature.episode)}{#if 'episodeTitle' in feature && feature.episodeTitle} · {feature.episodeTitle}{/if}</span>{/if}
                {#if feature.year}<span>{feature.year}</span>{/if}
                {#if featureDirector}<span>{featureDirector}</span>{/if}
                {#if feature.certification}<span class="cert">{feature.certification}</span>{/if}
                {#if feature.durationSec}<span>{formatDuration(feature.durationSec)}</span>{/if}
                <span class="badges"><Badges item={feature} /></span>
              </div>

              {#if featureTagline}
                <p class="feature-tagline">{featureTagline}</p>
              {:else if feature.genres?.length}
                <p class="feature-tagline">{feature.genres.slice(0, 3).join(' · ')}</p>
              {/if}

              {#if featureScores.length}
                <div class="feature-scores tabular">
                  {#each featureScores as s (s.label)}
                    <span><strong class="score-num">{s.val}</strong> {s.label}</span>
                  {/each}
                </div>
              {/if}

              <div class="hero-actions">
                <a class="btn primary" href={href.play(feature.id, resumeAt(feature))}>
                  <Icon name="play" />{resumeAt(feature) ? 'Resume' : 'Play'}{#if featureEndsAt} · {featureEndsAt}{/if}
                </a>
                <a class="btn" href={href.item(feature.id)}><Icon name="info" />Details</a>
              </div>
            </div>
          </div>

          {#if companion}
            <a class="double-bill" href={href.item(companion.id)} onclick={morph}>
              <div class="db-main">
                <span class="db-kicker">Double Bill Pairing</span>
                <span class="muted">Paired with</span>
                <strong class="db-title">{companion.title}</strong>
                {#if companion.year}<span class="db-year tabular">({companion.year})</span>{/if}
                {#if companionNote}
                  <span class="muted">—</span>
                  <span class="db-note">{companionNote}</span>
                {/if}
              </div>
              {#if companionEndsAt}
                <span class="db-ends tabular">{companionEndsAt} →</span>
              {/if}
            </a>
          {/if}
        {:else if lead}
          <div class="skeleton hero-sk"></div>
        {/if}
      </div>

      <!-- Right 5 Cols: Editorial Resume Desk -->
      <section class="rail resume-desk">
        <div class="desk-head tabular">
          <h2>Continue watching</h2>
          <span>{continuing.length ? 'In Progress' : 'Evening Queue'}</span>
        </div>

        {#if continuing.length}
          <ul class="resume-list" aria-label="Continue watching">
            {#each continuing.slice(0, 4) as i (i.id)}
              <li>
                <a class="card resume-row" href={href.play(i.id, i.progress?.positionSec ?? 0)} onclick={morph}>
                  <div class="resume-thumb">
                    <Art item={i} kind="backdrop" ratio="16 / 9" label={cardTitle(i)} />
                    <div class="resume-progress">
                      <span style:width="{Math.round(progressOf(i) * 100)}%"></span>
                    </div>
                  </div>
                  <div class="resume-body">
                    <div class="resume-top">
                      <span class="title">{cardTitle(i)}</span>
                      <span class="resume-left tabular">{leftLabel(i)}</span>
                    </div>
                    <div class="resume-sub">{cardSub(i).filter(Boolean).join(' · ')}</div>
                  </div>
                </a>
              </li>
            {/each}
          </ul>
        {:else}
          <ul class="resume-list" aria-label="Suggested screenings">
            {#each recent.slice(0, 3) as i (i.id)}
              <li>
                <a class="card resume-row" href={href.item(i.id)} onclick={morph}>
                  <div class="resume-thumb">
                    <Art item={i} kind="backdrop" ratio="16 / 9" label={cardTitle(i)} />
                  </div>
                  <div class="resume-body">
                    <div class="resume-top">
                      <span class="title">{cardTitle(i)}</span>
                      {#if i.durationSec}<span class="resume-left tabular">{endsAtLabel(i.durationSec)}</span>{/if}
                    </div>
                    <div class="resume-sub">{cardSub(i).filter(Boolean).join(' · ')}</div>
                  </div>
                </a>
              </li>
            {/each}
          </ul>
        {/if}
      </section>
    </section>

    <div class="rails">
      {#if showSetup}
        <aside class="setup" aria-labelledby="setup-h">
          <div>
            <h2 id="setup-h">Posters, plots and cast for everything</h2>
            <p class="muted">Add a free TMDB key and ply fills in the details by itself, and keeps them up to date.</p>
          </div>
          <div class="setup-actions">
            <a class="btn primary" href={href.settingsTab('keys')}>Set it up</a>
            <button class="btn" onclick={dismiss}>Not now</button>
          </div>
        </aside>
      {/if}

      {#if acclaimedFilms.length >= 2}
        <Rail
          index="01 / AUTEUR FOCUS"
          title="World & Auteur Cinema"
          items={acclaimedFilms}
          key={(m) => m.id}
          size={moviePosters ? 'poster' : 'wide'}
          more={{ href: href.movies, label: `All ${films.length}` }}
        >
          {#snippet subhead()}
            {#if focusOptions.length > 2}
              <div class="focus-pills">
                {#each focusOptions as opt (opt)}
                  <button
                    type="button"
                    class="focus-pill"
                    class:active={focusFilter === opt}
                    onclick={() => (focusFilter = opt)}
                  >
                    {opt === 'All' ? 'All Curated Works' : opt}
                  </button>
                {/each}
              </div>
            {/if}
          {/snippet}
          {#snippet card(m: MovieCard)}
            <MediaCard
              item={m}
              shape={moviePosters ? 'poster' : 'wide'}
              href={href.item(m.id)}
              title={m.title}
              rating={m.rating}
              sub={cardSub(m)}
            />
          {/snippet}
        </Rail>
      {/if}

      {#if shortFeatures.length >= 2}
        <Rail
          index="02 / WEEKNIGHT DURATION"
          title="Acclaimed & Under 100 Minutes"
          subtitle="Finishes before 22:00"
          items={shortFeatures}
          key={(m) => m.id}
          size="wide"
        >
          {#snippet card(m: MovieCard)}
            <MediaCard
              item={m}
              shape="wide"
              href={href.item(m.id)}
              title={m.title}
              rating={m.rating}
              note={endsAtLabel(m.durationSec)}
              sub={cardSub(m)}
            />
          {/snippet}
        </Rail>
      {/if}

      <Rail
        index="03 / NEW ARRIVALS"
        title="Recently added"
        subtitle="Latest additions to your personal archive"
        items={recent}
        key={(i) => i.id}
      >
        {#snippet card(i: LibraryRow)}
          <MediaCard item={i} href={href.item(i.id)} title={cardTitle(i)} rating={'rating' in i ? i.rating : null} sub={cardSub(i)} />
        {/snippet}
      </Rail>

      <Rail index="04 / NEXT IN SERIES" title="Coming up" items={upcoming} key={(u) => u.titleId}>
        {#snippet card(u: UpcomingShow)}
          <MediaCard
            img={showWideImg(u, episodesOf(u.show)[0])}
            href={href.show(u.show)}
            title={u.show}
            label={u.show}
            note={airLabel(u.nextAirDate)}
            sub={u.nextEpisode
              ? [episodeLabel(u.nextEpisode.season, u.nextEpisode.episode), u.nextEpisode.name]
              : ['New episode']}
          />
        {/snippet}
      </Rail>

      {#each genreRails as r, idx (r.genre.id)}
        <Rail
          index={`0${idx + 5} / CURATED SHOWCASE`}
          title={r.genre.name}
          items={r.titles.slice(0, 30)}
          key={(t) => `${t.kind}:${t.id}`}
          size="poster"
          more={{ href: href.genre(r.genre.id), label: `All ${r.genre.count}` }}
        >
          {#snippet card(t: TitleCard)}
            <MediaCard shape="poster" img={posterImg(t)} href={titleHref(t)} title={t.title} rating={t.rating} sub={[t.year || '']} watch={titleBadge(t)} />
          {/snippet}
        </Rail>
      {/each}
    </div>
  </div>
{/if}

<style>
  .home-stage {
    padding-top: calc(var(--bar-h) + env(safe-area-inset-top));
  }
  .lens-bar {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 0.75rem 1.25rem;
    padding: 0.85rem var(--gutter);
    border-bottom: 1px solid var(--line);
    font-size: var(--fs-xs);
  }
  .lens-pills {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.5rem;
  }
  .lens-label {
    font-family: var(--font-mono);
    font-size: var(--fs-2xs);
    text-transform: uppercase;
    letter-spacing: var(--tracking-widest);
    color: var(--text-faint);
    margin-right: 0.25rem;
  }
  .lens-pill {
    padding: 0.35rem 0.9rem;
    font: inherit;
    font-size: var(--fs-xs);
    color: var(--text-muted);
    background: var(--fill-subtle);
    border: 1px solid transparent;
    border-radius: var(--radius-pill);
    cursor: pointer;
    transition:
      color var(--motion-hover),
      border-color var(--motion-hover),
      background var(--motion-hover);
  }
  .lens-pill:hover {
    color: var(--text);
  }
  .lens-pill.active {
    color: var(--text);
    font-weight: var(--fw-medium);
    background: var(--accent-wash);
    border-color: var(--accent);
  }
  .shuffle-btn {
    padding: 0.35rem 0.85rem;
    font-family: var(--font-mono);
    font-size: var(--fs-2xs);
    color: var(--text-muted);
    background: var(--fill-subtle);
    border: 1px solid transparent;
    border-radius: var(--radius-pill);
    cursor: pointer;
    transition: color var(--motion-hover);
  }
  .shuffle-btn:hover {
    color: var(--text);
  }

  /* Top Asymmetric Spread */
  .spread {
    display: grid;
    grid-template-columns: minmax(0, 7fr) minmax(0, 5fr);
    gap: 2.5rem;
    align-items: start;
    padding: 2rem var(--gutter);
  }
  .spread-feature {
    min-width: 0;
  }
  .kicker {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.65rem;
    margin-bottom: 1rem;
    font-family: var(--font-mono);
    font-size: var(--fs-2xs-2);
    text-transform: uppercase;
    letter-spacing: var(--tracking-widest);
    color: var(--text-muted);
  }
  .kicker-accent {
    color: var(--accent);
  }
  .feature-grid {
    display: grid;
    grid-template-columns: minmax(0, 7fr) minmax(0, 5fr);
    gap: 1.5rem;
    align-items: start;
  }
  .feature-art {
    display: block;
    overflow: hidden;
    border-radius: var(--radius);
    background: var(--surface-control);
    box-shadow: var(--ring-hairline);
  }
  .feature-art :global(img) {
    transition: transform var(--motion-progress) var(--ease);
  }
  .feature-art:hover :global(img) {
    transform: scale(1.03);
  }
  .feature-copy {
    min-width: 0;
    display: grid;
    gap: 0.65rem;
  }
  .feature-copy :global(h1) {
    font-size: var(--fs-display);
    line-height: var(--lh-display);
  }
  .feature-copy :global(.logo) {
    display: block;
    max-width: min(18rem, 90%);
    max-height: 4.5rem;
    width: auto;
    height: auto;
    object-fit: contain;
    object-position: left bottom;
  }
  .feature-tagline {
    margin: 0;
    font-family: var(--font-display);
    font-style: italic;
    font-size: var(--fs-xl);
    color: var(--text-muted);
    line-height: var(--lh-snug);
  }
  .feature-scores {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 1.15rem;
    padding-top: 0.15rem;
    font-family: var(--font-mono);
    font-size: var(--fs-xs);
    color: var(--text-muted);
  }
  .score-num {
    font-family: var(--font-display);
    font-size: var(--fs-3xl);
    font-weight: var(--fw-regular);
    color: var(--text);
    margin-right: 0.2rem;
  }
  .meta {
    color: var(--text-muted);
    font-family: var(--font-mono);
    font-size: var(--fs-xs);
  }

  /* Borderless Double Bill Pairing footnote */
  .double-bill {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    justify-content: space-between;
    gap: 0.5rem 1rem;
    margin-top: 1.35rem;
    padding-top: 1rem;
    border-top: 1px solid var(--line);
    font-size: var(--fs-xs);
    transition: opacity var(--motion-hover);
  }
  .double-bill:hover {
    opacity: 0.82;
  }
  .db-main {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 0.35rem;
  }
  .db-kicker {
    font-family: var(--font-mono);
    font-size: var(--fs-2xs);
    text-transform: uppercase;
    letter-spacing: var(--tracking-widest);
    color: var(--accent);
    margin-right: 0.35rem;
  }
  .db-title {
    font-family: var(--font-display);
    font-size: var(--fs-xl-2);
    font-weight: var(--fw-regular);
    color: var(--text);
  }
  .db-year {
    font-family: var(--font-mono);
    color: var(--text-muted);
  }
  .db-note {
    font-family: var(--font-display);
    font-style: italic;
    font-size: var(--fs-base);
    color: var(--text-muted);
  }
  .db-ends {
    font-family: var(--font-mono);
    font-size: var(--fs-2xs);
    color: var(--accent);
    white-space: nowrap;
  }

  /* Right Column: Editorial Resume Desk */
  .resume-desk {
    margin-top: 0;
    padding-top: 0;
    border-top: 0;
    padding-left: 2.5rem;
    border-left: 1px solid var(--line);
    min-width: 0;
  }
  .desk-head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 1rem;
    margin-bottom: 0.75rem;
    font-family: var(--font-mono);
    font-size: var(--fs-2xs-2);
    text-transform: uppercase;
    letter-spacing: var(--tracking-widest);
    color: var(--text-muted);
  }
  .desk-head h2 {
    font-family: var(--font-mono);
    font-size: var(--fs-2xs-2);
    font-weight: var(--fw-regular);
    letter-spacing: var(--tracking-widest);
    text-transform: uppercase;
    color: var(--text-muted);
  }
  .resume-list {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .resume-list li + li {
    border-top: 1px solid var(--line);
  }
  .resume-row {
    display: flex;
    align-items: center;
    gap: 1rem;
    padding: 0.85rem 0;
    content-visibility: visible;
    contain-intrinsic-size: none;
  }
  .resume-list li:first-child .resume-row {
    padding-top: 0.25rem;
  }
  .resume-list li:last-child .resume-row {
    padding-bottom: 0;
  }
  .resume-thumb {
    position: relative;
    width: 6.25rem;
    flex: none;
    overflow: hidden;
    border-radius: var(--radius);
    background: var(--surface-control);
  }
  .resume-progress {
    position: absolute;
    inset: auto 0 0;
    height: 3px;
    background: var(--progress-track);
  }
  .resume-progress span {
    display: block;
    height: 100%;
    background: var(--accent);
  }
  .resume-body {
    flex: 1;
    min-width: 0;
  }
  .resume-top {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 0.5rem;
  }
  .resume-top .title {
    margin: 0;
    font-family: var(--font-display);
    font-size: var(--fs-3xl);
    font-weight: var(--fw-regular);
    line-height: var(--lh-none);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .resume-left {
    font-family: var(--font-mono);
    font-size: var(--fs-2xs);
    color: var(--accent);
    flex: none;
  }
  .resume-sub {
    margin-top: 0.25rem;
    font-family: var(--font-mono);
    font-size: var(--fs-xs);
    color: var(--text-muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* Focus pills inside Chapter 01 */
  .focus-pills {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.5rem;
  }
  .focus-pill {
    padding: 0.25rem 0.9rem;
    font-family: var(--font-display);
    font-style: italic;
    font-size: var(--fs-base);
    color: var(--text-muted);
    background: var(--fill-subtle);
    border: 1px solid transparent;
    border-radius: var(--radius-pill);
    cursor: pointer;
    transition:
      color var(--motion-hover),
      border-color var(--motion-hover);
  }
  .focus-pill:hover {
    color: var(--text);
  }
  .focus-pill.active {
    color: var(--text);
    border-color: var(--text);
  }

  .rails {
    padding-bottom: 4rem;
    position: relative;
  }
  .setup {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 1rem 2rem;
    margin: 1.5rem var(--gutter) 0;
    padding: 1.1rem 1.25rem;
    background:
      radial-gradient(120% 140% at 0% 0%, color-mix(in oklab, var(--accent) 16%, transparent), transparent 60%),
      var(--surface-raised);
    border: 1px solid var(--line);
    border-radius: var(--radius-lg);
  }
  .setup h2 {
    margin-bottom: 0.2rem;
  }
  .setup p {
    margin: 0;
    max-width: 52ch;
  }
  .setup-actions {
    display: flex;
    gap: 0.6rem;
  }
  .empty {
    max-width: 34rem;
    margin: 0 auto;
  }
  .loading {
    padding: 0 0 4rem;
  }
  .hero-sk {
    height: min(62vh, 48vw);
  }
  .row-sk {
    display: flex;
    gap: 1.1rem;
    padding: 2.25rem var(--gutter) 0;
    overflow: hidden;
  }
  .card-sk {
    flex: 0 0 clamp(220px, 22vw, 320px);
    aspect-ratio: 16 / 9;
  }
  @media (max-width: 960px) {
    .spread {
      grid-template-columns: 1fr;
    }
    .resume-desk {
      padding-left: 0;
      border-left: 0;
      padding-top: 1.5rem;
      border-top: 1px solid var(--line);
    }
  }
  @media (max-width: 600px) {
    .feature-grid {
      grid-template-columns: 1fr;
    }
    .rails {
      padding-bottom: 6rem;
    }
    .hero-sk {
      height: 56vh;
    }
  }
</style>
