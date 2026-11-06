<script lang="ts">
  import { tick } from 'svelte';
  import { api, ApiError, imageUrl, type ReviewEntry, type TitleKind } from '../lib/api';
  import { isMatching } from '../lib/meta';
  import { href } from '../lib/router.svelte';
  import { resource } from '../lib/resource.svelte';
  import { app, rescan, scan } from '../lib/state.svelte';
  import BackButton from '../components/BackButton.svelte';
  import FixMatch from '../components/FixMatch.svelte';
  import KeyTest from '../components/KeyTest.svelte';
  import LibraryFolders from '../components/LibraryFolders.svelte';
  import Picture from '../components/Picture.svelte';
  import PlaybackLimits from '../components/PlaybackLimits.svelte';
  import RatingsSettings from '../components/RatingsSettings.svelte';
  import TvdbKey from '../components/TvdbKey.svelte';

  const settings = resource(() => {
    void app.libraryVersion;
    return api.settings();
  });
  // While matching runs, the status is asked for again every few seconds; when
  // it settles, the rest of the page (and the library) is refreshed once.
  let pollTick = $state(0);
  const status = resource(() => {
    void app.libraryVersion;
    void pollTick;
    return api.metadataStatus();
  });
  const matching = $derived(isMatching(status.data));
  // A Test that works clears the server's "TVDB rejected the key" at once: ask for the status line again.
  const refreshStatus = () => pollTick++;
  let wasMatching = false;
  $effect(() => {
    if (!matching) {
      if (wasMatching) app.libraryVersion++; // done: posters, the review list and counts changed
      wasMatching = false;
      return;
    }
    wasMatching = true;
    const t = setInterval(() => pollTick++, 3000);
    return () => clearInterval(t);
  });
  const review = resource(() => {
    void app.libraryVersion;
    return api.review();
  });

  // TMDB "language" codes people are likely to want; whatever the server has is kept.
  const LANGUAGES: [string, string][] = [
    ['en-US', 'English (US)'],
    ['en-GB', 'English (UK)'],
    ['en-AU', 'English (Australia)'],
    ['pt-BR', 'Português (Brasil)'],
    ['pt-PT', 'Português (Portugal)'],
    ['es-ES', 'Español (España)'],
    ['es-MX', 'Español (México)'],
    ['fr-FR', 'Français'],
    ['de-DE', 'Deutsch'],
    ['it-IT', 'Italiano'],
    ['nl-NL', 'Nederlands'],
    ['sv-SE', 'Svenska'],
    ['ja-JP', '日本語'],
    ['ko-KR', '한국어'],
    ['zh-CN', '中文（简体）'],
  ];

  const SOURCES: Record<string, string> = {
    flag: 'the -tmdb-key flag',
    env: 'the PLY_TMDB_KEY environment variable',
    settings: 'this page',
    default: 'the build',
  };

  let key = $state('');
  let saving = $state(false);
  let message = $state<{ ok: boolean; text: string } | null>(null);
  let messageEl = $state<HTMLElement>();
  // Key saves that reached the server: a key replaced by another looks the same in the settings.
  let keySaves = $state(0);

  /** keepFocus: from the key form, whose Save is disabled and whose Remove goes away after it. */
  async function save(update: Parameters<typeof api.saveSettings>[0], done: string, keepFocus = false) {
    saving = true;
    message = null;
    try {
      const s = await api.saveSettings(update);
      if (update.tmdbKey !== undefined) keySaves++;
      // With lookups switched off nothing is sent to TMDB, the key included:
      // it is stored as it is and checked once lookups are on again.
      const unchecked = !!update.tmdbKey && s.tmdbKeyChecked === false;
      message = unchecked
        ? { ok: true, text: 'Saved, not checked: online lookups are switched off. The key is checked when they are on.' }
        : { ok: true, text: done };
      key = '';
      app.libraryVersion++; // lookups may start (or stop): refresh this page and what every page shows
    } catch (e) {
      const why = e instanceof ApiError && e.status === 502 ? 'TMDB could not be reached to check the key.' : e instanceof Error ? e.message : String(e);
      message = { ok: false, text: why };
    } finally {
      saving = false;
    }
    if (!keepFocus) return;
    await tick();
    messageEl?.focus();
  }

  function saveKey(e: Event) {
    e.preventDefault();
    const k = key.trim();
    if (k) void save({ tmdbKey: k }, 'Key saved. Matching starts in the background.', true);
  }

  let refreshNote = $state('');
  async function refreshAll() {
    refreshNote = '';
    try {
      await api.refreshAll();
      refreshNote = 'Refreshing every title in the background.';
    } catch (e) {
      refreshNote = e instanceof Error ? e.message : String(e);
    }
  }

  // The "fix" dialog for one row of the review list.
  let fixing = $state(false);
  let fix = $state<{ kind: TitleKind; itemId?: number; show?: string; query: string; year?: number; matched: boolean }>({
    kind: 'movie',
    query: '',
    matched: false,
  });
  function openFix(r: ReviewEntry) {
    fix = {
      kind: r.kind,
      itemId: r.itemId,
      show: r.show,
      query: r.kind === 'series' ? r.show || r.title : r.title,
      year: r.year || undefined,
      matched: r.matchState !== 'unmatched', // nothing to remove from one that has no match
    };
    fixing = true;
  }

  const when = (iso?: string | null) => (iso ? new Date(iso).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' }) : '');
  const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;
</script>

<div class="page settings">
  <header>
    <BackButton />
    <h1>Settings</h1>
  </header>

  <section aria-labelledby="meta-h">
    <h2 id="meta-h">Movie and show details</h2>
    <p class="muted lead">
      ply fills in posters, plots, cast and more from TMDB, and TV shows from TheTVDB once it has a TVDB key. It sends
      only titles and years, never your files.
      <code>.nfo</code> files and artwork next to your media are used first and need no key.
    </p>

    {#if settings.error}
      <p class="error">{settings.error}</p>
    {:else if !settings.data}
      <div class="skeleton block" aria-busy="true"></div>
    {:else}
      {@const s = settings.data}
      <div class="settings-panel card">
        <div class="row">
          <div>
            <h3>TMDB key</h3>
            <p class="muted">
              {#if s.tmdbKeySet}
                A key is set, from {SOURCES[s.tmdbKeySource] ?? 'the server'}.
              {:else}
                No key yet. Get a free one from your
                <a class="link" href="https://www.themoviedb.org/settings/api" target="_blank" rel="noreferrer">TMDB account</a>
                (an API key or a read access token both work).
              {/if}
            </p>
          </div>
        </div>
        <form class="key" onsubmit={saveKey}>
          <label class="sr-only" for="tmdb-key">{s.tmdbKeySet ? 'Replace the key' : 'TMDB key'}</label>
          <input
            id="tmdb-key"
            class="text-field"
            bind:value={key}
            type="password"
            autocomplete="new-password"
            data-1p-ignore
            data-lpignore="true"
            data-bwignore
            spellcheck="false"
            placeholder={s.tmdbKeySet ? 'Paste a new key to replace it' : 'Paste your key'}
          />
          <button class="btn primary" disabled={saving || !key.trim()}>{saving ? 'Checking…' : 'Save key'}</button>
          {#if s.tmdbKeySet && s.tmdbKeySource === 'settings'}
            <button type="button" class="btn" disabled={saving} onclick={() => save({ tmdbKey: '' }, 'Key removed.', true)}>
              Remove
            </button>
          {/if}
        </form>
        {#if message}
          <p bind:this={messageEl} class:error={!message.ok} class:ok={message.ok} role="status" tabindex="-1">{message.text}</p>
        {/if}
        <div class="key-test">
          <KeyTest
            provider="tmdb"
            name="TMDB"
            busy={saving}
            about={[s.tmdbKeySet, s.tmdbKeySource, s.metadataEnabled, s.metadataForcedOff, keySaves].join('|')}
            onworks={refreshStatus}
            unavailable={!s.tmdbKeySet ? 'Set a key to test it.' : s.metadataForcedOff || !s.metadataEnabled ? 'Online lookups are off.' : ''}
          />
        </div>

        <div class="row">
          <div>
            <h3 id="online-h">Online lookups</h3>
            <p class="muted">
              {s.metadataForcedOff
                ? 'Turned off when ply was started (-metadata=false). Only local .nfo files and artwork are used.'
                : 'Off keeps everything on this network: only local .nfo files and artwork are used.'}
            </p>
          </div>
          <button
            class="switch"
            role="switch"
            aria-checked={s.metadataEnabled && !s.metadataForcedOff}
            aria-labelledby="online-h"
            disabled={saving || s.metadataForcedOff}
            onclick={() =>
              save({ metadataEnabled: !s.metadataEnabled }, s.metadataEnabled ? 'Online lookups are off.' : 'Online lookups are on.')}
          ><span></span></button>
        </div>

        <div class="row">
          <div>
            <h3><label for="lang">Language</label></h3>
            <p class="muted">For titles, plots and posters, where TMDB has them.</p>
          </div>
          <select
            id="lang"
            class="text-field"
            value={s.language}
            disabled={saving}
            onchange={(e) => save({ language: e.currentTarget.value }, 'Language saved. Refresh details to see it everywhere.')}
          >
            {#if !LANGUAGES.some(([code]) => code === s.language)}<option value={s.language}>{s.language}</option>{/if}
            {#each LANGUAGES as [code, name] (code)}<option value={code}>{name}</option>{/each}
          </select>
        </div>
      </div>

      <!-- Not from a server older than M8, which knows nothing of TVDB. -->
      {#if s.tvdbKeySet !== undefined}<TvdbKey settings={s} onworks={refreshStatus} />{/if}
    {/if}

    {#if status.data}
      {@const m = status.data}
      {@const anyKey = m.keySet || !!m.tvdbKeySet}
      <div class="settings-panel card status">
        <p>
          {#if !m.enabled}
            Matching is off.
          {:else if !anyKey}
            No TMDB key and no TVDB key, so only local .nfo files and artwork are used.
          {:else if !m.keySet}
            <!-- With TVDB alone, movies wait for TMDB for ever: only a running pass is "matching" (and polled). -->
            No TMDB key, so movies wait for one. {matching ? 'Looking up shows on TVDB…' : 'Shows are looked up on TVDB.'}
          {:else if matching}
            Matching {plural(m.pending, 'title')}…
          {:else}
            Everything has been looked up.
          {/if}
          {#if m.lastRefresh}<span class="muted">Last refreshed {when(m.lastRefresh)}.</span>{/if}
        </p>
        <!-- The server's own sentence: "Shows: TVDB", "Shows: TMDB (no TVDB key)", "TVDB unreachable"... -->
        {#if m.enabled && m.showsLine}
          <p class="shows-line" class:muted={m.showsLine.startsWith('Shows:')} class:warn={!m.showsLine.startsWith('Shows:')}>
            {m.showsLine}
          </p>
        {/if}
        {#if m.lastError}<p class="error">{m.lastError}</p>{/if}
        <div class="buttons">
          <!-- With no key the server only re-reads the local files of unmatched titles: nothing to refresh. -->
          <button class="btn" onclick={refreshAll} disabled={!m.enabled || !anyKey}>Refresh all details</button>
          <button class="btn" onclick={rescan} disabled={scan.running}>{scan.running ? 'Scanning…' : 'Scan the library now'}</button>
        </div>
        {#if refreshNote}<p class="muted" role="status">{refreshNote}</p>{/if}
      </div>
    {/if}
  </section>

  <!-- Not from a server older than M10, which has no ratings. -->
  {#if settings.data?.ratingsPrimary !== undefined}
    <section aria-labelledby="ratings-h">
      <h2 id="ratings-h">Ratings</h2>
      <p class="muted lead">
        Scores from IMDb, Rotten Tomatoes, Metacritic, Letterboxd, Trakt and TMDB, beside each title. IMDb’s files are downloaded
        whole; MDBList is sent only a title’s IMDb or TMDB id, never your files or their names.
      </p>
      <RatingsSettings settings={settings.data} />
    </section>
  {/if}

  <section aria-labelledby="review-h">
    <h2 id="review-h">
      Needs attention{#if review.data?.length}<span class="count">{review.data.length}</span>{/if}
    </h2>
    {#if review.error}
      <p class="error">{review.error}</p>
    {:else if !review.data}
      <div class="skeleton block" aria-busy="true"></div>
    {:else if !review.data.length}
      <p class="muted">Every movie and show has a match.</p>
    {:else}
      <ul class="review">
        {#each review.data as r (`${r.kind}:${r.itemId ?? r.show}`)}
          <li>
            <div class="poster">
              <Picture img={r.candidate?.poster ? { src: imageUrl(r.candidate.poster, 'w92') } : null} ratio="2 / 3" label={r.title} />
            </div>
            <div class="text">
              <a class="title" href={r.kind === 'movie' && r.itemId ? href.item(r.itemId) : href.show(r.show ?? r.title)}>
                {r.title}{#if r.year}<span class="muted year">{r.year}</span>{/if}
              </a>
              <p class="muted">
                {#if r.matchState === 'unmatched'}
                  Nothing found{r.kind === 'series' ? ' for this show' : ''}.
                {:else if r.episodes?.length}
                  <!-- Flagged for its episodes: the entry can't say whether the match is also unsure. -->
                  {#if r.candidate}Matched with {r.candidate.title}{r.candidate.year ? ` (${r.candidate.year})` : ''}.{/if}
                {:else if r.candidate}
                  Matched with {r.candidate.title}{r.candidate.year ? ` (${r.candidate.year})` : ''}, but not for sure.
                {:else}
                  Not sure about this match.
                {/if}
              </p>
              {#if r.episodes?.length}
                <details class="episodes">
                  <summary>
                    {plural(r.episodes.length, 'episode')} with no episode number in the file name
                  </summary>
                  <ul>
                    {#each r.episodes as ep (ep.id)}<li><a class="link" href={href.item(ep.id)}><code>{ep.path.split('/').pop()}</code></a></li>{/each}
                  </ul>
                </details>
              {/if}
            </div>
            <button class="btn" onclick={() => openFix(r)}>Fix</button>
          </li>
        {/each}
      </ul>
    {/if}
  </section>

  <section aria-labelledby="folders-h">
    <h2 id="folders-h">Library folders</h2>
    <p class="muted lead">Each folder holds movies or TV shows, never both. Changes take effect at once.</p>
    <LibraryFolders />
  </section>

  <!-- Not at all when the settings did not load (the error is shown above) or the server has no limits (older than M9). -->
  {#if settings.data ? settings.data.maxStreams !== undefined : !settings.error}
    <section aria-labelledby="playback-h">
      <h2 id="playback-h">Playback</h2>
      <p class="muted lead">
        Playing always comes first: background work waits while something plays. When a limit is reached, a new video
        waits and shows what is playing, so that one can be stopped to make room.
      </p>
      {#if settings.data}
        <PlaybackLimits settings={settings.data} />
      {:else}
        <div class="skeleton block" aria-busy="true"></div>
      {/if}
    </section>
  {/if}

  <footer class="attribution">
    <p class="muted">
      <!-- The notice the TMDB API terms (paragraph 3) require, in their words. -->
      This product uses TMDB and the TMDB APIs but is not endorsed, certified, or otherwise approved by
      <a class="link" href="https://www.themoviedb.org/" target="_blank" rel="noreferrer">TMDB</a>.
    </p>
  </footer>
</div>

<FixMatch
  bind:open={fixing}
  kind={fix.kind}
  itemId={fix.itemId}
  show={fix.show}
  query={fix.query}
  year={fix.year}
  matched={fix.matched}
/>

<style>
  .settings {
    max-width: 50rem;
    display: grid;
    gap: 2.75rem;
  }
  section {
    display: grid;
    gap: 1rem;
  }
  .lead {
    margin: 0;
    max-width: 62ch;
  }
  h3 label {
    cursor: pointer;
  }
  .row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem 2rem;
  }
  .row + .row,
  .key-test + .row {
    padding-top: 1.1rem;
    border-top: 1px solid var(--line);
  }
  .key {
    display: flex;
    flex-wrap: wrap;
    gap: 0.6rem;
  }
  input {
    flex: 1 1 16rem;
  }
  .ok {
    color: var(--ok);
  }
  .warn {
    color: var(--warn);
  }
  .status .buttons {
    display: flex;
    flex-wrap: wrap;
    gap: 0.6rem;
  }
  .block {
    height: 9rem;
  }
  .review {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 0.5rem;
  }
  .review li {
    display: grid;
    grid-template-columns: 2.6rem 1fr auto;
    align-items: center;
    gap: 0.9rem;
    padding: 0.5rem 0.75rem 0.5rem 0.5rem;
    background: var(--surface-raised);
    border-radius: var(--radius);
  }
  .review .poster {
    border-radius: var(--radius-sm);
    overflow: hidden;
  }
  .review .poster :global(.tile) {
    font-size: var(--fs-3xs);
  }
  .review .text {
    min-width: 0;
  }
  .review .title {
    font-weight: var(--fw-semibold-2);
  }
  .review .year {
    margin-left: 0.5rem;
    font-weight: var(--fw-medium);
  }
  .review p {
    margin: 0.1rem 0 0;
    font-size: var(--fs-sm);
  }
  .episodes {
    margin-top: 0.3rem;
    font-size: var(--fs-xs-3);
  }
  .episodes summary {
    color: var(--warn);
    cursor: pointer;
  }
  .episodes ul {
    margin: 0.35rem 0 0;
    padding-left: 1.1rem;
  }
  .episodes code {
    overflow-wrap: anywhere;
  }
  .attribution p {
    margin: 0;
    font-size: var(--fs-xs-3);
  }
  @media (max-width: 600px) {
    .row {
      align-items: flex-start;
    }
    .settings {
      padding-bottom: 6rem;
    }
  }
</style>
