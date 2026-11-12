// What the router's tests share (router.browser.test.ts, router-cap.jsdom.test.ts).
import { resetNavigation } from './router.svelte';

/** A fresh load of the page at hash (a deep link); clear=false keeps what pagehide stored (a reload). */
export function open(hash: string, state: unknown = null, clear = true) {
  if (clear) sessionStorage.clear();
  history.replaceState(state, '', `/${hash}`);
  resetNavigation();
}

/** A traversal has landed: popstate, then hashchange, have run. */
export const landed = () =>
  new Promise<void>((resolve) => window.addEventListener('popstate', () => setTimeout(resolve, 0), { once: true }));
