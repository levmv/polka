import { expect, type Page, test } from './fixtures';
import { createReaderTestUser, deleteTestUserAsAdmin, loginByRequest, type TestUser } from './helpers';

test.use({ timezoneId: 'Asia/Tokyo' });

async function openTestReader(page: Page): Promise<void> {
  const response = await page.request.get('/api/books?q=CBZ%20Reader%20Book');
  expect(response.ok()).toBe(true);
  const books = await response.json();
  expect(books).toHaveLength(1);
  await page.goto(`/read/${books[0].id}`);
  await expect(page.locator('.reader-epub-stage')).toHaveAttribute('data-reader-ready', 'true');
}

async function setVisibility(page: Page, value: 'hidden' | 'visible'): Promise<void> {
  // Headless browsers do not reliably hide a page when another tab is raised.
  await page.evaluate((state) => {
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: state });
    document.dispatchEvent(new Event('visibilitychange'));
  }, value);
}

test.describe('Reading activity', () => {
  let user: TestUser;

  test.beforeEach(async ({ page }) => {
    user = await createReaderTestUser(page, 'reading-activity');
    await loginByRequest(page, user.username, user.password);
  });

  test.afterEach(async ({ page }) => {
    await page.goto('/');
    await deleteTestUserAsAdmin(page, user);
  });

  test('initializes the reader’s own zone and preserves an explicit choice', async ({ page }) => {
    expect((await (await page.request.get('/api/settings')).json()).time_zone).toBe('');
    await page.goto('/');
    await expect.poll(async () => (await (await page.request.get('/api/settings')).json()).time_zone).toBe('Asia/Tokyo');
    await page.locator('.account-settings').click();
    const modal = page.locator('.settings-modal');
    await modal.getByRole('tab', { name: 'General' }).click();
    const input = modal.getByRole('combobox', { name: 'Time zone', exact: true });
    await expect(input).toHaveValue('Asia/Tokyo');
    await input.fill('UTC');
    await input.press('Tab');
    await expect.poll(async () => (await (await page.request.get('/api/settings')).json()).time_zone).toBe('UTC');
    await page.screenshot({ path: 'screenshots/settings-time-zone.png', fullPage: true });
    await page.setViewportSize({ width: 390, height: 720 });
    await expect(input).toBeVisible();
    await page.screenshot({ path: 'screenshots/settings-time-zone-mobile.png' });

    // A fresh page must keep the explicit UTC choice despite this browser's Tokyo zone.
    await page.setViewportSize({ width: 1280, height: 720 });
    await page.reload();
    await page.locator('.account-settings').click();
    await modal.getByRole('tab', { name: 'General' }).click();
    await expect(input).toHaveValue('UTC');
  });

  test('bounds an abandoned reader and resumes only after a real action', async ({ page }) => {
    type Checkpoint = { session_id: string; elapsed_ms: number; last_activity_ms: number; finished: boolean };
    const starts: string[] = [];
    const checkpoints: Checkpoint[] = [];
    // Browser time is simulated; the Go server clock would not advance with it.
    await page.route('**/api/reader/assets/*/activity', async (route) => {
      const payload = route.request().postDataJSON();
      if (route.request().method() === 'POST') starts.push(payload.session_id);
      else checkpoints.push(payload);
      await route.fulfill({ json: { active: !payload.finished, counted_ms: payload.elapsed_ms || 0 } });
    });
    await page.clock.install();
    await openTestReader(page);
    await expect.poll(() => starts.length).toBe(1);
    await expect.poll(async () => (await (await page.request.get('/api/settings')).json()).time_zone).toBe('Asia/Tokyo');

    for (let i = 0; i < 6; i++) {
      await page.clock.fastForward(60_000);
    }
    await expect.poll(() => checkpoints.some((p) => p.finished)).toBe(true);
    const first = checkpoints.filter((p) => p.session_id === starts[0]);
    expect(first.at(-1)?.elapsed_ms).toBe(300_000);
    expect(first.every((p) => p.last_activity_ms === 0)).toBe(true);

    await page.locator('foliate-view').evaluate((view) => {
      view.dispatchEvent(new CustomEvent('relocate', { detail: { fraction: 0.1 } }));
    });
    await page.clock.fastForward(3_600_000);
    expect(starts.length).toBe(1);
    const resumed = page.waitForResponse((r) => r.url().endsWith('/activity') && r.request().method() === 'POST');
    await page.keyboard.press('ArrowRight');
    await (await resumed).finished();
    await expect.poll(() => starts.length).toBe(2);
    expect(starts[1]).not.toBe(starts[0]);
    await page.clock.runFor(61_000);
    await expect.poll(() => checkpoints.some((p) => p.session_id === starts[1] && !p.finished)).toBe(true);
  });

  test('short hidden pauses reuse the session without counting the gap', async ({ page }) => {
    type Update = { session_id: string; segment: number; elapsed_ms?: number; finished?: boolean };
    const starts: Update[] = [];
    const checkpoints: Update[] = [];
    await page.route('**/api/reader/assets/*/activity', async (route) => {
      const payload = route.request().postDataJSON() as Update;
      (route.request().method() === 'POST' ? starts : checkpoints).push(payload);
      await route.fulfill({ json: { active: !payload.finished, counted_ms: payload.elapsed_ms || 0 } });
    });
    const clockStart = new Date('2026-09-05T12:00:00Z');
    await page.clock.install({ time: clockStart });
    await page.clock.pauseAt(new Date(clockStart.getTime() + 1_000));
    await openTestReader(page);
    await expect.poll(() => starts.length).toBe(1);
    await page.clock.fastForward(60_000);
    await expect.poll(() => checkpoints.length).toBeGreaterThan(0);
    await setVisibility(page, 'hidden');
    await page.clock.fastForward(120_000);
    await setVisibility(page, 'visible');
    await expect.poll(() => starts.length).toBe(2);
    expect(starts[1].session_id).toBe(starts[0].session_id);
    expect(starts[1].segment).toBe(1);
    await page.clock.fastForward(60_000);
    await expect.poll(() => checkpoints.some((p) => p.segment === 1)).toBe(true);
    expect(checkpoints.filter((p) => p.segment === 1).at(-1)?.elapsed_ms).toBe(60_000);

    await setVisibility(page, 'hidden');
    await page.clock.fastForward(360_000);
    await setVisibility(page, 'visible');
    await expect.poll(() => starts.length).toBe(3);
    expect(starts[2].session_id).not.toBe(starts[0].session_id);
    expect(starts[2].segment).toBe(0);
  });

  test('closing a reader persists its final checkpoint on the server', async ({ page }) => {
    let sessionId = '';
    let endpoint = '';
    let updates = 0;
    page.on('request', (request) => {
      if (!/\/api\/reader\/assets\/[^/]+\/activity$/.test(request.url())) return;
      if (request.method() === 'POST') {
        sessionId = request.postDataJSON().session_id;
        endpoint = request.url();
      } else if (request.method() === 'PUT') updates += 1;
    });
    await openTestReader(page);
    await expect.poll(() => sessionId).not.toBe('');
    await page.keyboard.press('ArrowRight');
    await expect.poll(() => updates).toBeGreaterThan(0);
    await page.locator('.reader-close').click();
    await expect(page).not.toHaveURL(/\/read\//);
    await expect.poll(async () => {
      // An idempotent start lets us inspect the final stored result.
      const response = await page.request.post(endpoint, { data: { session_id: sessionId } });
      expect(response.ok()).toBe(true);
      return (await response.json()).active;
    }).toBe(false);
    const result = await (await page.request.post(endpoint, { data: { session_id: sessionId } })).json();
    expect(result.counted_ms).toBeGreaterThan(0);
  });

  test('a quick return cannot overtake the last report before a pause', async ({ page }) => {
    const accepted: string[] = [];
    let release!: () => void;
    const heldReport = new Promise<void>((resolve) => { release = resolve; });
    await page.route('**/api/reader/assets/*/activity', async (route) => {
      const payload = route.request().postDataJSON();
      if (route.request().method() === 'POST') {
        accepted.push(`start:${payload.segment}`);
      } else {
        await heldReport;
        accepted.push(`checkpoint:${payload.segment}`);
      }
      await route.fulfill({ json: { active: !payload.finished, counted_ms: payload.elapsed_ms || 0 } });
    });
    try {
      await openTestReader(page);
      await expect.poll(() => accepted).toEqual(['start:0']);
      const pauseSent = page.waitForRequest((r) => r.url().endsWith('/activity') && r.method() === 'PUT');
      await setVisibility(page, 'hidden');
      await pauseSent;
      const settingsReady = page.waitForResponse('**/api/settings');
      await setVisibility(page, 'visible');
      await settingsReady;
      // Let the return run while only the previous report is still in flight.
      await page.waitForTimeout(150);
      release();
      await expect.poll(() => accepted).toEqual(['start:0', 'checkpoint:0', 'start:1']);
    } finally {
      release();
    }
  });

  test('a displaced reader cannot reclaim time through background checkpoints', async ({ page, browserErrors }) => {
    const starts: string[] = [];
    let endpoint = '';
    page.on('request', (request) => {
      if (request.method() === 'POST' && /\/api\/reader\/assets\/[^/]+\/activity$/.test(request.url())) {
        starts.push(request.postDataJSON().session_id);
        endpoint = request.url();
      }
    });
    await page.clock.install();
    await openTestReader(page);
    await expect.poll(() => starts.length).toBe(1);
    let expectedNetworkErrors = 0;
    // The single aborted checkpoint below deliberately produces this error.
    browserErrors.allow((message) =>
      message === `Failed to load resource: net::ERR_FAILED [${endpoint}]` && expectedNetworkErrors++ === 0,
    );
    let failNextCheckpoint = true;
    await page.route('**/api/reader/assets/*/activity', async (route) => {
      if (route.request().method() === 'PUT' && failNextCheckpoint) {
        failNextCheckpoint = false;
        await route.abort('failed');
      } else await route.continue();
    });
    const navigationSave = page.waitForEvent('requestfailed', (r) => r.url() === endpoint && r.method() === 'PUT');
    await page.keyboard.press('ArrowRight');
    await navigationSave;
    const claim = await page.request.post(endpoint, { data: { session_id: 'ffffffffffffffffffffffffffffffff' } });
    expect((await claim.json()).active).toBe(true);
    const displaced = page.waitForResponse((r) => r.url() === endpoint && r.request().method() === 'PUT');
    await page.clock.fastForward(60_000);
    expect((await (await displaced).json()).active).toBe(false);
    await page.clock.fastForward(600_000);
    expect(starts.length).toBe(1);
    await page.keyboard.press('ArrowRight');
    await expect.poll(() => starts.length).toBe(2);
  });
});
