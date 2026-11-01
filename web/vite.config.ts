import { readFileSync, mkdirSync, writeFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { defineConfig, type Plugin } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// emptyOutDir wipes web/dist, including the .gitkeep that lets `go build`
// embed an otherwise empty dist/. Put it back (same content) after the build.
function keepGitkeep(): Plugin {
  let keep = '';
  let file = '';
  return {
    name: 'ply-keep-gitkeep',
    apply: 'build',
    configResolved(config) {
      file = resolve(config.root, config.build.outDir, '.gitkeep');
    },
    buildStart() {
      try {
        keep = readFileSync(file, 'utf8');
      } catch {
        keep = '';
      }
    },
    closeBundle() {
      mkdirSync(resolve(file, '..'), { recursive: true });
      writeFileSync(file, keep);
    },
  };
}

export default defineConfig({
  plugins: [svelte(), keepGitkeep()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    // hls.js (~580 kB) is a lazy chunk, only fetched when a browser needs it for HLS.
    chunkSizeWarningLimit: 650,
  },
  server: {
    proxy: {
      '/api': 'http://localhost:8420',
    },
  },
});
