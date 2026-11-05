<script lang="ts">
  import { tick, untrack } from 'svelte';
  import { api, ApiError, type RatingSource, type Settings, type SettingsUpdate } from '../lib/api';
  import { formatDate } from '../lib/credits';
  import { applyRatingPrefs } from '../lib/ratingprefs.svelte';
  import { linkParts, PRIMARY_SOURCES, RATING_SOURCES, SOURCE_NAMES } from '../lib/ratings';
  import { app } from '../lib/state.svelte';
  import Icon from './Icon.svelte';
  import KeyTest from './KeyTest.svelte';

  // Settings → Ratings (M10): the MDBList key (Rotten Tomatoes, Metacritic, Letterboxd and Trakt),
  // the source whose rating cards show, which sources a title's page shows and in what order, and
  // the notices of IMDb and MDBList. The key is checked before it is stored and never shown back,
  // as the TMDB and TVDB keys are. Every choice is saved as it is made.
  let { settings: s }: { settings: Settings } = $props();

  const KEY_SOURCES: Record<string, string> = {
    flag: 'the -mdblist-key flag',
    env: 'the PLY_MDBLIST_KEY environment variable',
    settings: 'this page',
  };
  /** How long a save of a choice may take before it counts as refused. */
  const SAVE_TIMEOUT_MS = 15_000;
  const MDBLIST: RatingSource[] = ['rt_critics', 'rt_audience', 'metacritic', 'letterboxd', 'trakt'];
  const fixed = $derived(s.mdblistKeySource === 'flag' || s.mdblistKeySource === 'env');
  const lookupsOff = $derived(s.metadataForcedOff || !s.metadataEnabled);
  const testUnavailable = $derived(!s.mdblistKeySet ? 'Set a key to test it.' : lookupsOff ? 'Online lookups are off.' : '');
  const available = $derived(s.ratingsAvailable ?? []);

  // ---- the key ----

  let key = $state('');
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
    if (k && !fixed) void saveKey({ mdblistKey: k }, 'Key saved. Ratings are fetched from MDBList in the background.');
  }

  // ---- the choices ----

  // `primary` and `shown` are what the page shows and what was asked for last; `confirmed` is what
  // the server has (its last answer). The card follows the settings it is given until a choice is
  // made here; from then on it owns them: a GET of the settings that started before a save can't
  // put back what the save changed.
  let primary = $state<RatingSource>('imdb');
  let shown = $state<RatingSource[]>([]);
  let confirmed = { primary: 'imdb' as RatingSource, shown: [] as RatingSource[] };
  let owned = false;
  $effect(() => {
    const p = s.ratingsPrimary ?? 'imdb';
    const sh = [...(s.ratingsShown ?? [])];
    if (untrack(() => owned)) return;
    primary = p;
    shown = sh;
    confirmed = { primary: p, shown: [...sh] };
  });
  let choiceMessage = $state<{ ok: boolean; text: string } | null>(null);
  /** What the last change was, said once the server has it. */
  let done = '';
  let sending = false;

  const primaryOptions = $derived(PRIMARY_SOURCES.filter((p) => available.includes(p) || p === primary));
  const rows = $derived([...shown, ...RATING_SOURCES.filter((x) => !shown.includes(x))]);
  const same = (a: readonly string[], b: readonly string[]) => a.length === b.length && a.every((x, i) => x === b[i]);

  function why(src: RatingSource): string {
    if (available.includes(src)) return '';
    if (lookupsOff) return 'online lookups are off';
    if (MDBLIST.includes(src)) return 'needs an MDBList key';
    if (src === 'tmdb') return 'needs a TMDB key';
    return 'not available yet';
  }

  /**
   * Sends what the page has and the server doesn't, one save at a time, the latest last: answers
   * can't cross, and what the server keeps is what was asked for last. Only an answer to the
   * latest choice speaks; a refusal of it puts back what the server has.
   */
  async function flush(said: string) {
    owned = true; // the choice functions set it too, before anything can await
    done = said;
    choiceMessage = null;
    if (sending) return; // the save on its way sends this choice once it is answered
    sending = true;
    try {
      for (;;) {
        const update: SettingsUpdate = {};
        if (primary !== confirmed.primary) update.ratingsPrimary = primary;
        if (!same(shown, confirmed.shown)) update.ratingsShown = [...shown];
        if (update.ratingsPrimary === undefined && update.ratingsShown === undefined) return;
        const asked = { primary, shown: [...shown] };
        const latest = () => primary === asked.primary && same(shown, asked.shown);
        try {
          // A save that hangs would hold every later choice: past SAVE_TIMEOUT_MS it is a refusal.
          const r = await api.saveSettings(update, SAVE_TIMEOUT_MS);
          // One save at a time: this answer is the server's latest state.
          confirmed = { primary: r.ratingsPrimary ?? asked.primary, shown: [...(r.ratingsShown ?? asked.shown)] };
          applyRatingPrefs(r);
          if (update.ratingsPrimary !== undefined) app.libraryVersion++; // the cards' rating comes from the server
          if (!latest()) continue; // changed meanwhile: send the new choice
          // What the server keeps is what the page shows, should it have changed what it was sent.
          if (confirmed.primary !== primary) primary = confirmed.primary;
          if (!same(confirmed.shown, shown)) shown = [...confirmed.shown];
          choiceMessage = { ok: true, text: done };
        } catch (e) {
          if (!latest()) continue;
          primary = confirmed.primary;
          shown = [...confirmed.shown];
          choiceMessage = { ok: false, text: e instanceof Error ? e.message : String(e) };
        }
        return;
      }
    } finally {
      sending = false;
    }
  }

  function changePrimary(e: Event & { currentTarget: HTMLSelectElement }) {
    const next = e.currentTarget.value as RatingSource;
    if (next === primary) return;
    owned = true;
    primary = next;
    void flush(`Cards show the ${SOURCE_NAMES[next]} rating.`);
  }

  function toggle(src: RatingSource, on: boolean) {
    owned = true;
    shown = on ? [...shown, src] : shown.filter((x) => x !== src);
    void flush(on ? `${SOURCE_NAMES[src]} is shown.` : `${SOURCE_NAMES[src]} is hidden.`);
  }

  async function move(src: RatingSource, by: -1 | 1) {
    const i = shown.indexOf(src);
    const j = i + by;
    if (i < 0 || j < 0 || j >= shown.length) return;
    const next = [...shown];
    [next[i], next[j]] = [next[j], next[i]];
    owned = true; // before the tick below: a settings answer landing in it must not undo the move
    shown = next;
    // Keep the focus on the button pressed, or on its twin when that one is now at the end (and
    // disabled), at once: not after the server's answer.
    const atEnd = by < 0 ? j === 0 : j === next.length - 1;
    const focus = `rs-${atEnd ? (by < 0 ? 'down' : 'up') : by < 0 ? 'up' : 'down'}-${src}`;
    await tick();
    document.getElementById(focus)?.focus();
    void flush(`${SOURCE_NAMES[src]} moved ${by < 0 ? 'up' : 'down'}.`);
  }

  const imdbNotice = $derived(linkParts(s.imdbAttribution ?? '', s.imdbAttributionUrl ?? ''));
  const mdblistNotice = $derived(linkParts(s.mdblistAttribution ?? '', s.mdblistAttributionUrl ?? ''));
  const datasetDate = $derived(formatDate(s.imdbDatasetUpdatedAt));
</script>

<div class="settings-panel ratings-card" role="group" aria-label="Ratings">
  <div class="key-part" role="group" aria-labelledby="ratings-key-h">
    <div>
      <h3 id="ratings-key-h">MDBList key</h3>
      <p class="muted">
        IMDb ratings come from IMDb’s own data files and need no key. With an MDBList key, ply adds Rotten Tomatoes,
        Metacritic, Letterboxd and Trakt.
        {#if s.mdblistKeySet}
          A key is set, from {KEY_SOURCES[s.mdblistKeySource ?? ''] ?? 'the server'}.{#if fixed}{' '}Change it there.{/if}
        {:else}
          No key yet. Get a free one from
          <a class="link" href="https://mdblist.com/preferences/" target="_blank" rel="noreferrer">MDBList</a>.
        {/if}
      </p>
    </div>

    <!-- new-password and the managers' opt-outs: a password manager must never fill ply's own token in here. -->
    {#if !fixed}
      <form class="key" onsubmit={submit}>
        <label class="sr-only" for="mdblist-key">{s.mdblistKeySet ? 'Replace the MDBList key' : 'MDBList key'}</label>
        <input
          id="mdblist-key"
          class="settings-input"
          bind:value={key}
          type="password"
          autocomplete="new-password"
          data-1p-ignore
          data-lpignore="true"
          data-bwignore
          spellcheck="false"
          placeholder={s.mdblistKeySet ? 'Paste a new key to replace it' : 'Paste your key'}
        />
        <button class="btn primary" disabled={saving || !key.trim()}>{saving ? 'Checking…' : 'Save key'}</button>
        {#if s.mdblistKeySet && s.mdblistKeySource === 'settings'}
          <button type="button" class="btn" disabled={saving} onclick={() => saveKey({ mdblistKey: '' }, 'Key removed.')}>Remove</button>
        {/if}
      </form>
    {/if}
    {#if keyMessage}
      <p bind:this={keyMessageEl} class="key-message" class:error={!keyMessage.ok} class:ok={keyMessage.ok} role="status" tabindex="-1">
        {keyMessage.text}
      </p>
    {/if}
    <KeyTest provider="mdblist" name="MDBList" busy={saving} unavailable={testUnavailable} about={testAbout} />
  </div>

  <div class="settings-panel-part">
    <div class="primary">
      <div>
        <h3><label for="ratings-primary">Primary rating</label></h3>
        <p class="muted hint">The one rating cards and lists show, and sort and filter by.</p>
      </div>
      <select id="ratings-primary" class="settings-input" value={primary} onchange={changePrimary}>
        {#each primaryOptions as p (p)}<option value={p}>{SOURCE_NAMES[p]}</option>{/each}
      </select>
    </div>

    <fieldset class="shown" aria-describedby="ratings-shown-hint">
      <legend><h3>Shown on a title’s page</h3></legend>
      <p class="muted hint" id="ratings-shown-hint">Tick the ratings to show, and move them into the order you like.</p>
      <ul>
        {#each rows as src, i (src)}
          {@const on = shown.includes(src)}
          {@const note = why(src)}
          <li class:off={!on}>
            <input type="checkbox" id="rs-{src}" checked={on} onchange={(e) => toggle(src, e.currentTarget.checked)} />
            <label for="rs-{src}">
              {SOURCE_NAMES[src]}{#if note}<span class="muted note"> ({note})</span>{/if}
            </label>
            <span class="moves">
              <button
                type="button"
                class="btn icon move"
                id="rs-up-{src}"
                aria-label="Move {SOURCE_NAMES[src]} up"
                disabled={!on || i === 0}
                onclick={() => move(src, -1)}><Icon name="up" /></button
              >
              <button
                type="button"
                class="btn icon move"
                id="rs-down-{src}"
                aria-label="Move {SOURCE_NAMES[src]} down"
                disabled={!on || i === shown.length - 1}
                onclick={() => move(src, 1)}><Icon name="down" /></button
              >
            </span>
          </li>
        {/each}
      </ul>
    </fieldset>
    <p class="choice-message" class:error={choiceMessage && !choiceMessage.ok} class:ok={choiceMessage?.ok} role="status">
      {choiceMessage?.text ?? ''}
    </p>
  </div>

  <div class="settings-panel-part attribution muted">
    <p class="dataset">
      {datasetDate ? `IMDb ratings updated ${datasetDate}.` : 'IMDb ratings not downloaded yet.'}
    </p>
    <!-- The notices as the server gives them: IMDb's terms ask for exactly its words; MDBList is credited with a link. -->
    {#if s.imdbAttribution}
      <p class="imdb-notice">
        {imdbNotice.before}{#if imdbNotice.link}<a class="link" href={s.imdbAttributionUrl} target="_blank" rel="noreferrer">{imdbNotice.link}</a
          >{/if}{imdbNotice.after}
      </p>
    {/if}
    {#if s.mdblistAttribution}
      <p class="mdblist-notice">
        {mdblistNotice.before}{#if mdblistNotice.link}<a class="link" href={s.mdblistAttributionUrl} target="_blank" rel="noreferrer"
            >{mdblistNotice.link}</a
          >{/if}{mdblistNotice.after}
      </p>
    {/if}
  </div>
</div>

<style>
  /* The key's part lays out as the card does. */
  .key-part {
    display: grid;
    gap: inherit;
  }
  .key {
    display: flex;
    flex-wrap: wrap;
    gap: 0.6rem;
  }
  .key input {
    flex: 1 1 16rem;
    min-width: 0;
  }
  .primary {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 0.75rem 2rem;
  }
  .shown {
    margin: 0;
    padding: 0;
    border: 0;
    min-width: 0;
  }
  .shown legend {
    padding: 0;
  }
  .shown ul {
    list-style: none;
    margin: 0.6rem 0 0;
    padding: 0;
    display: grid;
    gap: 0.25rem;
  }
  .shown li {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 0.3rem 0.25rem 0.3rem 0.6rem;
    border-radius: var(--radius);
  }
  .shown li:hover,
  .shown li:focus-within {
    background: var(--bg-3);
  }
  .shown input {
    width: 1.1rem;
    height: 1.1rem;
    flex: none;
    accent-color: var(--accent);
  }
  .shown label {
    flex: 1;
    min-width: 0;
    cursor: pointer;
  }
  .off label {
    color: var(--muted);
  }
  .note {
    font-size: var(--fs-sm);
  }
  .moves {
    display: inline-flex;
    gap: 0.25rem;
    flex: none;
  }
  .move {
    width: var(--chip-h);
    min-height: var(--chip-h);
  }
  .attribution {
    gap: 0.5rem;
    font-size: var(--fs-sm);
  }
</style>
