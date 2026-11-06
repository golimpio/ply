<script lang="ts">
  import { api, type ItemSummary, type ItemWatch, type Stream } from '../lib/api';
  import { useAmbient } from '../lib/ambient.svelte';
  import {
    channelLabel,
    codecLabel,
    episodeLabel,
    formatBitrate,
    formatBytes,
    formatClock,
    formatDuration,
    methodLabel,
  } from '../lib/format';
  import { ensureLibrary, findItem, library, markWatched } from '../lib/library.svelte';
  import { CREDIT_LINES, crewWith, logoSrc, ratingLabel, runtimeLabel, versionLabel, yearOf } from '../lib/meta';
  import { titleRatings } from '../lib/ratings';
  import { href } from '../lib/router.svelte';
  import { resource } from '../lib/resource.svelte';
  import { app } from '../lib/state.svelte';
  import { artOf, progressOf } from '../lib/ui-types';
  import { WATCHED } from '../lib/watched';
  import BackButton from '../components/BackButton.svelte';
  import Badges from '../components/Badges.svelte';
  import EditTitle from '../components/EditTitle.svelte';
  import FixMatch from '../components/FixMatch.svelte';
  import Hero from '../components/Hero.svelte';
  import HeroTitle from '../components/HeroTitle.svelte';
  import Icon from '../components/Icon.svelte';
  import Menu from '../components/Menu.svelte';
  import PersonCard from '../components/PersonCard.svelte';
  import Rail from '../components/Rail.svelte';
  import Ratings from '../components/Ratings.svelte';
  import SourceBadge from '../components/SourceBadge.svelte';
  import WatchBadge from '../components/WatchBadge.svelte';

  let { id }: { id: number } = $props();

  $effect(() => {
    void app.libraryVersion;
    void ensureLibrary();
  });

  // The item request carries the playback decision for the built-in browser
  // profile. The server describes it without starting a session: no ffmpeg runs
  // until Play (the player then POSTs this browser's measured profile).
  const detail = resource(() => {
    void app.libraryVersion;
    return api.item(id, 'browser');
  });
  const decision = $derived({ data: detail.data?.playback, error: detail.error });

  // The cached summary paints the hero at once (and lands the card morph);
  // the full detail fills in the metadata and the technical section. The watch state is the
  // cached one's when there is one: a mark (here, or on another page) changes it there.
  // An item the cached list doesn't have (it is still loading, or failed) keeps the state of the
  // last mark made here.
  const cached = $derived(findItem(id));
  let own = $state<ItemWatch>();
  const watch = $derived(cached ?? (own?.id === id ? own : undefined));
  const item = $derived<ItemSummary | undefined>(
    detail.data && watch
      ? { ...detail.data, watched: watch.watched, watchedAt: watch.watchedAt, playCount: watch.playCount, progress: watch.progress }
      : (detail.data ?? cached),
  );
  const isEpisode = $derived(item?.kind === 'episode');
  const resumeAt = $derived(item && progressOf(item) > 0 ? (item.progress?.positionSec ?? 0) : 0);
  // The movie's title, or the episode's show.
  const meta = $derived(detail.data?.meta);
  const ep = $derived(detail.data?.episodeInfo);

  // Other files of the same film (4K and 1080p, a director's cut).
  const versions = $derived.by(() => {
    if (!item || item.kind !== 'movie' || !item.titleId) return [];
    const all = library.items.filter((i) => i.kind === 'movie' && i.titleId === item.titleId);
    return all.length > 1 ? all.sort((a, b) => (b.height || 0) - (a.height || 0) || a.id - b.id) : [];
  });

  // Previous / next episode of the same show.
  const siblings = $derived.by(() => {
    if (!item || item.kind !== 'episode') return { prev: undefined, next: undefined };
    const eps = library.items
      .filter((i) => i.kind === 'episode' && i.show === item.show)
      .sort((a, b) => (a.season ?? 0) - (b.season ?? 0) || (a.episode ?? 0) - (b.episode ?? 0));
    const i = eps.findIndex((e) => e.id === item.id);
    return { prev: i > 0 ? eps[i - 1] : undefined, next: i >= 0 ? eps[i + 1] : undefined };
  });

  const title = $derived(item ? (isEpisode ? ep?.name || item.episodeTitle || `Episode ${item.episode ?? ''}`.trim() : meta?.title || item.title) : '');
  const logo = $derived(isEpisode ? '' : logoSrc(meta));
  const year = $derived(isEpisode ? undefined : (yearOf(meta) ?? item?.year));
  const runtime = $derived(meta?.runtimeMin && !isEpisode ? runtimeLabel(meta.runtimeMin) : formatDuration(item?.durationSec ?? 0));
  // An episode's TMDB score (a number, as before M10) beside its own chips (IMDb); a film's chips
  // are every rating of its title, the card's one until the detail has come.
  const epScore = $derived(isEpisode ? ratingLabel(ep?.rating) : '');
  const ratings = $derived(isEpisode ? (ep?.ratings ?? item?.ratings ?? []) : titleRatings(meta, item));
  const overview = $derived(isEpisode ? ep?.overview || item?.overview : meta?.overview);
  const credits = $derived(
    CREDIT_LINES.map((l) => ({ ...l, people: crewWith(meta, l.jobs) })).filter((l) => l.people.length),
  );
  const needsReview = $derived(!!(meta?.needsReview ?? item?.needsReview));
  const matchKind = $derived<'movie' | 'series'>(isEpisode ? 'series' : 'movie');
  // An episode's parent is its show (at its season); a film's is Movies.
  const parent = $derived(
    isEpisode && item?.show ? { hash: href.show(item.show, item.season), label: meta?.title || item.show } : undefined,
  );

  let fixing = $state(false);
  let editing = $state(false);
  let notice = $state('');

  // Marking the film (all its versions) or the episode: the page changes at once and goes back on
  // an error, which is said under the hero.
  let marking = $state(false);
  let markError = $state('');
  // The button stays enabled (aria-disabled) while a mark is on its way, so that it keeps the focus.
  async function toggleWatched() {
    if (!item || marking) return;
    const watched = !item.watched;
    const was = own;
    if (!cached) {
      own = { id, watched, watchedAt: null, playCount: watched ? 1 : 0, progress: watched ? { positionSec: 0, watched: true } : null };
    }
    marking = true;
    markError = '';
    try {
      const answer = (await markWatched({ item }, watched)).items.find((w) => w.id === id);
      if (answer) own = answer;
    } catch (e) {
      own = was;
      markError = `Couldn’t mark it as ${watched ? 'watched' : 'unwatched'}: ${e instanceof Error ? e.message : String(e)}`;
    } finally {
      marking = false;
    }
  }

  async function refresh() {
    if (!item) return;
    notice = '';
    try {
      if (isEpisode && item.show) await api.refreshShow(item.show);
      else await api.refreshItem(item.id);
      notice = 'Fetching the latest details. They appear here in a moment.';
      setTimeout(() => app.libraryVersion++, 4000);
    } catch (e) {
      notice = e instanceof Error ? e.message : String(e);
    }
  }

  const menu = $derived([
    {
      label: isEpisode ? 'Fix the show’s match' : 'Fix the match',
      run: (): void => {
        fixing = true;
      },
    },
    {
      label: 'Edit details',
      run: (): void => {
        editing = true;
      },
      hidden: !meta || isEpisode, // an episode's meta is its show's: edited on the show page
    },
    { label: 'Refresh details', run: refresh },
  ]);

  useAmbient(() => artOf(item)?.color);

  const transfer: Record<string, string> = { smpte2084: 'PQ (HDR10)', 'arib-std-b67': 'HLG' };

  function streamDetails(s: Stream): string {
    const parts: string[] = [];
    if (s.type === 'video') {
      if (s.width && s.height) parts.push(`${s.width}×${s.height}`);
      if (s.profile) parts.push(s.profile);
      if (s.pixFmt) parts.push(s.pixFmt);
      if (s.colorTransfer && transfer[s.colorTransfer]) parts.push(transfer[s.colorTransfer]);
    } else if (s.type === 'audio') {
      if (s.channels) parts.push(channelLabel(s.channels));
      if (s.profile) parts.push(s.profile);
    }
    return parts.join(', ');
  }

  function streamFlags(s: Stream): string {
    return [s.default && 'default', s.forced && 'forced'].filter(Boolean).join(', ');
  }

  const epTitle = (e: ItemSummary) => e.episodeTitle || `Episode ${e.episode ?? ''}`.trim();
  const sources: Record<string, string> = {
    tvdb: 'TheTVDB',
    tmdb: 'TMDB',
    nfo: 'the .nfo file next to it',
    mixed: 'TMDB and the .nfo file',
  };
</script>

<!-- Until the item is known, an episode's parent (its show) isn't: the button keeps its place unseen. -->
<BackButton fallback={parent} pending={!item && !detail.error} over />
{#if !item && detail.error}
  <div class="page center">
    <p class="error">{detail.error}</p>
    <p><a href={href.home}>Back to the library</a></p>
  </div>
{:else if !item}
  <div class="skeleton hero-sk" aria-busy="true"></div>
{:else}
  <Hero {item} label={item.show || item.title} landing back>
    {#if isEpisode && item.show}
      <a class="show link" href={href.show(item.show, item.season)}>{meta?.title || item.show}</a>
    {/if}
    <HeroTitle {title} {logo} />
    {#if meta?.tagline && !isEpisode}<p class="tagline">{meta.tagline}</p>{/if}
    <div class="meta">
      {#if isEpisode}<span>{episodeLabel(item.season, item.episode)}</span>{/if}
      {#if year}<span>{year}</span>{/if}
      {#if meta?.certification}<span class="cert">{meta.certification}</span>{/if}
      {#if runtime}<span>{runtime}</span>{/if}
      {#if epScore}<span class="rating" title="TMDB"><Icon name="star" filled /><span class="sr-only">Rated on TMDB</span>{epScore}</span>{/if}
      <span class="badges">{#if item.watched}<WatchBadge badge={WATCHED} />{/if}<Badges {item} /><SourceBadge source={meta?.source} /></span>
    </div>
    <Ratings {ratings} />
    <div class="hero-actions">
      {#if resumeAt}
        <a class="btn primary" href={href.play(item.id, resumeAt)}>
          <Icon name="play" />Resume from {formatClock(resumeAt)}
        </a>
        <a class="btn" href={href.play(item.id)}>Play from start</a>
      {:else}
        <a class="btn primary" href={href.play(item.id)}><Icon name="play" />Play</a>
      {/if}
      <span class="pair">
        <button class="btn" aria-disabled={marking} onclick={toggleWatched}>
          <Icon name="check" />{item.watched ? 'Mark as unwatched' : 'Mark as watched'}
        </button>
        <Menu items={menu} />
      </span>
    </div>
  </Hero>

  <div class="body">
    {#if needsReview}
      <div class="review-note" role="note">
        <Icon name="alert" />
        <p>{isEpisode ? 'This might not be the right show.' : 'This might not be the right film.'}</p>
        <button class="btn" onclick={() => (fixing = true)}>Check the match</button>
      </div>
    {/if}
    {#if notice}<p class="muted notice" role="status">{notice}</p>{/if}
    {#if markError}<p class="error notice" role="alert">{markError}</p>{/if}

    {#if overview || meta?.genres.length || credits.length || meta?.collection}
      <section class="about">
        {#if overview}<p class="title-overview">{overview}</p>{/if}
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
        {#if meta?.collection}
          <p class="collection">
            Part of <a class="link" href={href.collection(meta.collection.id)}>{meta.collection.name}</a>
          </p>
        {/if}
      </section>
    {/if}

    {#if versions.length}
      <section class="versions">
        <h2>Versions</h2>
        <ul class="chips" aria-label="Versions of this film">
          {#each versions as v (v.id)}
            <li>
              <a class="chip" href={href.item(v.id)} aria-current={v.id === item.id ? 'page' : undefined}>{versionLabel(v)}</a>
            </li>
          {/each}
        </ul>
      </section>
    {/if}
  </div>

  {#if meta?.cast.length}
    <Rail title="Cast" items={meta.cast.slice(0, 30)} key={(c) => c.personId} size="person">
      {#snippet card(c)}
        <PersonCard id={c.personId} name={c.name} profile={c.profile} role={c.character} />
      {/snippet}
    </Rail>
  {/if}

  <!-- Busy until the details are in: a scroll restored on Back waits for them (lib/router). -->
  <div class="body" aria-busy={!detail.data && !detail.error}>
    {#if siblings.prev || siblings.next}
      <nav class="siblings" aria-label="Other episodes">
        {#if siblings.prev}
          <a href={href.item(siblings.prev.id)}>
            <span class="muted">Previous</span>
            <span>{episodeLabel(siblings.prev.season, siblings.prev.episode)} {epTitle(siblings.prev)}</span>
          </a>
        {/if}
        {#if siblings.next}
          <a class="next" href={href.item(siblings.next.id)}>
            <span class="muted">Next</span>
            <span>{episodeLabel(siblings.next.season, siblings.next.episode)} {epTitle(siblings.next)}</span>
          </a>
        {/if}
      </nav>
    {/if}

    <section class="playback">
      <h2>In a web browser</h2>
      {#if decision.error}
        <p class="error">{decision.error}</p>
      {:else if !decision.data}
        <p class="muted">Checking how this plays…</p>
      {:else}
        {@const d = decision.data}
        <p class="decision">
          <span class="badge method {d.method}">{methodLabel(d.method)}</span>
          <span class="muted">
            {d.method === 'direct'
              ? 'Plays the file as it is, with no conversion.'
              : `Video is ${d.videoAction === 'copy' ? 'passed through' : 're-encoded'}, audio is ${
                  d.audioAction === 'copy' ? 'passed through' : 're-encoded'
                }.`}
          </span>
        </p>
      {/if}
    </section>

    <details class="tech">
      <summary>Technical details</summary>
      {#if meta}
        <p class="muted source">
          Details from {sources[meta.source] ?? meta.source}{meta.locked ? ', with your edits' : ''}.
        </p>
      {/if}
      {#if decision.data?.reasons.length}
        <h3>Why this playback method</h3>
        <ul class="reasons">
          {#each decision.data.reasons as reason}<li>{reason}</li>{/each}
        </ul>
      {/if}
      {#if detail.data}
        {@const d = detail.data}
        <h3>File</h3>
        <dl class="kv">
          <dt>Name</dt><dd class="wrap">{d.path}</dd>
          <dt>Container</dt><dd>{d.container}</dd>
          <dt>Video</dt><dd>{[codecLabel(d.videoCodec), d.width ? `${d.width}×${d.height}` : ''].filter(Boolean).join(', ')}</dd>
          <dt>Audio</dt><dd>{codecLabel(d.audioCodec)}</dd>
          <dt>Duration</dt><dd>{formatDuration(d.durationSec) || 'Unknown'}</dd>
          <dt>Size</dt><dd>{formatBytes(d.size) || 'Unknown'}</dd>
          <dt>Bitrate</dt><dd>{formatBitrate(d.bitrate) || 'Unknown'}</dd>
        </dl>

        <h3>Streams</h3>
        <div class="scroll">
          <table class="table">
            <thead>
              <tr><th>#</th><th>Type</th><th>Codec</th><th>Details</th><th>Language</th><th>Title</th><th>Flags</th></tr>
            </thead>
            <tbody>
              {#each d.streams as s (s.index)}
                <tr>
                  <td>{s.index}</td>
                  <td>{s.type}</td>
                  <td>{codecLabel(s.codec)}{#if s.codecTag && s.type === 'video'} <span class="muted">({s.codecTag})</span>{/if}</td>
                  <td>{streamDetails(s)}</td>
                  <td>{s.language ?? ''}</td>
                  <td>{s.title ?? ''}</td>
                  <td>{streamFlags(s)}</td>
                </tr>
              {:else}
                <tr><td colspan="7" class="muted">No stream information.</td></tr>
              {/each}
            </tbody>
          </table>
        </div>

        {#if d.subtitles.length}
          <h3>Subtitles</h3>
          <ul class="reasons">
            {#each d.subtitles as sub (sub.key)}
              <li>
                {sub.title || sub.language || sub.key}
                <span class="muted">{sub.format}{sub.image ? ', image (burned in when selected)' : ''}</span>
              </li>
            {/each}
          </ul>
        {/if}
      {:else if detail.error}
        <p class="error">{detail.error}</p>
      {:else}
        <p class="muted">Loading file details…</p>
      {/if}
    </details>
  </div>

  <FixMatch
    bind:open={fixing}
    kind={matchKind}
    itemId={isEpisode ? undefined : item.id}
    show={isEpisode ? item.show : undefined}
    query={isEpisode ? item.show || '' : item.title}
    year={isEpisode ? undefined : item.year}
    matched={!!meta && meta.matchState !== 'unmatched'}
  />
  {#if meta && !isEpisode}<EditTitle bind:open={editing} {meta} />{/if}
{/if}

<style>
  .show {
    font-weight: var(--fw-semibold-2);
    font-size: var(--fs-lg);
    width: fit-content;
  }
  .tagline {
    margin: -0.2rem 0 0;
    font-size: var(--fs-lede);
    font-style: italic;
    color: var(--text-lede);
    text-wrap: balance;
  }
  .rating {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    color: var(--text);
    font-weight: var(--fw-semibold-2);
  }
  .rating :global(svg) {
    width: 0.95em;
    height: 0.95em;
    color: var(--rating-star);
  }
  .body {
    padding: 0.5rem var(--gutter) 0;
    max-width: 60rem;
    display: grid;
    gap: 2rem;
  }
  .body:last-of-type {
    padding-bottom: 4rem;
  }
  .notice {
    margin: 0;
  }
  .about {
    display: grid;
    gap: 1.25rem;
  }
  .versions .chip {
    display: inline-flex;
    align-items: center;
  }
  .collection {
    margin: 0;
    color: var(--text-muted);
  }
  .versions h2 {
    margin-bottom: 0.6rem;
  }
  .versions ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .versions [aria-current='page'] {
    color: var(--text-inverse);
    background: var(--text);
    border-color: var(--text);
  }
  .siblings {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 1rem;
    margin-top: 1rem;
  }
  .siblings a {
    display: grid;
    gap: 0.1rem;
    padding: 0.85rem 1rem;
    background: var(--surface-raised);
    border-radius: var(--radius);
  }
  .siblings a:hover {
    background: var(--surface-control);
  }
  .siblings .next {
    grid-column: 2;
    text-align: right;
  }
  .siblings span:last-child {
    font-weight: var(--fw-semibold);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .playback h2 {
    margin-bottom: 0.6rem;
  }
  .decision {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.6rem 0.9rem;
    margin: 0;
  }
  .tech {
    border-top: 1px solid var(--line);
    padding-top: 1rem;
  }
  .tech summary {
    cursor: pointer;
    font-weight: var(--fw-semibold-2);
    width: fit-content;
  }
  .tech h3 {
    margin: 1.5rem 0 0.6rem;
    font-size: var(--fs-base);
  }
  .source {
    margin: 1rem 0 0;
    font-size: var(--fs-sm-3);
  }
  .reasons {
    margin: 0;
    padding-left: 1.2rem;
    color: var(--text-muted);
  }
  .reasons .muted {
    margin-left: 0.6rem;
  }
  .kv {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 0.35rem 1.5rem;
    margin: 0;
  }
  .kv dt {
    color: var(--text-muted);
  }
  .kv dd {
    margin: 0;
  }
  .wrap {
    overflow-wrap: anywhere;
  }
  .scroll {
    overflow-x: auto;
  }
  @media (max-width: 600px) {
    .siblings {
      grid-template-columns: 1fr;
    }
    .siblings .next {
      grid-column: 1;
    }
  }
</style>
