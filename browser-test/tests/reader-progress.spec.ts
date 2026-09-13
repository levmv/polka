import { expect, test } from './fixtures';
import {
  createReaderTestUser,
  deleteTestUserAsAdmin,
  loginByRequest,
  readerMutationFields,
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
    const bookId = href ? new URL(href, page.url()).pathname.split('/').pop() : '';
    if (!bookId) throw new Error('missing CBZ book id');

    await page.goto(href || `/book/${encodeURIComponent(bookId)}`);
    await expect(page.locator('.detail-title')).toHaveText('CBZ Reader Book');
    const assetId = Number(await page
      .locator('[data-reader-progress-asset]')
      .getAttribute('data-reader-progress-asset'));
    if (!assetId) throw new Error('missing readable CBZ asset');

    // Foliate weights fixed-layout section progress by the compressed page
    // size. Keep this before the first-page boundary even when the AVIF fixture
    // is much larger than the tiny PNG pages, so ArrowRight has somewhere to go.
    const savedProgress = 0.05;
    const saveRes = await page.request.put(
      `/api/reader/assets/${assetId}/state`,
      {
        data: {
          ...await readerMutationFields(page, assetId),
          progress: savedProgress,
          locator: {},
        },
      },
    );
    expect(saveRes.ok()).toBe(true);

    await page.goto(`/read/${encodeURIComponent(bookId)}`);
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
    expect(restored.locator).toEqual({});

    // Move once, then leave before the 700 ms debounce expires. pagehide must
    // flush that pending relocation through a keepalive request.
    await page.keyboard.press('ArrowRight');
    await expect.poll(() => currentReaderFraction(page)).toBeGreaterThan(savedProgress);
    expect((await fetchReaderState(page, assetId)).progress).toBe(savedProgress);
    await page.locator('.reader-close').click();
    await expect(page).toHaveURL(new RegExp(`/book/${bookId}$`));
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
    expect(reset.updated_at ?? 0).toBe(0);
    expect(reset.reading_status.status).toBe(statusBeforeReset);

    const continueRes = await page.request.get('/api/reader/continue?limit=20');
    expect(continueRes.ok()).toBe(true);
    const continuing = (await continueRes.json()) as Array<{ asset_id: number }>;
    expect(continuing.some((item) => item.asset_id === assetId)).toBe(false);
  });

  test('decodes an AVIF comic page through the local CBZ adapter', async ({ page }) => {
    await openProgressReader(page);

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

  // Merge rules and write outcomes are covered in frontend/test/reader-position.test.mjs.
  // Here we exercise browser lifecycle events and actual reader navigation.
  test('quietly retries a failed save and finishes it when the reader closes', async ({ page, browserErrors }) => {
    const assetId = await openProgressReader(page);
    browserErrors.allow((message) => message.includes('503') && message.includes(`/api/reader/assets/${assetId}/state`));
    let failedSaves = 0;
    let allowSaves = false;
    let saving = false;
    let release!: () => void;
    const pending = new Promise<void>((resolve) => { release = resolve; });
    await page.route(`**/api/reader/assets/${assetId}/state`, async (route) => {
      if (route.request().method() !== 'PUT') return route.continue();
      if (!allowSaves) {
        failedSaves++;
        return route.fulfill({ status: 503, contentType: 'text/plain', body: 'database busy' });
      }
      saving = true;
      await pending;
      await route.continue();
    });

    const initial = await fetchReaderState(page, assetId);
    await page.clock.install();
    await page.keyboard.press('ArrowRight');
    await expect.poll(() => currentReaderFraction(page)).toBeGreaterThan(initial.progress);
    await expect.poll(() => failedSaves).toBe(3);
    await expect(page.locator('.toast-error')).toBeHidden();
    const pendingProgress = await currentReaderFraction(page);
    expect((await fetchReaderState(page, assetId)).progress).toBe(initial.progress);

    try {
      // Recovery needs no further page turn or online event. Closing while the
      // retry is in flight must still let its keepalive request reach the server.
      allowSaves = true;
      await page.clock.fastForward(5_000);
      await expect.poll(() => saving).toBe(true);
      await page.locator('.reader-close').click();
      await expect(page).toHaveURL(/\/book\/\d+$/);
    } finally {
      release();
    }
    await expect.poll(async () => (await fetchReaderState(page, assetId)).progress)
      .toBeCloseTo(pendingProgress, 5);
  });

  test('recovers a failed initial load without moving someone who kept reading', async ({ page, browserErrors }) => {
    const assetId = await openProgressReader(page);
    const saved = await saveRemotePosition(page, assetId, 3, 0.99);
    browserErrors.allow((message) => message.includes('Failed to fetch reader state') ||
      (message.includes('503') && message.includes('/state')));
    let allowReads = false;
    await page.route(`**/api/reader/assets/${assetId}/state`, (route) =>
      route.request().method() !== 'GET' || allowReads
        ? route.continue()
        : route.fulfill({ status: 503, body: 'database busy' }));
    await page.clock.install();
    await page.setViewportSize({ width: 390, height: 844 });
    await page.reload();
    await expect(page.locator('.reader-epub-stage')).toHaveAttribute('data-reader-ready', 'true');
    const notice = page.locator('.toast-error');
    await expect(notice).toContainText('Could not load the saved position.');

    const before = await currentReaderCFI(page);
    await page.keyboard.press('ArrowRight');
    await expect.poll(() => currentReaderCFI(page)).not.toBe(before);
    const local = await currentReaderCFI(page);
    allowReads = true;
    await page.clock.fastForward(5_000);
    await expect(notice).toBeHidden();
    expect(await currentReaderCFI(page)).toBe(local);
    expect((await fetchReaderState(page, assetId)).revision).toBe(saved.revision);
  });

  test('keeps active reading in place, then resumes remote progress including a move backwards', async ({ page, browserErrors }) => {
    const assetId = await openProgressReader(page);
    browserErrors.allow((message) => message.includes('409') && message.includes('/state'));
    await page.clock.install();
    const forward = await saveRemotePosition(page, assetId, 3, 0.99);
    const reconciled = page.waitForResponse((response) =>
      response.url().endsWith('/state') && response.request().method() === 'GET');
    const before = await currentReaderCFI(page);
    await page.keyboard.press('ArrowRight');
    await expect.poll(() => currentReaderCFI(page)).not.toBe(before);
    const local = await currentReaderCFI(page);
    await reconciled;
    await page.clock.fastForward(1_000);
    expect(await currentReaderCFI(page)).toBe(local);
    expect((await fetchReaderState(page, assetId)).revision).toBe(forward.revision);

    await page.evaluate(() => window.dispatchEvent(new Event('focus')));
    await expect.poll(() => currentReaderCFI(page)).toBe(forward.locator.cfi);
    await page.setViewportSize({ width: 390, height: 844 });
    await page.clock.fastForward(1_000);
    expect((await fetchReaderState(page, assetId)).revision).toBe(forward.revision);

    const backward = await saveRemotePosition(page, assetId, 0, 0.01);
    await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')));
    await expect.poll(() => currentReaderCFI(page)).toBe(backward.locator.cfi);
    await page.clock.fastForward(1_000);
    expect((await fetchReaderState(page, assetId)).revision).toBe(backward.revision);

    await page.keyboard.press('ArrowRight');
    await expect.poll(async () => (await fetchReaderState(page, assetId)).revision).toBe(backward.revision + 1);
    const advanced = await fetchReaderState(page, assetId);
    await page.keyboard.press('ArrowLeft');
    await expect.poll(async () => (await fetchReaderState(page, assetId)).revision).toBe(advanced.revision + 1);
    expect((await fetchReaderState(page, assetId)).progress).toBeLessThan(advanced.progress);
    await expect(page.locator('.toast-error')).toBeHidden();
  });

  test('uses the reported percentage when an external CFI cannot be resolved', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 400 });
    const assetId = await openProgressReader(page, 'With Cover Book');
    let expectedFraction = 0;
    // One unresolvable DOM address is enough to exercise the real engine's
    // fallback. Parsing and interchange variants belong in the non-browser tests.
    for (const references of [[], ['#epubcfi(/6/2!/4/9998/1:100)']]) {
      const response = await page.request.put(`/opds/progression/${assetId}`, {
        data: {
          modified: new Date().toISOString(),
          device: { id: 'urn:reader:test', name: 'External reader' },
          progression: 0.67,
          references,
        },
      });
      expect(response.ok()).toBe(true);
      await page.reload();
      await expect(page.locator('.reader-epub-stage')).toHaveAttribute('data-reader-ready', 'true');
      const fraction = await currentReaderFraction(page);
      if (references.length === 0) {
        expect(fraction).toBeGreaterThan(0);
        expectedFraction = fraction;
      } else {
        expect(fraction).toBeCloseTo(expectedFraction, 5);
      }
      const state = await fetchReaderState(page, assetId);
      expect(state.progress).toBe(0.67);
      expect(state.locator).toEqual(references.length ? { cfi: references[0].slice(1) } : {});
    }
  });

  test('ignores reader state loaded for an obsolete book detail render', async ({ page }) => {
    await page.goto('/?q=CBZ%20Reader%20Book');
    const card = page.locator('.book-card', { hasText: 'CBZ Reader Book' });
    const href = await card.locator('.book-title-link').getAttribute('href');
    if (!href) throw new Error('missing CBZ book link');
    await page.goto(href);

    const status = page.locator('#btn-reading-status');
    const assetId = Number(await status.getAttribute('data-reader-progress-asset'));
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
    await page.route(`**/api/reader/assets/${assetId}/state`, async (route) => {
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
    await page.unroute(`**/api/reader/assets/${assetId}/state`);

    const saved = await fetchReaderState(page, assetId);
    expect(saved.reading_status.status).toBe('dropped');
  });
});

async function fetchReaderState(
  page: import('@playwright/test').Page,
  assetId: number,
): Promise<{
  revision: number;
  progress: number;
  locator: { cfi?: string };
  updated_at?: number;
  reading_status: { status: string };
}> {
  const res = await page.request.get(
    `/api/reader/assets/${assetId}/state`,
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

async function currentReaderCFI(page: import('@playwright/test').Page): Promise<string | undefined> {
  return page.locator('foliate-view').evaluate((view) =>
    (view as HTMLElement & { lastLocation?: { cfi?: string } }).lastLocation?.cfi);
}

async function saveRemotePosition(page: import('@playwright/test').Page, assetId: number, section: number, progress: number) {
  const cfi = await page.locator('foliate-view').evaluate((view, index) =>
    (view as HTMLElement & { getCFI: (index: number) => string }).getCFI(index), section);
  const response = await page.request.put(`/api/reader/assets/${assetId}/state`, {
    data: {
      ...await readerMutationFields(page, assetId), progress,
      locator: { cfi },
    },
  });
  expect(response.ok()).toBe(true);
  return await fetchReaderState(page, assetId);
}

function readingStatusLabel(status: string): string {
  return status.charAt(0).toUpperCase() + status.slice(1);
}
async function openProgressReader(page: import('@playwright/test').Page, title = 'CBZ Reader Book'): Promise<number> {
  await page.goto(`/?q=${encodeURIComponent(title)}`);
  const href = await page.locator('.book-card', { hasText: title }).locator('.book-title-link').getAttribute('href');
  if (!href) throw new Error(`missing book: ${title}`);
  const bookId = new URL(href, page.url()).pathname.split('/').pop();
  await page.goto(`/read/${bookId}`);
  await expect(page.locator('.reader-epub-stage')).toHaveAttribute('data-reader-ready', 'true');
  return Number(await page.locator('.reader-page').getAttribute('data-reader-asset-id'));
}
