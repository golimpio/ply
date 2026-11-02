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
  /** What <video>.canPlayType says: 'probably' for true, '' for false, or 'maybe' (a bare container, in Chrome). */
  canPlay?: (mime: string) => boolean | 'maybe';
}

/** The profile of a browser that answers like `b`. browserProfile caches, so the module is loaded afresh. */
async function profileOf(b: Browser): Promise<Profile> {
  vi.resetModules();
  delete win.MediaSource;
  delete win.ManagedMediaSource;
  if (b.mse) win.MediaSource = { isTypeSupported: b.mse };
  if (b.managed) win.ManagedMediaSource = { isTypeSupported: b.managed };
  vi.spyOn(HTMLMediaElement.prototype, 'canPlayType').mockImplementation((mime) => {
    const r = b.canPlay?.(mime);
    return r === 'maybe' ? 'maybe' : r ? 'probably' : '';
  });
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

  // The exact strings the player asks about. A different profile or level is a different question,
  // and the browser would answer it differently: the substring checks above would not notice.
  const ASKED = [
    ['videoCodecs', 'h264', 'video/mp4; codecs="avc1.640028"'],
    ['videoCodecs', 'hevc', 'video/mp4; codecs="hvc1.1.6.L120.90"'],
    ['videoCodecs', 'vp9', 'video/webm; codecs="vp09.00.10.08"'],
    ['videoCodecs', 'vp9', 'video/mp4; codecs="vp09.00.10.08"'],
    ['videoCodecs', 'av1', 'video/mp4; codecs="av01.0.05M.08"'],
    ['videoCodecs', 'av1', 'video/webm; codecs="av01.0.05M.08"'],
    ['audioCodecs', 'aac', 'audio/mp4; codecs="mp4a.40.2"'],
    ['audioCodecs', 'mp3', 'audio/mpeg'],
    ['audioCodecs', 'mp3', 'audio/mp4; codecs="mp3"'],
    ['audioCodecs', 'opus', 'audio/webm; codecs="opus"'],
    ['audioCodecs', 'opus', 'audio/mp4; codecs="opus"'],
    ['audioCodecs', 'flac', 'audio/mp4; codecs="flac"'],
    ['audioCodecs', 'flac', 'audio/flac'],
    ['audioCodecs', 'ac3', 'audio/mp4; codecs="ac-3"'],
    ['audioCodecs', 'eac3', 'audio/mp4; codecs="ec-3"'],
  ] as const;

  describe.each([
    ['Media Source', (mime: string): Browser => ({ mse: exact(mime) })],
    ['canPlayType', (mime: string): Browser => ({ canPlay: exact(mime) })],
  ])('asked of %s', (_, answeringOnly) => {
    it.each(ASKED)('%s: %s is claimed for %s, and nothing else is', async (field, codec, mime) => {
      const p = await profileOf(answeringOnly(mime));
      expect(p[field]).toEqual([codec]);
      expect(field === 'videoCodecs' ? p.audioCodecs : p.videoCodecs).toEqual([]);
      expect(p.containers).toEqual([]);
      expect(p.requireHvc1).toBe(codec === 'hevc');
    });
  });

  it.each([
    ['mp4', 'video/mp4'],
    ['webm', 'video/webm'],
    ['mov', 'video/quicktime'],
  ])('the %s container is canPlayType(%s) and nothing else', async (container, mime) => {
    const p = await profileOf({ canPlay: exact(mime) });
    expect(p.containers).toEqual([container]);
    expect(p.videoCodecs).toEqual([]);
    expect(p.audioCodecs).toEqual([]);
  });

  it('takes the containers from canPlayType alone: Media Source says nothing about files', async () => {
    const p = await profileOf({ mse: everything, canPlay: nothing });
    expect(p.videoCodecs.length, 'control: Media Source did say yes to the codecs').toBeGreaterThan(0);
    expect(p.containers).toEqual([]);
    expect((await profileOf({ mse: nothing, canPlay: exact('video/mp4') })).containers).toEqual(['mp4']);
  });

  it("counts canPlayType's 'maybe' as a yes (all Chrome says of a bare container)", async () => {
    const p = await profileOf({ canPlay: (m) => (m === 'video/mp4' ? 'maybe' : false) });
    expect(p.containers).toEqual(['mp4']);
  });

  it('prefers MediaSource to ManagedMediaSource where there are both', async () => {
    const both = await profileOf({ mse: only('avc1'), managed: only('hvc1') });
    expect(both.videoCodecs).toEqual(['h264']);
    expect(both.requireHvc1).toBe(false);
    // Control: ManagedMediaSource is consulted at all, and answers differently.
    expect((await profileOf({ managed: only('hvc1') })).videoCodecs).toEqual(['hevc']);
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
