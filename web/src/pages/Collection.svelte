<script lang="ts">
  import { api } from '../lib/api';
  import { cardHref, yearSpan } from '../lib/credits';
  import { backdropOf, posterImg, prefersPosters } from '../lib/meta';
  import { resource } from '../lib/resource.svelte';
  import { href } from '../lib/router.svelte';
  import { titleBadge } from '../lib/library.svelte';
  import BackButton from '../components/BackButton.svelte';
  import Hero from '../components/Hero.svelte';
  import MediaCard from '../components/MediaCard.svelte';

  let { id }: { id: number } = $props();

  const detail = resource(() => api.collection(id));
  const collection = $derived(detail.data);

  // Only titles that can be opened, in release order (as the server sends them).
  const titles = $derived((collection?.titles ?? []).filter(cardHref));
  // Most of them without a poster (no TMDB key) read better as wide cards.
  const wide = $derived(!prefersPosters(titles));
  const years = $derived(yearSpan(titles));
  // A collection with no backdrop of its own borrows its first title's.
  const backdrop = $derived(backdropOf(collection) ?? backdropOf(titles.find((t) => t.backdrop)));
</script>

<BackButton over />
{#if !collection && detail.error}
  <div class="page center">
    <p class="error">{detail.error}</p>
    <p><a href={href.home}>Back to the library</a></p>
  </div>
{:else if !collection}
  <div class="skeleton hero-sk" aria-busy="true"></div>
{:else}
  <Hero item={undefined} img={backdrop} label={collection.name} back>
    <h1>{collection.name}</h1>
    {#if collection.overview}<p class="overview">{collection.overview}</p>{/if}
    <div class="meta">
      <span>{titles.length} {titles.length === 1 ? 'title' : 'titles'} in your library</span>
      {#if years}<span>{years}</span>{/if}
    </div>
  </Hero>

  <div class="page flush">
    <h2 class="heading">In release order</h2>
    {#if titles.length === 0}
      <p class="muted">None of this collection is in your library right now.</p>
    {:else}
      <div class={wide ? 'grid' : 'poster-grid'}>
        {#each titles as card (card.id)}
          <MediaCard
            shape={wide ? 'wide' : 'poster'}
            img={wide ? backdropOf(card) : posterImg(card)}
            href={cardHref(card) ?? href.home}
            title={card.title}
            sub={[card.year || undefined]}
            rating={card.rating}
            watch={titleBadge(card)}
          />
        {/each}
      </div>
    {/if}
  </div>
{/if}

<style>
  .overview {
    margin: 0;
    max-width: 65ch;
    font-size: var(--fs-lede-2);
    line-height: var(--lh-relaxed);
    color: var(--text-lede);
  }
  .heading {
    margin: 0.5rem 0 1.25rem;
  }
</style>
