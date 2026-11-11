import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { DEVICE_ID as VALID, deviceId, forgetDeviceForTests } from './device';

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
    vi.stubGlobal('crypto', { getRandomValues: (a: Uint8Array) => real.getRandomValues(a) });
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
