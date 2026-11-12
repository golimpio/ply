// The tabs of Settings, in the URL as #/settings?tab=<id> (Library, the default, as #/settings), and
// the "last tested" line of a key.

export const SETTINGS_TABS = ['library', 'schedules', 'details', 'ratings', 'playback', 'devices', 'keys'] as const;
export type SettingsTab = (typeof SETTINGS_TABS)[number];

export const TAB_LABELS: Record<SettingsTab, string> = {
  library: 'Library',
  schedules: 'Schedules',
  details: 'Details',
  ratings: 'Ratings',
  playback: 'Playback',
  devices: 'Devices',
  keys: 'API keys',
};

const isTab = (t: string | null): t is SettingsTab => SETTINGS_TABS.includes(t as SettingsTab);

/**
 * The tab a query asks for: ?tab=<id>, or the player's older link to the limits (?s=playback);
 * anything else is Library.
 */
export function settingsTab(q: URLSearchParams): SettingsTab {
  const tab = q.get('tab');
  if (isTab(tab)) return tab;
  if (q.get('s') === 'playback') return 'playback';
  return 'library';
}

export function settingsHash(tab: SettingsTab): string {
  return tab === 'library' ? '#/settings' : `#/settings?tab=${tab}`;
}

const UNITS: [Intl.RelativeTimeFormatUnit, number][] = [
  ['minute', 60],
  ['hour', 60 * 60],
  ['day', 24 * 60 * 60],
];

/** "just now", "5 minutes ago", "yesterday", "12 days ago"; a date past a month. */
export function sinceWords(unixSec: number, nowMs = Date.now()): string {
  const sec = Math.max(0, nowMs / 1000 - unixSec);
  if (sec < 45) return 'just now';
  if (sec >= 30 * 24 * 60 * 60) return `on ${new Date(unixSec * 1000).toLocaleDateString('en-GB', { dateStyle: 'medium' })}`;
  const rtf = new Intl.RelativeTimeFormat('en', { numeric: 'auto' });
  let [unit, size] = UNITS[0];
  for (const [u, s] of UNITS) if (sec >= s) [unit, size] = [u, s];
  return rtf.format(-Math.max(1, Math.floor(sec / size)), unit);
}

/** What the last test of a key came to; '' when it was never tested (or the key changed since: 0). */
export function testedLine(testedAt: number | undefined, ok: boolean | undefined, nowMs = Date.now()): string {
  if (!testedAt) return '';
  return `${ok ? 'Worked' : 'Failed'} when tested ${sinceWords(testedAt, nowMs)}.`;
}
