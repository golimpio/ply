// Navigation with a card → detail morph (same-document View Transitions).
// The clicked card's artwork gets the one shared name, `hero-art`, only for the
// duration of the transition, so the name stays unique on a page where the same
// item can appear in several rails. The page's own hero-art (a show's hero, when
// one of its episodes is clicked) is unnamed meanwhile: two of one name abort it.
import { tick } from 'svelte';
import { navigate } from './router.svelte';

const NAME = 'hero-art';
const reduced = () => matchMedia('(prefers-reduced-motion: reduce)').matches;

/** The page's own landing hero (Hero.svelte names it inline): a show's, when one of its episodes is clicked. */
const named = () =>
  [...document.querySelectorAll<HTMLElement>('[style*="view-transition-name"]')].filter(
    (el) => el.style.getPropertyValue('view-transition-name') === NAME,
  );

interface Transition {
  ready: Promise<void>;
  finished: Promise<void>;
  updateCallbackDone: Promise<void>;
}

/** Gives back the names a morph holds until its update callback runs (or a later go() starts). */
let release: (() => void) | undefined;

export function go(hash: string, from?: Element | null): void {
  const doc = document as Document & { startViewTransition?: (cb: () => Promise<void>) => Transition };
  if (!doc.startViewTransition || reduced()) {
    navigate(hash);
    return;
  }
  release?.();
  const art = from as HTMLElement | null | undefined;
  // Two elements of one name abort the whole transition, so the clicked card's art is the only one
  // while the page left is captured.
  const others = art ? named() : [];
  for (const el of others) el.style.setProperty('view-transition-name', 'none');
  art?.style.setProperty('view-transition-name', NAME);
  const mine = () => {
    if (release !== mine) return; // a later go() gave them back already
    release = undefined;
    art?.style.removeProperty('view-transition-name');
    for (const el of others) el.style.setProperty('view-transition-name', NAME);
  };
  release = mine;
  const vt = doc.startViewTransition(async () => {
    mine();
    navigate(hash);
    await tick();
  });
  // A failing update rejects all three promises with its error: it is reported once, below.
  vt.finished.catch(() => {});
  vt.ready.catch((e: unknown) => {
    // Another go() before this one is drawn skips it; the page still opens.
    if (e instanceof DOMException && e.name === 'AbortError') return;
    // Anything else surfaces (duplicate names; a resize or a hidden tab before the capture would
    // too): the update's own error through updateCallbackDone, or else this one.
    return vt.updateCallbackDone.then(() => {
      throw e;
    });
  });
}

/** onclick handler for an <a href="#/..."> that should morph its [data-art] child. */
export function morph(e: MouseEvent): void {
  if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
  const a = e.currentTarget as HTMLAnchorElement;
  const hash = a.getAttribute('href');
  if (!hash?.startsWith('#')) return;
  e.preventDefault();
  go(hash, a.querySelector('[data-art]'));
}
