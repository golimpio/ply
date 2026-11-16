import { describe, expect, it } from 'vitest';
import { formatAudioTrack, formatVideoSpec } from './format';

describe('formatVideoSpec', () => {
  it('classifies scope 1920x800 as 1080p rather than 800p', () => {
    expect(formatVideoSpec({ width: 1920, height: 800, videoCodec: 'h264' })).toBe('1080p (H.264)');
  });

  it('includes HDR classification and codec when present', () => {
    expect(formatVideoSpec({ width: 3840, height: 1600, hdr: 'hdr10', videoCodec: 'hevc' })).toBe('4K HDR10 (HEVC)');
  });
});

describe('formatAudioTrack', () => {
  it('formats language, codec, channels, and stream title', () => {
    expect(
      formatAudioTrack({
        language: 'rus',
        codec: 'eac3',
        channels: 6,
        title: 'RGB [Bravo Records Georgia]',
      }),
    ).toBe('Russian (E-AC-3 5.1 · RGB [Bravo Records Georgia])');
  });

  it('avoids repeating the language name when the stream title equals it', () => {
    expect(formatAudioTrack({ language: 'eng', codec: 'eac3', channels: 6, title: 'English' })).toBe(
      'English (E-AC-3 5.1)',
    );
  });
});
