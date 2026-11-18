import { flushSync, mount, tick, unmount } from 'svelte';
import { expect, it, vi } from 'vitest';
import { api, type MovieCard, type UpcomingShow } from '../lib/api';
import { app } from '../lib/state.svelte';
import Home from './Home.svelte';

const movie: MovieCard = {
  id: 1,
  kind: 'movie',
  title: 'Library film',
  sortTitle: 'Library film',
  durationSec: 3600,
  container: 'mp4',
  width: 1920,
  height: 1080,
  addedAt: 1,
  watched: false,
  progress: null,
  matchState: 'matched',
  needsReview: false,
  art: null,
  rating: null,
};

it('keeps the newer Coming up rail when the old library response arrives last', async () => {
  let resolveOld!: (value: UpcomingShow[]) => void;
  const old = new Promise<UpcomingShow[]>((resolve) => { resolveOld = resolve; });
  const newer = { titleId: 2, show: 'New schedule', nextAirDate: '2099-01-01' };
  vi.spyOn(api, 'movieCards').mockResolvedValue([movie]);
  vi.spyOn(api, 'episodeCards').mockResolvedValue([]);
  vi.spyOn(api, 'shows').mockResolvedValue([]);
  vi.spyOn(api, 'continueWatching').mockResolvedValue([]);
  vi.spyOn(api, 'genres').mockResolvedValue([]);
  vi.spyOn(api, 'item').mockRejectedValue(new Error('No additional film metadata'));
  vi.spyOn(api, 'settings').mockRejectedValue(new Error('No settings'));
  const upcoming = vi.spyOn(api, 'upcoming').mockReturnValueOnce(old).mockResolvedValue([newer]);

  const target = document.body.appendChild(document.createElement('div'));
  const home = mount(Home, { target });
  try {
    flushSync();
    expect(upcoming).toHaveBeenCalledTimes(1);

    flushSync(() => app.libraryVersion++);
    expect(upcoming).toHaveBeenCalledTimes(2);
    await vi.waitFor(() => expect(target.textContent).toContain(newer.show));

    resolveOld([{ ...newer, titleId: 3, show: 'Old schedule' }]);
    await old;
    await tick();
    expect(target.textContent).toContain(newer.show);
    expect(target.textContent).not.toContain('Old schedule');
  } finally {
    await unmount(home);
    target.remove();
  }
});
