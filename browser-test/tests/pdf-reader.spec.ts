import { pdf } from './book-fixtures';
import { expect, test } from './fixtures';
import { importTestBook, readerMutationFields } from './helpers';
import type { Annotation } from '../../frontend/src/types';

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
  const file = pdf(title, 'PDF Fixture Author', `pdf-reader-${stamp}`);
  file.name = file.name.toUpperCase();
  const bookId = await importTestBook(page, file);
  const reader = page.locator('.reader-page');
  const stage = page.locator('.reader-pdf-stage');

  try {
    await test.step('saves and restores the page while zoom stays local', async () => {
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
      await expect
        .poll(async () =>
          page.evaluate(async (id) => {
            const response = await fetch(`/api/reader/assets/${id}/state`);
            if (!response.ok) return 0;
            const state = await response.json();
            return state.locator;
          }, assetId),
        )
        .toEqual({ page: 2 });
      await reader.evaluate((element) => element.classList.remove('reader-chrome-hidden'));
      await page.getByRole('button', { name: 'Zoom in' }).click();
      await expect(reader).toHaveAttribute('data-reader-pdf-zoom', '1.200');
      await expect(page.getByRole('button', { name: 'Fit page' })).toHaveText('120%');
      await expect(page.locator('[data-pdf-page]')).toHaveAttribute('data-pdf-rendered-page', '2');
      await expect(page.locator('[data-pdf-text-layer]')).toContainText('Second PDF page');

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

    await test.step('resumes a remote move backwards while keeping the local zoom', async () => {
      const assetId = Number(await reader.getAttribute('data-reader-asset-id'));
      const response = await page.request.put(`/api/reader/assets/${assetId}/state`, {
        data: { ...await readerMutationFields(page, assetId), progress: 1 / 3, locator: { page: 1 } },
      });
      expect(response.ok()).toBe(true);
      await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')));
      await expect(page.locator('[data-pdf-page]')).toHaveAttribute('data-pdf-rendered-page', '1');
      await expect(reader).toHaveAttribute('data-reader-pdf-zoom', '1.440');
    });

    await test.step('keeps rapid page turns consistent', async () => {
      await page.getByRole('button', { name: 'Fit page' }).click();
      await expect(reader).toHaveAttribute('data-reader-pdf-zoom', '1.000');
      await page.getByRole('button', { name: 'Zoom in' }).click();
      await page.getByRole('button', { name: 'Zoom in' }).click();
      await expect(reader).toHaveAttribute('data-reader-pdf-zoom', '1.440');
      await page.getByRole('button', { name: 'Next page' }).click();
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
      await expect(page.locator('.detail-page-count')).toHaveText('3 pages');
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

test('PDF highlights retain their page and geometry through zoom, navigation and reopening', async ({ page, browserName }) => {
  test.setTimeout(30_000);
  const stamp = `${browserName}-${Date.now().toString(36)}`;
  const bookId = await importTestBook(page, pdf(`PDF Highlights ${stamp}`, 'PDF Author', `pdf-highlights-${stamp}`, [0, 90], 'Another line'));
  try {
    await page.goto(`/read/${bookId}`);
    const root = page.locator('.reader-page');
    await expect(page.locator('.reader-pdf-stage')).toHaveAttribute('data-reader-ready', 'true');
    const assetId = Number(await root.getAttribute('data-reader-asset-id'));
    const highlights = page.locator('.reader-pdf-highlights span');
    const editor = page.getByRole('dialog', { name: 'Highlight note' });
    let firstID = 0;
    // Font metrics may round a line box differently after reload. Check every
    // edge with a two-pixel tolerance, on ordinary and rotated pages alike.
    const alignmentError = () => page.evaluate(() => {
      const text = [...document.querySelectorAll('[data-pdf-text-layer] span')].filter((element) => ['First PDF page', 'Second PDF page', 'Another line'].includes(element.textContent ?? ''));
      const highlights = [...document.querySelectorAll('.reader-pdf-highlights span')];
      if (text.length !== 2 || highlights.length !== 2) return 1000;
      return Math.max(...text.flatMap((element, index) => {
        const range = document.createRange();
        range.selectNodeContents(element);
        const expected = range.getBoundingClientRect();
        const actual = highlights[index].getBoundingClientRect();
        return (['left', 'right', 'top', 'bottom'] as const).map((edge) => Math.abs(actual[edge] - expected[edge]));
      }));
    });
    for (const [index, label] of ['First PDF page', 'Second PDF page'].entries()) {
      if (index > 0) {
        await page.getByRole('button', { name: 'Next page' }).click();
        await expect(page.locator('[data-pdf-page]')).toHaveAttribute('data-pdf-rendered-page', '2');
        await expect(highlights).toHaveCount(0);
      }
      const quote = await page.locator('[data-pdf-text-layer]').evaluate((element, label) => {
        const span = [...element.querySelectorAll('span')].find((span) => span.textContent === label);
        const last = [...element.querySelectorAll('span')].find((span) => span.textContent === 'Another line');
        if (!span?.firstChild || !last?.firstChild) throw new Error('missing PDF text');
        const range = document.createRange();
        range.setStart(span.firstChild, 0);
        range.setEnd(last.firstChild, last.firstChild.textContent!.length);
        const selection = window.getSelection()!;
        selection.removeAllRanges();
        selection.addRange(range);
        document.dispatchEvent(new Event('selectionchange'));
        return selection.toString();
      }, label);
      await page.getByRole('toolbar', { name: 'Selection actions' })
        .getByRole('button', { name: index === 0 ? 'Add note' : 'Highlight', exact: true }).click();
      if (index === 0) {
        await editor.getByRole('textbox', { name: 'Note' }).fill('Keep this passage');
        await editor.getByRole('radio', { name: 'Blue' }).check();
        await editor.getByRole('button', { name: 'Done' }).click();
        await expect(editor).toBeHidden();
      }
      await expect(highlights).toHaveCount(2);
      const rows: Annotation[] = await (await page.request.get(`/api/reader/assets/${assetId}/annotations`)).json();
      const saved = rows.find((row) => row.locator.page === index + 1)!;
      expect(saved.quote).toBe(quote);
      const rects = saved.locator.rects!;
      expect(rects).toHaveLength(2);
      // The PDF prints at x=72, baseline y=700 regardless of page rotation.
      expect(rects[0].x).toBeCloseTo(72, 0);
      expect(rects[0].y).toBeGreaterThan(680);
      expect(rects[0].y).toBeLessThan(705);
      await expect.poll(alignmentError).toBeLessThan(2);
      if (index === 0) {
        firstID = saved.id;
        expect(saved).toMatchObject({ note: 'Keep this passage', color: 'blue' });
        await root.evaluate((element) => element.classList.remove('reader-chrome-hidden'));
        await page.getByRole('button', { name: 'Zoom in' }).click();
        await expect.poll(alignmentError).toBeLessThan(2);
      }
    }

    // The panel must navigate to the annotation's own page and open its note.
    await page.getByRole('button', { name: 'Highlights', exact: true }).click();
    await page.locator(`[data-reader-annotation-id="${firstID}"].reader-annotations-item`).click();
    await expect(page.locator('[data-pdf-page]')).toHaveAttribute('data-pdf-rendered-page', '1');
    await expect(editor.getByRole('textbox', { name: 'Note' })).toHaveValue('Keep this passage');
    await editor.getByRole('button', { name: 'Done' }).click();
    await page.getByRole('button', { name: 'Next page' }).click();
    await expect(page.locator('[data-pdf-page]')).toHaveAttribute('data-pdf-rendered-page', '2');
    await page.locator('.reader-close').click();
    await expect(page).toHaveURL(new RegExp(`/book/${bookId}$`));

    // Reopening a passage overrides the saved page and restores its rectangles.
    await page.goto(`/read/asset/${assetId}#annotation=${firstID}`);
    await expect(page.locator('.reader-pdf-stage')).toHaveAttribute('data-reader-ready', 'true');
    await expect(page.locator('[data-pdf-page-input]')).toHaveValue('1');
    await expect.poll(alignmentError).toBeLessThan(2);
    const bounds = await highlights.first().boundingBox();
    if (!bounds) throw new Error('missing highlight after reopening');
    await page.mouse.click(bounds.x + bounds.width / 2, bounds.y + bounds.height / 2);
    page.once('dialog', (dialog) => dialog.accept());
    await page.getByRole('toolbar', { name: 'Highlight actions' }).getByRole('button', { name: 'Delete highlight' }).click();
    await expect(highlights).toHaveCount(0);
    const remaining = await (await page.request.get(`/api/reader/assets/${assetId}/annotations`)).json();
    expect(remaining).toHaveLength(1);
    expect(remaining[0].locator.page).toBe(2);
  } finally {
    await page.request.delete(`/api/books/${bookId}`);
  }
});
