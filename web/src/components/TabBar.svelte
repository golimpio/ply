<script lang="ts">
  import { openSearch } from '../lib/palette.svelte';
  import { href, router } from '../lib/router.svelte';
  import Icon from './Icon.svelte';

  const section = $derived(router.route.name === 'show' ? 'shows' : router.route.name);
</script>

<nav class="tabs" aria-label="Library">
  <a href={href.home} aria-current={section === 'home' ? 'page' : undefined}><Icon name="home" />Home</a>
  <a href={href.movies} aria-current={section === 'movies' ? 'page' : undefined}><Icon name="film" />Movies</a>
  <a href={href.shows} aria-current={section === 'shows' ? 'page' : undefined}><Icon name="tv" />Shows</a>
  <button onclick={openSearch}><Icon name="search" />Search</button>
</nav>

<style>
  .tabs {
    display: none;
  }
  @media (max-width: 600px) {
    .tabs {
      position: fixed;
      inset: auto 0 0;
      z-index: 20;
      display: grid;
      grid-template-columns: repeat(4, 1fr);
      padding: 0.35rem 0.5rem calc(0.35rem + env(safe-area-inset-bottom));
      background: rgb(16 14 12 / 0.86);
      backdrop-filter: saturate(1.4) blur(18px);
      -webkit-backdrop-filter: saturate(1.4) blur(18px);
      box-shadow: 0 -1px 0 rgb(255 255 255 / 0.06);
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
    font-size: 0.7rem;
    font-weight: 550;
    color: var(--muted);
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
