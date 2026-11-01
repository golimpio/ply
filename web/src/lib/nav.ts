// Navigation with a card → detail morph (same-document View Transitions).
// The clicked card's artwork gets the one shared name, `hero-art`, only for the
// duration of the transition, so the name stays unique on a page where the same
// item can appear in several rails.
import { tick } from 'svelte';
import { parseRoute, router } from './router.svelte';

const reduced = () => matchMedia('(prefers-reduced-motion: reduce)').matches;

export function go(hash: string, from?: Element | null): void {
  const doc = document as Document & { startViewTransition?: (cb: () => Promise<void>) => unknown };
  if (!doc.startViewTransition || reduced()) {
    location.hash = hash;
    return;
  }
  const art = from as HTMLElement | null | undefined;
  art?.style.setProperty('view-transition-name', 'hero-art');
  doc.startViewTransition(async () => {
    art?.style.removeProperty('view-transition-name');
    history.pushState(null, '', hash);
    router.route = parseRoute(hash);
    window.scrollTo(0, 0);
    await tick();
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
