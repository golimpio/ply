<script lang="ts">
  import type { WatchBadge } from '../lib/watched';

  // A film's, an episode's, a season's or a show's watch state (lib/watched): ✓, "3 left" or "In
  // progress", in the style of the format badges; a dot when an episode is started. A screen reader
  // hears the words ("Watched", "3 episodes left, 1 in progress").
  let { badge }: { badge: WatchBadge } = $props();
</script>

<span class="badge watch {badge.kind}" data-watch={badge.kind}>
  {#if badge.started && badge.kind !== 'started'}<span class="dot" aria-hidden="true"></span>{/if}
  <span aria-hidden="true">{badge.text}</span>
  <span class="sr-only">{badge.label}</span>
</span>

<style>
  .watch {
    position: relative; /* holds its sr-only words */
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
  }
  .done {
    color: var(--ok);
  }
  .dot {
    width: var(--season-dot);
    height: var(--season-dot);
    border-radius: var(--radius-round);
    background: var(--accent);
  }
  .started {
    color: var(--accent);
  }
</style>
