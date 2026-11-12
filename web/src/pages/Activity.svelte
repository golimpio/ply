<script lang="ts">
  import { untrack } from 'svelte';
  import { activity, actionError, describeTiers, countText, groupQueued, isSynthetic } from '../lib/activity.svelte';
  import { api, ApiError, type Job, type PlayingSession } from '../lib/api';
  import { formatClock } from '../lib/format';
  import BackButton from '../components/BackButton.svelte';
  import Icon from '../components/Icon.svelte';

  // What ply does in the background and what waits, below playback and browsing. The store
  // (lib/activity) follows the queue and what plays live; this page adds the actions.

  const snap = $derived(activity.snap);
  const tiers = $derived(describeTiers(snap));
  const groups = $derived(groupQueued(snap.queued));

  // Being here is seeing it: a failure stops lighting the header's indicator.
  $effect(() => {
    void snap.history[0];
    untrack(() => activity.markSeen());
  });

  // ---- playing now: from the feed while it says (session events, through its reconnects);
  // otherwise polled every 5 s, and at once when the number of plays changes

  let polled = $state<PlayingSession[] | null>(null);
  let sessionsError = $state('');
  const live = $derived(activity.sessions !== null);
  const sessions = $derived(live ? activity.sessions : polled);
  async function loadSessions() {
    try {
      polled = await api.sessions();
      sessionsError = '';
    } catch (e) {
      sessionsError = e instanceof Error ? e.message : String(e);
    }
  }
  $effect(() => {
    void snap.tier.playback.Sessions;
    if (live) return;
    untrack(() => void loadSessions());
  });
  $effect(() => {
    if (live) return;
    const t = setInterval(() => {
      if (document.visibilityState === 'visible') void loadSessions();
    }, 5000);
    return () => clearInterval(t);
  });

  // A clock for the elapsed times, only while something runs.
  let now = $state(Date.now());
  const ticking = $derived(snap.running.length > 0 || !!sessions?.length);
  $effect(() => {
    if (!ticking) return;
    now = Date.now();
    const t = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(t);
  });

  // ---- actions

  let busy = $state<Record<number, boolean>>({});
  let errors = $state<Record<number, string>>({});

  async function act(j: Job, action: 'cancel' | 'bump' | 'retry') {
    busy[j.id] = true;
    delete errors[j.id];
    try {
      if (action === 'cancel') await api.cancelJob(j.id);
      else if (action === 'bump') await api.bumpJob(j.id);
      else await api.retryJob(j.id);
      if (activity.mode !== 'live') void activity.refresh();
    } catch (e) {
      errors[j.id] = actionError(e, action);
    } finally {
      delete busy[j.id];
    }
  }

  let queueBusy = $state(false);
  let queueError = $state('');
  async function setPaused(paused: boolean) {
    queueBusy = true;
    queueError = '';
    try {
      await (paused ? api.pauseQueue() : api.resumeQueue());
      activity.apply(paused ? 'queue.paused' : 'queue.resumed', null);
    } catch (e) {
      queueError = e instanceof Error ? e.message : String(e);
    } finally {
      queueBusy = false;
    }
  }

  let confirming = $state<string | null>(null);
  let stopping = $state<string | null>(null);
  let stopErrors = $state<Record<string, string>>({});
  async function stop(p: PlayingSession) {
    stopping = p.id;
    delete stopErrors[p.id];
    try {
      await api.stopSession(p.id);
      confirming = null;
      if (!live) {
        polled = polled?.filter((s) => s.id !== p.id) ?? null;
        void loadSessions();
      } // live: the feed says it ended
    } catch (e) {
      stopErrors[p.id] = e instanceof ApiError && e.status === 404 ? 'It had already stopped.' : e instanceof Error ? e.message : String(e);
    } finally {
      stopping = null;
    }
  }
  const focus = (el: HTMLElement) => el.focus();

  // ---- history

  const HISTORY_PAGE = 30;
  let filter = $state<'all' | 'failed'>('all');
  let showAll = $state(false);
  const failed = $derived(snap.history.filter((j) => j.state === 'failed'));
  const history = $derived(filter === 'failed' ? failed : snap.history);
  const shown = $derived(showAll ? history : history.slice(0, HISTORY_PAGE));

  // ---- words

  const time = (iso?: string) => (iso ? Date.parse(iso) : NaN);
  const hhmm = (ms: number) => new Date(ms).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  function finishedAt(iso?: string): string {
    const ms = time(iso);
    if (!ms) return '';
    const d = new Date(ms);
    if (d.toDateString() === new Date(now).toDateString()) return hhmm(ms);
    return d.toLocaleString([], { weekday: 'short', hour: '2-digit', minute: '2-digit' });
  }
  function took(j: Job): string {
    const sec = (time(j.finishedAt) - time(j.startedAt)) / 1000;
    if (!(sec >= 0)) return '';
    if (sec < 1) return 'under a second';
    if (sec < 60) return `${Math.round(sec)} s`;
    const m = Math.round(sec / 60);
    return m < 60 ? `${m} min` : `${Math.floor(m / 60)} h ${m % 60} min`;
  }
  const elapsed = (j: Job) => (j.startedAt ? formatClock(Math.max(0, now - time(j.startedAt)) / 1000) : '');
  const retryAt = (j: Job) => (time(j.runAt) > Date.now() ? hhmm(time(j.runAt)) : '');
  function why(j: Job): string {
    if (j.user) return 'You asked for this';
    if (j.priority <= 10) return 'New files';
    if (j.priority <= 20) return 'Details for new files';
    if (j.priority <= 30) return 'Regular scan';
    if (j.priority <= 40) return 'Refresh';
    return 'Housekeeping';
  }
  const KINDS: Record<string, string> = {
    'scan.full': 'Library scans',
    'scan.dirs': 'New files to read',
    'meta.match': 'Details to look up',
    'ratings.imdb': 'IMDb ratings',
    'ratings.mdblist': 'Ratings from MDBList',
    'cache.prune': 'Cache tidying',
  };
  const PLAY_KIND = { direct: 'Direct', remux: 'Remux', transcode: 'Transcode' };
  const STATE = { done: 'Done', failed: 'Failed', cancelled: 'Cancelled' } as Record<string, string>;
  const pct = (j: Job) => `${Math.min(100, (j.done / j.total) * 100)}%`;
  const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;
</script>

{#snippet jobError(j: Job)}
  {#if errors[j.id]}<p class="err" role="alert">{errors[j.id]}</p>{/if}
{/snippet}

{#snippet queuedRow(j: Job, first: boolean)}
  <li class="row" data-job={j.id}>
    <div class="what">
      <span class="title">{j.title}</span>
      <span class="sub muted">
        <span>{why(j)}</span>
        {#if retryAt(j)}<span class="retry">Retry at {retryAt(j)}</span>{/if}
        {#if j.attempts > 0 && j.error}<span class="err-inline">Last try: {j.error}</span>{/if}
      </span>
      {@render jobError(j)}
    </div>
    <div class="actions">
      {#if !first}
        <button class="btn sm" disabled={busy[j.id]} onclick={() => act(j, 'bump')} aria-label="Run next: {j.title}">
          <Icon name="up" />Run next
        </button>
      {/if}
      <button class="btn sm ghost" disabled={busy[j.id]} onclick={() => act(j, 'cancel')} aria-label="Cancel: {j.title}">Cancel</button>
    </div>
  </li>
{/snippet}

<div class="page page-container activity">
  <header class="top">
    <div class="intro">
      <BackButton />
      <h1>Activity</h1>
      <p class="muted">What ply does in the background. Playing and browsing always come first.</p>
    </div>
    <div class="pause" class:paused={snap.paused}>
      <div>
        <span class="label" id="queue-switch">Background work</span>
        <span class="small muted">
          {snap.paused ? 'Paused: nothing new starts; running jobs finish.' : 'On. Pause it to keep new jobs from starting.'}
        </span>
      </div>
      <button
        class="switch"
        role="switch"
        aria-checked={!snap.paused}
        aria-labelledby="queue-switch"
        disabled={queueBusy || !activity.loaded}
        onclick={() => setPaused(!snap.paused)}
      ><span></span></button>
    </div>
    {#if queueError}<p class="err" role="alert">{queueError}</p>{/if}
  </header>

  {#if !activity.loaded}
    {#if activity.error}
      <div class="panel empty">
        <p class="err">{activity.error}</p>
        <button class="btn" onclick={() => activity.refresh()}>Try again</button>
      </div>
    {:else}
      <div class="skeleton block" aria-busy="true"></div>
    {/if}
  {:else}
    <section class="tiers" aria-labelledby="tiers-h">
      <h2 id="tiers-h" class="sr-only">What has priority now</h2>
      <p class="headline" role="status">{tiers.headline}</p>
      <ol class="lanes">
        {#each tiers.lanes as lane, i (lane.key)}
          <li class="lane {lane.state}" data-lane={lane.key}>
            <span class="rank" aria-hidden="true">{i + 1}</span>
            <span class="lane-text">
              <span class="name">{lane.name}</span>
              <span class="state">{lane.text}</span>
            </span>
          </li>
        {/each}
      </ol>
    </section>

    {#if sessions?.length}
      <section aria-labelledby="playing-h">
        <h2 id="playing-h">Playing now<span class="count">{sessions.length}</span></h2>
        <ul class="panel">
          {#each sessions as p (p.id)}
            <li class="row" data-session={p.id}>
              <div class="what">
                <span class="title">{p.title || 'Untitled'}</span>
                <span class="sub muted">
                  <span class="badge method {p.kind}">{PLAY_KIND[p.kind] ?? p.kind}</span>
                  <span>{p.client || 'Unknown device'}</span>
                  <span class="tabular">{p.paused ? 'Paused at' : 'At'} {formatClock(p.positionSec)}</span>
                </span>
                {#if stopErrors[p.id]}<p class="err" role="alert">{stopErrors[p.id]}</p>{/if}
              </div>
              <div class="actions">
                {#if confirming === p.id}
                  <span class="confirm-q">Stop it for whoever is watching?</span>
                  <button class="btn sm danger" disabled={stopping === p.id} onclick={() => stop(p)} {@attach focus}>
                    <Icon name="stop" filled />Stop playback
                  </button>
                  <button class="btn sm ghost" onclick={() => (confirming = null)}>Keep playing</button>
                {:else}
                  <button class="btn sm" onclick={() => (confirming = p.id)} aria-label="Stop: {p.title}">Stop</button>
                {/if}
              </div>
            </li>
          {/each}
        </ul>
      </section>
    {:else if sessionsError}
      <p class="err small">Could not list what is playing: {sessionsError}</p>
    {/if}

    <section aria-labelledby="running-h">
      <h2 id="running-h">Running{#if snap.running.length}<span class="count">{snap.running.length}</span>{/if}</h2>
      {#if snap.running.length}
        <ul class="panel">
          {#each snap.running as j (j.id)}
            <li class="row job" class:held={!!j.held} data-job={j.id}>
              <div class="what">
                <span class="title">{j.title}</span>
                {#if j.held}
                  <span class="hold"><Icon name="pause" />Waiting: {j.held}</span>
                {/if}
                <div
                  class="bar"
                  class:unknown={j.total <= 0}
                  role="progressbar"
                  aria-label="Progress: {j.title}"
                  aria-valuemin={0}
                  aria-valuemax={j.total > 0 ? j.total : undefined}
                  aria-valuenow={j.total > 0 ? j.done : undefined}
                >
                  <span style:width={j.total > 0 ? pct(j) : undefined}></span>
                </div>
                <span class="sub muted">
                  {#if j.note}<span class="note">{j.note}</span>{/if}
                  {#if countText(j)}<span class="tabular">{countText(j)}</span>{/if}
                </span>
                {@render jobError(j)}
              </div>
              <div class="actions">
                <!-- Two fixed cells, so that times and buttons line up from row to row. -->
                <span class="elapsed tabular" title={elapsed(j) ? 'Running for' : undefined}>{elapsed(j)}</span>
                {#if !isSynthetic(j)}
                  <button class="btn sm ghost" disabled={busy[j.id]} onclick={() => act(j, 'cancel')} aria-label="Cancel: {j.title}">Cancel</button>
                {:else}
                  <span></span>
                {/if}
              </div>
            </li>
          {/each}
        </ul>
      {:else}
        <p class="empty-note muted">
          {snap.paused && snap.queued.length
            ? 'Nothing running: the queue is paused.'
            : 'Nothing running. ply rescans your folders on a schedule, and new files are picked up within seconds.'}
        </p>
      {/if}
    </section>

    <section aria-labelledby="queued-h">
      <h2 id="queued-h">Up next{#if snap.queued.length}<span class="count">{snap.queued.length}</span>{/if}</h2>
      {#if snap.queued.length}
        <ol class="panel">
          {#each groups as g, gi (g.jobs[0].id)}
            {#if g.jobs.length === 1}
              {@render queuedRow(g.jobs[0], gi === 0)}
            {:else}
              <li class="group">
                <details>
                  <summary>
                    <span class="title">{KINDS[g.kind] ?? g.jobs[0].title}</span>
                    <span class="muted">{plural(g.jobs.length, 'job')}</span>
                    <span class="chev" aria-hidden="true"><Icon name="right" /></span>
                  </summary>
                  <ol>
                    {#each g.jobs as j, i (j.id)}{@render queuedRow(j, gi === 0 && i === 0)}{/each}
                  </ol>
                </details>
              </li>
            {/if}
          {/each}
        </ol>
      {:else}
        <p class="empty-note muted">Nothing waiting.</p>
      {/if}
    </section>

    <section aria-labelledby="history-h">
      <div class="section-head">
        <h2 id="history-h">Recent</h2>
        <div class="chips" role="radiogroup" aria-label="Show">
          <button class="chip" role="radio" aria-checked={filter === 'all'} onclick={() => (filter = 'all')}>All</button>
          <button class="chip" role="radio" aria-checked={filter === 'failed'} onclick={() => (filter = 'failed')}>
            Failed{#if failed.length}<span class="chip-n tabular">{failed.length}</span>{/if}
          </button>
        </div>
      </div>
      {#if shown.length}
        <ul class="panel history">
          {#each shown as j (j.id)}
            <li class="row past {j.state}" data-job={j.id}>
              <span class="mark" aria-hidden="true"><Icon name={j.state === 'done' ? 'check' : j.state === 'failed' ? 'alert' : 'close'} /></span>
              <div class="what">
                <span class="title"><span class="sr-only">{STATE[j.state]}: </span>{j.title}</span>
                {#if j.error}<p class="reason">{j.error}</p>{/if}
                {@render jobError(j)}
              </div>
              <span class="when muted tabular">
                <span>{finishedAt(j.finishedAt)}</span>
                {#if took(j)}<span class="took">{took(j)}</span>{/if}
              </span>
              {#if j.state !== 'done' && !isSynthetic(j)}
                <div class="actions">
                  <button class="btn sm" disabled={busy[j.id]} onclick={() => act(j, 'retry')} aria-label="Retry: {j.title}">Retry</button>
                </div>
              {/if}
            </li>
          {/each}
        </ul>
        {#if history.length > shown.length}
          <button class="btn sm ghost more" onclick={() => (showAll = true)}>Show {history.length - shown.length} more</button>
        {/if}
      {:else}
        <p class="empty-note muted">{filter === 'failed' ? 'No failures in the last week.' : 'Nothing has run in the last week.'}</p>
      {/if}
    </section>
  {/if}
</div>

<style>
  .activity {
    display: grid;
    gap: 2.25rem;
  }
  section {
    display: grid;
    gap: 0.75rem;
    min-width: 0;
  }
  p {
    margin: 0;
  }
  .small {
    font-size: var(--fs-xs-3);
  }
  .err {
    color: var(--danger);
    font-size: var(--fs-xs-3);
  }

  /* ---- header: title, and the queue's switch */
  .top {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-end;
    justify-content: space-between;
    gap: 1rem 2rem;
  }
  .intro {
    display: grid;
    gap: 0.4rem;
  }
  .pause {
    display: flex;
    align-items: center;
    gap: 1rem;
    max-width: 24rem;
    padding: 0.7rem 0.8rem 0.7rem 1rem;
    background: var(--surface-panel);
    border: 1px solid var(--line);
    border-radius: var(--radius-lg);
    backdrop-filter: var(--glass-blur);
  }
  .pause > div {
    display: grid;
    gap: 0.1rem;
  }
  .pause .label {
    font-weight: var(--fw-semibold-2);
  }
  .pause.paused {
    border-color: color-mix(in oklab, var(--warn) 40%, var(--line));
  }
  .pause.paused .small {
    color: var(--warn);
  }
  /* The shared switch (app.css), green when the queue runs. */
  .switch {
    --switch-on: var(--ok);
    --switch-knob-on: var(--surface-room);
  }

  /* ---- the tiers, in priority order */
  .tiers {
    gap: 0.9rem;
  }
  .headline {
    font-size: var(--fs-base);
    font-weight: var(--fw-medium-2);
    letter-spacing: normal;
    text-wrap: pretty;
  }
  .lanes {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    background: var(--surface-panel);
    border: 1px solid var(--line);
    border-radius: var(--radius-lg);
    backdrop-filter: var(--glass-blur);
    overflow: hidden;
  }
  .lane {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 0.85rem 1rem;
    min-width: 0;
  }
  .lane + .lane {
    border-left: 1px solid var(--line);
  }
  .rank {
    flex: none;
    display: grid;
    place-items: center;
    width: 1.6rem;
    height: 1.6rem;
    font-size: var(--fs-xs);
    font-weight: var(--fw-bold);
    color: var(--text-muted);
    border: 1.5px solid var(--line);
    border-radius: var(--radius-round);
  }
  .lane-text {
    display: grid;
    min-width: 0;
  }
  .lane .name {
    font-weight: var(--fw-semibold-2);
    font-size: var(--fs-md-2);
  }
  .lane .state {
    color: var(--text-muted);
    font-size: var(--fs-xs-3);
  }
  .lane.idle .name {
    color: var(--text-muted);
  }
  .lane.active {
    background: color-mix(in oklab, var(--ok) 7%, transparent);
  }
  .lane.active .rank {
    color: var(--text-inverse);
    background: var(--ok);
    border-color: var(--ok);
  }
  .lane.active .state {
    color: var(--text);
  }
  .lane.waiting .rank {
    color: var(--warn);
    border-color: var(--warn);
  }
  .lane.waiting .state {
    color: var(--warn);
  }

  /* ---- lists */
  h2 .count {
    font-size: var(--fs-rel-sm);
  }
  .panel {
    list-style: none;
    margin: 0;
    padding: 0;
    background: var(--surface-panel);
    border: 1px solid var(--line);
    border-radius: var(--radius-lg);
    backdrop-filter: var(--glass-blur);
    overflow: hidden;
  }
  .panel ol {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 0.75rem 1rem;
    padding: 0.8rem 1rem;
    min-width: 0;
  }
  .panel > li + li,
  .group ol > li {
    border-top: 1px solid var(--line);
  }
  .what {
    flex: 1;
    display: grid;
    gap: 0.35rem;
    min-width: 0;
  }
  .title {
    font-weight: var(--fw-semibold);
    overflow-wrap: anywhere;
  }
  .sub {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.2rem 0.9rem;
    font-size: var(--fs-xs-3);
  }
  .retry {
    color: var(--warn);
  }
  .err-inline {
    color: var(--danger);
    overflow-wrap: anywhere;
  }
  .actions {
    flex: none;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: flex-end;
    gap: 0.5rem;
  }
  .btn.sm {
    min-height: 2rem;
    padding: 0.25rem 0.85rem;
    font-size: var(--fs-xs-3);
    gap: 0.35rem;
  }
  .btn.ghost {
    background: transparent;
    border-color: transparent;
    color: var(--text-muted);
  }
  .btn.ghost:hover:not(:disabled) {
    color: var(--text);
  }
  .btn.danger {
    color: var(--text-inverse);
    background: var(--danger);
    border-color: var(--danger);
  }
  .btn.danger:hover:not(:disabled) {
    background: color-mix(in oklab, var(--danger) 85%, var(--lighten));
  }
  .empty-note {
    padding: 0.9rem 1.1rem;
    background: var(--surface-panel);
    border: 1px solid var(--line);
    border-radius: var(--radius-lg);
    backdrop-filter: var(--glass-blur);
    font-size: var(--fs-sm-4);
  }
  .panel.empty {
    display: grid;
    justify-items: start;
    gap: 0.75rem;
    padding: 1.1rem 1.25rem;
  }
  .block {
    height: 14rem;
  }

  /* ---- running */
  .job {
    align-items: flex-start;
  }
  .job .actions {
    display: grid;
    grid-template-columns: 3.4rem 5.2rem;
    justify-items: end;
    padding-top: 0.05rem;
  }
  .elapsed {
    font-size: var(--fs-xs-3);
    color: var(--text-muted);
  }
  .hold {
    display: inline-flex;
    align-items: center;
    justify-self: start;
    gap: 0.4rem;
    padding: 0.15rem 0.6rem 0.15rem 0.45rem;
    font-size: var(--fs-xs-3);
    font-weight: var(--fw-medium-2);
    color: var(--warn);
    background: color-mix(in oklab, var(--warn) 12%, transparent);
    border-radius: var(--radius-pill);
  }
  .hold :global(svg) {
    flex: none;
    width: 0.85rem;
    height: 0.85rem;
  }
  .bar {
    position: relative;
    height: 4px;
    margin: 0.35rem 0 0.1rem;
    background: var(--surface-control);
    border-radius: var(--radius-pill);
    overflow: hidden;
  }
  .bar span {
    position: absolute;
    inset: 0 auto 0 0;
    background: var(--tint);
    border-radius: inherit;
    transition: width var(--motion-progress) var(--ease);
  }
  .bar.unknown span {
    width: 30%;
    animation: slide var(--motion-sweep) var(--ease) infinite;
  }
  .held .bar span {
    background: color-mix(in oklab, var(--warn) 55%, var(--surface-control));
    animation-play-state: paused;
  }
  @keyframes slide {
    from {
      transform: translateX(-100%);
    }
    to {
      transform: translateX(340%);
    }
  }
  .note {
    overflow-wrap: anywhere;
  }

  /* ---- up next */
  .group summary {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 0.8rem 1rem;
    cursor: pointer;
    list-style: none;
  }
  .group summary::-webkit-details-marker {
    display: none;
  }
  .group summary .title {
    flex: 1;
  }
  .chev {
    display: inline-flex;
    color: var(--text-muted);
    transition: transform var(--motion-toggle) var(--ease);
  }
  .chev :global(svg) {
    width: 1rem;
    height: 1rem;
  }
  .group details[open] .chev {
    transform: rotate(90deg);
  }
  .group ol {
    background: var(--surface-inset);
  }

  /* ---- recent */
  .section-head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 0.75rem;
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
  }
  .chip-n {
    font-size: var(--fs-xs);
    opacity: 0.8;
  }
  .past {
    align-items: center;
  }
  .mark {
    flex: none;
    display: grid;
    place-items: center;
    width: 1.6rem;
    height: 1.6rem;
    border-radius: var(--radius-round);
    color: var(--text-muted);
    background: var(--surface-control);
  }
  .mark :global(svg) {
    width: 0.95rem;
    height: 0.95rem;
  }
  .past.done .mark {
    color: var(--ok);
    background: color-mix(in oklab, var(--ok) 14%, transparent);
  }
  .past.failed .mark {
    color: var(--danger);
    background: color-mix(in oklab, var(--danger) 14%, transparent);
  }
  .past.cancelled .title {
    color: var(--text-muted);
  }
  .reason {
    font-size: var(--fs-xs-3);
    color: var(--danger);
    overflow-wrap: anywhere;
  }
  .when {
    flex: none;
    display: grid;
    justify-items: end;
    font-size: var(--fs-xs-2);
    line-height: var(--lh-compact);
  }
  .took {
    opacity: 0.75;
  }
  .history .actions {
    min-width: 4.6rem;
  }
  .history .actions:empty {
    display: none;
  }
  .more {
    justify-self: start;
  }
  .confirm-q {
    font-size: var(--fs-xs-3);
    color: var(--text-muted);
  }

  @media (max-width: 600px) {
    .activity {
      gap: 1.75rem;
    }
    .pause {
      max-width: none;
      width: 100%;
    }
    .lanes {
      grid-template-columns: 1fr;
    }
    .lane + .lane {
      border-left: 0;
      border-top: 1px solid var(--line);
    }
    .row {
      flex-wrap: wrap;
    }
    .row .actions {
      width: 100%;
      justify-content: flex-end;
    }
    /* Running jobs keep their time and Cancel beside the title. */
    .row.job {
      flex-wrap: nowrap;
      gap: 0.5rem;
    }
    .job .actions {
      width: auto;
      grid-template-columns: 2.8rem 4.6rem;
    }
    .history .actions {
      min-width: 0;
      width: auto;
      margin-left: auto;
    }
    .when {
      justify-items: end;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .bar.unknown span {
      animation: none;
      width: 100%;
      opacity: 0.35;
    }
  }
</style>
