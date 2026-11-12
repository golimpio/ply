<script lang="ts">
  import { api, type Subtitle, type SubtitleSearchResult } from '../lib/api';
  import { languageLabel } from '../lib/format';
  import { href } from '../lib/router.svelte';
  import Dialog from './Dialog.svelte';
  import Icon from './Icon.svelte';

  let {
    open = $bindable(false),
    itemId,
    title,
    isEpisode = false,
    show = undefined,
    season = undefined,
    episode = undefined,
    subtitles = $bindable([]),
    onchange = undefined,
  }: {
    open: boolean;
    itemId: number;
    title: string;
    isEpisode?: boolean;
    show?: string;
    season?: number;
    episode?: number;
    subtitles: Subtitle[];
    onchange?: (subs: Subtitle[]) => void;
  } = $props();

  /** The three sections, in order: what the item has, more from online, a file of your own. */
  const TABS = [
    { id: 'manage', label: 'Current', icon: 'cc' },
    { id: 'search', label: 'Search online', icon: 'search' },
    { id: 'upload', label: 'Upload file', icon: 'upload' },
  ] as const;
  type TabMode = (typeof TABS)[number]['id'];

  let activeTab = $state<TabMode>('manage');

  const tabId = (id: TabMode) => `subtitle-tab-${id}`;
  const panelId = (id: TabMode) => `subtitle-panel-${id}`;

  // Search state
  let searchQuery = $state('');
  let searchLang = $state('en');
  let searchResults = $state<SubtitleSearchResult[]>([]);
  let searching = $state(false);
  let searchError = $state('');
  let searchNotConfigured = $state(false);
  let downloadingId = $state<number | null>(null);

  // Upload state
  let uploadFile = $state<File | null>(null);
  let uploadLang = $state('en');
  let uploadForced = $state(false);
  let uploading = $state(false);
  let uploadError = $state('');

  // Delete state
  let deletingKey = $state<string | null>(null);
  let actionStatus = $state('');

  const defaultQuery = $derived.by(() => {
    if (isEpisode && show) {
      const s = season ?? 1;
      const e = episode ?? 1;
      return `${show} S${String(s).padStart(2, '0')}E${String(e).padStart(2, '0')}`;
    }
    return title;
  });

  $effect(() => {
    if (open) {
      activeTab = 'manage';
      searchQuery = defaultQuery;
      actionStatus = '';
      searchError = '';
      uploadError = '';
      uploadFile = null;
    }
  });

  const LANGUAGES = [
    { code: 'all', name: 'All languages' },
    { code: 'en', name: 'English' },
    { code: 'es', name: 'Spanish' },
    { code: 'fr', name: 'French' },
    { code: 'de', name: 'German' },
    { code: 'it', name: 'Italian' },
    { code: 'pt', name: 'Portuguese' },
    { code: 'pt-br', name: 'Portuguese (Brazil)' },
    { code: 'ja', name: 'Japanese' },
    { code: 'zh', name: 'Chinese' },
    { code: 'ko', name: 'Korean' },
    { code: 'ar', name: 'Arabic' },
    { code: 'ru', name: 'Russian' },
    { code: 'nl', name: 'Dutch' },
    { code: 'sv', name: 'Swedish' },
    { code: 'pl', name: 'Polish' },
  ];

  // The codecs ffprobe names and the extensions of a sidecar, as the short code on a track's tile.
  // Anything else (a container ply does not know) keeps its own name, upper-cased.
  const FORMATS: Record<string, string> = {
    srt: 'SRT',
    subrip: 'SRT',
    vtt: 'VTT',
    webvtt: 'VTT',
    ass: 'ASS',
    ssa: 'SSA',
    mov_text: 'MP4',
    text: 'TXT',
    dvd_subtitle: 'DVD',
    pgs: 'PGS',
    hdmv_pgs_subtitle: 'PGS',
    dvb_subtitle: 'DVB',
    xsub: 'XSUB',
  };

  /**
   * Selects a tab, and asks for the online results the first time that tab is opened. With `focus`
   * the tab is given the focus, as an arrow key does (the WAI-ARIA tabs pattern).
   */
  function selectTab(id: TabMode, focus = false) {
    activeTab = id;
    if (id === 'search' && searchResults.length === 0 && !searching) void performSearch();
    if (focus) document.getElementById(tabId(id))?.focus();
  }

  // Arrows walk the tabs, Home and End the first and the last, the selection following the focus.
  function onTabKeydown(e: KeyboardEvent) {
    const n = TABS.length;
    const i = Math.max(
      0,
      TABS.findIndex((t) => t.id === activeTab),
    );
    let j: number;
    switch (e.key) {
      case 'ArrowRight':
      case 'ArrowDown':
        j = (i + 1) % n;
        break;
      case 'ArrowLeft':
      case 'ArrowUp':
        j = (i - 1 + n) % n;
        break;
      case 'Home':
        j = 0;
        break;
      case 'End':
        j = n - 1;
        break;
      default:
        return;
    }
    e.preventDefault();
    selectTab(TABS[j].id, true);
  }

  async function performSearch() {
    if (!searchQuery.trim() || searching) return;
    searching = true;
    searchError = '';
    searchNotConfigured = false;
    try {
      const res = await api.searchSubtitles(itemId, {
        query: searchQuery.trim(),
        language: searchLang,
      });
      if (!res.configured) {
        searchNotConfigured = true;
        searchResults = [];
      } else {
        searchResults = res.results;
        if (searchResults.length === 0) {
          searchError = 'No subtitles found matching this query.';
        }
      }
    } catch (err: unknown) {
      searchError = err instanceof Error ? err.message : 'Search failed';
    } finally {
      searching = false;
    }
  }

  async function downloadSub(result: SubtitleSearchResult) {
    if (downloadingId !== null) return;
    downloadingId = result.fileId;
    actionStatus = '';
    try {
      const updated = await api.downloadSubtitle(itemId, {
        fileId: result.fileId,
        fileName: result.fileName || `${searchQuery}.${result.language}.${result.format || 'srt'}`,
        language: result.language,
        forced: false,
      });
      subtitles = updated;
      onchange?.(updated);
      actionStatus = `Subtitle "${result.fileName || result.language}" added!`;
      activeTab = 'manage';
    } catch (err: unknown) {
      searchError = err instanceof Error ? err.message : 'Download failed';
    } finally {
      downloadingId = null;
    }
  }

  async function handleUpload() {
    if (!uploadFile || uploading) return;
    uploading = true;
    uploadError = '';
    try {
      const updated = await api.uploadSubtitle(itemId, uploadFile, {
        language: uploadLang,
        forced: uploadForced,
      });
      subtitles = updated;
      onchange?.(updated);
      actionStatus = `Uploaded "${uploadFile.name}" successfully!`;
      uploadFile = null;
      activeTab = 'manage';
    } catch (err: unknown) {
      uploadError = err instanceof Error ? err.message : 'Upload failed';
    } finally {
      uploading = false;
    }
  }

  async function deleteSub(key: string) {
    if (deletingKey !== null) return;
    deletingKey = key;
    actionStatus = '';
    try {
      await api.deleteSubtitle(itemId, key);
      const updated = subtitles.filter((s) => s.key !== key);
      subtitles = updated;
      onchange?.(updated);
      actionStatus = 'Subtitle file removed.';
    } catch (err: unknown) {
      actionStatus = err instanceof Error ? err.message : 'Failed to delete subtitle';
    } finally {
      deletingKey = null;
    }
  }

  function onFileSelected(e: Event) {
    const input = e.target as HTMLInputElement;
    if (input.files && input.files.length > 0) {
      uploadFile = input.files[0];
    }
  }

  // A file dropped on the zone is the same as one chosen in the picker: it is a File either way, and
  // the API takes it whole, so the hidden input stays untouched.
  function onDragOver(e: DragEvent) {
    e.preventDefault(); // a drop is only allowed over an element that says so
  }

  function onDrop(e: DragEvent) {
    e.preventDefault();
    const file = e.dataTransfer?.files?.[0];
    if (file) uploadFile = file;
  }

  /** The language's own name, from its tag (and "Unknown" when the file does not say). */
  function langName(lang?: string): string {
    return languageLabel(lang) || 'Unknown';
  }

  /** The short code of a track's format: what a viewer picks between. */
  function formatCode(format: string): string {
    return FORMATS[format.toLowerCase()] ?? format.toUpperCase();
  }

  /** Under a track's name: which track it is, or which sidecar file, and any title that adds to it. */
  function trackMeta(sub: Subtitle): string {
    if (sub.external) return sub.path ?? 'Sidecar file';
    // ffprobe's title is often the language over again, or the bare stream index: neither says more.
    const title = (sub.title ?? '').trim();
    const repeats = [langName(sub.language).toLowerCase(), (sub.language ?? '').toLowerCase()];
    const extra = title && title !== sub.key && !repeats.includes(title.toLowerCase()) ? title : '';
    return [extra, `Track ${sub.key}`].filter(Boolean).join(' · ');
  }

  /** Under a result's name: its language, and how many people downloaded it. */
  function resultMeta(item: SubtitleSearchResult): string {
    const downloads = item.downloadCount ? `${item.downloadCount.toLocaleString()} downloads` : '';
    return [langName(item.language), downloads].filter(Boolean).join(' · ');
  }
</script>

<Dialog bind:open title="Closed Captions & Subtitles" wide>
  <div class="sub-manager">
    <!-- The WAI-ARIA tabs pattern, as Settings and a show's seasons have it: one Tab stop, arrows to
         walk the tabs, the selection following the focus, and only the open panel mounted. -->
    <!-- svelte-ignore a11y_interactive_supports_focus -->
    <div class="tabs" role="tablist" aria-label="Subtitle sections" onkeydown={onTabKeydown}>
      {#each TABS as t (t.id)}
        {@const on = t.id === activeTab}
        <button
          type="button"
          class="chip"
          role="tab"
          id={tabId(t.id)}
          aria-selected={on}
          aria-controls={on ? panelId(t.id) : undefined}
          tabindex={on ? 0 : -1}
          onclick={() => selectTab(t.id)}
        >
          <Icon name={t.icon} />
          {t.label}
          {#if t.id === 'manage'}
            <span class="tab-count">{subtitles.length}</span>
          {/if}
        </button>
      {/each}
    </div>

    {#if actionStatus}
      <p class="notice ok" role="status">
        <Icon name="check" />
        <span>{actionStatus}</span>
      </p>
    {/if}

    <!-- Tab 1: Current Subtitles -->
    {#if activeTab === 'manage'}
      <div class="tab-panel" id={panelId('manage')} role="tabpanel" aria-labelledby={tabId('manage')}>
        {#if subtitles.length === 0}
          <div class="empty">
            <span class="empty-icon"><Icon name="cc" /></span>
            <p class="empty-lead">No subtitles for this item</p>
            <p class="empty-hint">Search online for one, or add a file of your own.</p>
            <div class="empty-actions">
              <button class="btn pill primary" onclick={() => selectTab('search')}>
                <Icon name="search" />Search online
              </button>
              <button class="btn pill subtle" onclick={() => selectTab('upload')}>
                <Icon name="upload" />Upload a file
              </button>
            </div>
          </div>
        {:else}
          <ul class="row-list" aria-label="Subtitle tracks">
            {#each subtitles as sub (sub.key)}
              <li class="row">
                <span class="lang-tile" aria-hidden="true">{formatCode(sub.format)}</span>
                <div class="row-body">
                  <span class="row-name">
                    <span class="row-title">{langName(sub.language)}</span>
                    {#if sub.image}<span class="badge">Burned in</span>{/if}
                    {#if sub.forced}<span class="badge">Forced</span>{/if}
                    {#if sub.default}<span class="badge">Default</span>{/if}
                  </span>
                  <span class="row-meta">{trackMeta(sub)}</span>
                </div>
                {#if sub.external}
                  <button
                    class="btn pill subtle remove"
                    disabled={deletingKey === sub.key}
                    onclick={() => deleteSub(sub.key)}
                    aria-label={`Remove the ${sub.path ?? langName(sub.language)} subtitle`}
                  >
                    <Icon name="trash" />
                    {deletingKey === sub.key ? 'Removing…' : 'Remove'}
                  </button>
                {/if}
              </li>
            {/each}
          </ul>
        {/if}
      </div>
    {/if}

    <!-- Tab 2: Search Online -->
    {#if activeTab === 'search'}
      <div class="tab-panel" id={panelId('search')} role="tabpanel" aria-labelledby={tabId('search')}>
        <form
          class="search-bar"
          onsubmit={(e) => {
            e.preventDefault();
            void performSearch();
          }}
        >
          <div class="settings-input search-field">
            <Icon name="search" />
            <input
              type="text"
              bind:value={searchQuery}
              placeholder="Search a film or an episode…"
              aria-label="Search online subtitles"
              disabled={searching}
            />
          </div>

          <select
            class="settings-input lang-select"
            bind:value={searchLang}
            disabled={searching}
            aria-label="Subtitle language"
          >
            {#each LANGUAGES as l (l.code)}
              <option value={l.code}>{l.name}</option>
            {/each}
          </select>

          <button class="btn pill primary" type="submit" disabled={searching || !searchQuery.trim()}>
            {searching ? 'Searching…' : 'Search'}
          </button>
        </form>

        {#if searchNotConfigured}
          <div class="notice" role="note">
            <Icon name="alert" />
            <div class="notice-text">
              <strong>An OpenSubtitles key is needed for online search</strong>
              <p>
                Search and one-click download need a free API key. A subtitle file of your own always
                goes in under Upload file.
              </p>
              <a class="btn pill" href={href.settingsTab('keys')} onclick={() => (open = false)}>
                Open Settings &rarr; API keys
              </a>
            </div>
          </div>
        {:else if searchError}
          <p class="error" role="alert">{searchError}</p>
        {:else if searching}
          <p class="muted" aria-busy="true">Searching subtitles online…</p>
        {:else if searchResults.length > 0}
          <ul class="row-list results" aria-label="Subtitles found online">
            {#each searchResults as item (item.id)}
              <li class="row">
                <span class="lang-tile" aria-hidden="true">{formatCode(item.format || 'srt')}</span>
                <div class="row-body">
                  <span class="row-name">
                    <span class="row-title">{item.release || item.fileName}</span>
                    {#if item.hearingImpaired}<span class="badge">SDH</span>{/if}
                  </span>
                  <span class="row-meta">{resultMeta(item)}</span>
                </div>
                <button
                  class="btn pill subtle"
                  disabled={downloadingId === item.fileId}
                  onclick={() => downloadSub(item)}
                >
                  <Icon name="download" />
                  {downloadingId === item.fileId ? 'Downloading…' : 'Download & Attach'}
                </button>
              </li>
            {/each}
          </ul>
        {/if}
      </div>
    {/if}

    <!-- Tab 3: Upload File -->
    {#if activeTab === 'upload'}
      <div class="tab-panel" id={panelId('upload')} role="tabpanel" aria-labelledby={tabId('upload')}>
        <label class="drop" class:has-file={!!uploadFile} ondragover={onDragOver} ondrop={onDrop}>
          <input type="file" accept=".srt,.vtt,.ass" class="file-input" onchange={onFileSelected} />
          <Icon name="upload" />
          {#if uploadFile}
            <span class="drop-name">{uploadFile.name}</span>
            <span class="muted small">{(uploadFile.size / 1024).toFixed(1)} KB</span>
          {:else}
            <span class="drop-name">Choose a subtitle file</span>
            <span class="muted small">Click to browse, or drop an .srt, .vtt or .ass here</span>
          {/if}
        </label>

        <div class="upload-options">
          <div class="field">
            <label class="field-label" for="upload-lang">Language</label>
            <select id="upload-lang" class="settings-input" bind:value={uploadLang}>
              {#each LANGUAGES.filter((l) => l.code !== 'all') as l (l.code)}
                <option value={l.code}>{l.name}</option>
              {/each}
            </select>
          </div>

          <label class="checkbox">
            <input type="checkbox" bind:checked={uploadForced} />
            <span>Forced track: foreign dialogue only</span>
          </label>
        </div>

        {#if uploadError}
          <p class="error" role="alert">{uploadError}</p>
        {/if}

        <div class="upload-actions">
          <button class="btn pill primary" disabled={!uploadFile || uploading} onclick={handleUpload}>
            {uploading ? 'Attaching…' : 'Attach to this item'}
          </button>
        </div>
      </div>
    {/if}
  </div>
</Dialog>

<style>
  .sub-manager {
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }

  /* Tabs: the shared .chip pill, in a row above the line that divides them from the panel. */
  .tabs {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
    padding-bottom: 0.6rem;
    border-bottom: 1px solid var(--line-quiet);
  }
  .tab-count {
    font-size: var(--fs-2xs);
    font-variant-numeric: tabular-nums;
    opacity: 0.7;
  }

  .tab-panel {
    display: flex;
    flex-direction: column;
    gap: 0.6rem;
    /* Room for the empty state or one track, so switching tabs does not make the dialog jump. */
    min-height: 11rem;
  }

  /* What the last action did, or what a section needs before it can work. */
  .notice {
    display: flex;
    align-items: flex-start;
    gap: 0.6rem;
    padding: 0.75rem 0.85rem;
    font-size: var(--fs-sm-2);
    color: var(--text-muted);
    background: var(--surface-room);
    border: 1px solid var(--line);
    border-radius: var(--radius-row);
  }
  .notice > :global(svg) {
    width: 1.15rem;
    height: 1.15rem;
    flex: none;
    margin-top: 0.25rem;
  }
  .notice.ok {
    color: var(--ok);
  }
  .notice-text {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 0.4rem;
  }
  .notice-text p {
    margin: 0;
  }
  .notice-text strong {
    color: var(--text);
  }

  /* One card a track: what it is on the left, what can be done with it on the right. */
  .row-list {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .results {
    max-height: 20rem;
    overflow-y: auto;
    /* Room for the focus ring, which the scroll box would clip. */
    margin: calc(-1 * var(--focus-offset) - 2px);
    padding: calc(var(--focus-offset) + 2px);
  }
  .row {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 0.75rem 0.85rem;
    background: var(--surface-panel);
    border: 1px solid var(--line-quiet);
    border-radius: var(--radius-row);
  }

  /* The track's language as a tag, so the column of them can be scanned. */
  .lang-tile {
    display: flex;
    align-items: center;
    justify-content: center;
    flex: none;
    min-width: 3rem;
    min-height: 2rem;
    padding: 0 0.4rem;
    font-size: var(--fs-2xs);
    font-weight: var(--fw-bold);
    letter-spacing: var(--tracking-wider);
    font-variant-numeric: tabular-nums;
    color: var(--text-muted);
    background: var(--surface-room);
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
  }

  .row-body {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    flex: 1 1 auto;
    min-width: 0;
  }
  .row-name {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 0.4rem;
    min-width: 0;
    font-size: var(--fs-md);
    font-weight: var(--fw-semibold-2);
    color: var(--text);
  }
  .row-title {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .row-meta {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: var(--fs-xs-2);
    color: var(--text-muted);
  }

  /* Nothing yet: a well, with the two ways to fill it. */
  .empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    flex: 1 1 auto;
    gap: 0.4rem;
    padding: 1.25rem 1rem;
    text-align: center;
    background: var(--surface-room);
    border: 1px dashed var(--line);
    border-radius: var(--radius-row);
  }
  .empty-icon :global(svg) {
    width: 1.75rem;
    height: 1.75rem;
    color: var(--text-dim);
  }
  .empty-lead {
    margin: 0;
    font-size: var(--fs-lg);
    font-weight: var(--fw-semibold-2);
    color: var(--text);
  }
  .empty-hint {
    margin: 0;
    font-size: var(--fs-sm);
    color: var(--text-muted);
  }
  .empty-actions {
    display: flex;
    flex-wrap: wrap;
    justify-content: center;
    gap: 0.5rem;
    margin-top: 0.5rem;
  }

  /* Search: the shared .settings-input as the pill that holds the icon and the field. */
  .search-bar {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.5rem;
  }
  .search-field {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex: 1 1 12rem;
    min-width: 0;
  }
  .search-field:focus-within {
    border-color: var(--text);
  }
  .search-field > :global(svg) {
    width: 1rem;
    height: 1rem;
    flex: none;
    color: var(--text-muted);
  }
  .search-field input {
    flex: 1 1 auto;
    min-width: 0;
    padding: 0;
    font: inherit;
    color: var(--text);
    background: none;
    border: 0;
  }
  .search-field input::placeholder {
    color: var(--text-dim);
  }
  .lang-select {
    flex: 0 1 auto;
    min-width: 0;
  }

  /* Upload: a well to drop a file on, then the two choices the request needs. */
  .drop {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 0.4rem;
    padding: 1.5rem 1rem;
    text-align: center;
    background: var(--surface-room);
    border: 1px dashed var(--line);
    border-radius: var(--radius-row);
    cursor: pointer;
    transition:
      border-color var(--motion-hover) var(--ease),
      background var(--motion-hover) var(--ease);
  }
  .drop:hover,
  .drop.has-file {
    background: var(--surface-panel);
    border-color: var(--line-hover);
  }
  .drop > :global(svg) {
    width: 1.5rem;
    height: 1.5rem;
    color: var(--text-muted);
  }
  .drop-name {
    font-size: var(--fs-sm);
    font-weight: var(--fw-semibold-2);
    color: var(--text);
    overflow-wrap: anywhere;
  }
  .file-input {
    display: none;
  }
  .upload-options {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 0.4rem;
    max-width: 18rem;
  }
  .field-label {
    font-size: var(--fs-xs-2);
    color: var(--text-muted);
  }
  .checkbox {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    font-size: var(--fs-sm);
    color: var(--text);
    cursor: pointer;
  }
  .upload-actions {
    display: flex;
    justify-content: flex-end;
  }
  .small {
    font-size: var(--fs-xs);
  }

  /* A sidecar's Remove stays in the danger colour under the pointer too: .btn.pill.subtle is more
     specific than a bare class, hence the full selector. */
  .btn.pill.subtle.remove {
    color: var(--danger);
  }
  .btn.pill.subtle.remove:hover:not(:disabled) {
    color: var(--danger);
    border-color: var(--danger);
  }

  @media (max-width: 600px) {
    .search-field {
      flex: 1 1 100%;
    }
    .lang-select {
      flex: 1 1 auto;
    }
    /* A result's Download is too wide to sit beside its name on a phone: it takes its own line,
       while a track's row keeps its tile beside the name and wraps only the badges. */
    .row {
      align-items: flex-start;
    }
    .results .row {
      flex-wrap: wrap;
    }
    .results .row .btn {
      width: 100%;
    }
  }
</style>
