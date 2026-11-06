<script lang="ts">
  import { openSearch } from '../lib/palette.svelte';
  import { href, router } from '../lib/router.svelte';
  import { app, rescan, scan } from '../lib/state.svelte';
  import ActivityIndicator from './ActivityIndicator.svelte';
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
  <ActivityIndicator current={section === 'activity'} />
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
    z-index: var(--z-bar);
    height: calc(var(--bar-h) + env(safe-area-inset-top));
    display: flex;
    align-items: center;
    gap: 1.25rem;
    padding: env(safe-area-inset-top) var(--gutter) 0;
    background: linear-gradient(to bottom, var(--surface-bar-fade), transparent);
    transition:
      background var(--motion-bar),
      box-shadow var(--motion-bar);
  }
  .bar.scrolled {
    background: var(--surface-bar);
    /* Unprefixed only: the build adds -webkit- itself, and drops this one when both are written. */
    backdrop-filter: var(--blur-bar);
    box-shadow: var(--shadow-bar);
  }
  .brand {
    font-family: var(--font-display);
    font-weight: var(--fw-black);
    font-size: var(--fs-3xl);
    letter-spacing: var(--tracking-brand);
  }
  nav {
    display: flex;
    gap: 1.1rem;
  }
  nav a {
    color: var(--text-muted);
    font-weight: var(--fw-medium-2);
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
    color: var(--text-muted);
    font-size: var(--fs-xs-3);
  }
  .search {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    min-height: 2.5rem;
    padding: 0 0.6rem 0 0.85rem;
    font: inherit;
    font-size: var(--fs-sm-3);
    color: var(--text-muted);
    background: var(--fill-subtle);
    border: 1px solid var(--line-overlay);
    border-radius: var(--radius-pill);
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
    font-size: var(--fs-2xs-2);
    padding: 0.05rem 0.4rem;
    border: 1px solid var(--line-quiet);
    border-radius: var(--radius-kbd);
  }
  .spin {
    display: inline-flex;
  }
  .spin.on {
    animation: turn var(--motion-spin-slow) var(--ease-linear) infinite;
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
