<script lang="ts">
  import { openSearch } from '../lib/palette.svelte';
  import { href, router } from '../lib/router.svelte';
  import { app, rescan, scan } from '../lib/state.svelte';
  import Icon from './Icon.svelte';

  // Clear over a hero, frosted once the page scrolls under it.
  let scrolled = $state(false);
  const section = $derived(router.route.name === 'show' ? 'shows' : router.route.name);
  const mac = /Mac|iPhone|iPad/.test(navigator.platform);
</script>

<svelte:window onscroll={() => (scrolled = window.scrollY > 8)} />

<header class="bar" class:scrolled>
  <a class="brand" href={href.home} aria-label="ply home">ply</a>
  <nav aria-label="Library">
    <a href={href.home} aria-current={section === 'home' ? 'page' : undefined}>Home</a>
    <a href={href.movies} aria-current={section === 'movies' ? 'page' : undefined}>Movies</a>
    <a href={href.shows} aria-current={section === 'shows' ? 'page' : undefined}>Shows</a>
  </nav>
  <div class="grow"></div>
  {#if scan.message}<span class="status" role="status">{scan.message}</span>{/if}
  <button class="search" onclick={openSearch} aria-label="Search the library" aria-keyshortcuts={mac ? 'Meta+K' : 'Control+K'}>
    <Icon name="search" />
    <span class="hint">Search</span>
    <kbd>{mac ? '⌘' : 'Ctrl'} K</kbd>
  </button>
  <button
    class="btn icon"
    onclick={rescan}
    disabled={scan.running}
    aria-label={scan.running ? 'Scanning the library' : 'Rescan the library'}
    title={app.serverName ? `Rescan ${app.serverName}` : 'Rescan'}
  >
    <span class="spin" class:on={scan.running}><Icon name="rescan" /></span>
  </button>
  <a
    class="btn icon"
    href={href.settings}
    aria-label="Settings"
    title="Settings"
    aria-current={section === 'settings' ? 'page' : undefined}
  >
    <Icon name="settings" />
  </a>
</header>

<style>
  .bar {
    position: fixed;
    inset: 0 0 auto;
    z-index: 20;
    height: calc(var(--bar-h) + env(safe-area-inset-top));
    display: flex;
    align-items: center;
    gap: 1.25rem;
    padding: env(safe-area-inset-top) var(--gutter) 0;
    background: linear-gradient(to bottom, rgb(16 14 12 / 0.7), transparent);
    transition:
      background 0.25s,
      box-shadow 0.25s;
  }
  .bar.scrolled {
    background: rgb(16 14 12 / 0.78);
    backdrop-filter: saturate(1.4) blur(18px);
    -webkit-backdrop-filter: saturate(1.4) blur(18px);
    box-shadow: 0 1px 0 rgb(255 255 255 / 0.06);
  }
  .brand {
    font-weight: 850;
    font-size: 1.45rem;
    letter-spacing: -0.04em;
  }
  nav {
    display: flex;
    gap: 1.1rem;
  }
  nav a {
    color: var(--muted);
    font-weight: 550;
    padding: 0.35rem 0;
    border-bottom: 2px solid transparent;
  }
  nav a:hover {
    color: var(--text);
  }
  nav a[aria-current='page'] {
    color: var(--text);
    border-bottom-color: var(--tint);
  }
  .grow {
    flex: 1;
  }
  .status {
    color: var(--muted);
    font-size: 0.85rem;
  }
  .search {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    min-height: 2.5rem;
    padding: 0 0.6rem 0 0.85rem;
    font: inherit;
    font-size: 0.9rem;
    color: var(--muted);
    background: rgb(255 255 255 / 0.07);
    border: 1px solid rgb(255 255 255 / 0.1);
    border-radius: 999px;
    cursor: pointer;
  }
  .search:hover {
    color: var(--text);
  }
  .search :global(svg) {
    width: 1.05rem;
    height: 1.05rem;
  }
  kbd {
    font: inherit;
    font-size: 0.75rem;
    padding: 0.05rem 0.4rem;
    border: 1px solid rgb(255 255 255 / 0.14);
    border-radius: 5px;
  }
  .spin {
    display: inline-flex;
  }
  .spin.on {
    animation: turn 1s linear infinite;
  }
  @keyframes turn {
    to {
      transform: rotate(360deg);
    }
  }
  @media (max-width: 600px) {
    nav,
    .search,
    .status {
      display: none; /* the tab bar carries navigation and search on phones */
    }
  }
</style>
