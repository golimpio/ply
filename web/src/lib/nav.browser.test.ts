import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { go, morph } from './nav';
import { resetNavigation, router, viewStack } from './router.svelte';

// A loose view of document, so tests can install and remove the API freely.
const doc = document as unknown as { startViewTransition?: (cb: () => Promise<void>) => unknown };

let reduced = false;
beforeEach(() => {
  reduced = false;
  window.matchMedia = ((q: string) => ({ matches: q.includes('reduce') && reduced })) as typeof window.matchMedia;
  history.replaceState(null, '', '/#/');
  resetNavigation();
  doc.startViewTransition = undefined; // Chrome has one: each test installs its own, or none
});
afterEach(() => {
  delete doc.startViewTransition;
  document.body.replaceChildren();
});

/** A transition that animates: none of its promises rejects. */
const animated = () => ({ ready: Promise.resolve(), finished: Promise.resolve(), updateCallbackDone: Promise.resolve() });

function card(hash: string) {
  const a = document.createElement('a');
  a.setAttribute('href', hash);
  const art = document.createElement('div');
  art.setAttribute('data-art', '');
  a.append(art);
  document.body.append(a);
  return { a, art };
}

describe('go', () => {
  it('falls back to a plain hash change without View Transitions', () => {
    go('#/item/3');
    expect(location.hash).toBe('#/item/3');
    expect(router.route).toEqual({ name: 'item', id: 3 });
    expect(viewStack().hashes).toEqual(['#/', '#/item/3']); // a view Back can return from
  });

  it('names only the clicked art for the morph, then routes inside the transition', async () => {
    const { art } = card('#/item/4');
    let nameDuringCapture = '';
    let update: Promise<void> | undefined;
    doc.startViewTransition = vi.fn((cb: () => Promise<void>) => {
      nameDuringCapture = art.style.getPropertyValue('view-transition-name');
      update = cb();
      return animated();
    });

    go('#/item/4', art);
    await update;
    expect(nameDuringCapture).toBe('hero-art');
    expect(art.style.getPropertyValue('view-transition-name')).toBe(''); // unique name released
    expect(location.hash).toBe('#/item/4');
    expect(router.route).toEqual({ name: 'item', id: 4 });
    expect(router.previous).toBe('#/');
  });

  // Chrome aborts the whole transition when two elements have one name (ready rejects: "Transition
  // was aborted because of invalid state. Snapshot capture failed"): a page with a landing hero of
  // its own (a show) morphing one of its cards.
  it("unnames the page's own hero while the clicked card is captured, then gives the name back", async () => {
    const hero = document.createElement('div');
    hero.style.setProperty('view-transition-name', 'hero-art');
    document.body.append(hero);
    const { art } = card('#/item/8');
    let named: string[] = [];
    let update: Promise<void> | undefined;
    doc.startViewTransition = vi.fn((cb: () => Promise<void>) => {
      named = [hero, art].map((el) => el.style.getPropertyValue('view-transition-name'));
      update = cb();
      return animated();
    });

    go('#/item/8', art);
    await update;
    expect(named).toEqual(['none', 'hero-art']); // one hero-art in the old state: the card's
    expect(hero.style.getPropertyValue('view-transition-name')).toBe('hero-art');
    expect(location.hash).toBe('#/item/8');
  });

  // Chrome captures later, then runs the skipped one's callback before the new one's.
  it('a second morph before the first is captured: still one hero-art, and every name given back', async () => {
    const hero = document.createElement('div');
    hero.style.setProperty('view-transition-name', 'hero-art');
    document.body.append(hero);
    const one = card('#/item/11').art;
    const two = card('#/item/12').art;
    const callbacks: (() => Promise<void>)[] = [];
    doc.startViewTransition = vi.fn((cb: () => Promise<void>) => {
      callbacks.push(cb);
      return animated();
    });
    const names = () => [hero, one, two].map((el) => el.style.getPropertyValue('view-transition-name'));

    go('#/item/11', one);
    go('#/item/12', two);
    expect(names()).toEqual(['none', '', 'hero-art']); // the second capture
    await callbacks[0](); // the skipped transition's update still runs
    expect(names()).toEqual(['none', '', 'hero-art']);
    await callbacks[1]();
    expect(names()).toEqual(['hero-art', '', '']);
    expect(location.hash).toBe('#/item/12');
  });

  it('skips the animation when the user prefers reduced motion', () => {
    reduced = true;
    const vt = vi.fn();
    doc.startViewTransition = vt;
    go('#/item/5');
    expect(vt).not.toHaveBeenCalled();
    expect(location.hash).toBe('#/item/5');
  });
});

// The promises a transition rejects, seen as the page sees them: unhandledrejection events. A
// listener of the test's own also keeps vitest from failing the run on purpose.
describe("a transition's rejections", () => {
  let seen: unknown[] = [];
  const onRejection = (e: PromiseRejectionEvent) => {
    e.preventDefault();
    seen.push(e.reason);
  };
  beforeEach(() => {
    seen = [];
    window.addEventListener('unhandledrejection', onRejection);
  });
  afterEach(() => window.removeEventListener('unhandledrejection', onRejection));
  const settle = () => new Promise((r) => setTimeout(r, 10));

  function rejecting(reason: unknown) {
    let update: Promise<void> | undefined;
    doc.startViewTransition = vi.fn((cb: () => Promise<void>) => {
      update = cb();
      const ready = Promise.reject(reason);
      return { ready, finished: update, updateCallbackDone: update };
    });
    return () => update;
  }

  it('skipped by another one starting (an AbortError): no page error, and the page still opens', async () => {
    const update = rejecting(new DOMException('Transition was skipped. New ViewTransition started', 'AbortError'));
    go('#/item/9');
    await update();
    await settle();
    expect(location.hash).toBe('#/item/9');
    expect(seen).toEqual([]);
  });

  it('aborted for any other reason: surfaces (the positive control)', async () => {
    const err = new DOMException('Transition was aborted because of invalid state. Snapshot capture failed', 'InvalidStateError');
    const update = rejecting(err);
    go('#/item/10');
    await update();
    await settle();
    expect(location.hash).toBe('#/item/10');
    expect(seen).toEqual([err]);
  });

  // As Chrome does when the update callback throws: ready, finished and updateCallbackDone all
  // reject with its error, and each one left unhandled is a page error of its own.
  it('a failing update: its error surfaces once, not three times', async () => {
    const err = new Error('boom');
    doc.startViewTransition = vi.fn(() => ({
      ready: Promise.reject(err),
      finished: Promise.reject(err),
      updateCallbackDone: Promise.reject(err),
    }));
    go('#/item/13');
    await settle();
    expect(seen).toEqual([err]);
  });
});

describe('morph', () => {
  it('takes over a plain left click', () => {
    const { a } = card('#/item/6');
    const e = new MouseEvent('click', { button: 0, cancelable: true });
    Object.defineProperty(e, 'currentTarget', { value: a });
    morph(e);
    expect(e.defaultPrevented).toBe(true);
    expect(location.hash).toBe('#/item/6');
  });

  it.each(['metaKey', 'ctrlKey', 'shiftKey', 'altKey'])('leaves a %s click to the browser (new tab, etc.)', (key) => {
    const { a } = card('#/item/7');
    const e = new MouseEvent('click', { button: 0, cancelable: true, [key]: true });
    Object.defineProperty(e, 'currentTarget', { value: a });
    morph(e);
    expect(e.defaultPrevented).toBe(false);
    expect(location.hash).toBe('#/');
  });
});
