import { defineConfig } from '@playwright/test';

// Runs against scripts/dev-server.sh (portal + Cloudflare mock on a fresh
// temporary database). The front end must be built first: npm run build.
export default defineConfig({
  testDir: './e2e',
  timeout: 60_000,
  expect: { timeout: 10_000 },
  workers: 1,
  fullyParallel: false,
  reporter: [['list'], ['html', { open: 'never' }]],
  globalSetup: './e2e/global-setup.ts',
  use: {
    baseURL: 'http://127.0.0.1:18080',
    locale: 'zh-TW',
    timezoneId: 'Asia/Taipei',
    trace: 'retain-on-failure',
  },
  webServer: {
    command: '../scripts/dev-server.sh',
    url: 'http://127.0.0.1:18080/healthz',
    timeout: 180_000,
    reuseExistingServer: false,
    stdout: 'ignore',
    stderr: 'pipe',
  },
});
