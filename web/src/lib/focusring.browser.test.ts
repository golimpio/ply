import { beforeEach, describe, expect, it } from 'vitest';
import { focusRing, setFocusRing, toggleFocusRing } from './focusring.svelte';

describe('focusRing', () => {
  beforeEach(() => {
    localStorage.clear();
    setFocusRing(false);
  });

  it('defaults to disabled', () => {
    expect(focusRing.enabled).toBe(false);
    expect(document.documentElement.dataset.focusRing).toBeUndefined();
  });

  it('can be enabled and disabled', () => {
    setFocusRing(true);
    expect(focusRing.enabled).toBe(true);
    expect(document.documentElement.dataset.focusRing).toBe('true');
    expect(localStorage.getItem('ply.focus_ring')).toBe('true');

    setFocusRing(false);
    expect(focusRing.enabled).toBe(false);
    expect(document.documentElement.dataset.focusRing).toBeUndefined();
    expect(localStorage.getItem('ply.focus_ring')).toBeNull();
  });

  it('can be toggled', () => {
    toggleFocusRing();
    expect(focusRing.enabled).toBe(true);
    expect(document.documentElement.dataset.focusRing).toBe('true');

    toggleFocusRing();
    expect(focusRing.enabled).toBe(false);
    expect(document.documentElement.dataset.focusRing).toBeUndefined();
  });
});
