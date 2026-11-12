// Not in Chrome: it keeps 50 history entries a tab, as many as the view stack holds, so it can
// never go back to a view the stack dropped. Safari keeps more, and can.
import { describe, expect, it, vi } from 'vitest';
import { href, MAX_VIEWS, navigate, viewStack } from './router.svelte';
import { landed, open } from './router.testing';

describe('the cap', () => {
  it('takes back a dropped view at the front when the browser goes back to it', async () => {
    vi.spyOn(window, 'scrollTo').mockImplementation(() => {}); // jsdom does not implement scrolling
    open('#/');
    for (let i = 1; i <= 60; i++) navigate(href.item(i));
    expect(viewStack().hashes[0]).toBe(href.item(11));
    const back = landed();
    history.go(-50);
    await back;
    expect(location.hash).toBe(href.item(10));
    expect(viewStack().at).toBe(0);
    expect(viewStack().hashes.slice(0, 2)).toEqual([href.item(10), href.item(11)]); // not the list wiped
    expect(viewStack().length).toBe(MAX_VIEWS);
  });
});
