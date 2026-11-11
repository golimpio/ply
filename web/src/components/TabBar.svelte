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
      grid-template-columns: repeat(5, 1fr);
      padding: 0.35rem 0.5rem calc(0.35rem + env(safe-area-inset-bottom));
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
    gap: 0.15rem;
    min-height: 44px;
    padding: 0.3rem 0;
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
