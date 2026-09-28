import { writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

import preact from '@preact/preset-vite';
import type { Plugin } from 'vite';
import { defineConfig } from 'vitest/config';

const outDir = fileURLToPath(new URL('../internal/ui/dist', import.meta.url));
const server = 'http://localhost:8080';

function keepGitkeep(): Plugin {
  return {
    name: 'stackorder-keep-gitkeep',
    apply: 'build',
    closeBundle() {
      writeFileSync(join(outDir, '.gitkeep'), '');
    },
  };
}

export default defineConfig({
  base: '/',
  plugins: [preact(), keepGitkeep()],
  build: {
    outDir: '../internal/ui/dist',
    emptyOutDir: true,
    assetsDir: 'assets',
  },
  server: {
    proxy: {
      '/v1': server,
      '/auth': server,
    },
  },
  preview: {
    port: 4173,
  },
  test: {
    environment: 'jsdom',
    include: ['src/**/*.test.{ts,tsx}'],
    setupFiles: ['./src/test/setup.ts'],
    restoreMocks: true,
  },
});
