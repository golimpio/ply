// What the pages show of the M10 ratings (docs/M10-RATINGS.md): the words and numbers of a rating
// chip, the order the chips go in, and how the lists sort and filter by rating. The server gives
// every entry a score100 (0 to 100), so nothing here needs to know a source's scale to compare.
import type { RatingEntry, RatingSource } from './api';

/** The server's fixed order (title detail `ratings`), and the order of the settings list. */
export const RATING_SOURCES: RatingSource[] = ['imdb', 'rt_critics', 'rt_audience', 'metacritic', 'letterboxd', 'trakt', 'tmdb'];

/** The sources a card can be asked to show (ratingsPrimary). */
export const PRIMARY_SOURCES: RatingSource[] = ['imdb', 'rt_critics', 'metacritic', 'letterboxd', 'trakt', 'tmdb'];

/** The source's name in full (settings, labels for assistive tech, tooltips). */
export const SOURCE_NAMES: Record<RatingSource, string> = {
  imdb: 'IMDb',
  rt_critics: 'Rotten Tomatoes critics',
  rt_audience: 'Rotten Tomatoes audience',
  metacritic: 'Metacritic',
  letterboxd: 'Letterboxd',
  trakt: 'Trakt',
  tmdb: 'TMDB',
};

/** The name a chip shows beside its number: short, so seven chips fit. */
export const SOURCE_SHORT: Record<RatingSource, string> = {
  imdb: 'IMDb',
  rt_critics: 'Critics',
  rt_audience: 'Audience',
  metacritic: 'Metacritic',
  letterboxd: 'Letterboxd',
  trakt: 'Trakt',
  tmdb: 'TMDB',
};

/** The minimum-rating filter of the Movies and Shows pages, on score100. */
export const MIN_RATINGS = [60, 70, 80] as const;

const isSource = (s: unknown): s is RatingSource => RATING_SOURCES.includes(s as RatingSource);

/** "999", "1.5K", "12K", "1.2M": a vote count in short form; '' for none. */
export function shortVotes(n: number | undefined | null): string {
  if (!n || n <= 0 || !Number.isFinite(n)) return '';
  const short = (v: number, unit: string) => {
    const s = v < 10 ? (Math.round(v * 10) / 10).toFixed(1).replace(/\.0$/, '') : String(Math.round(v));
    return `${s}${unit}`;
  };
  if (n < 1000) return String(Math.round(n));
  // 999,950 would round to "1000K": that is a million.
  if (n < 999_500) return short(n / 1000, 'K');
  return short(n / 1_000_000, 'M');
}

/** The number a chip shows: "8.7", "93%", "74", "4.1". */
export function ratingValue(e: Pick<RatingEntry, 'source' | 'value' | 'scale' | 'score100'>): string {
  switch (e.source) {
    case 'rt_critics':
    case 'rt_audience':
    case 'trakt':
      return `${e.score100}%`;
    case 'metacritic':
      return String(e.score100);
    default:
      // IMDb and TMDB out of 10, Letterboxd out of 5: one decimal, as each site shows it.
      return (Math.round(e.value * 10) / 10).toFixed(1);
  }
}

/** "out of 10" and the like, for a label read aloud; '' for a percentage. */
function outOf(e: Pick<RatingEntry, 'source' | 'scale'>): string {
  if (e.source === 'rt_critics' || e.source === 'rt_audience' || e.source === 'trakt') return '';
  return e.scale > 0 ? ` out of ${e.scale}` : '';
}

/** Metacritic's colour bands of a Metascore: 61 and up green, 40 to 60 yellow, under 40 red. */
export type Band = 'good' | 'mixed' | 'bad';
export function metacriticBand(score: number): Band {
  if (score >= 61) return 'good';
  if (score >= 40) return 'mixed';
  return 'bad';
}

/**
 * Rotten Tomatoes' word for a score, as the server gives it, else from the 60% line. 'fresh' or
 * 'rotten' for the critics (or 'certified'), 'upright' or 'spilled' for the audience (or
 * 'verified_hot'); '' for any other source.
 */
export function tomatoState(e: Pick<RatingEntry, 'source' | 'score100' | 'state'>): string {
  if (e.source === 'rt_critics') {
    return e.state === 'fresh' || e.state === 'rotten' || e.state === 'certified' ? e.state : e.score100 >= 60 ? 'fresh' : 'rotten';
  }
  if (e.source === 'rt_audience') {
    return e.state === 'upright' || e.state === 'spilled' || e.state === 'verified_hot' ? e.state : e.score100 >= 60 ? 'upright' : 'spilled';
  }
  return '';
}

const STATE_WORDS: Record<string, string> = {
  fresh: 'fresh',
  rotten: 'rotten',
  certified: 'certified fresh',
  upright: 'upright',
  spilled: 'spilled',
  verified_hot: 'verified hot',
};

/**
 * "IMDb 8.7, 1.2M votes", "Rotten Tomatoes critics 93%, fresh", "Metacritic 74 out of 100": what
 * a chip says aloud. It holds what the chip shows, in the same order (WCAG 2.5.3, label in name):
 * IMDb's chip shows its votes right after the number, so its scale is not put between them.
 */
export function ratingAriaLabel(e: RatingEntry): string {
  const state = STATE_WORDS[tomatoState(e)];
  const votes = shortVotes(e.votes);
  const scale = e.source === 'imdb' && votes ? '' : outOf(e);
  return [`${SOURCE_NAMES[e.source] ?? e.source} ${ratingValue(e)}${scale}`, state, votes && `${votes} votes`].filter(Boolean).join(', ');
}

/** "rated today", "rated 1 day ago", "rated 12 days ago"; '' when fetchedAt is not a date. */
export function ratedAgo(fetchedAt: string | undefined | null, now: Date = new Date()): string {
  const t = fetchedAt ? Date.parse(fetchedAt) : NaN;
  if (!Number.isFinite(t)) return '';
  const days = Math.max(0, Math.floor((now.getTime() - t) / 86_400_000));
  if (days === 0) return 'rated today';
  return days === 1 ? 'rated 1 day ago' : `rated ${days} days ago`;
}

/** The tooltip of a chip: the source, the votes in full and how old the number is. */
export function ratingTooltip(e: RatingEntry, now: Date = new Date()): string {
  const votes = e.votes && e.votes > 0 ? `${e.votes.toLocaleString('en-US')} ${e.votes === 1 ? 'vote' : 'votes'}` : '';
  const head = [SOURCE_NAMES[e.source] ?? e.source, votes].filter(Boolean).join(', ');
  const ago = ratedAgo(e.fetchedAt, now);
  return ago ? `${head}. ${ago[0].toUpperCase()}${ago.slice(1)}.` : `${head}.`;
}

/**
 * The entries to show, in the order the settings ask for (ratingsShown). With no settings (they
 * could not be read) every entry is shown, in the order the server sent them.
 */
export function orderShown(ratings: RatingEntry[], shown: readonly string[] | null | undefined): RatingEntry[] {
  const valid = ratings.filter((r) => isSource(r.source));
  if (!shown) return valid;
  const bySource = new Map(valid.map((r) => [r.source, r]));
  return shown.flatMap((s) => bySource.get(s as RatingSource) ?? []);
}

/**
 * A title's ratings for its detail page: the detail's own list once it has come; before that (the
 * cached summary paints the page first), or from a server that has no list, the one rating the
 * card had.
 */
export function titleRatings(
  meta: { ratings?: RatingEntry[] | null; rating?: RatingEntry | null } | undefined | null,
  summary: { rating?: RatingEntry | null } | undefined | null,
): RatingEntry[] {
  if (meta?.ratings) return meta.ratings;
  const one = meta?.rating ?? summary?.rating;
  return one && typeof one === 'object' ? [one] : [];
}

/** The number the lists sort by: score100, or -1 for a title with no rating (it goes last). */
export function ratingScore(x: { rating?: RatingEntry | null } | undefined | null): number {
  const r = x?.rating;
  return r && typeof r === 'object' && Number.isFinite(r.score100) ? r.score100 : -1;
}

/** Whether a title passes a minimum rating (0 for any). A title with no rating passes only "any". */
export function passesMinRating(x: { rating?: RatingEntry | null } | undefined | null, min: number): boolean {
  return min <= 0 || ratingScore(x) >= min;
}

/** The entry with its arrays and optional fields as the page expects (Go sends null for none). */
export function normaliseRating(e: RatingEntry | null | undefined): RatingEntry | null {
  if (!e || typeof e !== 'object' || !isSource(e.source)) return null;
  return { ...e, url: e.url ?? '', fetchedAt: e.fetchedAt ?? '' };
}

/** A list of entries from the server: null as [], and entries of a source this page does not know left out. */
export function normaliseRatings(list: (RatingEntry | null)[] | null | undefined): RatingEntry[] {
  return (list ?? []).map(normaliseRating).filter((e): e is RatingEntry => e !== null);
}

/**
 * An attribution as the server words it, with its URL made a link where the text has it
 * ("Information courtesy of IMDb (https://www.imdb.com). Used with permission."): before, the
 * link's text and after. With the URL not in the text, the link is the URL's host, after it.
 */
export function linkParts(text: string, url: string): { before: string; link: string; after: string } {
  for (const u of [url, url.replace(/\/$/, ''), `${url.replace(/\/$/, '')}/`]) {
    const i = u ? text.indexOf(u) : -1;
    if (i >= 0) return { before: text.slice(0, i), link: u, after: text.slice(i + u.length) };
  }
  let host = url;
  try {
    host = new URL(url).hostname.replace(/^www\./, '');
  } catch {
    // not a URL: shown as it is
  }
  return { before: url ? `${text} ` : text, link: url ? host : '', after: '' };
}

/** A chip links only over https (the server keeps only https links on the source's own site). */
export function safeRatingUrl(url: string | undefined | null): string {
  if (!url) return '';
  try {
    return new URL(url).protocol === 'https:' ? url : '';
  } catch {
    return '';
  }
}
