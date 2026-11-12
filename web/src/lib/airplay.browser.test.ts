import { describe, expect, it, vi } from 'vitest';
import {
  needsReceiverChoice,
  observeAirPlay,
  playBase,
  reachesTarget,
  showAirPlay,
  supportsAirPlay,
  type AirPlayVideo,
} from './airplay';
import type { Receiver } from './api';

function video(): AirPlayVideo {
  const v: AirPlayVideo = document.createElement('video');
  v.canPlayType = () => 'probably';
  v.webkitShowPlaybackTargetPicker = vi.fn();
  v.webkitCurrentPlaybackTargetIsWireless = false;
  return v;
}

function availability(v: AirPlayVideo, value: string) {
  const e = new Event('webkitplaybacktargetavailabilitychanged');
  Object.assign(e, { availability: value });
  v.dispatchEvent(e);
}

describe('AirPlay', () => {
  it('requires both the picker and native HLS, without browser sniffing', () => {
    const v = video();
    expect(supportsAirPlay(v)).toBe(true);
    v.canPlayType = () => '';
    expect(supportsAirPlay(v)).toBe(false);
    v.canPlayType = () => 'probably';
    delete v.webkitShowPlaybackTargetPicker;
    expect(supportsAirPlay(v)).toBe(false);
  });

  it('tracks device availability and wireless state independently, including an existing route', () => {
    const v = video();
    const update = vi.fn();
    const stop = observeAirPlay(v, update);
    expect(update).toHaveBeenLastCalledWith({ supported: true, available: false, wireless: false });
    availability(v, 'available');
    expect(update).toHaveBeenLastCalledWith({ supported: true, available: true, wireless: false });
    v.webkitCurrentPlaybackTargetIsWireless = true;
    v.dispatchEvent(new Event('webkitcurrentplaybacktargetiswirelesschanged'));
    availability(v, 'not-available');
    expect(update).toHaveBeenLastCalledWith({ supported: true, available: true, wireless: true });
    v.webkitCurrentPlaybackTargetIsWireless = false;
    v.dispatchEvent(new Event('webkitcurrentplaybacktargetiswirelesschanged'));
    expect(update).toHaveBeenLastCalledWith({ supported: true, available: false, wireless: false });
    stop();
    v.webkitCurrentPlaybackTargetIsWireless = true;
    const initial = vi.fn();
    observeAirPlay(v, initial)();
    expect(initial).toHaveBeenLastCalledWith({ supported: true, available: true, wireless: true });
  });

  it('removes listeners without touching the media when disposed', () => {
    const v = video();
    const update = vi.fn();
    const pause = vi.spyOn(v, 'pause').mockImplementation(() => {});
    const stop = observeAirPlay(v, update);
    availability(v, 'available');
    expect(update).toHaveBeenCalledTimes(2);
    stop();
    availability(v, 'not-available');
    v.dispatchEvent(new Event('webkitcurrentplaybacktargetiswirelesschanged'));
    expect(update).toHaveBeenCalledTimes(2);
    expect(pause).not.toHaveBeenCalled();
  });

  it('ignores synthetic availability in unsupported browsers', () => {
    const v = video();
    delete v.webkitShowPlaybackTargetPicker;
    const update = vi.fn();
    const stop = observeAirPlay(v, update);
    expect(update).toHaveBeenCalledTimes(1);
    availability(v, 'available');
    expect(update).toHaveBeenCalledTimes(1);
    expect(update).toHaveBeenLastCalledWith({ supported: false, available: false, wireless: false });
    stop();
  });

  it('invokes the picker synchronously with the element as its receiver and contains errors', () => {
    const v = video();
    let receiver: unknown;
    v.webkitShowPlaybackTargetPicker = function () { receiver = this; };
    expect(showAirPlay(v)).toBe(true);
    expect(receiver).toBe(v);
    v.webkitShowPlaybackTargetPicker = () => { throw new Error('route unavailable'); };
    expect(showAirPlay(v)).toBe(false);
    delete v.webkitShowPlaybackTargetPicker;
    expect(showAirPlay(v)).toBe(false);
  });
});

describe('a play reaching an AirPlay target', () => {
  it('names the base of an absolute URL only', () => {
    expect(playBase('http://192.168.4.196:8420/api/sessions/s/index.m3u8')).toBe('http://192.168.4.196:8420');
    expect(playBase('https://ply.home.arpa/x')).toBe('https://ply.home.arpa');
    expect(playBase('/api/items/7/file')).toBe('');
    expect(playBase('blob:http://127.0.0.1:8420/abc')).toBe('');
  });

  it('says whether the target of a routed play is the one asked for', () => {
    const tv = 'http://192.168.4.196:8420';
    // The server routed the play to the TV's network, or to another one.
    expect(reachesTarget(tv + '/api/sessions/s/index.m3u8', tv, 'localhost:8420')).toBe(true);
    expect(reachesTarget('http://192.168.139.3:8420/api/x', tv, 'localhost:8420')).toBe(false);
  });

  it('treats a relative play as the page address, which the target reaches only on its network', () => {
    const tv = 'http://192.168.4.196:8420';
    expect(reachesTarget('/api/sessions/s/index.m3u8', tv, '192.168.4.196:8420')).toBe(true);
    expect(reachesTarget('/api/sessions/s/index.m3u8', tv, 'localhost:8420')).toBe(false);
    // A target with no base (the server is on no network of its) can never be reached.
    expect(reachesTarget(tv + '/api/x', undefined, '192.168.4.196:8420')).toBe(false);
    expect(reachesTarget('/api/x', 'not a url', 'localhost')).toBe(false);
  });
});

describe('whether the AirPlay button needs the TVs by name first', () => {
  const tv = (base: string): Receiver => ({ id: 'tv' + base, name: 'TV', kind: 'airplay', model: 'AppleTV3,2', base });
  const mac = (base: string): Receiver => ({ id: 'mac' + base, name: 'Mac', kind: 'airplay', model: 'Mac14,13', base });
  const farm = 'http://192.168.4.196:8420';

  it('goes straight to the picker when the server routed the play', () => {
    expect(needsReceiverChoice([tv(farm)], farm + '/api/sessions/s/index.m3u8', 'localhost:8420')).toBe(false);
  });

  it("goes straight to the picker when the page is already on the TV's network", () => {
    expect(needsReceiverChoice([tv(farm)], '/api/sessions/s/index.m3u8', '192.168.4.196:8420')).toBe(false);
  });

  it('offers the TVs only when the play was not routed and they are elsewhere', () => {
    expect(needsReceiverChoice([tv(farm)], '/api/sessions/s/index.m3u8', 'localhost:8420')).toBe(true);
  });

  it('offers the TVs when they are on different networks', () => {
    expect(needsReceiverChoice([tv(farm), tv('http://192.168.139.3:8420')], '/x', '192.168.4.196:8420')).toBe(true);
  });

  it('ignores a Mac and a receiver with no address of ours', () => {
    expect(needsReceiverChoice([mac('http://192.168.139.3:8420')], '/x', 'localhost:8420')).toBe(false);
    expect(needsReceiverChoice([{ id: 'x', name: 'x', kind: 'airplay' }], '/x', 'localhost')).toBe(false);
  });
});
