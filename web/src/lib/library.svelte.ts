// One shared copy of the whole library for the pages that need all of it
// (home rails, search, show artwork, instant detail heroes). Refetched after
// every scan; a list of ~10k summaries is a few MB and one request.
import { untrack } from 'svelte';
import { api, type ItemSummary, type ItemWatch, type Progress, type ShowSummary, type TitleCard, type WatchResult } from './api';
import { app } from './state.svelte';
import { countsBadge, itemBadge, showCounts, type EpisodeCounts, type WatchBadge } from './watched';

export interface ShowEntry {
  show: string;
  /** First episode by season/episode: its artwork stands in for the show's. */
  cover: ItemSummary;
  seasons: number[];
  episodeCount: number;
  /** How far through its episodes, from the cached items (lib/watched showCounts). */
  counts: EpisodeCounts;
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
  return indexed().byId.get(id);
}

// The cached list by id and by show, built once per fetched list (a mark changes no file's id or
// show). Built untracked: a page that looks one item up depends on that item, not on all of them.
let index: { items: ItemSummary[]; byId: Map<number, ItemSummary>; byShow: Map<string, ItemSummary[]> } | undefined;
function indexed() {
  const items = library.items;
  if (index?.items !== items) {
    index = untrack(() => {
      const byId = new Map<number, ItemSummary>();
      const byShow = new Map<string, ItemSummary[]>();
      for (const i of items) {
        byId.set(i.id, i);
        if (i.kind !== 'episode' || !i.show) continue;
        const list = byShow.get(i.show);
        if (list) list.push(i);
        else byShow.set(i.show, [i]);
      }
      return { items, byId, byShow };
    });
  }
  return index;
}

/** The episode files of a show in the cached list. */
export function episodesOf(show: string): ItemSummary[] {
  return indexed().byShow.get(show) ?? [];
}

const now = () => Math.floor(Date.now() / 1000);

// Marks can overlap (a season marked while one of its episodes is unmarked, a toggle pressed again
// before the first answer). Each change stamps the files it touches with a new generation; an answer
// or a rollback only writes the files whose last change is still its own.
let generation = 0;
const stamps = new Map<number, number>();
function stamp(ids: Iterable<number>): number {
  const g = ++generation;
  for (const id of ids) stamps.set(id, g);
  return g;
}
const mine = (id: number, g: number) => (stamps.get(id) ?? 0) <= g;

// Title cards (Genre, Collection, Person) carry the server's counts of a series, over every show
// linked to it. A show changed here since the list was fetched has its counts from before the first
// change kept, and a card adds what changed since (titleBadge).
let touchedIn: ItemSummary[] | null = null; // the list `touched` is about
const touched = $state<{ before: Record<string, EpisodeCounts> }>({ before: {} });
function touch(files: readonly ItemSummary[]): void {
  if (touchedIn !== library.items) {
    touchedIn = library.items;
    touched.before = {};
  }
  for (const f of files) {
    if (f.kind === 'episode' && f.show && !(f.show in touched.before)) touched.before[f.show] = untrack(() => showCounts(episodesOf(f.show!)));
  }
}

/**
 * Reflect a progress report in the cached list, so cards and heroes update without a refetch. A
 * report that answers watched makes the item watched (a film in all its versions); one that does not
 * leaves a watched item watched, as the server does.
 */
export function applyProgress(id: number, progress: Progress | null): void {
  const item = findItem(id);
  if (!item) return;
  touch([item]);
  stamp([id]);
  item.progress = progress;
  if (!progress?.watched || item.watched) return;
  item.watched = true;
  item.watchedAt = now();
  item.playCount = Math.max(item.playCount ?? 0, 1);
  syncFilms([item]);
}

/** A film's watch state is its versions' together (any watched, the latest time, the highest count). */
function syncFilms(changed: Iterable<ItemSummary>): void {
  const titles = new Set<number>();
  for (const it of changed) if (it.kind === 'movie' && it.titleId) titles.add(it.titleId);
  for (const t of titles) {
    const vs = library.items.filter((i) => i.kind === 'movie' && i.titleId === t);
    const seen = vs.filter((v) => v.watched);
    const watched = seen.length > 0;
    const at = watched ? Math.max(...seen.map((v) => v.watchedAt ?? 0)) || null : null;
    const count = watched ? Math.max(...seen.map((v) => v.playCount ?? 0)) : 0;
    for (const v of vs) Object.assign(v, { watched, watchedAt: at, playCount: count });
  }
}

/**
 * Patch the cached list from the answer to a PUT .../watched: every card, rail and page follows.
 * `g` is the generation of the change it answers: a file changed again since keeps the newer state.
 */
export function applyWatch(items: readonly ItemWatch[], g = stamp(items.map((w) => w.id))): void {
  const changed: ItemSummary[] = [];
  for (const w of items) {
    const it = findItem(w.id);
    if (!it || !mine(w.id, g)) continue;
    Object.assign(it, { watched: w.watched, watchedAt: w.watchedAt, playCount: w.playCount, progress: w.progress });
    changed.push(it);
  }
  syncFilms(changed);
}

/** What a mark covers: a film or an episode (its item), a whole show, or one season of it (0: specials). */
export type WatchTarget = { item: Pick<ItemSummary, 'id' | 'kind' | 'titleId'> } | { show: string; season?: number };

/** The cached files a mark covers, as the server will: a film with a title is all its versions. */
function coveredBy(t: WatchTarget): ItemSummary[] {
  if ('item' in t) {
    const { id, kind, titleId } = t.item;
    if (kind === 'movie' && titleId) return library.items.filter((i) => i.kind === 'movie' && i.titleId === titleId);
    const it = findItem(id);
    return it ? [it] : [];
  }
  return episodesOf(t.show).filter((i) => t.season === undefined || (i.season ?? 0) === t.season);
}

function request(t: WatchTarget, watched: boolean) {
  if ('item' in t) {
    const { id, kind, titleId } = t.item;
    return kind === 'movie' && titleId ? api.setTitleWatched(titleId, watched) : api.setItemWatched(id, watched);
  }
  return t.season === undefined ? api.setShowWatched(t.show, watched) : api.setSeasonWatched(t.show, t.season, watched);
}

/**
 * Mark a film, an episode, a season or a show watched or not. The cached list changes at once (as
 * the server will change it: marking keeps a watched file's time and count and puts the others at
 * position 0; unmarking clears them), then takes the server's answer, which is returned; on an
 * error it goes back to what it was and the error is thrown on. A file changed again meanwhile is
 * left to the later change, by the answer and by the rollback.
 */
export async function markWatched(t: WatchTarget, watched: boolean): Promise<WatchResult> {
  const files = coveredBy(t);
  touch(files);
  const g = stamp(files.map((f) => f.id));
  const before = files.map((f) => ({ f, was: { watched: f.watched, watchedAt: f.watchedAt, playCount: f.playCount, progress: f.progress } }));
  const at = now();
  for (const f of files) {
    if (!watched) Object.assign(f, { watched: false, watchedAt: null, playCount: 0, progress: null });
    else if (!f.watched || !f.progress?.watched) {
      Object.assign(f, { watched: true, watchedAt: f.watchedAt ?? at, playCount: Math.max(f.playCount ?? 0, 1), progress: { positionSec: 0, watched: true } });
    }
  }
  try {
    const result = await request(t, watched);
    applyWatch(result.items, g);
    return result;
  } catch (e) {
    for (const { f, was } of before) if (mine(f.id, g)) Object.assign(f, was);
    throw e;
  }
}

/**
 * The watch badge of a title card (Genre, Collection, Person, Home's genre rails). A film's is its
 * cached item's, else the card's. A series' are the card's counts (over every show linked to it),
 * plus what a mark or a progress report changed in its show since the list was fetched.
 */
export function titleBadge(t: Pick<TitleCard, 'kind' | 'itemId' | 'show' | 'watched' | 'episodeCount' | 'watchedEpisodes' | 'inProgress'>): WatchBadge | null {
  if (t.kind === 'movie') return itemBadge((t.itemId ? findItem(t.itemId) : undefined) ?? t);
  const before = t.show && touchedIn === library.items ? touched.before[t.show] : undefined;
  if (!before || !t.show) return countsBadge(t);
  const after = showCounts(episodesOf(t.show));
  return countsBadge({
    episodeCount: t.episodeCount ?? after.episodeCount,
    watchedEpisodes: Math.max(0, (t.watchedEpisodes ?? 0) + after.watchedEpisodes - before.watchedEpisodes),
    inProgress: Math.max(0, (t.inProgress ?? 0) + after.inProgress - before.inProgress),
  });
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
  const byShow = new Map<string, Omit<ShowEntry, 'counts'> & { seasonSet: Set<number>; files: ItemSummary[] }>();
  for (const ep of items) {
    if (ep.kind !== 'episode' || !ep.show) continue;
    let s = byShow.get(ep.show);
    if (!s) {
      s = { show: ep.show, cover: ep, seasons: [], episodeCount: 0, latestAddedAt: 0, seasonSet: new Set(), files: [] };
      byShow.set(ep.show, s);
    }
    s.episodeCount++;
    s.files.push(ep);
    s.seasonSet.add(ep.season ?? 0);
    s.latestAddedAt = Math.max(s.latestAddedAt, ep.addedAt);
    if (episodeOrder(ep) < episodeOrder(s.cover)) s.cover = ep;
  }
  return [...byShow.values()]
    .map(({ seasonSet, files, ...s }) => ({
      ...s,
      seasons: [...seasonSet].sort((a, b) => a - b),
      counts: showCounts(files),
      meta: meta.get(s.show),
    }))
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
