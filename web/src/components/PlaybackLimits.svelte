<script lang="ts">
  import { onMount } from 'svelte';
  import { api, ApiError, type Settings } from '../lib/api';

  // The Playback card of Settings: how many videos may play at once, and how many of them ply may
  // convert. Each saves on change; one set on the command line is shown locked.
  let { settings }: { settings: Settings } = $props();

  type Field = 'maxStreams' | 'maxTranscodes';
  interface Row {
    field: Field;
    source: 'maxStreamsSource' | 'maxTranscodesSource';
    id: string;
    label: string;
    help: string;
    /** The server's ceiling; it is what refuses a number above it (400). */
    max: number;
    saved: (n: number) => string;
  }
  const ROWS: Row[] = [
    {
      field: 'maxStreams',
      source: 'maxStreamsSource',
      id: 'max-streams',
      label: 'Videos playing at once',
      help: 'Every play counts, including videos sent to the device as they are. Past this, a new one waits until another stops.',
      max: 32,
      saved: (n) => `Saved: up to ${n} ${n === 1 ? 'video' : 'videos'} at once.`,
    },
    {
      field: 'maxTranscodes',
      source: 'maxTranscodesSource',
      id: 'max-transcodes',
      label: 'Conversions at once',
      help: "Videos ply converts on the CPU or GPU for a device that can't play the file as it is. Each one is heavy work for this computer.",
      max: 16,
      saved: (n) => `Saved: up to ${n} ${n === 1 ? 'conversion' : 'conversions'} at once.`,
    },
  ];

  /** What the server said last of each field (after a save of that field), over the page's copy. */
  let latest = $state<{ [K in Field]?: number }>({});
  let locked = $state<{ [K in Field]?: boolean }>({});
  /** What the last change of each field came to: saved, refused, or found locked (a 409). */
  let notes = $state<{ [K in Field]?: { kind: 'ok' | 'error' | 'locked'; text: string } }>({});
  /** The number each field was last changed to, and whether a save of that field is on its way. */
  const wanted: { [K in Field]?: number } = {};
  const sending: { [K in Field]?: boolean } = {};
  /** The field holds an entry that is not a number to save: an answer to an earlier save leaves it (and its error) alone. */
  const invalid: { [K in Field]?: boolean } = {};

  const value = (r: Row) => latest[r.field] ?? settings[r.field];
  const isLocked = (r: Row) => locked[r.field] || settings[r.source] === 'flag';

  async function save(r: Row, input: HTMLInputElement) {
    const n = input.valueAsNumber;
    if (!Number.isInteger(n)) {
      invalid[r.field] = true;
      notes[r.field] = { kind: 'error', text: `Enter a whole number from 1 to ${r.max}.` };
      return;
    }
    invalid[r.field] = false;
    wanted[r.field] = n;
    notes[r.field] = undefined;
    if (sending[r.field]) return; // the save on its way sends this number once it is answered
    sending[r.field] = true;
    // One save of a field at a time, the latest number last: answers cannot cross on the way, and
    // what the server keeps is what was asked for last.
    try {
      for (;;) {
        const asked = wanted[r.field]!;
        try {
          const s = await api.saveSettings({ [r.field]: asked });
          if (wanted[r.field] !== asked) continue; // changed meanwhile: send the new number
          if (invalid[r.field]) return; // a newer entry that can't be sent: its error stands
          // This field only: the other may have a change of its own on its way.
          latest[r.field] = s[r.field] ?? asked;
          notes[r.field] = { kind: 'ok', text: r.saved(latest[r.field]!) };
        } catch (e) {
          if (wanted[r.field] !== asked) continue;
          if (e instanceof ApiError && e.status === 409) locked[r.field] = true; // locked whatever the field holds
          if (invalid[r.field]) return;
          if (e instanceof ApiError && e.status === 409) {
            input.value = String(value(r) ?? '');
            notes[r.field] = { kind: 'locked', text: "Set when ply was started, so it can't be changed here." };
          } else if (e instanceof ApiError && e.status === 400) {
            notes[r.field] = { kind: 'error', text: `Choose a number from 1 to ${r.max}.` };
          } else {
            notes[r.field] = { kind: 'error', text: e instanceof Error ? e.message : String(e) };
          }
        }
        return;
      }
    } finally {
      sending[r.field] = false;
    }
  }

  // Settings links here from the player's "too many videos" panel (#/settings?s=playback).
  let card = $state<HTMLElement>();
  onMount(() => {
    if (/[?&]s=playback\b/.test(location.hash)) card?.scrollIntoView({ block: 'center' });
  });
</script>

<div class="card" bind:this={card}>
  {#each ROWS as r (r.field)}
    {@const lock = isLocked(r)}
    {@const note = notes[r.field]}
    <div class="row" data-limit={r.field}>
      <div>
        <h3><label for={r.id}>{r.label}</label></h3>
        <p class="muted" id={`${r.id}-help`}>{r.help}</p>
        {#if lock && note?.kind !== 'locked'}<p class="muted lock">Set when ply was started.</p>{/if}
      </div>
      <input
        id={r.id}
        type="number"
        inputmode="numeric"
        min="1"
        max={r.max}
        step="1"
        value={value(r)}
        disabled={lock}
        aria-describedby={`${r.id}-help`}
        aria-invalid={note?.kind === 'error' ? 'true' : undefined}
        onchange={(e) => save(r, e.currentTarget)}
      />
    </div>
    {#if note}
      <p class:error={note.kind === 'error'} class:ok={note.kind === 'ok'} class:muted={note.kind === 'locked'} role="status">{note.text}</p>
    {/if}
  {/each}
</div>

<style>
  .card {
    display: grid;
    gap: 1.1rem;
    padding: 1.1rem 1.25rem;
    background: var(--bg-2);
    border: 1px solid var(--line);
    border-radius: var(--radius-lg);
  }
  .card p {
    margin: 0;
  }
  h3 {
    margin: 0 0 0.15rem;
    font-size: 1rem;
  }
  h3 label {
    cursor: pointer;
  }
  .row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem 2rem;
  }
  .row + .row,
  [role='status'] + .row {
    padding-top: 1.1rem;
    border-top: 1px solid var(--line);
  }
  .card .lock {
    margin-top: 0.3rem;
    font-size: 0.875rem;
  }
  input {
    flex: none;
    width: 5.5rem;
    min-height: 2.5rem;
    padding: 0.45rem 0.6rem 0.45rem 0.85rem;
    font: inherit;
    font-variant-numeric: tabular-nums;
    color: var(--text);
    background: var(--bg);
    border: 1px solid var(--line);
    border-radius: var(--radius);
  }
  input:focus-visible {
    border-color: var(--text);
    outline: none;
  }
  input:disabled {
    opacity: 0.6;
  }
  input[aria-invalid='true'] {
    border-color: var(--danger);
  }
  .ok {
    color: var(--ok);
  }
  .error {
    color: var(--danger);
  }
  @media (max-width: 600px) {
    .row {
      align-items: flex-start;
    }
  }
</style>
