import { defineConfig, devices } from '@playwright/test'
import { env } from './e2e/env'

/*
  Browser checks for the project/factory settings surfaces. These drive the
  real SPA served by a real bridge (`scripts/e2e/run_browser_checks.sh` starts
  one), so they observe the shipped bundle rather than a jsdom stand-in.

  The spec files are named `*.pw.ts` on purpose: vitest's default include
  matches `*.spec.ts`, and a browser suite that vitest tries to run would fail
  for having no browser. `npm test` therefore stays jsdom-only, and these run
  with `npm run test:browser`.
*/
export default defineConfig({
  testDir: './e2e',
  testMatch: '**/*.pw.ts',
  // The fixture project's settings are shared state, so the cases run in one
  // worker and in file order.
  fullyParallel: false,
  workers: 1,
  forbidOnly: env('CI', '') !== '',
  retries: 0,
  reporter: [['list']],
  use: {
    baseURL: env('PW_BASE_URL', 'http://127.0.0.1:7799'),
    trace: 'off',
    video: 'off',
  },
  projects: [
    { name: 'desktop', use: { ...devices['Desktop Chrome'], viewport: { width: 1280, height: 900 } } },
    // The design's narrow-width target: a phone viewport, one column.
    { name: 'phone', use: { ...devices['Desktop Chrome'], viewport: { width: 390, height: 844 } } },
  ],
})
