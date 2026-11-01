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

export interface ItemSummary {
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
  progress?: Progress;
  /** Artwork links; null/absent when the server has none for this item. */
  art?: ArtUrls | null;
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
  key: string;
  language?: string;
  title?: string;
  format: string;
  image: boolean;
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
}

export interface ShowSummary {
  show: string;
  seasons: number[];
  episodeCount: number;
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

  // M5 (progress / continue watching) — present for the contract, unused by the UI so far.
  putProgress: (id: number, positionSec: number, durationSec: number) =>
    request<void>('PUT', `/api/items/${id}/progress`, { positionSec, durationSec }),

  async continueWatching(): Promise<ItemSummary[]> {
    return (await request<ItemSummary[] | null>('GET', '/api/continue')) ?? [];
  },
};
