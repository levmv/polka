import { expect, test } from './fixtures';
import { epub } from './book-fixtures';
import { importTestBook } from './helpers';

test('shows a calculated page count without replacing a note draft', async ({ page }) => {
  const bookId = await importTestBook(page, epub('Book length example', 'A reader', `page-count-${Date.now()}`, 'A description that remains available while the book length is calculated.'));
  const detail = await (await page.request.get(`/api/books/${bookId}`)).json();
  const asset = detail.assets.find((item: { is_primary: boolean }) => item.is_primary);
  expect(asset.page_count).toBeUndefined();
  const initialWrite = await page.request.post(`/api/books/${bookId}/writeback`);
  expect((await initialWrite.json()).book.writeback).toEqual({ available: true, dirty: false });
  const annotation = await page.request.post(`/api/reader/assets/${asset.id}/annotations`, {
    data: { cfi: 'epubcfi(/6/2!/4/2/1:0)', quote: 'A reader', note: 'An observation', color: 'green' },
  });
  expect(annotation.ok()).toBe(true);

  let release!: () => void;
  const pending = new Promise<void>((resolve) => { release = resolve; });
  await page.route(`**/api/books/${bookId}/page-count`, async (route) => {
    const response = await route.fetch();
    await pending;
    await route.fulfill({ response });
  });
  try {
    await page.goto(`/book/${bookId}`);
    await expect(page.locator('.detail-title')).toHaveText('Book length example');
    await expect(page.locator('.detail-page-count')).toBeHidden();
    await page.locator('.book-annotation-quote').click();
    const note = page.getByRole('textbox', { name: 'Note', exact: true });
    await note.fill('Keep this unsaved thought');
    release();
    await expect(page.locator('.detail-page-count')).toHaveText('≈ 1 page');
    await expect(note).toHaveValue('Keep this unsaved thought');
    await expect(note).toBeFocused();
    await page.screenshot({ path: 'screenshots/book-page-count-desktop.png', fullPage: true, animations: 'disabled' });
    await page.locator('#btn-book-menu').click();
    await expect(page.getByRole('menuitem', { name: 'Metadata file is up to date' })).toBeDisabled();
    await page.keyboard.press('Escape');
    await page.reload();
    await expect(page.locator('.detail-page-count')).toHaveText('≈ 1 page');
    await page.locator('#btn-book-menu').click();
    await expect(page.getByRole('menuitem', { name: 'Metadata file is up to date' })).toBeDisabled();
    await page.keyboard.press('Escape');
  } finally {
    release();
    await page.unroute(`**/api/books/${bookId}/page-count`);
    await page.request.delete(`/api/books/${bookId}`);
  }
});
