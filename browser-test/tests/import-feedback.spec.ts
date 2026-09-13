import { expect, test } from './fixtures';

test('Batch failures stay readable and a second drop reports the active upload', async ({
  page,
  browserErrors,
}) => {
  browserErrors.allow((message) => message.includes('422 (Unprocessable Entity)'));
  await page.clock.install();
  let releaseUpload!: () => void;
  const uploadGate = new Promise<void>((resolve) => {
    releaseUpload = resolve;
  });
  let uploads = 0;
  await page.route('**/api/import', async (route) => {
    uploads++;
    if (uploads === 1) {
      await uploadGate;
      await route.fulfill({ json: { status: 'imported', book: { id: 999, title: 'Good book' } } });
    } else {
      await route.fulfill({ status: 422, body: 'Invalid <package> document' });
    }
  });
  await page.goto('/');
  await expect(page.locator('#book-upload-btn')).toBeEnabled();
  await page.locator('#book-upload-input').setInputFiles([
    { name: 'good.fb2', mimeType: 'application/xml', buffer: Buffer.from('good') },
    { name: 'broken.epub', mimeType: 'application/epub+zip', buffer: Buffer.from('broken') },
  ]);
  await expect(page.locator('#book-upload-btn')).toBeDisabled();
  await expect.poll(() => uploads).toBe(1);
  try {
    await page.evaluate(() => {
      const transfer = new DataTransfer();
      transfer.items.add(new File(['another book'], 'later.fb2'));
      document.dispatchEvent(new DragEvent('drop', { dataTransfer: transfer, bubbles: true }));
    });
    await expect(page.locator('.toast')).toContainText('An upload is in progress');
    expect(uploads).toBe(1);
  } finally {
    releaseUpload();
  }
  const result = page.getByRole('status', { name: 'Import result' });
  await expect(result).toContainText('Imported 1, Failed 1');
  await expect(result).toContainText('broken.epub: Invalid <package> document');
  await expect(result.locator('package')).toHaveCount(0);
  await expect(page.locator('#book-upload-btn')).toBeEnabled();
  expect(uploads).toBe(2);
  await page.clock.fastForward(30000);
  await expect(page.locator('.toast')).toHaveCount(0);
  await expect(result).toBeVisible();
  await page.setViewportSize({ width: 375, height: 812 });
  await expect(result).toBeInViewport({ ratio: 1 });
  await result.getByRole('button', { name: 'Dismiss import result' }).click();
  await expect(result).toHaveCount(0);
});

test('Server-folder import keeps error details and reports a truncated list', async ({ page }) => {
  const storageResponse = await page.request.get('/api/admin/storage');
  expect(storageResponse.ok()).toBe(true);
  const storage = await storageResponse.json();
  const preview = {
    path: '/srv/books',
    files: 11,
    calibre_books: 0,
    would_import: 11,
    duplicates: 0,
    trashed: 0,
    skipped: 0,
    failed: 0,
  };
  await page.route('**/api/admin/storage/import/preview', (route) =>
    route.fulfill({ json: preview }),
  );
  const errors = Array.from(
    { length: 8 },
    (_, i) => `broken-${i + 1}.epub: Invalid package document`,
  );
  await page.route('**/api/admin/storage/import', (route) =>
    route.fulfill({
      json: {
        path: '/srv/books',
        files: 11,
        calibre_books: 0,
        imported: 1,
        duplicates: 0,
        trashed: 0,
        restored: 0,
        skipped: 0,
        failed: 10,
        warnings: 0,
        errors,
        storage,
      },
    }),
  );
  await page.goto('/');
  await page.locator('.account-settings').click();
  const modal = page.locator('.settings-modal');
  await modal.getByRole('tab', { name: 'Storage' }).click();
  await modal.getByRole('button', { name: 'Add from folder…' }).click();
  await modal.getByPlaceholder('/srv/books').fill('/srv/books');
  await modal.getByRole('button', { name: 'Preview', exact: true }).click();
  await modal.getByRole('button', { name: 'Import', exact: true }).click();
  const result = modal.getByRole('status', { name: 'Import result' });
  await expect(result).toContainText('1 imported');
  await expect(result).toContainText('10 failed');
  await expect(result.locator('li')).toHaveText(errors);
  await expect(result).toContainText('Showing 8 of 10 errors');
  await expect(modal.getByRole('button', { name: 'Preview', exact: true })).toBeEnabled();
  await modal.getByRole('button', { name: 'Hide', exact: true }).click();
  await modal.getByRole('button', { name: 'Add from folder…' }).click();
  await expect(result).toContainText('broken-8.epub');
  let releasePreview!: () => void;
  const previewGate = new Promise<void>((resolve) => {
    releasePreview = resolve;
  });
  await page.route('**/api/admin/storage/import/preview', async (route) => {
    await previewGate;
    await route.fulfill({ json: preview });
  });
  await modal.getByRole('button', { name: 'Preview', exact: true }).click();
  try {
    await result.getByRole('button', { name: 'Dismiss import result' }).click();
  } finally {
    releasePreview();
  }
  await expect(result).toHaveCount(0);
  await expect(modal.getByRole('button', { name: 'Import', exact: true })).toBeEnabled();
  for (const transition of ['hide', 'tab']) {
    const nextPreviewGate = new Promise<void>((resolve) => {
      releasePreview = resolve;
    });
    await page.route('**/api/admin/storage/import/preview', async (route) => {
      await nextPreviewGate;
      await route.fulfill({ json: preview });
    });
    await modal.getByRole('button', { name: 'Preview', exact: true }).click();
    try {
      if (transition === 'hide') {
        await modal.getByRole('button', { name: 'Hide', exact: true }).click();
        await modal.getByRole('button', { name: 'Add from folder…' }).click();
      } else {
        await modal.getByRole('tab', { name: 'General', exact: true }).click();
        await modal.getByRole('tab', { name: 'Storage', exact: true }).click();
      }
    } finally {
      releasePreview();
    }
    await expect(modal.getByRole('button', { name: 'Import', exact: true })).toBeEnabled();
  }
});
