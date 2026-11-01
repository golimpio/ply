/** The part of an hls.js error event that tells whether the server has dropped the session. */
export interface HlsErrorInfo {
  fatal: boolean;
  type: string;
  response?: { code?: number };
}

/** Starting a new session this many times in a row without playback running is not going to help. */
export const MAX_SESSION_RESTARTS = 2;

/**
 * A network error answered with 404 means the session is gone: the server ends sessions nobody has
 * asked anything of for a while (-stream-idle), and a paused player with a full buffer asks for
 * nothing. Retrying the same URL cannot help, a new playback at the current position can. It does
 * not wait for the error to turn fatal: hls.js retries a 404 fragment for around half a minute
 * first. Other network errors (no response at all, 5xx) are left to the usual retries.
 */
export function sessionGone(data: HlsErrorInfo): boolean {
  return data.type === 'networkError' && data.response?.code === 404;
}
