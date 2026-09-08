import { pdf } from './book-fixtures';
import { expect, test } from './fixtures';
import { importTestBook } from './helpers';

test('PDF reader behavior', async ({
  page,
  browserName,
  browserErrors,
}) => {
  test.setTimeout(45_000);
  browserErrors.allow(
    (message) =>
      browserName === 'webkit' &&
      message.includes('/api/reader/assets/') &&
      message.includes('/state due to access control checks'),
  );
  const stamp = `${browserName}-${Date.now().toString(36)}`;
  let activityCheckpoints = 0;
  page.on('request', (request) => {
    if (request.method() === 'PUT' && /\/api\/reader\/assets\/[^/]+\/activity$/.test(request.url())) {
      activityCheckpoints += 1;
    }
  });
  const title = `PDF Reader ${stamp}`;
  const bookId = await importTestBook(page, pdf(title, 'PDF Fixture Author', `pdf-reader-${stamp}`));
  const reader = page.locator('.reader-page');
  const stage = page.locator('.reader-pdf-stage');

  try {
    await test.step('opens, saves and restores the page and zoom', async () => {
      await page.goto(`/read/${bookId}`);
      await expect(reader).toHaveAttribute('data-reader-format', 'pdf');
      await expect.poll(async () => stage.getAttribute('data-reader-ready')).toBe('true');
      await expect(page.locator('[data-pdf-canvas]')).toBeVisible();
      await expect(page.locator('[data-pdf-text-layer]')).toContainText('First PDF page');
      await expect(page.locator('[data-pdf-page-input]')).toHaveValue('1');
      await expect(page.locator('[data-pdf-page-total]')).toHaveText('3');

      await page.getByRole('button', { name: 'Next page' }).click();
      await expect(page.locator('[data-pdf-page-input]')).toHaveValue('2');
      await expect(page.locator('[data-pdf-text-layer]')).toContainText('Second PDF page');
      await expect.poll(() => activityCheckpoints).toBeGreaterThan(0);

      const assetId = Number(await reader.getAttribute('data-reader-asset-id'));
      if (!assetId) throw new Error('missing PDF asset id');
      await reader.evaluate((element) => element.classList.remove('reader-chrome-hidden'));
      await page.getByRole('button', { name: 'Zoom in' }).click();
      await expect
        .poll(async () => Number(await reader.getAttribute('data-reader-pdf-zoom')))
        .toBeGreaterThan(1);
      await expect(page.getByRole('button', { name: 'Fit page' })).toHaveText('120%');
      await expect(page.locator('[data-pdf-page]')).toHaveAttribute('data-pdf-rendered-page', '2');
      await expect(page.locator('[data-pdf-text-layer]')).toContainText('Second PDF page');
      await expect
        .poll(async () =>
          page.evaluate(async (id) => {
            const response = await fetch(`/api/reader/assets/${id}/state`);
            if (!response.ok) return 0;
            const state = await response.json();
            return state.locator?.engine === 'pdfjs' ? state.locator : null;
          }, assetId),
        )
        .toMatchObject({ page: 2, zoom: 1.2 });
      await page.screenshot({ path: `screenshots/reader-pdf-${browserName}.png`, fullPage: true });

      await reader.evaluate((element) => element.classList.remove('reader-chrome-hidden'));
      await stage.click();
      await expect(reader).toHaveClass(/reader-chrome-hidden/);
      await stage.click();
      await expect(reader).not.toHaveClass(/reader-chrome-hidden/);

      await page.reload();
      await expect.poll(async () => stage.getAttribute('data-reader-ready')).toBe('true');
      await expect(page.locator('[data-pdf-page-input]')).toHaveValue('2');
      await expect(page.locator('[data-pdf-text-layer]')).toContainText('Second PDF page');
      await expect(reader).toHaveAttribute('data-reader-pdf-zoom', '1.200');
      await expect(page.getByRole('button', { name: 'Fit page' })).toHaveText('120%');
      await page.getByRole('button', { name: 'Zoom in' }).click();
      await expect(reader).toHaveAttribute('data-reader-pdf-zoom', '1.440');
      await expect(page.locator('[data-pdf-page]')).toHaveAttribute('data-pdf-rendered-page', '2');
    });

    await test.step('keeps rapid page turns consistent', async () => {
      await page.getByRole('button', { name: 'Fit page' }).click();
      await expect(reader).toHaveAttribute('data-reader-pdf-zoom', '1.000');
      await page.getByRole('button', { name: 'Zoom in' }).click();
      await page.getByRole('button', { name: 'Zoom in' }).click();
      await expect(reader).toHaveAttribute('data-reader-pdf-zoom', '1.440');
      await page.getByRole('button', { name: 'Previous page' }).click();
      await page.getByRole('button', { name: 'Next page' }).click();
      await page.getByRole('button', { name: 'Previous page' }).click();
      await expect(page.locator('[data-pdf-page]')).toHaveAttribute('data-pdf-rendered-page', '1');
      await expect(page.locator('[data-pdf-text-layer]')).toContainText('First PDF page');
    });

    await test.step('navigates document contents', async () => {
      await reader.evaluate((element) => element.classList.remove('reader-chrome-hidden'));
      const tocToggle = page.getByRole('button', { name: 'Contents' });
      await expect(tocToggle).toBeVisible();
      await tocToggle.click();
      const tocPanel = page.locator('#reader-toc-panel');
      await expect(tocPanel).toBeVisible();
      await expect(tocPanel.locator('.reader-toc-item')).toHaveText(['Opening', 'Final PDF page']);
      await page.screenshot({
        path: `screenshots/reader-pdf-outline-${browserName}.png`,
        fullPage: true,
      });
      await tocPanel.getByRole('button', { name: 'Final PDF page' }).click();
      await expect(tocPanel).toBeHidden();
      await expect(page.locator('[data-pdf-page]')).toHaveAttribute('data-pdf-rendered-page', '3');
      await expect(page.locator('[data-pdf-text-layer]')).toContainText('Third PDF page');
      await page.keyboard.press('Home');
      await expect(page.locator('[data-pdf-page]')).toHaveAttribute('data-pdf-rendered-page', '1');
    });

    await test.step('replaces searches and highlights the selected text', async () => {
      await page.keyboard.press('/');
      const searchPanel = page.locator('#reader-search-panel');
      await expect(searchPanel).toBeVisible();
      const searchInput = searchPanel.getByRole('searchbox', { name: 'Search this book' });
      await searchInput.fill('я');
      await expect(searchPanel.locator('.reader-search-status')).toHaveText(
        'Type 2 characters, or one Han ideograph.',
      );
      await searchInput.fill('First PDF page');
      await searchInput.press('Enter');
      await searchInput.fill('Bottom PDF target');
      await searchInput.press('Enter');
      await expect(searchPanel.locator('.reader-search-group-title')).toHaveText('Page 3');
      const searchResult = searchPanel.locator('.reader-search-result-btn').first();
      await expect(searchResult).toContainText('Bottom PDF target');
      await expect(searchPanel.locator('.reader-search-status')).toHaveText('1 result');
      await page.screenshot({
        path: `screenshots/reader-pdf-search-${browserName}.png`,
        fullPage: true,
      });
      await searchResult.click();
      await expect(page.locator('[data-pdf-page]')).toHaveAttribute('data-pdf-rendered-page', '3');
      await expect(page.locator('[data-pdf-text-layer]')).toContainText('Bottom PDF target');
      const highlight = page.locator('[data-pdf-search-highlights] .reader-pdf-search-highlight');
      await expect(highlight).toBeVisible();
      const scrollOffset = async () => stage.evaluate((element) => element.scrollTop);
      await expect.poll(scrollOffset).toBeGreaterThan(0);
      await stage.evaluate((element) => element.scrollTo({ top: 0, behavior: 'auto' }));
      await expect.poll(scrollOffset).toBe(0);
      await searchResult.click();
      await expect(highlight).toBeVisible();
      await expect.poll(scrollOffset).toBeGreaterThan(0);
      const finishedToast = page.locator('.toast', { hasText: 'Marked as finished' });
      if (await finishedToast.isVisible()) {
        await finishedToast.getByRole('button', { name: 'Dismiss' }).click();
        await expect(finishedToast).toHaveCount(0);
      }
      await page.screenshot({
        path: `screenshots/reader-pdf-search-highlight-${browserName}.png`,
        fullPage: true,
      });
      await page.keyboard.press('Escape');
      await expect(searchPanel).toBeHidden();
      await expect(highlight).toHaveCount(0);
      await expect(page).toHaveURL(new RegExp(`/read/${bookId}$`));
    });

    await test.step('reveals chrome and then closes with Escape', async () => {
      await reader.evaluate((element) => element.classList.add('reader-chrome-hidden'));
      await page.keyboard.press('Escape');
      await expect(reader).not.toHaveClass(/reader-chrome-hidden/);
      await expect(page).toHaveURL(new RegExp(`/read/${bookId}$`));
      await page.keyboard.press('Escape');
      await expect(page).toHaveURL(new RegExp(`/book/${bookId}$`));
    });
  } finally {
    const trash = await page.request.post('/api/books/bulk/trash', {
      data: { ids: [bookId] },
    });
    expect(trash.ok()).toBeTruthy();
    const purge = await page.request.delete(`/api/books/${bookId}/purge`);
    expect(purge.status()).toBe(204);
  }
});
