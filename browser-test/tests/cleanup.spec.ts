import { epub, fb2, type UploadFile } from './book-fixtures';
import { expect, type Page, test } from './fixtures';

type CleanupResponse = {
  possible_duplicates: {
    groups: {
      books: {
        id: string;
        title: string;
        assets: { extension: string }[];
      }[];
    }[];
  };
};

async function uploadDuplicatePair(page: Page, files: UploadFile[], title: string): Promise<void> {
  await page.goto('/');
  await page.locator('#book-upload-input').setInputFiles(files);
  await expect(page.locator('.book-card', { hasText: title })).toHaveCount(files.length);
}

async function cleanupGroupForTitle(
  page: Page,
  title: string,
): Promise<CleanupResponse['possible_duplicates']['groups'][number]> {
  const res = await page.request.get('/api/cleanup');
  expect(res.ok()).toBeTruthy();
  const cleanup = (await res.json()) as CleanupResponse;
  const group = cleanup.possible_duplicates.groups.find((g) => g.books.some((b) => b.title === title));
  expect(group).toBeTruthy();
  return group!;
}

async function purgeWorks(page: Page, ids: string[]): Promise<void> {
  const trash = await page.request.post('/api/books/bulk/trash', { data: { ids } });
  expect(trash.ok()).toBeTruthy();
  for (const id of ids) {
    const purge = await page.request.delete(`/api/books/${encodeURIComponent(id)}/purge`);
    expect(purge.status()).toBe(204);
  }
}

test.describe('Cleanup page', () => {
  test('metadata tiles link into filtered library searches', async ({ page }) => {
    await page.goto('/cleanup');
    await expect(page.locator('#nav-library')).toHaveClass(/active/);

    const tiles = page.locator('.cleanup-tile');
    await expect(tiles).toHaveCount(4);
    for (const label of ['Missing cover', 'Unknown author', 'No tags', 'No description']) {
      await expect(tiles.filter({ hasText: label }).locator('.cleanup-tile-count')).toHaveText(
        /^\d[\d,]*$/,
      );
    }

    await tiles.filter({ hasText: 'Missing cover' }).click();
    await expect(page).toHaveURL((url) => url.pathname === '/' && url.searchParams.get('q') === 'no:cover');
    await expect(page.locator('#search-input')).toHaveValue('no:cover');

    await page.screenshot({ path: 'screenshots/cleanup-tiles.png', fullPage: true });
  });

  test('library menu keeps Cleanup and Trash available without primary nav items', async ({
    page,
  }) => {
    await page.goto('/');
    await expect(page.locator('#nav-cleanup')).toHaveCount(0);
    await expect(page.locator('#nav-trash')).toHaveCount(0);
    await expect(page.locator('#nav-authors')).toBeVisible();

    const trigger = page.getByRole('button', { name: 'Manage library' });
    await expect(trigger).toBeVisible();
    await page.evaluate(() => {
      (window as typeof window & { __polkaLibraryMenuMarker?: string }).__polkaLibraryMenuMarker =
        'same-doc';
    });

    await trigger.click();
    await expect(page.getByRole('menuitem', { name: 'Cleanup' })).toBeVisible();
    await expect(page.getByRole('menuitem', { name: 'Trash' })).toBeVisible();
    await page.screenshot({ path: 'screenshots/library-actions-menu.png', fullPage: true });
    await page.getByRole('menuitem', { name: 'Cleanup' }).click();
    await expect(page).toHaveURL((url) => url.pathname === '/cleanup');
    await expect(page.locator('.cleanup-container')).toBeVisible();
    await expect(page.locator('#nav-library')).toHaveClass(/active/);
    await expect(trigger).toBeVisible();
    expect(
      await page.evaluate(
        () =>
          (window as typeof window & { __polkaLibraryMenuMarker?: string })
            .__polkaLibraryMenuMarker,
      ),
    ).toBe('same-doc');

    await trigger.click();
    await page.getByRole('menuitem', { name: 'Trash' }).click();
    await expect(page).toHaveURL((url) => url.pathname === '/trash');
    await expect(page.locator('.trash-container')).toBeVisible();
    await expect(page.locator('#nav-library')).toHaveClass(/active/);
    await expect(trigger).toBeVisible();
  });

  test('dismiss hides the selected duplicate group', async ({ page }) => {
    const stamp = Date.now().toString(36);
    const title = `Cleanup Dismiss ${stamp}`;
    const author = `Cleanup Author ${stamp}`;
    await uploadDuplicatePair(
      page,
      [
        fb2(title, author, `cleanup-dismiss-a-${stamp}`, 'first copy'),
        fb2(title, author, `cleanup-dismiss-b-${stamp}`, 'second copy'),
      ],
      title,
    );
    const apiGroup = await cleanupGroupForTitle(page, title);
    const ids = apiGroup.books.map((book) => book.id);

    await page.goto('/cleanup');
    const group = page.locator('.duplicate-group', { hasText: title });
    await expect(group).toBeVisible();
    await group.getByRole('button', { name: 'Dismiss' }).click();
    await expect(page.locator('.toast', { hasText: 'Dismissed duplicate group' })).toBeVisible();
    await expect(page.locator('.duplicate-group', { hasText: title })).toHaveCount(0);
    await purgeWorks(page, ids);

  });

  test('merge combines an EPUB and FB2 pair into one book', async ({ page }) => {
    const stamp = Date.now().toString(36);
    const title = `Cleanup Merge ${stamp}`;
    const author = `Merge Author ${stamp}`;
    await uploadDuplicatePair(
      page,
      [
        epub(title, author, `cleanup-merge-${stamp}`),
        fb2(title, author, `cleanup-merge-${stamp}`, 'fb2 copy'),
      ],
      title,
    );

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
    const extensions = survivorBook.assets.map((asset: { extension: string }) => asset.extension).sort();
    expect(extensions).toEqual(['.epub', '.fb2']);
    await purgeWorks(
      page,
      apiGroup.books.map((book) => book.id),
    );

  });
});
