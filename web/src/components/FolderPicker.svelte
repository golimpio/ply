<script lang="ts">
  import { tick } from 'svelte';
  import { api, type DirEntry, type DirListing, type FolderKind, type LibraryFolder } from '../lib/api';
  import Dialog from './Dialog.svelte';
  import Icon from './Icon.svelte';

  // Picks a folder on the server's disks (GET /api/fs) and its type, and adds it
  // (POST /api/folders). The folder added is the one listed: the last listing
  // that loaded. `onadded` gets the new folder.
  let { open = $bindable(false), onadded }: { open: boolean; onadded: (f: LibraryFolder) => void } = $props();

  let listing = $state<DirListing | null>(null);
  let loading = $state(false);
  let browseError = $state('');
  let typed = $state('');
  let kind = $state<FolderKind | ''>('');
  let adding = $state(false);
  let addError = $state('');
  let listEl = $state<HTMLElement>();
  let pathEl = $state<HTMLInputElement>();

  // Only the answer to the latest request counts: a slow listing of a folder
  // left a moment ago must not replace the one being looked at, nor its error.
  let latest = 0;
  async function go(path?: string, focusList = true) {
    const ticket = ++latest;
    loading = true;
    browseError = '';
    addError = '';
    try {
      const d = await api.listDirs(path);
      if (ticket !== latest) return;
      listing = d;
      typed = d.path;
      // Keyboard users land in the new list, not on the page behind the dialog.
      if (!focusList) return;
      await tick();
      (listEl?.querySelector<HTMLElement>('.dir') ?? listEl)?.focus();
    } catch (e) {
      if (ticket !== latest) return;
      browseError = e instanceof Error ? e.message : String(e);
    } finally {
      if (ticket === latest) loading = false;
    }
  }

  // Each opening starts from the places, with no type chosen: the type is a
  // decision about the folder, so it is never assumed.
  let wasOpen = false;
  $effect(() => {
    if (open && !wasOpen) {
      listing = null;
      typed = '';
      kind = '';
      addError = '';
      browseError = '';
      void go(undefined, false); // the path box has the focus on opening
      void tick().then(() => pathEl?.focus());
    }
    wasOpen = open;
  });

  const atPlaces = $derived(!!listing && !listing.path);
  const entries = $derived<DirEntry[]>(listing ? (atPlaces ? (listing.places ?? []) : listing.dirs) : []);
  const chosen = $derived(listing?.path ?? '');
  const baseName = (p: string) => p.split('/').filter(Boolean).pop() ?? p;
  const said = $derived(
    !listing || loading ? '' : atPlaces ? `${entries.length} places` : `${entries.length} ${entries.length === 1 ? 'folder' : 'folders'} in ${listing.path}`,
  );

  function submitTyped(e: Event) {
    e.preventDefault();
    const p = typed.trim();
    if (p) void go(p);
  }

  async function add() {
    if (!chosen || !kind || adding) return;
    const path = chosen;
    adding = true;
    addError = '';
    try {
      const f = await api.addFolder(path, kind);
      open = false;
      onadded(f);
    } catch (e) {
      addError = e instanceof Error ? e.message : String(e);
    } finally {
      adding = false;
    }
  }
</script>

<Dialog bind:open title="Add a folder" wide>
  <form class="where" onsubmit={submitTyped}>
    <label class="sr-only" for="folder-path">Folder path</label>
    <input
      id="folder-path"
      bind:this={pathEl}
      bind:value={typed}
      placeholder="/Volumes/Media/Movies"
      autocomplete="off"
      spellcheck="false"
    />
    <button class="btn" disabled={!typed.trim()}>Go</button>
  </form>

  <nav class="crumbs" aria-label="Folder navigation">
    <!-- Nothing here waits for a listing: a slow share must not trap the picker (go() keeps the latest). -->
    <button type="button" class="btn small" disabled={atPlaces} onclick={() => go()}>Places</button>
    <button
      type="button"
      class="btn small"
      disabled={!listing?.parent}
      onclick={() => listing?.parent && go(listing.parent)}
    ><Icon name="up" /> Up</button>
  </nav>

  <p class="sr-only" role="status">{said}</p>

  <div class="list" aria-busy={loading} bind:this={listEl} tabindex="-1">
    {#if browseError}
      <p class="error" role="alert">
        {browseError}
        {#if chosen}<span class="still">Still in <code>{chosen}</code>.</span>{/if}
      </p>
    {/if}
    {#if !listing && loading}
      <div class="skeleton rows"></div>
    {:else if listing}
      {#if !entries.length}
        <p class="muted empty">{atPlaces ? 'Nothing to start from. Type a path above.' : 'No folders in here.'}</p>
      {:else}
        <ul aria-label={atPlaces ? 'Places' : `Folders in ${listing.path}`}>
          {#each entries as d (d.path)}
            <li>
              <button type="button" class="dir" onclick={() => go(d.path)} title={d.path}>
                <Icon name="folder" />
                <span>{d.name}</span>
                {#if atPlaces && d.name !== baseName(d.path)}<span class="muted where-is">{d.path}</span>{/if}
              </button>
            </li>
          {/each}
        </ul>
      {/if}
    {/if}
  </div>

  {#if chosen}
    <fieldset class="kind">
      <legend>What's in <code>{chosen}</code>?</legend>
      <label>
        <input type="radio" name="folder-kind" value="movies" bind:group={kind} />
        <span><strong>Movies</strong><span class="muted">Every file is a film.</span></span>
      </label>
      <label>
        <input type="radio" name="folder-kind" value="shows" bind:group={kind} />
        <span><strong>TV shows</strong><span class="muted">One folder per show, seasons inside it.</span></span>
      </label>
    </fieldset>
  {/if}

  {#if addError}<p class="error" role="alert">{addError}</p>{/if}

  {#snippet footer()}
    <button type="button" class="btn" onclick={() => (open = false)}>Cancel</button>
    <button type="button" class="btn primary" disabled={!chosen || !kind || adding || loading} onclick={add}>
      {adding ? 'Adding…' : chosen ? 'Add this folder' : 'Open a folder to add it'}
    </button>
  {/snippet}
</Dialog>

<style>
  .where {
    display: flex;
    gap: 0.5rem;
  }
  .where input {
    flex: 1;
    min-width: 0;
    min-height: 2.5rem;
    padding: 0.45rem 0.85rem;
    font: inherit;
    font-size: 0.92rem;
    color: var(--text);
    background: var(--bg);
    border: 1px solid var(--line);
    border-radius: var(--radius);
  }
  .where input:focus-visible {
    border-color: var(--text);
    outline: none;
  }
  .crumbs {
    display: flex;
    gap: 0.4rem;
    margin: 0.75rem 0 0.5rem;
  }
  .btn.small {
    min-height: 2rem;
    padding: 0.25rem 0.7rem;
    font-size: 0.85rem;
  }
  .btn.small :global(svg) {
    width: 1rem;
    height: 1rem;
  }
  .list {
    height: min(18rem, 40dvh);
    overflow-y: auto;
    border: 1px solid var(--line);
    border-radius: var(--radius);
    background: rgb(0 0 0 / 0.18);
  }
  .list:focus-visible {
    outline: 2px solid var(--text);
    outline-offset: 2px;
  }
  .list[aria-busy='true'] ul {
    opacity: 0.55;
    transition: opacity 0.2s 0.15s; /* only a slow listing dims */
  }
  .list ul {
    list-style: none;
    margin: 0;
    padding: 0.3rem;
  }
  .list p {
    margin: 0;
    padding: 0.9rem 1rem;
  }
  .still {
    display: block;
    margin-top: 0.25rem;
    color: var(--muted);
    overflow-wrap: anywhere;
  }
  .rows {
    height: 100%;
  }
  .dir {
    display: flex;
    align-items: center;
    gap: 0.65rem;
    width: 100%;
    min-height: 2.4rem;
    padding: 0.35rem 0.7rem;
    font: inherit;
    font-size: 0.95rem;
    text-align: left;
    color: var(--text);
    background: none;
    border: 0;
    border-radius: 8px;
    cursor: pointer;
  }
  .dir :global(svg) {
    flex: none;
    width: 1.15rem;
    height: 1.15rem;
    color: var(--accent);
  }
  .dir span {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .dir .where-is {
    margin-left: auto;
    font-size: 0.8rem;
  }
  .dir:hover,
  .dir:focus-visible {
    background: var(--bg-3);
    outline: none;
  }
  .kind {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 0.6rem;
    margin: 1rem 0 0;
    padding: 0;
    border: 0;
  }
  .kind legend {
    margin-bottom: 0.5rem;
    padding: 0;
    overflow-wrap: anywhere;
  }
  .kind label {
    display: flex;
    gap: 0.6rem;
    align-items: flex-start;
    padding: 0.7rem 0.8rem;
    border: 1px solid var(--line);
    border-radius: var(--radius);
    cursor: pointer;
  }
  .kind label:has(input:checked) {
    border-color: var(--accent);
    background: rgb(240 182 78 / 0.08);
  }
  .kind label:has(input:focus-visible) {
    outline: 2px solid var(--text);
    outline-offset: 2px;
  }
  .kind input {
    margin: 0.2rem 0 0;
    accent-color: var(--accent);
  }
  .kind label > span {
    display: grid;
    gap: 0.15rem;
  }
  .kind .muted {
    font-size: 0.85rem;
  }
  .error {
    margin: 0.75rem 0 0;
  }
  @media (max-width: 600px) {
    .kind {
      grid-template-columns: 1fr;
    }
  }
</style>
