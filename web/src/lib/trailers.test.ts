import { describe, expect, it } from 'vitest';
import type { TitleTrailers } from './api';
import { buttonState } from './trailers';

const local = (itemId: number, name = `t${itemId}`) => ({ itemId, name, durationSec: 120 });

describe('buttonState', () => {
  it('plays the first trailer on disk, even while a download is reported', () => {
    const t: TitleTrailers = { local: [local(8), local(9)], download: { state: 'running', jobId: 3 } };
    expect(buttonState(t)).toEqual({ kind: 'play', itemId: 8, choices: [local(8), local(9)] });
  });
  it('spins while the server downloads one', () => {
    expect(buttonState({ local: [], download: { state: 'queued', jobId: 3 } })).toEqual({ kind: 'downloading', jobId: 3 });
    expect(buttonState({ local: [], download: { state: 'running', jobId: 4 } })).toEqual({ kind: 'downloading', jobId: 4 });
  });
  it('shows nothing for a failed download, no trailer, or no answer yet', () => {
    expect(buttonState({ local: [], download: { state: 'failed', jobId: 3, error: 'x' } })).toEqual({ kind: 'none' });
    expect(buttonState({ local: [] })).toEqual({ kind: 'none' });
    expect(buttonState(undefined)).toEqual({ kind: 'none' });
  });
});
