import { describe, expect, it } from 'vitest';
import { SETTINGS_TABS, settingsHash, settingsTab, sinceWords, testedLine } from './settings-tabs';

const q = (s: string) => new URLSearchParams(s);

describe('settingsTab', () => {
  it.each(SETTINGS_TABS)('?tab=%s is that tab', (tab) => {
    expect(settingsTab(q(`tab=${tab}`))).toBe(tab);
  });

  it('is Library with no tab, or one it does not know', () => {
    expect(settingsTab(q(''))).toBe('library');
    expect(settingsTab(q('tab=nope'))).toBe('library');
    expect(settingsTab(q('tab=KEYS'))).toBe('library');
    expect(settingsTab(q('tab='))).toBe('library');
  });

  it("maps the player's older link to the limits", () => {
    expect(settingsTab(q('s=playback'))).toBe('playback');
    // Only that one: another s is not a tab.
    expect(settingsTab(q('s=keys'))).toBe('library');
    // A tab named outright wins over it.
    expect(settingsTab(q('s=playback&tab=keys'))).toBe('keys');
  });

  it('reads back what settingsHash writes', () => {
    for (const tab of SETTINGS_TABS) {
      const query = settingsHash(tab).split('?')[1] ?? '';
      expect(settingsTab(q(query))).toBe(tab);
    }
  });

  it('writes Library, the default, as the bare page', () => {
    expect(settingsHash('library')).toBe('#/settings');
    expect(settingsHash('keys')).toBe('#/settings?tab=keys');
  });
});

describe('testedLine', () => {
  const now = Date.parse('2026-11-06T12:00:00Z');
  const ago = (sec: number) => now / 1000 - sec;

  it('is empty when the key was never tested (0)', () => {
    expect(testedLine(0, false, now)).toBe('');
    expect(testedLine(undefined, undefined, now)).toBe('');
  });

  it('says whether it worked, and when', () => {
    expect(testedLine(ago(10), true, now)).toBe('Worked when tested just now.');
    expect(testedLine(ago(5 * 60), false, now)).toBe('Failed when tested 5 minutes ago.');
    expect(testedLine(ago(3 * 3600 + 120), true, now)).toBe('Worked when tested 3 hours ago.');
  });

  it('counts whole units', () => {
    expect(sinceWords(ago(50), now)).toBe('1 minute ago');
    expect(sinceWords(ago(119), now)).toBe('1 minute ago');
    expect(sinceWords(ago(26 * 3600), now)).toBe('yesterday');
    expect(sinceWords(ago(12 * 86400), now)).toBe('12 days ago');
  });

  it('gives a date past a month, and never a time to come', () => {
    expect(sinceWords(Date.parse('2026-09-01T12:00:00Z') / 1000, now)).toBe('on 1 Sept 2026');
    expect(sinceWords(ago(-300), now)).toBe('just now'); // a clock a little ahead of the server's
  });
});
