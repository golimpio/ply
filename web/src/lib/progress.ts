/**
 * Where playback is, as far as the server's resume point is concerned.
 *
 * One instance follows one `<video>` through any number of streams (a new audio track, a burned-in
 * subtitle, an expired session, the page coming back from the back/forward cache). Each stream is a
 * generation: `begin(target)` says where it is meant to start, and nothing is reported until the
 * element has really got there. That is the whole point: between generations the element holds no
 * position at all (unloading it makes `currentTime` 0 and fires `timeupdate`/`pause` for it), and
 * the server deletes progress below 30 s, so a report from that gap erases a resume point.
 *
 *   begin(target)  ->  [loading: nothing counts]  ->  armed (media at target)  ->  end()
 *
 * `position()` is always the place to start the next generation from: the live position while armed,
 * else the last one that counted in this generation, else the target it was asked to start at.
 */
export interface MediaLike {
  currentTime: number;
  duration: number;
  readyState: number;
  seeking: boolean;
  currentSrc: string;
}

/** HTMLMediaElement.HAVE_METADATA: the element knows its duration and can be positioned. */
const HAVE_METADATA = 1;

/** How close to the target counts as having got there (hls.js lands on a keyframe; a seek is not exact). */
const ARRIVED = 3;

export class Progress {
  /** Position the current generation was asked to start at. */
  target = 0;
  private armed = false;
  private resume = 0;
  private lastSent = -1;

  private readonly media: () => MediaLike | undefined;
  private readonly send: (positionSec: number, durationSec: number, keepalive: boolean) => void;
  private readonly fallbackDuration: () => number;

  constructor(
    media: () => MediaLike | undefined,
    send: (positionSec: number, durationSec: number, keepalive: boolean) => void,
    /** Duration to report when the element does not know one. */
    fallbackDuration: () => number = () => 0,
  ) {
    this.media = media;
    this.send = send;
    this.fallbackDuration = fallbackDuration;
  }

  /** A stream is about to load, meant to begin at `target`. Whatever the last one was doing no longer counts. */
  begin(target: number): void {
    this.target = target;
    this.resume = target;
    this.armed = false;
    this.lastSent = -1;
  }

  /** The element is showing a loaded stream, not the empty state after unloading (readyState 0, time 0). */
  private live(m: MediaLike): boolean {
    return m.readyState >= HAVE_METADATA && m.currentSrc !== '' && Number.isFinite(m.currentTime);
  }

  /** Call on every `timeupdate`. */
  update(): void {
    const m = this.media();
    if (!m || !this.live(m)) return;
    if (!this.armed && !m.seeking && m.currentTime >= this.target - ARRIVED) this.armed = true;
    if (this.armed) this.resume = m.currentTime;
  }

  /** Where the next stream should start from. */
  position(): number {
    const m = this.media();
    return this.armed && m && this.live(m) ? m.currentTime : this.resume;
  }

  /** Tell the server where we are, if that is a position that means something. */
  report(keepalive = false): void {
    const m = this.media();
    if (!this.armed || !m || !this.live(m) || m.currentTime === this.lastSent) return;
    this.lastSent = m.currentTime;
    const duration = Number.isFinite(m.duration) && m.duration > 0 ? m.duration : this.fallbackDuration();
    this.send(m.currentTime, duration, keepalive);
  }

  /** The stream is going away: keep its position, and let nothing report until the next `begin`. */
  end(): void {
    this.resume = this.position();
    this.armed = false;
  }
}
