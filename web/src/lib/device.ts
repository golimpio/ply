// This browser as a device of ply (docs/M11-CLIENTS.md, "Devices"): one id, made the first time and
// kept, sent with the app's requests so the server can tell its plays and its feed apart. It names no
// one: the server names a browser from its User-Agent.

/** The header the id travels in (an EventSource, which cannot send headers, uses ?device=). */
export const DEVICE_HEADER = 'X-Ply-Device';

const KEY = 'ply.device';
/** What a device id is (the server ignores anything else). */
export const DEVICE_ID = /^[A-Za-z0-9_-]{8,64}$/;

let cached = '';

function newId(): string {
  if (typeof crypto.randomUUID === 'function') return crypto.randomUUID();
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  return Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
}

/** This browser's device id: the one it was given before, else a new one, kept for next time. */
export function deviceId(): string {
  if (cached) return cached;
  let id = '';
  try {
    id = localStorage.getItem(KEY) ?? '';
  } catch {
    // storage unavailable: a new id, for as long as the page lives
  }
  if (!DEVICE_ID.test(id)) {
    id = newId();
    try {
      localStorage.setItem(KEY, id);
    } catch {
      // kept until reload
    }
  }
  cached = id;
  return id;
}

const ATTEMPT_KEY = 'ply.attempt';
let lastAttempt = 0;

/**
 * The number to send with this device's next POST /playback (docs/M11-CLIENTS.md, "Devices"): larger than any
 * it sent before, from this tab, another one or before a reload, so that of two asks the server handles out of
 * order it keeps the newer. The clock's milliseconds, made to go up even when the clock goes back.
 */
export function nextAttempt(): number {
  let stored = 0;
  try {
    const s = Number(localStorage.getItem(ATTEMPT_KEY));
    // Only what the server takes (a whole number below 2^53): anything else would stop every play.
    if (Number.isSafeInteger(s) && s > 0 && s < Number.MAX_SAFE_INTEGER) stored = s;
  } catch {
    // storage unavailable: this page's own count still goes up
  }
  const n = Math.max(Date.now(), lastAttempt + 1, stored + 1);
  lastAttempt = n;
  try {
    localStorage.setItem(ATTEMPT_KEY, String(n));
  } catch {
    // kept until reload
  }
  return n;
}

/** For tests: forget what was read and counted, as a new page would. */
export function forgetDeviceForTests(): void {
  cached = '';
  lastAttempt = 0;
}
