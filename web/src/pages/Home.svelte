<script lang="ts">
  import { api, type ItemSummary } from '../lib/api';
  import { useAmbient } from '../lib/ambient.svelte';
  import { episodeLabel, formatDuration } from '../lib/format';
  import { byAdded, byTitle, ensureLibrary, library, showsOf, type ShowEntry } from '../lib/library.svelte';
  import { href } from '../lib/router.svelte';
  import { rescan, scan } from '../lib/state.svelte';
  import { app } from '../lib/state.svelte';
  import { artOf, progressOf } from '../lib/ui-types';
  import Badges from '../components/Badges.svelte';
  import Hero from '../components/Hero.svelte';
  import Icon from '../components/Icon.svelte';
  import MediaCard from '../components/MediaCard.svelte';
  import Rail from '../components/Rail.svelte';

  let continueItems = $state<ItemSummary[]>([]);

  $effect(() => {
    void app.libraryVersion;
    void ensureLibrary();
    api.continueWatching().then(
      (list) => (continueItems = list),
      () => (continueItems = []),
    );
  });

  const items = $derived(library.items);
  const recent = $derived([...items].sort(byAdded).slice(0, 24));
  const movies = $derived(items.filter((i) => i.kind === 'movie').sort(byTitle));
  const shows = $derived(showsOf(items).sort((a, b) => b.latestAddedAt - a.latestAddedAt));
  // Lead with the newest film that has artwork; fall back to the newest anything.
  const feature = $derived(recent.find((i) => i.kind === 'movie' && artOf(i)) ?? recent.find((i) => artOf(i)) ?? recent[0]);

  useAmbient(() => artOf(feature)?.color);

  const isEpisode = (i: ItemSummary) => i.kind === 'episode';
  const cardTitle = (i: ItemSummary) => (isEpisode(i) ? i.show || i.title : i.title);
  const cardSub = (i: ItemSummary) =>
    isEpisode(i) ? [episodeLabel(i.season, i.episode), i.episodeTitle] : [i.year, formatDuration(i.durationSec)];
  const resumeAt = (i: ItemSummary) => (progressOf(i) > 0 ? (i.progress?.positionSec ?? 0) : 0);
</script>

{#if library.error && !library.loaded}
  <div class="page center">
    <p class="error">{library.error}</p>
    <button class="btn" onclick={() => ensureLibrary()}>Try again</button>
  </div>
{:else if !library.loaded}
  <div class="loading" aria-busy="true" aria-label="Loading the library">
    <div class="skeleton hero-sk"></div>
    <div class="row-sk">
      {#each Array(5) as _, i (i)}<div class="skeleton card-sk"></div>{/each}
    </div>
  </div>
{:else if items.length === 0}
  <div class="page center empty">
    <h1>Nothing to watch yet</h1>
    <p class="muted">
      Start ply with <code>-media /path/to/movies</code>, then rescan. Episodes named like
      <code>Show S01E02</code> are grouped into shows.
    </p>
    <button class="btn primary" onclick={rescan} disabled={scan.running}>
      {scan.running ? 'Scanning…' : 'Rescan the library'}
    </button>
  </div>
{:else}
  {#if feature}
    <Hero item={feature} label={cardTitle(feature)} tall>
      <h1>{cardTitle(feature)}</h1>
      <div class="meta">
        {#if isEpisode(feature)}<span>{episodeLabel(feature.season, feature.episode)}</span>{/if}
        {#if feature.year}<span>{feature.year}</span>{/if}
        {#if feature.durationSec}<span>{formatDuration(feature.durationSec)}</span>{/if}
        <span class="badges"><Badges item={feature} /></span>
      </div>
      <div class="actions">
        <a class="btn primary" href={href.play(feature.id, resumeAt(feature))}>
          <Icon name="play" />{resumeAt(feature) ? 'Resume' : 'Play'}
        </a>
        <a class="btn" href={href.item(feature.id)}><Icon name="info" />Details</a>
      </div>
    </Hero>
  {/if}

  <div class="rails">
    <Rail title="Continue watching" items={continueItems} key={(i) => i.id}>
      {#snippet card(i: ItemSummary)}
        <MediaCard item={i} href={href.play(i.id, i.progress?.positionSec ?? 0)} title={cardTitle(i)} sub={cardSub(i)} />
      {/snippet}
    </Rail>
    <Rail title="Recently added" items={recent} key={(i) => i.id}>
      {#snippet card(i: ItemSummary)}
        <MediaCard item={i} href={href.item(i.id)} title={cardTitle(i)} sub={cardSub(i)} />
      {/snippet}
    </Rail>
    <Rail title="Movies" items={movies.slice(0, 30)} key={(i) => i.id} more={{ href: href.movies, label: `All ${movies.length}` }}>
      {#snippet card(i: ItemSummary)}
        <MediaCard item={i} href={href.item(i.id)} title={i.title} sub={cardSub(i)} />
      {/snippet}
    </Rail>
    <Rail title="Shows" items={shows} key={(s) => s.show} more={{ href: href.shows, label: 'All shows' }}>
      {#snippet card(s: ShowEntry)}
        <MediaCard
          item={s.cover}
          href={href.show(s.show)}
          title={s.show}
          sub={[`${s.episodeCount} episode${s.episodeCount === 1 ? '' : 's'}`]}
        />
      {/snippet}
    </Rail>
  </div>
{/if}

<style>
  .meta {
    color: rgb(244 239 230 / 0.82);
  }
  .badges {
    display: inline-flex;
    gap: 4px;
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: 0.75rem;
    margin-top: 0.4rem;
  }
  .rails {
    padding-bottom: 4rem;
    position: relative;
  }
  .empty {
    max-width: 34rem;
    margin: 0 auto;
  }
  .loading {
    padding: 0 0 4rem;
  }
  .hero-sk {
    height: min(82vh, 64vw);
    border-radius: 0;
  }
  .row-sk {
    display: flex;
    gap: 1.1rem;
    padding: 2.25rem var(--gutter) 0;
    overflow: hidden;
  }
  .card-sk {
    flex: 0 0 clamp(220px, 22vw, 320px);
    aspect-ratio: 16 / 9;
  }
  @media (max-width: 600px) {
    .rails {
      padding-bottom: 6rem;
    }
  }
</style>
