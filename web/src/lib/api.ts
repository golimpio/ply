// Typed client for the ply HTTP API (see docs/DESIGN.md "HTTP API").
import { normaliseRating, normaliseRatings, RATING_SOURCES } from './ratings';

export type Kind = 'movie' | 'episode';
export type Method = 'direct' | 'remux' | 'transcode';
export type Action = 'copy' | 'transcode';

export interface Info {
  id: string;
  name: string;
  version: string;
  authRequired: boolean;
}

export interface Progress {
  positionSec: number;
  watched: boolean;
}

export interface ArtUrls {
  backdrop: string;
  thumb: string;
  /** Dominant colour as "#rrggbb", once the artwork exists. */
  color?: string;
}

/** A source of ratings (M10). */
export type RatingSource = 'imdb' | 'tmdb' | 'rt_critics' | 'rt_audience' | 'metacritic' | 'letterboxd' | 'trakt';

/**
 * A rating from one source (M10, docs/M10-RATINGS.md). `value` is on the source's own `scale` (IMDb
 * 10, Letterboxd 5, Metacritic 100); `score100` is value / scale × 100, rounded, and is what lists
 * sort and filter by. `state` only on Rotten Tomatoes' two: fresh, rotten or certified (critics),
 * upright, spilled or verified_hot (audience). `url` is '' when unknown; `fetchedAt` is RFC 3339.
 */
export interface RatingEntry {
  source: RatingSource;
  value: number;
  scale: number;
  score100: number;
  votes?: number;
  state?: string;
  url: string;
  fetchedAt: string;
}

/** Where a file stands with the metadata matcher (M6). */
export type MatchState = 'pending' | 'matched' | 'unmatched' | 'manual' | 'disabled';

/**
 * What list rows (items, shows) know about their title. The image fields are server-relative URLs
 * (with ?token= when the server needs one); `poster` is 2:3 and sized for a card (w342, see
 * imageUrl for other sizes).
 */
export interface MetaFields {
  titleId?: number;
  /** What a movie or a show is listed under ("Matrix, The"); not on an episode. See lib/az. */
  sortTitle?: string;
  poster?: string;
  backdrop?: string;
  logo?: string;
  /** The primary source's rating, else the first there is in the fixed order (M10); null for none. */
  rating?: RatingEntry | null;
  certification?: string;
  genres?: string[];
  matchState: MatchState;
  /** The matcher was not sure: show the "Needs attention" hint. */
  needsReview: boolean;
}

export interface ItemSummary extends Partial<MetaFields> {
  id: number;
  kind: Kind;
  title: string;
  year?: number;
  show?: string;
  season?: number;
  episode?: number;
  episodeTitle?: string;
  durationSec: number;
  container: string;
  videoCodec: string;
  audioCodec: string;
  width: number;
  height: number;
  /** The contract does not pin the shape: bool or a label such as "hdr10" / "dv" / "hlg". */
  hdr?: boolean | string | null;
  addedAt: number;
  /** Resume state; null (or absent) when the item has not been started. */
  progress?: Progress | null;
  /** Artwork links; null/absent when the server has none for this item. */
  art?: ArtUrls | null;
  /** An episode's overview and still, when the metadata has them. */
  overview?: string;
  still?: string;
  /** An episode's own ratings (its IMDb rating, or []); absent on movies. */
  ratings?: RatingEntry[];
  /**
   * Watch state (M11): the file's, or for a film with a title the film's, across all its versions.
   * watchedAt is unix seconds, null when not watched; playCount 0 then.
   */
  watched?: boolean;
  watchedAt?: number | null;
  playCount?: number;
}

/** A card's rating: the entry without its link, votes and fetch time. `state` only on Rotten Tomatoes'. */
export type CardRating = Pick<RatingEntry, 'source' | 'value' | 'scale' | 'score100' | 'state'>;

/** A card's frame-grab artwork without the backdrop: the thumb, and its colour once the picture exists. */
export type CardArt = Pick<ArtUrls, 'thumb' | 'color'>;

/**
 * A movie as GET /api/items?kind=movie&fields=card sends it (internal/api/card.go): the full row's
 * fields, with the same names and shapes, reduced to what a grid needs. Anything else is on the
 * item's detail. `art` and `rating` are always sent, null for none.
 */
export interface MovieCard
  extends Pick<ItemSummary, 'id' | 'kind' | 'title' | 'year' | 'titleId' | 'addedAt' | 'poster' | 'backdrop' | 'durationSec' | 'container' | 'width' | 'height' | 'genres'>,
    Required<Pick<ItemSummary, 'sortTitle' | 'watched' | 'progress' | 'matchState' | 'needsReview'>> {
  // Not picked: the full row's art and rating carry more fields than these.
  art: CardArt | null;
  rating: CardRating | null;
  hdr?: string;
}

/**
 * An episode as GET /api/items?kind=episode&fields=card sends it (internal/api/card.go): what a show's
 * list, the home page's rails and the search draw, and no more. It is the fields a page reads, so a
 * full row (ItemSummary) fits it too, which is what the rails that mix both kinds need. `art` and
 * `rating` are always sent, null for none; anything else an episode has is on the item's detail.
 */
export type EpisodeCard = Pick<
  ItemSummary,
  | 'id'
  | 'kind'
  | 'title'
  | 'year'
  | 'show'
  | 'season'
  | 'episode'
  | 'episodeTitle'
  | 'addedAt'
  | 'durationSec'
  | 'still'
  | 'overview'
  | 'width'
  | 'height'
  | 'hdr'
  | 'watched'
  | 'watchedAt'
  | 'playCount'
  | 'progress'
> & {
  art?: CardArt | null;
  /** The episode's own ratings as a card carries them: without the url, the votes and the fetch time. */
  ratings?: CardRating[];
};

/** One file a PUT .../watched covered, as it is now; progress is the file's own (null: not started). */
export interface ItemWatch {
  id: number;
  watched: boolean;
  watchedAt: number | null;
  playCount: number;
  progress: Progress | null;
}

/** The answer to every PUT .../watched: what was asked for, and every file it covered. */
export interface WatchResult {
  watched: boolean;
  items: ItemWatch[];
}

export interface Stream {
  index: number;
  type: 'video' | 'audio' | 'subtitle' | string;
  codec: string;
  codecTag?: string;
  profile?: string;
  width?: number;
  height?: number;
  pixFmt?: string;
  colorTransfer?: string;
  channels?: number;
  language?: string;
  title?: string;
  default?: boolean;
  forced?: boolean;
}

export interface Subtitle {
  /** The ffprobe stream index of an embedded track, or ext<N> for a sidecar file. */
  key: string;
  language?: string;
  title?: string;
  format: string;
  /** A bitmap track (PGS, VobSub, DVB): it can only be burned into the video (subtitleIndex). */
  image: boolean;
  forced?: boolean;
  default?: boolean;
  /** WebVTT for a <track>; set for text tracks only (with ?token= when the server needs one). */
  url?: string;
}

export interface Playback {
  method: Method;
  videoAction: Action;
  audioAction: Action;
  audioIndex: number;
  reasons: string[];
  /**
   * Relative server URL: the file for direct play, an HLS playlist for remux and transcode. Empty
   * when there is none: GET /api/items/{id} only describes a remux or transcode, POST /playback
   * starts it (or the server has streaming disabled; the last reason says so).
   */
  url: string;
  /** Set when the server opened an HLS session; DELETE /api/sessions/{id} ends it. */
  sessionId?: string;
  /** The requested resume position; the HLS timeline is the file's, so seek the player there. */
  startSec: number;
}

export interface ItemDetail extends ItemSummary {
  path: string;
  size: number;
  bitrate: number;
  streams: Stream[];
  subtitles: Subtitle[];
  playback: Playback;
  /** The movie's title, or the show's for an episode. Absent when nothing is known. */
  meta?: TitleDetail;
  /** An episode's own metadata. (Not `episode`: that is the episode number.) */
  episodeInfo?: EpisodeInfo;
}

export interface ShowSummary extends Partial<MetaFields> {
  show: string;
  seasons: number[];
  /** Episodes, the specials apart (unless there is nothing else); of them watched, and started. */
  episodeCount: number;
  watchedEpisodes?: number;
  inProgress?: number;
  year?: number;
  overview?: string;
}

// ---- metadata (M6) -----------------------------------------------------------

export type TitleKind = 'movie' | 'series';

export interface TitleCard {
  id: number;
  kind: TitleKind;
  title: string;
  /** 0 when unknown. */
  year: number;
  poster?: string;
  backdrop?: string;
  /** The primary source's rating, else the first there is in the fixed order (M10); null for none. */
  rating?: RatingEntry | null;
  /** Movies: the first file. */
  itemId?: number;
  /** Shows: the show's name in the library. */
  show?: string;
  /** Shows: when the next episode airs (YYYY-MM-DD). May be past until the show is next refreshed. */
  nextAirDate?: string;
  /** A film: watched in any version; a series: in all its episodes (M11). */
  watched?: boolean;
  /** A series: how far through its episodes. */
  episodeCount?: number;
  watchedEpisodes?: number;
  inProgress?: number;
}

export interface Genre {
  id: number;
  name: string;
}

export interface CastMember {
  personId: number;
  name: string;
  character?: string;
  profile?: string;
}

export interface CrewMember {
  personId: number;
  name: string;
  job: string;
  profile?: string;
}

/** Where a title's details come from: TVDB or TMDB, an .nfo file, or TMDB with an .nfo on top. */
export type TitleSource = 'tvdb' | 'tmdb' | 'nfo' | 'mixed';

/** How a TVDB show's files map to its episodes. */
export type EpisodeOrder = 'aired' | 'dvd' | 'absolute';

export interface TitleDetail extends TitleCard {
  tmdbId?: number;
  tvdbId?: number;
  imdbId?: string;
  originalTitle?: string;
  sortTitle: string;
  overview?: string;
  tagline?: string;
  releaseDate?: string;
  endDate?: string;
  status?: string;
  runtimeMin?: number;
  voteCount?: number;
  certification?: string;
  logo?: string;
  genres: Genre[];
  collection?: { id: number; name: string };
  cast: CastMember[];
  crew: CrewMember[];
  source: TitleSource;
  /** Only for a show whose source is 'tvdb': the order PATCH /api/titles/{id} can change. */
  episodeOrder?: EpisodeOrder;
  /** Only in the answer to a PATCH that changed the order: the episode files that have no episode in it. */
  unmapped?: number;
  /** The user edited the text: a refresh keeps it. */
  locked: boolean;
  matchState: MatchState;
  needsReview: boolean;
  /** Every rating the title has (M10), in the fixed order: IMDb, RT critics, RT audience, Metacritic, Letterboxd, Trakt, TMDB. */
  ratings?: RatingEntry[];
}

/** The episode of a show that airs next. */
export interface NextEpisode {
  season: number;
  episode: number;
  /** Empty when TMDB has no name for it yet. */
  name: string;
}

/** A show of the library with an episode still to air (GET /api/upcoming). */
export interface UpcomingShow {
  titleId: number;
  /** The show's name in the library. */
  show: string;
  poster?: string;
  backdrop?: string;
  /** YYYY-MM-DD, today or later. */
  nextAirDate: string;
  /** Absent when TMDB does not say which episode it is. */
  nextEpisode?: NextEpisode;
}

export interface EpisodeInfo {
  name?: string;
  overview?: string;
  airDate?: string;
  still?: string;
  /** The episode's TMDB score out of 10, as before M10. */
  rating?: number;
  /** The episode's IMDb rating, or [] (M10). */
  ratings?: RatingEntry[];
}

export interface PersonCard {
  id: number;
  name: string;
  profile?: string;
  knownFor?: string;
}

export interface PersonCredit {
  titleId: number;
  kind: 'cast' | 'crew';
  job?: string;
  character?: string;
  title: TitleCard;
}

/** A trailer or teaser of a title (GET /api/titles/{id}/trailers), by its key at the site that plays it. */
export interface Trailer {
  site: 'youtube' | 'vimeo';
  key: string;
  name?: string;
  type?: 'trailer' | 'teaser';
  language?: string;
  official: boolean;
  /** YYYY-MM-DD */
  publishedAt?: string;
}

export interface Person extends PersonCard {
  biography?: string;
  birthday?: string;
  deathday?: string;
  placeOfBirth?: string;
  imdbId?: string;
  tmdbId?: number;
  tvdbId?: number;
  /** Only titles in the library, newest first. */
  credits: PersonCredit[];
}

export interface GenreCount extends Genre {
  count: number;
}

export interface CollectionDetail {
  id: number;
  name: string;
  overview?: string;
  poster?: string;
  backdrop?: string;
  /** Release order. */
  titles: TitleCard[];
}

export interface SeasonInfo {
  season: number;
  name: string;
  overview?: string;
  poster?: string;
  airDate?: string;
  /** Episodes in the library (one that has two files is one). */
  episodeCount: number;
  /** Of them, watched. */
  watchedEpisodes: number;
  /** Of them, started and not watched. */
  inProgress: number;
}

export interface ShowDetail extends Partial<MetaFields> {
  show: string;
  episodeCount: number;
  watchedEpisodes?: number;
  inProgress?: number;
  year?: number;
  overview?: string;
  meta?: TitleDetail;
  seasons: SeasonInfo[];
}

/** A TVDB or TMDB title the "fix match" dialog offers. */
export interface MatchCandidate {
  source: 'tvdb' | 'tmdb';
  /** The id at its source; of tmdbId and tvdbId only the one of the source is set. */
  id: number;
  tmdbId?: number;
  tvdbId?: number;
  title: string;
  originalTitle?: string;
  year: number;
  overview?: string;
  poster?: string;
  /** How well it fits what was asked, 0 to 1. */
  score: number;
}

export interface ReviewEntry {
  kind: TitleKind;
  itemId?: number;
  show?: string;
  title: string;
  year: number;
  matchState: MatchState;
  needsReview: boolean;
  score: number;
  candidate?: TitleCard;
  /** A show's episodes the scanner flagged: their file names have no episode number. */
  episodes?: { id: number; path: string }[];
}

export interface MetadataStatus {
  enabled: boolean;
  keySet: boolean;
  running: boolean;
  pending: number;
  unmatched: number;
  needsReview: number;
  lastRefresh?: string | null;
  lastError: string;
  /** There is a TVDB key (keySet is TMDB's only). Absent from a server older than M8. */
  tvdbKeySet?: boolean;
  /** Where shows come from: 'tvdb', 'tmdb' (no TVDB key) or '' (nothing may be asked). */
  shows?: 'tvdb' | 'tmdb' | '';
  /** The sentence for the status line ("Shows: TVDB", "TVDB unreachable"), shown as it is; '' for none. */
  showsLine?: string;
}

export type KeySource = 'flag' | 'env' | 'settings' | 'default' | '';

/** GET /api/settings. The key itself is never in it. */
export interface Settings {
  tmdbKeySet: boolean;
  tmdbKeySource: KeySource;
  metadataEnabled: boolean;
  /** -metadata=false was given: the switch cannot be turned on here. */
  metadataForcedOff: boolean;
  language: string;
  /**
   * Only in the answer to a PUT that stored a key: whether the key was checked against TMDB.
   * It is not with -metadata=false (nothing is sent to TMDB then), and may be refused later.
   */
  tmdbKeyChecked?: boolean;
  /** How many plays may run at once, direct ones included (absent from a server older than M9). */
  maxStreams?: number;
  /** How many of them may be transcodes (conversions on the CPU or GPU). */
  maxTranscodes?: number;
  /** 'flag': set on the command line or in the environment, so it cannot be changed here (409). */
  maxStreamsSource?: LimitSource;
  maxTranscodesSource?: LimitSource;
  /** TheTVDB (M8; absent from an older server). The key and the PIN are never in it either. */
  tvdbKeySet?: boolean;
  tvdbPinSet?: boolean;
  tvdbKeySource?: KeySource;
  /** 'flag' or 'env': the PIN can't be changed or removed here (409). Absent from servers before it was added. */
  tvdbPinSource?: 'flag' | 'env' | 'settings' | '';
  /** Like tmdbKeyChecked, for a TVDB key (a PIN alone does not set it). */
  tvdbKeyChecked?: boolean;
  /** The notice TVDB's terms ask for, to be shown as it is, with a link to tvdbAttributionUrl. */
  tvdbAttribution?: string;
  tvdbAttributionUrl?: string;
  /** MDBList (M10; absent from an older server): Rotten Tomatoes, Metacritic, Letterboxd and Trakt. */
  mdblistKeySet?: boolean;
  mdblistKeySource?: KeySource;
  /** Like tmdbKeyChecked, for an MDBList key. */
  mdblistKeyChecked?: boolean;
  /** The source whose rating cards show. */
  ratingsPrimary?: RatingSource;
  /** The sources a detail page shows, in order. */
  ratingsShown?: RatingSource[];
  /** The sources this install can have, given its keys and the privacy switch. */
  ratingsAvailable?: RatingSource[];
  /** The notices to show as they are, each with a link to its URL. */
  imdbAttribution?: string;
  imdbAttributionUrl?: string;
  mdblistAttribution?: string;
  mdblistAttributionUrl?: string;
  /** When the IMDb dataset in use was published (RFC 3339); null until the first download. */
  imdbDatasetUpdatedAt?: string | null;
  /**
   * The last test of the key in use (a Test, or a save that checked it), in unix seconds: 0 when it
   * was never tested or the key has changed since. ...KeyTestOk is false when there is no test.
   * Absent from a server before they were added.
   */
  tmdbKeyTestedAt?: number;
  tmdbKeyTestOk?: boolean;
  tvdbKeyTestedAt?: number;
  tvdbKeyTestOk?: boolean;
  mdblistKeyTestedAt?: number;
  mdblistKeyTestOk?: boolean;
}

export type LimitSource = 'flag' | 'settings' | 'default';

export interface SettingsUpdate {
  /** "" removes the stored key. The server checks a new key against TMDB. */
  tmdbKey?: string;
  metadataEnabled?: boolean;
  language?: string;
  /** 1..32; ApiError 400 out of range, 409 when the command line set it. */
  maxStreams?: number;
  /** 1..16; the same. */
  maxTranscodes?: number;
  /** "" removes the stored key. The server checks a new key with a login at TVDB. */
  tvdbKey?: string;
  /** The subscriber PIN that goes with a user-supported key; "" removes it. */
  tvdbPin?: string;
  /** "" removes the stored key. The server checks a new key with MDBList. */
  mdblistKey?: string;
  ratingsPrimary?: RatingSource;
  /** Each source once, in order; [] shows none. */
  ratingsShown?: RatingSource[];
}

/** The settings with Go's nulls in the ratings lists as []. */
export function normaliseSettings(s: Settings): Settings {
  if (s.ratingsPrimary === undefined) return s; // a server older than M10
  return { ...s, ratingsShown: s.ratingsShown ?? [], ratingsAvailable: s.ratingsAvailable ?? [] };
}

/**
 * A list row (or a card) with its rating normalised, when it has one (a server older than M10
 * sends none): an entry of a source the page does not know as null, an episode's ratings null as [].
 */
function normaliseRow<T extends { rating?: unknown; ratings?: unknown; kind?: string }>(r: T): T {
  const out = { ...r };
  if ('rating' in r) out.rating = normaliseRating(r.rating as RatingEntry | null);
  if ('ratings' in r || (r.kind === 'episode' && 'rating' in r)) out.ratings = normaliseRatings(r.ratings as RatingEntry[] | null);
  return out;
}

/** A card with a rating the page cannot show, without copying it: only a row whose rating is of a
 * source this page does not know is copied, with the rating null. */
function normaliseCard<T extends object>(c: T): T {
  const r = 'rating' in c ? (c as { rating?: CardRating | null }).rating : null;
  return r && (typeof r !== 'object' || !RATING_SOURCES.includes(r.source)) ? ({ ...c, rating: null } as T) : c;
}

function normaliseTitle<T extends TitleDetail>(t: T): T {
  const out = normaliseRow({ ...t, genres: t.genres ?? [], cast: t.cast ?? [], crew: t.crew ?? [] });
  if ('ratings' in t) out.ratings = normaliseRatings(t.ratings);
  return out;
}

/** The services whose key POST /api/settings/test can check. */
export type KeyProvider = 'tmdb' | 'tvdb' | 'mdblist';

/** POST /api/settings/test: whether the key in use works. error never holds the key. */
export interface KeyTest {
  ok: boolean;
  error?: string;
}

/**
 * What to link a file or show to: a TMDB, IMDb or TVDB id, or what was typed to name a show at
 * TVDB ("tvdb:81189", a thetvdb.com address), which the server reads.
 */
export type MatchRef = { tmdbId: number } | { imdbId: string } | { tvdbId: number } | { tvdb: string };

export interface TitlePatch {
  title?: string;
  sortTitle?: string;
  year?: number;
  overview?: string;
  tagline?: string;
  /** A TVDB show only: re-maps every season at once (it does not lock the title). */
  episodeOrder?: EpisodeOrder;
}

export interface WatchRoot {
  root: string;
  watching: boolean;
  dirs: number;
  error?: string;
}

export interface Profile {
  name: string;
  containers: string[];
  videoCodecs: string[];
  audioCodecs: string[];
  requireHvc1: boolean;
  maxHeight: number;
  hls: boolean;
  textSubs: string[];
}

export interface PlaybackRequest {
  profile?: string;
  custom?: Profile;
  startSec?: number;
  audioIndex?: number;
  subtitleIndex?: number;
  subtitleKey?: string;
}

export interface ScanStatus {
  running: boolean;
  lastStarted?: string | null;
  lastFinished?: string | null;
  lastError?: string | null;
  itemCount: number;
  /** Per media root: is the folder being watched for changes. Empty when watching is off. */
  watching?: WatchRoot[];
}

// ---- library folders (M7) -------------------------------------------------------

/** What every file of a library folder is: never mixed. */
export type FolderKind = 'movies' | 'shows';

export interface LibraryFolder {
  /** 0 for a -media folder whose type the first scan has not guessed yet: it can't be changed or removed. */
  id: number;
  path: string;
  /** '' until a -media folder's type is guessed. */
  kind: FolderKind | '';
  /** The type was guessed from the file names, not chosen. */
  guessed: boolean;
  /** 'flag': given on the command line, so it can't be removed here (it would come back at the next start). */
  source: 'flag' | 'ui';
  /** The type was given on the command line (-movies, -shows). */
  kindLocked: boolean;
  items: number;
  /** Unix seconds of the last scan that covered all of the folder without trouble; 0 for none. */
  lastScanAt: number;
  state: 'ok' | 'missing' | 'error';
  error: string;
}

export interface DirEntry {
  name: string;
  path: string;
}

/** GET /api/fs: the directories of `path`, or with no path the places to start from. */
export interface DirListing {
  path: string;
  /** '' at the top. */
  parent: string;
  dirs: DirEntry[];
  places?: DirEntry[];
}

// ---- background queue (M9) -----------------------------------------------------

export type JobState = 'queued' | 'running' | 'done' | 'failed' | 'cancelled';

/** A job of the background queue. A negative id is work outside the queue (no actions). */
export interface Job {
  id: number;
  kind: string;
  key?: string;
  title: string;
  /** Lower runs first: 0 a person's request, 10 new files, 20 their details, 30 scans, 40 refreshes, 50 caches. */
  priority: number;
  state: JobState;
  /** A person asked for it (a button, a bump, a retry). */
  user: boolean;
  done: number;
  /** 0 when the amount of work is not known. */
  total: number;
  note?: string;
  /** Why it waits for playback or browsing, while it does. */
  held?: string;
  attempts: number;
  error?: string;
  createdAt: string;
  /** A retry: not before this. */
  runAt?: string;
  startedAt?: string;
  finishedAt?: string;
}

/** What playback is doing. Capitalised: the Go struct has no JSON tags. */
export interface PlaybackLoad {
  Sessions: number;
  /** Of the sessions, not paused. */
  Active: number;
  Transcodes: number;
  /** Of the transcodes, on the CPU. */
  Software: number;
  /** Segment requests blocked on ffmpeg: a viewer is stalled. */
  Waiting: number;
  Paths: string[];
}

/** The tiers above the queue: playback, then browsing. */
export interface TierState {
  playback: PlaybackLoad;
  browsing: boolean;
  /** What holds light background work right now. */
  hold: { held: boolean; reason?: string };
}

/** GET /api/jobs, and the first event of GET /api/events. */
export interface JobsSnapshot {
  paused: boolean;
  /** Started, including those still held; work outside the queue (negative ids) last. */
  running: Job[];
  /** In the order they will start. */
  queued: Job[];
  /** Newest first, at most 200. */
  history: Job[];
  tier: TierState;
}

/** An event of GET /api/events other than a snapshot. */
export interface JobEvent {
  seq: number;
  type: string;
  job?: Job;
  tier?: TierState;
}

/** Something being played (GET /api/sessions). */
export interface PlayingSession {
  id: string;
  sessionId: string;
  itemId: number;
  title: string;
  kind: Method;
  client: string;
  startedAt: string;
  lastSeenAt: string;
  positionSec: number;
  paused: boolean;
}

/** The tiers with Go's nulls and missing fields filled in. */
export function normaliseTier(t: Partial<TierState> | null | undefined): TierState {
  const p: Partial<PlaybackLoad> = t?.playback ?? {};
  return {
    playback: {
      Sessions: p.Sessions ?? 0,
      Active: p.Active ?? 0,
      Transcodes: p.Transcodes ?? 0,
      Software: p.Software ?? 0,
      Waiting: p.Waiting ?? 0,
      Paths: p.Paths ?? [],
    },
    browsing: !!t?.browsing,
    hold: { held: !!t?.hold?.held, reason: t?.hold?.reason || undefined },
  };
}

/** A snapshot with Go's null slices as [] (and an empty one for null). */
export function normaliseSnapshot(s: Partial<JobsSnapshot> | null | undefined): JobsSnapshot {
  return {
    paused: !!s?.paused,
    running: s?.running ?? [],
    queued: s?.queued ?? [],
    history: s?.history ?? [],
    tier: normaliseTier(s?.tier),
  };
}

export class ApiError extends Error {
  readonly status: number;
  /** Seconds from a Retry-After header given in seconds (a 503 while the server is busy); else undefined. */
  readonly retryAfter?: number;
  /** The JSON body of the answer, when it had one (the 503 at a play limit says what is playing). */
  readonly body?: unknown;
  constructor(status: number, message: string, retryAfter?: number, body?: unknown) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.retryAfter = retryAfter;
    this.body = body;
  }
}

function retryAfterOf(res: Response): number | undefined {
  const v = res.headers.get('Retry-After')?.trim();
  return v && /^\d+$/.test(v) ? Number(v) : undefined;
}

// ---- token -----------------------------------------------------------------

const TOKEN_KEY = 'ply.token';

function readStoredToken(): string {
  try {
    return localStorage.getItem(TOKEN_KEY) ?? '';
  } catch {
    return '';
  }
}

let token = readStoredToken();
let unauthorized: (() => void) | null = null;

export function getToken(): string {
  return token;
}

export function setToken(value: string): void {
  token = value;
  try {
    if (value) localStorage.setItem(TOKEN_KEY, value);
    else localStorage.removeItem(TOKEN_KEY);
  } catch {
    // storage unavailable (private mode): keep the in-memory token only
  }
}

export function clearToken(): void {
  setToken('');
}

/** Called when any request is answered with 401. */
export function onUnauthorized(fn: () => void): void {
  unauthorized = fn;
}

/**
 * Resolve a server-relative URL (stream, file, subtitle) to an absolute one and
 * attach ?token= so <video>/hls.js requests, which cannot send headers, authenticate.
 */
export function mediaUrl(rel: string): string {
  const u = new URL(rel, location.origin);
  if (token && !u.searchParams.has('token')) u.searchParams.set('token', token);
  return u.toString();
}

/** The TMDB image sizes the server serves (its allowlist, TVDB's images too), by width. */
export const IMAGE_SIZES = { w92: 92, w185: 185, w342: 342, w500: 500, w780: 780, w1280: 1280 } as const;
export type ImageSize = keyof typeof IMAGE_SIZES | 'original';

const TMDB_IMAGE = /^(\/api\/images\/tmdb\/)(w92|w185|w342|w500|w780|w1280|original)(\/[A-Za-z0-9_-]+\.(?:jpg|png|svg))(\?.*)?$/;
// TVDB's paths have directories (/banners/posters/81189-1.jpg). As the server's TVDBImagePath: letters,
// digits, "_", "." and "-"; no "." segment and no ".." anywhere; a jpg, jpeg or png in any case.
const TVDB_IMAGE =
  /^(\/api\/images\/tvdb\/)(w92|w185|w342|w500|w780|w1280|original)(?![^?]*\.\.)((?:\/(?!\.(?:\/|$))[A-Za-z0-9_.-]+)+\.(?:[jJ][pP][eE]?[gG]|[pP][nN][gG]))(\?.*)?$/;

const sizedImage = (rel: string) => TMDB_IMAGE.exec(rel) ?? TVDB_IMAGE.exec(rel);

/**
 * An image URL from the API as an absolute URL with the token, optionally at another size (the
 * server links posters at w342, backdrops at w1280; TMDB and TVDB images alike). Sidecar and other
 * URLs keep their size. Empty or missing gives ''.
 */
export function imageUrl(rel: string | null | undefined, size?: ImageSize): string {
  if (!rel) return '';
  const m = size ? sizedImage(rel) : null;
  return mediaUrl(m ? `${m[1]}${size}${m[3]}${m[4] ?? ''}` : rel);
}

/**
 * A srcset for a TMDB or TVDB image at the given sizes ("…/w185/x.jpg 185w, …"); for any other URL
 * a single candidate, so that the attribute is still valid.
 */
export function imageSrcset(rel: string | null | undefined, sizes: Exclude<ImageSize, 'original'>[] = ['w185', 'w342', 'w500']): string {
  if (!rel) return '';
  if (!sizedImage(rel)) return mediaUrl(rel);
  return sizes.map((sz) => `${imageUrl(rel, sz)} ${IMAGE_SIZES[sz]}w`).join(', ');
}

/** A show at TVDB, named by its id or (an address with no id) by its slug. */
export interface TvdbRef {
  /** 0 when only the slug is known. */
  id: number;
  slug: string;
}

/** What the "fix match" box was given: an id, and the kind of title when the link says so. */
export interface MatchInput {
  ref: MatchRef;
  /** From a themoviedb.org /movie/ or /tv/ link, or anything TVDB's (shows only); absent for IMDb ids and tmdb:N. */
  kind?: TitleKind;
  /** For a TVDB reference: what it names, to say so. */
  tvdb?: TvdbRef;
}

const TVDB_SLUG = /^[A-Za-z0-9][A-Za-z0-9_-]{0,199}$/;
// Up to 15 digits: the server reads an int64; past that a number in JS is no longer exact.
const positiveId = (s: string | null) => (s && /^\d{1,15}$/.test(s) && Number(s) > 0 ? Number(s) : 0);

/**
 * Reads a show named at TVDB the way the server does (metadata.ParseTVDBRef): "tvdb:81189",
 * "TVDB : 81189", "tvdb 81189" or "tvdb81189", or the address of a series at thetvdb.com ("thetvdb.com/series/breaking-bad",
 * "https://www.thetvdb.com/series/81189/allseasons/official", the old "?tab=series&id=81189"),
 * with or without the scheme. null for anything else, an address at any other host included.
 */
export function parseTvdbRef(input: string): TvdbRef | null {
  const s = input.trim();
  const tagged = /^tvdb\s*:?\s*(\d{1,10})$/i.exec(s);
  // The server refuses any port, the default one too, which URL would drop without a word.
  const authority = /^(?:[A-Za-z][A-Za-z0-9+.-]*:\/\/)?([^/?#]*)/.exec(s)?.[1] ?? '';
  if (tagged) {
    const id = positiveId(tagged[1]);
    return id ? { id, slug: '' } : null;
  }
  if (!s || /\s/.test(s)) return null;
  let u: URL;
  try {
    u = new URL(s.includes('://') ? s : `https://${s}`);
  } catch {
    return null;
  }
  if ((u.protocol !== 'https:' && u.protocol !== 'http:') || u.username || u.password || u.port || authority.includes(':')) return null;
  if (u.hostname !== 'thetvdb.com' && u.hostname !== 'www.thetvdb.com') return null;
  let segs = u.pathname.replace(/^\/+|\/+$/g, '').split('/');
  if (segs.length >= 2 && segs[0] === 'dereferrer') segs = segs.slice(1);
  if (segs.length >= 2 && segs[0] === 'series') {
    let name: string;
    try {
      name = decodeURIComponent(segs[1]); // the server reads the decoded path
    } catch {
      return null;
    }
    const id = positiveId(name);
    if (id) return { id, slug: '' };
    return TVDB_SLUG.test(name) && !/^\d+$/.test(name) ? { id: 0, slug: name.toLowerCase() } : null;
  }
  if (u.searchParams.get('tab') === 'series') {
    const id = positiveId(u.searchParams.get('id'));
    if (id) return { id, slug: '' };
  }
  return null;
}

/**
 * Reads what a person pastes into the "fix match" box: a themoviedb.org movie or tv URL, an
 * imdb.com title URL, an IMDb id (tt0133093), a TMDB id said as such ("tmdb:603", "tmdb 603"), or
 * a TVDB show (see parseTvdbRef). A bare number is not an id: it is a title to search for ("1917",
 * "300", "24"). null when the input is none of those.
 *
 * A TVDB reference is sent as typed ({tvdb}) for the server to read, an address as it is and an id
 * as "tvdb:N" (the form every version of the server takes).
 */
export function parseMatchInput(input: string): MatchInput | null {
  const s = input.trim();
  const imdb = /(?:^|[^A-Za-z0-9])(tt\d{7,8})(?![A-Za-z0-9])/i.exec(s);
  if (imdb && (/imdb\.com/i.test(s) || /^tt\d{7,8}$/i.test(s))) return { ref: { imdbId: imdb[1].toLowerCase() } };
  const url = /themoviedb\.org\/(movie|tv)\/(\d{1,10})(?!\d)/i.exec(s);
  if (url) return { ref: { tmdbId: Number(url[2]) }, kind: url[1].toLowerCase() === 'tv' ? 'series' : 'movie' };
  const tagged = /^tmdb(?:\s*:\s*|\s+)(\d{1,10})$/i.exec(s);
  if (tagged && Number(tagged[1]) > 0) return { ref: { tmdbId: Number(tagged[1]) } };
  const tvdb = parseTvdbRef(s);
  if (tvdb) return { ref: { tvdb: /^tvdb/i.test(s) ? `tvdb:${tvdb.id}` : s }, kind: 'series', tvdb };
  return null;
}

/** What choosing a search result sends: the id of its own source. */
export function candidateRef(c: Pick<MatchCandidate, 'source' | 'id' | 'tmdbId' | 'tvdbId'>): MatchRef {
  return c.source === 'tvdb' ? { tvdbId: c.tvdbId || c.id } : { tmdbId: c.tmdbId || c.id };
}

/** A search result with its source and id filled in (a server older than M8 sends tmdbId only). */
function normaliseCandidate(c: Partial<MatchCandidate> & { title: string }): MatchCandidate {
  const source = c.source === 'tvdb' ? 'tvdb' : 'tmdb';
  const id = c.id || (source === 'tvdb' ? c.tvdbId : c.tmdbId) || 0;
  return { year: 0, score: 0, ...c, source, id };
}

/**
 * The person a 404 of GET /api/people/{id} says the asked-for one was merged into
 * ({"error": "merged", "into": N}), or 0 for any other error, a plain 404 included.
 */
export function mergedInto(e: unknown): number {
  if (!(e instanceof ApiError) || e.status !== 404) return 0;
  const b = e.body as { error?: unknown; into?: unknown } | null | undefined;
  return b?.error === 'merged' && typeof b.into === 'number' && Number.isInteger(b.into) && b.into > 0 ? b.into : 0;
}

/** The id in what was pasted into the "fix match" box (see parseMatchInput), or null. */
export function parseMatchRef(input: string): MatchRef | null {
  return parseMatchInput(input)?.ref ?? null;
}

// ---- transport -------------------------------------------------------------

/** The message of an ApiError (status 0) for a request given up on after its timeout. */
export const NO_ANSWER = 'The server didn’t answer.';

/**
 * timeoutMs (optional, 0 for none): give up on a request the server has not answered by then, as
 * ApiError(0, NO_ANSWER). The answer's body is read within the same time.
 */
async function request<T>(method: string, path: string, body?: unknown, keepalive = false, timeoutMs = 0): Promise<T> {
  const headers: Record<string, string> = {};
  if (token) headers.Authorization = `Bearer ${token}`;
  if (body !== undefined) headers['Content-Type'] = 'application/json';

  const abort = timeoutMs > 0 ? new AbortController() : null;
  const timer = abort ? setTimeout(() => abort.abort(), timeoutMs) : undefined;
  try {
    return await send<T>(method, path, headers, body, keepalive, abort?.signal);
  } catch (e) {
    if (abort?.signal.aborted) throw new ApiError(0, NO_ANSWER);
    throw e;
  } finally {
    clearTimeout(timer);
  }
}

async function send<T>(
  method: string,
  path: string,
  headers: Record<string, string>,
  body: unknown,
  keepalive: boolean,
  signal: AbortSignal | undefined,
): Promise<T> {
  let res: Response;
  try {
    res = await fetch(path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      keepalive,
      ...(signal ? { signal } : {}),
    });
  } catch {
    throw new ApiError(0, 'Cannot reach the ply server');
  }

  if (res.status === 401) {
    clearToken();
    unauthorized?.();
  }
  if (!res.ok) {
    let message = res.statusText || `HTTP ${res.status}`;
    let body: unknown;
    try {
      body = await res.json();
      const j = body as { error?: string } | null;
      if (j?.error) message = j.error;
    } catch {
      // non-JSON error body
    }
    throw new ApiError(res.status, message, retryAfterOf(res), body);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

async function watchRequest(path: string, watched: boolean): Promise<WatchResult> {
  const r = await request<WatchResult>('PUT', path, { watched });
  return { watched: r.watched, items: r.items ?? [] };
}

function qs(params: Record<string, string | undefined>): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v) p.set(k, v);
  const s = p.toString();
  return s ? `?${s}` : '';
}

// ---- endpoints -------------------------------------------------------------

export const api = {
  info: () => request<Info>('GET', '/api/info'),

  // Go encodes nil slices as null; normalise so callers can iterate safely.
  async items(params: { kind?: Kind; show?: string } = {}): Promise<ItemSummary[]> {
    return ((await request<ItemSummary[] | null>('GET', `/api/items${qs(params)}`)) ?? []).map(normaliseRow);
  },

  /** The movies as the grid needs them (a third of the full rows' weight); the rest is on api.item. */
  async movieCards(): Promise<MovieCard[]> {
    return ((await request<MovieCard[] | null>('GET', '/api/items?kind=movie&fields=card')) ?? []).map(normaliseCard);
  },

  /** The episodes as a show's list and the rails need them: 358 bytes a row against the full row's 885. */
  async episodeCards(): Promise<EpisodeCard[]> {
    return ((await request<EpisodeCard[] | null>('GET', '/api/items?kind=episode&fields=card')) ?? []).map(normaliseCard);
  },

  async shows(): Promise<ShowSummary[]> {
    const list = (await request<ShowSummary[] | null>('GET', '/api/shows')) ?? [];
    return list.map((s) => normaliseRow({ ...s, seasons: s.seasons ?? [] }));
  },

  /** `playback` in the result is for a built-in profile (avplayer), not for this browser. */
  async item(id: number, target?: string): Promise<ItemDetail> {
    const d = await request<ItemDetail>('GET', `/api/items/${id}${qs({ target })}`);
    // Defensive: if the server opened a session just to describe this decision, release it.
    if (d.playback?.sessionId) void api.deleteSession(d.playback.sessionId);
    return {
      ...normaliseRow(d),
      streams: d.streams ?? [],
      subtitles: d.subtitles ?? [],
      playback: d.playback && { ...d.playback, reasons: d.playback.reasons ?? [] },
      ...(d.meta ? { meta: normaliseTitle(d.meta) } : {}),
      // episodeInfo.rating is TMDB's number: only its ratings list is normalised.
      ...(d.episodeInfo
        ? { episodeInfo: { ...d.episodeInfo, ...('ratings' in d.episodeInfo ? { ratings: normaliseRatings(d.episodeInfo.ratings) } : {}) } }
        : {}),
    };
  },

  async playback(id: number, req: PlaybackRequest): Promise<Playback> {
    const p = await request<Playback>('POST', `/api/items/${id}/playback`, req);
    return { ...p, reasons: p.reasons ?? [], url: p.url ?? '', startSec: p.startSec ?? 0 };
  },

  /** Start a library scan. Resolves 'running' if one is already in progress (409). */
  async startScan(): Promise<'started' | 'running'> {
    try {
      await request<{ started: boolean }>('POST', '/api/scan');
      return 'started';
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) return 'running';
      throw e;
    }
  },

  scanStatus: () => request<ScanStatus>('GET', '/api/scan'),

  /** Stop an HLS session. keepalive lets it complete while the page is unloading. */
  async deleteSession(sid: string): Promise<void> {
    try {
      await request<void>('DELETE', `/api/sessions/${encodeURIComponent(sid)}`, undefined, true);
    } catch {
      // best effort: the server also reaps idle sessions
    }
  },

  /**
   * Keep an HLS session alive: every 10 s while it is open, paused or not, and at once on pause and
   * play (lib/heartbeat.ts). ApiError 404: the session is gone, start another.
   */
  async heartbeat(sid: string, positionSec: number, paused: boolean, wireless?: boolean): Promise<void> {
    await request<void>('POST', `/api/sessions/${encodeURIComponent(sid)}/heartbeat`, { positionSec, paused, wireless });
  },

  /**
   * Report where playback is. The server answers with the resulting state (null when the position
   * is too early to count as started). keepalive lets it complete while the page is unloading.
   */
  putProgress: (id: number, positionSec: number, durationSec: number, keepalive = false) =>
    request<Progress | null>('PUT', `/api/items/${id}/progress`, { positionSec, durationSec }, keepalive),

  // Watch state (M11): each answers 400 without `watched`, 404 for what is not there.

  /** An episode, or a file no title is known for; a film with a title is marked in all its versions. */
  setItemWatched: (id: number, watched: boolean) => watchRequest(`/api/items/${id}/watched`, watched),
  /** Every version of a film, or every episode of a series. */
  setTitleWatched: (id: number, watched: boolean) => watchRequest(`/api/titles/${id}/watched`, watched),
  setShowWatched: (show: string, watched: boolean) => watchRequest(`/api/shows/${encodeURIComponent(show)}/watched`, watched),
  /** Season 0 is the specials. */
  setSeasonWatched: (show: string, season: number, watched: boolean) =>
    watchRequest(`/api/shows/${encodeURIComponent(show)}/seasons/${season}/watched`, watched),

  async continueWatching(): Promise<ItemSummary[]> {
    return ((await request<ItemSummary[] | null>('GET', '/api/continue')) ?? []).map(normaliseRow);
  },

  // ---- metadata (M6) ----

  /** A show with its seasons (names, posters and air dates when known) and its title. */
  async show(name: string): Promise<ShowDetail> {
    const d = await request<ShowDetail>('GET', `/api/shows/${encodeURIComponent(name)}`);
    return { ...normaliseRow(d), seasons: d.seasons ?? [], ...(d.meta ? { meta: normaliseTitle(d.meta) } : {}) };
  },

  /** People with a title in the library, by name (at most 20). */
  async people(q: string): Promise<PersonCard[]> {
    return (await request<PersonCard[] | null>('GET', `/api/people${qs({ q })}`)) ?? [];
  },

  async person(id: number): Promise<Person> {
    const p = await request<Person>('GET', `/api/people/${id}`);
    return { ...p, credits: (p.credits ?? []).map((c) => ({ ...c, title: normaliseRow(c.title) })) };
  },

  /** A title's trailers, best first; [] when it has none or online lookups are off. */
  async trailers(titleId: number): Promise<Trailer[]> {
    return (await request<Trailer[] | null>('GET', `/api/titles/${titleId}/trailers`)) ?? [];
  },

  async genres(): Promise<GenreCount[]> {
    return (await request<GenreCount[] | null>('GET', '/api/genres')) ?? [];
  },

  /** Titles of the library, narrowed by genre, collection and/or person (ids). */
  async titles(f: { genre?: number; collection?: number; person?: number } = {}): Promise<TitleCard[]> {
    // An id of 0 is no id (the server would answer 400): leave it out.
    const q = qs({
      genre: f.genre ? String(f.genre) : undefined,
      collection: f.collection ? String(f.collection) : undefined,
      person: f.person ? String(f.person) : undefined,
    });
    return ((await request<TitleCard[] | null>('GET', `/api/titles${q}`)) ?? []).map(normaliseRow);
  },

  async collection(id: number): Promise<CollectionDetail> {
    const c = await request<CollectionDetail>('GET', `/api/collections/${id}`);
    return { ...c, titles: (c.titles ?? []).map(normaliseRow) };
  },

  /** Shows of the library whose next episode airs today or later, soonest first (at most 50). */
  async upcoming(): Promise<UpcomingShow[]> {
    return (await request<UpcomingShow[] | null>('GET', '/api/upcoming')) ?? [];
  },

  /**
   * Edit a title's text; the title is then locked, so that a refresh keeps the edit. With
   * episodeOrder (a TVDB show) every season is mapped again and the answer has `unmapped`;
   * ApiError 409 for no TVDB key or no episodes in that order, 502 when TVDB can't be reached.
   */
  async patchTitle(id: number, patch: TitlePatch): Promise<TitleDetail> {
    return normaliseTitle(await request<TitleDetail>('PATCH', `/api/titles/${id}`, patch));
  },

  // fixing matches

  /**
   * Titles a query could mean, best first; for shows with TVDB on, TVDB's before TMDB's. "tvdb:81189"
   * or a thetvdb.com address gives that one show. 409 (ApiError) when lookups (or TVDB) are off.
   */
  async searchMetadata(kind: TitleKind, q: string, year?: number): Promise<MatchCandidate[]> {
    const path = `/api/metadata/search${qs({ kind, q, year: year ? String(year) : undefined })}`;
    const list = (await request<(Partial<MatchCandidate> & { title: string })[] | null>('GET', path)) ?? [];
    return list.map(normaliseCandidate);
  },

  /** Link a movie file to a TMDB title (by hand: it is never re-matched on its own). */
  matchItem: (id: number, ref: MatchRef) => request<ItemDetail>('PUT', `/api/items/${id}/match`, ref),

  /** Back to what the file name says; not matched again on its own. */
  unmatchItem: (id: number) => request<ItemDetail>('DELETE', `/api/items/${id}/match`),

  matchShow: (name: string, ref: MatchRef) =>
    request<ShowDetail>('PUT', `/api/shows/${encodeURIComponent(name)}/match`, ref),

  unmatchShow: (name: string) => request<ShowDetail>('DELETE', `/api/shows/${encodeURIComponent(name)}/match`),

  // refreshing (all answer at once; the work is done in the background)

  async refreshItem(id: number): Promise<void> {
    await request<{ started: boolean }>('POST', `/api/items/${id}/metadata/refresh`);
  },

  async refreshShow(name: string): Promise<void> {
    await request<{ started: boolean }>('POST', `/api/shows/${encodeURIComponent(name)}/metadata/refresh`);
  },

  async refreshAll(): Promise<void> {
    await request<{ started: boolean }>('POST', '/api/metadata/refresh');
  },

  metadataStatus: () => request<MetadataStatus>('GET', '/api/metadata/status'),

  /** Movies and shows nothing was found for, or that the matcher was unsure about. */
  async review(): Promise<ReviewEntry[]> {
    return (await request<ReviewEntry[] | null>('GET', '/api/metadata/review')) ?? [];
  },

  // ---- library folders (M7) ----

  async folders(): Promise<LibraryFolder[]> {
    return (await request<LibraryFolder[] | null>('GET', '/api/folders')) ?? [];
  },

  /** A scan of the folder starts at once. ApiError 400 (bad path, overlap) and 409 (already there) say why. */
  addFolder: (path: string, kind: FolderKind) => request<LibraryFolder>('POST', '/api/folders', { path, kind }),

  /** Reads the folder's files again as the new type; progress is kept. 409 for a type set on the command line. */
  setFolderKind: (id: number, kind: FolderKind) => request<LibraryFolder>('PATCH', `/api/folders/${id}`, { kind }),

  /**
   * Takes the folder out of the library. Without `forget` its progress and fixed matches are kept
   * and come back if it is added again. 503 when a scan is busy: nothing was removed.
   */
  async removeFolder(id: number, forget = false): Promise<void> {
    await request<void>('DELETE', `/api/folders/${id}${forget ? '?forget=1' : ''}`);
  },

  /** Directories only, dot-folders left out. With no path: home, mounted disks and the library folders. */
  async listDirs(path?: string): Promise<DirListing> {
    const d = await request<DirListing>('GET', `/api/fs${qs({ path })}`);
    return { ...d, path: d.path ?? '', parent: d.parent ?? '', dirs: d.dirs ?? [], places: d.places ?? undefined };
  },

  settings: async () => normaliseSettings(await request<Settings>('GET', '/api/settings')),

  /**
   * Change the TMDB key, the online switch or the language. A new key is checked against TMDB:
   * ApiError 400 says why it was refused, 502 that TMDB could not be reached. With timeoutMs, an
   * answer that takes longer is ApiError(0, NO_ANSWER) (the server may still have stored it).
   */
  saveSettings: async (u: SettingsUpdate, timeoutMs = 0) =>
    normaliseSettings(await request<Settings>('PUT', '/api/settings', u, false, timeoutMs)),

  /** Checks the key in use (TVDB: a login, with the PIN). A key that does not work is { ok: false }, not an error. */
  async testKey(provider: KeyProvider): Promise<KeyTest> {
    const r = await request<Partial<KeyTest> | null>('POST', '/api/settings/test', { provider });
    return { ok: r?.ok === true, ...(r?.error ? { error: r.error } : {}) };
  },

  // ---- background queue (M9) ----
  // Actions: ApiError 404 for a job the queue no longer has, 409 when its state does not allow it.

  async jobs(): Promise<JobsSnapshot> {
    return normaliseSnapshot(await request<JobsSnapshot | null>('GET', '/api/jobs'));
  },

  /** Stops a running job, or drops a queued one. */
  async cancelJob(id: number): Promise<void> {
    await request<void>('POST', `/api/jobs/${id}/cancel`);
  },

  /** Moves a queued job to the front. */
  async bumpJob(id: number): Promise<void> {
    await request<void>('POST', `/api/jobs/${id}/bump`);
  },

  /** Queues a finished job again; the answer is the new job. */
  retryJob: (id: number) => request<Job>('POST', `/api/jobs/${id}/retry`),

  /** Nothing new starts; running jobs finish. */
  async pauseQueue(): Promise<void> {
    await request<void>('POST', '/api/queue/pause');
  },

  async resumeQueue(): Promise<void> {
    await request<void>('POST', '/api/queue/resume');
  },

  /** Everything being played, oldest first. */
  async sessions(): Promise<PlayingSession[]> {
    return (await request<PlayingSession[] | null>('GET', '/api/sessions')) ?? [];
  },

  /** Stops something being played; unlike deleteSession, failures are thrown. */
  async stopSession(id: string): Promise<void> {
    await request<void>('DELETE', `/api/sessions/${encodeURIComponent(id)}`);
  },
};
