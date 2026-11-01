<script lang="ts">
  import { href, router } from '../lib/router.svelte';
  import { app, rescan, scan } from '../lib/state.svelte';

  const section = $derived(router.route.name === 'show' ? 'shows' : router.route.name);
</script>

<header class="bar">
  <a class="brand" href={href.movies}>ply</a>
  <nav>
    <a href={href.movies} class:active={section === 'movies'}>Movies</a>
    <a href={href.shows} class:active={section === 'shows'}>Shows</a>
  </nav>
  <div class="grow"></div>
  {#if scan.message}<span class="status" role="status">{scan.message}</span>{/if}
  <button class="btn" onclick={rescan} disabled={scan.running} title={app.serverName}>
    {scan.running ? 'Scanning…' : 'Rescan'}
  </button>
</header>

<style>
  .bar {
    position: sticky;
    top: 0;
    z-index: 10;
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 0.5rem 1rem;
    padding: 0.6rem 1rem;
    padding-top: max(0.6rem, env(safe-area-inset-top));
    background: rgb(14 17 22 / 0.92);
    backdrop-filter: blur(8px);
    border-bottom: 1px solid var(--line);
  }
  .brand {
    font-weight: 800;
    font-size: 1.25rem;
    letter-spacing: 0.02em;
    color: var(--text);
  }
  .brand:hover {
    text-decoration: none;
  }
  nav {
    display: flex;
    gap: 0.25rem;
  }
  nav a {
    padding: 0.35rem 0.7rem;
    border-radius: 999px;
    color: var(--muted);
  }
  nav a:hover {
    text-decoration: none;
    color: var(--text);
  }
  nav a.active {
    background: var(--bg-3);
    color: var(--text);
  }
  .grow {
    flex: 1;
  }
  .status {
    color: var(--muted);
    font-size: 0.85rem;
  }
</style>
