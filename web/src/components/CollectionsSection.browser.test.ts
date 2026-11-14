// The Collections section on a film and on a show. What follows opens with its own top hairline (File
// and playback on a film, the Cast rail on a show), so a line under the last collection row would sit
// a little above a second one. Chrome computes the styles: no fake of them here.
import '../styles/tokens.css'; // --line, which the component's own stylesheet resolves against
import { flushSync, mount, unmount } from 'svelte';
import { describe, expect, it } from 'vitest';
import CollectionsSection from './CollectionsSection.svelte';

function render(...names: string[]) {
  const target = document.body.appendChild(document.createElement('div'));
  const component = mount(CollectionsSection, {
    target,
    props: { collections: names.map((name, i) => ({ id: i + 1, name })) },
  });
  flushSync();
  return {
    rows: [...target.querySelectorAll<HTMLAnchorElement>('.collection-row')],
    close() {
      unmount(component);
      target.remove();
    },
  };
}

const hairline = (el: Element) => getComputedStyle(el).borderBottomWidth;

describe('CollectionsSection hairlines', () => {
  it('rules between the collections and leaves the last one to the section below', () => {
    const { rows, close } = render('Alienoid Collection', 'The Mann Collection');
    try {
      expect(rows).toHaveLength(2);
      expect(hairline(rows[0])).toBe('1px'); // positive control: a row does draw its hairline
      expect(hairline(rows[1])).toBe('0px'); // and the last one draws none, or it doubles the next
    } finally {
      close();
    }
  });

  it('rules under no row when there is one collection', () => {
    const { rows, close } = render('Alienoid Collection');
    try {
      expect(rows).toHaveLength(1);
      expect(getComputedStyle(rows[0]).display).toBe('flex'); // positive control: the styles are in
      expect(hairline(rows[0])).toBe('0px');
    } finally {
      close();
    }
  });
});
