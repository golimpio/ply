<script lang="ts">
  import { api, type GenreCount, type ItemSummary, type Settings, type TitleCard, type UpcomingShow } from '../lib/api';
  import { useAmbient } from '../lib/ambient.svelte';
  import { episodeLabel, formatDuration } from '../lib/format';
  import { byName } from '../lib/az';
  import { byAdded, ensureLibrary, library, showsOf, type ShowEntry } from '../lib/library.svelte';
  import {
    airLabel,
    groupByTitle,
    logoSrc,
    onePerTitle,
    posterImg,
    prefersPosters,
    showWideImg,
    titleHref,
    type TitleGroup,
  } from '../lib/meta';
  import { href } from '../lib/router.svelte';
  import { rescan, scan } from '../lib/state.svelte';
  import { app } from '../lib/state.svelte';
  import { artOf, progressOf } from '../lib/ui-types';
  import Badges from '../components/Badges.svelte';
  import Hero from '../components/Hero.svelte';
  import HeroTitle from '../components/HeroTitle.svelte';
  import Icon from '../components/Icon.svelte';
  import MediaCard from '../components/MediaCard.svelte';
  import Rail from '../components/Rail.svelte';

  let continueItems = $state<ItemSummary[]>([]);
  let upcoming = $state<UpcomingShow[]>([]);
  let genreRails = $state<{ genre: GenreCount; titles: TitleCard[] }[]>([]);
  let settings = $state<Settings>();

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

  // A few genre shelves: the biggest genres that have enough titles to fill one.
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

  const items = $derived(library.items);
  // One card per film: its newest version in Recently added, and in Continue
  // the version played last (the server lists the most recent first).
  const recent = $derived(onePerTitle([...items].sort(byAdded)).slice(0, 24));
  const continuing = $derived(onePerTitle(continueItems));
  const movies = $derived(groupByTitle(items.filter((i) => i.kind === 'movie')).sort((a, b) => byName(a.item, b.item))); // as Movies lists them
  const shows = $derived(showsOf(items, library.shows).sort((a, b) => b.latestAddedAt - a.latestAddedAt));
  const moviePosters = $derived(prefersPosters(movies.map((g) => g.item)));
  const showPosters = $derived(prefersPosters(shows.map((s) => s.meta ?? {})));
  // Lead with the newest film that has a real backdrop, then any artwork, then anything.
  const feature = $derived(
    recent.find((i) => i.kind === 'movie' && i.backdrop) ??
      recent.find((i) => i.kind === 'movie' && artOf(i)) ??
      recent.find((i) => artOf(i)) ??
      recent[0],
  );
  const featureLogo = $derived(feature?.kind === 'movie' ? logoSrc(feature) : '');

  useAmbient(() => artOf(feature)?.color);

  const isEpisode = (i: ItemSummary) => i.kind === 'episode';
  const cardTitle = (i: ItemSummary) => (isEpisode(i) ? i.show || i.title : i.title);
  const cardSub = (i: ItemSummary) =>
    isEpisode(i) ? [episodeLabel(i.season, i.episode), i.episodeTitle] : [i.year, formatDuration(i.durationSec)];
  const resumeAt = (i: ItemSummary) => (progressOf(i) > 0 ? (i.progress?.positionSec ?? 0) : 0);
  const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`;
</script>

{#if library.error && !library.loaded}
  <div class="page center">
    <p class="error">{library.error}</p>
    <button class="btn" onclick={() => ensureLibrary()}>Try again</button>
  </div>
{:else if !library.loaded}
  <div class="loading" aria-busy="true" aria-label="Loading the library">
    <div class="skeleton hero-sk"></div>
    <div class="row-sk">
      {#each Array(5) as _, i (i)}<div class="skeleton card-sk"></div>{/each}
    </div>
  </div>
{:else if items.length === 0}
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
  {#if feature}
    <Hero item={feature} label={cardTitle(feature)} tall>
      <HeroTitle title={cardTitle(feature)} logo={featureLogo} />
      <div class="meta">
        {#if isEpisode(feature)}<span>{episodeLabel(feature.season, feature.episode)}</span>{/if}
        {#if feature.year}<span>{feature.year}</span>{/if}
        {#if feature.certification}<span class="cert">{feature.certification}</span>{/if}
        {#if feature.durationSec}<span>{formatDuration(feature.durationSec)}</span>{/if}
        {#if feature.genres?.length}<span>{feature.genres.slice(0, 3).join(', ')}</span>{/if}
        <span class="badges"><Badges item={feature} /></span>
      </div>
      <div class="hero-actions">
        <a class="btn primary" href={href.play(feature.id, resumeAt(feature))}>
          <Icon name="play" />{resumeAt(feature) ? 'Resume' : 'Play'}
        </a>
        <a class="btn" href={href.item(feature.id)}><Icon name="info" />Details</a>
      </div>
    </Hero>
  {/if}

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

    <Rail title="Continue watching" items={continuing} key={(i) => i.id}>
      {#snippet card(i: ItemSummary)}
        <MediaCard item={i} href={href.play(i.id, i.progress?.positionSec ?? 0)} title={cardTitle(i)} sub={cardSub(i)} />
      {/snippet}
    </Rail>
    <Rail title="Recently added" items={recent} key={(i) => i.id}>
      {#snippet card(i: ItemSummary)}
        <MediaCard item={i} href={href.item(i.id)} title={cardTitle(i)} sub={cardSub(i)} />
      {/snippet}
    </Rail>
    <Rail
      title="Movies"
      items={movies.slice(0, 30)}
      key={(g) => g.key}
      size={moviePosters ? 'poster' : 'wide'}
      more={{ href: href.movies, label: `All ${movies.length}` }}
    >
      {#snippet card(g: TitleGroup)}
        <MediaCard item={g.item} shape={moviePosters ? 'poster' : 'wide'} href={href.item(g.item.id)} title={g.item.title} sub={cardSub(g.item)} />
      {/snippet}
    </Rail>
    <Rail title="Shows" items={shows} key={(s) => s.show} size={showPosters ? 'poster' : 'wide'} more={{ href: href.shows, label: 'All shows' }}>
      {#snippet card(s: ShowEntry)}
        <MediaCard
          shape={showPosters ? 'poster' : 'wide'}
          img={showPosters ? posterImg(s.meta) : showWideImg(s.meta, s.cover)}
          href={href.show(s.show)}
          title={s.show}
          sub={[plural(s.episodeCount, 'episode')]}
        />
      {/snippet}
    </Rail>
    <Rail title="Coming up" items={upcoming} key={(u) => u.titleId}>
      {#snippet card(u: UpcomingShow)}
        <MediaCard
          img={showWideImg(u, library.items.find((i) => i.show === u.show))}
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
    {#each genreRails as r (r.genre.id)}
      <Rail title={r.genre.name} items={r.titles.slice(0, 30)} key={(t) => `${t.kind}:${t.id}`} size="poster" more={{ href: href.genre(r.genre.id), label: `All ${r.genre.count}` }}>
        {#snippet card(t: TitleCard)}
          <MediaCard shape="poster" img={posterImg(t)} href={titleHref(t)} title={t.title} sub={[t.year || '']} />
        {/snippet}
      </Rail>
    {/each}
  </div>
{/if}

<style>
  .meta {
    color: var(--text-hero-meta);
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
    height: min(82vh, 64vw); /* the home page's hero is the tall one */
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
  @media (max-width: 600px) {
    .rails {
      padding-bottom: 6rem;
    }
  }
</style>
