import { expect, test } from './fixtures';
import {
  createReaderTestUser,
  deleteTestUserAsAdmin,
  loginByRequest,
  type TestUser,
} from './helpers';

test.describe('Reader progress lifecycle', () => {
  let readerUser: TestUser | null = null;

  test.beforeEach(async ({ page }) => {
    readerUser = await createReaderTestUser(page, 'reader-progress');
    await loginByRequest(page, readerUser.username, readerUser.password);
  });

  test.afterEach(async ({ page }) => {
    if (readerUser) {
      await deleteTestUserAsAdmin(page, readerUser);
      readerUser = null;
    }
  });

  test('preserves and flushes CBZ progress, then resets only reader state', async ({ page }) => {
    await page.goto('/?q=CBZ%20Reader%20Book');
    const card = page.locator('.book-card', { hasText: 'CBZ Reader Book' });
    await expect(card).toBeVisible();
    const href = await card.locator('.book-title-link').getAttribute('href');
    const workId = href ? new URL(href, page.url()).pathname.split('/').pop() : '';
    if (!workId) throw new Error('missing CBZ work id');

    await page.goto(href || `/book/${encodeURIComponent(workId)}`);
    await expect(page.locator('.detail-title')).toHaveText('CBZ Reader Book');
    const assetId = await page
      .locator('[data-reader-progress-asset]')
      .getAttribute('data-reader-progress-asset');
    if (!assetId) throw new Error('missing readable CBZ asset');

    // Foliate weights fixed-layout section progress by the compressed page
    // size. Keep this before the first-page boundary even when the AVIF fixture
    // is much larger than the tiny PNG pages, so ArrowRight has somewhere to go.
    const savedProgress = 0.05;
    const saveRes = await page.request.put(
      `/api/reader/assets/${encodeURIComponent(assetId)}/state`,
      {
        data: {
          progress: savedProgress,
          locator: { engine: 'foliate', fraction: savedProgress },
        },
      },
    );
    expect(saveRes.ok()).toBe(true);

    await page.goto(`/read/${encodeURIComponent(workId)}`);
    const stage = page.locator('.reader-epub-stage');
    await expect(stage).toBeVisible();
    await expect.poll(async () => stage.getAttribute('data-reader-ready')).toBe('true');
    const sections = await page.locator('foliate-view').evaluate((view) => {
      const book = (view as HTMLElement & { book?: { sections?: Array<{ id?: string }> } }).book;
      return book?.sections?.map((section) => section.id) ?? [];
    });
    expect(sections).toEqual(['1.png', '2.bin', '10.png', '11.avif']);

    // CBZ emits a relocation while the fixed-layout renderer initializes. It
    // must not replace an existing position with the first page.
    await page.waitForTimeout(900);
    const restored = await fetchReaderState(page, assetId);
    expect(restored.progress).toBe(savedProgress);
    expect(restored.locator.fraction).toBe(savedProgress);

    // Move once, then leave before the 700 ms debounce expires. pagehide must
    // flush that pending relocation through a keepalive request.
    await page.keyboard.press('ArrowRight');
    await expect.poll(() => currentReaderFraction(page)).toBeGreaterThan(savedProgress);
    expect((await fetchReaderState(page, assetId)).progress).toBe(savedProgress);
    await page.locator('.reader-close').click();
    await expect(page).toHaveURL(new RegExp(`/book/${workId}$`));
    await expect
      .poll(async () => (await fetchReaderState(page, assetId)).progress)
      .toBeGreaterThan(savedProgress);

    const progressBar = page.locator(`[data-reader-progress-asset="${assetId}"]`);
    await expect(progressBar).toBeVisible();
    const statusBeforeReset = (await fetchReaderState(page, assetId)).reading_status.status;
    await expect(progressBar.locator('[data-reading-status-label]')).toHaveText(
      readingStatusLabel(statusBeforeReset),
    );
    await page.getByRole('button', { name: 'More actions' }).click();
    await page.getByRole('menuitem', { name: 'Reset reading position' }).click();

    const dialog = page.getByRole('dialog', { name: 'Reset reading position?' });
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText('Highlights and notes are kept.');
    await dialog.screenshot({ path: 'screenshots/reader-progress-reset.png' });
    await dialog.getByRole('button', { name: 'Reset', exact: true }).click();

    await expect(page.locator('.toast')).toHaveText('Reading position reset');
    await expect(progressBar).toBeVisible();
    await expect(progressBar.locator('[data-reading-status-label]')).toHaveText(
      readingStatusLabel(statusBeforeReset),
    );
    await expect(progressBar.locator('[data-reader-progress-track]')).toBeHidden();
    await expect.poll(async () => await fetchReaderState(page, assetId)).toMatchObject({
      progress: 0,
      locator: {},
    });
    const reset = await fetchReaderState(page, assetId);
    expect(reset.last_read_at ?? 0).toBe(0);
    expect(reset.updated_at ?? 0).toBe(0);
    expect(reset.reading_status.status).toBe(statusBeforeReset);

    const continueRes = await page.request.get('/api/reader/continue?limit=20');
    expect(continueRes.ok()).toBe(true);
    const continuing = (await continueRes.json()) as Array<{ asset_id: string }>;
    expect(continuing.some((item) => item.asset_id === assetId)).toBe(false);
  });

  test('decodes an AVIF comic page through the local CBZ adapter', async ({ page }) => {
    await page.goto('/?q=CBZ%20Reader%20Book');
    const card = page.locator('.book-card', { hasText: 'CBZ Reader Book' });
    const href = await card.locator('.book-title-link').getAttribute('href');
    if (!href) throw new Error('missing CBZ work link');
    const workId = new URL(href, page.url()).pathname.split('/').pop();
    if (!workId) throw new Error('missing CBZ work id');
    await page.goto(`/read/${encodeURIComponent(workId)}`);
    await expect
      .poll(async () => page.locator('.reader-epub-stage').getAttribute('data-reader-ready'))
      .toBe('true');

    const avifDimensions = await page.locator('foliate-view').evaluate(async (view) => {
      type ComicSection = { id?: string; load: () => Promise<string> };
      const book = (view as HTMLElement & { book?: { sections?: ComicSection[] } }).book;
      const section = book?.sections?.find((candidate) => candidate.id === '11.avif');
      if (!section) return null;
      const pageURL = await section.load();
      const html = await (await fetch(pageURL)).text();
      const imageURL = new DOMParser()
        .parseFromString(html, 'text/html')
        .querySelector('img')
        ?.getAttribute('src');
      if (!imageURL) return null;
      const image = new Image();
      image.src = imageURL;
      await image.decode();
      return [image.naturalWidth, image.naturalHeight];
    });
    expect(avifDimensions).toEqual([2, 2]);
  });

  test('retries a failed save and resends the pending position when visible', async ({
    page,
    browserErrors,
  }) => {
    await page.goto('/?q=CBZ%20Reader%20Book');
    const card = page.locator('.book-card', { hasText: 'CBZ Reader Book' });
    await expect(card).toBeVisible();
    const href = await card.locator('.book-title-link').getAttribute('href');
    const workId = href ? new URL(href, page.url()).pathname.split('/').pop() : '';
    if (!workId) throw new Error('missing CBZ work id');

    await page.goto(href || `/book/${encodeURIComponent(workId)}`);
    const assetId = await page
      .locator('[data-reader-progress-asset]')
      .getAttribute('data-reader-progress-asset');
    if (!assetId) throw new Error('missing readable CBZ asset');
    browserErrors.allow(
      (message) =>
        message.includes('status of 503') &&
        message.includes(`/api/reader/assets/${assetId}/state`),
    );

    await page.goto(`/read/${encodeURIComponent(workId)}`);
    const stage = page.locator('.reader-epub-stage');
    await expect.poll(async () => stage.getAttribute('data-reader-ready')).toBe('true');

    let failedSaves = 0;
    let allowSaves = false;
    await page.route(`**/api/reader/assets/${encodeURIComponent(assetId)}/state`, async (route) => {
      if (route.request().method() !== 'PUT' || allowSaves) {
        await route.continue();
        return;
      }
      failedSaves++;
      await route.fulfill({ status: 503, contentType: 'text/plain', body: 'database busy' });
    });

    const initial = await fetchReaderState(page, assetId);
    await page.keyboard.press('ArrowRight');
    await expect.poll(() => currentReaderFraction(page)).toBeGreaterThan(initial.progress);
    await expect.poll(() => failedSaves).toBe(3);

    const saveStatus = page.locator('[data-reader-save-status]');
    await expect(saveStatus).toBeVisible();
    await expect(saveStatus).toHaveText('Position not saved');
    await page.screenshot({ path: 'screenshots/reader-progress-unsaved.png', fullPage: true });

    const pendingProgress = await currentReaderFraction(page);
    expect(pendingProgress).toBeGreaterThan(initial.progress);
    expect((await fetchReaderState(page, assetId)).progress).toBe(initial.progress);

    // Returning to a visible tab retries the latest in-memory position without
    // creating a durable stale write that could outlive this reader session.
    allowSaves = true;
    await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')));
    await expect
      .poll(async () => (await fetchReaderState(page, assetId)).progress)
      .toBeCloseTo(pendingProgress, 5);
    await expect(saveStatus).toBeHidden();

  });

  test('ignores reader state loaded for an obsolete book detail render', async ({ page }) => {
    await page.goto('/?q=CBZ%20Reader%20Book');
    const card = page.locator('.book-card', { hasText: 'CBZ Reader Book' });
    const href = await card.locator('.book-title-link').getAttribute('href');
    if (!href) throw new Error('missing CBZ work link');
    await page.goto(href);

    const status = page.locator('#btn-reading-status');
    const assetId = await status.getAttribute('data-reader-progress-asset');
    if (!assetId) throw new Error('missing readable CBZ asset');

    let releaseResponse = () => {};
    const responseReleased = new Promise<void>((resolve) => {
      releaseResponse = resolve;
    });
    let staleRequestHeld = false;
    let staleResponseDelivered = () => {};
    const responseDelivered = new Promise<void>((resolve) => {
      staleResponseDelivered = resolve;
    });
    await page.route(`**/api/reader/assets/${encodeURIComponent(assetId)}/state`, async (route) => {
      if (route.request().method() !== 'GET' || staleRequestHeld) {
        await route.continue();
        return;
      }
      staleRequestHeld = true;
      const response = await route.fetch();
      await responseReleased;
      await route.fulfill({ response });
      staleResponseDelivered();
    });

    await page.reload();
    await expect.poll(() => staleRequestHeld).toBe(true);
    await status.click();
    await page.getByRole('menuitem', { name: 'Dropped' }).click();
    await expect(status).toContainText('Dropped');

    releaseResponse();
    await responseDelivered;
    await page.waitForTimeout(50);
    await expect(status).toContainText('Dropped');
    await page.unroute(`**/api/reader/assets/${encodeURIComponent(assetId)}/state`);

    const saved = await fetchReaderState(page, assetId);
    expect(saved.reading_status.status).toBe('dropped');
  });
});

async function fetchReaderState(
  page: import('@playwright/test').Page,
  assetId: string,
): Promise<{
  progress: number;
  locator: { fraction?: number };
  last_read_at?: number;
  updated_at?: number;
  reading_status: { status: string };
}> {
  const res = await page.request.get(
    `/api/reader/assets/${encodeURIComponent(assetId)}/state`,
  );
  if (!res.ok()) throw new Error(`reader state status ${res.status()}: ${await res.text()}`);
  return await res.json();
}

async function currentReaderFraction(page: import('@playwright/test').Page): Promise<number> {
  return page.evaluate(() => {
    const view = document.querySelector('foliate-view') as HTMLElement & {
      lastLocation?: { fraction?: number };
    };
    return view.lastLocation?.fraction ?? 0;
  });
}

function readingStatusLabel(status: string): string {
  return status.charAt(0).toUpperCase() + status.slice(1);
}
