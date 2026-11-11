// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest';
import { observeAirPlay, showAirPlay, supportsAirPlay, type AirPlayVideo } from './airplay';

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
