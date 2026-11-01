<script lang="ts">
  import type { ItemSummary } from '../lib/api';
  import { useAmbient } from '../lib/ambient.svelte';
  import { episodeLabel, formatDuration, seasonLabel } from '../lib/format';
  import { ensureLibrary, library, showsOf } from '../lib/library.svelte';
  import { morph } from '../lib/nav';
  import { href } from '../lib/router.svelte';
  import { app } from '../lib/state.svelte';
  import { artOf, progressOf } from '../lib/ui-types';
  import Art from '../components/Art.svelte';
  import Badges from '../components/Badges.svelte';
  import Hero from '../components/Hero.svelte';
  import Icon from '../components/Icon.svelte';

  let { show, season }: { show: string; season?: number } = $props();

  $effect(() => {
    void app.libraryVersion;
    void ensureLibrary();
  });

  const entry = $derived(showsOf(library.items.filter((i) => i.show === show))[0]);
  const episodes = $derived(
    library.items
      .filter((i) => i.kind === 'episode' && i.show === show)
      .sort((a, b) => (a.season ?? 0) - (b.season ?? 0) || (a.episode ?? 0) - (b.episode ?? 0)),
  );
  // Regular seasons first, specials last.
  const seasons = $derived(
    [...new Set(episodes.map((e) => e.season ?? 0))].sort((a, b) => (a || 1000) - (b || 1000)),
  );
  // Next up: the first started-but-unfinished episode, else the first unwatched one.
  const next = $derived(
    episodes.find((e) => progressOf(e) > 0) ?? episodes.find((e) => !e.progress?.watched) ?? episodes[0],
  );
  const current = $derived(season !== undefined && seasons.includes(season) ? season : (next?.season ?? seasons[0]));
  const list = $derived(episodes.filter((e) => (e.season ?? 0) === current));

  useAmbient(() => artOf(entry?.cover)?.color);

  const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`;
  const resumeAt = (e: ItemSummary) => (progressOf(e) > 0 ? (e.progress?.positionSec ?? 0) : 0);
</script>

{#if library.error && !library.loaded}
  <div class="page"><p class="error">{library.error}</p></div>
{:else if !library.loaded}
  <div class="skeleton hero-sk" aria-busy="true"></div>
{:else if !entry}
  <div class="page center">
    <h1>No episodes of “{show}”</h1>
    <p><a href={href.shows}>Back to all shows</a></p>
  </div>
{:else}
  <Hero item={entry.cover} label={show} landing>
    <h1>{show}</h1>
    <div class="meta">
      <span>{plural(seasons.filter((s) => s !== 0).length || seasons.length, 'season')}</span>
      <span>{plural(episodes.length, 'episode')}</span>
    </div>
    {#if next}
      <div class="actions">
        <a class="btn primary" href={href.play(next.id, resumeAt(next))}>
          <Icon name="play" />
          {resumeAt(next) ? 'Resume' : 'Play'}
          {episodeLabel(next.season, next.episode)}
        </a>
      </div>
    {/if}
  </Hero>

  <div class="body">
    {#if seasons.length > 1}
      <div class="chips" role="tablist" aria-label="Seasons">
        {#each seasons as s (s)}
          <a class="chip" role="tab" aria-selected={s === current} href={href.show(show, s)}>{seasonLabel(s)}</a>
        {/each}
      </div>
    {:else}
      <h2>{seasonLabel(current)}</h2>
    {/if}

    <ol class="episodes" role={seasons.length > 1 ? 'tabpanel' : undefined}>
      {#each list as ep (ep.id)}
        {@const p = progressOf(ep)}
        <li>
          <a class="ep" href={href.item(ep.id)} onclick={morph}>
            <div class="thumb">
              <Art item={ep} label={show} />
              {#if p > 0}<div class="bar"><span style:width="{p * 100}%"></span></div>{/if}
              {#if ep.progress?.watched}<span class="watched">Watched</span>{/if}
            </div>
            <div class="text">
              <div class="line">
                <span class="num tabular">{ep.episode ?? ''}</span>
                <span class="title">{ep.episodeTitle || `Episode ${ep.episode ?? ''}`.trim()}</span>
              </div>
              <div class="meta small">
                {#if ep.durationSec}<span>{formatDuration(ep.durationSec)}</span>{/if}
                <span class="badges"><Badges item={ep} notable /></span>
              </div>
            </div>
          </a>
          <a class="btn icon play" href={href.play(ep.id, resumeAt(ep))} aria-label="Play {episodeLabel(ep.season, ep.episode)}">
            <Icon name="play" />
          </a>
        </li>
      {/each}
    </ol>
  </div>
{/if}

<style>
  .hero-sk {
    height: min(68vh, 56vw);
    border-radius: 0;
  }
  .actions {
    margin-top: 0.4rem;
  }
  .body {
    padding: 0.5rem var(--gutter) 4rem;
    max-width: 72rem;
  }
  .episodes {
    list-style: none;
    margin: 1.25rem 0 0;
    padding: 0;
    display: grid;
    gap: 0.5rem;
  }
  li {
    display: flex;
    align-items: center;
    gap: 1rem;
    padding: 0.5rem;
    margin: 0 -0.5rem;
    border-radius: var(--radius-lg);
  }
  li:hover,
  li:focus-within {
    background: rgb(255 255 255 / 0.04);
  }
  .ep {
    flex: 1;
    min-width: 0;
    display: grid;
    grid-template-columns: clamp(8rem, 22vw, 13rem) 1fr;
    align-items: center;
    gap: 1.1rem;
  }
  .ep:focus-visible {
    outline: none;
  }
  .ep:focus-visible .thumb {
    outline: 3px solid var(--text);
    outline-offset: 2px;
  }
  .thumb {
    position: relative;
    border-radius: var(--radius);
    overflow: hidden;
  }
  .thumb :global(.tile) {
    font-size: 1.2rem;
  }
  .bar {
    position: absolute;
    inset: auto 0 0;
    height: 4px;
    background: rgb(0 0 0 / 0.5);
  }
  .bar span {
    display: block;
    height: 100%;
    background: var(--text);
  }
  .watched {
    position: absolute;
    top: 6px;
    right: 6px;
    padding: 0.05rem 0.45rem;
    font-size: 0.72rem;
    font-weight: 650;
    background: rgb(16 14 12 / 0.75);
    border-radius: 4px;
  }
  .text {
    min-width: 0;
  }
  .line {
    display: flex;
    gap: 0.75rem;
    align-items: baseline;
  }
  .num {
    color: var(--muted);
    min-width: 1.5rem;
  }
  .title {
    font-weight: 650;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .small {
    margin-top: 0.2rem;
    padding-left: 2.25rem;
    font-size: 0.875rem;
  }
  .badges {
    display: inline-flex;
    gap: 4px;
  }
  .play {
    flex: none;
  }
  @media (max-width: 600px) {
    .ep {
      grid-template-columns: 7.5rem 1fr;
      gap: 0.8rem;
    }
    .small {
      padding-left: 0;
    }
    .num {
      display: none;
    }
  }
</style>
