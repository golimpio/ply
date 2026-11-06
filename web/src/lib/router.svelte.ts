import { tick } from 'svelte';
import { settingsHash, settingsTab, type SettingsTab } from './settings-tabs';

export type Sort = 'title' | 'added' | 'year' | 'duration' | 'rating';
export type Filter = '4k' | 'hdr' | 'unwatched';

export interface LibraryView {
  sort: Sort;
  filters: Filter[];
  /** The minimum rating, on score100 (1 to 100); 0 for any. */
  minRating: number;
}

export type Route =
  | { name: 'home' }
  | ({ name: 'movies' } & LibraryView)
  | ({ name: 'shows' } & LibraryView)
  | { name: 'show'; show: string; season?: number }
  | { name: 'item'; id: number }
  | { name: 'play'; id: number; startSec: number }
  | { name: 'person'; id: number }
  | { name: 'genre'; id: number }
  | { name: 'collection'; id: number }
  | { name: 'settings'; tab: SettingsTab }
  | { name: 'activity' }
  | { name: 'notfound' };

const SORTS: Sort[] = ['title', 'added', 'year', 'duration', 'rating'];
const FILTERS: Filter[] = ['4k', 'hdr', 'unwatched'];

function libraryView(q: URLSearchParams, fallback: Sort): LibraryView {
  const s = q.get('sort') as Sort;
  const filters = (q.get('f') ?? '').split(',').filter((f): f is Filter => FILTERS.includes(f as Filter));
  const min = q.get('min') ?? '';
  const minRating = /^\d{1,3}$/.test(min) && Number(min) <= 100 ? Number(min) : 0;
  return { sort: SORTS.includes(s) ? s : fallback, filters, minRating };
}

/**
 * Hash forms: #/ (home), #/movies[?sort=&f=&min=], #/shows[?sort=&f=&min=], #/shows/<name>[?s=<season>],
 * #/item/<id>, #/play/<id>[?t=<resume seconds>], #/person/<id>, #/genre/<id>,
 * #/collection/<id>, #/settings[?tab=<tab>], #/activity.
 */
export function parseRoute(hash: string): Route {
  const [path, query = ''] = hash.replace(/^#\/?/, '').split('?');
  const [head = '', arg = ''] = path.split('/');
  const q = new URLSearchParams(query);
  try {
    switch (head) {
      case '':
        return { name: 'home' };
      case 'movies':
        return { name: 'movies', ...libraryView(q, 'title') };
      case 'shows': {
        if (!arg) return { name: 'shows', ...libraryView(q, 'title') };
        const s = q.get('s') ?? '';
        const season = s.trim() === '' ? NaN : Number(s); // Number('') would be 0, i.e. Specials
        return { name: 'show', show: decodeURIComponent(arg), season: Number.isInteger(season) ? season : undefined };
      }
      case 'settings':
        if (!arg) return { name: 'settings', tab: settingsTab(q) };
        break;
      case 'activity':
        if (!arg) return { name: head };
        break;
      case 'person':
      case 'genre':
      case 'collection': {
        const id = Number(arg);
        if (!Number.isInteger(id) || id <= 0) break;
        return { name: head, id };
      }
      case 'item':
      case 'play': {
        const id = Number(arg);
        if (!Number.isInteger(id) || id <= 0) break;
        if (head === 'item') return { name: 'item', id };
        const t = Number(q.get('t'));
        return { name: 'play', id, startSec: Number.isFinite(t) && t > 0 ? t : 0 };
      }
    }
  } catch {
    // malformed percent-encoding in the show name
  }
  return { name: 'notfound' };
}

function viewQuery(v: Partial<LibraryView>, fallback: Sort): string {
  const p = new URLSearchParams();
  if (v.sort && v.sort !== fallback) p.set('sort', v.sort);
  if (v.filters?.length) p.set('f', v.filters.join(','));
  if (v.minRating && v.minRating > 0) p.set('min', String(v.minRating));
  const s = p.toString();
  return s ? `?${s}` : '';
}

export const href = {
  home: '#/',
  movies: '#/movies',
  shows: '#/shows',
  moviesWith: (v: Partial<LibraryView>) => `#/movies${viewQuery(v, 'title')}`,
  showsWith: (v: Partial<LibraryView>) => `#/shows${viewQuery(v, 'title')}`,
  show: (name: string, season?: number) =>
    `#/shows/${encodeURIComponent(name)}${season === undefined ? '' : `?s=${season}`}`,
  item: (id: number) => `#/item/${id}`,
  play: (id: number, startSec = 0) => {
    const t = Math.floor(startSec);
    return `#/play/${id}${t > 0 ? `?t=${t}` : ''}`;
  },
  person: (id: number) => `#/person/${id}`,
  genre: (id: number) => `#/genre/${id}`,
  collection: (id: number) => `#/collection/${id}`,
  settings: '#/settings',
  settingsTab: (tab: SettingsTab) => settingsHash(tab),
  activity: '#/activity',
};

// ---- Back ----------------------------------------------------------------------------------------
//
// The views of this visit, oldest first, mirror the browser's history entries one for one: each
// entry made here carries its view's key in history.state, so a traversal (the browser's Back or
// Forward, or goBack's history.back()) finds its view, and with it the scroll position and the
// state the page left there. A link, a typed address or navigate() makes a new view; redirect()
// replaces the current one. A view whose key isn't known (an entry of an earlier load of the page,
// or one dropped by the cap) starts the list again: it has no previous view, like a deep link.

/** Where Back goes when there is no previous view: a page's parent, and its name for the button. */
export interface BackTarget {
  hash: string;
  label: string;
}

/** The parent of a page that isn't a top-level section (undefined for Home, Movies and Shows). */
export function parentOf(route: Route): BackTarget | undefined {
  switch (route.name) {
    case 'home':
    case 'movies':
    case 'shows':
      return undefined;
    case 'item':
      return { hash: href.movies, label: 'Movies' }; // an episode's page passes its show instead
    case 'show':
      return { hash: href.shows, label: 'Shows' };
    case 'play':
      return { hash: href.item(route.id), label: 'Details' };
    default:
      return { hash: href.home, label: 'Home' };
  }
}

/** The most views kept; Back past the oldest goes to the parent. */
export const MAX_VIEWS = 50;
/** How long a restored scroll position waits for its page to be there. */
export const RESTORE_WAIT_MS = 8000;
/** Frames without growth after which a page counts as drawn (one left at its very bottom). */
const STILL_FRAMES = 3;
/** Frames the jump is corrected over, at most, while the cards around it take their real sizes. */
const MAX_CORRECTIONS = 10;
/** Frames the link must stay put for the restore to be over. */
const STEADY_FRAMES = 2;
/** Where the views of the tab wait across a reload. */
const STORE_KEY = 'ply.views';

/** The link nearest the middle of the screen when a view was left, and where it was. */
export interface Anchor {
  href: string;
  /** From the top of the screen. */
  top: number;
  /** From the top of the page: which one, when a title is in several rails. */
  at: number;
}

interface View {
  key: string;
  hash: string;
  scroll: number;
  anchor?: Anchor;
  /** What a page keeps with its view beyond the URL (see remember). */
  state: Record<string, unknown>;
}
type Tag = { key: string; scroll?: number; anchor?: Anchor };

const visit = Math.random().toString(36).slice(2, 8);
let serial = 0;
let views: View[] = [];
let at = 0;
let backPending = false;
let backTimer: ReturnType<typeof setTimeout> | undefined;

const tagOf = (state: unknown): Tag | undefined => (state as { ply?: Tag } | null)?.ply;
const tagFor = (v: View): Tag => ({ key: v.key, scroll: v.scroll, anchor: v.anchor });
const here = () => location.hash || '#/';
const newView = (hash: string, key = `${visit}.${++serial}`): View => ({ key, hash, scroll: 0, state: {} });

export const router = $state<{ route: Route; previous: string | null }>({
  route: parseRoute(location.hash),
  previous: null,
});

function publish(route: Route) {
  router.route = route;
  router.previous = at > 0 ? views[at - 1].hash : null;
}

// Sort/filter/season changes re-render in place; only real navigation scrolls to the top.
const samePage = (cur: Route, next: Route) =>
  ((next.name === 'movies' || next.name === 'shows') && next.name === cur.name) ||
  (next.name === 'show' && cur.name === 'show' && next.show === cur.show);

const LINKS = 'main a[href^="#/"]:not(.back)';

const onScreen = (r: DOMRect) => r.height > 0 && r.bottom >= 0 && r.top <= window.innerHeight;
const anchorOf = (a: Element, r: DOMRect): Anchor => ({ href: a.getAttribute('href') ?? '', top: r.top, at: r.top + window.scrollY });

/** The link followed (it has the focus, after a click or Enter), or else the one nearest the middle. */
function anchorHere(): Anchor | undefined {
  const focused = document.activeElement;
  if (focused?.matches(LINKS)) {
    const r = focused.getBoundingClientRect();
    if (onScreen(r)) return anchorOf(focused, r);
  }
  let best: Anchor | undefined;
  let far = Infinity;
  for (const a of document.querySelectorAll(LINKS)) {
    const r = a.getBoundingClientRect();
    if (!onScreen(r)) continue;
    const d = Math.hypot(r.left + r.width / 2 - window.innerWidth / 2, r.top + r.height / 2 - window.innerHeight / 2);
    if (d < far) [far, best] = [d, anchorOf(a, r)];
  }
  return best;
}

/** The page being left: where it was scrolled to, and what was in the middle of the screen. */
function save(v: View) {
  v.scroll = window.scrollY;
  v.anchor = anchorHere();
}

function push(hash: string): View {
  restoring++;
  save(views[at]);
  const v = newView(hash);
  views.splice(at + 1, Infinity, v);
  if (views.length > MAX_VIEWS) views.splice(0, views.length - MAX_VIEWS);
  at = views.length - 1;
  return v;
}

/** Opens a page as a link would: a new history entry. */
export function navigate(hash: string): void {
  if (hash === here()) return; // a link to the page it is on adds no entry either
  const cur = router.route;
  const left = views[at];
  const v = push(hash);
  // The entry left keeps where it was in itself too: a reload, or a page of another site, finds it.
  history.replaceState({ ply: tagFor(left) }, '');
  history.pushState({ ply: { key: v.key } }, '', hash);
  const next = parseRoute(hash);
  publish(next);
  if (!samePage(cur, next)) window.scrollTo(0, 0);
}

/**
 * Shows another page in place of this one (a redirect): no new history entry. Another state of the
 * same page (a show's season) stays where it is scrolled to, as a link to it would.
 */
export function redirect(hash: string): void {
  restoring++;
  const cur = router.route;
  const v = newView(hash, views[at].key);
  views[at] = v;
  history.replaceState({ ply: { key: v.key } }, '', hash);
  const next = parseRoute(hash);
  publish(next);
  if (!samePage(cur, next)) window.scrollTo(0, 0);
}

/** Where goBack(fallback) goes, and what its button says. */
export function backTarget(fallback?: BackTarget): BackTarget & { previous: boolean } {
  if (router.previous !== null) return { hash: router.previous, label: 'Back', previous: true };
  return { ...(fallback ?? parentOf(router.route) ?? { hash: href.home, label: 'Home' }), previous: false };
}

/**
 * Back to the previous view of this visit, through the browser's own history so that its Back and
 * Forward stay in step; with none (a deep link), to the parent page, replacing this entry so that
 * the browser's Back doesn't return here. `fallback` overrides parentOf (an episode's show).
 */
export function goBack(fallback?: BackTarget): void {
  if (at > 0) {
    if (backPending) return; // a second click before the first has landed
    backPending = true;
    backTimer = setTimeout(() => (backPending = false), 1000);
    history.back();
    return;
  }
  redirect(backTarget(fallback).hash);
}

/** Keeps a value with the current view, for Back to find it (a sort that isn't in the URL). */
export function remember(name: string, value: unknown): void {
  views[at].state[name] = value;
}

/** A value remember()ed with the current view, or `initial`. */
export function recall<T>(name: string, initial: T): T {
  return name in views[at].state ? (views[at].state[name] as T) : initial;
}

/** What the scroll restoration asks of the page; replaceable in tests (jsdom has no layout). */
export const page = {
  /** How far the document can scroll now. */
  room: () => document.documentElement.scrollHeight - window.innerHeight,
  /** No page yet (the app connecting), or one still drawing its skeletons or fetching its details. */
  busy: () => !document.querySelector('main') || document.querySelector('main [aria-busy="true"]') !== null,
};

let restoring = 0;
let pending: { run: number; scroll: number; anchor?: Anchor; refocus: boolean; claimed: boolean } | undefined;
const INPUTS = ['wheel', 'touchstart', 'keydown', 'pointerdown'] as const;

/**
 * For a page that puts itself back where it was (a virtualized grid, from its first visible index):
 * called as it mounts, it answers where its view was left when this is a return to it (Back,
 * Forward, a reload), and takes that restore over: the router then leaves the scroll and the focus
 * alone for this view. Undefined for a view opened afresh. What the page remember()ed is recall()ed
 * as usual. `refocus`: whether the router would have put the focus back on the anchor's link (Back
 * and Forward do; a reload doesn't).
 */
export function claimRestore(): { scroll: number; anchor?: Anchor; refocus: boolean } | undefined {
  if (!pending || pending.run !== restoring) return undefined;
  pending.claimed = true;
  return { scroll: pending.scroll, anchor: pending.anchor, refocus: pending.refocus };
}

/**
 * Scrolls to y once the page is there: no skeletons left, and the link that was in the middle of
 * the screen drawn again, or room enough to scroll that far, or a page that has stopped growing (one
 * left at its very bottom). Then it puts that link back where it was, over a few frames: the cards
 * of a grid skip their rendering off screen (content-visibility, see MediaCard) and the page left
 * may have had some at an estimated size, so y alone could land on another card. It gives up
 * waiting after RESTORE_WAIT_MS, stops at once if the user scrolls or another navigation comes
 * first, and on arriving moves the focus to that link (or the page's title), as the page left had
 * it on the link that was followed.
 */
function restoreScroll(y: number, anchor?: Anchor, refocus = true) {
  const run = ++restoring;
  const job = { run, scroll: y, anchor, refocus, claimed: false };
  pending = job;
  if (y <= 0) window.scrollTo(0, 0);
  const cancel = () => {
    if (run === restoring) restoring++;
  };
  for (const e of INPUTS) window.addEventListener(e, cancel, { passive: true });
  const done = (arrived: boolean) => {
    for (const e of INPUTS) window.removeEventListener(e, cancel);
    if (pending === job) pending = undefined;
    if (arrived && refocus) focusBack(anchor);
  };
  const until = performance.now() + RESTORE_WAIT_MS;
  let room = -1;
  let still = 0;
  let shown = 0;
  let jumped = false;
  let frames = 0;
  let steady = 0;
  const step = () => {
    if (run !== restoring || job.claimed) return done(false);
    if (!jumped) {
      const late = performance.now() >= until;
      if (page.busy() && !late) {
        shown = 0;
        return void requestAnimationFrame(step);
      }
      // A frame drawn first, where the page is: content-visibility lays out every card of a new
      // grid once, in the first frame it is drawn, and they keep those real sizes after
      // (contain-intrinsic-size: auto). Jumping before it would leave the cards above y at the
      // estimate, and y rows away from where it was.
      if (shown++ < 1 && !late) return void requestAnimationFrame(step);
      const now = page.room();
      still = now <= room ? still + 1 : 0;
      room = now;
      const drawn = !!(anchor && closest(anchor)) || now >= y || still >= STILL_FRAMES;
      if (!drawn && !late) return void requestAnimationFrame(step);
      window.scrollTo(0, Math.min(y, Math.max(now, 0)));
      jumped = true;
    }
    // The cards around it take their real sizes over the next frames: put it back where it was
    // until it stays there for a few frames running.
    const el = anchor && closest(anchor);
    const off = anchor && el ? el.getBoundingClientRect().top - anchor.top : 0;
    if (frames++ < MAX_CORRECTIONS && el) {
      if (Math.abs(off) >= 1) {
        window.scrollTo(0, window.scrollY + off);
        steady = 0;
      }
      if (steady++ < STEADY_FRAMES) return void requestAnimationFrame(step);
    }
    done(true);
  };
  void tick().then(() => requestAnimationFrame(step));
}

/** The link of the anchor on the page now, the one nearest where it was. */
function closest(anchor: Anchor): HTMLElement | undefined {
  let best: HTMLElement | undefined;
  let far = Infinity;
  for (const a of document.querySelectorAll<HTMLElement>(LINKS)) {
    if (a.getAttribute('href') !== anchor.href) continue;
    const d = Math.abs(a.getBoundingClientRect().top + window.scrollY - anchor.at);
    if (d < far) [far, best] = [d, a];
  }
  return best;
}

function focusBack(anchor?: Anchor) {
  const link = anchor && closest(anchor);
  if (link) return link.focus({ preventScroll: true });
  const title = document.querySelector<HTMLElement>('main h1');
  if (!title) return;
  if (!title.hasAttribute('tabindex')) title.setAttribute('tabindex', '-1');
  title.focus({ preventScroll: true });
}

const serialOf = (key: string) => {
  const [v, n] = key.split('.');
  return { v, n: Number(n) };
};

/**
 * A traversal to an entry whose view isn't kept: one dropped by the cap (older than the oldest, or
 * newer than the newest, of this visit) takes its place at that end; any other (an earlier load's)
 * starts the list again.
 */
function adopt(tag: Tag): number {
  const v = { ...newView(here(), tag.key), scroll: tag.scroll ?? 0, anchor: tag.anchor };
  const k = serialOf(tag.key);
  const first = serialOf(views[0].key);
  const last = serialOf(views[views.length - 1].key);
  if (k.v === first.v && k.n < first.n) {
    views.unshift(v);
    if (views.length > MAX_VIEWS) views.pop();
    return 0;
  }
  if (k.v === last.v && k.n > last.n) {
    views.push(v);
    if (views.length > MAX_VIEWS) views.shift();
    return views.length - 1;
  }
  views = [v];
  return 0;
}

// A link, a typed address (both untagged), the browser's Back or Forward, or history.back(). A
// fragment navigation can fire popstate and hashchange both: the second finds its view current.
function onHistory() {
  const tag = tagOf(history.state);
  if (tag?.key === views[at].key) return;
  backPending = false;
  clearTimeout(backTimer);
  const cur = router.route;
  const next = parseRoute(location.hash);
  if (!tag) {
    const v = push(here());
    history.replaceState({ ply: { key: v.key } }, '');
    publish(next);
    if (!samePage(cur, next)) window.scrollTo(0, 0);
    return;
  }
  save(views[at]);
  const j = views.findIndex((v) => v.key === tag.key);
  if (j >= 0) {
    at = j;
    views[j].hash = here();
  } else {
    at = adopt(tag);
  }
  publish(next);
  restoreScroll(views[at].scroll, views[at].anchor);
}

function storedViews(): View[] | undefined {
  try {
    const list: unknown = JSON.parse(sessionStorage.getItem(STORE_KEY) ?? 'null');
    if (!Array.isArray(list)) return undefined;
    return list
      .filter((v) => typeof v?.key === 'string' && typeof v.hash === 'string')
      .map((v) => ({ key: v.key, hash: v.hash, scroll: Number(v.scroll) || 0, anchor: v.anchor, state: { ...v.state } }));
  } catch {
    return undefined;
  }
}

/**
 * Starts the list of views at the current entry, as a load does. A reload finds the views of the tab
 * where pagehide left them, previous views and scroll positions with them; any other load (a deep
 * link, a bookmark, a new tab) starts afresh. Exported for tests.
 */
export function resetNavigation(): void {
  const tag = tagOf(history.state);
  const stored = tag && storedViews();
  const j = stored ? stored.findIndex((v) => v.key === tag.key) : -1;
  if (stored && j >= 0) {
    views = stored;
    at = j;
  } else {
    views = [{ ...newView(here()), scroll: tag?.scroll ?? 0, anchor: tag?.anchor }];
    at = 0;
  }
  views[at].hash = here();
  backPending = false;
  clearTimeout(backTimer);
  restoring++;
  history.replaceState({ ply: { key: views[at].key } }, '');
  publish(parseRoute(location.hash));
  if (views[at].scroll > 0) restoreScroll(views[at].scroll, views[at].anchor, false);
}

/** Writes the views of the tab down, for a reload (pagehide). Exported for tests. */
export function storeViews(): void {
  save(views[at]);
  history.replaceState({ ply: tagFor(views[at]) }, '');
  try {
    sessionStorage.setItem(STORE_KEY, JSON.stringify(views));
  } catch {
    // full or blocked: a reload then starts afresh
  }
}

/** The list of views, for tests. */
export const viewStack = () => ({ length: views.length, at, hashes: views.map((v) => v.hash) });

if ('scrollRestoration' in history) history.scrollRestoration = 'manual'; // restoreScroll's job
resetNavigation();
window.addEventListener('popstate', onHistory);
window.addEventListener('hashchange', onHistory);
window.addEventListener('pagehide', storeViews);
