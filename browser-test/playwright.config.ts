import { defineConfig, devices } from '@playwright/test';
import {
  authSetupPattern,
  laneAAdminStorageState,
  laneBAdminStorageState,
  pagerAdminStorageState,
} from './auth-state';

const browserWorkers = process.env.POLKA_BROWSER_WORKERS
  ? Number.parseInt(process.env.POLKA_BROWSER_WORKERS, 10)
  : 2;
const laneABaseURL = process.env.POLKA_LANE_A_BASE_URL || 'http://127.0.0.1:8099';
const laneBBaseURL = process.env.POLKA_LANE_B_BASE_URL || 'http://127.0.0.1:8097';
const pagerBaseURL = process.env.POLKA_PAGER_BASE_URL || 'http://127.0.0.1:8098';

export default defineConfig({
  testDir: './tests',
  // Each catalog lane has one worker and its own database. The global limit
  // lets the two lanes run concurrently without interleaving mutations inside
  // either catalog.
  fullyParallel: false,
  forbidOnly: true,
  retries: process.env.CI ? 2 : 0,
  workers: Number.isFinite(browserWorkers) && browserWorkers > 0 ? browserWorkers : 1,
  // Successful runs need only the terminal summary. Failure screenshots and
  // retry traces remain in browser-test/test-results without leaving a report
  // in the repository root.
  reporter: 'list',
  outputDir: 'test-results',
  // Fail fast on a hung assertion instead of waiting the 30s default.
  timeout: 15000,
  expect: { timeout: 5000 },
  use: {
    baseURL: laneABaseURL,
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
  },
  projects: [
    {
      name: 'setup-lane-a',
      testMatch: authSetupPattern,
      use: {
        ...devices['Desktop Chrome'],
        baseURL: laneABaseURL,
      },
    },
    {
      name: 'setup-lane-b',
      testMatch: authSetupPattern,
      use: {
        ...devices['Desktop Chrome'],
        baseURL: laneBBaseURL,
      },
    },
    {
      name: 'setup-pager',
      testMatch: authSetupPattern,
      use: {
        ...devices['Desktop Chrome'],
        baseURL: pagerBaseURL,
      },
    },
    {
      // These files intentionally share seeded state, so one worker keeps their
      // catalog and managed files deterministic.
      name: 'lane-a-chromium',
      dependencies: ['setup-lane-a'],
      workers: 1,
      testMatch: /lane-a\/.*\.spec\.ts/,
      use: {
        ...devices['Desktop Chrome'],
        baseURL: laneABaseURL,
        storageState: laneAAdminStorageState,
      },
    },
    {
      // Every other desktop spec defaults to a second serial catalog. This is
      // isolation from lane A, not a claim that every test is net-zero. Keeping
      // an ignore list means a newly added spec is still exercised.
      name: 'lane-b-chromium',
      dependencies: ['setup-lane-b'],
      workers: 1,
      testIgnore: [
        /lane-a\/.*\.spec\.ts/,
        /pdf-reader\.spec\.ts/,
        /responsive\.spec\.ts/,
        /pagination\.spec\.ts/,
        /retained-navigation\.spec\.ts/,
        authSetupPattern,
      ],
      use: {
        ...devices['Desktop Chrome'],
        baseURL: laneBBaseURL,
        storageState: laneBAdminStorageState,
      },
    },
    {
      name: 'pdf-reader-chromium',
      dependencies: ['lane-a-chromium'],
      workers: 1,
      testMatch: /pdf-reader\.spec\.ts/,
      use: {
        ...devices['Desktop Chrome'],
        baseURL: laneABaseURL,
        storageState: laneAAdminStorageState,
      },
    },
    {
      // Keep the same stateful test serial with Chromium, then let the two
      // read-only responsive projects share lane A after both PDF runs clean up.
      name: 'pdf-reader-webkit',
      dependencies: ['pdf-reader-chromium'],
      workers: 1,
      testMatch: /pdf-reader\.spec\.ts/,
      use: {
        ...devices['iPad Mini'],
        browserName: 'webkit',
        baseURL: laneABaseURL,
        storageState: laneAAdminStorageState,
      },
    },
    {
      // Responsive checks reuse lane A only after its mutating smoke pass has
      // completed. Both responsive projects are read-only and may overlap.
      name: 'ipad-chromium',
      dependencies: ['pdf-reader-webkit'],
      testMatch: /responsive\.spec\.ts/,
      use: {
        ...devices['Desktop Chrome'],
        baseURL: laneABaseURL,
        viewport: { width: 768, height: 1024 },
        hasTouch: true,
        storageState: laneAAdminStorageState,
      },
    },
    {
      // Safari-engine pass for the same iPad-sized responsive checks. This uses
      // Playwright's Linux WebKit build, not real iOS Safari, but it catches
      // WebKit layout/runtime differences that Chromium cannot.
      name: 'ipad-webkit',
      dependencies: ['pdf-reader-webkit'],
      testMatch: /responsive\.spec\.ts/,
      use: {
        ...devices['iPad Mini'],
        browserName: 'webkit',
        baseURL: laneABaseURL,
        storageState: laneAAdminStorageState,
      },
    },
    {
      // Pagination and retained navigation need >50 books and a scrollable
      // document; both run against the filler-only library (:8098) so the main
      // suite stays small.
      name: 'pager-chromium',
      dependencies: ['setup-pager'],
      workers: 1,
      testMatch: /(pagination|retained-navigation)\.spec\.ts/,
      use: {
        ...devices['Desktop Chrome'],
        baseURL: pagerBaseURL,
        storageState: pagerAdminStorageState,
      },
    },
  ],
});
