// The frosted glass of dialogs, menus and the bars must reach every browser. The minifier keeps
// only -webkit-backdrop-filter when a rule spells out both (Safari blurred, Chrome and Firefox
// didn't), so the components write the unprefixed property alone and the build adds the prefix.
import { beforeAll, describe, expect, it } from 'vitest';
import { builtCss } from '../../scripts/built-css.mjs';

let css = '';
beforeAll(async () => {
  css = await builtCss();
}, 60_000);

/** The declarations of each rule whose selector matches, from the minified CSS. */
const rules = (selector: RegExp) => [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)].filter((m) => selector.test(m[1])).map((m) => m[2]);
const unprefixed = (body: string) => /(^|;)backdrop-filter:/.test(body);

describe('backdrop-filter in the built CSS', () => {
  it.each([
    ['the dialogs (Dialog, the search palette)', /^dialog\.svelte-/, 2],
    ['the menu', /^\.list\.svelte-/, 1],
    ["the header, once scrolled", /^\.bar\.scrolled\.svelte-/, 1],
    ['the tab bar', /^\.tabs\.svelte-/, 1],
  ])('%s: unprefixed, for Chrome and Firefox', (_, selector, at_least) => {
    const blurred = rules(selector).filter((body) => body.includes('backdrop-filter'));
    // Positive control: the rules are there, with a blur.
    expect(blurred.length).toBeGreaterThanOrEqual(at_least);
    for (const body of blurred) expect(unprefixed(body), body).toBe(true);
  });

  it('the header when not scrolled has no backdrop-filter (transparent over hero)', () => {
    // Positive control: the header once scrolled DOES have backdrop-filter.
    const scrolled = rules(/^\.bar\.scrolled\.svelte-/).filter((b) => b.includes('backdrop-filter'));
    expect(scrolled.length).toBeGreaterThanOrEqual(1);

    // When unscrolled, .bar has no backdrop-filter property.
    const unscrolled = rules(/^\.bar(?!\.scrolled)\.svelte-/).filter(unprefixed);
    expect(unscrolled).toEqual([]);
  });

  it('the hero scrim darkens toward the top with --scrim-hero-top', () => {
    const scrimRules = rules(/^\.scrim\.svelte-/).filter((b) => b.includes('--scrim-hero-top'));
    expect(scrimRules.length).toBeGreaterThanOrEqual(1);
    for (const body of scrimRules) {
      expect(body).toContain('--scrim-hero-top');
    }
  });

  it('no rule anywhere has the -webkit- form alone', () => {
    const lone = [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)].filter((m) => m[2].includes('-webkit-backdrop-filter') && !unprefixed(m[2])).map((m) => m[1]);
    expect(lone).toEqual([]);
  });
});
