<script lang="ts">
  import { api } from '../../lib/api';
  import { resource } from '../../lib/resource.svelte';
  import { app } from '../../lib/state.svelte';
  import RatingsSettings from '../../components/RatingsSettings.svelte';

  // Settings → Ratings: which rating cards show, and which a title's page shows, in what order.
  let { onkeys }: { onkeys?: () => void } = $props();

  const settings = resource(() => {
    void app.libraryVersion;
    return api.settings();
  });
</script>

<section aria-labelledby="ratings-h">
  <h2 id="ratings-h">Ratings</h2>
  <p class="muted lead">
    Scores from IMDb, Rotten Tomatoes, Metacritic, Letterboxd, Trakt and TMDB, beside each title. IMDb’s files are downloaded
    whole; MDBList is sent only a title’s IMDb or TMDB id, never your files or their names.
  </p>
  {#if settings.error}
    <p class="error">{settings.error}</p>
  {:else if !settings.data}
    <div class="skeleton block" aria-busy="true"></div>
  {:else if settings.data.ratingsPrimary === undefined}
    <!-- A server older than M10 has no ratings. -->
    <p class="muted">This server has no ratings.</p>
  {:else}
    <RatingsSettings settings={settings.data} {onkeys} />
  {/if}
</section>
