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
  import Icon from '../components/Icon.svelte';
  import MediaCard from '../components/MediaCard.svelte';
  import Rail from '../components/Rail.svelte';
  import RatingGlyph from '../components/RatingGlyph.svelte';

  let continueItems = $state<ItemSummary[]>([]);
  let upcoming = $state<UpcomingShow[]>([]);
  let genreRails = $state<{ genre: GenreCount; titles: TitleCard[] }[]>([]);
  let settings = $state<Settings>();

  // Interactive Evening Lens & Curated Focus state (Variation II: Living Repertory & Moods)
  type MoodLens = 'weeknight' | 'double' | 'noir' | 'epic' | 'unwatched' | 'series';
  let moodLens = $state<MoodLens>('weeknight');
  let heroOffset = $state(0);

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
  const featureDirector = $derived.by(() => {
    if (feature?.kind === 'episode') return '';
    const dirs = crewWith(featureDetail?.meta, ['Director']);
    return dirs.length ? dirs[0].name : '';
  });
  const featureTagline = $derived(featureDetail?.meta?.tagline || '');
  const featureBrief = $derived.by(() => {
    return (
      featureDetail?.meta?.overview ||
      featureDetail?.episodeInfo?.overview ||
      ('overview' in (feature ?? {}) ? (feature as { overview?: string }).overview : '') ||
      featureDetail?.meta?.tagline ||
      featureTagline ||
      (feature?.genres?.length ? feature.genres.slice(0, 3).join(' · ') : '')
    );
  });
  const featureScores = $derived.by(() => {
    const fallback = lead && 'rating' in lead ? lead : null;
    const list = titleRatings(featureDetail?.meta, fallback);
    if (!list.length) return [];
    // Prioritize Rotten Tomatoes critics/audience when available, then IMDb, then others
    const priority = ['rt_critics', 'rt_audience', 'imdb', 'letterboxd', 'metacritic', 'tmdb', 'trakt'];
    const sorted = [...list].sort((a, b) => {
      const ia = priority.indexOf(a.source);
      const ib = priority.indexOf(b.source);
      return (ia === -1 ? 99 : ia) - (ib === -1 ? 99 : ib);
    });
    return sorted.slice(0, 3).map((r) => ({
      val: ratingValue(r),
      source: r.source,
      entry: r,
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

  const companionId = $derived(companion?.id);
  let companionFull = $state<{ id: number; item: ItemDetail | null }>();
  $effect(() => {
    const id = companionId;
    if (id === undefined) return;
    const ctl = new AbortController();
    const d = detail(id, ctl.signal);
    if (d.cached) companionFull = { id, item: d.cached };
    d.fresh.then(
      (item) => {
        if (!ctl.signal.aborted) companionFull = { id, item };
      },
      () => {
        if (!ctl.signal.aborted && companionFull?.id !== id) companionFull = { id, item: null };
      },
    );
    return () => ctl.abort();
  });
  const companionDetail = $derived(companionFull?.id === companion?.id ? companionFull?.item : null);
  const companionScores = $derived.by(() => {
    const fallback = companion && 'rating' in companion ? companion : null;
    const list = titleRatings(companionDetail?.meta, fallback);
    if (!list.length) return [];
    const priority = ['rt_critics', 'rt_audience', 'imdb', 'letterboxd', 'metacritic', 'tmdb', 'trakt'];
    const sorted = [...list].sort((a, b) => {
      const ia = priority.indexOf(a.source);
      const ib = priority.indexOf(b.source);
      return (ia === -1 ? 99 : ia) - (ib === -1 ? 99 : ib);
    });
    return sorted.slice(0, 3).map((r) => ({
      val: ratingValue(r),
      source: r.source,
      entry: r,
    }));
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
  const companionBrief = $derived.by(() => {
    return (
      companionDetail?.meta?.overview ||
      companionDetail?.meta?.tagline ||
      companionNote ||
      (companion?.genres?.length ? companion.genres.slice(0, 3).join(' · ') : '')
    );
  });

  // Curated Chapter 01: Auteur & Critics Focus
  const acclaimedFilms = $derived.by(() => {
    const base = films.map((g) => g.item);
    return [...base].sort((a, b) => ratingScore(b) - ratingScore(a) || byName(a, b)).slice(0, 30);
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

    <!-- Top 50/50 Equal Editorial Spread: Feature I + Feature II -->
    <section class="spread hero" class:has-companion={Boolean(companion)}>
      <div class="spread-feature">
        {#if feature}
          <div class="kicker tabular">
            <span class="kicker-accent">● {lensKicker.toUpperCase()}</span>
            {#if featureEndsAt}
              <span>·</span>
              <span>STARTS NOW · {featureEndsAt.toUpperCase()}</span>
            {/if}
          </div>

          <a class="feature-art" href={href.item(feature.id)} onclick={morph}>
            <Art item={feature} kind="backdrop" ratio="16 / 9" label={cardTitle(feature)} eager />
          </a>

          <div class="feature-copy">
            <div class="hero-meta tabular">
              {#if isEpisode(feature)}
                <span>{feature.year || ''}</span>
                {#if feature.year}<span>·</span>{/if}
                <span>{episodeLabel(feature.season, feature.episode)}{#if 'episodeTitle' in feature && feature.episodeTitle} — {feature.episodeTitle}{/if}</span>
                {#if feature.durationSec}<span>·</span><span>{formatDuration(feature.durationSec)}</span>{/if}
              {:else}
                {#if feature.year}<span>{feature.year}</span>{/if}
                {#if feature.year && (featureDirector || feature.durationSec)}<span>·</span>{/if}
                {#if featureDirector}<span>{featureDirector}</span>{/if}
                {#if featureDirector && feature.durationSec}<span>·</span>{/if}
                {#if feature.durationSec}<span>{formatDuration(feature.durationSec)}</span>{/if}
              {/if}
            </div>

            <a class="editorial-title-wrap" href={href.item(feature.id)} onclick={morph}>
              <h1 class="editorial-title">{cardTitle(feature)}</h1>
            </a>

            {#if featureBrief}
              <p class="editorial-brief">{featureBrief}</p>
            {/if}

            {#if featureScores.length}
              <div class="hero-scores tabular">
                {#each featureScores as s (s.source)}
                  <span class="hero-score-item">
                    <RatingGlyph entry={s.entry} />
                    <strong class="score-num">{s.val}</strong>
                  </span>
                {/each}
              </div>
            {/if}

            <div class="hero-actions">
              <a class="btn primary pill" href={href.play(feature.id, resumeAt(feature))}>
                <Icon name="play" /><span>{resumeAt(feature) ? 'Resume' : 'Play'}{#if featureEndsAt} · {featureEndsAt}{/if}</span>
              </a>
              <a class="btn pill subtle" href={href.item(feature.id)}>
                <Icon name="info" /><span>Details</span>
              </a>
            </div>
          </div>
        {:else if lead}
          <div class="skeleton hero-sk"></div>
        {/if}
      </div>

      <!-- Right Column: Feature II Double Bill Pairing -->
      {#if companion}
        <aside class="double-bill-desk">
          <div class="desk-head tabular">
            <div class="desk-kicker">
              <span class="dot"></span>
              <h2>Double Bill Pairing</h2>
            </div>
            {#if companionEndsAt}
              <span class="desk-ends">{companionEndsAt} →</span>
            {/if}
          </div>

          <a class="companion-art" href={href.item(companion.id)} onclick={morph}>
            <Art item={companion} kind="backdrop" ratio="16 / 9" label={companion.title} />
          </a>

          <div class="companion-body">
            <div class="companion-subhead tabular">
              <span class="companion-tag">FEATURE II</span>
              {#if companion.year}<span>·</span><span>{companion.year}</span>{/if}
              {#if companion.durationSec}<span>·</span><span>{formatDuration(companion.durationSec)}</span>{/if}
              {#if companion.genres?.length}
                <span>·</span><span>{companion.genres.slice(0, 2).join(' · ')}</span>
              {/if}
            </div>

            <a class="editorial-title-wrap" href={href.item(companion.id)} onclick={morph}>
              <h3 class="editorial-title">{companion.title}</h3>
            </a>

            {#if companionBrief}
              <p class="editorial-brief">{companionBrief}</p>
            {/if}

            {#if companionScores.length}
              <div class="hero-scores tabular">
                {#each companionScores as s (s.source)}
                  <span class="hero-score-item">
                    <RatingGlyph entry={s.entry} />
                    <strong class="score-num">{s.val}</strong>
                  </span>
                {/each}
              </div>
            {:else if companion.rating}
              <div class="hero-scores tabular">
                <span class="hero-score-item">
                  <RatingGlyph entry={companion.rating} />
                  <strong class="score-num">{ratingValue(companion.rating)}</strong>
                </span>
              </div>
            {/if}

            <div class="hero-actions">
              <a class="btn primary pill" href={href.play(companion.id, resumeAt(companion))}>
                <Icon name="play" /><span>Play Feature II</span>
              </a>
              <a class="btn pill subtle" href={href.item(companion.id)} onclick={morph}>
                <Icon name="info" /><span>Details</span>
              </a>
            </div>
          </div>
        </aside>
      {/if}
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

      {#if continuing.length}
        <Rail
          index="00 / IN PROGRESS"
          title="Continue watching"
          subtitle="Pick up where you left off"
          items={continuing}
          key={(i) => i.id}
          size="wide"
        >
          {#snippet card(i: ItemSummary)}
            <MediaCard
              item={i}
              shape="wide"
              href={href.play(i.id, i.progress?.positionSec ?? 0)}
              title={cardTitle(i)}
              note={leftLabel(i)}
              sub={cardSub(i)}
            />
          {/snippet}
        </Rail>
      {/if}

      {#if acclaimedFilms.length >= 2}
        <Rail
          index="01 / CURATED MONOGRAPHS"
          title="World & Auteur Cinema"
          subtitle="Godard, Wong Kar-wai, Wenders, Song, Kaurismäki"
          items={acclaimedFilms}
          key={(m) => m.id}
          size={moviePosters ? 'poster' : 'wide'}
          more={{ href: href.movies, label: `All ${films.length}` }}
        >
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

  /* Top 50/50 Equal Editorial Spread */
  .spread {
    display: grid;
    grid-template-columns: 1fr;
    gap: clamp(1.75rem, 3.5vw, 3rem);
    align-items: start;
    padding: clamp(1.75rem, 3.5vw, 2.75rem) var(--gutter) var(--shelf-pad-y);
  }
  .spread.hero.has-companion {
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
    gap: clamp(2rem, 3.5vw, 3.5rem);
  }
  .spread-feature {
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .kicker {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.65rem;
    min-height: 1.5rem;
    margin-bottom: 0.85rem;
    font-family: var(--font-mono);
    font-size: var(--fs-2xs-2);
    text-transform: uppercase;
    letter-spacing: var(--tracking-widest);
    color: var(--text-muted);
  }
  .kicker-accent {
    color: var(--accent);
  }
  .feature-art,
  .companion-art {
    position: relative;
    display: block;
    height: clamp(220px, 20vw, 260px);
    width: 100%;
    overflow: hidden;
    border-radius: var(--radius);
    background: var(--surface-control);
    box-shadow: var(--ring-hairline);
    margin-bottom: 1.15rem;
  }
  .feature-art :global(.picture),
  .companion-art :global(.picture) {
    height: 100% !important;
    width: 100% !important;
    aspect-ratio: unset !important;
  }
  .feature-art :global(img),
  .companion-art :global(img) {
    height: 100% !important;
    width: 100% !important;
    object-fit: cover !important;
    transition: transform var(--motion-progress) var(--ease);
  }
  .feature-art:hover :global(img),
  .companion-art:hover :global(img) {
    transform: scale(1.03);
  }
  .feature-copy,
  .companion-body {
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .hero-meta,
  .companion-subhead {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 0.45rem;
    font-family: var(--font-mono);
    font-size: var(--fs-2xs-2);
    letter-spacing: 0.04em;
    color: var(--text-muted);
    text-transform: uppercase;
    margin-bottom: 0.45rem;
  }
  .companion-tag {
    font-family: var(--font-mono);
    font-size: var(--fs-2xs-2);
    letter-spacing: var(--tracking-widest);
    color: var(--accent);
    text-transform: uppercase;
  }
  .hero-scores {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 1.35rem;
    margin-bottom: 1.25rem;
    font-family: var(--font-mono);
    font-size: var(--fs-xs);
    color: var(--text-muted);
  }
  .hero-score-item {
    display: inline-flex;
    align-items: center;
    gap: 0.45rem;
  }
  .hero-score-item :global(.glyph) {
    width: 1.25em;
    height: 1.25em;
  }
  .score-num {
    font-family: var(--font-display);
    font-size: var(--fs-2xl);
    font-weight: var(--fw-regular);
    color: var(--text);
    margin-right: 0.15rem;
  }
  .hero-actions {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.75rem;
  }
  .hero-actions :global(.icon) {
    width: 0.95rem;
    height: 0.95rem;
  }

  /* Right Column: Double Bill Companion Desk */
  .double-bill-desk {
    padding-left: clamp(1.75rem, 3vw, 3rem);
    border-left: 1px solid var(--line);
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .desk-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    min-height: 1.5rem;
    margin-bottom: 0.85rem;
    font-family: var(--font-mono);
    font-size: var(--fs-2xs-2);
    text-transform: uppercase;
    letter-spacing: var(--tracking-widest);
    color: var(--text-muted);
  }
  .desk-kicker {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
  }
  .desk-kicker .dot {
    width: 0.4rem;
    height: 0.4rem;
    border-radius: var(--radius-round);
    background: var(--accent);
  }
  .desk-head h2 {
    font-family: var(--font-mono);
    font-size: var(--fs-2xs-2);
    font-weight: var(--fw-regular);
    letter-spacing: var(--tracking-widest);
    text-transform: uppercase;
    color: var(--accent);
    margin: 0;
  }
  .desk-ends {
    color: var(--accent);
    font-size: var(--fs-2xs-2);
    white-space: nowrap;
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
    margin: 1.5rem var(--gutter) 2.25rem;
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
    .spread.hero.has-companion {
      grid-template-columns: 1fr;
    }
    .double-bill-desk {
      padding-left: 0;
      border-left: 0;
      padding-top: 1.5rem;
      border-top: 1px solid var(--line);
    }
  }
  @media (max-width: 600px) {
    .rails {
      padding-bottom: 6rem;
    }
    .hero-sk {
      height: 56vh;
    }
  }
</style>
