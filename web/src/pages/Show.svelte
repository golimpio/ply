<script lang="ts">
  import { api, ApiError, type EpisodeCard, type EpisodeOrder, type Subtitle } from '../lib/api';
  import { useAmbient } from '../lib/ambient.svelte';
  import { episodeLabel, formatDuration, seasonLabel } from '../lib/format';
  import { ensureEpisodes, episodes as episodeList, episodesOf, markWatched, showMeta, showsOf, type WatchTarget } from '../lib/library.svelte';
  import { backdropOf, CREDIT_LINES, crewWith, logoSrc, posterImg, yearSpan } from '../lib/meta';
  import { morph } from '../lib/nav';
  import { ensureRatingPrefs, ratingPrefs } from '../lib/ratingprefs.svelte';
  import { orderShown, titleRatings } from '../lib/ratings';
  import { href, redirect } from '../lib/router.svelte';
  import { resource } from '../lib/resource.svelte';
  import { app } from '../lib/state.svelte';
  import { artOf, progressOf } from '../lib/ui-types';
  import { countEpisodes, countsBadge, showCounts, WATCHED } from '../lib/watched';
  import Art from '../components/Art.svelte';
  import BackButton from '../components/BackButton.svelte';
  import Badges from '../components/Badges.svelte';
  import EditTitle from '../components/EditTitle.svelte';
  import FixMatch from '../components/FixMatch.svelte';
  import Hero from '../components/Hero.svelte';
  import HeroTitle from '../components/HeroTitle.svelte';
  import Icon from '../components/Icon.svelte';
  import Menu from '../components/Menu.svelte';
  import PersonCard from '../components/PersonCard.svelte';
  import SubtitleManager from '../components/SubtitleManager.svelte';
  import Rail from '../components/Rail.svelte';
  import Rating from '../components/Rating.svelte';
  import Ratings from '../components/Ratings.svelte';
  import SeasonRow, { type SeasonCard } from '../components/SeasonRow.svelte';
  import SourceBadge from '../components/SourceBadge.svelte';
  import TrailerButton from '../components/TrailerButton.svelte';
  import WatchBadge from '../components/WatchBadge.svelte';

  let { show, season }: { show: string; season?: number } = $props();

  $effect(() => {
    void app.libraryVersion;
    void ensureEpisodes();
  });

  // The show's metadata and season details; the episodes come from the cached
  // library, so the page paints before this arrives (and without it).
  const detail = resource(() => {
    void app.libraryVersion;
    return api.show(show);
  });

  const entry = $derived(showsOf(episodesOf(show))[0]);
  // Regular seasons first, specials last: the row's order, and the one "next up" looks in.
  const seasonOrder = (s: number | undefined) => s || Infinity;
  const episodes = $derived(
    [...episodesOf(show)].sort((a, b) => seasonOrder(a.season) - seasonOrder(b.season) || (a.episode ?? 0) - (b.episode ?? 0)),
  );
  const seasons = $derived([...new Set(episodes.map((e) => e.season ?? 0))]);
  // Next up: the first started-but-unfinished episode, else the first unwatched one.
  const next = $derived(
    episodes.find((e) => progressOf(e) > 0) ?? episodes.find((e) => !e.watched) ?? episodes[0],
  );
  const current = $derived(season !== undefined && seasons.includes(season) ? season : (next?.season ?? seasons[0]));
  const list = $derived(episodes.filter((e) => (e.season ?? 0) === current));

  const meta = $derived(detail.data?.meta);
  const summary = $derived(detail.data ?? showMeta(show));
  const seasonInfo = $derived(detail.data?.seasons.find((s) => s.season === current));
  const seasonName = (n: number) => detail.data?.seasons.find((s) => s.season === n)?.name || seasonLabel(n);

  // The seasons' posters; one with none of its own has the show's, with its name over it. The
  // counts are the cached episodes' (lib/watched), so a mark shows on the cards at once.
  const showPoster = $derived(posterImg(summary) ?? posterImg(meta));
  const seasonCards = $derived<SeasonCard[]>(
    seasons.map((n) => {
      const own = posterImg(detail.data?.seasons.find((s) => s.season === n));
      const counts = countEpisodes(episodes.filter((e) => (e.season ?? 0) === n));
      return { season: n, name: seasonName(n), poster: own ?? showPoster, fallback: !own, episodes: counts.episodeCount, badge: countsBadge(counts) };
    }),
  );
  const currentCounts = $derived(countEpisodes(list));
  const seasonDone = $derived(currentCounts.episodeCount > 0 && currentCounts.watchedEpisodes === currentCounts.episodeCount);
  const totals = $derived(showCounts(episodes));
  const showDone = $derived(totals.episodeCount > 0 && totals.watchedEpisodes === totals.episodeCount);
  const showBadge = $derived(countsBadge(totals));
  // Another season: in place of this one in the history, the page left where it is scrolled to.
  const selectSeason = (n: number) => redirect(href.show(show, n));
  const logo = $derived(logoSrc(meta));
  const years = $derived(yearSpan(meta, summary?.year));
  const ratings = $derived(titleRatings(meta, summary));
  // An episode's own rating (IMDb), when the settings show that source.
  const epRating = (e: EpisodeCard) => orderShown(e.ratings ?? [], ratingPrefs.shown)[0];
  $effect(() => {
    void app.libraryVersion;
    if (list.some((e) => e.ratings?.length)) void ensureRatingPrefs();
  });
  const credits = $derived(CREDIT_LINES.map((l) => ({ ...l, people: crewWith(meta, l.jobs) })).filter((l) => l.people.length));
  const needsReview = $derived(!!(meta?.needsReview ?? summary?.needsReview));

  let fixing = $state(false);
  let editing = $state(false);
  let managingSubs = $state(false);
  let nextSubs = $state<Subtitle[]>([]);
  let notice = $state('');

  $effect(() => {
    if (managingSubs && next) {
      void api.item(next.id).then((d) => {
        nextSubs = d.subtitles ?? [];
      });
    }
  });

  // Marking: one at a time; the cards and counts change at once and go back on an error, which is
  // said next to what was pressed (the hero's button, or the season's).
  let marking = $state(false);
  let markError = $state<{ at: 'show' | 'season'; text: string }>();
  // The buttons stay enabled (aria-disabled) while a mark is on its way, so that the focus stays on
  // the one pressed; a press meanwhile does nothing.
  async function mark(t: WatchTarget, watched: boolean, at: 'show' | 'season') {
    if (marking) return;
    // The season shown follows the next episode until one is chosen: keep it where it is.
    if (season === undefined && current !== undefined) redirect(href.show(show, current));
    marking = true;
    markError = undefined;
    try {
      await markWatched(t, watched);
    } catch (e) {
      const why = e instanceof Error ? e.message : String(e);
      markError = { at, text: `Couldn’t mark ${watched ? 'it as watched' : 'it as unwatched'}: ${why}` };
    } finally {
      marking = false;
    }
  }
  const seasonWord = (n: number) => (n === 0 ? 'specials' : 'season');

  async function refresh() {
    notice = '';
    try {
      await api.refreshShow(show);
      notice = 'Fetching the latest details. They appear here in a moment.';
      setTimeout(() => app.libraryVersion++, 4000);
    } catch (e) {
      notice = e instanceof Error ? e.message : String(e);
    }
  }

  // A show from TVDB can be numbered in another of TVDB's orders: every season is mapped again.
  const ORDERS: [EpisodeOrder, string][] = [
    ['aired', 'Aired'],
    ['dvd', 'DVD'],
    ['absolute', 'Absolute'],
  ];
  // Once changed here, the order is what the last PATCH answered: a fetch of the page that started
  // before it (or is still on its way) can't snap the picker back, or swallow a change back. It is
  // about the show's match at the time only: a new match (another TVDB show, or TMDB and back, which
  // keep the title's id) starts at the server's order again, which the match resets to aired. The
  // match of a show is fixed from this page (any other page remounts it), so opening the Fix dialog
  // drops it.
  let changed = $state<EpisodeOrder>();
  const order = $derived(meta?.source === 'tvdb' ? (changed ?? meta.episodeOrder ?? 'aired') : undefined);
  $effect(() => {
    if (fixing) changed = undefined;
  });
  let ordering = $state(false);
  let orderNote = $state('');
  let orderError = $state('');

  async function changeOrder(e: Event & { currentTarget: HTMLSelectElement }) {
    const select = e.currentTarget;
    const next = select.value as EpisodeOrder;
    if (!meta || next === order) return;
    const was = order ?? 'aired';
    ordering = true;
    orderNote = '';
    orderError = '';
    try {
      const t = await api.patchTitle(meta.id, { episodeOrder: next });
      changed = t.episodeOrder ?? next;
      const n = t.unmapped ?? 0;
      if (n > 0) orderNote = n === 1 ? '1 episode doesn’t fit this order.' : `${n} episodes don’t fit this order.`;
      app.libraryVersion++; // the episodes are numbered anew: the seasons, the list and the library
    } catch (err) {
      select.value = was;
      if (err instanceof ApiError && err.status === 502) orderError = 'TVDB can’t be reached right now.';
      else orderError = err instanceof Error ? err.message : String(err);
    } finally {
      ordering = false;
    }
  }

  const menu = $derived([
    {
      label: 'Fix the match',
      run: (): void => {
        fixing = true;
      },
    },
    {
      label: 'Edit details',
      run: (): void => {
        editing = true;
      },
      hidden: !meta,
    },
    {
      label: 'Manage subtitles',
      run: (): void => {
        managingSubs = true;
      },
      hidden: !next,
    },
    { label: 'Refresh details', run: refresh },
  ]);

  useAmbient(() => artOf(entry?.cover)?.color);

  const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`;
  const resumeAt = (e: EpisodeCard) => (progressOf(e) > 0 ? (e.progress?.positionSec ?? 0) : 0);
  const status: Record<string, string> = {
    'Returning Series': 'Still running',
    Ended: 'Ended',
    Canceled: 'Cancelled',
    'In Production': 'In production',
  };
</script>

<BackButton over />
{#if episodeList.error && !episodeList.loaded}
  <div class="page"><p class="error">{episodeList.error}</p></div>
{:else if !episodeList.loaded}
  <div class="skeleton hero-sk" aria-busy="true"></div>
{:else if !entry}
  <div class="page center">
    <h1>No episodes of “{show}”</h1>
    <p><a href={href.shows}>Back to all shows</a></p>
  </div>
{:else}
  <Hero item={entry.cover} img={backdropOf(summary) ?? undefined} label={show} landing back>
    <HeroTitle title={meta?.title || show} {logo} />
    <div class="meta">
      {#if years}<span>{years}</span>{/if}
      {#if meta?.certification}<span class="cert">{meta.certification}</span>{/if}
      <span>{plural(seasons.filter((s) => s !== 0).length || seasons.length, 'season')}</span>
      <span>{plural(totals.episodeCount, 'episode')}</span>
      {#if showBadge}<WatchBadge badge={showBadge} />{/if}
      {#if meta?.status && status[meta.status]}<span>{status[meta.status]}</span>{/if}
      {#if meta}<SourceBadge source={meta.source} />{/if}
    </div>
    <Ratings {ratings} />
    <div class="hero-actions">
      {#if next}
        <a class="btn primary" href={href.play(next.id, resumeAt(next))}>
          <Icon name="play" />
          {resumeAt(next) ? 'Resume' : 'Play'}
          {episodeLabel(next.season, next.episode)}
        </a>
        <button class="btn" onclick={() => (managingSubs = true)}>
          <Icon name="cc" />Subtitles
        </button>
      {/if}
      {#if meta}{#key meta.id}<TrailerButton titleId={meta.id} title={meta.title || show} />{/key}{/if}
      <span class="pair">
        <button class="btn" aria-disabled={marking} onclick={() => mark({ show }, !showDone, 'show')}>
          <Icon name="check" />{showDone ? 'Mark show as unwatched' : 'Mark show as watched'}
        </button>
        <Menu items={menu} />
      </span>
    </div>
  </Hero>

  <!-- Busy until the details are in: a scroll restored on Back waits for them (lib/router). -->
  <div class="body" aria-busy={!detail.data && !detail.error}>
    {#if needsReview}
      <div class="review-note" role="note">
        <Icon name="alert" />
        <p>This might not be the right show.</p>
        <button class="btn" onclick={() => (fixing = true)}>Check the match</button>
      </div>
    {/if}
    {#if notice}<p class="muted notice" role="status">{notice}</p>{/if}
    {#if markError?.at === 'show'}<p class="error notice" role="alert">{markError.text}</p>{/if}

    {#if meta?.overview || summary?.overview || meta?.genres.length || credits.length}
      <section class="about">
        <div class="about-main">
          {#if meta?.overview || summary?.overview}<p class="title-overview">{meta?.overview || summary?.overview}</p>{/if}
          {#if meta?.genres.length}
            <ul class="genres" aria-label="Genres">
              {#each meta.genres as g (g.id)}<li><a class="chip" href={href.genre(g.id)}>{g.name}</a></li>{/each}
            </ul>
          {/if}
        </div>
        {#if credits.length}
          <div class="about-side">
            <dl class="credits">
              {#each credits as line (line.label)}
                <dt>{line.people.length > 1 ? line.plural : line.label}</dt>
                <dd>
                  {#each line.people as p, i (p.personId)}{#if i}{', '}{/if}<a class="link" href={href.person(p.personId)}>{p.name}</a>{/each}
                </dd>
              {/each}
            </dl>
          </div>
        {/if}
      </section>
    {/if}

    {#if order}
      <div class="order">
        <label for="episode-order">Episode order</label>
        <select id="episode-order" class="settings-input" value={order} disabled={ordering} aria-describedby="episode-order-hint" onchange={changeOrder}>
          {#each ORDERS as [value, label] (value)}<option {value}>{label}</option>{/each}
        </select>
        <span id="episode-order-hint" class="muted">
          {ordering ? 'Numbering the episodes again…' : 'How your files are numbered: as aired, as on DVD, or straight through.'}
        </span>
      </div>
      {#if orderError}<p class="error order-msg" role="alert">{orderError}</p>{/if}
      {#if orderNote}<p class="muted order-msg" role="status">{orderNote}</p>{/if}
    {/if}

    <section class="season">
      <div class="season-top" class:solo={seasons.length === 1}>
        <SeasonRow
          seasons={seasonCards}
          selected={current}
          onselect={selectSeason}
          panel="season-panel"
          label={show}
          ready={!!detail.data || !!detail.error}
        />
        <div class="season-head">
          <h2>{seasonName(current)}</h2>
          {#if seasons.length === 1}<span class="muted">{plural(currentCounts.episodeCount, 'episode')}</span>{/if}
          <button
            class="btn"
            aria-disabled={marking || !list.length}
            onclick={() => list.length && mark({ show, season: current }, !seasonDone, 'season')}
          >
            <Icon name="check" />{`Mark ${seasonWord(current)} as ${seasonDone ? 'unwatched' : 'watched'}`}
          </button>
        </div>
      </div>
      {#if markError?.at === 'season'}<p class="error mark-error" role="alert">{markError.text}</p>{/if}
      {#if seasonInfo?.overview}<p class="muted season-overview">{seasonInfo.overview}</p>{/if}

      <ol
        class="episodes"
        id="season-panel"
        role={seasons.length > 1 ? 'tabpanel' : undefined}
        aria-labelledby={seasons.length > 1 ? `season-tab-${current}` : undefined}
      >
        {#each list as ep (ep.id)}
          {@const p = progressOf(ep)}
          {@const r = epRating(ep)}
          {@const what = `Mark ${ep.episode ? episodeLabel(ep.season, ep.episode) : ep.episodeTitle || ep.title} as ${ep.watched ? 'unwatched' : 'watched'}`}
          <li>
            <a class="ep" href={href.item(ep.id)} onclick={morph}>
              <div class="thumb">
                <Art item={ep} label={show} sizes="(max-width: 600px) 7.5rem, 13rem" />
                {#if p > 0}<div class="bar"><span style:width="{p * 100}%"></span></div>{/if}
                {#if ep.watched}<span class="watched"><WatchBadge badge={WATCHED} /></span>{/if}
              </div>
              <div class="text">
                <div class="line">
                  <span class="num tabular">{ep.episode ?? ''}</span>
                  <span class="title">{ep.episodeTitle || `Episode ${ep.episode ?? ''}`.trim()}</span>
                </div>
                <div class="meta small">
                  {#if ep.durationSec}<span>{formatDuration(ep.durationSec)}</span>{/if}
                  {#if r}<Rating entry={r} compact />{/if}
                  <span class="badges"><Badges item={ep} notable /></span>
                </div>
                {#if ep.overview}<p class="ep-overview">{ep.overview}</p>{/if}
              </div>
            </a>
            <button class="btn icon mark" class:on={ep.watched} aria-disabled={marking} aria-label={what} title={what} onclick={() => mark({ item: ep }, !ep.watched, 'season')}>
              <Icon name="check" />
            </button>
            <a class="btn icon play" href={href.play(ep.id, resumeAt(ep))} aria-label="Play {episodeLabel(ep.season, ep.episode)}">
              <Icon name="play" />
            </a>
          </li>
        {/each}
      </ol>
    </section>
  </div>

  {#if meta?.cast.length}
    <div class="cast">
      <Rail title="Cast" items={meta.cast.slice(0, 30)} key={(c) => c.personId} size="person">
        {#snippet card(c)}
          <PersonCard id={c.personId} name={c.name} profile={c.profile} role={c.character} />
        {/snippet}
      </Rail>
    </div>
  {/if}

  <FixMatch bind:open={fixing} kind="series" {show} query={show} matched={!!meta && meta.matchState !== 'unmatched'} />
  {#if meta}<EditTitle bind:open={editing} {meta} />{/if}
  {#if next}
    <SubtitleManager
      bind:open={managingSubs}
      itemId={next.id}
      title={meta?.title || show}
      isEpisode={true}
      show={show}
      season={next.season ?? undefined}
      episode={next.episode ?? undefined}
      subtitles={nextSubs}
      onchange={(newSubs) => {
        nextSubs = newSubs;
      }}
    />
  {/if}
{/if}

<style>
  .body {
    padding: 0.5rem var(--gutter) 0;
    max-width: 72rem;
    display: grid;
    gap: 2rem;
  }
  .cast {
    padding-bottom: 4rem;
  }
  .body:last-child {
    padding-bottom: 4rem;
  }
  .notice {
    margin: 0;
  }
  .order {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.5rem 0.75rem;
  }
  .order label {
    font-weight: var(--fw-semibold-2);
  }
  .order select {
    background: var(--surface-raised);
  }
  .order select:disabled {
    opacity: 0.6;
  }
  .order .muted {
    font-size: var(--fs-sm);
  }
  .order-msg {
    margin: -1.25rem 0 0;
  }
  .about {
    display: grid;
    grid-template-columns: minmax(0, 7fr) minmax(0, 5fr);
    gap: 2rem;
    align-items: start;
    max-width: 72rem;
    padding-top: 1rem;
    border-top: 1px solid var(--line);
  }
  .about-main {
    display: grid;
    gap: 1.25rem;
    min-width: 0;
  }
  .about-side {
    display: grid;
    gap: 1.1rem;
    padding-left: 2rem;
    border-left: 1px solid var(--line);
    min-width: 0;
  }
  @media (max-width: 760px) {
    .about {
      grid-template-columns: 1fr;
      gap: 1.25rem;
    }
    .about-side {
      padding-left: 0;
      border-left: 0;
      padding-top: 1rem;
      border-top: 1px solid var(--line);
    }
  }
  .season-top {
    display: grid;
    gap: 1.25rem;
    min-width: 0;
  }
  /* One season: its card beside its name and button, not a row with one card in it. */
  .season-top.solo {
    grid-template-columns: auto minmax(0, 1fr);
    align-items: center;
  }
  .solo .season-head {
    flex-direction: column;
    align-items: flex-start;
    gap: 0.4rem;
  }
  .solo .season-head button {
    margin-top: 0.6rem;
  }
  .season-head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.5rem 1rem;
  }
  .season-head h2 {
    margin: 0;
  }
  .mark-error {
    margin: 0.75rem 0 0;
  }
  .season-overview {
    margin: 0.9rem 0 0;
    max-width: 65ch;
  }
  .episodes {
    list-style: none;
    margin: 1.25rem 0 0;
    padding: 0;
    display: grid;
    border-top: 1px solid var(--line);
  }
  li {
    display: flex;
    align-items: center;
    gap: 1rem;
    padding: 0.9rem 0.5rem;
    margin: 0 -0.5rem;
    border-bottom: 1px solid var(--line);
  }
  li:hover,
  li:focus-within {
    background: var(--fill-faint);
  }
  .ep {
    flex: 1;
    min-width: 0;
    display: grid;
    grid-template-columns: clamp(8rem, 22vw, 13rem) 1fr;
    align-items: center;
    gap: 1.25rem;
  }
  .ep:focus-visible {
    outline: none;
  }
  .ep:focus-visible .thumb {
    outline: 3px solid var(--text);
    outline-offset: 2px;
  }
  .thumb {
    position: relative;
    border-radius: var(--radius);
    overflow: hidden;
  }
  .thumb :global(.tile) {
    font-size: var(--fs-xl-2);
  }
  .bar {
    position: absolute;
    inset: auto 0 0;
    height: 3px;
    background: var(--progress-track);
  }
  .bar span {
    display: block;
    height: 100%;
    background: var(--accent);
  }
  .watched {
    position: absolute;
    top: 6px;
    right: 6px;
  }
  .mark {
    flex: none;
    color: var(--text-muted);
  }
  .mark.on {
    color: var(--ok);
  }
  .text {
    min-width: 0;
  }
  .line {
    display: flex;
    gap: 0.75rem;
    align-items: baseline;
  }
  .num {
    font-family: var(--font-mono);
    font-size: var(--fs-xs);
    color: var(--text-muted);
    min-width: 1.75rem;
  }
  .title {
    font-family: var(--font-display);
    font-size: var(--fs-2xl-2);
    font-weight: var(--fw-regular);
    line-height: var(--lh-tight);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .small {
    margin-top: 0.2rem;
    padding-left: 2.25rem;
    font-size: var(--fs-sm);
  }
  .ep-overview {
    margin: 0.35rem 0 0;
    padding-left: 2.25rem;
    max-width: 70ch;
    color: var(--text-muted);
    font-size: var(--fs-sm-3);
    line-height: var(--lh-body-2);
    display: -webkit-box;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
  .play {
    flex: none;
  }
  @media (max-width: 600px) {
    .ep {
      grid-template-columns: 7.5rem 1fr;
      gap: 0.8rem;
      align-items: start;
    }
    .small,
    .ep-overview {
      padding-left: 0;
    }
    .ep-overview {
      display: none;
    }
    .num {
      display: none;
    }
  }
</style>
