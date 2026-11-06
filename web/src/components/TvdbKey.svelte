<script lang="ts">
  import { tick } from 'svelte';
  import { api, ApiError, type Settings, type SettingsUpdate } from '../lib/api';
  import { app } from '../lib/state.svelte';
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
  const testUnavailable = $derived(
    !s.tvdbKeySet ? 'Set a key to test it.' : s.metadataForcedOff || !s.metadataEnabled ? 'Online lookups are off.' : '',
  );
  const attributionHost = $derived.by(() => {
    try {
      return new URL(s.tvdbAttributionUrl ?? '').hostname.replace(/^www\./, '');
    } catch {
      return '';
    }
  });

  let key = $state('');
  let pin = $state('');
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

<div class="settings-panel card tvdb" role="group" aria-labelledby="tvdb-h">
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

  <!-- new-password and the managers' opt-outs: a password manager must never fill ply's own
       token (saved by the sign-in prompt) in here, where Save would send it to TVDB. -->
  <form class="fields" onsubmit={submit}>
    {#if !fixed}
      <div class="field">
        <label for="tvdb-key">{s.tvdbKeySet ? 'Replace the key' : 'Key'}</label>
        <input
          id="tvdb-key"
          class="text-field"
          bind:value={key}
          type="password"
          autocomplete="new-password"
          data-1p-ignore
          data-lpignore="true"
          data-bwignore
          spellcheck="false"
          placeholder={s.tvdbKeySet ? 'Paste a new key to replace it' : 'Paste your key'}
        />
      </div>
    {/if}
    <div class="field">
      {#if pinFixed}
        <span class="label">Subscriber PIN</span>
        <p class="muted hint pin-elsewhere">{PIN_ELSEWHERE}</p>
      {:else}
        <label for="tvdb-pin">Subscriber PIN <span class="muted">(optional)</span></label>
        <input
          id="tvdb-pin"
          class="text-field"
          bind:value={pin}
          type="password"
          autocomplete="new-password"
          data-1p-ignore
          data-lpignore="true"
          data-bwignore
          spellcheck="false"
          aria-describedby="tvdb-pin-hint"
          placeholder={s.tvdbPinSet ? 'Type a new PIN to replace it' : 'Only if your key needs one'}
        />
        <p id="tvdb-pin-hint" class="muted hint">
          A user-supported key works with the PIN of a TVDB subscription.{#if s.tvdbPinSet}{' '}A PIN is set.{/if}
        </p>
      {/if}
    </div>
    <div class="buttons">
      <button class="btn primary" disabled={saving || (!key.trim() && !pin.trim())}>{saving ? 'Checking…' : 'Save'}</button>
      {#if s.tvdbKeySet && s.tvdbKeySource === 'settings'}
        <button type="button" class="btn" disabled={saving} onclick={() => save({ tvdbKey: '' }, 'Key removed.')}>Remove key</button>
      {/if}
      {#if pinRemovable}
        <button type="button" class="btn" disabled={saving} onclick={() => save({ tvdbPin: '' }, 'PIN removed.')}>Remove PIN</button>
      {/if}
    </div>
  </form>
  {#if message}
    <p bind:this={messageEl} class="message" class:error={!message.ok} class:ok={message.ok} role="status" tabindex="-1">
      {message.text}
    </p>
  {/if}

  <KeyTest provider="tvdb" name="TVDB" busy={saving} unavailable={testUnavailable} about={testAbout} {onworks} />

  {#if s.tvdbAttribution}
    <!-- TVDB's terms ask for this notice, with a direct link to TheTVDB.com: shown as the server gives it. -->
    <p class="attribution muted">
      {s.tvdbAttribution}
      {#if s.tvdbAttributionUrl}<a class="link" href={s.tvdbAttributionUrl} target="_blank" rel="noreferrer">{attributionHost || s.tvdbAttributionUrl}</a>{/if}
    </p>
  {/if}
</div>

<style>
  .fields {
    display: grid;
    gap: 0.9rem;
  }
  .field {
    display: grid;
    gap: 0.35rem;
  }
  label,
  .label {
    font-weight: var(--fw-semibold);
    font-size: var(--fs-md);
  }
  label .muted {
    font-weight: var(--fw-regular);
  }
  /* A notch smaller than the shared .settings-panel .hint: .tvdb makes this rule the more specific
     one, rather than leaving it to the order of the stylesheets. */
  .tvdb .hint {
    font-size: var(--fs-xs-3);
  }
  .buttons {
    display: flex;
    flex-wrap: wrap;
    gap: 0.6rem;
  }
  .ok {
    color: var(--ok);
  }
  .attribution {
    padding-top: 1.1rem;
    border-top: 1px solid var(--line);
    font-size: var(--fs-xs-3);
  }
</style>
