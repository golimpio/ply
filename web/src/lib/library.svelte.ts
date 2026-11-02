// One shared copy of the whole library for the pages that need all of it
// (home rails, search, show artwork, instant detail heroes). Refetched after
// every scan; a list of ~10k summaries is a few MB and one request.
import { api, type ItemSummary, type Progress, type ShowSummary } from './api';
import { app } from './state.svelte';

export interface ShowEntry {
  show: string;
  /** First episode by season/episode: its artwork stands in for the show's. */
  cover: ItemSummary;
  seasons: number[];
  episodeCount: number;
  latestAddedAt: number;
  /** The show's metadata (poster, backdrop, overview) from /api/shows, when there is any. */
  meta?: ShowSummary;
}

export const library = $state({
  items: [] as ItemSummary[],
  /** /api/shows: one row per show, with its metadata. */
  shows: [] as ShowSummary[],
  loaded: false,
  error: '',
});

let fetchedVersion = -1;
let inflight: { version: number; promise: Promise<void> } | null = null;

/**
 * Load (or refresh after a scan) the full item list. Safe to call often. A
 * fetch started before a scan finished is not reused for the newer library
 * version, and an older response landing late never overwrites a newer one.
 */
export function ensureLibrary(): Promise<void> {
  const version = app.libraryVersion;
  if (fetchedVersion === version && library.loaded) return Promise.resolve();
  if (inflight?.version === version) return inflight.promise;
  // The show list only adds metadata to what the items say: if it fails, the
  // shows are still there (from their episodes), without posters.
  const promise: Promise<void> = Promise.all([api.items(), api.shows().catch(() => [] as ShowSummary[])])
    .then(([items, shows]) => {
      if (version < fetchedVersion) return; // a newer fetch already landed
      library.items = items;
      library.shows = shows;
      library.error = '';
      library.loaded = true;
      fetchedVersion = version;
    })
    .catch((e: unknown) => {
      if (version >= fetchedVersion) library.error = e instanceof Error ? e.message : String(e);
    })
    .finally(() => {
      if (inflight?.promise === promise) inflight = null;
    });
  inflight = { version, promise };
  return promise;
}

export function findItem(id: number): ItemSummary | undefined {
  return library.items.find((i) => i.id === id);
}

/** Reflect a progress report in the cached list, so cards and heroes update without a refetch. */
export function applyProgress(id: number, progress: Progress | null): void {
  const item = findItem(id);
  if (item) item.progress = progress;
}

/** The metadata row of a show, if the server sent one. */
export function showMeta(name: string): ShowSummary | undefined {
  return library.shows.find((s) => s.show === name);
}

/**
 * The shows of items, from their episodes; `shows` (from /api/shows) adds
 * each one's metadata.
 */
export function showsOf(items: ItemSummary[], shows: ShowSummary[] = library.shows): ShowEntry[] {
  const meta = new Map(shows.map((s) => [s.show, s]));
  const byShow = new Map<string, ShowEntry & { seasonSet: Set<number> }>();
  for (const ep of items) {
    if (ep.kind !== 'episode' || !ep.show) continue;
    let s = byShow.get(ep.show);
    if (!s) {
      s = { show: ep.show, cover: ep, seasons: [], episodeCount: 0, latestAddedAt: 0, seasonSet: new Set() };
      byShow.set(ep.show, s);
    }
    s.episodeCount++;
    s.seasonSet.add(ep.season ?? 0);
    s.latestAddedAt = Math.max(s.latestAddedAt, ep.addedAt);
    if (episodeOrder(ep) < episodeOrder(s.cover)) s.cover = ep;
  }
  return [...byShow.values()]
    .map(({ seasonSet, ...s }) => ({ ...s, seasons: [...seasonSet].sort((a, b) => a - b), meta: meta.get(s.show) }))
    .sort((a, b) => a.show.localeCompare(b.show));
}

function episodeOrder(ep: ItemSummary): number {
  // Specials (season 0) sort after the regular seasons for cover purposes.
  const season = ep.season || 1000;
  return season * 10000 + (ep.episode ?? 0);
}

export const byTitle = (a: ItemSummary, b: ItemSummary) =>
  a.title.localeCompare(b.title, undefined, { numeric: true, sensitivity: 'base' });
export const byAdded = (a: ItemSummary, b: ItemSummary) => b.addedAt - a.addedAt;
