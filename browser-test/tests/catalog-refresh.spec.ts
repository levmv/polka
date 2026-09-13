import { Buffer } from 'node:buffer';
import { queryTerm } from '../../frontend/src/search-query';
import { epub } from './book-fixtures';
import { expect, type Page, test } from './fixtures';
import { importTestBook, readerMutationFields } from './helpers';

async function addBook(page: Page, title: string, author = 'Catalog Author'): Promise<number> {
  return importTestBook(page, epub(title, author, title));
}

async function saveAndClose(page: Page): Promise<void> {
  await page.locator('.edit-modal .edit-save-btn').click();
  await expect(page.locator('.edit-modal .save-indicator')).toHaveText('Saved');
  await page.locator('.edit-modal .modal-close').click();
}

test('Series edits reorder the result and keep selection; unrelated tags patch in place', async ({
  page,
}) => {
  const a = await addBook(page, 'Catalog series A');
  const b = await addBook(page, 'Catalog series B');
  const series = `Catalog series ${a}`;
  for (const [id, index] of [
    [a, 1],
    [b, 2],
  ]) {
    expect(
      (
        await page.request.patch(`/api/books/${id}`, { data: { series, series_index: index } })
      ).ok(),
    ).toBe(true);
  }
  await page.addInitScript(() => localStorage.setItem('polka-view-mode', 'table'));
  let reads = 0;
  page.on('request', (request) => {
    if (new URL(request.url()).pathname === '/api/books') reads++;
  });
  await page.goto(`/?q=${encodeURIComponent(queryTerm('series', series))}&sort=series`);
  const rows = page.locator('.table-row');
  await expect(rows).toHaveCount(2);
  const rowA = page.locator(`.table-row[data-id="${a}"]`);
  await rowA.locator('.table-select-row').check();
  await page.locator('.bulk-bar-action[data-action="series"]').click();
  const bulk = page.locator('.bulk-modal');
  await bulk.locator('#bulk-series-input').fill(series);
  await bulk.locator('.bulk-segmented-btn', { hasText: 'Number by order' }).click();
  await bulk.locator('[data-start]').fill('3');
  await bulk.getByRole('button', { name: 'Apply', exact: true }).click();
  await expect(bulk).toHaveCount(0);
  await expect(rows.first()).toHaveAttribute('data-id', String(b));
  await expect(rowA.locator('.table-select-row')).toBeChecked();
  expect(reads).toBe(2);
  await page.getByRole('button', { name: 'Clear selection' }).click();

  await rowA.locator('.btn-quick-edit').click();
  await page.locator('.edit-modal input[name="tags"]').fill('Checked');
  await saveAndClose(page);
  await expect(rowA).toContainText('Checked');
  expect(reads).toBe(2);
});

test('Adding a cover removes the book from a missing-cover search', async ({ page }) => {
  const id = await addBook(page, 'Catalog cover');
  await page.addInitScript(() => localStorage.setItem('polka-view-mode', 'table'));
  const query = `${queryTerm('title', 'Catalog cover')} no:cover`;
  await page.goto(`/?q=${encodeURIComponent(query)}&sort=added`);
  const row = page.locator(`.table-row[data-id="${id}"]`);
  await row.locator('.btn-quick-edit').click();
  await page.locator('.edit-cover-container').click();
  const cover = await page.evaluate(() => {
    const canvas = document.createElement('canvas');
    canvas.width = 80;
    canvas.height = 120;
    const ctx = canvas.getContext('2d')!;
    ctx.fillStyle = '#50788a';
    ctx.fillRect(0, 0, 80, 120);
    return canvas.toDataURL('image/png').split(',')[1];
  });
  await page.locator('input[id^="edit-cover-upload-"]').setInputFiles({
    name: 'cover.png',
    mimeType: 'image/png',
    buffer: Buffer.from(cover, 'base64'),
  });
  await page.locator('.cover-picker-modal').getByLabel('Close').click();
  await saveAndClose(page);
  await expect(row).toHaveCount(0);
  await expect(page.locator('.library-empty-state')).toContainText('No matches');
  expect((await (await page.request.get(`/api/books/${id}`)).json()).has_cover).toBe(true);
});

test('A reading-status save completed after Back refreshes the retained search', async ({
  page,
}) => {
  const id = await addBook(page, 'Catalog reading');
  await page.request.put(`/api/books/${id}/reading-status`, { data: { status: 'reading' } });
  const query = `${queryTerm('title', 'Catalog reading')} status:reading`;
  await page.goto(`/?q=${encodeURIComponent(query)}&sort=added`);
  await page.locator(`.book-card[data-id="${id}"] .book-title-link`).click();
  await expect(page.locator('.detail-title')).toHaveText('Catalog reading');
  let started!: () => void;
  const saving = new Promise<void>((resolve) => {
    started = resolve;
  });
  let release!: () => void;
  const pending = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route(`**/api/books/${id}/reading-status`, async (route) => {
    const response = await route.fetch();
    started();
    await pending;
    await route.fulfill({ response });
  });
  try {
    await page.getByRole('button', { name: 'Change reading status, currently Reading' }).click();
    await page.getByRole('menuitem', { name: 'Finished', exact: true }).click();
    await saving;
    await page.goBack();
    await expect(page.locator(`.book-card[data-id="${id}"]`)).toHaveCount(1);
  } finally {
    release();
  }
  await expect(page.locator('.library-empty-state')).toContainText('No matches');
  await expect(page).toHaveURL(/status%3Areading/);
});

test('Shared author sorting refreshes every affected book even if the follow-up read fails', async ({
  page,
  browserErrors,
}) => {
  browserErrors.allow((message) => /500/.test(message));
  const a = await addBook(page, 'Catalog shared A');
  const b = await addBook(page, 'Catalog shared B');
  const c = await addBook(page, 'Catalog shared C', 'Different Author');
  await page.request.post('/api/authors/sort-name', {
    data: { name: 'Catalog Author', sort_name: 'A Shared' },
  });
  await page.addInitScript(() => localStorage.setItem('polka-view-mode', 'table'));
  await page.goto(`/?q=${encodeURIComponent(queryTerm('title', 'Catalog shared'))}&sort=author`);
  const rows = page.locator('.table-row');
  await expect(rows).toHaveCount(3);
  await expect(rows.first()).toHaveAttribute('data-id', String(a));
  await page.locator(`.table-row[data-id="${a}"] .btn-quick-edit`).click();
  const modal = page.locator('.edit-modal');
  await modal.getByRole('button', { name: 'sorts as “A Shared”' }).click();
  await modal.locator('input[id^="author-sort-input-"]').fill('Z Shared');

  let writes = 0;
  page.on('request', (request) => {
    if (new URL(request.url()).pathname === '/api/authors/sort-name') writes++;
  });
  await page.route(`**/api/books/${a}`, (route) =>
    route.fulfill({ status: 500, body: 'Unavailable' }),
  );
  await modal.locator('.edit-save-btn').click();
  await expect(page.locator('.toast')).toContainText(
    'Author sort saved, but book details could not be refreshed.',
  );
  await expect(modal.locator('.edit-save-btn')).toBeDisabled();
  await modal.locator('.modal-close').click();
  await expect(rows.first()).toHaveAttribute('data-id', String(c));
  expect(
    await rows.evaluateAll((elements) => elements.map((el) => Number(el.getAttribute('data-id')))),
  ).toEqual([c, a, b]);
  expect(writes).toBe(1);
});

test('Bulk removal invalidates an older Continue reading response', async ({ page }) => {
  const removed = await addBook(page, 'Catalog reading removed');
  const kept = await addBook(page, 'Catalog reading kept');
  await page.request.put('/api/settings', { data: { show_continue_reading: true } });
  let release!: () => void;
  const pending = new Promise<void>((resolve) => {
    release = resolve;
  });
  let started!: () => void;
  const loading = new Promise<void>((resolve) => {
    started = resolve;
  });
  let reads = 0;
  try {
    for (const id of [removed, kept]) {
      const book = await (await page.request.get(`/api/books/${id}`)).json();
      expect(
        (
          await page.request.put(`/api/reader/assets/${book.assets[0].id}/state`, {
            data: {
              ...(await readerMutationFields(page, book.assets[0].id)),
              progress: 0.37,
              locator: {},
            },
          })
        ).ok(),
      ).toBe(true);
    }
    await page.route('**/api/reader/continue?*', async (route) => {
      reads++;
      if (reads === 1) {
        const response = await route.fetch();
        started();
        await pending;
        await route.fulfill({ response });
      } else await route.continue();
    });
    await page.goto('/');
    await loading;
    const cancelled = page.waitForEvent('requestfailed', {
      predicate: (request) => new URL(request.url()).pathname === '/api/reader/continue',
    });
    const card = page.locator(`.book-card[data-id="${removed}"]`);
    await card.hover();
    await card.locator('.card-select').click();
    await page.locator('.bulk-bar-action[data-action="delete"]').click();
    await page
      .locator('.modal-confirm')
      .getByRole('button', { name: 'Remove', exact: true })
      .click();
    await cancelled;
    release();
    await expect(
      page.locator('.continue-reading-card', { hasText: 'Catalog reading kept' }),
    ).toBeVisible();
    await expect(
      page.locator('.continue-reading-card', { hasText: 'Catalog reading removed' }),
    ).toHaveCount(0);
    expect(reads).toBe(2);
  } finally {
    release();
  }
});
