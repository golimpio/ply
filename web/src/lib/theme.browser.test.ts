import { beforeEach, describe, expect, it } from 'vitest';
import { theme, toggleTheme } from './theme.svelte';

describe('theme', () => {
  beforeEach(() => {
    localStorage.clear();
    if (theme.current !== 'dark') {
      toggleTheme();
    }
  });

  it('defaults to dark', () => {
    expect(theme.current).toBe('dark');
    expect(document.documentElement.dataset.theme).toBeUndefined();
  });

  it('toggles from dark to light and back to dark', () => {
    toggleTheme();
    expect(theme.current).toBe('light');
    expect(document.documentElement.dataset.theme).toBe('light');
    expect(localStorage.getItem('ply.theme')).toBe('light');

    toggleTheme();
    expect(theme.current).toBe('dark');
    expect(document.documentElement.dataset.theme).toBeUndefined();
    expect(localStorage.getItem('ply.theme')).toBe('dark');
  });

  it('updates meta theme-color tag if present', () => {
    const meta = document.createElement('meta');
    meta.setAttribute('name', 'theme-color');
    meta.setAttribute('content', '#0c0c0b');
    document.head.append(meta);

    toggleTheme(); // switch to light
    expect(meta.getAttribute('content')).toBe('#f6f4ef');

    toggleTheme(); // switch back to dark
    expect(meta.getAttribute('content')).toBe('#0c0c0b');

    meta.remove();
  });
});
