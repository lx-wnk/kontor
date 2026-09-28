import { defineConfig } from '@playwright/test'
import { APP_BASE_URL, APP_PORT, APP_RATE_LIMIT_RPS, LIMITER_PORT } from './tests/e2e/servers'

export default defineConfig({
  testDir: './tests/e2e',
  globalSetup: './tests/e2e/global-setup.ts',
  // All specs share one stateful backend on :13199; run serially so concurrent
  // specs don't race on shared server/render state (flaky otherwise).
  workers: 1,
  fullyParallel: false,
  // The stubbed specs are deterministic; the real-backend integration specs
  // (create/delete projects against the shared server) can flake transiently,
  // so allow one retry to keep the gate stable without masking hard failures.
  retries: 1,
  // Build-if-stale, then serve the real Go binary (embedded SPA + live /api)
  // on the E2E port — not `pnpm dev` (vite on 5173, which never satisfies the
  // wait). A cold build is allowed for by the timeout; warm runs skip it.
  //
  // The port is the dashboard's own +79 and reuse is off on purpose: on 13120
  // a running desktop app answers the readiness probe, and the suite would
  // then test that app's embedded SPA — an older build — and report its
  // results as this branch's. Refusing to start is the loud version of that.
  //
  // Playwright starts these in order, so the second never races the first's build.
  webServer: [
    {
      command: 'bash scripts/e2e-server.sh',
      port: APP_PORT,
      reuseExistingServer: false,
      timeout: 180_000,
      env: { KONTOR_RATE_LIMIT_RPS: APP_RATE_LIMIT_RPS },
    },
    {
      // Production limiter, its own port and (per-port) database: cold-start-rate-limit.spec.ts
      // measures the real control (see tests/e2e/servers.ts).
      command: 'bash scripts/e2e-server.sh',
      port: LIMITER_PORT,
      reuseExistingServer: false,
      timeout: 180_000,
      env: { E2E_PORT: String(LIMITER_PORT) },
    },
  ],
  use: {
    baseURL: APP_BASE_URL,
  },
})
