<script lang="ts">
  import { api } from '../lib/api';
  import { cardHref, sortCards, type TitleSort } from '../lib/credits';
  import { backdropOf, posterImg, prefersPosters } from '../lib/meta';
  import { resource } from '../lib/resource.svelte';
  import { href } from '../lib/router.svelte';
  import MediaCard from '../components/MediaCard.svelte';

  let { id }: { id: number } = $props();

  const titles = resource(() => api.titles({ genre: id }));
  // The name comes from the genre list; a failure there only costs the name.
  const genres = resource(() => api.genres());
  const name = $derived(genres.data?.find((g) => g.id === id)?.name ?? 'Genre');

  const sorts: { value: TitleSort; label: string }[] = [
    { value: 'title', label: 'Title' },
    { value: 'year', label: 'Year' },
    { value: 'rating', label: 'Rating' },
  ];
  let sort = $state<TitleSort>('title');

  // Only titles that can be opened: a film with a file, a series with a show.
  const all = $derived((titles.data ?? []).filter(cardHref));
  const shown = $derived(sortCards(all, sort));
  // Most of them without a poster (no TMDB key) read better as wide cards.
  const wide = $derived(!prefersPosters(all));
</script>

{#if !titles.data && titles.error}
  <div class="page center">
    <p class="error">{titles.error}</p>
    <p><a href={href.home}>Back to the library</a></p>
  </div>
{:else if !titles.data || genres.loading}
  <div class="page" aria-busy="true">
    <div class="skeleton sk-name"></div>
    <div class="poster-grid">
      {#each Array(8) as _, i (i)}<div class="skeleton sk-card"></div>{/each}
    </div>
  </div>
{:else}
  <div class="page">
    <h1>{name}{#if all.length}<span class="count">{all.length}</span>{/if}</h1>

    {#if all.length === 0}
      <p class="muted empty">No titles in this genre yet.</p>
    {:else}
      <div class="chips controls" role="group" aria-label="Sort by">
        {#each sorts as s (s.value)}
          <button class="chip" aria-pressed={sort === s.value} onclick={() => (sort = s.value)}>{s.label}</button>
        {/each}
      </div>

      <div class={wide ? 'grid' : 'poster-grid'}>
        {#each shown as card (card.id)}
          <MediaCard
            shape={wide ? 'wide' : 'poster'}
            img={wide ? backdropOf(card) : posterImg(card)}
            href={cardHref(card) ?? href.home}
            title={card.title}
            sub={[card.year || undefined]}
            rating={card.rating}
          />
        {/each}
      </div>
    {/if}
  </div>
{/if}

<style>
  .controls {
    margin: 1.25rem 0 1.75rem;
  }
  .empty {
    margin-top: 1.25rem;
  }
  .sk-name {
    height: 2.6rem;
    width: min(14rem, 70%);
    margin-bottom: 4.25rem;
  }
</style>
