<script lang="ts">
  import { api, ApiError, mergedInto, type Person } from '../lib/api';
  import { cardHref, groupCredits, lifeSpan } from '../lib/credits';
  import { backdropOf, posterImg, prefersPosters, profileImg } from '../lib/meta';
  import { resource } from '../lib/resource.svelte';
  import { href } from '../lib/router.svelte';
  import { openSearch } from '../lib/palette.svelte';
  import MediaCard from '../components/MediaCard.svelte';
  import Picture from '../components/Picture.svelte';

  let { id }: { id: number } = $props();

  // A person of TVDB that TMDB knows too is merged into the TMDB person the first time the page
  // is opened: that answer is the survivor's (another id), and after it the old id is a 404
  // {"error": "merged", "into": N}. Any other 404 is a person the library doesn't have.
  type Missing = { into: number };
  const detail = resource<Person | Missing>(() =>
    api.person(id).catch((e: unknown) => {
      if (e instanceof ApiError && e.status === 404) return { into: mergedInto(e) };
      throw e;
    }),
  );
  const person = $derived(detail.data && !('into' in detail.data) ? detail.data : undefined);
  const missing = $derived(detail.data && 'into' in detail.data ? detail.data : undefined);
  $effect(() => {
    // The page's address follows the merge, so that it can be kept and shared.
    const to = missing?.into || (person && person.id !== id ? person.id : 0);
    if (to && to !== id) location.replace(href.person(to));
  });

  // Only credits that can be opened: a film with a file, a series with a show.
  const credits = $derived((person?.credits ?? []).filter((c) => cardHref(c.title)));
  const sections = $derived(groupCredits(credits));
  // Most of a person's titles without a poster (no TMDB key) read better as wide cards.
  const wide = $derived(!prefersPosters([...new Map(credits.map((c) => [c.title.id, c.title])).values()]));

  const life = $derived(lifeSpan(person?.birthday, person?.deathday));
  const knownFor = $derived(person?.knownFor ? `Known for ${person.knownFor.toLowerCase()}` : '');

  // The biography stops at six lines until asked for more.
  let bioEl = $state<HTMLElement>();
  let expanded = $state(false);
  let clipped = $state(false);
  $effect(() => {
    const el = bioEl;
    if (!el) return;
    const measure = () => {
      if (!expanded) clipped = el.scrollHeight > el.clientHeight + 1;
    };
    measure();
    const watch = new ResizeObserver(measure);
    watch.observe(el);
    return () => watch.disconnect();
  });
</script>

{#if missing?.into}
  <div class="page center">
    <p>This person was merged into another.</p>
    <p><a class="btn" href={href.person(missing.into)}>Open their page</a></p>
  </div>
{:else if missing}
  <div class="page center">
    <p>This person isn’t in the library.</p>
    <p><button class="btn" onclick={openSearch}>Search for them</button></p>
  </div>
{:else if !person && detail.error}
  <div class="page center">
    <p class="error">{detail.error}</p>
    <p><a href={href.home}>Back to the library</a></p>
  </div>
{:else if !person}
  <div class="page" aria-busy="true">
    <div class="head">
      <div class="skeleton portrait sk-portrait"></div>
      <div class="who">
        <div class="skeleton sk-name"></div>
        <div class="skeleton sk-line"></div>
        <div class="skeleton sk-line short"></div>
      </div>
    </div>
    <div class="posters">
      {#each Array(6) as _, i (i)}<div class="skeleton sk-card"></div>{/each}
    </div>
  </div>
{:else}
  <div class="page">
    <header class="head">
      <div class="portrait">
        <Picture img={profileImg(person)} ratio="2 / 3" label={person.name} sizes="192px" eager />
      </div>
      <div class="who">
        <h1>{person.name}</h1>
        {#if life || person.placeOfBirth || knownFor}
          <div class="meta">
            {#if life}<span>{life}</span>{/if}
            {#if person.placeOfBirth}<span>{person.placeOfBirth}</span>{/if}
            {#if knownFor}<span>{knownFor}</span>{/if}
          </div>
        {/if}
        {#if person.biography}
          <div class="bio">
            <p id="bio" bind:this={bioEl} class:clamped={!expanded}>{person.biography}</p>
            {#if clipped}
              <button class="more link" aria-expanded={expanded} aria-controls="bio" onclick={() => (expanded = !expanded)}>
                {expanded ? 'Read less' : 'Read more'}
              </button>
            {/if}
          </div>
        {/if}
      </div>
    </header>

    <h2 class="library">In your library</h2>
    {#each sections as section (section.group)}
      <section class="group" aria-labelledby="group-{section.group}">
        <h3 id="group-{section.group}">{section.group}<span class="n">{section.entries.length}</span></h3>
        <div class={wide ? 'grid' : 'posters'}>
          {#each section.entries as { card, detail: role } (card.id)}
            <MediaCard
              shape={wide ? 'wide' : 'poster'}
              img={wide ? backdropOf(card) : posterImg(card)}
              href={cardHref(card) ?? href.home}
              title={card.title}
              sub={[card.year || undefined, role]}
            />
          {/each}
        </div>
      </section>
    {:else}
      <p class="muted">None of their work is in your library right now.</p>
    {/each}
  </div>
{/if}

<style>
  .head {
    display: flex;
    align-items: flex-start;
    gap: 1.5rem 2.5rem;
    margin-bottom: 2.75rem;
  }
  .portrait {
    flex: none;
    width: 12rem;
    border-radius: var(--radius);
    overflow: hidden;
    box-shadow: 0 0 0 1px rgb(255 255 255 / 0.06);
  }
  .portrait :global(.tile) {
    font-size: 2.2rem;
  }
  .who {
    min-width: 0;
    flex: 1;
    max-width: 46rem;
    display: grid;
    gap: 1rem;
    align-content: start;
  }
  .bio {
    display: grid;
    justify-items: start;
    gap: 0.5rem;
  }
  .bio p {
    margin: 0;
    max-width: 65ch;
    line-height: 1.6;
    white-space: pre-line;
  }
  .bio p.clamped {
    display: -webkit-box;
    -webkit-line-clamp: 6;
    line-clamp: 6;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
  .more {
    padding: 0;
    font: inherit;
    font-weight: 650;
    background: none;
    border: 0;
    cursor: pointer;
  }
  .library {
    margin-bottom: 1.5rem;
  }
  .group + .group {
    margin-top: 2.5rem;
  }
  h3 {
    margin-bottom: 1rem;
    font-size: 1.05rem;
    font-weight: 650;
  }
  .n {
    margin-left: 0.6rem;
    font-weight: 400;
    color: var(--muted);
    font-variant-numeric: tabular-nums;
  }
  .posters {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(min(170px, 100%), 1fr));
    gap: 1.75rem 1.25rem;
  }

  .sk-portrait {
    aspect-ratio: 2 / 3;
  }
  .sk-name {
    height: 2.6rem;
    width: min(18rem, 80%);
  }
  .sk-line {
    height: 1.1rem;
    max-width: 40rem;
  }
  .sk-line.short {
    width: 60%;
  }
  .sk-card {
    aspect-ratio: 2 / 3;
  }

  @media (max-width: 700px) {
    .head {
      flex-direction: column;
      margin-bottom: 2.25rem;
    }
    .portrait {
      width: 9.5rem;
    }
    .who {
      width: 100%;
    }
    .posters {
      grid-template-columns: repeat(auto-fill, minmax(min(140px, 100%), 1fr));
      gap: 1.25rem 0.75rem;
    }
  }
</style>
