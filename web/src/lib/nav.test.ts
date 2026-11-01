import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { go, morph } from './nav';
import { router } from './router.svelte';

// A loose view of document, so tests can install and remove the API freely.
const doc = document as unknown as { startViewTransition?: (cb: () => Promise<void>) => unknown };

let reduced = false;
beforeEach(() => {
  reduced = false;
  window.matchMedia = ((q: string) => ({ matches: q.includes('reduce') && reduced })) as typeof window.matchMedia;
  history.replaceState(null, '', '/#/');
  router.route = { name: 'home' };
});
afterEach(() => {
  delete doc.startViewTransition;
});

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
  });

  it('names only the clicked art for the morph, then routes inside the transition', async () => {
    const { art } = card('#/item/4');
    let nameDuringCapture = '';
    let update: Promise<void> | undefined;
    doc.startViewTransition = vi.fn((cb: () => Promise<void>) => {
      nameDuringCapture = art.style.getPropertyValue('view-transition-name');
      update = cb();
      return {};
    });

    go('#/item/4', art);
    await update;
    expect(nameDuringCapture).toBe('hero-art');
    expect(art.style.getPropertyValue('view-transition-name')).toBe(''); // unique name released
    expect(location.hash).toBe('#/item/4');
    expect(router.route).toEqual({ name: 'item', id: 4 });
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
