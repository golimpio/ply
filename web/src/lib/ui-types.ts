// UI-side extensions of the API types. api.ts is owned by the playback work;
// fields the server adds for the UI (artwork) are typed here until api.ts
// declares them, and every reader tolerates their absence.
import { mediaUrl, type ItemSummary } from './api';

export interface ArtURLs {
  backdrop: string;
  thumb: string;
  /** "#rrggbb" once the server has generated an image for the item. */
  color?: string;
}

export type WithArt = ItemSummary & { art?: ArtURLs | null };

export function artOf(item: ItemSummary | undefined | null): ArtURLs | null {
  return (item as WithArt | undefined)?.art ?? null;
}

export function artSrc(item: ItemSummary | undefined | null, kind: 'thumb' | 'backdrop'): string {
  const a = artOf(item);
  return a ? mediaUrl(a[kind]) : '';
}

/** Fraction watched in [0, 1], or 0 when there is no progress. */
export function progressOf(item: ItemSummary): number {
  const p = item.progress;
  if (!p || p.watched || !item.durationSec) return 0;
  return Math.min(1, Math.max(0, p.positionSec / item.durationSec));
}
