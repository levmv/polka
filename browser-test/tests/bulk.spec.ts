import { Buffer } from 'node:buffer';
import { queryTerm } from '../../frontend/src/search-query';
import { expect, type Page, test } from './fixtures';

function fb2(title: string) {
  return {
    name: `${title}.fb2`,
    mimeType: 'application/xml',
    buffer: Buffer.from(`<FictionBook><body><p>${title}</p></body></FictionBook>`),
  };
}

async function uploadBooks(page: Page, titles: string[]): Promise<void> {
  await page.locator('#book-upload-input').setInputFiles(titles.map(fb2));
  for (const title of titles) {
    await expect(page.locator('.book-card', { hasText: title })).toBeVisible();
  }
}

// Select the named cards via their cover checkbox. The checkbox is revealed on
// hover (and stays visible once a selection exists); the floating bar appears
// after the first pick.
async function selectCards(page: Page, titles: string[]): Promise<void> {
  await page.locator('body.can-curate').waitFor({ state: 'attached' });
  for (const title of titles) {
    const card = page.locator('.book-card', { hasText: title });
    await card.hover();
    await card.locator('.card-select').click();
  }
  await expect(page.locator('.bulk-bar')).toBeVisible();
  await expect(page.locator('.bulk-bar-count')).toHaveText(`${titles.length} selected`);
}

test.describe('Bulk actions', () => {
  test('Bulk tags add applies to selected books', async ({ page }) => {
    const stamp = Date.now().toString(36);
    const titleA = `Bulk Tag A ${stamp}`;
    const titleB = `Bulk Tag B ${stamp}`;
    const tag = `bulktag${stamp}`;

    await page.goto('/');
    await uploadBooks(page, [titleA, titleB]);
    await selectCards(page, [titleA, titleB]);

    await page.locator('.bulk-bar-action[data-action="tags"]').click();
    const dialog = page.locator('.bulk-modal');
    await expect(dialog).toBeVisible();
    await dialog.locator('#bulk-tags-input').fill(tag);
    await expect(dialog.locator('.bulk-summary')).toContainText('2 to change');
    await expect(dialog.locator('.bulk-preview-tag', { hasText: tag }).first()).toBeVisible();

    await dialog.getByRole('button', { name: 'Apply' }).click();
    await expect(dialog).toHaveCount(0);
    await expect(page.locator('.toast', { hasText: 'Updated 2 books' })).toBeVisible();

    const matched = await page.request.get(
      `/api/books?q=${encodeURIComponent(queryTerm('tag', tag))}`,
    );
    expect(matched.ok()).toBe(true);
    expect((await matched.json()).length).toBe(2);

    await page.screenshot({ path: 'screenshots/bulk-tags.png', fullPage: true });
  });

  test('Bulk authors set replaces the author on selected books', async ({ page }) => {
    const stamp = Date.now().toString(36);
    const titleA = `Bulk Auth A ${stamp}`;
    const titleB = `Bulk Auth B ${stamp}`;
    const author = `Bulk Author ${stamp}`;

    await page.goto('/');
    await uploadBooks(page, [titleA, titleB]);
    await selectCards(page, [titleA, titleB]);

    await page.locator('.bulk-bar-action[data-action="authors"]').click();
    const dialog = page.locator('.bulk-modal');
    await expect(dialog).toBeVisible();
    await dialog.locator('#bulk-authors-input').fill(author);
    await expect(dialog.locator('.bulk-summary')).toContainText('2 to change');
    await expect(dialog.locator('.bulk-preview-table')).toContainText(author);

    await dialog.getByRole('button', { name: 'Apply' }).click();
    await expect(dialog).toHaveCount(0);
    await expect(page.locator('.toast', { hasText: 'Updated 2 books' })).toBeVisible();

    const matched = await page.request.get(
      `/api/books?q=${encodeURIComponent(queryTerm('author', author))}`,
    );
    expect(matched.ok()).toBe(true);
    expect((await matched.json()).length).toBe(2);

  });

  test('Bulk series numbers selected books by order', async ({ page }) => {
    const stamp = Date.now().toString(36);
    const titleA = `Bulk Ser A ${stamp}`;
    const titleB = `Bulk Ser B ${stamp}`;
    const series = `Bulk Series ${stamp}`;

    await page.goto('/');
    await uploadBooks(page, [titleA, titleB]);
    await selectCards(page, [titleA, titleB]);

    await page.locator('.bulk-bar-action[data-action="series"]').click();
    const dialog = page.locator('.bulk-modal');
    await expect(dialog).toBeVisible();
    await dialog.locator('#bulk-series-input').fill(series);
    await dialog.locator('.bulk-segmented-btn', { hasText: 'Number by order' }).click();
    await expect(dialog.locator('.bulk-preview-table')).toContainText(`${series} #1`);
    await expect(dialog.locator('.bulk-preview-table')).toContainText(`${series} #2`);

    await dialog.getByRole('button', { name: 'Apply' }).click();
    await expect(dialog).toHaveCount(0);
    await expect(page.locator('.toast', { hasText: 'Updated 2 books' })).toBeVisible();

    const matched = await page.request.get(
      `/api/books?q=${encodeURIComponent(queryTerm('series', series))}&sort=series`,
    );
    expect(matched.ok()).toBe(true);
    const books = await matched.json();
    const indexes = books.map((b: { series_index: number | null }) => b.series_index).sort();
    expect(indexes).toEqual([1, 2]);

  });

  test('Bulk shelves adds selected books to a shelf', async ({ page }) => {
    const stamp = Date.now().toString(36);
    const titleA = `Bulk Shelf A ${stamp}`;
    const titleB = `Bulk Shelf B ${stamp}`;
    const shelfName = `Bulk Shelf ${stamp}`;

    await page.goto('/');
    await uploadBooks(page, [titleA, titleB]);

    const createShelf = await page.request.post('/api/shelves', {
      data: { name: shelfName, kind: 'manual', query: '', shared: false },
    });
    expect(createShelf.ok()).toBe(true);
    const shelfId = ((await createShelf.json()) as { id: string }).id;

    await selectCards(page, [titleA, titleB]);
    await page.locator('.bulk-bar-action[data-action="shelves"]').click();
    const dialog = page.locator('.bulk-modal');
    await expect(dialog).toBeVisible();
    await dialog.locator('.bulk-shelf-row', { hasText: shelfName }).locator('input').check();
    await dialog.screenshot({ path: 'screenshots/bulk-shelves.png' });
    await dialog.getByRole('button', { name: 'Apply' }).click();
    await expect(dialog).toHaveCount(0);
    await expect(
      page.locator('.toast', { hasText: `Added 2 books to “${shelfName}”` }),
    ).toBeVisible();

    const inShelf = await page.request.get(`/api/books?shelf=${encodeURIComponent(shelfId)}`);
    expect(inShelf.ok()).toBe(true);
    expect((await inShelf.json()).length).toBe(2);

    const deleteShelf = await page.request.delete(`/api/shelves/${encodeURIComponent(shelfId)}`);
    expect(deleteShelf.status()).toBe(204);
  });

  test('Bulk delete moves selected books to Trash', async ({ page }) => {
    const stamp = Date.now().toString(36);
    const titleA = `Bulk Del A ${stamp}`;
    const titleB = `Bulk Del B ${stamp}`;

    await page.goto('/');
    await uploadBooks(page, [titleA, titleB]);
    await selectCards(page, [titleA, titleB]);

    await page.locator('.bulk-bar-action[data-action="delete"]').click();
    await page.locator('.modal-confirm').getByRole('button', { name: 'Remove' }).click();

    await expect(page.locator('.toast', { hasText: 'Moved 2 books to Trash' })).toBeVisible();
    await expect(page.locator('.book-card', { hasText: titleA })).toHaveCount(0);
    await expect(page.locator('.book-card', { hasText: titleB })).toHaveCount(0);
    await expect(page.locator('.bulk-bar')).toHaveCount(0);

    // Both landed in Trash; purge them so the run stays net-zero.
    await page.goto('/trash');
    for (const title of [titleA, titleB]) {
      const trashCard = page.locator('.trash-card', { hasText: title });
      await expect(trashCard).toBeVisible();
      await trashCard.locator('.btn-purge').click();
      await page.locator('.modal-confirm').getByRole('button', { name: 'Delete permanently' }).click();
      await expect(page.locator('.trash-card', { hasText: title })).toHaveCount(0);
    }

  });

  test('Clearing the selection hides the bar and styling', async ({ page }) => {
    const stamp = Date.now().toString(36);
    const title = `Bulk Exit ${stamp}`;

    await page.goto('/');
    await uploadBooks(page, [title]);
    await selectCards(page, [title]);

    const card = page.locator('.book-card', { hasText: title });
    await expect(card).toHaveClass(/selected/);

    await page.locator('.bulk-bar-exit').click();
    await expect(page.locator('.bulk-bar')).toHaveCount(0);
    await expect(page.locator('body.has-selection')).toHaveCount(0);
    await expect(card).not.toHaveClass(/selected/);

    await card.locator('.book-title').click();
    await expect(page.locator('.detail-title')).toContainText(title);

  });

  test('Bulk bar collapses to icon-only actions on a narrow screen', async ({ page }) => {
    await page.setViewportSize({ width: 400, height: 800 });
    await page.goto('/');
    await page.locator('body.can-curate').waitFor({ state: 'attached' });

    // Select two of the existing fixture books (no mutation, so net-zero).
    const cards = page.locator('.book-card');
    for (const i of [0, 1]) {
      await cards.nth(i).hover();
      await cards.nth(i).locator('.card-select').click();
    }

    const bar = page.locator('.bulk-bar');
    await expect(bar).toBeVisible();
    // Labels collapse to icons, but the accessible name survives on each button.
    const tags = bar.locator('.bulk-bar-action[data-action="tags"]');
    await expect(tags.locator('span')).toBeHidden();
    await expect(tags).toHaveAttribute('aria-label', 'Tags');

    await page.screenshot({ path: 'screenshots/bulk-bar-narrow.png' });
  });

  test('Table view scrolls inside its container, keeping the bulk bar on screen', async ({
    page,
  }) => {
    const vw = 390;
    const vh = 780;
    await page.setViewportSize({ width: vw, height: vh });
    await page.goto('/');
    await page.locator('body.can-curate').waitFor({ state: 'attached' });
    await page.locator('#view-table-btn').click();
    await page.locator('.library-table').waitFor();

    // The wide table scrolls within #library-grid, not the whole page: a
    // page-level horizontal overflow is what pins position:fixed to the
    // off-screen layout viewport on iOS.
    const layout = await page.evaluate(() => {
      const grid = document.getElementById('library-grid') as HTMLElement;
      return {
        pageOverflow: document.documentElement.scrollWidth - document.documentElement.clientWidth,
        gridScrolls: grid.scrollWidth > grid.clientWidth,
      };
    });
    expect(layout.pageOverflow).toBe(0);
    expect(layout.gridScrolls).toBe(true);

    // The floating bar stays fully inside the viewport.
    await page.locator('.table-row').first().locator('.table-select-row').check();
    await expect(page.locator('.bulk-bar')).toBeVisible();
    const box = await page.locator('.bulk-bar').boundingBox();
    expect(box).not.toBeNull();
    if (box) {
      expect(box.x).toBeGreaterThanOrEqual(0);
      expect(box.x + box.width).toBeLessThanOrEqual(vw + 1);
      expect(box.y + box.height).toBeLessThanOrEqual(vh + 1);
    }

  });

  test('Table header checkbox selects every loaded book', async ({ page }) => {
    const stamp = Date.now().toString(36);
    const titles = [`Tbl A ${stamp}`, `Tbl B ${stamp}`];

    await page.goto('/');
    await uploadBooks(page, titles);
    await page.locator('body.can-curate').waitFor({ state: 'attached' });
    await page.locator('#view-table-btn').click();
    await expect(page.locator('.library-table')).toBeVisible();

    const rowCount = await page.locator('.table-row').count();
    await page.locator('.table-select-all').check();
    await expect(page.locator('.bulk-bar-count')).toHaveText(`${rowCount} selected`);

    await page.locator('.table-select-all').uncheck();
    await expect(page.locator('.bulk-bar')).toHaveCount(0);

  });
});
