import { expect, type Page, test } from './fixtures';
import { findBook } from './helpers';

async function enterLibrary(page: Page) {
  await page.goto('/series');
  if (await page.locator('#sidebar-toggle').isVisible())
    await page.locator('#sidebar-toggle').click();
  await page.locator('#nav-library').click();
  await expect(page.locator('.book-card').first()).toBeVisible();
}

test('Author links preserve search context through Back, Forward and Save search', async ({
  page,
}) => {
  await enterLibrary(page);
  await page.getByRole('button', { name: 'Table view', exact: true }).click();
  const rows = page.locator('.table-row');
  const count = await rows.count();
  const selected = rows.filter({ hasText: 'No Cover Book' });
  const author = selected.locator('.table-author-link');
  const name = (await author.innerText()).trim();
  const query = `author:"${name}"`;
  const search = page.locator('#search-input');
  await author.click();
  await expect(search).toHaveValue(query);
  await expect(rows).toHaveCount(1);
  await expect(selected).toBeVisible();

  await page.goBack();
  await expect(search).toHaveValue('');
  await expect(rows).toHaveCount(count);
  await page.goForward();
  await expect(search).toHaveValue(query);
  await expect(rows).toHaveCount(1);

  await page.getByRole('button', { name: 'Save search as shelf' }).click();
  const dialog = page.getByRole('dialog', { name: 'Save search', exact: true });
  await expect(dialog.getByLabel('Name')).toHaveValue(name);
  await expect(dialog.getByLabel('Search query')).toHaveValue(query);
  await dialog.getByRole('button', { name: 'Cancel' }).click();
  await expect(dialog).toHaveCount(0);

  await search.fill('co');
  await expect(selected).toBeVisible();
  await expect(rows.filter({ hasText: 'With Cover Book' })).toBeVisible();
  await author.click();
  await expect(search).toHaveValue(`co ${query}`);
  await expect(rows).toHaveCount(1);
  await expect(selected).toBeVisible();
});

test('Live search groups edits and commits before opening Quick Edit', async ({ page }) => {
  const clockStart = new Date('2026-09-27T12:00:00Z');
  await page.clock.install({ time: clockStart });
  await enterLibrary(page);
  await page.getByRole('button', { name: 'Table view', exact: true }).click();
  const search = page.locator('#search-input');
  await search.fill('Foun');
  await expect(page).toHaveURL(/q=Foun$/);
  await search.fill('Foundation');
  await expect(page.locator('.table-row')).toHaveCount(2);
  await search.press('Enter');
  const selected = page.locator('.table-row').first();
  const selectedTitle = await selected.locator('.table-title-link').innerText();
  // Hold the debounce while a real click blurs the search and opens its result.
  await page.clock.pauseAt(new Date(clockStart.getTime() + 60_000));
  await search.fill('With Cover');
  await selected.locator('.btn-quick-edit').click();
  await expect(page.locator('.edit-modal input[name="title"]')).toHaveValue(selectedTitle);
  await page.clock.resume();
  await page.goBack();
  await expect(page.locator('.edit-modal')).toHaveCount(0);
  await expect(search).toHaveValue('With Cover');
  await expect(page.locator('.table-row')).toHaveCount(1);
  await page.goBack();
  await expect(search).toHaveValue('Foundation');
  await expect(page.locator('.table-row')).toHaveCount(2);
  await page.goBack();
  await expect(search).toHaveValue('');
});

test('Settings addresses restore the selected section and the page underneath', async ({
  page,
}) => {
  await page.goto('/?q=With+Cover');
  if (await page.locator('#sidebar-toggle').isVisible())
    await page.locator('#sidebar-toggle').click();
  await page.locator('.account-settings').click();
  const apps = page.getByRole('tab', { name: 'Reading apps', exact: true });
  await apps.click();
  await expect(page).toHaveURL(/q=With\+Cover&settings=apps$/);
  const address = page.url();
  await page.goBack();
  await expect(page.locator('.settings-modal')).toHaveCount(0);
  await expect(page.locator('.book-card')).toHaveCount(1);
  await page.goForward();
  await expect(apps).toHaveAttribute('aria-selected', 'true');
  await page.reload();
  await expect(apps).toHaveAttribute('aria-selected', 'true');
  await expect(page.locator('#search-input')).toHaveValue('With Cover');

  const direct = await page.context().newPage();
  try {
    await direct.goto(address);
    await expect(direct.getByRole('tab', { name: 'Reading apps' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    await direct
      .getByRole('dialog', { name: 'Settings' })
      .getByRole('button', { name: 'Close', exact: true })
      .click();
    await expect(direct).toHaveURL(/q=With\+Cover$/);
    await expect(direct.locator('.book-card')).toHaveCount(1);
  } finally {
    await direct.close();
  }
});

test('A delayed Settings reopen preserves a newer editor and its draft', async ({ page }) => {
  await page.goto('/?q=With+Cover');
  await page.getByRole('button', { name: 'Table view', exact: true }).click();
  if (await page.locator('#sidebar-toggle').isVisible())
    await page.locator('#sidebar-toggle').click();
  await page.locator('.account-settings').click();
  await expect(page.locator('.settings-modal')).toBeVisible();
  await page.goBack();
  await expect(page.locator('.settings-modal')).toHaveCount(0);

  let release!: () => void;
  const blocked = new Promise<void>((resolve) => {
    release = resolve;
  });
  const requested = page.waitForRequest('**/api/me');
  await page.route('**/api/me', async (route) => {
    await blocked;
    await route.continue();
  });
  try {
    await page.goForward();
    await requested;
    await page.locator('.btn-quick-edit').click();
    const title = page.locator('.edit-modal input[name="title"]');
    await title.fill('Keep this draft');
    const response = page.waitForResponse('**/api/me');
    release();
    await (await response).finished();
    // Wait until the delayed open has settled, including its loading indicator.
    await expect(page.locator('body')).not.toHaveClass(/busy/);
    await expect(title).toHaveValue('Keep this draft');
    await expect(page.locator('.settings-modal')).toHaveCount(0);
    await page.goBack();
    await page.getByRole('button', { name: 'Keep editing', exact: true }).click();
    await expect(title).toHaveValue('Keep this draft');
  } finally {
    release();
    await page.unrouteAll({ behavior: 'wait' });
  }
});

test('Closing the reader returns to its origin without adding another book visit', async ({
  page,
}) => {
  await page.goto('/?q=With+Cover');
  await page.locator('.book-card').locator('.book-title-link').click();
  await expect(page.locator('.detail-title')).toBeVisible();
  const origin = page.url();
  await page.getByRole('link', { name: 'Read', exact: true }).click();
  await expect(page.locator('.reader-epub-stage')).toHaveAttribute('data-reader-ready', 'true');
  await page.getByRole('link', { name: 'Close reader', exact: true }).click();
  await expect(page).toHaveURL(origin);
  await expect(page.locator('.detail-title')).toBeVisible();
  await page.goBack();
  await expect(page.locator('#search-input')).toHaveValue('With Cover');
  await expect(page.locator('.book-card')).toHaveCount(1);
});

test('A direct reader link closes to its book without leaving a reader loop', async ({ page }) => {
  const book = await findBook(page, 'With Cover Book');
  await page.goto('/series');
  await page.goto(`/read/${book.id}`);
  await expect(page.locator('.reader-epub-stage')).toHaveAttribute('data-reader-ready', 'true');
  await page.getByRole('link', { name: 'Close reader', exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`/book/${book.id}$`));
  await expect(page.locator('.detail-title')).toBeVisible();
  await page.goBack();
  await expect(page.locator('.series-container')).toBeVisible();
});
