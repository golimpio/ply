<script lang="ts">
  import { tick } from 'svelte';
  import { api, ApiError, type Settings, type SettingsUpdate } from '../lib/api';
  import { app } from '../lib/state.svelte';
  import ConfirmRemove from './ConfirmRemove.svelte';
  import KeyField from './KeyField.svelte';
  import KeyTest from './KeyTest.svelte';

  // The TVDB card of the settings: shows come from TheTVDB when there is a key. Like the TMDB key,
  // a new key is checked (a login at TVDB, with the PIN) before it is stored, and neither the key
  // nor the PIN is ever shown back.
  let { settings: s, onworks }: { settings: Settings; onworks?: () => void } = $props();

  const SOURCES: Record<string, string> = {
    flag: 'the -tvdb-key flag',
    env: 'the PLY_TVDB_KEY environment variable',
    settings: 'this page',
    default: 'the build',
  };
  const PIN_ELSEWHERE = 'The PIN comes from -tvdb-pin or PLY_TVDB_PIN; change it there.';
  // A key from the command line or the environment beats one stored here: it is changed there.
  const fixed = $derived(s.tvdbKeySource === 'flag' || s.tvdbKeySource === 'env');
  const pinFixed = $derived(s.tvdbPinSource === 'flag' || s.tvdbPinSource === 'env');
  // A server without tvdbPinSource can't say: offer Remove, and read the answer (see save).
  const pinRemovable = $derived(!!s.tvdbPinSet && (s.tvdbPinSource === 'settings' || s.tvdbPinSource === undefined));
  const attributionHost = $derived.by(() => {
    try {
      return new URL(s.tvdbAttributionUrl ?? '').hostname.replace(/^www\./, '');
    } catch {
      return '';
    }
  });

  let key = $state('');
  let pin = $state('');
  // A key or PIN typed and not saved: Test would try the saved one, so it waits for the save (which checks it).
  const testUnavailable = $derived(
    !s.tvdbKeySet
      ? 'Set a key to test it.'
      : s.metadataForcedOff || !s.metadataEnabled
        ? 'Online lookups are off.'
        : key.trim() || pin.trim()
          ? 'Save the key to test it.'
          : '',
  );
  let saving = $state(false);
  let message = $state<{ ok: boolean; text: string } | null>(null);
  let messageEl = $state<HTMLElement>();
  // Saves that reached the server: a key replaced by another looks the same in the settings.
  let saves = $state(0);
  const testAbout = $derived(
    [s.tvdbKeySet, s.tvdbKeySource, s.tvdbPinSet, s.tvdbPinSource, s.metadataEnabled, s.metadataForcedOff, saves].join('|'),
  );

  function said(r: Settings, update: SettingsUpdate, done: string): { ok: boolean; text: string } {
    // A PIN from a flag or the environment is still there after the stored one is removed.
    if (update.tvdbPin === '' && r.tvdbPinSet) return { ok: false, text: PIN_ELSEWHERE };
    // With -metadata=false nothing is sent to TVDB: what was typed is stored as it is and checked later.
    if (update.tvdbKey && r.tvdbKeyChecked === false)
      return { ok: true, text: 'Saved, not checked: online lookups are switched off. The key is checked when they are on.' };
    if (update.tvdbPin && !update.tvdbKey && r.metadataForcedOff)
      return { ok: true, text: 'PIN saved, not checked: online lookups are switched off. It is checked when they are on.' };
    return { ok: true, text: done };
  }

  async function save(update: SettingsUpdate, done: string) {
    saving = true;
    message = null;
    try {
      const r = await api.saveSettings(update);
      saves++;
      message = said(r, update, done);
      key = '';
      pin = '';
      app.libraryVersion++; // shows may move to TVDB: refresh this page and what every page shows
    } catch (e) {
      const why =
        e instanceof ApiError && e.status === 409 && update.tvdbPin !== undefined
          ? PIN_ELSEWHERE
          : e instanceof ApiError && e.status === 502
            ? 'TVDB could not be reached to check the key.'
            : e instanceof Error
              ? e.message
              : String(e);
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
    const p = pin.trim();
    if (!k && !p) return;
    const update: SettingsUpdate = {};
    if (k && !fixed) update.tvdbKey = k;
    if (p && !pinFixed) update.tvdbPin = p;
    if (!update.tvdbKey && update.tvdbPin === undefined) return;
    const done = update.tvdbKey ? 'Key saved. Shows are looked up on TVDB in the background.' : 'PIN saved.';
    void save(update, done);
  }
</script>

<div class="settings-panel card key-card tvdb" role="group" aria-labelledby="tvdb-h">
  <div>
    <h3 id="tvdb-h">TVDB key</h3>
    <p class="muted">
      With a key, TV shows come from TheTVDB. Movies, and shows TVDB doesn’t know, still come from TMDB.
      {#if s.tvdbKeySet}
        A key is set, from {SOURCES[s.tvdbKeySource ?? ''] ?? 'the server'}.{#if fixed}{' '}Change it there.{/if}
      {:else}
        No key yet. Get one from
        <a class="link" href="https://thetvdb.com/api-information" target="_blank" rel="noreferrer">TheTVDB</a>.
      {/if}
    </p>
  </div>

  <form class="key-fields" onsubmit={submit}>
    {#if !fixed}
      <div class="key-field">
        <label for="tvdb-key">{s.tvdbKeySet ? 'Replace the key' : 'Key'}</label>
        <KeyField id="tvdb-key" set={!!s.tvdbKeySet} bind:value={key} />
      </div>
    {/if}
    <div class="key-field">
      {#if pinFixed}
        <span class="label">Subscriber PIN</span>
        <p class="muted hint pin-elsewhere">{PIN_ELSEWHERE}</p>
      {:else}
        <label for="tvdb-pin">Subscriber PIN <span class="muted">(optional)</span></label>
        <KeyField id="tvdb-pin" set={!!s.tvdbPinSet} bind:value={pin} describedby="tvdb-pin-hint" unsetPlaceholder="Only if your key needs one" />
        <p id="tvdb-pin-hint" class="muted hint">
          A user-supported key works with the PIN of a TVDB subscription.{#if s.tvdbPinSet}{' '}A PIN is set.{/if}
        </p>
      {/if}
    </div>
    {#if message}
      <p bind:this={messageEl} class="key-message" class:error={!message.ok} class:ok={message.ok} role="status" tabindex="-1">
        {message.text}
      </p>
    {/if}
    <div class="key-buttons">
      <button class="btn primary" disabled={saving || (!key.trim() && !pin.trim())}>{saving ? 'Checking…' : 'Save'}</button>
      {#if s.tvdbKeySet && s.tvdbKeySource === 'settings'}
        <ConfirmRemove label="Remove key" question="Remove the TVDB key?" disabled={saving} onconfirm={() => save({ tvdbKey: '' }, 'Key removed.')} />
      {/if}
      {#if pinRemovable}
        <ConfirmRemove label="Remove PIN" question="Remove the TVDB PIN?" disabled={saving} onconfirm={() => save({ tvdbPin: '' }, 'PIN removed.')} />
      {/if}
      <KeyTest
        provider="tvdb"
        name="TVDB"
        busy={saving}
        unavailable={testUnavailable}
        about={testAbout}
        {onworks}
        testedAt={s.tvdbKeyTestedAt}
        testOk={s.tvdbKeyTestOk}
      />
    </div>
  </form>

  {#if s.tvdbAttribution}
    <!-- TVDB's terms ask for this notice, with a direct link to TheTVDB.com: shown as the server gives it. -->
    <p class="attribution key-notice muted">
      {s.tvdbAttribution}
      {#if s.tvdbAttributionUrl}<a class="link" href={s.tvdbAttributionUrl} target="_blank" rel="noreferrer">{attributionHost || s.tvdbAttributionUrl}</a>{/if}
    </p>
  {/if}
</div>

