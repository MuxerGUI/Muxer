import { defineConfig } from 'vite'

export default defineConfig({
  base: './',
  plugins: [
    {
      name: 'wails-iife-script',
      transformIndexHtml(html) {
        return html
          .replace(/<script type="module" crossorigin src="(\.\/assets\/[^"]+)"><\/script>/,
            '<script defer src="$1"></script>')
          .replace(/<script type="module" crossorigin src="(\/assets\/[^"]+)"><\/script>/,
            '<script defer src="$1"></script>')
      },
    },
  ],
  build: {
    rollupOptions: {
      output: {
        format: 'iife',
        name: 'MuxerApp',
        inlineDynamicImports: true,
      },
    },
  },
})
