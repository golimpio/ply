<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import { openActivity } from './lib/activity.svelte';
  import { api, ApiError, getToken, onUnauthorized, setToken } from './lib/api';
  import { ambient } from './lib/ambient.svelte';
  import { router } from './lib/router.svelte';
  import { app, attachToRunningScan } from './lib/state.svelte';
  import Header from './components/Header.svelte';
  import SearchPalette from './components/SearchPalette.svelte';
  import TabBar from './components/TabBar.svelte';
  import TokenPrompt from './components/TokenPrompt.svelte';
  import Activity from './pages/Activity.svelte';
  import Collection from './pages/Collection.svelte';
  import Genre from './pages/Genre.svelte';
  import Home from './pages/Home.svelte';
  import Item from './pages/Item.svelte';
  import Movies from './pages/Movies.svelte';
  import Person from './pages/Person.svelte';
  import Play from './pages/Play.svelte';
  import Settings from './pages/Settings.svelte';
  import Show from './pages/Show.svelte';
  import Shows from './pages/Shows.svelte';

  let phase = $state<'loading' | 'prompt' | 'ready' | 'error'>('loading');
  let message = $state('');
  let checking = $state(false);
  // Bumped after a successful unlock so every page remounts and refetches with the new token.
  let epoch = $state(0);

  const route = $derived(router.route);
  const playing = $derived(route.name === 'play');

  // Projection light: the current film's colour, eased in and out by CSS.
  $effect(() => {
    document.documentElement.style.setProperty('--tint', ambient.color || 'var(--accent)');
    document.documentElement.toggleAttribute('data-lit', !!ambient.color);
  });

  // The background queue's live feed, for the header's indicator and the Activity page. Closed while
  // a video plays: nothing shows it then, and playback comes first.
  $effect(() => {
    if (phase === 'ready' && !playing) return untrack(openActivity);
  });

  onUnauthorized(() => {
    phase = 'prompt';
    message = 'That token was not accepted.';
  });

  async function boot() {
    phase = 'loading';
    try {
      const info = await api.info();
      app.serverName = info.name;
      document.title = info.name ? `${info.name} · ply` : 'ply';
      if (info.authRequired) {
        if (!getToken()) {
          message = '';
          phase = 'prompt';
          return;
        }
        await api.scanStatus(); // any authenticated endpoint; 401 flips to the prompt via onUnauthorized
      }
      phase = 'ready';
      void attachToRunningScan();
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) return;
      message = e instanceof Error ? e.message : String(e);
      phase = 'error';
    }
  }

  async function unlock(token: string) {
    checking = true;
    setToken(token);
    try {
      await api.scanStatus();
      epoch++;
      phase = 'ready';
      void attachToRunningScan();
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) message = e instanceof Error ? e.message : String(e);
    } finally {
      checking = false;
    }
  }

  onMount(boot);
</script>

{#if phase === 'loading'}
  <p class="center muted">Connecting…</p>
{:else if phase === 'error'}
  <div class="center">
    <p class="error">{message}</p>
    <button class="btn" onclick={boot}>Try again</button>
  </div>
{:else if phase === 'prompt'}
  <TokenPrompt serverName={app.serverName} error={message} busy={checking} onsave={unlock} />
{:else}
  <div class="light" aria-hidden="true"></div>
  {#if !playing}<Header />{/if}
  <main>
    {#key epoch}
      {#if route.name === 'home'}
        <Home />
      {:else if route.name === 'movies'}
        <Movies view={route} />
      {:else if route.name === 'shows'}
        <Shows view={route} />
      {:else if route.name === 'show'}
        {#key route.show}<Show show={route.show} season={route.season} />{/key}
      {:else if route.name === 'item'}
        {#key route.id}<Item id={route.id} />{/key}
      {:else if route.name === 'play'}
        <div class="play-page">
          {#key route.id}<Play id={route.id} startSec={route.startSec} />{/key}
        </div>
      {:else if route.name === 'person'}
        {#key route.id}<Person id={route.id} />{/key}
      {:else if route.name === 'genre'}
        {#key route.id}<Genre id={route.id} />{/key}
      {:else if route.name === 'collection'}
        {#key route.id}<Collection id={route.id} />{/key}
      {:else if route.name === 'settings'}
        <Settings />
      {:else if route.name === 'activity'}
        <Activity />
      {:else}
        <div class="page center">
          <h1>This page doesn’t exist</h1>
          <p><a href="#/">Go to the library</a></p>
        </div>
      {/if}
    {/key}
  </main>
  {#if !playing}<TabBar />{/if}
  <SearchPalette />
{/if}

<style>
  /* The light thrown by the screen: a soft wash of the current film's colour
     from the top of the room, fading out before the content. */
  .light {
    position: fixed;
    inset: 0 0 auto;
    height: 90vh;
    z-index: -1;
    pointer-events: none;
    background: radial-gradient(120% 70% at 30% 0%, var(--tint), transparent 70%);
    opacity: 0.12;
    transition: opacity 0.8s var(--ease);
  }
  :global(html[data-lit]) .light {
    opacity: 0.3;
  }
  /* The player page has no header bar above it. */
  .play-page {
    padding: max(1rem, env(safe-area-inset-top)) var(--gutter) 2rem;
  }
</style>
