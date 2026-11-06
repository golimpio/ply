import { describe, expect, it } from 'vitest';
import type { ItemSummary } from './api';
import { countEpisodes, countsBadge, itemBadge, showCounts, WATCHED } from './watched';

let nextId = 1;
const ep = (season: number, episode: number, o: Partial<ItemSummary> = {}): ItemSummary => ({
  id: nextId++,
  kind: 'episode',
  title: '',
  show: 'Lost',
  season,
  episode,
  durationSec: 2700,
  container: 'mkv',
  videoCodec: 'h264',
  audioCodec: 'aac',
  width: 1920,
  height: 1080,
  addedAt: 0,
  ...o,
});
const seen = { watched: true, progress: { positionSec: 0, watched: true } };
const going = { progress: { positionSec: 600, watched: false } };

describe('countEpisodes', () => {
  it('counts episodes watched and in progress', () => {
    expect(countEpisodes([ep(1, 1, seen), ep(1, 2, going), ep(1, 3)])).toEqual({ episodeCount: 3, watchedEpisodes: 1, inProgress: 1 });
  });

  it('counts an episode with two files once: watched when either is, started only when neither is watched', () => {
    const twice = [ep(1, 1), ep(1, 1, seen), ep(1, 2, going), ep(1, 2)];
    expect(countEpisodes(twice)).toEqual({ episodeCount: 2, watchedEpisodes: 1, inProgress: 1 });
    expect(countEpisodes([ep(1, 3, going), ep(1, 3, seen)])).toEqual({ episodeCount: 1, watchedEpisodes: 1, inProgress: 0 });
  });

  it('makes a file with no episode number an episode of its own', () => {
    expect(countEpisodes([ep(1, 0), ep(1, 0, seen)])).toEqual({ episodeCount: 2, watchedEpisodes: 1, inProgress: 0 });
  });

  it('does not count a position at 0, or a watched file, as in progress', () => {
    expect(countEpisodes([ep(1, 1, { progress: { positionSec: 0, watched: false } }), ep(1, 2, seen)]).inProgress).toBe(0);
  });
});

describe('showCounts', () => {
  it('leaves the specials out', () => {
    expect(showCounts([ep(1, 1, seen), ep(1, 2, seen), ep(0, 1)])).toEqual({ episodeCount: 2, watchedEpisodes: 2, inProgress: 0 });
  });

  it('counts the specials of a show that has nothing else', () => {
    expect(showCounts([ep(0, 1, seen), ep(0, 2)])).toEqual({ episodeCount: 2, watchedEpisodes: 1, inProgress: 0 });
  });
});

describe('countsBadge ("N left")', () => {
  it('says how many are left once some are watched', () => {
    expect(countsBadge({ episodeCount: 10, watchedEpisodes: 3, inProgress: 0 })).toEqual({
      kind: 'left',
      text: '7 left',
      label: '7 episodes left',
      started: false,
    });
    expect(countsBadge({ episodeCount: 10, watchedEpisodes: 9, inProgress: 1 })).toEqual({
      kind: 'left',
      text: '1 left',
      label: '1 episode left, 1 in progress',
      started: true,
    });
  });

  it('is a check once every episode is watched', () => {
    expect(countsBadge({ episodeCount: 10, watchedEpisodes: 10, inProgress: 0 })).toBe(WATCHED);
    expect(countsBadge({ episodeCount: 4, watchedEpisodes: 6, inProgress: 0 })).toBe(WATCHED); // never more than all
    expect(WATCHED).toMatchObject({ text: '✓', label: 'Watched' });
  });

  it('says one is in progress when none is watched', () => {
    expect(countsBadge({ episodeCount: 10, watchedEpisodes: 0, inProgress: 2 })).toEqual({
      kind: 'started',
      text: 'In progress',
      label: '2 episodes in progress',
      started: true,
    });
  });

  it('says nothing of a show not started, or with no episodes, or with no counts', () => {
    // Positive control above: the same shape with some watched has a badge.
    expect(countsBadge({ episodeCount: 10, watchedEpisodes: 0, inProgress: 0 })).toBeNull();
    expect(countsBadge({ episodeCount: 0, watchedEpisodes: 0, inProgress: 0 })).toBeNull();
    expect(countsBadge({})).toBeNull();
    expect(countsBadge(undefined)).toBeNull();
  });
});

describe('itemBadge', () => {
  it('is a check for a watched film or episode, nothing otherwise', () => {
    expect(itemBadge({ watched: true })).toBe(WATCHED);
    expect(itemBadge({ watched: false })).toBeNull();
    expect(itemBadge({})).toBeNull();
    expect(itemBadge(undefined)).toBeNull();
  });
});
