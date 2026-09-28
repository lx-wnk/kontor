import process from 'node:process'
import { fileURLToPath, URL } from 'node:url'
import tailwindcss from '@tailwindcss/vite'
import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'
import { VitePWA } from 'vite-plugin-pwa'

const DASHBOARD_PORT = process.env.DASHBOARD_PORT || '13120'
const VITE_DEV_PORT = Number(process.env.VITE_DEV_PORT) || 5173
const DESKTOP_DEV_NONCE = process.env.DESKTOP_DEV_NONCE || ''

const D3_MODULE_RE = /node_modules\/d3-/
const VUE_MODULE_RE = /node_modules\/(?:@vue|vue)\//

export default defineConfig(({ mode }) => ({
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  plugins: [
    tailwindcss(),
    vue(),
    // Lets desktop/target_dev.go tell OUR Vite dev server (started by `task
    // dev:desktop:wails`, which shares this nonce through Taskfile.yml) apart
    // from another process squatting the same loopback port.
    DESKTOP_DEV_NONCE
      ? {
          name: 'desktop-dev-nonce',
          configureServer(server) {
            server.middlewares.use((_req, res, next) => {
              res.setHeader('x-dashboard-dev-nonce', DESKTOP_DEV_NONCE)
              next()
            })
          },
        }
      : null,
    // PWA intent: offline capability is limited to precached static assets only;
    // HTML navigation fallback is not handled (no NavigationRoute in injectManifest strategy).
    // API calls (SSE, /api/*) always require a live server connection.
    // injectManifest strategy is used so src/sw.ts can include custom Background Sync logic.
    VitePWA({
      // Use 'prompt' so the user controls when a new service worker activates.
      // This prevents stale cached assets from silently replacing a running session.
      registerType: 'prompt',
      // Registration is done by src/utils/serviceWorker.ts, not by an injected
      // script: the same bundle is served to a browser (register) and to the
      // desktop shell (do not register — it serves this SPA from its own
      // in-process server, so a precache only pins the previous build).
      injectRegister: null,
      // injectManifest: custom SW at src/sw.ts handles Background Sync + precaching.
      strategies: 'injectManifest',
      srcDir: 'src',
      filename: 'sw.ts',
      includeAssets: ['favicon.ico', 'apple-touch-icon.png', 'icon-192.png', 'icon-512.png'],
      manifest: {
        name: 'Kontor',
        short_name: 'Kontor',
        description: 'Real-time monitoring dashboard for Claude Code agents',
        theme_color: '#1e293b',
        background_color: '#0f172a',
        display: 'standalone',
        start_url: '/',
        icons: [
          {
            src: '/icon-192.png',
            sizes: '192x192',
            type: 'image/png',
          },
          {
            src: '/icon-512.png',
            sizes: '512x512',
            type: 'image/png',
          },
          // Maskable variant (SEO-P3-1b). The current icon is a solid fill, so it
          // is inherently mask-safe (no content near the edges to clip); a
          // dedicated padded asset should replace it once real branding exists.
          {
            src: '/icon-512.png',
            sizes: '512x512',
            type: 'image/png',
            purpose: 'maskable',
          },
        ],
      },
      injectManifest: {
        // Raise the per-file limit to 5 MB to avoid Workbox warnings on large chunks.
        maximumFileSizeToCacheInBytes: 5 * 1024 * 1024,
        globPatterns: ['**/*.{js,css,html,ico,woff2}'],
      },
    }),
  ],
  build: {
    // Emit directly into the Go embed directory. server/frontend/embed.go does
    // `//go:embed dist` relative to server/frontend/, so the compiled SPA MUST land
    // in server/frontend/dist for `task build` to bake the real frontend into the
    // binary. Vite's default (repo-root ./dist) is NOT embedded — building there
    // ships the committed placeholder index.html instead of the real app.
    outDir: 'server/frontend/dist',
    emptyOutDir: true,
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (D3_MODULE_RE.test(id))
            return 'charts'
          if (VUE_MODULE_RE.test(id))
            return 'vendor'
        },
      },
    },
  },
  // Strip console.* and debugger statements from production bundles to avoid
  // leaking diagnostic data (session IDs, payload shapes) into user DevTools.
  // Applied at build time only — `vite dev` keeps console output for debugging.
  esbuild: mode === 'production' ? { drop: ['console', 'debugger'] } : {},
  server: {
    port: VITE_DEV_PORT,
    // Bind IPv4 loopback explicitly. Vite's default host is 'localhost', which
    // Node resolves to ::1 first on a dual-stack machine — so the dev server
    // ends up listening on [::1] only, and desktop/target_dev.go, which polls
    // and redirects the webview to 127.0.0.1:<port>, gets ECONNREFUSED. The
    // proxy targets below already spell 127.0.0.1 out for exactly this reason;
    // the listen address needs the same treatment. Browsers reaching
    // http://localhost:<port> still connect: they fall back to IPv4.
    host: '127.0.0.1',
    // A silently-moved port (Vite's default when the port is taken) would leave
    // desktop/target_dev.go redirecting the webview at a foreign loopback origin.
    strictPort: true,
    // Use 127.0.0.1 explicitly — on dual-stack IPv6 systems 'localhost' may resolve
    // to ::1 first, causing ECONNREFUSED when the server only binds to 127.0.0.1.
    proxy: {
      '/api': {
        target: `http://127.0.0.1:${DASHBOARD_PORT}`,
        changeOrigin: true,
        ws: true,
        // Rewrite Origin to match the backend so RequireSameOriginForMutations passes in dev.
        // changeOrigin rewrites Host but not Origin; without this the CSRF middleware sees
        // Origin: localhost:5173 vs Host: 127.0.0.1:13120 and blocks mutation requests.
        headers: { origin: `http://127.0.0.1:${DASHBOARD_PORT}` },
        configure: (proxy) => {
          proxy.on('error', (err) => {
            if ((err as NodeJS.ErrnoException).code === 'ECONNREFUSED')
              return
            console.error('[vite proxy]', err)
          })
        },
      },
      '/auth': {
        target: `http://127.0.0.1:${DASHBOARD_PORT}`,
        changeOrigin: true,
        headers: { origin: `http://127.0.0.1:${DASHBOARD_PORT}` },
        configure: (proxy) => {
          proxy.on('error', (err) => {
            if ((err as NodeJS.ErrnoException).code === 'ECONNREFUSED')
              return
            console.error('[vite proxy]', err)
          })
        },
      },
    },
  },
}))
