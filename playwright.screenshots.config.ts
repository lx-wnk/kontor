import { defineConfig } from '@playwright/test'

// Own port, away from tests/e2e's 13198/13199 — this suite never talks to the
// Go backend at all (see fixtures.ts): DASHBOARD_PORT is set to an unused port
// so Vite's /api proxy has nothing real to reach, and every /api/** request is
// answered by page.route fixtures instead.
const SCREENSHOTS_PORT = 13197

export default defineConfig({
  testDir: './tests/screenshots',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  webServer: {
    command: 'pnpm vite',
    port: SCREENSHOTS_PORT,
    reuseExistingServer: false,
    timeout: 60_000,
    env: {
      VITE_DEV_PORT: String(SCREENSHOTS_PORT),
      DASHBOARD_PORT: '1',
    },
  },
  use: {
    baseURL: `http://127.0.0.1:${SCREENSHOTS_PORT}`,
    viewport: { width: 1440, height: 900 },
    deviceScaleFactor: 2,
    colorScheme: 'dark',
  },
})
