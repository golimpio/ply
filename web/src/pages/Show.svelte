<script lang="ts">
  import { api, type ItemSummary } from '../lib/api';
  import { formatDuration, seasonLabel } from '../lib/format';
  import { href } from '../lib/router.svelte';
  import { resource } from '../lib/resource.svelte';
  import { app } from '../lib/state.svelte';
  import Badges from '../components/Badges.svelte';

  let { show }: { show: string } = $props();

  const episodes = resource(() => {
    void app.libraryVersion;
    return api.items({ kind: 'episode', show });
  });

  const seasons = $derived.by(() => {
    const bySeason = new Map<number, ItemSummary[]>();
    for (const ep of episodes.data ?? []) {
      const key = ep.season ?? 0;
      const list = bySeason.get(key);
      if (list) list.push(ep);
      else bySeason.set(key, [ep]);
    }
    return [...bySeason]
      .sort(([a], [b]) => a - b)
      .map(([season, list]) => ({ season, list: list.sort((a, b) => (a.episode ?? 0) - (b.episode ?? 0)) }));
  });
</script>

<p><a href={href.shows}>&larr; All shows</a></p>
<h1>{show}</h1>

{#if episodes.error}
  <p class="error">{episodes.error}</p>
{:else if !episodes.data}
  <p class="muted">Loading…</p>
{:else if episodes.data.length === 0}
  <p class="muted">No episodes found for this show.</p>
{:else}
  <p class="muted">
    {seasons.length} season{seasons.length === 1 ? '' : 's'} · {episodes.data.length} episode{episodes.data
      .length === 1
      ? ''
      : 's'}
  </p>
  {#each seasons as { season, list } (season)}
    <section>
      <h2>{seasonLabel(season)}</h2>
      <ul class="rows">
        {#each list as ep (ep.id)}
          <li class="row">
            <a class="main" href={href.item(ep.id)}>
              <span class="num">{ep.episode ?? '–'}</span>
              <span class="title">{ep.episodeTitle || `Episode ${ep.episode ?? ''}`.trim()}</span>
              <span class="muted dur">{formatDuration(ep.durationSec)}</span>
              <span class="badges"><Badges item={ep} /></span>
            </a>
            <a class="btn" href={href.play(ep.id)} aria-label="Play {ep.episodeTitle || `episode ${ep.episode}`}">
              Play
            </a>
          </li>
        {/each}
      </ul>
    </section>
  {/each}
{/if}

<style>
  section {
    margin-top: 1.5rem;
  }
  h2 {
    margin: 0 0 0.5rem;
  }
  .rows {
    list-style: none;
    margin: 0;
    padding: 0;
    border: 1px solid var(--line);
    border-radius: var(--radius);
    overflow: hidden;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 0.5rem 0.75rem;
    background: var(--bg-2);
  }
  .row + .row {
    border-top: 1px solid var(--line);
  }
  .main {
    flex: 1;
    min-width: 0;
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 0.25rem 0.75rem;
    color: inherit;
    padding: 0.2rem 0;
  }
  .main:hover {
    text-decoration: none;
  }
  .main:hover .title {
    color: var(--accent);
  }
  .num {
    width: 2rem;
    text-align: right;
    color: var(--muted);
    font-variant-numeric: tabular-nums;
  }
  .title {
    flex: 1 1 12rem;
    min-width: 0;
    font-weight: 500;
  }
  .dur {
    font-size: 0.85rem;
    font-variant-numeric: tabular-nums;
  }
  .badges {
    display: inline-flex;
    gap: 4px;
  }
</style>
