import { ApiError } from './api';

/** How often a player beats while its session is open (the server stops one 30 s after its last beat). */
export const HEARTBEAT_MS = 10_000;

export interface HeartbeatOptions {
  /** POST /api/sessions/{sid}/heartbeat; rejects with ApiError. */
  send: (sid: string, positionSec: number, paused: boolean) => Promise<unknown>;
  /** Where the player is, in file seconds. */
  position: () => number;
  paused: () => boolean;
  /** The server no longer has the session (it answered 404): start another at `positionSec`. */
  onGone: (sid: string, positionSec: number, paused: boolean) => void;
  /** Someone stopped the session on purpose (410): do not start another on its own. */
  onStopped: (sid: string, positionSec: number) => void;
}

/**
 * Keeps an HLS session alive (docs/M9-ARCHITECTURE.md, "Orphan-proof playback"). It beats every
 * 10 s whether the player is playing or not: a paused player asks for no segments, and without
 * beats the server would take its session for an abandoned one. A 404 means the session is gone
 * (onGone), a 410 that someone stopped it (onStopped): either is called once, and the beats stop
 * until start() is given the next session. Any other failure (no network, a 5xx) is left to the
 * next beat.
 */
export class Heartbeat {
  private readonly o: HeartbeatOptions;
  private sid: string | undefined;
  private timer: ReturnType<typeof setInterval> | undefined;

  constructor(o: HeartbeatOptions) {
    this.o = o;
  }

  /** The session being kept alive, if any. */
  get session(): string | undefined {
    return this.sid;
  }

  /** Beat for `sid` every 10 s from now on, until stop() or a 404. */
  start(sid: string): void {
    this.stop();
    this.sid = sid;
    this.timer = setInterval(() => this.beat(), HEARTBEAT_MS);
  }

  /** Beat now (on pause and play). Does nothing without a session. */
  beat(): void {
    const sid = this.sid;
    if (!sid) return;
    const p = this.o.position();
    const positionSec = Number.isFinite(p) && p > 0 ? p : 0;
    const paused = this.o.paused();
    this.o.send(sid, positionSec, paused).catch((e: unknown) => {
      // Only the session this beat was for, and only once: beats in flight may all come back 404.
      if (!(e instanceof ApiError) || (e.status !== 404 && e.status !== 410) || this.sid !== sid) return;
      this.stop();
      if (e.status === 410) this.o.onStopped(sid, positionSec);
      else this.o.onGone(sid, positionSec, paused);
    });
  }

  /** No more beats (the player is closing, or moving to another session). */
  stop(): void {
    clearInterval(this.timer);
    this.timer = undefined;
    this.sid = undefined;
  }
}
