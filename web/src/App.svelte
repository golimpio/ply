<script lang="ts">
  import { onMount } from 'svelte';
  import { api, ApiError, getToken, onUnauthorized, setToken } from './lib/api';
  import { router } from './lib/router.svelte';
  import { app, attachToRunningScan } from './lib/state.svelte';
  import Header from './components/Header.svelte';
  import TokenPrompt from './components/TokenPrompt.svelte';
  import Item from './pages/Item.svelte';
  import Movies from './pages/Movies.svelte';
  import Play from './pages/Play.svelte';
  import Show from './pages/Show.svelte';
  import Shows from './pages/Shows.svelte';

  let phase = $state<'loading' | 'prompt' | 'ready' | 'error'>('loading');
  let message = $state('');
  let checking = $state(false);
  // Bumped after a successful unlock so every page remounts and refetches with the new token.
  let epoch = $state(0);

  const route = $derived(router.route);

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
    <button class="btn" onclick={boot}>Retry</button>
  </div>
{:else if phase === 'prompt'}
  <TokenPrompt serverName={app.serverName} error={message} busy={checking} onsave={unlock} />
{:else}
  {#if route.name !== 'play'}<Header />{/if}
  <main class="page">
    {#key epoch}
      {#if route.name === 'movies'}
        <Movies />
      {:else if route.name === 'shows'}
        <Shows />
      {:else if route.name === 'show'}
        {#key route.show}<Show show={route.show} />{/key}
      {:else if route.name === 'item'}
        {#key route.id}<Item id={route.id} />{/key}
      {:else if route.name === 'play'}
        {#key route.id}<Play id={route.id} startSec={route.startSec} />{/key}
      {:else}
        <h1>Not found</h1>
        <p><a href="#/movies">Back to the library</a></p>
      {/if}
    {/key}
  </main>
{/if}
