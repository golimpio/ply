// The parts of the search palette that are not about drawing it: when people
// are looked up, which genres a query finds, and how to tell an answer for the
// query on screen from one for a query typed over since.
import type { GenreCount, ItemSummary } from './api';
import { search } from './fuzzy';

/** Typing pauses this long (ms) before people are looked up. */
export const PEOPLE_DEBOUNCE_MS = 150;

/** People are only looked up from this many characters on. */
export const MIN_PEOPLE_QUERY = 2;

/** The query to look people up with (trimmed), or null when it is too short to ask the server. */
export function peopleQuery(q: string): string | null {
  const t = q.trim();
  return Array.from(t).length >= MIN_PEOPLE_QUERY ? t : null;
}

/**
 * Tells the newest request from the ones it replaced. Take a ticket when a
 * request starts; when its answer arrives it may only be used if the ticket is
 * still current. cancel() makes every ticket stale (the query became too short,
 * the palette closed).
 */
export class Latest {
  private seq = 0;

  begin(): number {
    return ++this.seq;
  }

  cancel(): void {
    this.seq++;
  }

  isCurrent(ticket: number): boolean {
    return ticket === this.seq;
  }
}

/** The genres a query finds, best match first (the same fuzzy match as titles). */
export function matchGenres(genres: GenreCount[], q: string, limit = 4): GenreCount[] {
  // The matcher works on library items; a genre stands in as one that only has a title.
  const stand = (genre: GenreCount): ItemSummary & { genre: GenreCount } => ({
    genre,
    id: genre.id,
    kind: 'movie',
    title: genre.name,
    durationSec: 0,
    container: '',
    videoCodec: '',
    audioCodec: '',
    width: 0,
    height: 0,
    addedAt: 0,
  });
  return search(genres.map(stand), q, limit).map((s) => s.genre);
}
