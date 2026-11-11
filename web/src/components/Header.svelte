<script lang="ts">
  import { episodes, movies, showsOf } from '../lib/library.svelte';
  import { groupByTitle } from '../lib/meta';
  import { openSearch } from '../lib/palette.svelte';
  import { href, router } from '../lib/router.svelte';
  import { app, rescan, scan } from '../lib/state.svelte';
  import { theme, toggleTheme } from '../lib/theme.svelte';
  import ActivityIndicator from './ActivityIndicator.svelte';
  import Icon from './Icon.svelte';

  // Pinned at the top at all times; frosted once the page scrolls under it.
  let scrolled = $state(false);
  const section = $derived(router.route.name === 'show' ? 'shows' : router.route.name);
  const mac = /Mac|iPhone|iPad/.test(navigator.platform);

  const movieCount = $derived(movies.loaded ? groupByTitle([...movies.rows]).length : 0);
  const showCount = $derived(episodes.loaded ? showsOf(episodes.rows).length : 0);
  const totalCount = $derived(movieCount + showCount);
</script>

<svelte:window onscroll={() => (scrolled = window.scrollY > 8)} />

<header class="bar" class:scrolled>
  <a class="brand" href={href.home} aria-label="ply home">Ply</a>
  <nav aria-label="Library">
    <a href={href.home} aria-current={section === 'home' ? 'page' : undefined}>
      Programme{#if totalCount}<span class="nav-count tabular">{totalCount}</span>{/if}
    </a>
    <a href={href.movies} aria-current={section === 'movies' ? 'page' : undefined}>
      Movies{#if movieCount}<span class="nav-count tabular">{movieCount}</span>{/if}
    </a>
    <a href={href.shows} aria-current={section === 'shows' ? 'page' : undefined}>
      Shows{#if showCount}<span class="nav-count tabular">{showCount}</span>{/if}
    </a>
  </nav>
  <div class="grow"></div>
  {#if scan.message}<span class="status" role="status">{scan.message}</span>{/if}
  <button class="search" onclick={openSearch} aria-label="Search the library" aria-keyshortcuts={mac ? 'Meta+K' : 'Control+K'}>
    <Icon name="search" />
    <span class="hint">Search title, director, year…</span>
    <kbd>{mac ? '⌘' : 'Ctrl'} K</kbd>
  </button>
  <button
    class="theme-pill"
    type="button"
    onclick={toggleTheme}
    aria-label={theme.current === 'dark' ? 'Switch to paper light theme' : 'Switch to cinema dark theme'}
    title={theme.current === 'dark' ? 'Switch to Paper Light' : 'Switch to Cinema Dark'}
  >
    <span aria-hidden="true">{theme.current === 'dark' ? '☀' : '☾'}</span>
    <span class="theme-label">{theme.current === 'dark' ? 'Paper Light' : 'Cinema Dark'}</span>
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
    gap: 1.15rem;
    padding: env(safe-area-inset-top) var(--gutter) 0;
    background: var(--surface-bar);
    border-bottom: 1px solid var(--line);
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
    font-style: italic;
    font-weight: var(--fw-regular);
    font-size: var(--fs-3xl-2);
    letter-spacing: var(--tracking-brand);
    line-height: var(--lh-none);
    margin-right: 0.5rem;
  }
  nav {
    display: flex;
    align-items: baseline;
    gap: 1.5rem;
  }
  nav a {
    display: inline-flex;
    align-items: baseline;
    gap: 0.35rem;
    color: var(--text-muted);
    font-size: var(--fs-sm-3);
    font-weight: var(--fw-medium);
    padding: 0.35rem 0;
    border-bottom: 2px solid transparent;
    transition: color var(--motion-hover);
  }
  nav a:hover {
    color: var(--text);
  }
  nav a[aria-current='page'] {
    color: var(--text);
    border-bottom-color: var(--accent);
  }
  .nav-count {
    font-family: var(--font-mono);
    font-size: var(--fs-2xs-2);
    color: var(--text-faint);
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
    gap: 0.55rem;
    min-height: 2.25rem;
    padding: 0 0.65rem 0 0.9rem;
    font: inherit;
    font-size: var(--fs-xs-3);
    color: var(--text-muted);
    background: var(--fill-subtle);
    border: 1px solid var(--line-overlay);
    border-radius: var(--radius-pill);
    cursor: pointer;
    transition:
      color var(--motion-hover),
      border-color var(--motion-hover);
  }
  .search:hover {
    color: var(--text);
    border-color: var(--line-quiet);
  }
  .search :global(svg) {
    width: 1rem;
    height: 1rem;
  }
  kbd {
    font-family: var(--font-mono);
    font-size: var(--fs-2xs);
    padding: 0.05rem 0.4rem;
    border: 1px solid var(--line-quiet);
    border-radius: var(--radius-kbd);
  }
  .theme-pill {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    min-height: 2.15rem;
    padding: 0 0.75rem;
    font: inherit;
    font-size: var(--fs-xs);
    color: var(--text-muted);
    background: var(--fill-subtle);
    border: 1px solid var(--line-overlay);
    border-radius: var(--radius-pill);
    cursor: pointer;
    transition:
      color var(--motion-hover),
      background var(--motion-hover);
  }
  .theme-pill:hover {
    color: var(--text);
    background: var(--fill-hover);
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
  @media (max-width: 900px) {
    .hint,
    .theme-label {
      display: none;
    }
  }
  @media (max-width: 600px) {
    nav,
    .search,
    .status,
    .theme-pill {
      display: none; /* the tab bar carries navigation and search on phones */
    }
  }
</style>
