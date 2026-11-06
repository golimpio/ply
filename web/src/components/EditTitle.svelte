<script lang="ts">
  import { untrack } from 'svelte';
  import { api, type TitleDetail, type TitlePatch } from '../lib/api';
  import { app } from '../lib/state.svelte';
  import Dialog from './Dialog.svelte';

  // Edit a title's text. The title is then locked: refreshing its metadata
  // keeps these words (pictures, cast and crew still refresh).
  let { open = $bindable(false), meta }: { open: boolean; meta: TitleDetail } = $props();

  let form = $state({ title: '', sortTitle: '', year: '', tagline: '', overview: '' });
  let busy = $state(false);
  let error = $state('');

  // Filled from the title when the dialog opens, and only then: a library
  // refresh while it is open must not wipe what is being typed.
  $effect(() => {
    if (!open) return;
    untrack(() => {
      form = {
        title: meta.title,
        sortTitle: meta.sortTitle && meta.sortTitle !== meta.title ? meta.sortTitle : '',
        year: meta.year ? String(meta.year) : '',
        tagline: meta.tagline ?? '',
        overview: meta.overview ?? '',
      };
      error = '';
    });
  });

  async function save(e: Event) {
    e.preventDefault();
    const patch: TitlePatch = {};
    const y = form.year.trim() === '' ? 0 : Number(form.year);
    if (!form.title.trim()) {
      error = 'A title is needed.';
      return;
    }
    if (!Number.isInteger(y) || y < 0 || (y > 0 && (y < 1870 || y > 2200))) {
      error = 'The year should look like 1999, or be left empty.';
      return;
    }
    if (form.title.trim() !== meta.title) patch.title = form.title.trim();
    if (form.sortTitle.trim() !== (meta.sortTitle !== meta.title ? meta.sortTitle : '')) patch.sortTitle = form.sortTitle.trim();
    if (y !== (meta.year || 0)) patch.year = y;
    if (form.tagline.trim() !== (meta.tagline ?? '')) patch.tagline = form.tagline.trim();
    if (form.overview.trim() !== (meta.overview ?? '')) patch.overview = form.overview.trim();
    if (!Object.keys(patch).length) {
      open = false;
      return;
    }
    busy = true;
    error = '';
    try {
      await api.patchTitle(meta.id, patch);
      app.libraryVersion++;
      open = false;
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      busy = false;
    }
  }
</script>

<Dialog bind:open title="Edit details">
  <form id="edit-title" class="fields" onsubmit={save}>
    <label>
      <span>Title</span>
      <input class="text-field" bind:value={form.title} required />
    </label>
    <div class="pair">
      <label>
        <span>Sort as</span>
        <input class="text-field" bind:value={form.sortTitle} placeholder={meta.title} />
      </label>
      <label>
        <span>Year</span>
        <input class="text-field" bind:value={form.year} inputmode="numeric" maxlength="4" />
      </label>
    </div>
    <label>
      <span>Tagline</span>
      <input class="text-field" bind:value={form.tagline} />
    </label>
    <label>
      <span>Overview</span>
      <textarea class="text-field" bind:value={form.overview} rows="6"></textarea>
    </label>
    <p class="muted note">Refreshing the metadata keeps what you write here.</p>
    {#if error}<p class="error" role="alert">{error}</p>{/if}
  </form>

  {#snippet footer()}
    <button class="btn" onclick={() => (open = false)}>Cancel</button>
    <button class="btn primary" form="edit-title" disabled={busy}>Save</button>
  {/snippet}
</Dialog>

<style>
  .fields {
    display: grid;
    gap: 0.9rem;
  }
  .pair {
    display: grid;
    grid-template-columns: 1fr 6rem;
    gap: 0.75rem;
  }
  label {
    display: grid;
    gap: 0.3rem;
  }
  label span {
    font-size: var(--fs-xs-3);
    color: var(--text-muted);
  }
  input,
  textarea {
    width: 100%;
    min-height: auto;
    padding: 0.55rem 0.8rem;
    resize: vertical;
  }
  .note,
  .error {
    margin: 0;
    font-size: var(--fs-sm);
  }
</style>
