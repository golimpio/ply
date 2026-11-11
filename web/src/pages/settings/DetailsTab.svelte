<script lang="ts">
  import { api } from '../../lib/api';
  import { matchingStatus } from '../../lib/matching.svelte';
  import { href } from '../../lib/router.svelte';
  import { resource } from '../../lib/resource.svelte';
  import { app } from '../../lib/state.svelte';
  import { focusRing, toggleFocusRing } from '../../lib/focusring.svelte';
  import TmdbNotice from '../../components/TmdbNotice.svelte';

  // Settings → Details: online lookups, their language, how matching is going, and a refresh. The
  // keys these lookups use are in API keys (`onkeys` opens it).
  let { onkeys }: { onkeys?: () => void } = $props();

  const settings = resource(() => {
    void app.libraryVersion;
    return api.settings();
  });
  const status = matchingStatus({ poll: true });

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

  let saving = $state(false);
  let message = $state<{ ok: boolean; text: string } | null>(null);

  async function save(update: { metadataEnabled?: boolean; language?: string }, done: string) {
    saving = true;
    message = null;
    try {
      await api.saveSettings(update);
      message = { ok: true, text: done };
      app.libraryVersion++; // lookups may start (or stop): refresh this page and what every page shows
    } catch (e) {
      message = { ok: false, text: e instanceof Error ? e.message : String(e) };
    } finally {
      saving = false;
    }
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

  function openKeys(e: MouseEvent) {
    if (!onkeys || e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return; // a new tab or window: the link's own way
    e.preventDefault();
    onkeys();
  }

  const when = (iso?: string | null) => (iso ? new Date(iso).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' }) : '');
  const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;
</script>

<section aria-labelledby="meta-h">
  <h2 id="meta-h">Movie and show details</h2>
  <p class="muted lead">
    ply fills in posters, plots, cast and more from TMDB, and TV shows from TheTVDB once it has a TVDB key (in
    <a class="link" href={href.settingsTab('keys')} onclick={openKeys}>API keys</a>). It sends only titles and years, never
    your files. <code>.nfo</code> files and artwork next to your media are used first and need no key.
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
          onclick={() => save({ metadataEnabled: !s.metadataEnabled }, s.metadataEnabled ? 'Online lookups are off.' : 'Online lookups are on.')}
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
      {#if message}
        <p class:error={!message.ok} class:ok={message.ok} role="status">{message.text}</p>
      {/if}
    </div>
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
          No TMDB key, so movies wait for one. {status.matching ? 'Looking up shows on TVDB…' : 'Shows are looked up on TVDB.'}
        {:else if status.matching}
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
      </div>
      {#if refreshNote}<p class="muted" role="status">{refreshNote}</p>{/if}
    </div>
  {/if}
  <!-- Whatever loaded: the lookups it is about are switched here. -->
  <TmdbNotice />
</section>

<section aria-labelledby="a11y-h">
  <h2 id="a11y-h">Accessibility</h2>
  <p class="muted lead">Options for visibility, contrast, and navigation preferences.</p>
  <div class="settings-panel card">
    <div class="row">
      <div>
        <h3 id="focus-ring-h">Focus borders</h3>
        <p class="muted">
          Show a prominent outline around the currently focused item for keyboard navigation.
        </p>
      </div>
      <button
        class="switch"
        role="switch"
        aria-checked={focusRing.enabled}
        aria-labelledby="focus-ring-h"
        onclick={toggleFocusRing}
      ><span></span></button>
    </div>
  </div>
</section>

<style>
  h3 label {
    cursor: pointer;
  }
  .row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem 2rem;
  }
  .row + .row {
    padding-top: 1.1rem;
    border-top: 1px solid var(--line);
  }
  .status .buttons {
    display: flex;
    flex-wrap: wrap;
    gap: 0.6rem;
  }
  @media (max-width: 600px) {
    .row {
      align-items: flex-start;
    }
  }
</style>
