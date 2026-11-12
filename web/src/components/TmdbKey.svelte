<script lang="ts">
  import { tick } from 'svelte';
  import { api, ApiError, type Settings } from '../lib/api';
  import { app } from '../lib/state.svelte';
  import ConfirmRemove from './ConfirmRemove.svelte';
  import KeyField from './KeyField.svelte';
  import KeyTest from './KeyTest.svelte';
  import TmdbNotice from './TmdbNotice.svelte';

  // Settings → API keys → TMDB: movies' and shows' details. A new key is checked against TMDB
  // before it is stored, and is never shown back.
  let { settings: s, onworks }: { settings: Settings; onworks?: () => void } = $props();

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
  // A key typed and not saved: Test would try the saved one, so it waits for the save (which checks it).
  const testUnavailable = $derived(
    !s.tmdbKeySet
      ? 'Set a key to test it.'
      : s.metadataForcedOff || !s.metadataEnabled
        ? 'Online lookups are off.'
        : key.trim()
          ? 'Save the key to test it.'
          : '',
  );

  async function save(tmdbKey: string, done: string) {
    saving = true;
    message = null;
    try {
      const r = await api.saveSettings({ tmdbKey });
      keySaves++;
      // With lookups switched off nothing is sent to TMDB, the key included:
      // it is stored as it is and checked once lookups are on again.
      message =
        tmdbKey && r.tmdbKeyChecked === false
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
    // Save is disabled and Remove goes away: the focus would fall to the page. Keep it here.
    await tick();
    messageEl?.focus();
  }

  function submit(e: Event) {
    e.preventDefault();
    const k = key.trim();
    // An empty save is not a remove: Remove is.
    if (k) void save(k, 'Key saved. Matching starts in the background.');
  }
</script>

<div class="settings-panel card key-card tmdb" role="group" aria-labelledby="tmdb-h">
  <div>
    <h3 id="tmdb-h">TMDB key</h3>
    <p class="muted">
      Posters, plots, cast and more for movies and shows.
      {#if s.tmdbKeySet}
        A key is set, from {SOURCES[s.tmdbKeySource] ?? 'the server'}.
      {:else}
        No key yet. Get a free one from your
        <a class="link" href="https://www.themoviedb.org/settings/api" target="_blank" rel="noreferrer">TMDB account</a>
        (an API key or a read access token both work).
      {/if}
    </p>
  </div>
  <form class="key-fields" onsubmit={submit}>
    <div class="key-field">
      <label for="tmdb-key">{s.tmdbKeySet ? 'Replace the key' : 'Key'}</label>
      <KeyField id="tmdb-key" set={s.tmdbKeySet} bind:value={key} />
    </div>
    {#if message}
      <p bind:this={messageEl} class="key-message" class:error={!message.ok} class:ok={message.ok} role="status" tabindex="-1">{message.text}</p>
    {/if}
    <div class="key-buttons">
      <button class="btn primary" disabled={saving || !key.trim()}>{saving ? 'Checking…' : 'Save key'}</button>
      {#if s.tmdbKeySet && s.tmdbKeySource === 'settings'}
        <ConfirmRemove label="Remove key" question="Remove the TMDB key?" disabled={saving} onconfirm={() => save('', 'Key removed.')} />
      {/if}
      <KeyTest
        provider="tmdb"
        name="TMDB"
        busy={saving}
        about={[s.tmdbKeySet, s.tmdbKeySource, s.metadataEnabled, s.metadataForcedOff, keySaves].join('|')}
        {onworks}
        unavailable={testUnavailable}
        testedAt={s.tmdbKeyTestedAt}
        testOk={s.tmdbKeyTestOk}
      />
    </div>
  </form>
  <TmdbNotice class="key-notice" />
</div>
