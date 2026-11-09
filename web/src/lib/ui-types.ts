// Small helpers over the API's row types: a row's artwork and how far it has been watched. Each
// takes only the fields it reads, so a movie card and a full row both fit.
import { mediaUrl, type ArtUrls, type ItemSummary } from './api';

/** The artwork links a helper may read: a movie card has the thumb but no backdrop. */
export type ArtLinks = Partial<Pick<ArtUrls, 'thumb' | 'backdrop'>>;

/** A row's artwork, as the row types it (a full row's ArtUrls, a card's CardArt), or null. */
export function artOf<A extends object>(item: { art?: A | null } | undefined | null): A | null {
  return item?.art ?? null;
}

/** The absolute URL of a row's frame grab, or '' when the row has none of that kind. */
export function artSrc(item: { art?: ArtLinks | null } | undefined | null, kind: 'thumb' | 'backdrop'): string {
  const rel = artOf(item)?.[kind];
  return rel !== undefined ? mediaUrl(rel) : '';
}

/** Fraction watched in [0, 1], or 0 when there is no progress. */
export function progressOf(item: Pick<ItemSummary, 'progress' | 'durationSec'>): number {
  const p = item.progress;
  if (!p || p.watched || !item.durationSec) return 0;
  return Math.min(1, Math.max(0, p.positionSec / item.durationSec));
}
