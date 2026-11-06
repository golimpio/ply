import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  backTarget,
  claimRestore,
  goBack,
  href,
  MAX_VIEWS,
  navigate,
  page,
  parentOf,
  parseRoute,
  recall,
  redirect,
  remember,
  resetNavigation,
  RESTORE_WAIT_MS,
  router,
  viewStack,
  type LibraryView,
  type Route,
} from './router.svelte';

const notfound = { name: 'notfound' };

describe('parseRoute', () => {
  it.each([
    ['', { name: 'home' }],
    ['#/', { name: 'home' }],
    ['#/movies', { name: 'movies', sort: 'title', filters: [], minRating: 0 }],
    ['#/movies?sort=added&f=4k,hdr,bogus', { name: 'movies', sort: 'added', filters: ['4k', 'hdr'], minRating: 0 }],
    ['#/movies?sort=nonsense', { name: 'movies', sort: 'title', filters: [], minRating: 0 }],
    ['#/shows?sort=added', { name: 'shows', sort: 'added', filters: [], minRating: 0 }],
    ['#/movies?sort=rating&min=70', { name: 'movies', sort: 'rating', filters: [], minRating: 70 }],
    ['#/shows?sort=rating&min=80', { name: 'shows', sort: 'rating', filters: [], minRating: 80 }],
    ['#/movies?min=101', { name: 'movies', sort: 'title', filters: [], minRating: 0 }],
    ['#/movies?min=-5', { name: 'movies', sort: 'title', filters: [], minRating: 0 }],
    ['#/movies?min=7.5', { name: 'movies', sort: 'title', filters: [], minRating: 0 }],
    ['#/item/12', { name: 'item', id: 12 }],
    ['#/item/0', notfound],
    ['#/item/1.5', notfound],
    ['#/item/abc', notfound],
    ['#/play/3?t=125', { name: 'play', id: 3, startSec: 125 }],
    ['#/play/3?t=-5', { name: 'play', id: 3, startSec: 0 }],
    ['#/play/3?t=soon', { name: 'play', id: 3, startSec: 0 }],
    ['#/nowhere', notfound],
    ['#/person/31', { name: 'person', id: 31 }],
    ['#/genre/18', { name: 'genre', id: 18 }],
    ['#/collection/2344', { name: 'collection', id: 2344 }],
    ['#/person/0', notfound],
    ['#/genre/-3', notfound],
    ['#/collection/x', notfound],
    ['#/settings', { name: 'settings', tab: 'library' }],
    ['#/settings?tab=keys', { name: 'settings', tab: 'keys' }],
    ['#/settings?s=playback', { name: 'settings', tab: 'playback' }],
    ['#/settings/extra', notfound],
    ['#/activity', { name: 'activity' }],
    ['#/activity/7', notfound],
  ])('%s', (hash, want) => {
    expect(parseRoute(hash)).toEqual(want);
  });

  it.each([
    ['#/shows/Lost', undefined],
    ['#/shows/Lost?s=', undefined], // regression: Number('') is 0, which would mean Specials
    ['#/shows/Lost?s=%20', undefined],
    ['#/shows/Lost?s=0', 0],
    ['#/shows/Lost?s=2', 2],
    ['#/shows/Lost?s=1.5', undefined],
    ['#/shows/Lost?s=two', undefined],
  ])('season of %s', (hash, season) => {
    expect(parseRoute(hash)).toEqual({ name: 'show', show: 'Lost', season });
  });

  it('treats malformed percent-encoding as not found instead of throwing', () => {
    expect(parseRoute('#/shows/%E0%A4%A')).toEqual(notfound);
  });
});

describe('href', () => {
  it.each(["Who's [Next]? & 100% #1", 'a/b', 'C++ + more', 'Amélie', '?', '%41'])(
    'round-trips the show name %s',
    (name) => {
      expect(parseRoute(href.show(name))).toEqual({ name: 'show', show: name, season: undefined });
      expect(parseRoute(href.show(name, 0))).toEqual({ name: 'show', show: name, season: 0 });
    },
  );

  it('round-trips library views and leaves the default sort out of the URL', () => {
    const view: LibraryView = { sort: 'year', filters: ['hdr', 'unwatched'], minRating: 0 };
    expect(parseRoute(href.moviesWith(view))).toEqual({ name: 'movies', ...view });
    const rated: LibraryView = { sort: 'rating', filters: ['4k'], minRating: 60 };
    expect(parseRoute(href.showsWith(rated))).toEqual({ name: 'shows', ...rated });
    expect(href.moviesWith({ sort: 'title', filters: [], minRating: 0 })).toBe('#/movies');
    expect(href.moviesWith({ sort: 'rating', minRating: 70 })).toBe('#/movies?sort=rating&min=70');
    expect(href.showsWith({ sort: 'added' })).toBe('#/shows?sort=added');
  });

  it('floors the resume time and omits a zero one', () => {
    expect(href.play(5)).toBe('#/play/5');
    expect(href.play(5, 0.4)).toBe('#/play/5');
    expect(href.play(5, 61.9)).toBe('#/play/5?t=61');
  });

  it('round-trips the metadata pages', () => {
    expect(parseRoute(href.person(7))).toEqual({ name: 'person', id: 7 });
    expect(parseRoute(href.genre(18))).toEqual({ name: 'genre', id: 18 });
    expect(parseRoute(href.collection(9))).toEqual({ name: 'collection', id: 9 });
    expect(parseRoute(href.settings)).toEqual({ name: 'settings', tab: 'library' });
    expect(parseRoute(href.settingsTab('ratings'))).toEqual({ name: 'settings', tab: 'ratings' });
    expect(parseRoute(href.activity)).toEqual({ name: 'activity' });
  });
});

// ---- Back ----------------------------------------------------------------------------------------

/** A fresh load of the page at hash (a deep link); clear=false keeps what pagehide stored (a reload). */
function open(hash: string, state: unknown = null, clear = true) {
  if (clear) sessionStorage.clear();
  history.replaceState(state, '', `/${hash}`);
  resetNavigation();
}
/** A traversal has landed: popstate, then hashchange, have run. */
const landed = () =>
  new Promise<void>((resolve) => window.addEventListener('popstate', () => setTimeout(resolve, 0), { once: true }));
const frames = (n: number) =>
  new Promise<void>((resolve) => {
    const next = (k: number) => (k ? requestAnimationFrame(() => next(k - 1)) : resolve());
    next(n);
  });

// jsdom has no layout: the scroll position, the page's room and its skeletons are played here.
let y = 0;
let room = 0;
let busy = false;
const { room: realRoom, busy: realBusy } = page;
beforeEach(() => {
  y = 0;
  room = 100_000;
  busy = false;
  Object.defineProperty(window, 'scrollY', { configurable: true, get: () => y });
  vi.spyOn(window, 'scrollTo').mockImplementation(((_x: number, to: number) => {
    y = to;
  }) as typeof window.scrollTo);
  page.room = () => room;
  page.busy = () => busy;
});
afterEach(() => {
  page.room = realRoom;
  page.busy = realBusy;
});

describe('the view stack', () => {
  it('goes back to the previous view through the browser history, keeping the forward one', async () => {
    open('#/');
    navigate('#/movies');
    navigate('#/item/3');
    expect(router.previous).toBe('#/movies');
    const entries = history.length;
    goBack();
    await landed();
    expect(location.hash).toBe('#/movies');
    expect(router.route.name).toBe('movies');
    expect(router.previous).toBe('#/');
    expect(history.length).toBe(entries); // traversed, not pushed
    expect(viewStack()).toEqual({ length: 3, at: 1, hashes: ['#/', '#/movies', '#/item/3'] });
  });

  it('makes one view of a link, even when popstate and hashchange both report it', async () => {
    open('#/');
    const changed = new Promise((r) => window.addEventListener('hashchange', r, { once: true }));
    location.hash = '#/shows';
    await changed;
    window.dispatchEvent(new PopStateEvent('popstate', { state: history.state })); // as Chrome sends
    expect(router.route.name).toBe('shows');
    expect(viewStack().hashes).toEqual(['#/', '#/shows']);
  });

  it('goes back once for two clicks before the first lands', async () => {
    open('#/');
    navigate('#/movies');
    navigate('#/item/3');
    goBack();
    goBack();
    await landed();
    await new Promise((r) => setTimeout(r, 20));
    expect(location.hash).toBe('#/movies');
  });

  it('drops the forward views when a new page is opened after Back', async () => {
    open('#/');
    navigate('#/movies');
    navigate('#/item/3');
    goBack();
    await landed();
    navigate('#/item/4');
    expect(viewStack().hashes).toEqual(['#/', '#/movies', '#/item/4']);
  });

  it(`keeps at most ${MAX_VIEWS} views, the newest`, () => {
    open('#/');
    for (let i = 1; i <= 60; i++) navigate(href.item(i));
    const s = viewStack();
    expect(s.length).toBe(MAX_VIEWS);
    expect(s.at).toBe(MAX_VIEWS - 1);
    expect(s.hashes[0]).toBe(href.item(11));
    expect(router.previous).toBe(href.item(59));
  });

  it('ignores a link to the page it is on', () => {
    open('#/movies');
    navigate('#/movies');
    expect(viewStack().length).toBe(1);
  });
});

describe('the fallback parents', () => {
  it.each<[Route, string | undefined]>([
    [{ name: 'home' }, undefined],
    [{ name: 'movies', sort: 'title', filters: [], minRating: 0 }, undefined],
    [{ name: 'shows', sort: 'title', filters: [], minRating: 0 }, undefined],
    [{ name: 'item', id: 3 }, '#/movies'],
    [{ name: 'show', show: 'Lost' }, '#/shows'],
    [{ name: 'play', id: 3, startSec: 40 }, '#/item/3'],
    [{ name: 'person', id: 3 }, '#/'],
    [{ name: 'collection', id: 3 }, '#/'],
    [{ name: 'genre', id: 3 }, '#/'],
    [{ name: 'settings', tab: 'library' }, '#/'],
    [{ name: 'activity' }, '#/'],
  ])('%o', (route, parent) => {
    expect(parentOf(route)?.hash).toBe(parent);
  });

  it('replaces the entry with the parent when there is no previous view', () => {
    open('#/item/5');
    expect(backTarget()).toEqual({ hash: '#/movies', label: 'Movies', previous: false });
    const entries = history.length;
    goBack();
    expect(location.hash).toBe('#/movies');
    expect(router.route.name).toBe('movies');
    expect(history.length).toBe(entries); // replaced: the browser's Back won't come back here
    expect(viewStack().length).toBe(1);
  });

  it("takes a page's own parent: an episode's show", () => {
    open('#/item/11');
    const show = { hash: href.show('Lost', 1), label: 'Lost' };
    expect(backTarget(show)).toEqual({ ...show, previous: false });
    goBack(show);
    expect(router.route).toEqual({ name: 'show', show: 'Lost', season: 1 });
  });

  it('sends the player back to its item', () => {
    open('#/play/7?t=30');
    goBack();
    expect(location.hash).toBe('#/item/7');
  });

  it('prefers the previous view to the fallback', async () => {
    open('#/person/2');
    navigate('#/item/11');
    expect(backTarget({ hash: '#/shows/Lost', label: 'Lost' })).toEqual({ hash: '#/person/2', label: 'Back', previous: true });
    goBack({ hash: '#/shows/Lost', label: 'Lost' });
    await landed();
    expect(location.hash).toBe('#/person/2');
  });
});

describe('a deep link', () => {
  it('has no previous view, even over an entry of an earlier load', () => {
    open('#/person/4', { ply: { key: 'earlier.3', scroll: 0 } });
    expect(router.previous).toBeNull();
    expect(backTarget()).toEqual({ hash: '#/', label: 'Home', previous: false });
  });

  it('restores the scroll position a reload left in its entry', async () => {
    open('#/movies', { ply: { key: 'earlier.4', scroll: 1800 } });
    await frames(3);
    expect(y).toBe(1800);
  });
});

describe("the browser's Back", () => {
  it('lands on the view goBack would, with its filters', async () => {
    open('#/');
    navigate(href.moviesWith({ sort: 'year', filters: ['4k'] }));
    navigate('#/item/2');
    history.back();
    await landed();
    expect(location.hash).toBe('#/movies?sort=year&f=4k');
    expect(router.route).toEqual({ name: 'movies', sort: 'year', filters: ['4k'], minRating: 0 });
    expect(router.previous).toBe('#/');
    goBack();
    await landed();
    expect(location.hash).toBe('#/');
  });

  it('after a fallback, leaves the page instead of returning to it', async () => {
    // Entries of an earlier load (another visit's keys), then a reload on the last one.
    history.replaceState({ ply: { key: 'earlier.7' } }, '', '/#/collection/77');
    history.pushState({ ply: { key: 'earlier.8' } }, '', '/#/genre/8');
    open('#/item/99');
    goBack();
    expect(location.hash).toBe('#/movies');
    history.back();
    await landed();
    expect(location.hash).toBe('#/collection/77'); // not #/item/99 again
    expect(router.previous).toBeNull(); // an earlier load's entry starts the list again
    expect(viewStack().length).toBe(1);
  });

  it('goes forward to the view it left', async () => {
    open('#/');
    navigate('#/settings');
    goBack();
    await landed();
    history.forward();
    await landed();
    expect(router.route.name).toBe('settings');
    expect(router.previous).toBe('#/');
  });
});

/** A card link in a fake main, at docTop() on the page (jsdom has no layout). */
function cardAt(hrefTo: string, docTop: () => number) {
  const main = document.createElement('main');
  const card = Object.assign(document.createElement('a'), { className: 'card' });
  card.setAttribute('href', hrefTo);
  card.getBoundingClientRect = () => ({ top: docTop() - y, bottom: docTop() - y + 100, height: 100, left: 0, width: 100 }) as DOMRect;
  main.append(card);
  document.body.append(main);
  return { main, card };
}

describe('scroll positions', () => {
  it('keeps one per view and restores it on Back once the page is there', async () => {
    open('#/movies');
    y = 2400;
    navigate('#/item/1');
    expect(y).toBe(0); // a new page starts at the top
    y = 300;
    busy = true; // the grid's skeletons
    room = 0;
    goBack();
    await landed();
    await frames(6);
    expect(y).toBe(300); // not yet: no jump to the top or part way
    busy = false;
    room = 9000;
    await frames(3);
    expect(y).toBe(2400);
    history.forward();
    await landed();
    await frames(3);
    expect(y).toBe(300); // the item's own
  });

  it('waits while the page grows, and takes a page that stopped growing as drawn (left at its bottom)', async () => {
    open('#/genre/18');
    y = 5000;
    navigate('#/item/1');
    room = 1000;
    goBack();
    await landed();
    for (const r of [2000, 3000, 4000, 4950]) {
      room = r; // still growing: no jump yet
      await frames(1);
      expect(y).toBe(0);
    }
    await frames(6); // 4950 for a few frames: drawn, though less than 5000
    expect(y).toBe(4950);
  });

  it('puts the link that was in the middle of the screen back where it was, over a few frames', async () => {
    let docTop = 1400;
    const { main, card } = cardAt('#/item/9', () => docTop);
    try {
      open('#/movies');
      y = 1000; // the card at 400, in the middle of the screen
      navigate('#/item/9');
      main.remove(); // the film's page has no such card
      goBack();
      await landed();
      document.body.append(main);
      // The rows above are estimates (content-visibility): the card sits 100 px lower than it did,
      // and the cards around it take their real sizes once on screen (30 px more), a frame later.
      docTop = 1500;
      let jumps = 0;
      vi.mocked(window.scrollTo).mockImplementation(((_x: number, to: number) => {
        y = to;
        if (++jumps === 2) docTop = 1530;
      }) as typeof window.scrollTo);
      await frames(6);
      expect(y).toBe(1130); // 1000, +100, then +30 the next frame
      expect(card.getBoundingClientRect().top).toBe(400);
      expect(document.activeElement).toBe(card); // the focus back on the link that was followed
    } finally {
      main.remove();
    }
  });

  it('takes the link followed (focused) as the anchor, before the one in the middle', async () => {
    const middle = cardAt('#/item/1', () => 1400);
    const followed = cardAt('#/item/2', () => 1100);
    try {
      open('#/movies');
      y = 1000;
      followed.card.focus();
      navigate('#/item/2');
      followed.card.blur(); // the film's page took the focus away
      expect(document.activeElement).not.toBe(followed.card);
      goBack();
      await landed();
      await frames(4);
      expect(document.activeElement).toBe(followed.card);
    } finally {
      middle.main.remove();
      followed.main.remove();
    }
  });

  it("focuses the page's title when the link isn't there", async () => {
    const main = document.createElement('main');
    const h1 = document.createElement('h1');
    main.append(h1);
    try {
      open('#/settings');
      navigate('#/activity');
      document.body.append(main);
      goBack();
      await landed();
      await frames(3);
      expect(document.activeElement).toBe(h1);
      expect(h1.getAttribute('tabindex')).toBe('-1');
    } finally {
      main.remove();
    }
  });

  it('stops waiting when the user scrolls', async () => {
    open('#/movies');
    y = 2000;
    navigate('#/item/1');
    busy = true;
    goBack();
    await landed();
    window.dispatchEvent(new Event('wheel'));
    busy = false;
    await frames(3);
    expect(y).toBe(0);
  });

  it('removes the input listeners it adds, arrived or cancelled', async () => {
    const inputs = new Set(['wheel', 'touchstart', 'keydown', 'pointerdown']);
    let added = 0;
    let removed = 0;
    const add = window.addEventListener.bind(window);
    const remove = window.removeEventListener.bind(window);
    vi.spyOn(window, 'addEventListener').mockImplementation(((t: string, ...rest: [EventListener, boolean | AddEventListenerOptions]) => {
      if (inputs.has(t)) added++;
      add(t, ...rest);
    }) as typeof window.addEventListener);
    vi.spyOn(window, 'removeEventListener').mockImplementation(((t: string, ...rest: [EventListener, boolean | EventListenerOptions]) => {
      if (inputs.has(t)) removed++;
      remove(t, ...rest);
    }) as typeof window.removeEventListener);
    open('#/movies');
    y = 2000;
    navigate('#/item/1');
    goBack();
    await landed();
    await frames(3);
    expect(y).toBe(2000); // arrived
    history.forward();
    await landed();
    busy = true;
    history.back();
    await landed();
    window.dispatchEvent(new Event('wheel')); // cancelled
    await frames(3);
    expect(added).toBeGreaterThan(0);
    expect(removed).toBe(added);
  });

  it('leaves a page opened during a pending restore at the top', async () => {
    open('#/movies');
    y = 2000;
    navigate('#/item/1');
    busy = true;
    goBack();
    await landed();
    navigate('#/person/3'); // before Movies is drawn
    busy = false;
    await frames(4);
    expect(location.hash).toBe('#/person/3');
    expect(y).toBe(0);
  });

  it(`gives up after ${RESTORE_WAIT_MS} ms and scrolls as far as it can`, async () => {
    open('#/movies');
    y = 2000;
    navigate('#/item/1');
    room = 800;
    busy = true; // a skeleton that never goes
    goBack();
    await landed();
    await frames(4);
    expect(y).toBe(0);
    const now = performance.now();
    vi.spyOn(performance, 'now').mockReturnValue(now + RESTORE_WAIT_MS + 1);
    await frames(2);
    expect(y).toBe(800);
  });

  it('keeps the position through a sort or filter change; a new page starts at the top', () => {
    open('#/movies');
    y = 500;
    navigate(href.moviesWith({ sort: 'year' }));
    expect(y).toBe(500);
    navigate('#/shows');
    expect(y).toBe(0);
  });
});

describe('a page that restores itself', () => {
  it('claims the restore on a return: the router leaves the scroll alone', async () => {
    open('#/movies');
    remember('first', 120);
    y = 2000;
    navigate('#/item/1');
    expect(claimRestore()).toBeUndefined(); // a view opened afresh
    goBack();
    await landed();
    const saved = claimRestore(); // as the page mounts
    expect(saved?.scroll).toBe(2000);
    expect(saved?.refocus).toBe(true); // Back puts the focus back: the page's to do now
    expect(recall('first', 0)).toBe(120);
    await frames(4);
    expect(y).toBe(0); // not the router's to move
  });

  it('is told not to move the focus on a reload', async () => {
    open('#/');
    navigate('#/movies');
    remember('first', 120);
    y = 3000;
    window.dispatchEvent(new Event('pagehide'));
    y = 0;
    resetNavigation(); // the reload
    const saved = claimRestore();
    expect(saved?.scroll).toBe(3000);
    expect(saved?.refocus).toBe(false);
    expect(recall('first', 0)).toBe(120);
  });

  it('is too late once another navigation came', async () => {
    open('#/movies');
    y = 2000;
    navigate('#/item/1');
    busy = true;
    goBack();
    await landed();
    navigate('#/person/2');
    expect(claimRestore()).toBeUndefined();
  });
});

describe('a reload', () => {
  it('finds the scroll position in the entry left by navigate()', async () => {
    open('#/movies');
    y = 1234;
    navigate('#/item/1');
    history.back();
    await landed();
    expect(history.state.ply.scroll).toBe(1234);
  });

  it('finds the views of the tab where pagehide left them', async () => {
    open('#/');
    navigate('#/movies');
    y = 3000;
    window.dispatchEvent(new Event('pagehide'));
    expect(JSON.parse(sessionStorage.getItem('ply.views') ?? '[]').map((v: { hash: string }) => v.hash)).toEqual(['#/', '#/movies']);
    y = 0;
    resetNavigation(); // the reload
    expect(router.previous).toBe('#/'); // Back still goes where it went
    await frames(3);
    expect(y).toBe(3000);
  });

  it('of another entry starts afresh', () => {
    open('#/');
    navigate('#/movies');
    window.dispatchEvent(new Event('pagehide'));
    open('#/item/4', { ply: { key: 'other.1' } }, false);
    expect(router.previous).toBeNull();
  });
});

describe('the cap', () => {
  it('takes back a dropped view at the front when the browser goes back to it', async () => {
    open('#/');
    for (let i = 1; i <= 60; i++) navigate(href.item(i));
    expect(viewStack().hashes[0]).toBe(href.item(11));
    history.go(-50);
    await landed();
    expect(location.hash).toBe(href.item(10));
    expect(viewStack().at).toBe(0);
    expect(viewStack().hashes.slice(0, 2)).toEqual([href.item(10), href.item(11)]); // not the list wiped
    expect(viewStack().length).toBe(MAX_VIEWS);
  });
});

describe('view state', () => {
  it('is kept with its view and found again on Back', async () => {
    open('#/genre/3');
    remember('sort', 'year');
    navigate('#/item/1');
    expect(recall('sort', 'title')).toBe('title');
    goBack();
    await landed();
    expect(recall('sort', 'title')).toBe('year');
  });

  it('starts again on a redirect, which adds no entry', () => {
    open('#/person/4');
    remember('x', 1);
    const entries = history.length;
    redirect('#/person/9');
    expect(history.length).toBe(entries);
    expect(router.route).toEqual({ name: 'person', id: 9 });
    expect(router.previous).toBeNull();
    expect(recall('x', 0)).toBe(0);
  });

  it('keeps the scroll on a redirect to another season of the same show, not to another page', () => {
    open('#/shows/Lost?s=1');
    y = 700;
    const entries = history.length;
    redirect(href.show('Lost', 2));
    expect(router.route).toEqual({ name: 'show', show: 'Lost', season: 2 });
    expect(history.length).toBe(entries);
    expect(y).toBe(700);
    redirect(href.show('Fringe', 1)); // another show is another page
    expect(y).toBe(0);
  });
});
