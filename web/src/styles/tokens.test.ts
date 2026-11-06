// scripts/check-tokens.mjs: what it flags, what it lets through, and the real tree (src) clean.
import { describe, expect, it } from 'vitest';
import { checkSources, readSources } from '../../scripts/check-tokens.mjs';

// Every stylesheet of the app as it is on disk ('components/A.svelte': text). Read by the script, not
// imported: vitest hands .css imports over empty, even with ?raw.
const tree = readSources();

const svelte = (css: string) => `<script lang="ts">\n  let a = 1;\n</script>\n\n<p>{a}</p>\n\n<style>\n${css}\n</style>\n`;
const clean = `  .a {\n    color: var(--text);\n    border: 1px solid transparent;\n    font: inherit;\n    border-radius: 0 var(--radius) 0 0;\n    transition: opacity var(--motion-hover) var(--ease);\n  }`;
const TOKENS = '/* ======== Primitives ======== */\n:root {\n  --stone-900: #111;\n  --dur-9: 0.9s;\n  /* ======== Semantic tokens ======== */\n  --surface: var(--stone-900);\n}\n';
/** One declaration in app.css, line 2. */
const decl = (d: string) => checkSources({ 'styles/tokens.css': TOKENS, 'app.css': `.a {\n  ${d}\n}\n` });

describe('check-tokens', () => {
  it('finds one raw colour, at its file and line', () => {
    const found = checkSources({ 'components/A.svelte': svelte(`  .a {\n    margin: 0;\n    color: #123456;\n  }`) });
    // <style> is line 7, ".a {" line 8, "color" line 10.
    expect(found).toEqual([{ file: 'components/A.svelte', line: 10, message: expect.stringContaining('#123456') }]);
  });

  it('passes a sheet that uses tokens only, and never checks tokens.css', () => {
    expect(checkSources({ 'components/A.svelte': svelte(clean), 'app.css': clean, 'styles/tokens.css': `${TOKENS}:root { --x: #fff; --y: 0.85rem; }` })).toEqual([]);
  });

  it.each([
    ['an rgb() colour', 'background: rgb(255 255 255 / 0.08);'],
    ['an upper-case RGB() colour', 'color: RGB(1 2 3);'],
    ['an hsl() colour', 'color: hsl(10 50% 50%);'],
    ['a named colour', 'border-color: tomato;'],
    ['a named colour in a mix', 'background: color-mix(in oklab, var(--danger) 85%, white);'],
    ['a system colour', 'color: CanvasText;'],
    ['another system colour', 'background: GrayText;'],
    ['a colour behind !important', 'color: #fff !important;'],
    ['an upper-case property', 'COLOR: #fff;'],
    ['an upper-case property with a rule of its own', 'FONT-SIZE: 12px;'],
    ['a colour in a custom property', '--mine: #000;'],
    ['a radius in a custom property', '--r: 8px;'],
    ['a duration in a custom property', '--d: 0.2s;'],
    ['a font family', 'font-family: Georgia, serif;'],
    ['a font shorthand', 'font: 600 1rem/1.2 system-ui;'],
    ['a font size', 'font-size: 0.85rem;'],
    ['a font size behind !important', 'font-size: 12px !important;'],
    ['a radius', 'border-radius: 8px;'],
    ['a radius after a /', 'border-radius: var(--radius) / 50%;'],
    ['a corner radius', 'border-top-left-radius: 50%;'],
    ['a logical corner radius', 'border-start-start-radius: 4px;'],
    ['another logical corner radius', 'border-end-end-radius: 6px;'],
    ['a duration', 'transition: opacity 0.2s var(--ease);'],
    ['a duration in ms', 'transition: opacity 200ms var(--ease);'],
    ['a delay', 'transition-delay: 0.15s;'],
    ['an animation delay', 'animation-delay: 1s;'],
    ['an easing', 'animation: spin var(--motion-spin) linear infinite;'],
    ['a cubic-bezier', 'transition-timing-function: cubic-bezier(0.2, 0.7, 0.2, 1);'],
    ['a primitive colour', 'background: var(--stone-900);'],
    ['a primitive duration', 'transition: opacity var(--dur-9) var(--ease);'],
  ])('flags %s', (_, d) => {
    expect(decl(d)).toEqual([{ file: 'app.css', line: 2, message: expect.any(String) }]);
  });

  it.each([
    ['a colour word in a url()', 'background: url(/img/red.png);'],
    ['a hex in a string', "content: '#fff';"],
    ['a colour word in a string', 'content: "Red";'],
    ['a grid area named like a colour', 'grid-area: gold;'],
    ['a semantic token', 'background: var(--surface);'],
    ['a token behind !important', 'font-size: var(--fs-sm) !important;'],
    ['an elliptical radius of tokens', 'border-radius: var(--radius) / var(--radius-lg);'],
    ['an animation name', 'animation-name: ease-spin;'],
  ])('lets through %s', (_, d) => {
    expect(decl(d)).toEqual([]);
  });

  it("flags the app's own primitives when no tokens.css is given", () => {
    for (const p of ['--velvet-900', '--dur-150', '--size-0875', '--radius-8']) {
      expect(checkSources({ 'app.css': `.a {\n  x: var(${p});\n}\n` })).toEqual([{ file: 'app.css', line: 2, message: expect.stringContaining(p) }]);
    }
  });

  it('refuses a tokens.css without its two layers marked', () => {
    expect(() => checkSources({ 'styles/tokens.css': ':root { --x: #fff; }', 'app.css': '' })).toThrow(/markers/);
  });

  it('reads the last declaration of a block, with no ;', () => {
    expect(checkSources({ 'app.css': '.a{color:#fff}' })).toEqual([{ file: 'app.css', line: 1, message: expect.stringContaining('#fff') }]);
  });

  it('reads a declaration after a nested rule', () => {
    expect(checkSources({ 'app.css': '.a {\n  &:hover { color: var(--text); }\n  color: #fff;\n}\n' })).toEqual([
      { file: 'app.css', line: 3, message: expect.stringContaining('#fff') },
    ]);
  });

  it('reads every <style> block of a component', () => {
    const two = `${svelte('  .a {\n    color: var(--text);\n  }')}<style>\n  .b {\n    color: #fff;\n  }\n</style>\n`;
    // The first block ends at line 11; the second opens at 12, so "color" is line 14.
    expect(checkSources({ 'components/A.svelte': two })).toEqual([{ file: 'components/A.svelte', line: 14, message: expect.stringContaining('#fff') }]);
  });

  it('reads a comment as nothing, and keeps the lines true after it', () => {
    const found = checkSources({ 'app.css': '/* color: red; #fff\n   more */\n.a {\n  color: var(--text); /* not #000 */\n  font-size: 13px;\n}\n' });
    expect(found).toEqual([{ file: 'app.css', line: 5, message: expect.stringContaining('13px') }]);
  });

  it("allows the title tile's generated hue only where ALLOW says, and nothing else there", () => {
    const tile = svelte('  .tile {\n    background: linear-gradient(145deg, hsl(var(--h) var(--tile-from)), hsl(calc(var(--h) + var(--tile-hue-shift)) var(--tile-to)));\n  }');
    // Positive control: the same declaration anywhere else is a raw hsl().
    expect(checkSources({ 'components/Tile.svelte': tile, 'components/Other.svelte': tile })).toEqual([
      { file: 'components/Other.svelte', line: 9, message: expect.stringContaining('hsl()') },
    ]);
    // Negative control: in Tile.svelte, another background is not exempt.
    expect(checkSources({ 'components/Tile.svelte': svelte('  .tile {\n    background: #f00;\n  }') })).toEqual([
      { file: 'components/Tile.svelte', line: 9, message: expect.stringContaining('#f00') },
    ]);
  });

  it('finds no raw value in the real tree', () => {
    // Positive control: the tree was read, components and app.css included, with their text.
    expect(Object.keys(tree)).toEqual(expect.arrayContaining(['app.css', 'components/MediaCard.svelte', 'styles/tokens.css']));
    expect(tree['app.css']).toContain('.btn {');
    expect(tree['components/MediaCard.svelte']).toContain('<style>');
    expect(checkSources(tree)).toEqual([]);
  });
});
