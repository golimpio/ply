<script lang="ts">
  import { ApiError, api, type FolderKind, type LibraryFolder } from '../lib/api';
  import { resource } from '../lib/resource.svelte';
  import { app, attachToRunningScan } from '../lib/state.svelte';
  import Dialog from './Dialog.svelte';
  import FolderPicker from './FolderPicker.svelte';
  import Icon from './Icon.svelte';

  // Settings → Library folders (docs/M7-FOLDERS.md): the folders ply serves, each
  // movies or TV shows, added, retyped and removed with no restart.

  const folders = resource(() => {
    void app.libraryVersion;
    return api.folders();
  });
  const scanInfo = resource(() => {
    void app.libraryVersion;
    return api.scanStatus();
  });
  const watchOf = (path: string) => scanInfo.data?.watching?.find((w) => w.root === path);

  const KIND_NAME: Record<FolderKind, string> = { movies: 'Movies', shows: 'TV shows' };
  const KIND_IN_TEXT: Record<FolderKind, string> = { movies: 'movies', shows: 'TV shows' };
  const KINDS: FolderKind[] = ['movies', 'shows'];
  const baseName = (p: string) => p.split('/').filter(Boolean).pop() ?? p;
  const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;
  const when = (unix: number) =>
    new Date(unix * 1000).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
  const whenIso = (iso?: string | null) => (iso ? new Date(iso).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' }) : '');
  const messageOf = (e: unknown) => (e instanceof Error ? e.message : String(e));

  // The library changed under every page: refresh them all, and follow the scan the server started.
  function changed() {
    app.libraryVersion++;
    void attachToRunningScan();
  }

  // A 404 (gone) or 409 (changed at startup, already there) means this list is out of date.
  function refetchIfStale(e: unknown) {
    if (e instanceof ApiError && (e.status === 404 || e.status === 409)) app.libraryVersion++;
  }

  // One line under the list that says what just happened, then goes away.
  let notice = $state('');
  let noticeTimer: ReturnType<typeof setTimeout> | undefined;
  function say(text: string) {
    notice = text;
    clearTimeout(noticeTimer);
    noticeTimer = setTimeout(() => (notice = ''), 8000);
  }
  $effect(() => () => clearTimeout(noticeTimer));

  let picking = $state(false);
  function added(f: LibraryFolder) {
    say(`${baseName(f.path)} was added. Its files are being read now.`);
    changed();
  }

  // Changing a folder's type, after a confirmation: every file is read again.
  let retype = $state<{ f: LibraryFolder; kind: FolderKind } | null>(null);
  let retypeOpen = $state(false);
  let retyping = $state(false);
  let retypeError = $state('');
  function askRetype(f: LibraryFolder, kind: FolderKind) {
    if (f.kind === kind) return;
    retype = { f, kind };
    retypeError = '';
    retypeOpen = true;
  }
  async function doRetype() {
    const r = retype;
    if (!r || retyping) return;
    retyping = true;
    retypeError = '';
    try {
      await api.setFolderKind(r.f.id, r.kind);
      retypeOpen = false;
      say(`${baseName(r.f.path)} is read as ${KIND_IN_TEXT[r.kind]} now.`);
      changed();
    } catch (e) {
      retypeError = messageOf(e);
      if (!retypeOpen) say(`${baseName(r.f.path)} was not changed: ${retypeError}`);
      refetchIfStale(e);
    } finally {
      retyping = false;
    }
  }

  // Removing a folder: its items leave the library, the files on disk stay.
  // The server waits up to 30 s for a running scan, then answers 503 with
  // Retry-After (nothing removed); that is retried a few times while the dialog is open.
  const MAX_RETRIES = 3;
  let removing = $state<LibraryFolder | null>(null);
  let removeOpen = $state(false);
  let forget = $state(false);
  let busy = $state(false);
  let waitingLong = $state(false);
  let removeError = $state('');
  let retryIn = $state(0);
  let retries = 0;
  let retryTimer: ReturnType<typeof setInterval> | undefined;
  function stopRetrying() {
    clearInterval(retryTimer);
    retryTimer = undefined;
    retryIn = 0;
  }
  $effect(() => {
    if (!removeOpen) stopRetrying(); // closing the dialog gives up
  });
  $effect(() => () => stopRetrying());

  function askRemove(f: LibraryFolder) {
    stopRetrying();
    removing = f;
    forget = false;
    removeError = '';
    retries = 0;
    removeOpen = true;
  }
  async function doRemove() {
    const f = removing;
    if (!f || busy) return;
    const forgetIt = forget;
    stopRetrying();
    busy = true;
    removeError = '';
    const slow = setTimeout(() => (waitingLong = true), 1500);
    try {
      await api.removeFolder(f.id, forgetIt);
      removeOpen = false;
      say(`${baseName(f.path)} was removed from the library.`);
      changed();
    } catch (e) {
      if (e instanceof ApiError && e.status === 503) {
        const busyNow = 'A scan is still running, so nothing was removed.';
        if (removeOpen && retries < MAX_RETRIES) {
          retries++;
          removeError = busyNow;
          retryIn = Math.min(Math.max(e.retryAfter ?? 5, 1), 30);
          retryTimer = setInterval(() => {
            if (--retryIn > 0) return;
            stopRetrying();
            void doRemove();
          }, 1000);
        } else {
          removeError = `${busyNow} Try again in a moment.`;
        }
      } else {
        removeError = messageOf(e);
        refetchIfStale(e);
      }
      if (!removeOpen) say(`${baseName(f.path)} was not removed: ${removeError}`);
    } finally {
      clearTimeout(slow);
      busy = false;
      waitingLong = false;
    }
  }
</script>

{#if scanInfo.data}
  {@const sc = scanInfo.data}
  <p class="muted summary">
    {plural(sc.itemCount, 'item')} in the library.
    {#if sc.lastFinished}Last full scan {whenIso(sc.lastFinished)}.{/if}
  </p>
{/if}

{#if folders.error}
  <p class="error">{folders.error}</p>
{:else if !folders.data}
  <div class="skeleton block" aria-busy="true"></div>
{:else}
  {#if !folders.data.length}
    <div class="empty card">
      <p>No folders yet. Add the folder your movies or your TV shows are in.</p>
    </div>
  {:else}
    <ul class="folders" aria-label="Library folders">
      {#each folders.data as f, i (f.path)}
        {@const w = watchOf(f.path)}
        {@const noteId = `folder-note-${i}`}
        <li class="card">
          <div class="head">
            <Icon name="folder" />
            <code class="path">{f.path}</code>
            {#if f.source === 'flag'}<span class="tag" title="Given on the command line">Set at startup</span>{/if}
          </div>

          <p class="muted facts">
            {f.items ? `${plural(f.items, 'item')}.` : 'No items yet.'}
            {#if f.lastScanAt}Scanned {when(f.lastScanAt)}.{:else if f.state === 'ok'}Not scanned yet.{/if}
          </p>

          {#if f.state === 'missing'}
            <p class="warn">Can't be found. Is the disk or share connected?</p>
          {:else if f.state === 'error'}
            <p class="warn">Can't be read{f.error ? `: ${f.error}` : ''}.</p>
          {:else if w}
            {#if w.watching}
              <p class="ok">New files show up within seconds.</p>
            {:else}
              <p class="warn">Not watched{w.error ? `: ${w.error}` : ''}. New files wait for the regular scan.</p>
            {/if}
          {/if}

          <div class="controls">
            {#if !f.kind}
              <p class="muted">The type is guessed from the file names after the first scan.</p>
            {:else}
              <div
                class="seg"
                role="group"
                aria-label={`Type of ${baseName(f.path)}`}
                aria-describedby={f.kindLocked || f.guessed ? noteId : undefined}
              >
                {#each KINDS as k (k)}
                  <button
                    type="button"
                    aria-pressed={f.kind === k}
                    disabled={f.kindLocked || !f.id}
                    onclick={() => askRetype(f, k)}
                  >{KIND_NAME[k]}</button>
                {/each}
              </div>
            {/if}
            {#if f.kindLocked}
              <span class="muted note" id={noteId}>Set at startup with -{f.kind}: the type is fixed and the folder can't be removed here.</span>
            {:else if f.source === 'flag'}
              <span class="muted note" id={noteId}>Set at startup, so it can't be removed here.{f.guessed ? ' The type was guessed from the file names.' : ''}</span>
            {:else if f.guessed}
              <span class="muted note" id={noteId}>Guessed from the file names. Change it if it's wrong.</span>
            {/if}
            {#if f.source === 'ui' && f.id}
              <button type="button" class="btn remove" aria-label={`Remove ${f.path}`} onclick={() => askRemove(f)}>Remove</button>
            {/if}
          </div>
        </li>
      {/each}
    </ul>
  {/if}
  <div class="add">
    <button type="button" class="btn primary" onclick={() => (picking = true)}><Icon name="plus" /> Add a folder</button>
    <p class="muted" role="status">{notice}</p>
  </div>
{/if}

<FolderPicker bind:open={picking} onadded={added} />

<Dialog bind:open={retypeOpen} title={retype ? `Read ${baseName(retype.f.path)} as ${KIND_IN_TEXT[retype.kind]}?` : ''}>
  {#if retype}
    <p>
      ply reads the {plural(retype.f.items, 'file')} in <code>{retype.f.path}</code> again, each as
      {retype.kind === 'shows' ? 'an episode of a show' : 'a movie'}.
    </p>
    <p class="muted">
      Watch progress is kept. Details are looked up again. A match you fixed by hand that no longer fits is put
      aside, and comes back if you change the type back.
    </p>
    {#if retypeError}<p class="error" role="alert">{retypeError}</p>{/if}
  {/if}
  {#snippet footer()}
    <button type="button" class="btn" onclick={() => (retypeOpen = false)}>Cancel</button>
    <button type="button" class="btn primary" disabled={retyping} onclick={doRetype}>
      {retyping ? 'Reading…' : retype ? `Read as ${KIND_IN_TEXT[retype.kind]}` : ''}
    </button>
  {/snippet}
</Dialog>

<Dialog bind:open={removeOpen} title={removing ? `Remove ${baseName(removing.path)}?` : ''}>
  {#if removing}
    <p>
      {removing.items ? `Its ${plural(removing.items, 'item')} leave the library.` : 'It has no items yet.'}
      The files on disk aren't touched.
    </p>
    <label class="forget">
      <input type="checkbox" bind:checked={forget} disabled={busy} />
      <span>
        Also forget watch progress and fixed matches
        <span class="muted">{forget ? 'Adding the folder again starts from scratch.' : 'Kept: they come back if you add the folder again.'}</span>
      </span>
    </label>
    {#if busy && waitingLong}<p class="muted wait" role="status">Waiting for the scan to finish…</p>{/if}
    {#if removeError}
      <p class="error" role="alert">
        {removeError}
        {#if retryIn}<span class="muted">Retrying in {retryIn} s…</span>{/if}
      </p>
    {/if}
  {/if}
  {#snippet footer()}
    <button type="button" class="btn" onclick={() => (removeOpen = false)}>Cancel</button>
    <button type="button" class="btn danger" disabled={busy} onclick={doRemove}>
      {busy ? 'Removing…' : forget ? 'Remove and forget' : 'Remove folder'}
    </button>
  {/snippet}
</Dialog>

<style>
  .summary {
    margin: 0;
  }
  .block {
    height: 7rem;
  }
  .card {
    padding: 1rem 1.25rem;
    background: var(--surface-raised);
    border: 1px solid var(--line);
    border-radius: var(--radius-lg);
  }
  .empty p {
    margin: 0;
  }
  .folders {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 0.6rem;
  }
  .folders li {
    display: grid;
    gap: 0.35rem;
  }
  .folders p {
    margin: 0;
    font-size: var(--fs-sm-3);
  }
  .head {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    min-width: 0;
  }
  .head :global(svg) {
    flex: none;
    width: 1.2rem;
    height: 1.2rem;
    color: var(--accent);
  }
  .path {
    min-width: 0;
    overflow-wrap: anywhere;
    font-size: var(--fs-md-2);
  }
  .tag {
    flex: none;
    margin-left: auto;
    padding: 0.1rem 0.55rem;
    font-size: var(--fs-2xs-3);
    color: var(--text-muted);
    border: 1px solid var(--line);
    border-radius: var(--radius-pill);
  }
  .ok {
    color: var(--ok);
  }
  .warn {
    color: var(--warn);
  }
  .controls {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.6rem 0.9rem;
    margin-top: 0.4rem;
  }
  .note {
    flex: 1 1 14rem;
    font-size: var(--fs-xs-3);
  }
  /* Movies | TV shows: two buttons, the folder's type pressed. */
  .seg {
    display: inline-flex;
    padding: 3px;
    background: var(--surface-room);
    border: 1px solid var(--line);
    border-radius: var(--radius-pill);
  }
  .seg button {
    min-height: 2rem;
    padding: 0.2rem 0.9rem;
    font: inherit;
    font-size: var(--fs-sm-2);
    color: var(--text-muted);
    background: none;
    border: 0;
    border-radius: var(--radius-pill);
    cursor: pointer;
  }
  .seg button[aria-pressed='true'] {
    color: var(--on-accent);
    background: var(--accent);
    font-weight: var(--fw-semibold-2);
  }
  .seg button:hover:not(:disabled):not([aria-pressed='true']) {
    color: var(--text);
  }
  .seg button:focus-visible {
    outline: 2px solid var(--text);
    outline-offset: 1px;
  }
  .seg button:disabled {
    cursor: default;
  }
  .seg button:disabled:not([aria-pressed='true']) {
    opacity: 0.45;
  }
  .remove {
    margin-left: auto;
  }
  .btn.danger {
    color: var(--on-danger);
    background: var(--danger);
    border-color: var(--danger);
  }
  .add {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.6rem 1rem;
  }
  .add p {
    margin: 0;
  }
  .forget {
    display: flex;
    gap: 0.6rem;
    align-items: flex-start;
    margin: 0.9rem 0 0;
    cursor: pointer;
  }
  .forget input {
    margin: 0.25rem 0 0;
    accent-color: var(--danger);
  }
  .forget > span {
    display: grid;
    gap: 0.15rem;
  }
  .forget .muted {
    font-size: var(--fs-xs-3);
  }
  .wait {
    margin: 0.75rem 0 0;
  }
  .error {
    margin: 0.75rem 0 0;
  }
  .error .muted {
    display: block;
    margin-top: 0.2rem;
  }
</style>
