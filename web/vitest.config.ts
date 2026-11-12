import { defineConfig, mergeConfig } from 'vitest/config';
import { playwright } from '@vitest/browser-playwright';
import viteConfig from './vite.config.ts';

// Unit tests for src, in three projects chosen by the file name:
// - *.browser.test.ts run in Chrome (the one the e2e checks use): the DOM, storage, layout and
//   events are the browser's own, not a fake of them.
// - *.jsdom.test.ts run under jsdom. Most of them import modules afresh for each test
//   (vi.resetModules), which browser mode cannot do: a module is loaded once a page.
// - every other *.test.ts is pure logic and runs in Node, which starts fastest.
// The Svelte plugin from vite.config compiles runes modules (*.svelte.ts). The 'browser'
// condition gives the DOM projects the browser build of Svelte, which mounts components and keeps
// $state reactive; Node gets the server build.
const browser = '**/*.browser.test.ts';
const jsdom = '**/*.jsdom.test.ts';

// Not the dev server's /api proxy: in Chrome a relative fetch would reach a running ply.
const { server: _devServer, ...vite } = viteConfig;

export default mergeConfig(
  vite,
  defineConfig({
    resolve: { conditions: ['browser'] },
    test: {
      restoreMocks: true,
      // In web/: Vitest's default is node_modules at the root it finds above, which can be outside
      // the project (a node_modules in the home directory), where a sandbox refuses to write.
      fsModuleCachePath: 'node_modules/.vitest-cache',
      projects: [
        {
          extends: true,
          test: { name: 'node', environment: 'node', include: ['src/**/*.test.ts'], exclude: [browser, jsdom] },
        },
        { extends: true, test: { name: 'jsdom', environment: 'jsdom', include: [`src/${jsdom}`] } },
        {
          extends: true,
          test: {
            name: 'browser',
            include: [`src/${browser}`],
            setupFiles: ['src/test-browser-setup.ts'],
            browser: {
              enabled: true,
              headless: true,
              provider: playwright({ launchOptions: { channel: 'chrome' } }),
              instances: [{ browser: 'chromium' }],
            },
          },
        },
      ],
    },
  }),
);
