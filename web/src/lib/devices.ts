// Settings › Devices (docs/M11-CLIENTS.md, "Pairing"): what is said of each device, and how long a
// pairing code has left.
import type { Device } from './api';
import { sinceWords } from './settings-tabs';

const KINDS: Record<string, string> = {
  web: 'Browser',
  ios: 'iPhone',
  ipados: 'iPad',
  tvos: 'Apple TV',
  android: 'Android',
  androidtv: 'Android TV',
};

/** "iPad · ply 1.2 · seen 5 minutes ago": the kind (an open list: one not known is as it is), the app. */
export function deviceLine(d: Device, nowMs = Date.now()): string {
  const app = [d.app, d.version].filter(Boolean).join(' ');
  const parts = [KINDS[d.kind] ?? d.kind, app, `seen ${sinceWords(Date.parse(d.lastSeen) / 1000, nowMs)}`];
  return parts.filter(Boolean).join(' · ');
}

/**
 * When a code runs out by this page's clock: the seconds the server gave it (expiresIn) from when
 * the answer came, so that a clock that is wrong does not matter.
 */
export const codeDeadline = (expiresInSec: number, receivedMs = Date.now()): number => receivedMs + expiresInSec * 1000;

/** What a code has left until deadlineMs: "4:59", "0:05"; "" once it has expired. */
export function codeLeft(deadlineMs: number, nowMs = Date.now()): string {
  const sec = Math.ceil((deadlineMs - nowMs) / 1000);
  if (!(sec > 0)) return '';
  return `${Math.floor(sec / 60)}:${String(sec % 60).padStart(2, '0')}`;
}
