import { expect, type Page, test } from './fixtures';
import { findBook } from './helpers';

async function showTable(page: Page) {
  await expect(page.locator('#library-grid')).toBeVisible();
  await page.getByRole('button', { name: 'Table view', exact: true }).click();
  await expect(page.locator('.library-table')).toBeVisible();
}

test.describe('Editor overlay history', () => {
  test('A guarded jump across the editor respects the selected history destination', async ({
    page,
  }) => {
    await page.goto('/');
    await page.locator('.book-card').first().locator('.book-title-link').click();
    await page.locator('#btn-edit-book').click();
    const title = page.locator('.edit-modal input[name="title"]');
    await title.fill('Unsaved title');
    await page.evaluate(() => history.go(-2));
    await page.getByRole('button', { name: 'Keep editing', exact: true }).click();
    await expect(title).toHaveValue('Unsaved title');
    await expect(page).toHaveURL(/\/book\//);
    await page.evaluate(() => history.go(-2));
    await page.getByRole('button', { name: 'Discard', exact: true }).click();
    await expect(page.locator('.edit-modal')).toHaveCount(0);
    await expect(page.locator('#library-grid')).toBeVisible();
    await expect(page).toHaveURL(/\/$/);
    await page.goForward();
    await expect(page.locator('.detail-title')).toBeVisible();
  });

  test('Cross-document Back protects a draft with the browser unload prompt', async ({ page }) => {
    const book = await findBook(page, 'Foundation');
    await page.goto('/series');
    await page.goto(`/book/${book.id}`);
    await page.locator('#btn-edit-book').click();
    const title = page.locator('.edit-modal input[name="title"]');
    await title.fill('Unsaved title');
    for (const discard of [false, true]) {
      const prompt = page.waitForEvent('dialog');
      await page.evaluate(() => history.go(-2));
      const dialog = await prompt;
      expect(dialog.type()).toBe('beforeunload');
      if (discard) await dialog.accept();
      else {
        await dialog.dismiss();
        await expect(title).toHaveValue('Unsaved title');
      }
    }
    await expect(page.locator('.series-container')).toBeVisible();
  });

  test('Back cancels a stalled editor load without affecting a later draft', async ({ page }) => {
    await page.goto('/?sort=title');
    await showTable(page);

    const row = page.locator('.table-row').first();
    const bookID = await row.getAttribute('data-id');
    expect(bookID).toBeTruthy();
    await page.evaluate((id) => {
      const nativeFetch = window.fetch.bind(window);
      let blocked = false;
      window.fetch = (input, init) => {
        const value = input instanceof Request ? input.url : input.toString();
        if (blocked || new URL(value, location.href).pathname !== `/api/books/${id}`) {
          return nativeFetch(input, init);
        }
        blocked = true;
        // A stuck networking process can ignore abort and return much later.
        return new Promise<Response>((resolve) => {
          window.addEventListener(
            'release-editor-load',
            async () => {
              resolve(await nativeFetch(input, { ...init, signal: undefined }));
              document.documentElement.dataset.editorLoadReleased = 'true';
            },
            { once: true },
          );
        });
      };
    }, bookID);

    await row.locator('.btn-quick-edit').click();
    await expect(page.locator('.edit-loading')).toBeVisible();
    await page.evaluate(() => window.history.back());
    await expect(page.locator('.edit-modal')).toHaveCount(0);
    await expect(page.locator('.library-table')).toBeVisible();
    await expect(page.locator('body')).not.toHaveClass(/busy/);

    await row.locator('.btn-quick-edit').click();
    const title = page.locator('.edit-modal input[name="title"]');
    await title.fill('New draft after cancelling the load');
    await page.evaluate(() => window.dispatchEvent(new Event('release-editor-load')));
    await expect(page.locator('html')).toHaveAttribute('data-editor-load-released', 'true');
    await expect(title).toHaveValue('New draft after cancelling the load');
    await expect(page.locator('.edit-modal')).toHaveCount(1);

    await page.goBack();
    await page.getByRole('button', { name: 'Keep editing', exact: true }).click();
    await expect(title).toHaveValue('New draft after cancelling the load');
  });

  test('Forward retries a failed editor load and protects the resulting draft', async ({
    page,
    browserErrors,
  }) => {
    await page.goto('/?sort=title');
    await showTable(page);
    const row = page.locator('.table-row').first();
    const bookID = await row.getAttribute('data-id');
    browserErrors.allow(
      (message) => message.includes('503') || message.includes('Failed to load book for editing:'),
    );
    await page.route(
      `**/api/books/${bookID}`,
      (route) => route.fulfill({ status: 503, body: 'Temporarily unavailable' }),
      { times: 1 },
    );

    await row.locator('.btn-quick-edit').click();
    const editor = page.getByRole('dialog', { name: 'Edit book', exact: true });
    await expect(editor.getByRole('alert')).toContainText('Could not load this book.');
    await expect(editor.getByRole('button', { name: 'Save', exact: true })).toBeDisabled();
    await expect(page.locator('body')).not.toHaveClass(/busy/);
    await editor.getByRole('button', { name: 'Close', exact: true }).click();
    await expect(editor).toHaveCount(0);
    await page.goForward();
    const title = editor.locator('input[name="title"]');
    await expect(title).toBeFocused();
    await title.fill('Draft after retry');
    await page.goBack();
    await page.getByRole('button', { name: 'Keep editing', exact: true }).click();
    await expect(title).toHaveValue('Draft after retry');
    await expect(page.locator('.library-table')).toBeVisible();
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
    const editorEntry = await page.evaluate(() => history.state.polkaEntryID);
    await page.evaluate(() => window.history.back());
    await expect(page.getByRole('heading', { name: 'Discard changes?' })).toBeVisible();
    // The prompt appears before the guarded traversal returns to the editor entry.
    await expect.poll(() => page.evaluate(() => history.state?.polkaEntryID)).toBe(editorEntry);

    // The prompt is the top layer, so another Back cancels it without reaching
    // the editor or its origin route.
    await page.evaluate(() => window.history.back());
    await expect(page.getByRole('heading', { name: 'Discard changes?' })).toHaveCount(0);
    await expect(title).toHaveValue(`${savedTitle} unsaved`);
    await expect.poll(() => page.evaluate(() => history.state?.polkaEntryID)).toBe(editorEntry);

    await title.fill(savedTitle);
    await page.keyboard.press('Escape');
    await expect(editor).toHaveCount(0);
    await page.evaluate(() => window.history.forward());
    await expect(page.locator('.edit-modal input[name="title"]')).toHaveValue(savedTitle);
    await expect(page.locator('.edit-modal button[id^="btn-edit-previous-"]')).toBeEnabled();
  });

  test('Back closes editor layers, then Forward and reload restore the selected book', async ({
    page,
  }) => {
    await page.goto('/?sort=title');
    const libraryURL = page.url();
    const firstCard = page.locator('.book-card').first();
    await expect(firstCard).toBeVisible();
    const firstTitle = await firstCard.locator('.book-title').innerText();
    await firstCard.locator('.book-title-link').click();
    await expect(page.locator('.detail-title')).toContainText(firstTitle);

    await page.locator('#btn-edit-book').click();
    const editor = page.locator('.edit-modal');
    const title = editor.locator('input[name="title"]');
    const next = editor.locator('button[id^="btn-edit-next-"]');
    await expect(next).toBeEnabled();
    await next.click();
    await expect(title).not.toHaveValue(firstTitle);
    const secondTitle = await title.inputValue();
    await expect(page.locator('.detail-title')).toContainText(secondTitle);
    const secondURL = page.url();

    await editor.getByRole('button', { name: 'Change cover' }).click();
    await expect(page.locator('.cover-picker-modal')).toBeVisible();
    await page.goBack();
    await expect(page.locator('.cover-picker-modal')).toHaveCount(0);
    await expect(editor).toBeVisible();
    await expect(title).toHaveValue(secondTitle);

    await page.goBack();
    await expect(editor).toHaveCount(0);
    await expect(page).toHaveURL(secondURL);
    await expect(page.locator('.detail-title')).toContainText(secondTitle);
    await page.goBack();
    await expect(page).toHaveURL(libraryURL);
    await expect(page.locator('.book-card').first()).toBeVisible();

    // Forward restores the page first, then its editor with the same sequence.
    await page.goForward();
    await expect(page).toHaveURL(secondURL);
    await expect(page.locator('.detail-title')).toContainText(secondTitle);
    await expect(editor).toHaveCount(0);
    await page.goForward();
    await expect(title).toHaveValue(secondTitle);

    const previous = editor.locator('button[id^="btn-edit-previous-"]');
    await expect(previous).toBeEnabled();
    await previous.click();
    await expect(title).toHaveValue(firstTitle);
    await expect(page.locator('.detail-title')).toContainText(firstTitle);

    await page.reload();
    await expect(title).toHaveValue(firstTitle);
    await expect(page.locator('.detail-title')).toContainText(firstTitle);
  });
});
