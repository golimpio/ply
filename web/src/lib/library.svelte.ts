// One shared copy of the whole library for the pages that need all of it
// (home rails, search, show artwork, instant detail heroes). Refetched after
// every scan; a list of ~10k summaries is a few MB and one request.
import { api, type ItemSummary } from './api';
import { app } from './state.svelte';

export interface ShowEntry {
  show: string;
  /** First episode by season/episode: its artwork stands in for the show's. */
  cover: ItemSummary;
  seasons: number[];
  episodeCount: number;
  latestAddedAt: number;
}

export const library = $state({
  items: [] as ItemSummary[],
  loaded: false,
  error: '',
});

let fetchedVersion = -1;
let inflight: Promise<void> | null = null;

/** Load (or refresh after a scan) the full item list. Safe to call often. */
export function ensureLibrary(): Promise<void> {
  if (fetchedVersion === app.libraryVersion && library.loaded) return Promise.resolve();
  if (inflight) return inflight;
  const version = app.libraryVersion;
  inflight = api
    .items()
    .then((items) => {
      library.items = items;
      library.error = '';
      library.loaded = true;
      fetchedVersion = version;
    })
    .catch((e: unknown) => {
      library.error = e instanceof Error ? e.message : String(e);
    })
    .finally(() => {
      inflight = null;
    });
  return inflight;
}

export function findItem(id: number): ItemSummary | undefined {
  return library.items.find((i) => i.id === id);
}

export function showsOf(items: ItemSummary[]): ShowEntry[] {
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
    .map(({ seasonSet, ...s }) => ({ ...s, seasons: [...seasonSet].sort((a, b) => a - b) }))
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
