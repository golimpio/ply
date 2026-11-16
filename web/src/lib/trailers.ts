import type { LocalTrailer, TitleTrailers } from './api';

/** What the Trailer button of a title's page is. */
export type TrailerButtonState =
  | { kind: 'play'; itemId: number; choices: LocalTrailer[] }
  | { kind: 'downloading'; jobId?: number }
  | { kind: 'none' };

/**
 * The button for a title's trailers: the first one on disk plays (the others are offered too),
 * a download the server is making spins, and a failed one or none means no button.
 */
export function buttonState(t: TitleTrailers | undefined): TrailerButtonState {
  if (t?.local.length) return { kind: 'play', itemId: t.local[0].itemId, choices: t.local };
  if (t?.download && t.download.state !== 'failed') return { kind: 'downloading', jobId: t.download.jobId };
  return { kind: 'none' };
}
