import { Buffer } from 'node:buffer';
import { queryTerm } from '../../frontend/src/search-query';
import { expect, type Page, test } from './fixtures';
import { importTestBook } from './helpers';

function fb2(title: string) {
  return {
    name: `${title}.fb2`,
    mimeType: 'application/xml',
    buffer: Buffer.from(`<FictionBook><body><p>${title}</p></body></FictionBook>`),
  };
}

async function prepareBooks(page: Page, titles: string[]): Promise<number[]> {
  const ids: number[] = [];
  for (const title of titles) ids.push(await importTestBook(page, fb2(title)));
  await page.goto('/');
  for (const title of titles) {
    await expect(page.locator('.book-card', { hasText: title })).toBeVisible();
  }
  return ids;
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

    await prepareBooks(page, [titleA, titleB]);
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

    await prepareBooks(page, [titleA, titleB]);
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

    await prepareBooks(page, [titleA, titleB]);
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

    await prepareBooks(page, [titleA, titleB]);

    const createShelf = await page.request.post('/api/shelves', {
      data: { name: shelfName, kind: 'manual', query: '', shared: false },
    });
    expect(createShelf.ok()).toBe(true);
    const shelfId = ((await createShelf.json()) as { id: number }).id;

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

    const inShelf = await page.request.get(`/api/books?shelf=${shelfId}`);
    expect(inShelf.ok()).toBe(true);
    expect((await inShelf.json()).length).toBe(2);

    const deleteShelf = await page.request.delete(`/api/shelves/${shelfId}`);
    expect(deleteShelf.status()).toBe(204);
  });

  test('Bulk delete moves selected books to Trash', async ({ page }) => {
    const stamp = Date.now().toString(36);
    const titleA = `Bulk Del A ${stamp}`;
    const titleB = `Bulk Del B ${stamp}`;

    const ids = await prepareBooks(page, [titleA, titleB]);
    await selectCards(page, [titleA, titleB]);

    await page.locator('.bulk-bar-action[data-action="delete"]').click();
    await page.locator('.modal-confirm').getByRole('button', { name: 'Remove' }).click();

    await expect(page.locator('.toast', { hasText: 'Moved 2 books to Trash' })).toBeVisible();
    await expect(page.locator('.book-card', { hasText: titleA })).toHaveCount(0);
    await expect(page.locator('.book-card', { hasText: titleB })).toHaveCount(0);
    await expect(page.locator('.bulk-bar')).toHaveCount(0);

    await page.goto('/trash');
    for (const title of [titleA, titleB]) {
      const trashCard = page.locator('.trash-card', { hasText: title });
      await expect(trashCard).toBeVisible();
    }
    for (const id of ids) {
      const purged = await page.request.delete(`/api/books/${id}/purge`);
      expect(purged.status()).toBe(204);
    }
  });

  test('Narrow-screen selection keeps actions accessible and clears before opening a book', async ({ page }) => {
    await page.setViewportSize({ width: 400, height: 800 });
    await page.goto('/');
    await page.locator('body.can-curate').waitFor({ state: 'attached' });
    const cards = page.locator('.book-card');
    for (const index of [0, 1]) {
      await cards.nth(index).hover();
      await cards.nth(index).locator('.card-select').click();
    }

    const bar = page.locator('.bulk-bar');
    await expect(bar).toBeInViewport({ ratio: 1 });
    await expect(page.locator('.bulk-bar-count')).toHaveText('2 selected');
    await expect(page.locator('.book-card.selected')).toHaveCount(2);
    const tags = bar.locator('.bulk-bar-action[data-action="tags"]');
    await expect(tags.locator('span')).toBeHidden();
    await expect(tags).toHaveAttribute('aria-label', 'Tags');
    await page.screenshot({ path: 'screenshots/bulk-bar-narrow.png' });

    await bar.getByRole('button', { name: 'Clear selection' }).click();
    await expect(bar).toHaveCount(0);
    await expect(page.locator('body')).not.toHaveClass(/has-selection/);
    await expect(page.locator('.book-card.selected')).toHaveCount(0);
    const title = await cards.first().locator('.book-title').innerText();
    await cards.first().locator('.book-title').click();
    await expect(page.locator('.detail-title')).toHaveText(title);
  });
});
