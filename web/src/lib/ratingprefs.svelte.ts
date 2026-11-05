// Which rating sources the detail pages show, and in which order (Settings → Ratings), read once
// per library version from GET /api/settings. Until it has come, or when it can't be read (an
// older server, a failure), `shown` is null and every rating is shown in the server's order.
import { api, type RatingSource, type Settings } from './api';
import { app } from './state.svelte';

export const ratingPrefs = $state<{ shown: RatingSource[] | null }>({ shown: null });

let fetchedVersion = -1;
let inflight: { version: number; promise: Promise<void> } | null = null;

export function ensureRatingPrefs(): Promise<void> {
  const version = app.libraryVersion;
  if (fetchedVersion === version) return Promise.resolve();
  if (inflight?.version === version) return inflight.promise;
  const promise: Promise<void> = api
    .settings()
    .then((s) => {
      if (version >= fetchedVersion) applyRatingPrefs(s); // an older answer landing late is dropped
    })
    .catch(() => {
      // not asked again until the library changes
    })
    .finally(() => {
      fetchedVersion = Math.max(fetchedVersion, version);
      if (inflight?.promise === promise) inflight = null;
    });
  inflight = { version, promise };
  return promise;
}

/** What a settings answer says (the Settings page calls it after a save). */
export function applyRatingPrefs(s: Pick<Settings, 'ratingsShown'>): void {
  ratingPrefs.shown = s.ratingsShown ? [...s.ratingsShown] : null;
}
