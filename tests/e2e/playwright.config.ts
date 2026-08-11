import { defineConfig } from '@playwright/test';
import * as path from 'node:path';

// The sandbox is a shared, privileged singleton (one outer container, one inner
// dockerd, one host-published port 3000). Running specs in parallel would race
// on that shared state, so we pin to a single worker and disable parallelism.
// Timeouts are generous because a cold run builds a Go binary, builds the
// sandbox Docker image, boots a privileged container + inner dockerd, then
// builds and runs a Node image *inside* DinD.
export default defineConfig({
  testDir: path.join(__dirname, 'tests'),
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: 0,
  timeout: 300_000,
  expect: { timeout: 15_000 },
  globalSetup: path.join(__dirname, 'global-setup.ts'),
  globalTeardown: path.join(__dirname, 'global-teardown.ts'),
  reporter: [
    ['list'],
    ['html', { outputFolder: path.join(__dirname, 'playwright-report'), open: 'never' }],
  ],
  // Deliberately no `webServer`: the "server" is the corral sandbox brought
  // up by global-setup, not a process Playwright should manage.
});
