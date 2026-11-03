import { defineConfig } from 'vite';
import type { Plugin } from 'vite';
import react from '@vitejs/plugin-react-swc';

// Resolved at runtime by the host import map to the host's React; never bundle them.
// Everything else (MUI, Emotion, plugin-theme) is bundled at the version this plugin pins.
const HOST_PROVIDED = ['react', 'react-dom', 'react/jsx-runtime'];

// A bundled CommonJS dependency that require()s React compiles to a stub that
// throws on load, and the host only logs plugin load errors to the console.
const failOnHostRequire = (): Plugin => ({
  name: 'fail-on-host-require',
  apply: 'build',
  renderChunk(code, chunk) {
    for (const id of HOST_PROVIDED) {
      if (code.includes(`__require("${id}")`)) {
        this.error(`${chunk.fileName} calls require("${id}"); alias that dependency to its ESM build`);
      }
    }
    return null;
  },
});

export default defineConfig(({ command }) => ({
  plugins: [react(), failOnHostRequire()],
  // Library mode leaves process.env untouched, but bundled MUI reads NODE_ENV.
  define:
    command === 'build'
      ? { 'process.env.NODE_ENV': JSON.stringify('production') }
      : undefined,
  build: {
    lib: {
      entry: 'src/main.tsx',
      formats: ['es'],
      fileName: () => 'main.js',
    },
    rollupOptions: {
      external: HOST_PROVIDED,
    },
  },
  server: {
    port: 3001,
    cors: true,
  },
}));
