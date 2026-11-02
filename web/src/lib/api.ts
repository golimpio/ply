// Typed client for the ply HTTP API (see docs/DESIGN.md "HTTP API").

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

/** Where a file stands with the metadata matcher (M6). */
export type MatchState = 'pending' | 'matched' | 'unmatched' | 'manual' | 'disabled';

/**
 * What list rows (items, shows) know about their title. The image fields are server-relative URLs
 * (with ?token= when the server needs one); `poster` is 2:3 and sized for a card (w342, see
 * imageUrl for other sizes).
 */
export interface MetaFields {
  titleId?: number;
  poster?: string;
  backdrop?: string;
  logo?: string;
  rating?: number;
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
  episodeCount: number;
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
  rating?: number;
  /** Movies: the first file. */
  itemId?: number;
  /** Shows: the show's name in the library. */
  show?: string;
  /** Shows: when the next episode airs (YYYY-MM-DD). May be past until the show is next refreshed. */
  nextAirDate?: string;
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

export interface TitleDetail extends TitleCard {
  tmdbId?: number;
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
  source: 'tmdb' | 'nfo' | 'mixed';
  /** The user edited the text: a refresh keeps it. */
  locked: boolean;
  matchState: MatchState;
  needsReview: boolean;
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
  rating?: number;
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

export interface Person extends PersonCard {
  biography?: string;
  birthday?: string;
  deathday?: string;
  placeOfBirth?: string;
  imdbId?: string;
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
  /** Episode files in the library. */
  episodeCount: number;
}

export interface ShowDetail extends Partial<MetaFields> {
  show: string;
  episodeCount: number;
  year?: number;
  overview?: string;
  meta?: TitleDetail;
  seasons: SeasonInfo[];
}

/** A TMDB title the "fix match" dialog offers. */
export interface MatchCandidate {
  tmdbId: number;
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
}

export interface SettingsUpdate {
  /** "" removes the stored key. The server checks a new key against TMDB. */
  tmdbKey?: string;
  metadataEnabled?: boolean;
  language?: string;
}

/** A TMDB or IMDb id, to link a file or show to. */
export type MatchRef = { tmdbId: number } | { imdbId: string };

export interface TitlePatch {
  title?: string;
  sortTitle?: string;
  year?: number;
  overview?: string;
  tagline?: string;
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

export class ApiError extends Error {
  readonly status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
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

/** The TMDB image sizes the server serves (its allowlist), by width. */
export const IMAGE_SIZES = { w92: 92, w185: 185, w342: 342, w500: 500, w780: 780, w1280: 1280 } as const;
export type ImageSize = keyof typeof IMAGE_SIZES | 'original';

const TMDB_IMAGE = /^(\/api\/images\/tmdb\/)(w92|w185|w342|w500|w780|w1280|original)(\/[A-Za-z0-9_-]+\.(?:jpg|png|svg))(\?.*)?$/;

/**
 * An image URL from the API as an absolute URL with the token, optionally at another TMDB size
 * (the server links posters at w342, backdrops at w1280). Sidecar and other URLs keep their size.
 * Empty or missing gives ''.
 */
export function imageUrl(rel: string | null | undefined, size?: ImageSize): string {
  if (!rel) return '';
  const m = size ? TMDB_IMAGE.exec(rel) : null;
  return mediaUrl(m ? `${m[1]}${size}${m[3]}${m[4] ?? ''}` : rel);
}

/**
 * A srcset for a TMDB image at the given sizes ("…/w185/x.jpg 185w, …"); for any other URL a
 * single candidate, so that the attribute is still valid.
 */
export function imageSrcset(rel: string | null | undefined, sizes: Exclude<ImageSize, 'original'>[] = ['w185', 'w342', 'w500']): string {
  if (!rel) return '';
  if (!TMDB_IMAGE.test(rel)) return mediaUrl(rel);
  return sizes.map((sz) => `${imageUrl(rel, sz)} ${IMAGE_SIZES[sz]}w`).join(', ');
}

/** What the "fix match" box was given: an id, and the kind of title when the link says so. */
export interface MatchInput {
  ref: MatchRef;
  /** From a themoviedb.org /movie/ or /tv/ link; absent for IMDb ids and tmdb:N. */
  kind?: TitleKind;
}

/**
 * Reads what a person pastes into the "fix match" box: a themoviedb.org movie or tv URL, an
 * imdb.com title URL, an IMDb id (tt0133093), or a TMDB id said as such ("tmdb:603", "tmdb 603").
 * A bare number is not an id: it is a title to search for ("1917", "300", "24"). null when the
 * input is none of those.
 */
export function parseMatchInput(input: string): MatchInput | null {
  const s = input.trim();
  const imdb = /(?:^|[^A-Za-z0-9])(tt\d{7,8})(?![A-Za-z0-9])/i.exec(s);
  if (imdb && (/imdb\.com/i.test(s) || /^tt\d{7,8}$/i.test(s))) return { ref: { imdbId: imdb[1].toLowerCase() } };
  const url = /themoviedb\.org\/(movie|tv)\/(\d{1,10})(?!\d)/i.exec(s);
  if (url) return { ref: { tmdbId: Number(url[2]) }, kind: url[1].toLowerCase() === 'tv' ? 'series' : 'movie' };
  const tagged = /^tmdb(?:\s*:\s*|\s+)(\d{1,10})$/i.exec(s);
  if (tagged && Number(tagged[1]) > 0) return { ref: { tmdbId: Number(tagged[1]) } };
  return null;
}

/** The id in what was pasted into the "fix match" box (see parseMatchInput), or null. */
export function parseMatchRef(input: string): MatchRef | null {
  return parseMatchInput(input)?.ref ?? null;
}

// ---- transport -------------------------------------------------------------

async function request<T>(method: string, path: string, body?: unknown, keepalive = false): Promise<T> {
  const headers: Record<string, string> = {};
  if (token) headers.Authorization = `Bearer ${token}`;
  if (body !== undefined) headers['Content-Type'] = 'application/json';

  let res: Response;
  try {
    res = await fetch(path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      keepalive,
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
    try {
      const j = (await res.json()) as { error?: string };
      if (j?.error) message = j.error;
    } catch {
      // non-JSON error body
    }
    throw new ApiError(res.status, message);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
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
    return (await request<ItemSummary[] | null>('GET', `/api/items${qs(params)}`)) ?? [];
  },

  async shows(): Promise<ShowSummary[]> {
    const list = (await request<ShowSummary[] | null>('GET', '/api/shows')) ?? [];
    return list.map((s) => ({ ...s, seasons: s.seasons ?? [] }));
  },

  /** `playback` in the result is for a built-in profile (avplayer), not for this browser. */
  async item(id: number, target?: string): Promise<ItemDetail> {
    const d = await request<ItemDetail>('GET', `/api/items/${id}${qs({ target })}`);
    // Defensive: if the server opened a session just to describe this decision, release it.
    if (d.playback?.sessionId) void api.deleteSession(d.playback.sessionId);
    return {
      ...d,
      streams: d.streams ?? [],
      subtitles: d.subtitles ?? [],
      playback: d.playback && { ...d.playback, reasons: d.playback.reasons ?? [] },
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
   * Report where playback is. The server answers with the resulting state (null when the position
   * is too early to count as started). keepalive lets it complete while the page is unloading.
   */
  putProgress: (id: number, positionSec: number, durationSec: number, keepalive = false) =>
    request<Progress | null>('PUT', `/api/items/${id}/progress`, { positionSec, durationSec }, keepalive),

  async continueWatching(): Promise<ItemSummary[]> {
    return (await request<ItemSummary[] | null>('GET', '/api/continue')) ?? [];
  },

  // ---- metadata (M6) ----

  /** A show with its seasons (names, posters and air dates when known) and its title. */
  async show(name: string): Promise<ShowDetail> {
    const d = await request<ShowDetail>('GET', `/api/shows/${encodeURIComponent(name)}`);
    return { ...d, seasons: d.seasons ?? [] };
  },

  /** People with a title in the library, by name (at most 20). */
  async people(q: string): Promise<PersonCard[]> {
    return (await request<PersonCard[] | null>('GET', `/api/people${qs({ q })}`)) ?? [];
  },

  async person(id: number): Promise<Person> {
    const p = await request<Person>('GET', `/api/people/${id}`);
    return { ...p, credits: p.credits ?? [] };
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
    return (await request<TitleCard[] | null>('GET', `/api/titles${q}`)) ?? [];
  },

  async collection(id: number): Promise<CollectionDetail> {
    const c = await request<CollectionDetail>('GET', `/api/collections/${id}`);
    return { ...c, titles: c.titles ?? [] };
  },

  /** Shows of the library whose next episode airs today or later, soonest first (at most 50). */
  async upcoming(): Promise<UpcomingShow[]> {
    return (await request<UpcomingShow[] | null>('GET', '/api/upcoming')) ?? [];
  },

  /** Edit a title's text; the title is then locked, so that a refresh keeps the edit. */
  patchTitle: (id: number, patch: TitlePatch) => request<TitleDetail>('PATCH', `/api/titles/${id}`, patch),

  // fixing matches

  /** TMDB titles a query could mean, best first. 409 (ApiError) when lookups are off. */
  async searchMetadata(kind: TitleKind, q: string, year?: number): Promise<MatchCandidate[]> {
    const path = `/api/metadata/search${qs({ kind, q, year: year ? String(year) : undefined })}`;
    return (await request<MatchCandidate[] | null>('GET', path)) ?? [];
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

  settings: () => request<Settings>('GET', '/api/settings'),

  /**
   * Change the TMDB key, the online switch or the language. A new key is checked against TMDB:
   * ApiError 400 says why it was refused, 502 that TMDB could not be reached.
   */
  saveSettings: (u: SettingsUpdate) => request<Settings>('PUT', '/api/settings', u),
};
