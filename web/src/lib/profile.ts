import type { Profile } from './api';

// Candidate MIME types per codec name (ffprobe names, as used by the server's Profile).
// A codec counts as supported if any candidate is.
const VIDEO: Record<string, string[]> = {
  h264: ['video/mp4; codecs="avc1.640028"'],
  hevc: ['video/mp4; codecs="hvc1.1.6.L120.90"'],
  vp9: ['video/webm; codecs="vp09.00.10.08"', 'video/mp4; codecs="vp09.00.10.08"'],
  av1: ['video/mp4; codecs="av01.0.05M.08"', 'video/webm; codecs="av01.0.05M.08"'],
};

const AUDIO: Record<string, string[]> = {
  aac: ['audio/mp4; codecs="mp4a.40.2"'],
  mp3: ['audio/mpeg', 'audio/mp4; codecs="mp3"'],
  opus: ['audio/webm; codecs="opus"', 'audio/mp4; codecs="opus"'],
  flac: ['audio/mp4; codecs="flac"', 'audio/flac'],
  ac3: ['audio/mp4; codecs="ac-3"'],
  eac3: ['audio/mp4; codecs="ec-3"'],
};

const CONTAINERS: Record<string, string> = {
  mp4: 'video/mp4',
  webm: 'video/webm',
  mov: 'video/quicktime',
};

type MediaSourceLike = { isTypeSupported(type: string): boolean };

/**
 * The most audio channels a browser decodes. Chrome's demuxer refuses an 8-channel (7.1) AAC
 * stream: native playback and Media Source Extensions both fail the segment, while
 * MediaCapabilities and isTypeSupported claim it plays (measured 2026-11-13, Chrome 157, macOS).
 * Nothing in a browser measures a mix it has not met yet, so this is a known limit rather than a
 * probe: a browser that could play 7.1 gets a stereo downmix instead. The server downmixes what is
 * more (internal/playback, MaxAudioChannels).
 */
const MAX_AUDIO_CHANNELS = 6;

let cached: Profile | undefined;

/** The Profile describing what this browser can decode, measured once and cached. */
export function browserProfile(): Profile {
  if (cached) return cached;

  const el = document.createElement('video');
  const w = window as unknown as { MediaSource?: MediaSourceLike; ManagedMediaSource?: MediaSourceLike };
  const mse = w.MediaSource ?? w.ManagedMediaSource;

  const canPlay = (mime: string) => el.canPlayType(mime) !== '';
  const supported = (mime: string) => {
    try {
      if (mse?.isTypeSupported(mime)) return true;
    } catch {
      // fall through to canPlayType
    }
    return canPlay(mime);
  };
  const pick = (table: Record<string, string[]>) =>
    Object.keys(table).filter((codec) => table[codec].some(supported));

  const videoCodecs = pick(VIDEO);
  cached = {
    name: 'browser',
    containers: Object.keys(CONTAINERS).filter((c) => canPlay(CONTAINERS[c])),
    videoCodecs,
    audioCodecs: pick(AUDIO),
    // Safari only direct-plays hevc tagged hvc1; harmless to ask for elsewhere.
    requireHvc1: videoCodecs.includes('hevc'),
    maxHeight: 0,
    maxAudioChannels: MAX_AUDIO_CHANNELS,
    hls: true,
    textSubs: ['vtt'],
  };
  return cached;
}
