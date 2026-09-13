import type { Cleanup, DuplicateGroup } from '../../frontend/src/types';
import { epub, fb2, type UploadFile } from './book-fixtures';
import { expect, type Page, test } from './fixtures';
import { importTestBook } from './helpers';

async function importDuplicatePair(page: Page, files: UploadFile[]): Promise<void> {
  for (const file of files) await importTestBook(page, file);
}

async function cleanupGroupForTitle(page: Page, title: string): Promise<DuplicateGroup> {
  const res = await page.request.get('/api/cleanup');
  expect(res.ok()).toBeTruthy();
  const cleanup = (await res.json()) as Cleanup;
  const group = cleanup.possible_duplicates.groups.find((g) =>
    g.books.some((b) => b.title === title),
  );
  expect(group).toBeTruthy();
  return group!;
}

test.describe('Cleanup page', () => {
  test('the library menu opens Cleanup and its tiles filter the library', async ({ page }) => {
    await page.goto('/');
    await page.getByRole('button', { name: 'Manage library' }).click();
    await page.getByRole('menuitem', { name: 'Cleanup', exact: true }).click();
    await expect(page).toHaveURL(/\/cleanup$/);
    await expect(page.locator('#nav-library')).toHaveClass(/active/);

    const tiles = page.locator('.cleanup-tile');
    await expect(tiles).toHaveCount(4);
    for (const label of ['Missing cover', 'Missing author', 'No tags', 'No description']) {
      await expect(tiles.filter({ hasText: label }).locator('.cleanup-tile-count')).toHaveText(
        /^\d[\d,]*$/,
      );
    }

    await tiles.filter({ hasText: 'Missing cover' }).click();
    await expect(page).toHaveURL(
      (url) => url.pathname === '/' && url.searchParams.get('q') === 'no:cover',
    );
    await expect(page.locator('#search-input')).toHaveValue('no:cover');
  });

  test('dismiss hides the selected duplicate group', async ({ page }) => {
    const title = 'Cleanup Dismiss';
    const author = 'Cleanup Author';
    await importDuplicatePair(page, [
      fb2(title, author, 'cleanup-dismiss-a', 'first copy'),
      fb2(title, author, 'cleanup-dismiss-b', 'second copy'),
    ]);

    await page.goto('/cleanup');
    const group = page.locator('.duplicate-group', { hasText: title });
    await expect(group).toBeVisible();
    await group.getByRole('button', { name: 'Dismiss' }).click();
    await expect(page.locator('.toast', { hasText: 'Dismissed duplicate group' })).toBeVisible();
    await expect(page.locator('.duplicate-group', { hasText: title })).toHaveCount(0);
  });

  test('merge combines an EPUB and FB2 pair into one book', async ({ page }) => {
    const title = 'Cleanup Merge';
    const author = 'Merge Author';
    await importDuplicatePair(page, [
      epub(title, author, 'cleanup-merge'),
      fb2(title, author, 'cleanup-merge', 'fb2 copy'),
    ]);

    const apiGroup = await cleanupGroupForTitle(page, title);
    const survivor = apiGroup.books.find((book) =>
      book.assets.some((asset) => asset.extension === '.epub'),
    );
    expect(survivor).toBeTruthy();

    await page.goto('/cleanup');
    const group = page.locator('.duplicate-group', { hasText: title });
    await expect(group).toBeVisible();
    await expect(group.locator('.cleanup-row')).toHaveCount(2);
    await expect(group.locator('.cleanup-format-chip', { hasText: 'EPUB' })).toBeVisible();
    await expect(group.locator('.cleanup-format-chip', { hasText: 'FB2' })).toBeVisible();

    await group.locator(`input[value="${survivor!.id}"]`).check();
    await group.getByRole('button', { name: 'Merge' }).click();
    await expect(page.getByRole('heading', { name: 'Merge duplicates?' })).toBeVisible();
    await page.locator('.modal-confirm').getByRole('button', { name: 'Merge' }).click();
    await expect(page.locator('.toast', { hasText: 'Merged duplicates' })).toBeVisible();
    await expect(page.locator('.duplicate-group', { hasText: title })).toHaveCount(0);

    const bookRes = await page.request.get(`/api/books/${encodeURIComponent(survivor!.id)}`);
    expect(bookRes.ok()).toBeTruthy();
    const survivorBook = await bookRes.json();
    const extensions = survivorBook.assets
      .map((asset: { extension: string }) => asset.extension)
      .sort();
    expect(extensions).toEqual(['.epub', '.fb2']);
  });
});
