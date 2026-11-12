<script lang="ts">
  import { api, type ScheduleUpdate, type ScheduledTask } from '../lib/api';
  import { href } from '../lib/router.svelte';
  import { daysWords, lastRunWords, nextRunWords, WEEKDAYS } from '../lib/schedules';

  // One background task's schedule, in Settings → Schedules (docs/M12-SCHEDULES.md): when it runs,
  // on which days, whether it may run at all (its key, or the privacy switch), and a way to queue it
  // now. Every change is saved at once, as the rest of the settings are.
  let { task, onkeys, onupdate }: {
    task: ScheduledTask;
    onkeys?: () => void;
    onupdate?: (t: ScheduledTask) => void;
  } = $props();

  let saving = $state(false);
  let running = $state(false);
  let note = $state<{ ok: boolean; text: string } | null>(null);
  const id = $derived(`schedule-${task.task.replace(/\./g, '-')}`);
  const last = $derived(lastRunWords(task.lastRun));

  const said = (e: unknown) => (e instanceof Error ? e.message : String(e));

  async function save(update: ScheduleUpdate, text: string): Promise<boolean> {
    if (saving) return false;
    saving = true;
    note = null;
    try {
      onupdate?.(await api.updateSchedule(task.task, update));
      note = { ok: true, text };
      return true;
    } catch (e) {
      note = { ok: false, text: said(e) };
      return false;
    } finally {
      saving = false;
    }
  }

  function toggle() {
    void save({ enabled: !task.enabled }, task.enabled ? 'The schedule is off.' : 'The schedule is on.');
  }

  async function setAt(e: Event & { currentTarget: HTMLInputElement }) {
    const input = e.currentTarget;
    const v = input.value;
    if (!/^([01]\d|2[0-3]):[0-5]\d$/.test(v)) {
      note = { ok: false, text: 'Pick a time of day.' };
      input.value = task.at; // the field shows what the schedule is, not a value the server never took
      return;
    }
    if (!(await save({ at: v }, `Runs at ${v}.`))) input.value = task.at;
  }

  function toggleDay(iso: number) {
    const days = task.days.includes(iso) ? task.days.filter((d) => d !== iso) : [...task.days, iso];
    void save({ days }, `Runs ${daysWords(days).toLowerCase()}.`);
  }

  async function runNow() {
    running = true;
    note = null;
    try {
      await api.runSchedule(task.task);
      note = { ok: true, text: 'Queued now: follow it on Activity.' };
    } catch (e) {
      note = { ok: false, text: said(e) };
    } finally {
      running = false;
    }
  }

  function openKeys(e: MouseEvent) {
    if (!onkeys || e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return; // a new tab: the link's own way
    e.preventDefault();
    onkeys();
  }
</script>

<li class="settings-panel schedule" id={id} aria-labelledby="{id}-h">
  <div class="head">
    <div class="text">
      <h3 id="{id}-h">{task.title}</h3>
      <p class="muted detail">{task.detail}</p>
    </div>
    <button class="switch" role="switch" aria-checked={task.enabled} aria-labelledby="{id}-h" disabled={saving} onclick={toggle}
    ><span></span></button
    >
  </div>

  {#if task.blocked}
    <p class="warn blocked" role="status">
      <strong>Will not run:</strong>
      {task.blocked}
      {#if task.missing.length}
        Add it in <a class="link" href={href.settingsTab('keys')} onclick={openKeys}>API keys</a>.
      {:else}
        <a class="link" href={href.settingsTab('details')} onclick={openKeys}>Details</a>.
      {/if}
    </p>
  {/if}
  {#each task.warnings as w (w)}
    <p class="muted warning">{w}</p>
  {/each}

  <div class="when">
    <label class="time">
      <span>At</span>
      <input class="settings-input" type="time" value={task.at} required disabled={saving} onchange={setAt} />
    </label>
    <fieldset class="days" aria-describedby="{id}-days-hint">
      <legend>Days</legend>
      <div class="day-chips">
        {#each WEEKDAYS as d (d.iso)}
          <button
            type="button"
            class="chip"
            aria-pressed={task.days.includes(d.iso)}
            aria-label={d.long}
            disabled={saving}
            onclick={() => toggleDay(d.iso)}>{d.short}</button
          >
        {/each}
      </div>
      <p class="muted hint" id="{id}-days-hint">{daysWords(task.days)}</p>
    </fieldset>
  </div>

  <div class="foot">
    <p class="muted state">
      {#if !task.enabled}
        Off.
      {:else if task.next}
        Next run {nextRunWords(task.next)}.
      {:else}
        Not running.
      {/if}
      {#if task.lastRun}
        Last run {last}.
      {:else}
        Not run by its schedule yet.
      {/if}
    </p>
    <button class="btn pill" disabled={saving || running || !!task.blocked} onclick={runNow}>
      {running ? 'Queuing…' : 'Run now'}
    </button>
  </div>
  <p class="muted message" class:error={!!note && !note.ok} class:ok={note?.ok === true} role="status">{note?.text ?? ''}</p>
</li>

<style>
  .schedule {
    list-style: none;
    gap: 0.75rem;
  }
  .head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 1rem;
  }
  .text {
    min-width: 0;
  }
  .head h3 {
    margin: 0 0 0.25rem;
  }
  .detail,
  .warning {
    max-width: 62ch;
    font-size: var(--fs-sm);
  }
  .when {
    display: grid;
    grid-template-columns: auto 1fr;
    align-items: start;
    gap: 0.75rem var(--gutter);
  }
  .time {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .days {
    margin: 0;
    padding: 0;
    border: 0;
    min-width: 0;
  }
  .days legend {
    padding: 0;
    margin-bottom: 0.4rem;
    color: var(--text-muted);
    font-size: var(--fs-sm);
  }
  .day-chips {
    display: flex;
    flex-wrap: wrap;
    gap: var(--chip-gap);
  }
  .days .hint {
    font-size: var(--fs-sm);
    margin-top: 0.4rem;
  }
  .foot {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 0.75rem;
  }
  .state,
  .message {
    margin: 0;
    font-size: var(--fs-sm);
  }
  .message:empty {
    display: none;
  }
  @media (max-width: 600px) {
    .when {
      grid-template-columns: 1fr;
    }
  }
</style>
