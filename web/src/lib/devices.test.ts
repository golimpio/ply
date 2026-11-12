import { describe, expect, it } from 'vitest';
import type { Device } from './api';
import { codeDeadline, codeLeft, deviceLine } from './devices';

const now = Date.parse('2026-11-12T10:00:00Z');
const ipad: Device = {
  id: 'phone-0001', name: 'Kitchen iPad', kind: 'ipados', app: 'ply', version: '1.2',
  firstSeen: '2026-11-01T10:00:00Z', lastSeen: '2026-11-12T09:55:00Z', paired: true,
};

describe('deviceLine', () => {
  it('names the kind, the app and when it was seen', () => {
    expect(deviceLine(ipad, now)).toBe('iPad · ply 1.2 · seen 5 minutes ago');
  });

  it('keeps a kind it does not know, and leaves out what the device did not say', () => {
    expect(deviceLine({ ...ipad, kind: 'fridge', app: '', version: '' }, now)).toBe('fridge · seen 5 minutes ago');
    expect(deviceLine({ ...ipad, kind: '', app: 'ply-web', version: '' }, now)).toBe('ply-web · seen 5 minutes ago');
  });
});

describe('codeLeft', () => {
  it('counts down in minutes and seconds', () => {
    expect(codeLeft(now + 300_000, now)).toBe('5:00');
    expect(codeLeft(now + 5_000, now)).toBe('0:05');
    expect(codeLeft(now + 400, now)).toBe('0:01'); // a part of a second left is one
  });

  it('is empty once the code has expired, or for a deadline it cannot read', () => {
    expect(codeLeft(now, now)).toBe('');
    expect(codeLeft(now - 60_000, now)).toBe('');
    expect(codeLeft(Number.NaN, now)).toBe('');
  });

  it("counts from what the server gave, whatever this page's clock says", () => {
    const received = Date.parse('2020-01-01T00:00:00Z'); // a clock years behind the server's
    expect(codeLeft(codeDeadline(300, received), received + 1000)).toBe('4:59');
  });
});
