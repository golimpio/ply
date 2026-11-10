import { describe, expect, it } from 'vitest';
import type { Trailer } from './api';
import { embedUrl, playable, trailerLabel } from './trailers';

const yt = (key: string, extra: Partial<Trailer> = {}): Trailer => ({ site: 'youtube', key, official: true, ...extra });

describe('embedUrl', () => {
  it('plays YouTube in its privacy-enhanced player, Vimeo with Do Not Track', () => {
    expect(embedUrl(yt('vKQi3bBA1y8'))).toBe('https://www.youtube-nocookie.com/embed/vKQi3bBA1y8?autoplay=1&rel=0&playsinline=1');
    expect(embedUrl({ site: 'vimeo', key: '76979871' })).toBe('https://player.vimeo.com/video/76979871?autoplay=1&dnt=1');
  });

  it('builds nothing from a key that is not one, whatever the server sent', () => {
    for (const t of [
      yt('vKQi3bBA1y'), // one short
      yt('vKQi3bBA1y8x'), // one too many
      yt('../../x?a=b'),
      yt('vKQi3b"A1y8'),
      yt('vKQi3bBA1y8\n'),
      { site: 'vimeo', key: '7697987a' },
      { site: 'vimeo', key: '' },
      { site: 'dailymotion', key: 'x7tgad0' },
    ] as Trailer[]) {
      expect(embedUrl(t), JSON.stringify(t)).toBeUndefined();
    }
  });
});

it('playable keeps the server’s order and drops what cannot be played', () => {
  const a = yt('aaaaaaaaaaa');
  const b = { site: 'vimeo', key: '1', official: false } as Trailer;
  expect(playable([a, yt('bad'), b])).toEqual([a, b]);
  expect(playable([])).toEqual([]);
});

describe('trailerLabel', () => {
  it('is the trailer’s own name, trimmed', () => {
    const t = yt('aaaaaaaaaaa', { name: '  Official Trailer ' });
    expect(trailerLabel(t, [t])).toBe('Official Trailer');
  });

  it('says what an unnamed one is, numbered among its kind when there are several', () => {
    const t1 = yt('aaaaaaaaaaa', { type: 'trailer' });
    const t2 = yt('bbbbbbbbbbb');
    const teaser = yt('ccccccccccc', { type: 'teaser', name: ' ' });
    const named = yt('ddddddddddd', { name: 'Final Trailer' });
    const all = [t1, named, t2, teaser];
    expect(all.map((t) => trailerLabel(t, all))).toEqual(['Trailer 1', 'Final Trailer', 'Trailer 2', 'Teaser']);
    expect(trailerLabel(t1, [t1, teaser])).toBe('Trailer');
  });
});
