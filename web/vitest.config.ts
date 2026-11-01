import { defineConfig, mergeConfig } from 'vitest/config';
import viteConfig from './vite.config.ts';

// Unit tests for src/lib. The Svelte plugin from vite.config compiles runes
// modules (*.svelte.ts); the browser build of Svelte keeps $state reactive.
export default mergeConfig(
  viteConfig,
  defineConfig({
    resolve: { conditions: ['browser'] },
    test: {
      environment: 'jsdom',
      include: ['src/**/*.test.ts'],
      setupFiles: ['src/test-setup.ts'],
      restoreMocks: true,
    },
  }),
);
