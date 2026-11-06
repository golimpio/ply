// The CSS the production build ships, built in memory with the app's own vite.config (nothing is
// written to dist). For tests of what the minifier does to the authored CSS.
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { build } from 'vite';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');

export async function builtCss() {
  const out = await build({ root, configFile: join(root, 'vite.config.ts'), logLevel: 'silent', build: { write: false } });
  const outputs = (Array.isArray(out) ? out : [out]).flatMap((o) => o.output);
  return outputs
    .filter((f) => f.type === 'asset' && f.fileName.endsWith('.css'))
    .map((f) => (typeof f.source === 'string' ? f.source : new TextDecoder().decode(f.source)))
    .join('\n');
}
