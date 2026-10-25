import { defineConfig, devices } from '@playwright/test';

export default defineConfig({
  testDir: './tests',
  fullyParallel: false,
  workers: 1, // one fake cart, shared: tests reset it, so they run in turn
  reporter: [['list'], ['html', { outputFolder: 'report', open: 'never' }], ['json', { outputFile: 'report/results.json' }]],
  use: {
    baseURL: 'http://127.0.0.1:18787',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: [
    { command: 'go run ./fakekroger', url: 'http://127.0.0.1:18788/__cart', reuseExistingServer: false, timeout: 120_000 },
    { command: './serve.sh', url: 'http://127.0.0.1:18787/', reuseExistingServer: false, timeout: 120_000 },
  ],
});
