<script lang="ts">
  import { tick } from 'svelte';
  import { api, type Settings, type SettingsUpdate } from '../lib/api';
  import { app } from '../lib/state.svelte';
  import ConfirmRemove from './ConfirmRemove.svelte';
  import KeyField from './KeyField.svelte';

  let { settings: s }: { settings: Settings } = $props();

  let key = $state('');
  let saving = $state(false);
  let keyMessage = $state<{ ok: boolean; text: string } | null>(null);
  let keyMessageEl = $state<HTMLElement>();

  async function saveKey(update: SettingsUpdate, done: string) {
    saving = true;
    keyMessage = null;
    try {
      const r = await api.saveSettings(update);
      keyMessage = { ok: true, text: done };
      key = '';
      s.opensubtitlesKeySet = r.opensubtitlesKeySet;
      app.libraryVersion++;
    } catch (e) {
      const why = e instanceof Error ? e.message : String(e);
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
    if (k) void saveKey({ opensubtitlesKey: k }, 'OpenSubtitles key saved. You can now search for subtitles online.');
  }
</script>

<div class="settings-panel card key-card opensubtitles" role="group" aria-labelledby="opensubtitles-h">
  <div>
    <h3 id="opensubtitles-h">OpenSubtitles key</h3>
    <p class="muted">
      Allows searching and downloading subtitle files directly inside movie and show details.
      {#if s.opensubtitlesKeySet}
        A consumer API key is set. What you enter replaces it.
      {:else}
        No key set yet. Register a free account and generate an API key in your account dashboard at
        <a class="link" href="https://www.opensubtitles.com/users/edit" target="_blank" rel="noreferrer">OpenSubtitles.com</a>.
      {/if}
    </p>
  </div>

  <form class="key-fields" onsubmit={submit}>
    <div class="key-field">
      <label for="opensubtitles-key">{s.opensubtitlesKeySet ? 'Replace the key' : 'Key'}</label>
      <KeyField id="opensubtitles-key" set={!!s.opensubtitlesKeySet} bind:value={key} unsetPlaceholder="Paste your OpenSubtitles API key" />
    </div>

    <div class="key-buttons">
      <button class="btn primary" disabled={saving || !key.trim()}>
        {saving ? 'Saving…' : 'Save key'}
      </button>
      {#if s.opensubtitlesKeySet}
        <ConfirmRemove
          label="Remove key"
          question="Remove the OpenSubtitles key?"
          disabled={saving}
          onconfirm={() => saveKey({ opensubtitlesKey: '' }, 'OpenSubtitles key removed.')}
        />
      {/if}
    </div>
  </form>

  {#if keyMessage}
    <p
      bind:this={keyMessageEl}
      class="key-message"
      class:error={!keyMessage.ok}
      class:ok={keyMessage.ok}
      role="status"
      tabindex="-1"
    >
      {keyMessage.text}
    </p>
  {/if}
</div>

<style>
  .key-fields {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }
  .key-field {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }
  .key-buttons {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
  }
  .key-message {
    font-size: var(--fs-sm);
    margin: 0;
  }
  .key-message.ok {
    color: var(--ok);
  }
  .key-message.error {
    color: var(--danger);
  }
</style>
