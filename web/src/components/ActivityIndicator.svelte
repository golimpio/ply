<script lang="ts">
  import { activity, indicatorLabel } from '../lib/activity.svelte';
  import { href } from '../lib/router.svelte';
  import Icon from './Icon.svelte';

  // The header's window on the background queue: a quiet glyph when nothing is going on, a turning
  // ring with the count while jobs run, amber when the queue is paused or a job just failed.
  let { current = false }: { current?: boolean } = $props();

  const s = $derived(activity.summary);
  const label = $derived(indicatorLabel(s));
  const tone = $derived(s.recentFailure ? 'failed' : s.paused ? 'paused' : s.running ? 'running' : s.queued ? 'queued' : 'idle');
</script>

<a
  class="act {tone}"
  class:count={s.running > 0}
  href={href.activity}
  aria-label={label}
  title={label}
  data-state={tone}
  aria-current={current ? 'page' : undefined}
>
  {#if s.running > 0}
    <svg class="ring" class:held={s.held === s.running} viewBox="0 0 20 20" aria-hidden="true">
      <circle cx="10" cy="10" r="7.5" class="track" />
      <circle cx="10" cy="10" r="7.5" class="arc" pathLength="100" />
    </svg>
    <span class="n tabular">{s.running}</span>
  {:else if s.recentFailure}
    <Icon name="alert" />
  {:else if s.paused}
    <Icon name="pause" />
  {:else}
    <Icon name="activity" />
  {/if}
  {#if s.recentFailure && s.running > 0}<span class="dot" aria-hidden="true"></span>{/if}
</a>

<style>
  .act {
    position: relative;
    flex: none;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 0.4rem;
    height: 2.5rem;
    min-width: 2.5rem;
    padding: 0 0.6rem;
    color: var(--text-muted);
    border: 1px solid transparent;
    border-radius: var(--radius-pill);
    transition:
      background var(--motion-hover),
      color var(--motion-hover),
      border-color var(--motion-hover);
  }
  .act:hover,
  .act[aria-current='page'] {
    color: var(--text);
    background: var(--fill);
  }
  .act :global(svg) {
    width: 1.15rem;
    height: 1.15rem;
    flex: none;
  }
  .idle {
    opacity: 0.7;
  }
  .queued,
  .running {
    color: var(--text);
    background: var(--fill);
    border-color: var(--line-control);
  }
  .count {
    padding: 0 0.75rem 0 0.6rem;
  }
  .paused,
  .failed {
    color: var(--warn);
    background: color-mix(in oklab, var(--warn) 12%, transparent);
    border-color: color-mix(in oklab, var(--warn) 35%, transparent);
  }
  .n {
    font-size: var(--fs-xs-3);
    font-weight: var(--fw-bold);
    line-height: var(--lh-none);
  }
  .ring {
    overflow: visible;
  }
  .ring circle {
    fill: none;
    stroke-width: 2.4;
  }
  .track {
    stroke: currentColor;
    opacity: 0.2;
  }
  .arc {
    stroke: var(--tint);
    stroke-linecap: round;
    stroke-dasharray: 28 72;
    transform-origin: 10px 10px;
    animation: turn var(--motion-turn) var(--ease-linear) infinite;
  }
  .paused .arc,
  .failed .arc,
  .held .arc {
    stroke: var(--warn);
  }
  /* Held by playback or browsing: it breathes instead of turning. */
  .held .arc {
    stroke-dasharray: 100 0;
    animation: breathe var(--motion-breathe) var(--ease-in-out) infinite;
  }
  .dot {
    position: absolute;
    top: 0.3rem;
    right: 0.3rem;
    width: 0.5rem;
    height: 0.5rem;
    background: var(--warn);
    border: 2px solid var(--surface-room);
    border-radius: var(--radius-round);
  }
  @keyframes turn {
    to {
      transform: rotate(360deg);
    }
  }
  @keyframes breathe {
    50% {
      opacity: 0.35;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .arc,
    .held .arc {
      animation: none;
      stroke-dasharray: 100 0;
    }
  }
</style>
