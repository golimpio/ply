import { describe, expect, it } from 'vitest';
import { clockOf, daysWords, lastRunWords, nextRunWords, scheduleWords } from './schedules';

/** A local moment: the words are the browser's own, as the app writes every other time. */
const local = (y: number, mo: number, d: number, h: number, mi = 0) => new Date(y, mo - 1, d, h, mi);
const iso = (d: Date) => d.toISOString();

describe('daysWords', () => {
  it('is every day for no days and for all seven', () => {
    expect(daysWords([])).toBe('Every day');
    expect(daysWords([1, 2, 3, 4, 5, 6, 7])).toBe('Every day');
  });

  it('names the working week, the weekend, and any other set in order', () => {
    expect(daysWords([1, 2, 3, 4, 5])).toBe('Weekdays');
    expect(daysWords([5, 1, 2, 3, 4])).toBe('Weekdays'); // the server keeps them in order; a page may not
    expect(daysWords([6, 7])).toBe('Weekends');
    expect(daysWords([4, 1, 7])).toBe('Mon, Thu, Sun');
    expect(daysWords([2, 2, 2])).toBe('Tue');
  });
});

describe('scheduleWords', () => {
  it('says the days and the time, or that it is off', () => {
    expect(scheduleWords({ enabled: true, at: '02:30', days: [] })).toBe('Every day at 02:30');
    expect(scheduleWords({ enabled: true, at: '03:00', days: [1, 3] })).toBe('Mon, Wed at 03:00');
    expect(scheduleWords({ enabled: false, at: '02:30', days: [] })).toBe('Off');
  });
});

describe('nextRunWords', () => {
  const now = local(2026, 11, 9, 9).getTime(); // Monday, 09:00

  it('says now for a run that is due or just passed', () => {
    expect(nextRunWords(iso(local(2026, 11, 9, 9, 0)), now)).toBe('now');
    expect(nextRunWords(iso(local(2026, 11, 9, 8)), now)).toBe('now');
  });

  it('says today and tomorrow by name', () => {
    expect(nextRunWords(iso(local(2026, 11, 9, 10, 5)), now)).toBe('today at 10:05');
    expect(nextRunWords(iso(local(2026, 11, 10, 3)), now)).toBe('tomorrow at 03:00');
  });

  it('names the weekday within the week', () => {
    expect(nextRunWords(iso(local(2026, 11, 11, 3)), now)).toBe('Wednesday at 03:00');
    expect(nextRunWords(iso(local(2026, 11, 15, 3)), now)).toBe('Sunday at 03:00');
  });

  it('gives a date past the week, and nothing without a next run', () => {
    expect(nextRunWords(iso(local(2026, 11, 16, 3)), now)).toBe('16 Nov 2026 at 03:00');
    expect(nextRunWords(undefined, now)).toBe('');
    expect(nextRunWords('not a time', now)).toBe('');
  });

  it('reads the hour and minute in the browser’s own time', () => {
    expect(clockOf(local(2026, 11, 9, 3, 7))).toBe('03:07');
  });
});

describe('lastRunWords', () => {
  const now = local(2026, 11, 9, 9).getTime();

  it('says when it ran, or that it never did', () => {
    expect(lastRunWords(undefined, now)).toBe('Never');
    expect(lastRunWords('not a time', now)).toBe('Never');
    expect(lastRunWords(iso(new Date(now - 5 * 60_000)), now)).toBe('5 minutes ago');
    expect(lastRunWords(iso(new Date(now - 2 * 86_400_000)), now)).toBe('2 days ago');
  });
});
