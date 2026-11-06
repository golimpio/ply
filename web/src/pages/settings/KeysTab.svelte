<script lang="ts">
  import { api } from '../../lib/api';
  import { matchingStatus } from '../../lib/matching.svelte';
  import { resource } from '../../lib/resource.svelte';
  import { app } from '../../lib/state.svelte';
  import MdblistKey from '../../components/MdblistKey.svelte';
  import TmdbKey from '../../components/TmdbKey.svelte';
  import TvdbKey from '../../components/TvdbKey.svelte';

  // Settings → API keys: TMDB and TVDB for details, MDBList for ratings, each with its notice. A key
  // is checked before it is stored and never shown back.
  const settings = resource(() => {
    void app.libraryVersion;
    return api.settings();
  });
  // For what the server says of the keys ("TVDB rejected the key or PIN"), asked for once and again
  // after a Test that works, which clears it. Matching itself is followed in Library and Details.
  const status = matchingStatus({ poll: false });
  const problem = $derived.by(() => {
    const m = status.data;
    if (!m?.enabled) return { shows: '', error: '' };
    return { shows: m.showsLine && !m.showsLine.startsWith('Shows:') ? m.showsLine : '', error: m.lastError };
  });
</script>

<section aria-labelledby="keys-h">
  <h2 id="keys-h">API keys</h2>
  <p class="muted lead">
    The services ply asks for details and ratings. Each key is checked before it is stored, and is never shown again: a key
    that is set shows as dots, and what you paste replaces it.
  </p>
  {#if problem.shows || problem.error}
    <div class="key-status" role="status">
      {#if problem.shows}<p class="shows-line warn">{problem.shows}</p>{/if}
      {#if problem.error}<p class="error">{problem.error}</p>{/if}
    </div>
  {/if}

  {#if settings.error}
    <p class="error">{settings.error}</p>
  {:else if !settings.data}
    <div class="skeleton block" aria-busy="true"></div>
  {:else}
    {@const s = settings.data}
    <TmdbKey settings={s} onworks={status.refresh} />
    <!-- Not from a server older than M8, which knows nothing of TVDB. -->
    {#if s.tvdbKeySet !== undefined}<TvdbKey settings={s} onworks={status.refresh} />{/if}
    <!-- Nor older than M10, which has no ratings. -->
    {#if s.mdblistKeySet !== undefined}<MdblistKey settings={s} />{/if}
  {/if}
</section>

<style>
  .key-status {
    display: grid;
    gap: 0.3rem;
  }
  .key-status p {
    margin: 0;
  }
</style>
