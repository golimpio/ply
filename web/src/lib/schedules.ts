// The schedules of Settings → Schedules (M12, docs/M12-SCHEDULES.md): the words for the days a task
// runs on, its time, and when it ran and runs next. The times are the server's clock; a run's instant
// is shown in the browser's own time, as everywhere else in the app.
import { sinceWords } from './settings-tabs';

export interface Weekday {
  /** ISO: Monday 1 to Sunday 7, as the server keeps them. */
  iso: number;
  short: string;
  long: string;
}

export const WEEKDAYS: readonly Weekday[] = [
  { iso: 1, short: 'Mon', long: 'Monday' },
  { iso: 2, short: 'Tue', long: 'Tuesday' },
  { iso: 3, short: 'Wed', long: 'Wednesday' },
  { iso: 4, short: 'Thu', long: 'Thursday' },
  { iso: 5, short: 'Fri', long: 'Friday' },
  { iso: 6, short: 'Sat', long: 'Saturday' },
  { iso: 7, short: 'Sun', long: 'Sunday' },
];

/** The days as one phrase: "Every day", "Weekdays", "Weekends", or "Mon, Thu". */
export function daysWords(days: readonly number[]): string {
  const set = [...new Set(days)].sort((a, b) => a - b);
  if (set.length === 0 || set.length === 7) return 'Every day';
  const key = set.join(',');
  if (key === '1,2,3,4,5') return 'Weekdays';
  if (key === '6,7') return 'Weekends';
  return set.map((d) => WEEKDAYS.find((w) => w.iso === d)?.short ?? String(d)).join(', ');
}

/** "Every day at 02:30", or "Off". */
export function scheduleWords(s: { enabled: boolean; at: string; days: readonly number[] }): string {
  return s.enabled ? `${daysWords(s.days)} at ${s.at}` : 'Off';
}

/** The clock of a moment, in the browser's own time, as the server's "HH:MM" is not: "at 02:30". */
export function clockOf(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

const startOfDay = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate());

/**
 * When a run falls, from the server's RFC 3339 instant: "now" (due, or just passed), "today at 03:00",
 * "tomorrow at 03:00", the weekday within a week ("Monday at 03:00"), or a date past that. Empty when
 * there is no next run.
 */
export function nextRunWords(next: string | undefined, now = Date.now()): string {
  if (!next) return '';
  const at = Date.parse(next);
  if (Number.isNaN(at)) return '';
  if (at <= now + 30_000) return 'now';
  const d = new Date(at);
  const days = Math.round((startOfDay(d).getTime() - startOfDay(new Date(now)).getTime()) / 86_400_000);
  if (days === 0) return `today at ${clockOf(d)}`;
  if (days === 1) return `tomorrow at ${clockOf(d)}`;
  if (days > 1 && days < 7) {
    const iso = d.getDay() === 0 ? 7 : d.getDay();
    return `${WEEKDAYS.find((w) => w.iso === iso)?.long ?? d.toDateString()} at ${clockOf(d)}`;
  }
  return `${d.toLocaleDateString('en-GB', { day: 'numeric', month: 'short', year: 'numeric' })} at ${clockOf(d)}`;
}

/** When the schedule last ran: "Never", or the same words as a key's last test. */
export function lastRunWords(last: string | undefined, now = Date.now()): string {
  if (!last) return 'Never';
  const at = Date.parse(last);
  if (Number.isNaN(at)) return 'Never';
  return sinceWords(at / 1000, now);
}
