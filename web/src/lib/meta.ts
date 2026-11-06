// What the pages show of the M6 metadata: which picture goes where, and the
// small labels around it. Every helper copes with the metadata being absent
// (no TMDB key, nothing matched yet): the frame grab and the file name stand in.
import {
  imageSrcset,
  imageUrl,
  mediaUrl,
  type CrewMember,
  type ItemSummary,
  type MetadataStatus,
  type RatingEntry,
  type TitleCard,
  type TitleDetail,
} from './api';
import { ratingValue } from './ratings';
import { href } from './router.svelte';
import { artOf } from './ui-types';

/** An image to show: src, plus a srcset when the server has it in several sizes. */
export interface Img {
  src: string;
  srcset?: string;
}

const POSTER_SIZES = ['w185', 'w342', 'w500'] as const;
const WIDE_SIZES = ['w342', 'w500', 'w780'] as const;
const BACKDROP_SIZES = ['w780', 'w1280'] as const;

function tmdb(rel: string | undefined | null, sizes: readonly ('w92' | 'w185' | 'w342' | 'w500' | 'w780' | 'w1280')[], size: (typeof sizes)[number]): Img | null {
  if (!rel) return null;
  const srcset = imageSrcset(rel, [...sizes]);
  return { src: imageUrl(rel, size), srcset: srcset.includes(' ') ? srcset : undefined };
}

/**
 * Whether a list reads better as 2:3 posters: when most of it has one. With no
 * metadata (no TMDB key) nothing has, and the 16:9 frame grabs stay.
 */
export function prefersPosters(list: { poster?: string }[]): boolean {
  if (!list.length) return false;
  return list.filter((x) => x.poster).length * 2 >= list.length;
}

/** A 2:3 poster (movie, show, season, collection), or null when there is none. */
export function posterImg(x: { poster?: string } | undefined | null): Img | null {
  return tmdb(x?.poster, POSTER_SIZES, 'w342');
}

/** A portrait of a person, or null. */
export function profileImg(x: { profile?: string } | undefined | null): Img | null {
  return tmdb(x?.profile, ['w92', 'w185', 'w342'], 'w185');
}

function frameGrab(item: ItemSummary | undefined | null, kind: 'thumb' | 'backdrop'): Img | null {
  const a = artOf(item);
  return a?.[kind] ? { src: mediaUrl(a[kind]) } : null;
}

/**
 * The 16:9 picture of a wide card: an episode's still, a movie's backdrop
 * (the poster's landscape sibling), else the frame grab.
 */
export function wideImg(item: ItemSummary | undefined | null): Img | null {
  if (!item) return null;
  if (item.kind === 'episode') return tmdb(item.still, WIDE_SIZES, 'w500') ?? frameGrab(item, 'thumb');
  return tmdb(item.backdrop, WIDE_SIZES, 'w500') ?? frameGrab(item, 'thumb');
}

/**
 * The full-bleed picture behind a page's headline: for an episode its still,
 * then its show's backdrop; for a movie its backdrop; else the frame grab.
 */
export function backdropImg(item: ItemSummary | undefined | null): Img | null {
  if (!item) return null;
  const own = item.kind === 'episode' ? tmdb(item.still, BACKDROP_SIZES, 'w1280') : null;
  return own ?? tmdb(item.backdrop, BACKDROP_SIZES, 'w1280') ?? frameGrab(item, 'backdrop');
}

/** A show's 16:9 card: its backdrop, else the frame grab of its first episode. */
export function showWideImg(meta: { backdrop?: string } | undefined | null, cover: ItemSummary | undefined): Img | null {
  return tmdb(meta?.backdrop, WIDE_SIZES, 'w500') ?? frameGrab(cover, 'thumb');
}

/** Where a title card leads: a film's page (its first file), a show's page. */
export function titleHref(card: Pick<TitleCard, 'kind' | 'itemId' | 'show' | 'title'>): string {
  if (card.kind === 'movie') return card.itemId ? href.item(card.itemId) : href.movies;
  return href.show(card.show || card.title);
}

/** A backdrop that is not an item's (a show's, a collection's), or null. */
export function backdropOf(x: { backdrop?: string } | undefined | null): Img | null {
  return tmdb(x?.backdrop, BACKDROP_SIZES, 'w1280');
}

/** A title's logo (transparent PNG or SVG), or ''. */
export function logoSrc(x: { logo?: string } | undefined | null): string {
  return x?.logo ? imageUrl(x.logo, 'w500') : '';
}

/**
 * When an episode airs, in words: "Today", "Tomorrow", the weekday within the
 * coming week, else the date ("7 Nov", with the year when it is not this one).
 * `date` is YYYY-MM-DD, read as a local calendar day.
 */
export function airLabel(date: string, now: Date = new Date()): string {
  const [y, m, d] = date.split('-').map(Number);
  if (!y || !m || !d) return '';
  const day = new Date(y, m - 1, d);
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const days = Math.round((day.getTime() - today.getTime()) / 86_400_000);
  if (days === 0) return 'Today';
  if (days === 1) return 'Tomorrow';
  if (days > 1 && days < 7) return day.toLocaleDateString(undefined, { weekday: 'long' });
  return day.toLocaleDateString(undefined, {
    day: 'numeric',
    month: 'short',
    ...(y !== now.getFullYear() ? { year: 'numeric' } : {}),
  });
}

/**
 * "7.8" for a rating out of 10 (an episode's TMDB score); for a rating entry (M10) its number as
 * its source shows it ("8.7", "93%", "74"). '' when there is none.
 */
export function ratingLabel(r: number | RatingEntry | undefined | null): string {
  if (r && typeof r === 'object') return r.score100 >= 0 ? ratingValue(r) : '';
  return typeof r === 'number' && r > 0 ? (Math.round(r * 10) / 10).toFixed(1) : '';
}

/** "2h 16m" for a runtime in minutes; '' when unknown. */
export function runtimeLabel(min: number | undefined | null): string {
  if (!min || min <= 0) return '';
  const h = Math.floor(min / 60);
  const m = min % 60;
  return h ? (m ? `${h}h ${m}m` : `${h}h`) : `${m}m`;
}

/** The year a title came out (or a show started), from its release date or year. */
export function yearOf(meta: Pick<TitleDetail, 'year' | 'releaseDate'> | undefined | null): number | undefined {
  if (!meta) return undefined;
  if (meta.year) return meta.year;
  const y = Number(meta.releaseDate?.slice(0, 4));
  return Number.isInteger(y) && y > 0 ? y : undefined;
}

/**
 * A show's years: "2008–2013" once it ended, "2019–" while it runs, a
 * single year when it ended the year it began or nothing more is known.
 */
export function yearSpan(
  meta: Pick<TitleDetail, 'releaseDate' | 'endDate' | 'status'> | undefined | null,
  fallbackYear?: number,
): string {
  const from = Number(meta?.releaseDate?.slice(0, 4)) || fallbackYear;
  if (!from) return '';
  const to = Number(meta?.endDate?.slice(0, 4));
  if (meta?.status === 'Ended' || meta?.status === 'Canceled') return to && to !== from ? `${from}–${to}` : String(from);
  return meta?.status ? `${from}–` : String(from);
}

/** The crew with one of the jobs, each person once, in the order the jobs are given. */
export function crewWith(meta: Pick<TitleDetail, 'crew'> | undefined | null, jobs: string[]): CrewMember[] {
  const out: CrewMember[] = [];
  const seen = new Set<number>();
  for (const job of jobs) {
    for (const c of meta?.crew ?? []) {
      if (c.job === job && !seen.has(c.personId)) {
        seen.add(c.personId);
        out.push(c);
      }
    }
  }
  return out;
}

/** Crew jobs grouped into the credit lines a title page shows. */
export const CREDIT_LINES: { label: string; plural: string; jobs: string[] }[] = [
  { label: 'Director', plural: 'Directors', jobs: ['Director'] },
  { label: 'Creator', plural: 'Creators', jobs: ['Creator'] },
  { label: 'Writer', plural: 'Writers', jobs: ['Screenplay', 'Writer', 'Story', 'Novel'] },
  { label: 'Music', plural: 'Music', jobs: ['Original Music Composer'] },
  { label: 'Cinematography', plural: 'Cinematography', jobs: ['Director of Photography'] },
];

/** A film and the files of it in the library (versions: 4K and 1080p, a director's cut). */
export interface TitleGroup {
  key: string;
  /** The version a card opens: the one being watched, else the sharpest. */
  item: ItemSummary;
  versions: ItemSummary[];
}

function rank(i: ItemSummary): number {
  const started = i.progress && !i.progress.watched && i.progress.positionSec > 0 ? 1 : 0;
  return started * 1e6 + (i.height || 0);
}

/**
 * Movies grouped by matched title: several files of one film make one card.
 * Unmatched files stay on their own. Order follows the first file of each
 * group in items.
 */
export function groupByTitle(items: ItemSummary[]): TitleGroup[] {
  const groups = new Map<string, TitleGroup>();
  for (const it of items) {
    const key = it.kind === 'movie' && it.titleId ? `t${it.titleId}` : `i${it.id}`;
    const g = groups.get(key);
    if (!g) {
      groups.set(key, { key, item: it, versions: [it] });
      continue;
    }
    g.versions.push(it);
    if (rank(it) > rank(g.item)) g.item = it;
  }
  for (const g of groups.values()) g.versions.sort((a, b) => (b.height || 0) - (a.height || 0) || a.id - b.id);
  return [...groups.values()];
}

/**
 * The first item of each film in list (files matched to one title), in order:
 * the newest version when list is newest first, the one played last in
 * Continue. Episodes and unmatched files all stay.
 */
export function onePerTitle(list: ItemSummary[]): ItemSummary[] {
  const seen = new Set<number>();
  return list.filter((i) => i.kind !== 'movie' || !i.titleId || (!seen.has(i.titleId) && !!seen.add(i.titleId)));
}

/** The Movies page's filters, applied to a whole film. */
export interface FilmFilter {
  is4k: (i: ItemSummary) => boolean;
  isHdr: (i: ItemSummary) => boolean;
  filters: string[];
}

/**
 * Whether a film passes the filters: 4K or HDR when any version is, both when one version is both
 * (a 4K SDR file beside a 1080p HDR one is neither 4K HDR); Unwatched when the film is not watched:
 * an item's `watched` is its film's, across all its versions (a film seen in 1080p that also has a
 * 4K file is a watched film).
 */
export function filmPasses(g: TitleGroup, f: FilmFilter): boolean {
  const picture = [f.filters.includes('4k') ? f.is4k : null, f.filters.includes('hdr') ? f.isHdr : null].filter((p) => p !== null);
  return (
    (!picture.length || g.versions.some((v) => picture.every((p) => p(v)))) &&
    (!f.filters.includes('unwatched') || !g.item.watched)
  );
}

/**
 * Whether titles are being looked up on TMDB: lookups are on, there is a key to make them with, and
 * a pass is running or titles are waiting. With no key (or lookups off) the queue never drains and
 * `running` only means a pass over the local files, so none of it is "matching".
 *
 * With a TVDB key and no TMDB key, shows are matched while a pass runs; the movies stay pending for
 * ever (they need TMDB), so pending alone is not matching then.
 */
export function isMatching(
  s: Pick<MetadataStatus, 'enabled' | 'keySet' | 'running' | 'pending' | 'tvdbKeySet'> | undefined | null,
): boolean {
  if (!s || !s.enabled) return false;
  if (s.keySet) return s.running || s.pending > 0;
  return !!s.tvdbKeySet && s.running;
}

/** A short label telling versions apart: "4K HDR10, MKV", "1080p, MP4". */
export function versionLabel(i: ItemSummary): string {
  const h = i.height || 0;
  const res = h >= 2000 ? '4K' : h >= 1400 ? '1440p' : h >= 1000 ? '1080p' : h >= 700 ? '720p' : h ? `${h}p` : '';
  const hdr = i.hdr ? (typeof i.hdr === 'string' ? i.hdr.toUpperCase() : 'HDR') : '';
  return [[res, hdr].filter(Boolean).join(' '), i.container?.toUpperCase()].filter(Boolean).join(', ');
}
