import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { DEVICE_ID as VALID, deviceId, forgetDeviceForTests, nextAttempt } from './device';

describe('deviceId', () => {
  beforeEach(() => {
    localStorage.clear();
    forgetDeviceForTests();
  });
  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  // crypto.randomUUID is only there in a secure context: ply at http://<lan-ip> is not one, so this is
  // the path a browser on another machine takes.
  it('makes a valid, random id without crypto.randomUUID', () => {
    const real = globalThis.crypto;
    vi.stubGlobal('crypto', { getRandomValues: (a: Uint8Array<ArrayBuffer>) => real.getRandomValues(a) });
    const first = deviceId();
    expect(first).toMatch(VALID);
    expect(first).toMatch(/^[0-9a-f]{32}$/);
    expect(localStorage.getItem('ply.device')).toBe(first);
    localStorage.clear();
    forgetDeviceForTests();
    expect(deviceId()).not.toBe(first);
  });

  it('is made once and kept: the same id after a reload', () => {
    const first = deviceId();
    expect(first).toMatch(VALID);
    expect(localStorage.getItem('ply.device')).toBe(first);
    forgetDeviceForTests(); // a new page
    expect(deviceId()).toBe(first);
  });

  it('replaces a stored id that is not one', () => {
    localStorage.setItem('ply.device', 'bad id!');
    const id = deviceId();
    expect(id).toMatch(VALID);
    expect(id).not.toBe('bad id!');
    // Positive control: a valid stored id is kept as it is.
    localStorage.setItem('ply.device', 'kept-0001');
    forgetDeviceForTests();
    expect(deviceId()).toBe('kept-0001');
  });

  it('still has an id when storage refuses', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('denied');
    });
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('denied');
    });
    const id = deviceId();
    expect(id).toMatch(VALID);
    expect(deviceId()).toBe(id); // the same for as long as the page lives
  });
});

describe('nextAttempt', () => {
  beforeEach(() => {
    localStorage.clear();
    forgetDeviceForTests();
  });
  afterEach(() => vi.restoreAllMocks());

  it('goes up with every ask, also when the clock stands still or goes back', () => {
    const now = vi.spyOn(Date, 'now').mockReturnValue(1_000_000);
    const a = nextAttempt();
    const b = nextAttempt(); // the same millisecond
    now.mockReturnValue(5); // the clock set back
    const c = nextAttempt();
    expect(a).toBe(1_000_000);
    expect(b).toBeGreaterThan(a);
    expect(c).toBeGreaterThan(b);
  });

  it('goes up across a reload and from another tab, by what is kept', () => {
    vi.spyOn(Date, 'now').mockReturnValue(1_000_000);
    const before = nextAttempt();
    forgetDeviceForTests(); // a new page (or another tab: one store, its own count)
    expect(nextAttempt()).toBeGreaterThan(before);
    localStorage.setItem('ply.attempt', String(before + 500)); // another tab asked since
    expect(nextAttempt()).toBe(before + 501);
  });

  it('ignores a stored count the server would refuse', () => {
    vi.spyOn(Date, 'now').mockReturnValue(1_000_000);
    for (const bad of ['NaN', 'Infinity', '-5', '1.5', '1e21', String(Number.MAX_SAFE_INTEGER), 'x']) {
      localStorage.setItem('ply.attempt', bad);
      forgetDeviceForTests();
      expect(nextAttempt(), bad).toBe(1_000_000);
    }
    // Positive control: a good one is gone past.
    localStorage.setItem('ply.attempt', '2000000');
    forgetDeviceForTests();
    expect(nextAttempt()).toBe(2_000_001);
  });

  it('still goes up when storage refuses', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('denied');
    });
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('denied');
    });
    vi.spyOn(Date, 'now').mockReturnValue(7);
    const a = nextAttempt();
    expect(nextAttempt()).toBeGreaterThan(a);
  });
});
