import { defineConfig, devices } from '@playwright/test';

export default defineConfig({
  testDir: './tests',
  fullyParallel: false,
  forbidOnly: true,
  retries: process.env.CI ? 2 : 0,
  workers: 2,
  reporter: 'list',
  outputDir: 'test-results',
  timeout: 15000,
  expect: { timeout: 5000 },
  use: {
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
  },
  projects: [
    {
      name: 'chromium',
      // New specs run on desktop by default; responsive cases use touch below.
      testIgnore: /responsive\.spec\.ts/,
      use: { ...devices['Desktop Chrome'] },
    },
    {
      name: 'ipad-chromium',
      testMatch: /responsive\.spec\.ts/,
      use: {
        ...devices['Desktop Chrome'],
        viewport: { width: 768, height: 1024 },
        hasTouch: true,
      },
    },
    {
      // Linux WebKit catches engine differences; it does not replace real iPad testing.
      name: 'ipad-webkit',
      testMatch: /(responsive|pdf-reader|book-annotations)\.spec\.ts/,
      use: { ...devices['iPad Mini'] },
    },
  ],
});
