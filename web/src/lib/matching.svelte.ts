import { api } from './api';
import { isMatching } from './meta';
import { resource } from './resource.svelte';
import { app } from './state.svelte';

/**
 * The matching status (GET /api/metadata/status) for a tab of Settings that shows it. With `poll`
 * (Library and Details, which show how matching goes), while matching runs it is asked for again
 * every few seconds, and when it settles the library version goes up once, so the tab (its review
 * list, its settings) and every page fetch what changed. Without (API keys, which shows only what
 * the server says of a key), it is asked for once. `refresh` asks for it again at once: a Test that
 * works clears the server's "TVDB rejected the key". Call it while a component initialises.
 */
export function matchingStatus({ poll }: { poll: boolean }) {
  let pollTick = $state(0);
  const status = resource(() => {
    void app.libraryVersion;
    void pollTick;
    return api.metadataStatus();
  });
  const matching = $derived(isMatching(status.data));
  let wasMatching = false;
  $effect(() => {
    if (!poll) return;
    if (!matching) {
      if (wasMatching) app.libraryVersion++; // done: posters, the review list and counts changed
      wasMatching = false;
      return;
    }
    wasMatching = true;
    const t = setInterval(() => pollTick++, 3000);
    return () => clearInterval(t);
  });
  return {
    get data() {
      return status.data;
    },
    get error() {
      return status.error;
    },
    get matching() {
      return matching;
    },
    refresh: () => pollTick++,
  };
}
