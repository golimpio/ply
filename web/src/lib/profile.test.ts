import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Profile } from './api';

type W = { MediaSource?: unknown; ManagedMediaSource?: unknown };
const win = window as unknown as W;
afterEach(() => {
  delete win.MediaSource;
  delete win.ManagedMediaSource;
});

interface Browser {
  /** What Media Source Extensions say they can play (the global MediaSource). */
  mse?: (mime: string) => boolean;
  /** The same through the older ManagedMediaSource (iPhone Safari). */
  managed?: (mime: string) => boolean;
  /** What <video>.canPlayType says: 'probably' for true, '' for false. */
  canPlay?: (mime: string) => boolean;
}

/** The profile of a browser that answers like `b`. browserProfile caches, so the module is loaded afresh. */
async function profileOf(b: Browser): Promise<Profile> {
  vi.resetModules();
  if (b.mse) win.MediaSource = { isTypeSupported: b.mse };
  if (b.managed) win.ManagedMediaSource = { isTypeSupported: b.managed };
  vi.spyOn(HTMLMediaElement.prototype, 'canPlayType').mockImplementation((mime) => (b.canPlay?.(mime) ? 'probably' : ''));
  return (await import('./profile')).browserProfile();
}

// Exactly the codec strings the player asks about.
const only = (...needles: string[]) => (mime: string) => needles.some((n) => mime.includes(n));
const exact = (...types: string[]) => (mime: string) => types.includes(mime);
const everything = () => true;
const nothing = () => false;

describe('browserProfile', () => {
  it('a Chromium-like browser: h264, vp9, av1 and the open audio codecs; not hevc, ac3 or eac3', async () => {
    expect(
      await profileOf({
        mse: only('avc1', 'vp09', 'av01', 'mp4a.40.2', 'opus', 'flac', 'audio/mpeg'),
        canPlay: exact('video/mp4', 'video/webm'), // containers, asked without codecs
      }),
    ).toEqual({
      name: 'browser',
      containers: ['mp4', 'webm'],
      videoCodecs: ['h264', 'vp9', 'av1'],
      audioCodecs: ['aac', 'mp3', 'opus', 'flac'],
      requireHvc1: false,
      maxHeight: 0,
      hls: true,
      textSubs: ['vtt'],
    });
  });

  it('a Safari-like browser: hevc (and so requireHvc1), ac3, eac3 and mov', async () => {
    const p = await profileOf({ mse: everything, canPlay: everything });
    expect(p.videoCodecs).toEqual(['h264', 'hevc', 'vp9', 'av1']);
    expect(p.audioCodecs).toEqual(['aac', 'mp3', 'opus', 'flac', 'ac3', 'eac3']);
    expect(p.containers).toEqual(['mp4', 'webm', 'mov']);
    expect(p.requireHvc1).toBe(true);
  });

  it('asks for hevc as hvc1, the only form AVPlayer direct-plays', async () => {
    const asked: string[] = [];
    await profileOf({ mse: (m) => (asked.push(m), false) });
    expect(asked.filter((m) => m.includes('hvc1') || m.includes('hev1'))).toEqual(['video/mp4; codecs="hvc1.1.6.L120.90"']);
  });

  it.each([
    ['hevc', 'hvc1', 'videoCodecs'],
    ['ac3', 'ac-3', 'audioCodecs'],
    ['eac3', 'ec-3', 'audioCodecs'],
  ] as const)('%s is detected by its own codec string and by nothing else', async (codec, needle, field) => {
    const p = await profileOf({ mse: only(needle) });
    expect(p[field]).toEqual([codec]);
    // Nothing else is claimed as a side effect, in particular not the sibling codecs.
    const others = field === 'videoCodecs' ? p.audioCodecs : p.videoCodecs;
    expect(others).toEqual([]);
    expect(p.containers).toEqual([]);
    expect(p.requireHvc1).toBe(codec === 'hevc');
  });

  it('claims nothing from a browser that supports nothing', async () => {
    expect(await profileOf({ mse: nothing, canPlay: nothing })).toMatchObject({
      containers: [],
      videoCodecs: [],
      audioCodecs: [],
      requireHvc1: false,
    });
  });

  it('claims nothing without Media Source and with a canPlayType that says no', async () => {
    const p = await profileOf({});
    expect(p).toMatchObject({ containers: [], videoCodecs: [], audioCodecs: [], requireHvc1: false });
    expect(p).toMatchObject({ hls: true, textSubs: ['vtt'], maxHeight: 0 }); // fixed fields are still there
  });

  it('falls back to canPlayType where Media Source does not know a type', async () => {
    const p = await profileOf({ mse: nothing, canPlay: (m) => only('ec-3', 'avc1')(m) || exact('video/mp4')(m) });
    expect(p.videoCodecs).toEqual(['h264']);
    expect(p.audioCodecs).toEqual(['eac3']);
    expect(p.containers).toEqual(['mp4']);
  });

  it('uses ManagedMediaSource when there is no MediaSource', async () => {
    const p = await profileOf({ managed: only('hvc1', 'ac-3') });
    expect(p.videoCodecs).toEqual(['hevc']);
    expect(p.audioCodecs).toEqual(['ac3']);
  });

  it('survives a Media Source that throws', async () => {
    const p = await profileOf({
      mse: () => {
        throw new TypeError('bad type');
      },
      canPlay: only('avc1'),
    });
    expect(p.videoCodecs).toEqual(['h264']);
  });

  it('a codec counts as supported when any of its candidate types is', async () => {
    // vp9 only in webm, av1 only in webm, opus only in mp4, flac only as audio/flac.
    const p = await profileOf({
      mse: (m) => m === 'video/webm; codecs="vp09.00.10.08"' || m === 'video/webm; codecs="av01.0.05M.08"' ||
        m === 'audio/mp4; codecs="opus"' || m === 'audio/flac',
    });
    expect(p.videoCodecs).toEqual(['vp9', 'av1']);
    expect(p.audioCodecs).toEqual(['opus', 'flac']);
  });

  it('is measured once', async () => {
    vi.resetModules();
    win.MediaSource = { isTypeSupported: everything };
    const spy = vi.spyOn(HTMLMediaElement.prototype, 'canPlayType').mockReturnValue('probably');
    const { browserProfile } = await import('./profile');
    const first = browserProfile();
    const calls = spy.mock.calls.length;
    win.MediaSource = { isTypeSupported: nothing }; // the browser changing its mind changes nothing
    expect(browserProfile()).toBe(first);
    expect(spy.mock.calls.length).toBe(calls);
  });
});
