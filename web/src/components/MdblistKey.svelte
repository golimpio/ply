<script lang="ts">
  import { tick } from 'svelte';
  import { api, ApiError, type Settings, type SettingsUpdate } from '../lib/api';
  import { applyRatingPrefs } from '../lib/ratingprefs.svelte';
  import { linkParts } from '../lib/ratings';
  import { app } from '../lib/state.svelte';
  import ConfirmRemove from './ConfirmRemove.svelte';
  import KeyField from './KeyField.svelte';
  import KeyTest from './KeyTest.svelte';

  // Settings → API keys → MDBList (M10): Rotten Tomatoes, Metacritic, Letterboxd and Trakt. The key
  // is checked before it is stored and never shown back, as the TMDB and TVDB keys are.
  let { settings: s }: { settings: Settings } = $props();

  const KEY_SOURCES: Record<string, string> = {
    flag: 'the -mdblist-key flag',
    env: 'the PLY_MDBLIST_KEY environment variable',
    settings: 'this page',
  };
  const fixed = $derived(s.mdblistKeySource === 'flag' || s.mdblistKeySource === 'env');
  const lookupsOff = $derived(s.metadataForcedOff || !s.metadataEnabled);

  let key = $state('');
  // A key typed and not saved: Test would try the saved one, so it waits for the save (which checks it).
  const testUnavailable = $derived(
    !s.mdblistKeySet ? 'Set a key to test it.' : lookupsOff ? 'Online lookups are off.' : key.trim() ? 'Save the key to test it.' : '',
  );
  let saving = $state(false);
  let keyMessage = $state<{ ok: boolean; text: string } | null>(null);
  let keyMessageEl = $state<HTMLElement>();
  let saves = $state(0);
  const testAbout = $derived([s.mdblistKeySet, s.mdblistKeySource, s.metadataEnabled, s.metadataForcedOff, saves].join('|'));

  async function saveKey(update: SettingsUpdate, done: string) {
    saving = true;
    keyMessage = null;
    try {
      const r = await api.saveSettings(update);
      saves++;
      keyMessage =
        update.mdblistKey && r.mdblistKeyChecked === false
          ? { ok: true, text: 'Saved, not checked: online lookups are switched off. The key is checked when they are on.' }
          : { ok: true, text: done };
      key = '';
      applyRatingPrefs(r);
      app.libraryVersion++; // the sources this install can have change: the page and the lists
    } catch (e) {
      const why =
        e instanceof ApiError && e.status === 502 ? 'MDBList could not be reached to check the key.' : e instanceof Error ? e.message : String(e);
      keyMessage = { ok: false, text: why };
    } finally {
      saving = false;
    }
    await tick();
    keyMessageEl?.focus();
  }

  function submit(e: Event) {
    e.preventDefault();
    const k = key.trim();
    // An empty save is not a remove: Remove is.
    if (k && !fixed) void saveKey({ mdblistKey: k }, 'Key saved. Ratings are fetched from MDBList in the background.');
  }

  const notice = $derived(linkParts(s.mdblistAttribution ?? '', s.mdblistAttributionUrl ?? ''));
</script>

<div class="settings-panel card key-card mdblist" role="group" aria-labelledby="mdblist-h">
  <div>
    <h3 id="mdblist-h">MDBList key</h3>
    <p class="muted">
      Adds Rotten Tomatoes, Metacritic, Letterboxd and Trakt ratings. IMDb’s come from its own data files and need no key.
      {#if s.mdblistKeySet}
        A key is set, from {KEY_SOURCES[s.mdblistKeySource ?? ''] ?? 'the server'}.{#if fixed}{' '}Change it there.{/if}
      {:else}
        No key yet. Get a free one from
        <a class="link" href="https://mdblist.com/preferences/" target="_blank" rel="noreferrer">MDBList</a>.
      {/if}
    </p>
  </div>

  {#if !fixed}
    <form class="key-fields" onsubmit={submit}>
      <div class="key-field">
        <label for="mdblist-key">{s.mdblistKeySet ? 'Replace the key' : 'Key'}</label>
        <KeyField id="mdblist-key" set={!!s.mdblistKeySet} bind:value={key} />
      </div>
      {#if keyMessage}
        <p bind:this={keyMessageEl} class="key-message" class:error={!keyMessage.ok} class:ok={keyMessage.ok} role="status" tabindex="-1">
          {keyMessage.text}
        </p>
      {/if}
      <div class="key-buttons">
        <button class="btn primary" disabled={saving || !key.trim()}>{saving ? 'Checking…' : 'Save key'}</button>
        {#if s.mdblistKeySet && s.mdblistKeySource === 'settings'}
          <ConfirmRemove
            label="Remove key"
            question="Remove the MDBList key?"
            disabled={saving}
            onconfirm={() => saveKey({ mdblistKey: '' }, 'Key removed.')}
          />
        {/if}
        <KeyTest
          provider="mdblist"
          name="MDBList"
          busy={saving}
          unavailable={testUnavailable}
          about={testAbout}
          testedAt={s.mdblistKeyTestedAt}
          testOk={s.mdblistKeyTestOk}
        />
      </div>
    </form>
  {:else}
    {#if keyMessage}
      <p bind:this={keyMessageEl} class="key-message" class:error={!keyMessage.ok} class:ok={keyMessage.ok} role="status" tabindex="-1">
        {keyMessage.text}
      </p>
    {/if}
    <div class="key-buttons">
      <KeyTest
        provider="mdblist"
        name="MDBList"
        busy={saving}
        unavailable={testUnavailable}
        about={testAbout}
        testedAt={s.mdblistKeyTestedAt}
        testOk={s.mdblistKeyTestOk}
      />
    </div>
  {/if}

  {#if s.mdblistAttribution}
    <!-- MDBList is credited with a link, as the server gives the notice. -->
    <p class="key-notice muted mdblist-notice">
      {notice.before}{#if notice.link}<a class="link" href={s.mdblistAttributionUrl} target="_blank" rel="noreferrer">{notice.link}</a
        >{/if}{notice.after}
    </p>
  {/if}
</div>

