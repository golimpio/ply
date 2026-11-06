#!/usr/bin/env node
// Every colour, font, font size, radius and motion value of the web UI comes from
// src/styles/tokens.css. This finds the ones that don't: raw colours (hex, rgb()/hsl()/...,
// named colours), raw font-family and font-size, raw border-radius, and raw durations or easings in
// transition and animation. It reads the <style> blocks of .svelte files and every .css file under
// src, except tokens.css itself, and prints file:line for each.
//
//   node scripts/check-tokens.mjs [dir]     (default: the src next to this script)
//
// Exit status 1 when it finds any. The few true exceptions are in ALLOW below, each with its reason.
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { dirname, join, relative, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const SRC = join(dirname(fileURLToPath(import.meta.url)), '..', 'src');

/**
 * Declarations allowed as they are. `file` is relative to the scanned directory (with /), `prop`
 * the property, `value` a pattern its value must match.
 */
const ALLOW = [
  {
    // The placeholder tile's gradient is made per title: its hue (--h) is set inline from the title.
    // Saturation, lightness and the hue shift are tokens; the hsl() only puts them together.
    file: 'components/Tile.svelte',
    prop: 'background',
    value: /^linear-gradient\(145deg, hsl\(var\(--h\) var\(--[\w-]+\)\), hsl\(calc\(var\(--h\) \+ var\(--[\w-]+\)\) var\(--[\w-]+\)\)\)$/,
  },
];

// CSS named colours (transparent and currentColor are fine: they are not a colour of the theme).
const NAMED = new Set(
  ('aliceblue antiquewhite aqua aquamarine azure beige bisque black blanchedalmond blue blueviolet brown burlywood cadetblue ' +
    'chartreuse chocolate coral cornflowerblue cornsilk crimson cyan darkblue darkcyan darkgoldenrod darkgray darkgreen darkgrey ' +
    'darkkhaki darkmagenta darkolivegreen darkorange darkorchid darkred darksalmon darkseagreen darkslateblue darkslategray ' +
    'darkslategrey darkturquoise darkviolet deeppink deepskyblue dimgray dimgrey dodgerblue firebrick floralwhite forestgreen ' +
    'fuchsia gainsboro ghostwhite gold goldenrod gray green greenyellow grey honeydew hotpink indianred indigo ivory khaki ' +
    'lavender lavenderblush lawngreen lemonchiffon lightblue lightcoral lightcyan lightgoldenrodyellow lightgray lightgreen ' +
    'lightgrey lightpink lightsalmon lightseagreen lightskyblue lightslategray lightslategrey lightsteelblue lightyellow lime ' +
    'limegreen linen magenta maroon mediumaquamarine mediumblue mediumorchid mediumpurple mediumseagreen mediumslateblue ' +
    'mediumspringgreen mediumturquoise mediumvioletred midnightblue mintcream mistyrose moccasin navajowhite navy oldlace olive ' +
    'olivedrab orange orangered orchid palegoldenrod palegreen paleturquoise palevioletred papayawhip peachpuff peru pink plum ' +
    'powderblue purple rebeccapurple red rosybrown royalblue saddlebrown salmon sandybrown seagreen seashell sienna silver skyblue ' +
    'slateblue slategray slategrey snow springgreen steelblue tan teal thistle tomato turquoise violet wheat white whitesmoke ' +
    'yellow yellowgreen').split(' '),
);
// System colours: the browser's or the OS's, not the theme's.
const SYSTEM = new Set(
  ('canvas canvastext linktext visitedtext activetext buttonface buttontext buttonborder field fieldtext highlight ' +
    'highlighttext selecteditem selecteditemtext mark marktext graytext accentcolor accentcolortext').split(' '),
);
const KEYWORDS = /^(inherit|initial|unset|revert|revert-layer)$/;
const MOTION = /^(transition|animation)(-(duration|timing-function|delay))?$/;
const EASINGS = /(?<![\w-])(linear|ease|ease-in|ease-out|ease-in-out|step-start|step-end)(?![\w-])|(?<![\w-])(cubic-bezier|steps|linear)\(/i;
const DURATION = /(?<![\w.-])\d*\.?\d+m?s(?![\w-])/i;
// Properties whose words are names (an area, an animation), never colours.
const NAMES = /^(grid-area|grid-row|grid-column|grid-template-areas|animation-name|view-transition-name|transition-property|will-change|counter-reset|counter-increment|list-style-type|container-name)$/;

/** What is wrong with one declaration, or '' when nothing is. */
function problem(prop, value) {
  // A token stands for itself: look only at what is around the var()s. Strings and url()s are text.
  const v = value.replace(/!important\s*$/i, '').trim();
  const bare = v
    .replace(/var\(--[\w-]+\)/g, 'TOKEN')
    .replace(/url\((?:'[^']*'|"[^"]*"|[^)]*)\)|'[^']*'|"[^"]*"/gi, 'TEXT');
  if (KEYWORDS.test(bare) || bare === 'TOKEN') return '';
  if (prop.startsWith('--')) {
    // A component's own custom property holds tokens, never a raw length, duration or easing.
    const raw = /(?<![\w.#-])\d*\.?\d+(px|rem|em|%|vw|vh|m?s)(?![\w-])/i.exec(bare) ?? EASINGS.exec(bare);
    if (raw) return `raw "${raw[0]}" in ${prop}: custom properties of components hold tokens (put the value in tokens.css)`;
  }
  if (prop === 'font-family') return `raw font-family "${v}": use --font-text, --font-display or --font-mono`;
  if (prop === 'font') return `raw font shorthand "${v}": use font: inherit, or the font-* properties with tokens`;
  if (prop === 'font-size') return `raw font-size "${v}": use a --fs-* token`;
  if (/^border(-(top|bottom|start|end)-(left|right|start|end))?-radius$/.test(prop)) {
    const raw = bare.split(/[\s/]+/).filter((t) => t && t !== 'TOKEN' && t !== '0' && !KEYWORDS.test(t));
    return raw.length ? `raw border-radius "${v}": use a --radius-* token` : '';
  }
  if (MOTION.test(prop)) {
    const dur = DURATION.exec(bare);
    if (dur) return `raw duration "${dur[0]}" in ${prop}: use a --motion-* token`;
    const ease = EASINGS.exec(bare);
    if (ease) return `raw easing "${ease[0]}" in ${prop}: use --ease, --ease-linear or --ease-in-out`;
  }
  const hex = /#[0-9a-fA-F]{3,8}(?![\w-])/.exec(bare);
  if (hex) return `raw colour "${hex[0]}" in ${prop}: use a semantic colour token`;
  const fn = /(?<![\w-])(rgba?|hsla?|hwb|lab|lch|oklab|oklch|color|light-dark)\(/i.exec(bare);
  if (fn) return `raw colour ${fn[1]}() in ${prop}: use a semantic colour token`;
  if (NAMES.test(prop)) return '';
  for (const word of bare.toLowerCase().match(/(?<![\w-])[a-z]+(?![\w-])/g) ?? []) {
    if (NAMED.has(word)) return `named colour "${word}" in ${prop}: use a semantic colour token`;
    if (SYSTEM.has(word)) return `system colour "${word}" in ${prop}: use a semantic colour token`;
  }
  return '';
}

/**
 * The names of the primitives: the custom properties tokens.css defines between its "Primitives"
 * and "Semantic tokens" markers. Only tokens.css may use them.
 */
function primitivesOf(tokens) {
  const m = /=+ Primitives =+([\s\S]*?)=+ Semantic tokens =+/.exec(tokens);
  if (!m) throw new Error('styles/tokens.css: the "======== Primitives ========" and "======== Semantic tokens ========" markers are gone');
  return new Set([...m[1].replace(/\/\*[\s\S]*?\*\//g, '').matchAll(/(--[\w-]+)\s*:/g)].map((x) => x[1]));
}

/** The declarations of a stylesheet, as [property, value, line] (line counted from `firstLine`). */
function declarations(css, firstLine) {
  // Comments become spaces (newlines kept), so that offsets and lines stay true.
  const text = css.replace(/\/\*[\s\S]*?\*\//g, (c) => c.replace(/[^\n]/g, ' '));
  const out = [];
  // A declaration follows {, ; or } (after a nested rule); a selector never matches, as its "value"
  // would run into a {.
  const re = /(?<=[;{}]\s*)([-\w]+)\s*:\s*([^;{}]+?)\s*(?=;|\})/g;
  for (const m of text.matchAll(re)) {
    const line = firstLine + text.slice(0, m.index).split('\n').length - 1;
    out.push([m[1].toLowerCase(), m[2].replace(/\s+/g, ' '), line]);
  }
  return out;
}

function stylesheets(path, text) {
  if (path.endsWith('.css')) return [[text, 1]];
  const sheets = [];
  for (const m of text.matchAll(/<style[^>]*>([\s\S]*?)<\/style>/g)) {
    const start = m.index + m[0].indexOf('>') + 1;
    sheets.push([m[1], text.slice(0, start).split('\n').length]);
  }
  return sheets;
}

function files(dir) {
  return readdirSync(dir).flatMap((name) => {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) return name === 'node_modules' ? [] : files(p);
    return /\.(svelte|css)$/.test(name) ? [p] : [];
  });
}

/**
 * The raw values of sources given as { 'components/A.svelte': text, ... } (paths relative to src,
 * with /): [{ file, line, message }]. styles/tokens.css is not checked; its primitives are, as uses
 * anywhere else (from the tokens.css given, else from the app's own).
 */
export function checkSources(sources) {
  const tokens = sources['styles/tokens.css'] ?? readFileSync(join(SRC, 'styles', 'tokens.css'), 'utf8');
  const primitives = primitivesOf(tokens);
  const found = [];
  for (const [file, text] of Object.entries(sources)) {
    if (file === 'styles/tokens.css' || !/\.(svelte|css)$/.test(file)) continue;
    for (const [css, firstLine] of stylesheets(file, text)) {
      for (const [prop, value, line] of declarations(css, firstLine)) {
        const primitive = [...value.matchAll(/var\((--[\w-]+)/g)].find((x) => primitives.has(x[1]));
        const message = primitive
          ? `primitive ${primitive[1]} in ${prop}: components use the semantic tokens (see tokens.css)`
          : problem(prop, value);
        if (!message) continue;
        if (ALLOW.some((a) => a.file === file && a.prop === prop && a.value.test(value))) continue;
        found.push({ file, line, message });
      }
    }
  }
  return found;
}

/** The .svelte and .css files under `dir` (default: the app's src), as checkSources takes them. */
export function readSources(dir = SRC) {
  return Object.fromEntries(files(dir).map((p) => [relative(dir, p).split(sep).join('/'), readFileSync(p, 'utf8')]));
}

/** Every raw value in the .svelte and .css files under `dir`. */
export function checkTokens(dir = SRC) {
  return checkSources(readSources(dir));
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  const dir = process.argv[2] ?? SRC;
  const found = checkTokens(dir);
  for (const f of found) console.log(`${join(relative(process.cwd(), dir), f.file)}:${f.line}: ${f.message}`);
  console.log(found.length ? `\n${found.length} raw value(s): see src/styles/tokens.css` : 'check-tokens: no raw values');
  process.exit(found.length ? 1 : 0);
}
