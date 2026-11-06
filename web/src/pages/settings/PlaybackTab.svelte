<script lang="ts">
  import { api } from '../../lib/api';
  import { resource } from '../../lib/resource.svelte';
  import { app } from '../../lib/state.svelte';
  import PlaybackLimits from '../../components/PlaybackLimits.svelte';

  // Settings → Playback: how many videos may play, and convert, at once.
  const settings = resource(() => {
    void app.libraryVersion;
    return api.settings();
  });
</script>

{#if settings.error}
  <p class="error">{settings.error}</p>
{:else if settings.data && settings.data.maxStreams === undefined}
  <!-- A server older than M9 has no limits: no empty Playback heading. -->
  <p class="muted">This server has no playback limits.</p>
{:else}
  <section aria-labelledby="playback-h">
    <h2 id="playback-h">Playback</h2>
    <p class="muted lead">
      Playing always comes first: background work waits while something plays. When a limit is reached, a new video
      waits and shows what is playing, so that one can be stopped to make room.
    </p>
    {#if settings.data}
      <PlaybackLimits settings={settings.data} />
    {:else}
      <div class="skeleton block" aria-busy="true"></div>
    {/if}
  </section>
{/if}
