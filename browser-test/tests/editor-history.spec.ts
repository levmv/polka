import { expect, type Page, test } from './fixtures';

async function showTable(page: Page) {
  await expect(page.locator('#library-grid')).toBeVisible();
  await page.locator('#view-table-btn').click();
  await expect(page.locator('.library-table')).toBeVisible();
}

async function currentHistoryEntryKey(page: Page): Promise<string | null> {
  return page.evaluate(() => {
    const navigation = (
      window as typeof window & {
        navigation?: { currentEntry?: { key?: unknown } };
      }
    ).navigation;
    const key = navigation?.currentEntry?.key;
    return typeof key === 'string' ? key : null;
  });
}

test.describe('Editor overlay history', () => {
  test('Back during loading dismisses only the editor layer', async ({ page }) => {
    await page.goto('/?sort=title');
    await showTable(page);

    const root = page.locator('.route-root');
    await root.evaluate((element) => {
      element.setAttribute('data-editor-origin', 'loading');
    });
    const row = page.locator('.table-row').first();
    const bookID = await row.getAttribute('data-id');
    expect(bookID).toBeTruthy();
    let releaseBookRequest!: () => void;
    const bookRequestBlocked = new Promise<void>((resolve) => {
      releaseBookRequest = resolve;
    });
    await page.route(`**/api/books/${bookID}`, async (route) => {
      if (route.request().method() === 'GET') {
        await bookRequestBlocked;
      }
      await route.continue();
    });

    try {
      await row.locator('.btn-quick-edit').click();
      await expect(page.locator('.edit-modal-loading')).toBeVisible();
      await page.evaluate(() => window.history.back());

      await expect(page.locator('.edit-modal')).toHaveCount(0);
      await expect(page.locator('.route-root')).toHaveAttribute('data-editor-origin', 'loading');
      await expect(page.locator('.library-table')).toBeVisible();
    } finally {
      releaseBookRequest();
    }
  });

  test('Back leaves a book editor on the book before returning to the library', async ({ page }) => {
    await page.goto('/?sort=title');
    const firstCard = page.locator('.book-card').first();
    await expect(firstCard).toBeVisible();
    await firstCard.locator('.book-title-link').click();
    await expect(page.locator('.detail-title')).toBeVisible();
    await page.locator('#btn-edit-book').click();
    await expect(page.locator('.edit-modal input[name="title"]')).toBeVisible();

    await page.goBack();
    await expect(page.locator('.edit-modal')).toHaveCount(0);
    await expect(page).toHaveURL(/\/book\//);
    await expect(page.locator('.detail-title')).toBeVisible();

    await page.goBack();
    await expect(page).toHaveURL(/\/?sort=title$/);
    await expect(page.locator('.book-card').first()).toBeVisible();
  });

  test('repeated Back closes the discard prompt and keeps the Save & Next target', async ({
    page,
  }) => {
    await page.goto('/?sort=title');
    await showTable(page);
    await page.locator('.table-row').first().locator('.btn-quick-edit').click();

    const editor = page.locator('.edit-modal');
    const title = editor.locator('input[name="title"]');
    const next = editor.locator('button[id^="btn-edit-next-"]');
    await expect(title).toBeVisible();
    const firstTitle = await title.inputValue();
    await expect(next).toBeEnabled();
    await next.click();
    await expect(title).not.toHaveValue(firstTitle);

    const savedTitle = await title.inputValue();
    await title.fill(`${savedTitle} unsaved`);
    await page.evaluate(() => window.history.back());
    await expect(page.getByRole('heading', { name: 'Discard changes?' })).toBeVisible();

    // The prompt is the top layer, so another Back cancels it without reaching
    // the editor or its origin route.
    await page.evaluate(() => window.history.back());
    await expect(page.getByRole('heading', { name: 'Discard changes?' })).toHaveCount(0);
    await expect(title).toHaveValue(`${savedTitle} unsaved`);

    await title.fill(savedTitle);
    await page.keyboard.press('Escape');
    await expect(editor).toHaveCount(0);
    await page.evaluate(() => window.history.forward());
    await expect(page.locator('.edit-modal input[name="title"]')).toHaveValue(savedTitle);
    await expect(page.locator('.edit-modal button[id^="btn-edit-previous-"]')).toBeEnabled();
  });

  test('Back closes a child dialog, then the editor, then the book page', async ({ page }) => {
    await page.goto('/?sort=title');
    const firstCard = page.locator('.book-card').first();
    await expect(firstCard).toBeVisible();
    await firstCard.locator('.book-title-link').click();
    await expect(page.locator('.detail-title')).toBeVisible();
    await page.locator('#btn-edit-book').click();

    const editor = page.locator('.edit-modal');
    await expect(editor.locator('input[name="title"]')).toBeVisible();
    const editorEntryKey = await currentHistoryEntryKey(page);
    expect(editorEntryKey).not.toBeNull();
    await editor.getByRole('button', { name: 'Change cover' }).click();
    await expect(page.locator('.cover-picker-modal')).toBeVisible();

    await page.goBack();
    await expect(page.locator('.cover-picker-modal')).toHaveCount(0);
    await expect(editor).toBeVisible();
    expect(await currentHistoryEntryKey(page)).toBe(editorEntryKey);

    await page.goBack();
    await expect(editor).toHaveCount(0);
    await expect(page).toHaveURL(/\/book\//);
    await expect(page.locator('.detail-title')).toBeVisible();
    await expect(page).toHaveURL(/\/book\//);

    await page.goBack();
    await expect(page).toHaveURL(/\/?sort=title$/);
    await expect(page.locator('.book-card').first()).toBeVisible();
  });

  test('Forward and reload reconnect a book editor to its detail page', async ({ page }) => {
    await page.goto('/?sort=title');
    const firstCard = page.locator('.book-card').first();
    await expect(firstCard).toBeVisible();
    const firstTitle = await firstCard.locator('.book-title').innerText();
    await firstCard.locator('.book-title-link').click();
    await expect(page.locator('.detail-title')).toContainText(firstTitle);

    await page.locator('#btn-edit-book').click();
    const title = page.locator('.edit-modal input[name="title"]');
    const next = page.locator('.edit-modal button[id^="btn-edit-next-"]');
    await expect(next).toBeEnabled();
    await next.click();
    await expect(title).not.toHaveValue(firstTitle);
    const secondTitle = await title.inputValue();
    await expect(page.locator('.detail-title')).toContainText(secondTitle);

    await page.keyboard.press('Escape');
    await expect(page.locator('.edit-modal')).toHaveCount(0);
    await page.evaluate(() => window.history.forward());
    await expect(page.locator('.edit-modal input[name="title"]')).toHaveValue(secondTitle);

    const previous = page.locator('.edit-modal button[id^="btn-edit-previous-"]');
    await expect(previous).toBeEnabled();
    await previous.click();
    await expect(page.locator('.edit-modal input[name="title"]')).toHaveValue(firstTitle);
    await expect(page.locator('.detail-title')).toContainText(firstTitle);

    await page.reload();
    await expect(page.locator('.edit-modal input[name="title"]')).toHaveValue(firstTitle);
  });

  test('navigation away leaves its URL in place and remounts before reopening', async ({ page }) => {
    await page.goto('/?sort=title');
    const firstCard = page.locator('.book-card').first();
    await expect(firstCard).toBeVisible();
    await firstCard.locator('.book-title-link').click();
    await page.locator('#btn-edit-book').click();
    const editorTitle = await page.locator('.edit-modal input[name="title"]').inputValue();

    // Invoke navigation directly because the backdrop blocks outside pointer input.
    await page.evaluate(() => document.querySelector<HTMLElement>('#nav-series')?.click());
    await expect(page).toHaveURL(/\/series$/);
    await expect(page.locator('.series-container')).toBeVisible();
    await expect(page.locator('.edit-modal')).toHaveCount(0);

    await page.evaluate(() => window.history.back());
    await expect(page).toHaveURL(/\/book\//);
    await expect(page.locator('.detail-title')).toBeVisible();
    await expect(page.locator('.edit-modal input[name="title"]')).toHaveValue(editorTitle);
  });
});
