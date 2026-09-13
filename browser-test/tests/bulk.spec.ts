import { queryTerm } from '../../frontend/src/search-query';
import { expect, type Page, test } from './fixtures';

const titles = ['With Cover Book', 'No Cover Book'];

// Select the named cards via their cover checkbox. The checkbox is revealed on
// hover (and stays visible once a selection exists); the floating bar appears
// after the first pick.
async function selectCards(page: Page): Promise<void> {
  await page.goto('/');
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
  test('Bulk tags preserve selection and keep narrow-screen actions usable', async ({ page }) => {
    await page.setViewportSize({ width: 400, height: 800 });
    const tag = 'Reviewed';
    await selectCards(page);
    const bar = page.locator('.bulk-bar');
    await expect(bar).toBeInViewport({ ratio: 1 });
    const tags = bar.locator('.bulk-bar-action[data-action="tags"]');
    await expect(tags.locator('span')).toBeHidden();
    await expect(tags).toHaveAttribute('aria-label', 'Tags');

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
    expect((await matched.json()).map((book: { title: string }) => book.title).sort()).toEqual(
      [...titles].sort(),
    );

    for (const title of titles) {
      const card = page.locator('.book-card', { hasText: title });
      await expect(card).toHaveClass(/selected/);
      await expect(card.locator('.card-select')).toHaveAttribute('aria-checked', 'true');
    }

    await bar.getByRole('button', { name: 'Clear selection' }).click();
    await expect(bar).toHaveCount(0);
    await expect(page.locator('body')).not.toHaveClass(/has-selection/);
    await expect(page.locator('.book-card.selected')).toHaveCount(0);
    await page.locator('.book-card', { hasText: titles[0] }).locator('.book-title-link').click();
    await expect(page.locator('.detail-title')).toHaveText(titles[0]);
  });

  test('Bulk authors set replaces the author on selected books', async ({ page }) => {
    const author = 'Bulk Author';
    await selectCards(page);
    await page.locator('#view-table-btn').click();
    await expect(page.locator('.table-row.selected')).toHaveCount(2);

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
    expect((await matched.json()).map((book: { title: string }) => book.title).sort()).toEqual(
      [...titles].sort(),
    );

    for (const title of titles) {
      const row = page.locator('.table-row', { hasText: title });
      await expect(row).toContainText(author);
      await expect(row.locator('.table-select-row')).toBeChecked();
    }
  });

  test('Bulk series numbers selected books by order', async ({ page }) => {
    const series = 'Bulk Series';
    await selectCards(page);

    const orderedTitles = await page.locator('.book-card.selected .book-title').allTextContents();
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
    expect(
      books.map((book: { title: string; series_index: number }) => ({
        title: book.title,
        series_index: book.series_index,
      })),
    ).toEqual(orderedTitles.map((title, index) => ({ title, series_index: index + 1 })));
  });

  test('Bulk shelves adds selected books to a shelf', async ({ page }) => {
    const shelfName = 'Bulk Shelf';

    const createShelf = await page.request.post('/api/shelves', {
      data: { name: shelfName, kind: 'manual', query: '', shared: false },
    });
    expect(createShelf.ok()).toBe(true);
    const shelfId = ((await createShelf.json()) as { id: number }).id;

    await selectCards(page);
    await page.locator('.bulk-bar-action[data-action="shelves"]').click();
    const dialog = page.locator('.bulk-modal');
    await expect(dialog).toBeVisible();
    await dialog.locator('.bulk-shelf-row', { hasText: shelfName }).locator('input').check();
    await dialog.getByRole('button', { name: 'Apply' }).click();
    await expect(dialog).toHaveCount(0);
    await expect(
      page.locator('.toast', { hasText: `Added 2 books to “${shelfName}”` }),
    ).toBeVisible();

    const inShelf = await page.request.get(`/api/books?shelf=${shelfId}`);
    expect(inShelf.ok()).toBe(true);
    expect((await inShelf.json()).map((book: { title: string }) => book.title).sort()).toEqual(
      [...titles].sort(),
    );
  });

  test('Bulk delete moves selected books to Trash', async ({ page }) => {
    await selectCards(page);

    await page.locator('.bulk-bar-action[data-action="delete"]').click();
    await page.locator('.modal-confirm').getByRole('button', { name: 'Remove' }).click();

    await expect(page.locator('.toast', { hasText: 'Moved 2 books to Trash' })).toBeVisible();
    for (const title of titles) {
      await expect(page.locator('.book-card', { hasText: title })).toHaveCount(0);
    }
    await expect(page.locator('.bulk-bar')).toHaveCount(0);

    await page.goto('/trash');
    for (const title of titles) {
      const trashCard = page.locator('.trash-card', { hasText: title });
      await expect(trashCard).toBeVisible();
    }
  });
});
