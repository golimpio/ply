// The library's two lists, each fetched whole once per library version and patched by the client's
// own marks and progress reports instead of refetched: the movies as cards (the grids, home rails,
// search) and the episodes as full rows with the show list (Shows, Show, home, search). Movies and
// Shows load only their own; a page that needs both calls ensureLibrary.
import { untrack } from 'svelte';
import { api, type ItemSummary, type ItemWatch, type MovieCard, type Progress, type ShowSummary, type TitleCard, type WatchResult } from './api';
import { RowStore, type Fields, type Row } from './rowstore.svelte';
import { app } from './state.svelte';
import { countsBadge, itemBadge, showCounts, type EpisodeCounts, type WatchBadge } from './watched';

/** A row of either list. Ids are unique across both. */
export type LibraryRow = MovieCard | ItemSummary;

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

const version = () => app.libraryVersion;

export const movies = new RowStore<MovieCard>({ version, fetch: () => api.movieCards() });

export const episodes = new RowStore<ItemSummary, ShowSummary[]>({
  version,
  fetch: () => api.items({ kind: 'episode' }),
  // The show list only adds metadata to what the episodes say: if it fails, the shows are still
  // there (from their episodes), without posters.
  extra: () => api.shows().catch(() => [] as ShowSummary[]),
});

/** Load (or refresh after a scan) the movie cards. Safe to call often. */
export const ensureMovies = (): Promise<void> => movies.ensure();
/** Load (or refresh after a scan) the episodes and the show list. Safe to call often. */
export const ensureEpisodes = (): Promise<void> => episodes.ensure();
/** Both lists, for the pages that show both (home, search, an item before its kind is known). */
export function ensureLibrary(): Promise<void> {
  return Promise.all([movies.ensure(), episodes.ensure()]).then(() => undefined);
}

/** Both lists together: loaded when both are; the first error there is. */
export const bothLists = {
  get loaded(): boolean {
    return movies.loaded && episodes.loaded;
  },
  get error(): string {
    return movies.error || episodes.error;
  },
};

/** The row of either list with this id. A reader that finds nothing yet hears the lists land. */
export function findItem(id: number): LibraryRow | undefined {
  return movies.get(id) ?? episodes.get(id);
}

let both: { m: readonly MovieCard[]; e: readonly ItemSummary[]; rows: readonly LibraryRow[] } | undefined;
/** Every row of both lists (the search source): a new array whenever either list changes. */
export function libraryRows(): readonly LibraryRow[] {
  const m = movies.rows;
  const e = episodes.rows;
  if (!both || both.m !== m || both.e !== e) both = { m, e, rows: [...m, ...e] };
  return both.rows;
}

// Rows grouped by a key, kept as positions in the list: a patch moves no row and changes no key, so
// the positions hold until the next list arrives (the store's epoch). The groups hand out the
// current rows, rebuilt lazily after each patch.
function grouped<T extends Row, E, K>(store: RowStore<T, E>, keyOf: (r: T) => K | undefined): (key: K) => T[] {
  let at: { epoch: number; of: Map<K, number[]> } | undefined;
  let made: { rows: readonly T[]; of: Map<K, T[]> } | undefined;
  return (key) => {
    const rows = store.rows;
    const epoch = store.epoch;
    if (!at || at.epoch !== epoch) {
      const of = new Map<K, number[]>();
      rows.forEach((r, i) => {
        const k = keyOf(r);
        if (k === undefined) return;
        const list = of.get(k);
        if (list) list.push(i);
        else of.set(k, [i]);
      });
      at = { epoch, of };
    }
    if (!made || made.rows !== rows) made = { rows, of: new Map() };
    let list = made.of.get(key);
    if (!list) {
      list = (at.of.get(key) ?? []).map((i) => rows[i]);
      made.of.set(key, list);
    }
    return list;
  };
}

const byShow = grouped(episodes, (r) => (r.kind === 'episode' && r.show) || undefined);
const byFilm = grouped(movies, (r) => r.titleId || undefined);

/** The episode files of a show in the cached list. */
export function episodesOf(show: string): ItemSummary[] {
  return byShow(show);
}

/** The cards of a film's versions (4K and 1080p, a director's cut), in list order. */
export function versionsOf(titleId: number): MovieCard[] {
  return byFilm(titleId);
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

/** The watch state a write carries. A card keeps only watched and progress. */
type WatchFields = Partial<Pick<ItemSummary, 'watched' | 'watchedAt' | 'playCount' | 'progress'>>;

const playCountOf = (r: LibraryRow) => ('playCount' in r ? (r.playCount ?? 0) : 0);
const watchedAtOf = (r: LibraryRow) => ('watchedAt' in r ? r.watchedAt : undefined);
const showOf = (r: LibraryRow) => ('show' in r && r.kind === 'episode' ? r.show : undefined);

function cardFields(f: WatchFields): Fields<MovieCard> {
  const c: Fields<MovieCard> = {};
  if ('watched' in f) c.watched = !!f.watched;
  if ('progress' in f) c.progress = f.progress ?? null;
  return c;
}

/** A row's watch state as it is, to put back on a rollback (a card's write drops what it lacks). */
const watchOf = (r: LibraryRow): WatchFields => ({
  watched: r.watched,
  watchedAt: watchedAtOf(r),
  playCount: 'playCount' in r ? r.playCount : undefined,
  progress: r.progress,
});

/**
 * Write watch state into whichever list has each row, one new array per list. A row neither list
 * has yet (they are loading) is written to both: a list still in flight gets it when it lands.
 */
function write(changes: readonly [number, WatchFields][]): void {
  const cards: [number, Fields<MovieCard>][] = [];
  const eps: [number, Fields<ItemSummary>][] = [];
  for (const [id, f] of changes) {
    const card = movies.get(id) !== undefined;
    if (card || episodes.get(id) === undefined) cards.push([id, cardFields(f)]);
    if (!card) eps.push([id, f]);
  }
  if (cards.length) movies.patchAll(cards);
  if (eps.length) episodes.patchAll(eps);
}

/** A film is watched when any of its versions is: the cards of the films of `ids` follow. */
function syncFilms(ids: Iterable<number>): void {
  const titles = new Set<number>();
  for (const id of ids) {
    const t = movies.get(id)?.titleId;
    if (t) titles.add(t);
  }
  const changes: [number, Fields<MovieCard>][] = [];
  for (const t of titles) {
    const vs = versionsOf(t);
    const watched = vs.some((v) => v.watched);
    for (const v of vs) if (v.watched !== watched) changes.push([v.id, { watched }]);
  }
  if (changes.length) movies.patchAll(changes);
}

// Title cards (Genre, Collection, Person) carry the server's counts of a series, over every show
// linked to it. A show changed here since the episode list was fetched has its counts from before the
// first change kept, and a card adds what changed since (titleBadge). Keyed on the list's epoch, not
// its array: every patch makes a new array.
let touchedEpoch = -1; // the episode list `touched` is about
const touched = $state<{ before: Record<string, EpisodeCounts> }>({ before: {} });
function touch(files: readonly LibraryRow[]): void {
  const epoch = episodes.epoch;
  if (touchedEpoch !== epoch) {
    touchedEpoch = epoch;
    touched.before = {};
  }
  for (const f of files) {
    const show = showOf(f);
    if (show && !(show in touched.before)) touched.before[show] = showCounts(episodesOf(show));
  }
}

/**
 * Reflect a progress report in the cached lists, so cards and heroes update without a refetch. A
 * report that answers watched makes the item watched (a film in all its versions); one that does not
 * leaves a watched item watched, as the server does.
 */
export function applyProgress(id: number, progress: Progress | null): void {
  untrack(() => {
    const item = findItem(id);
    if (item) touch([item]);
    stamp([id]);
    if (!item) {
      // Not in a list yet: a list loading gets it. Its time and count are the server's to say.
      write([[id, progress?.watched ? { progress, watched: true } : { progress }]]);
      return;
    }
    if (!progress?.watched || item.watched) {
      write([[id, { progress }]]);
      return;
    }
    write([[id, { progress, watched: true, watchedAt: now(), playCount: Math.max(playCountOf(item), 1) }]]);
    syncFilms([id]);
  });
}

/**
 * Patch the cached lists from the answer to a PUT .../watched: every card, rail and page follows.
 * `g` is the generation of the change it answers: a file changed again since keeps the newer state.
 */
export function applyWatch(items: readonly ItemWatch[], g = stamp(items.map((w) => w.id))): void {
  untrack(() => {
    const ours = items.filter((w) => mine(w.id, g));
    write(ours.map((w) => [w.id, { watched: w.watched, watchedAt: w.watchedAt, playCount: w.playCount, progress: w.progress }]));
    syncFilms(ours.map((w) => w.id));
  });
}

/** What a mark covers: a film or an episode (its item), a whole show, or one season of it (0: specials). */
export type WatchTarget = { item: Pick<ItemSummary, 'id' | 'kind' | 'titleId'> } | { show: string; season?: number };

/** The cached files a mark covers, as the server will: a film with a title is all its versions. */
function coveredBy(t: WatchTarget): LibraryRow[] {
  if ('item' in t) {
    const { id, kind, titleId } = t.item;
    if (kind === 'movie' && titleId) return versionsOf(titleId);
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
 * Mark a film, an episode, a season or a show watched or not. The cached lists change at once (as
 * the server will change them: marking keeps a watched file's time and count and puts the others at
 * position 0; unmarking clears them), then take the server's answer, which is returned; on an error
 * they go back to what they were and the error is thrown on. A file changed again meanwhile is left
 * to the later change, by the answer and by the rollback.
 */
export async function markWatched(t: WatchTarget, watched: boolean): Promise<WatchResult> {
  const { g, before } = untrack(() => {
    const files = coveredBy(t);
    touch(files);
    const g = stamp(files.map((f) => f.id));
    const before = files.map((f): [number, WatchFields] => [f.id, watchOf(f)]);
    const at = now();
    const changes: [number, WatchFields][] = [];
    for (const f of files) {
      if (!watched) changes.push([f.id, { watched: false, watchedAt: null, playCount: 0, progress: null }]);
      else if (!f.watched || !f.progress?.watched) {
        changes.push([f.id, { watched: true, watchedAt: watchedAtOf(f) ?? at, playCount: Math.max(playCountOf(f), 1), progress: { positionSec: 0, watched: true } }]);
      }
    }
    write(changes);
    return { g, before };
  });
  try {
    const result = await request(t, watched);
    applyWatch(result.items, g);
    return result;
  } catch (e) {
    // The saved fields go onto the row as it is now: a list that landed meanwhile keeps its own rows.
    untrack(() => write(before.filter(([id]) => mine(id, g))));
    throw e;
  }
}

/**
 * The watch badge of a title card (Genre, Collection, Person, Home's genre rails). A film's is its
 * cached card's, else the title card's. A series' are the card's counts (over every show linked to
 * it), plus what a mark or a progress report changed in its show since the episodes were fetched.
 */
export function titleBadge(t: Pick<TitleCard, 'kind' | 'itemId' | 'show' | 'watched' | 'episodeCount' | 'watchedEpisodes' | 'inProgress'>): WatchBadge | null {
  if (t.kind === 'movie') return itemBadge((t.itemId ? findItem(t.itemId) : undefined) ?? t);
  // Read the snapshot whatever the epoch: a badge drawn before the first change of a list must hear it.
  const snap = t.show ? touched.before[t.show] : undefined;
  const before = touchedEpoch === episodes.epoch ? snap : undefined;
  if (!before || !t.show) return countsBadge(t);
  const after = showCounts(episodesOf(t.show));
  return countsBadge({
    episodeCount: t.episodeCount ?? after.episodeCount,
    watchedEpisodes: (t.watchedEpisodes ?? 0) + after.watchedEpisodes - before.watchedEpisodes,
    inProgress: (t.inProgress ?? 0) + after.inProgress - before.inProgress,
  });
}

/** The metadata row of a show, if the server sent one. */
export function showMeta(name: string): ShowSummary | undefined {
  return episodes.extra?.find((s) => s.show === name);
}

/**
 * The shows of items, from their episodes; `shows` (from /api/shows) adds
 * each one's metadata.
 */
export function showsOf(items: readonly ItemSummary[], shows: readonly ShowSummary[] = episodes.extra ?? []): ShowEntry[] {
  const meta = new Map(shows.map((s) => [s.show, s]));
  const entries = new Map<string, Omit<ShowEntry, 'counts'> & { seasonSet: Set<number>; files: ItemSummary[] }>();
  for (const ep of items) {
    if (ep.kind !== 'episode' || !ep.show) continue;
    let s = entries.get(ep.show);
    if (!s) {
      s = { show: ep.show, cover: ep, seasons: [], episodeCount: 0, latestAddedAt: 0, seasonSet: new Set(), files: [] };
      entries.set(ep.show, s);
    }
    s.episodeCount++;
    s.files.push(ep);
    s.seasonSet.add(ep.season ?? 0);
    s.latestAddedAt = Math.max(s.latestAddedAt, ep.addedAt);
    if (episodeOrder(ep) < episodeOrder(s.cover)) s.cover = ep;
  }
  return [...entries.values()]
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

export const byTitle = (a: Pick<LibraryRow, 'title'>, b: Pick<LibraryRow, 'title'>) =>
  a.title.localeCompare(b.title, undefined, { numeric: true, sensitivity: 'base' });
export const byAdded = (a: Pick<LibraryRow, 'addedAt'>, b: Pick<LibraryRow, 'addedAt'>) => b.addedAt - a.addedAt;
