// The dialog of a person's credit outside the library. The page keeps one mounted and changes its
// credit: what was loaded for one credit must never show for the next, and a load still running when
// the dialog closed must not leave the next one spinning.
import '../styles/tokens.css';
import { flushSync, mount, unmount } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { PersonFilmographyEntry } from '../lib/api';
import CreditPreview from './CreditPreview.svelte';

const red: PersonFilmographyEntry = { kind: 'movie', tmdbId: 110, title: 'Three Colors: Red', year: 1994, department: 'Acting', creditKind: 'cast', inLibrary: false };
// A credit TMDB can't be asked about (TVDB's only).
const tvdbOnly: PersonFilmographyEntry = { kind: 'series', tvdbId: 77, title: 'A TVDB Show', department: 'Acting', creditKind: 'cast', inLibrary: false };
const overview = 'Valentine befriends a retired judge.';

function answer() {
  return new Response(JSON.stringify({ kind: 'movie', tmdbId: 110, title: 'Three Colors: Red', year: 1994, overview, genres: [], cast: [], crew: [] }), {
    headers: { 'Content-Type': 'application/json' },
  });
}

function render(entry: PersonFilmographyEntry) {
  const target = document.body.appendChild(document.createElement('div'));
  const props = $state({ entry, open: true });
  const component = mount(CreditPreview, { target, props });
  flushSync();
  const dialog = () => document.querySelector('dialog')!;
  return {
    props,
    text: () => dialog().textContent ?? '',
    loading: () => !!dialog().querySelector('.loading'),
    close() {
      unmount(component);
      target.remove();
    },
  };
}

afterEach(() => vi.unstubAllGlobals());

describe('CreditPreview', () => {
  it('shows nothing of the last credit for one TMDB cannot be asked about', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => answer()));
    const d = render(red);
    try {
      await vi.waitFor(() => expect(d.text()).toContain(overview)); // positive control: the details came in
      d.props.open = false;
      flushSync();
      d.props.entry = tvdbOnly;
      d.props.open = true;
      flushSync();
      expect(d.text()).toContain('A TVDB Show');
      expect(d.text()).not.toContain(overview);
    } finally {
      d.close();
    }
  });

  it('closing drops a load still running, and the next credit is not left loading', async () => {
    const fetch = vi.fn((_: string, init: RequestInit) => new Promise<Response>((_, reject) => init.signal?.addEventListener('abort', () => reject(new Error('aborted')))));
    vi.stubGlobal('fetch', fetch);
    const d = render(red);
    try {
      expect(d.loading()).toBe(true); // positive control: it is loading
      d.props.open = false;
      flushSync();
      expect(fetch.mock.calls[0][1].signal?.aborted).toBe(true); // closing dropped the request
      d.props.entry = tvdbOnly;
      d.props.open = true;
      flushSync();
      await new Promise((r) => setTimeout(r));
      expect(d.loading()).toBe(false);
    } finally {
      d.close();
    }
  });
});
