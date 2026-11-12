<script lang="ts">
  import { onMount } from 'svelte';
  import { api, ApiError } from '../lib/api';
  import { formatClock } from '../lib/format';
  import { limitMessage, type PlayLimit } from '../lib/playlimit';
  import { href } from '../lib/router.svelte';

  // Shown instead of the video when the server is at its limit of plays or conversions: what is
  // playing (each can be stopped to make room), a Try again button, and a countdown to trying again
  // on its own. It stays mounted while the refusals repeat: each new `limit` refreshes what it shows
  // and starts the countdown over, without announcing it all again or moving the focus.
  let {
    limit,
    auto,
    busy,
    onRetry,
  }: {
    limit: PlayLimit;
    /** Count down and try again on its own (the player stops that after a while). */
    auto: boolean;
    /** A retry is under way. */
    busy: boolean;
    /** auto: the countdown ran out (not the viewer). */
    onRetry: (auto: boolean) => void;
  } = $props();

  let left = $state(0);
  let stopping = $state<string | null>(null);
  let stopError = $state('');
  let heading = $state<HTMLElement>();

  // A new refusal (a new `limit`) starts the countdown over. It holds still while a retry or a Stop
  // is under way: that one's answer is what counts, and it must not be overtaken by a second retry.
  $effect(() => {
    left = limit.retryAfter;
    if (!auto) return;
    const t = setInterval(() => {
      if (busy || stopping !== null) return;
      if (--left > 0) return;
      clearInterval(t);
      onRetry(true);
    }, 1000);
    return () => clearInterval(t);
  });

  onMount(() => heading?.focus());

  const blocked = $derived(busy || stopping !== null);

  function tryAgain() {
    if (!blocked) onRetry(false);
  }

  /** Stop something that is playing, then try again at once. */
  async function stop(id: string) {
    if (blocked) return;
    stopping = id;
    stopError = '';
    try {
      await api.stopSession(id);
    } catch (e) {
      // Already gone (it ended, or was stopped elsewhere): its room is free all the same.
      if (!(e instanceof ApiError && (e.status === 404 || e.status === 410))) {
        stopError = `Could not stop it: ${e instanceof Error ? e.message : String(e)}`;
        stopping = null;
        return;
      }
    }
    stopping = null;
    onRetry(false);
  }
</script>

<!-- Buttons are aria-disabled, not disabled, while busy: disabling the focused one would drop the focus to the page. -->
<div class="limit" role="region" aria-labelledby="limit-h">
  <h2 id="limit-h" tabindex="-1" bind:this={heading}>This video can't start yet</h2>
  <p role="alert">{limitMessage(limit)}</p>
  {#if limit.playing.length}
    <ul aria-label={limit.limit === 'transcodes' ? 'Being converted' : 'Playing now'}>
      {#each limit.playing as p (p.id)}
        <li data-session={p.id}>
          <div class="what">
            <span class="title">{p.title}</span>
            <span class="muted">
              {p.client || 'Unknown device'} · <span class="tabular">{p.paused ? 'Paused at' : 'At'} {formatClock(p.positionSec)}</span>
            </span>
          </div>
          <button class="btn" aria-disabled={blocked} aria-label={`Stop ${p.title}`} onclick={() => stop(p.id)}>
            {stopping === p.id ? 'Stopping…' : 'Stop'}
          </button>
        </li>
      {/each}
    </ul>
  {/if}
  {#if stopError}<p class="error">{stopError}</p>{/if}
  <div class="actions">
    <button class="btn primary" aria-disabled={blocked} onclick={tryAgain}>
      {busy ? 'Trying…' : 'Try again'}
    </button>
    <span class="muted countdown">
      {#if busy}
        Asking the server…
      {:else if auto}
        Trying again in <span class="tabular">{left}</span> s
      {:else}
        Stopped trying on its own.
      {/if}
    </span>
  </div>
  <p class="muted small">
    <a class="link" href={href.settingsTab('playback')}>Raise the limit in Settings</a>
  </p>
</div>

<style>
  .limit {
    width: min(32rem, 100%);
    display: grid;
    gap: 0.75rem;
    text-align: left;
  }
  h2 {
    margin: 0;
    font-size: var(--fs-xl);
  }
  h2:focus {
    outline: none;
  }
  p {
    margin: 0;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 0.4rem;
  }
  li {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.75rem;
    padding: 0.5rem 0.5rem 0.5rem 0.8rem;
    background: var(--surface-panel);
    border: 1px solid var(--line);
    border-radius: var(--radius-lg);
    backdrop-filter: var(--glass-blur);
  }
  .what {
    display: grid;
    min-width: 0;
  }
  .title {
    font-weight: var(--fw-semibold-2);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .what .muted {
    font-size: var(--fs-xs-3);
  }
  .tabular {
    font-variant-numeric: tabular-nums;
  }
  button[aria-disabled='true'] {
    opacity: var(--opacity-disabled);
    cursor: default;
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.6rem 1rem;
  }
  .small {
    font-size: var(--fs-sm);
  }
  .error {
    color: var(--danger);
  }
</style>
