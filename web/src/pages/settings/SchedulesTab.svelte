<script lang="ts">
  import { api, type ScheduledTask } from '../../lib/api';
  import ScheduleCard from '../../components/ScheduleCard.svelte';

  // Settings → Schedules (M12, docs/M12-SCHEDULES.md): when each background task runs, and whether it
  // may. The times are the server's own clock, and the server keeps each run in its database: a
  // restart does not scan or ask again, and a task whose API key is missing is not run at all.
  let { onkeys }: { onkeys?: () => void } = $props();

  let tasks = $state<ScheduledTask[] | null>(null);
  let error = $state('');

  $effect(() => {
    let live = true;
    api
      .schedules()
      .then((list) => {
        if (live) tasks = list;
      })
      .catch((e: unknown) => {
        if (live) error = e instanceof Error ? e.message : String(e);
      });
    return () => {
      live = false;
    };
  });

  /** The server's answer to a change is the schedule: it replaces the one the page had. */
  function replace(t: ScheduledTask) {
    tasks = tasks?.map((x) => (x.task === t.task ? t : x)) ?? [t];
  }
</script>

<section aria-labelledby="schedules-h">
  <h2 id="schedules-h">Schedules</h2>
  <p class="muted lead">
    When ply's background work runs. The times are this server's own clock, and each run is kept in its database: a
    restart neither repeats it nor loses it. A task whose API key is missing is skipped, and says so here and in the log;
    <a class="link" href="#/activity">Activity</a> shows what is running and what it did.
  </p>
  {#if error}
    <p class="error">{error}</p>
  {:else if !tasks}
    <div class="skeleton block" aria-busy="true"></div>
  {:else if tasks.length === 0}
    <p class="muted">This server has no scheduled tasks.</p>
  {:else}
    <ul class="settings-list">
      {#each tasks as task (task.task)}
        <ScheduleCard {task} {onkeys} onupdate={replace} />
      {/each}
    </ul>
  {/if}
</section>

