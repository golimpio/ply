import { ApiError, type PlayingSession } from './api';

/** A play refused because the server is at one of its limits (503 from POST /playback or GET /file). */
export interface PlayLimit {
  /** 'streams': all plays together; 'transcodes': conversions on the CPU or GPU. */
  limit: 'streams' | 'transcodes';
  max: number;
  inUse: number;
  /** What is using the limit (for 'transcodes', the transcodes only). Empty for a cross-site page. */
  playing: PlayingSession[];
  /** Seconds to wait before asking again (Retry-After). */
  retryAfter: number;
}

/** Retry-After when the server gives none. */
const DEFAULT_RETRY_SEC = 5;

/** Times in a row the cap panel tries again on its own before it waits for the viewer. */
export const AUTO_RETRIES = 12;

const seconds = (v: unknown) => (typeof v === 'number' && Number.isFinite(v) && v >= 0 ? v : 0);
const count = (v: unknown) => Math.floor(seconds(v));

function isPlaying(v: unknown): v is PlayingSession {
  const p = v as PlayingSession | null;
  return !!p && typeof p === 'object' && typeof p.id === 'string' && p.id !== '';
}

/** The body of a 503 at a limit, or null for any other body (a 503 for another reason). */
export function parseLimit(body: unknown, retryAfter?: number): PlayLimit | null {
  if (!body || typeof body !== 'object') return null;
  const b = body as Record<string, unknown>;
  if (b.limit !== 'streams' && b.limit !== 'transcodes') return null;
  return {
    limit: b.limit,
    max: count(b.max),
    inUse: count(b.inUse),
    playing: (Array.isArray(b.playing) ? b.playing.filter(isPlaying) : []).map((p) => ({
      ...p,
      title: p.title || 'Untitled',
      client: p.client ?? '',
      positionSec: seconds(p.positionSec),
      paused: !!p.paused,
    })),
    retryAfter: retryAfter && retryAfter > 0 ? retryAfter : DEFAULT_RETRY_SEC,
  };
}

/** The limit an API call was refused for, or null when it failed for another reason. */
export function limitOf(e: unknown): PlayLimit | null {
  return e instanceof ApiError && e.status === 503 ? parseLimit(e.body, e.retryAfter) : null;
}

/** Why the server refused media: at a limit (503), or stopped on purpose by someone (410). */
export type Refusal = { kind: 'limit'; limit: PlayLimit } | { kind: 'stopped' };

/**
 * Why a media URL the <video> element failed on (a direct play's file, a native HLS playlist) was
 * refused: the element only says that it failed. Asks for one byte of it; null when it is served,
 * refused for another reason, or cannot be reached at all.
 */
export async function probeMedia(url: string): Promise<Refusal | null> {
  try {
    const res = await fetch(url, { headers: { Range: 'bytes=0-0' } });
    if (res.status === 410) return { kind: 'stopped' };
    if (res.status !== 503) {
      void res.body?.cancel();
      return null;
    }
    const after = res.headers.get('Retry-After')?.trim();
    const limit = parseLimit(await res.json(), after && /^\d+$/.test(after) ? Number(after) : undefined);
    return limit && { kind: 'limit', limit };
  } catch {
    return null;
  }
}

const videos = (n: number) => `${n} ${n === 1 ? 'video' : 'videos'}`;

/** What the cap panel says: "2 videos are already playing, the most this server allows." */
export function limitMessage(l: PlayLimit): string {
  const n = l.inUse || l.max;
  if (l.limit === 'transcodes') {
    return l.inUse > l.max
      ? `ply is already converting ${videos(n)}, more than the ${l.max} it allows.`
      : `ply is already converting ${videos(n)}, the most it allows.`;
  }
  const verb = n === 1 ? 'is' : 'are';
  return l.inUse > l.max
    ? `${videos(n)} ${verb} already playing, more than the ${l.max} this server allows.`
    : `${videos(n)} ${verb} already playing, the most this server allows.`;
}
