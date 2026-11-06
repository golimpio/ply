// Watch state on the client (docs/DESIGN.md, "Watch state"): how far through its episodes a show or a
// season is, read off the cached items so that a mark shows everywhere at once, and what a card
// says of it. The server counts the same way: an episode is a (show, season, number), watched when
// any of its files is, in progress when none is watched and one is started; a show's counts leave
// the specials out, unless it has nothing else.
import type { ItemSummary } from './api';

export interface EpisodeCounts {
  /** Episodes (one that has two files is one). */
  episodeCount: number;
  watchedEpisodes: number;
  /** Started and not watched. */
  inProgress: number;
}

const started = (i: ItemSummary) => !!i.progress && !i.progress.watched && i.progress.positionSec > 0;

/** The counts of a set of episode files (a season's, a show's). */
export function countEpisodes(files: readonly ItemSummary[]): EpisodeCounts {
  const byEpisode = new Map<string, { watched: boolean; started: boolean }>();
  for (const f of files) {
    // A file with no number in its name is an episode of its own.
    const key = f.episode ? `${f.show}\u0000${f.season ?? 0}\u0000${f.episode}` : `#${f.id}`;
    const e = byEpisode.get(key) ?? { watched: false, started: false };
    e.watched ||= !!f.watched;
    e.started ||= started(f);
    byEpisode.set(key, e);
  }
  let watchedEpisodes = 0;
  let inProgress = 0;
  for (const e of byEpisode.values()) {
    if (e.watched) watchedEpisodes++;
    else if (e.started) inProgress++;
  }
  return { episodeCount: byEpisode.size, watchedEpisodes, inProgress };
}

/** A show's counts: its specials (season 0) left out, unless it has nothing but specials. */
export function showCounts(files: readonly ItemSummary[]): EpisodeCounts {
  const regular = files.filter((f) => (f.season ?? 0) !== 0);
  return countEpisodes(regular.length ? regular : files);
}

/** What a card says of its watch state: watched (✓), so many left, or one in progress. */
export interface WatchBadge {
  kind: 'done' | 'left' | 'started';
  /** What the badge shows. */
  text: string;
  /** What it says to a screen reader. */
  label: string;
  /** Some episode is started: the badge carries a dot. */
  started: boolean;
}

const episodes = (n: number) => `${n} episode${n === 1 ? '' : 's'}`;

export const WATCHED: WatchBadge = { kind: 'done', text: '✓', label: 'Watched', started: false };

/**
 * The badge of a show or a season: ✓ once every episode is watched, "N left" once some are, "In
 * progress" when none is but one is started; null when it is not started (or has no episodes).
 */
export function countsBadge(c: Partial<EpisodeCounts> | null | undefined): WatchBadge | null {
  const count = c?.episodeCount ?? 0;
  if (count <= 0) return null;
  const watched = Math.min(c?.watchedEpisodes ?? 0, count);
  const going = (c?.inProgress ?? 0) > 0;
  if (watched === count) return WATCHED;
  const also = going ? `, ${c?.inProgress} in progress` : '';
  if (watched > 0) {
    const left = count - watched;
    return { kind: 'left', text: `${left} left`, label: `${episodes(left)} left${also}`, started: going };
  }
  if (going) return { kind: 'started', text: 'In progress', label: `${episodes(c?.inProgress ?? 0)} in progress`, started: true };
  return null;
}

/** The badge of a film or an episode: ✓ when it is watched. */
export const itemBadge = (i: Pick<ItemSummary, 'watched'> | null | undefined): WatchBadge | null => (i?.watched ? WATCHED : null);
