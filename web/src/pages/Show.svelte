<script lang="ts">
  import { api, ApiError, type EpisodeOrder, type ItemSummary } from '../lib/api';
  import { useAmbient } from '../lib/ambient.svelte';
  import { episodeLabel, formatDuration, seasonLabel } from '../lib/format';
  import { ensureLibrary, library, showMeta, showsOf } from '../lib/library.svelte';
  import { backdropOf, CREDIT_LINES, crewWith, logoSrc, yearSpan } from '../lib/meta';
  import { morph } from '../lib/nav';
  import { ensureRatingPrefs, ratingPrefs } from '../lib/ratingprefs.svelte';
  import { orderShown, titleRatings } from '../lib/ratings';
  import { href } from '../lib/router.svelte';
  import { resource } from '../lib/resource.svelte';
  import { app } from '../lib/state.svelte';
  import { artOf, progressOf } from '../lib/ui-types';
  import Art from '../components/Art.svelte';
  import Badges from '../components/Badges.svelte';
  import EditTitle from '../components/EditTitle.svelte';
  import FixMatch from '../components/FixMatch.svelte';
  import Hero from '../components/Hero.svelte';
  import HeroTitle from '../components/HeroTitle.svelte';
  import Icon from '../components/Icon.svelte';
  import Menu from '../components/Menu.svelte';
  import PersonCard from '../components/PersonCard.svelte';
  import Rail from '../components/Rail.svelte';
  import Rating from '../components/Rating.svelte';
  import Ratings from '../components/Ratings.svelte';
  import SourceBadge from '../components/SourceBadge.svelte';

  let { show, season }: { show: string; season?: number } = $props();

  $effect(() => {
    void app.libraryVersion;
    void ensureLibrary();
  });

  // The show's metadata and season details; the episodes come from the cached
  // library, so the page paints before this arrives (and without it).
  const detail = resource(() => {
    void app.libraryVersion;
    return api.show(show);
  });

  const entry = $derived(showsOf(library.items.filter((i) => i.show === show))[0]);
  const episodes = $derived(
    library.items
      .filter((i) => i.kind === 'episode' && i.show === show)
      .sort((a, b) => (a.season ?? 0) - (b.season ?? 0) || (a.episode ?? 0) - (b.episode ?? 0)),
  );
  // Regular seasons first, specials last.
  const seasons = $derived(
    [...new Set(episodes.map((e) => e.season ?? 0))].sort((a, b) => (a || 1000) - (b || 1000)),
  );
  // Next up: the first started-but-unfinished episode, else the first unwatched one.
  const next = $derived(
    episodes.find((e) => progressOf(e) > 0) ?? episodes.find((e) => !e.progress?.watched) ?? episodes[0],
  );
  const current = $derived(season !== undefined && seasons.includes(season) ? season : (next?.season ?? seasons[0]));
  const list = $derived(episodes.filter((e) => (e.season ?? 0) === current));

  const meta = $derived(detail.data?.meta);
  const summary = $derived(detail.data ?? showMeta(show));
  const seasonInfo = $derived(detail.data?.seasons.find((s) => s.season === current));
  const seasonName = (n: number) => detail.data?.seasons.find((s) => s.season === n)?.name || seasonLabel(n);
  const logo = $derived(logoSrc(meta));
  const years = $derived(yearSpan(meta, summary?.year));
  const ratings = $derived(titleRatings(meta, summary));
  // An episode's own rating (IMDb), when the settings show that source.
  const epRating = (e: ItemSummary) => orderShown(e.ratings ?? [], ratingPrefs.shown)[0];
  $effect(() => {
    void app.libraryVersion;
    if (list.some((e) => e.ratings?.length)) void ensureRatingPrefs();
  });
  const credits = $derived(CREDIT_LINES.map((l) => ({ ...l, people: crewWith(meta, l.jobs) })).filter((l) => l.people.length));
  const needsReview = $derived(!!(meta?.needsReview ?? summary?.needsReview));

  let fixing = $state(false);
  let editing = $state(false);
  let notice = $state('');

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
    { label: 'Refresh details', run: refresh },
  ]);

  useAmbient(() => artOf(entry?.cover)?.color);

  const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`;
  const resumeAt = (e: ItemSummary) => (progressOf(e) > 0 ? (e.progress?.positionSec ?? 0) : 0);
  const status: Record<string, string> = {
    'Returning Series': 'Still running',
    Ended: 'Ended',
    Canceled: 'Cancelled',
    'In Production': 'In production',
  };
</script>

{#if library.error && !library.loaded}
  <div class="page"><p class="error">{library.error}</p></div>
{:else if !library.loaded}
  <div class="skeleton hero-sk" aria-busy="true"></div>
{:else if !entry}
  <div class="page center">
    <h1>No episodes of “{show}”</h1>
    <p><a href={href.shows}>Back to all shows</a></p>
  </div>
{:else}
  <Hero item={entry.cover} img={backdropOf(summary) ?? undefined} label={show} landing>
    <HeroTitle title={meta?.title || show} {logo} />
    <div class="meta">
      {#if years}<span>{years}</span>{/if}
      {#if meta?.certification}<span class="cert">{meta.certification}</span>{/if}
      <span>{plural(seasons.filter((s) => s !== 0).length || seasons.length, 'season')}</span>
      <span>{plural(episodes.length, 'episode')}</span>
      {#if meta?.status && status[meta.status]}<span>{status[meta.status]}</span>{/if}
      {#if meta}<SourceBadge source={meta.source} />{/if}
    </div>
    <Ratings {ratings} />
    <div class="actions">
      {#if next}
        <a class="btn primary" href={href.play(next.id, resumeAt(next))}>
          <Icon name="play" />
          {resumeAt(next) ? 'Resume' : 'Play'}
          {episodeLabel(next.season, next.episode)}
        </a>
      {/if}
      <Menu items={menu} />
    </div>
  </Hero>

  <div class="body">
    {#if needsReview}
      <div class="review" role="note">
        <Icon name="alert" />
        <p>This might not be the right show.</p>
        <button class="btn" onclick={() => (fixing = true)}>Check the match</button>
      </div>
    {/if}
    {#if notice}<p class="muted notice" role="status">{notice}</p>{/if}

    {#if meta?.overview || summary?.overview || meta?.genres.length || credits.length}
      <section class="about">
        {#if meta?.overview || summary?.overview}<p class="overview">{meta?.overview || summary?.overview}</p>{/if}
        {#if meta?.genres.length}
          <ul class="genres" aria-label="Genres">
            {#each meta.genres as g (g.id)}<li><a class="chip" href={href.genre(g.id)}>{g.name}</a></li>{/each}
          </ul>
        {/if}
        {#if credits.length}
          <dl class="credits">
            {#each credits as line (line.label)}
              <dt>{line.people.length > 1 ? line.plural : line.label}</dt>
              <dd>
                {#each line.people as p, i (p.personId)}{#if i}{', '}{/if}<a class="link" href={href.person(p.personId)}>{p.name}</a>{/each}
              </dd>
            {/each}
          </dl>
        {/if}
      </section>
    {/if}

    {#if order}
      <div class="order">
        <label for="episode-order">Episode order</label>
        <select id="episode-order" value={order} disabled={ordering} aria-describedby="episode-order-hint" onchange={changeOrder}>
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
      {#if seasons.length > 1}
        <div class="chips" role="tablist" aria-label="Seasons">
          {#each seasons as s (s)}
            <a class="chip" role="tab" aria-selected={s === current} href={href.show(show, s)}>{seasonName(s)}</a>
          {/each}
        </div>
      {:else}
        <h2>{seasonName(current)}</h2>
      {/if}
      {#if seasonInfo?.overview}<p class="muted season-overview">{seasonInfo.overview}</p>{/if}

      <ol class="episodes" role={seasons.length > 1 ? 'tabpanel' : undefined}>
        {#each list as ep (ep.id)}
          {@const p = progressOf(ep)}
          {@const r = epRating(ep)}
          <li>
            <a class="ep" href={href.item(ep.id)} onclick={morph}>
              <div class="thumb">
                <Art item={ep} label={show} sizes="(max-width: 600px) 7.5rem, 13rem" />
                {#if p > 0}<div class="bar"><span style:width="{p * 100}%"></span></div>{/if}
                {#if ep.progress?.watched}<span class="watched">Watched</span>{/if}
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
{/if}

<style>
  .hero-sk {
    height: min(68vh, 56vw);
    border-radius: 0;
  }
  .cert {
    padding: 0 0.4rem;
    font-size: 0.8rem;
    font-weight: 700;
    line-height: 1.5;
    border: 1px solid currentColor;
    border-radius: 4px;
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.75rem;
    margin-top: 0.4rem;
  }
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
  .review {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.75rem 1rem;
    padding: 0.85rem 1rem;
    background: color-mix(in oklab, var(--warn) 12%, var(--bg-2));
    border: 1px solid color-mix(in oklab, var(--warn) 40%, transparent);
    border-radius: var(--radius);
  }
  .review :global(svg) {
    width: 1.3rem;
    height: 1.3rem;
    color: var(--warn);
    flex: none;
  }
  .review p {
    margin: 0;
    flex: 1;
    min-width: 12rem;
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
    font-weight: 650;
  }
  .order select {
    min-height: 2.5rem;
    padding: 0.45rem 0.85rem;
    font: inherit;
    color: var(--text);
    background: var(--bg-2);
    border: 1px solid var(--line);
    border-radius: var(--radius);
  }
  .order select:focus-visible {
    border-color: var(--text);
    outline: 2px solid var(--text);
    outline-offset: 2px;
  }
  .order select:disabled {
    opacity: 0.6;
  }
  .order .muted {
    font-size: 0.875rem;
  }
  .order-msg {
    margin: -1.25rem 0 0;
  }
  .about {
    display: grid;
    gap: 1.25rem;
    max-width: 60rem;
  }
  .overview {
    margin: 0;
    max-width: 65ch;
    font-size: 1.08rem;
    line-height: 1.6;
  }
  .genres {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
  }
  .genres .chip {
    display: inline-flex;
    align-items: center;
  }
  .credits {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 0.4rem 1.5rem;
    margin: 0;
  }
  .credits dt {
    color: var(--muted);
  }
  .credits dd {
    margin: 0;
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
    gap: 0.5rem;
  }
  li {
    display: flex;
    align-items: center;
    gap: 1rem;
    padding: 0.5rem;
    margin: 0 -0.5rem;
    border-radius: var(--radius-lg);
  }
  li:hover,
  li:focus-within {
    background: rgb(255 255 255 / 0.04);
  }
  .ep {
    flex: 1;
    min-width: 0;
    display: grid;
    grid-template-columns: clamp(8rem, 22vw, 13rem) 1fr;
    align-items: center;
    gap: 1.1rem;
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
    font-size: 1.2rem;
  }
  .bar {
    position: absolute;
    inset: auto 0 0;
    height: 4px;
    background: rgb(0 0 0 / 0.5);
  }
  .bar span {
    display: block;
    height: 100%;
    background: var(--text);
  }
  .watched {
    position: absolute;
    top: 6px;
    right: 6px;
    padding: 0.05rem 0.45rem;
    font-size: 0.72rem;
    font-weight: 650;
    background: rgb(16 14 12 / 0.75);
    border-radius: 4px;
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
    color: var(--muted);
    min-width: 1.5rem;
  }
  .title {
    font-weight: 650;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .small {
    margin-top: 0.2rem;
    padding-left: 2.25rem;
    font-size: 0.875rem;
  }
  .ep-overview {
    margin: 0.35rem 0 0;
    padding-left: 2.25rem;
    max-width: 70ch;
    color: var(--muted);
    font-size: 0.9rem;
    line-height: 1.45;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
  .badges {
    display: inline-flex;
    gap: 4px;
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
    .credits {
      grid-template-columns: 1fr;
      gap: 0.1rem;
    }
    .credits dd {
      margin-bottom: 0.5rem;
    }
  }
</style>
