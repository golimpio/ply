<script lang="ts">
  import { openSearch } from '../lib/palette.svelte';
  import { href, router } from '../lib/router.svelte';
  import Icon from './Icon.svelte';

  const section = $derived(router.route.name === 'show' ? 'shows' : router.route.name);
</script>

<nav class="tabs" aria-label="Library">
  <a href={href.home} aria-current={section === 'home' ? 'page' : undefined}><Icon name="home" />Programme</a>
  <a href={href.movies} aria-current={section === 'movies' ? 'page' : undefined}><Icon name="film" />Films</a>
  <a href={href.shows} aria-current={section === 'shows' ? 'page' : undefined}><Icon name="tv" />Series</a>
  <a href={href.collections} aria-current={section === 'collections' ? 'page' : undefined}><Icon name="layers" />Collections</a>
  <button onclick={openSearch}><Icon name="search" />Search</button>
  <a href={href.settings} aria-current={section === 'settings' ? 'page' : undefined}><Icon name="settings" />Settings</a>
</nav>

<style>
  .tabs {
    display: none;
  }
  @media (max-width: 600px) {
    .tabs {
      position: fixed;
      inset: auto 0 0;
      z-index: var(--z-bar);
      display: grid;
      grid-template-columns: repeat(6, 1fr);
      padding: var(--space-0350) var(--space-0500) calc(var(--space-0350) + env(safe-area-inset-bottom));
      background: var(--surface-tabbar);
      /* Unprefixed only: the build adds -webkit- itself, and drops this one when both are written. */
      backdrop-filter: var(--blur-bar);
      box-shadow: var(--shadow-tabbar);
    }
  }
  a,
  button {
    display: grid;
    justify-items: center;
    gap: var(--space-0150);
    min-height: 44px;
    padding: var(--space-0300) 0;
    font: inherit;
    font-size: var(--fs-3xs);
    font-weight: var(--fw-medium-2);
    color: var(--text-muted);
    background: none;
    border: 0;
    cursor: pointer;
  }
  a :global(svg),
  button :global(svg) {
    width: 1.4rem;
    height: 1.4rem;
  }
  a[aria-current='page'] {
    color: var(--text);
  }
</style>
