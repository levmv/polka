import { Buffer } from 'node:buffer';
import { readFileSync } from 'node:fs';
import { epub } from '../book-fixtures';
import { expect, type Page, test } from '../fixtures';
import {
  createReaderTestUser,
  deleteTestUserAsAdmin,
  login,
  loginByRequest,
} from '../helpers';

async function createManualShelfFromSidebar(page: Page, name: string): Promise<void> {
  await page.locator('#new-shelf-btn').click();
  const dialog = page.locator('.settings-submodal');
  await expect(dialog.getByRole('heading', { name: 'New shelf' })).toBeVisible();
  await dialog.getByLabel('Name').fill(name);
  await dialog.getByRole('button', { name: 'Create shelf' }).click();
  await expect(dialog).toHaveCount(0);
  await expect(page.locator('#shelf-nav .shelf-nav-item', { hasText: name })).toBeVisible();
}

function testMOBIWithPayload(payload: Buffer): Buffer {
  const record0Offset = 78 + 8;
  const data = Buffer.alloc(record0Offset + 32);
  data.write('BOOKMOBI', 60, 'ascii');
  data.writeUInt16BE(1, 76);
  data.writeUInt32BE(record0Offset, 78);
  data.write('MOBI', record0Offset + 16, 'ascii');
  return Buffer.concat([data, payload]);
}

test.describe('Library workflows', () => {
  test('Account menu logs out with POST', async ({ page }) => {
    await page.context().clearCookies();
    await login(page);

    const logoutMethods: string[] = [];
    page.on('request', (request) => {
      const path = new URL(request.url()).pathname;
      if (path === '/logout') logoutMethods.push(request.method());
    });

    await page.goto('/');
    await expect(page.getByRole('button', { name: 'Account menu for admin' })).toBeVisible();
    await page.getByRole('button', { name: 'Account menu for admin' }).click();
    await Promise.all([
      page.waitForURL((url) => new URL(url).pathname === '/login'),
      page.getByRole('menuitem', { name: 'Log out' }).click(),
    ]);

    expect(logoutMethods).toEqual(['POST']);

    await page.goto('/');
    await expect(page).toHaveURL(/\/login$/);
  });

  test('Continue reading appears only after reader state exists', async ({ page }) => {
    const readerUser = await createReaderTestUser(page, 'continue-reading');
    try {
      await loginByRequest(page, readerUser.username, readerUser.password);
      await page.goto('/');
      await expect(page.locator('#continue-reading')).toBeHidden();

      const target = await page.evaluate(async () => {
        const res = await fetch('/api/books');
        if (!res.ok) throw new Error(`books status ${res.status}`);
        const books = await res.json();
        const book = books.find((b: any) =>
          (b.assets || []).some((a: any) => ['.epub', '.fb2', '.pdf'].includes(a.extension)),
        );
        if (!book) throw new Error('missing readable book');
        const asset = book.assets.find((a: any) =>
          ['.epub', '.fb2', '.pdf'].includes(a.extension),
        );
        const save = await fetch(`/api/reader/assets/${encodeURIComponent(asset.id)}/state`, {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            progress: 0.37,
            locator: { engine: 'browser-test', id: 'continue' },
          }),
        });
        if (!save.ok) throw new Error(`save reader state status ${save.status}`);
        return { title: book.title, assetId: asset.id };
      });

      await page.goto('/');
      const rail = page.locator('#continue-reading');
      await expect(rail).toBeVisible();
      await expect(
        rail.locator('.continue-reading-card', { hasText: target.title }),
      ).toBeVisible();
      await expect(rail).toContainText('37%');
      await page.screenshot({ path: 'screenshots/continue-reading.png', fullPage: true });

      const readPath = `/read/asset/${encodeURIComponent(target.assetId)}`;
      await expect(
        rail.locator('.continue-reading-card', { hasText: target.title }),
      ).toHaveAttribute('href', readPath);
      expect((await page.request.get(readPath)).ok()).toBe(true);

      // The library DOM is replaced on each SPA visit. Return to it in the same
      // document and prove the newly rendered dismiss button owns a listener.
      await page.locator('#nav-series').click();
      await expect(page).toHaveURL(/\/series$/);
      await page.locator('#nav-library').click();
      await expect(page).toHaveURL((url) => url.pathname === '/');
      await expect(page.locator('#continue-reading')).toBeVisible();
      await page.getByRole('button', { name: 'Hide Continue reading' }).click();
      await expect(page.locator('#continue-reading')).toBeHidden();
      await expect
        .poll(async () => {
          const response = await page.request.get('/api/settings');
          return (await response.json()).hide_continue_reading;
        })
        .toBe(true);

      await expect(page).toHaveURL(
        (url) => url.pathname === '/',
      );
    } finally {
      await page.goto('/');
      await deleteTestUserAsAdmin(page, readerUser);
    }
  });

  test('Storage settings shows the books folder health line and scans on demand', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('.account-trigger')).toBeVisible();

    await page.locator('.account-trigger').click();
    await page.getByRole('menuitem', { name: 'Settings' }).click();

    const modal = page.locator('.settings-modal');
    await expect(modal).toBeVisible();

    await modal.getByRole('tab', { name: 'Storage' }).click();
    await expect(modal.getByRole('heading', { name: 'Storage' })).toBeVisible();

    // Books folder health line: the managed root is reachable and holds the
    // imported fixtures, so it reports "Reachable · … · N books · …".
    const health = modal.locator('.settings-health');
    await expect(health).toContainText('Reachable');
    await expect(health).toContainText('book');
    const addFromFolder = modal.getByRole('button', { name: 'Add from folder…' });
    await expect(addFromFolder).toHaveAttribute('aria-expanded', 'false');
    await expect(modal.getByPlaceholder('/srv/books')).toHaveCount(0);
    await page.screenshot({
        path: 'screenshots/settings-storage.png',
        fullPage: true,
        animations: 'disabled',
    });

    await addFromFolder.click();
    await expect(modal.getByPlaceholder('/srv/books')).toBeVisible();
    await expect(modal.getByRole('button', { name: 'Hide' })).toHaveAttribute(
      'aria-expanded',
      'true',
    );
    await page.screenshot({
      path: 'screenshots/settings-storage-import.png',
      fullPage: true,
      animations: 'disabled',
    });
    await modal.getByRole('button', { name: 'Hide' }).click();
    await expect(modal.getByPlaceholder('/srv/books')).toHaveCount(0);

    // Scan now runs an immediate ingest pass. The default incoming folder is
    // empty here, so it reports nothing to import through a toast.
    await modal.getByRole('button', { name: 'Scan now' }).click();
    await expect(page.locator('.toast')).toContainText(/No new files|imported|already in library/);
  });

  test('Metadata write-back: settings mode plus the book-page action', async ({ page }) => {
    // General shows the mode control (default Manual) and a backlog
    // line because write-back is a global library policy, not a path control.
    await page.goto('/');
    await expect(page.locator('.account-trigger')).toBeVisible();
    await page.locator('.account-trigger').click();
    await page.getByRole('menuitem', { name: 'Settings' }).click();
    const settings = page.locator('.settings-modal');
    const writebackRow = settings.locator('.settings-row', { hasText: 'Metadata write-back' });
    await expect(writebackRow).toBeVisible();
    await expect(writebackRow.locator('.settings-writeback-control')).toContainText('Manual');
    await settings.getByRole('tab', { name: 'Storage' }).click();
    const layoutRow = settings.locator('.settings-row', { hasText: 'File layout' });
    await expect(layoutRow).toBeVisible();
    await expect(layoutRow.locator('input')).toHaveValue(
        '{author_bucket}/{author_sort}/{title} [{asset_id}]{dot_ext}',
    );
    await expect(layoutRow.locator('.settings-note')).toContainText('CLI');

    // Use the dedicated write-back fixture so editing and rewriting its file does
    // not disturb the shared books other tests assert on.
    await page.goto('/?q=Writeback%20Fixture');
    const card = page.locator('.book-card', { hasText: 'Writeback Fixture' });
    await expect(card).toBeVisible();
    await card.locator('.book-title').click();
    await expect(page.locator('.detail-title')).toContainText('Writeback Fixture');

    // An edit puts the EPUB behind the catalog; the admin action then writes it
    // and flips to a disabled "up to date".
    await page.locator('#btn-edit-book').click();
    await expect(page.locator('.edit-modal')).toBeVisible();
    const titleInput = page.locator('.edit-modal input[name="title"]');
    const saveBtn = page.locator('.edit-modal .edit-save-btn');
    // A unique title guarantees the save bumps the metadata rev (and so the file
    // goes dirty) even if the test re-runs against an already-edited fixture.
    await titleInput.fill(`Writeback Fixture ${Date.now().toString(36)}`);
    await expect(saveBtn).toBeEnabled();
    // The edit modal saves in place and stays open, so wait on the PATCH before
    // closing it to return to the now-dirty detail.
    const saved = page.waitForResponse(
      (r) => /\/api\/books\/[^/]+$/.test(r.url()) && r.request().method() === 'PATCH',
    );
    await saveBtn.click();
    await saved;
    await page.locator('.edit-modal .modal-close').click();
    await expect(page.locator('.edit-modal')).toBeHidden();

    await page.locator('#btn-book-menu').click();
    const writeItem = page.locator('.menu-item', { hasText: 'Write metadata to file' });
    await expect(writeItem).toBeVisible();
    await writeItem.click();
    await expect(
      page.locator('.toast:not(.toast-leaving)', { hasText: /Metadata written to 1 file/ }),
    ).toBeVisible();

    await page.locator('#btn-book-menu').click();
    const upToDate = page.locator('.menu-item', { hasText: 'Metadata file is up to date' });
    await expect(upToDate).toBeVisible();
    await expect(upToDate).toBeDisabled();
    await page.keyboard.press('Escape');
  });

  test('Late metadata write-back does not replace a newer book detail', async ({ page }) => {
    await page.goto('/?q=Writeback%20Fixture');
    const writebackCard = page.locator('.book-card', { hasText: 'Writeback Fixture' });
    await expect(writebackCard).toBeVisible();
    await writebackCard.locator('.book-title-link').click();
    await expect(page.locator('.detail-title')).toContainText('Writeback Fixture');
    const writebackID = new URL(page.url()).pathname.split('/').pop();
    if (!writebackID) throw new Error('missing write-back fixture id');

    // Make the fixture dirty so the manual write-back action is available even
    // when this test runs independently or is retried against the same library.
    await page.locator('#btn-edit-book').click();
    const edit = page.locator('.edit-modal');
    const titleInput = edit.locator('input[name="title"]');
    await titleInput.fill(`Writeback Fixture Stale ${Date.now().toString(36)}`);
    const saved = page.waitForResponse(
      (response) =>
        response.request().method() === 'PATCH' &&
        new URL(response.url()).pathname === `/api/books/${writebackID}`,
    );
    await edit.locator('.edit-save-btn').click();
    await saved;
    await edit.locator('.modal-close').click();
    await expect(edit).toBeHidden();

    let releaseResponse = () => {};
    const responseRelease = new Promise<void>((resolve) => {
      releaseResponse = resolve;
    });
    let markResponseReady = () => {};
    const responseReady = new Promise<void>((resolve) => {
      markResponseReady = resolve;
    });
    const writebackPath = `/api/books/${writebackID}/writeback`;
    await page.route(`**${writebackPath}`, async (route) => {
      const response = await route.fetch();
      markResponseReady();
      await responseRelease;
      await route.fulfill({ response });
    });

    try {
      await page.locator('#btn-book-menu').click();
      await page.locator('.menu-item', { hasText: 'Write metadata to file' }).click();
      await responseReady;

      // Navigate within the SPA while A's completed server response is held.
      // Releasing it on B used to replace B's title and currentBookDetail with A.
      await page.locator('#nav-library').click();
      await expect(page).toHaveURL(/\/$/);
      const nextBook = page.locator('.book-card', { hasText: 'No Cover Book' });
      await expect(nextBook).toBeVisible();
      await nextBook.locator('.book-title-link').click();
      await expect(page.locator('.detail-title')).toHaveText('No Cover Book');
      const nextBookURL = page.url();

      releaseResponse();
      await expect(
        page.locator('.toast:not(.toast-leaving)', { hasText: /Metadata written|already up to date/ }),
      ).toBeVisible();
      await expect(page).toHaveURL(nextBookURL);
      await expect(page.locator('.detail-title')).toHaveText('No Cover Book');

      await page.locator('#btn-edit-book').click();
      await expect(page.locator('.edit-modal input[name="title"]')).toHaveValue('No Cover Book');
      await page.locator('.edit-modal .modal-close').click();
    } finally {
      releaseResponse();
      await page.unroute(`**${writebackPath}`);
    }
  });

  test('Add book upload reports an existing duplicate', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('.book-card').first()).toBeVisible();
    // The compact upload affordance lives beside the brand and stays available
    // across app pages; import results use the shared toast surface.
    await expect(page.locator('#sidebar-upload #book-upload-btn')).toHaveAttribute(
      'aria-label',
      'Add books',
    );

    await page.locator('#book-upload-input').setInputFiles('fixtures/without-cover.epub');

    const toast = page.locator('.toast:not(.toast-leaving)', {
      hasText: 'Already in library: No Cover Book',
    });
    await expect(toast).toBeVisible();

    await toast.getByRole('button', { name: 'Open' }).click();
    await expect(page.locator('.detail-title')).toContainText('No Cover Book');
  });

  test('Reuploading a trashed book restores it', async ({ page }) => {
    const title = `Upload Restore ${Date.now().toString(36)}`;
    const file = {
      name: `${title}.fb2`,
      mimeType: 'application/xml',
      buffer: Buffer.from(`<FictionBook><body><p>${title}</p></body></FictionBook>`),
    };

    await page.goto('/');
    await expect(page.locator('.book-card').first()).toBeVisible();
    await page.locator('#book-upload-input').setInputFiles(file);

    const card = page.locator('.book-card', { hasText: title });
    await expect(card).toBeVisible();
    await card.locator('.book-title-link').click();
    await expect(page.locator('.detail-title')).toContainText(title);
    await page.locator('#btn-book-menu').click();
    await page.locator('.menu-item', { hasText: 'Remove from library' }).click();
    await page.locator('.modal-confirm').getByRole('button', { name: 'Remove' }).click();
    await page.waitForURL((url) => new URL(url).pathname === '/');
    await expect(page.locator('.book-card', { hasText: title })).toHaveCount(0);

    await page.locator('#book-upload-input').setInputFiles(file);
    const toast = page.locator('.toast:not(.toast-leaving)', { hasText: `Restored: ${title}` });
    await expect(toast).toBeVisible();
    await expect(page.locator('.book-card', { hasText: title })).toBeVisible();
  });

  test('Multiple book upload reports imported and duplicate counts', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('.book-card').first()).toBeVisible();

    const title = `Batch Upload ${Date.now().toString(36)}`;
    await page.locator('#book-upload-input').setInputFiles([
      {
        name: 'without-cover.epub',
        mimeType: 'application/epub+zip',
        buffer: readFileSync('fixtures/without-cover.epub'),
      },
      {
        name: `${title}.fb2`,
        mimeType: 'application/xml',
        buffer: Buffer.from('<FictionBook><body><p>opaque upload</p></body></FictionBook>'),
      },
    ]);

    const toast = page.locator('.toast:not(.toast-leaving)', {
      hasText: 'Imported 1, Duplicates 1',
    });
    await expect(toast).toBeVisible();
    await expect(toast).toContainText('Duplicates 1');
    await expect(page.locator('.book-card', { hasText: title })).toBeVisible();
  });

  test('Manual shelf can be created and opened from the sidebar', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('.book-card').first()).toBeVisible();

    const firstCard = page.locator('.book-card').first();
    const title = ((await firstCard.locator('.book-title').textContent()) || '').trim();
    expect(title).not.toBe('');

    await createManualShelfFromSidebar(page, 'Browser Shelf');

    await firstCard.locator('.book-title').click();
    await expect(page.locator('.detail-title')).toContainText(title);

    await page.locator('#btn-book-shelves').click();
    // The picker is a popover; scope to it since the sidebar also names the shelf.
    const popover = page.locator('.shelf-popover');
    const checkbox = popover.getByLabel('Browser Shelf');
    await expect(checkbox).toBeVisible();
    await checkbox.check();
    await page.keyboard.press('Escape');
    await expect(popover).toBeHidden();

    await page.locator('#shelf-nav .shelf-nav-item', { hasText: 'Browser Shelf' }).click();
    await expect(page.locator('.book-card', { hasText: title })).toBeVisible();
  });

  test('A shelf can be created from the book-page popover', async ({ page }) => {
    await page.goto('/');
    await page.locator('.book-card').first().locator('.book-title').click();
    await expect(page.locator('.detail-title')).toBeVisible();

    await page.locator('#btn-book-shelves').click();
    const popover = page.locator('.shelf-popover');
    await popover.locator('.shelf-popover-create-btn').click();
    const dialog = page.locator('.settings-submodal');
    await expect(dialog.getByRole('heading', { name: 'New shelf' })).toBeVisible();
    await dialog.getByLabel('Name').fill('Popover Shelf');
    await dialog.getByRole('button', { name: 'Create shelf' }).click();
    await expect(dialog).toHaveCount(0);

    await expect(popover).toBeHidden();
    await expect(
      page.locator('#shelf-nav .shelf-nav-item', { hasText: 'Popover Shelf' }),
    ).toBeVisible();

    // Reopening the picker shows the new shelf already attached to this book.
    await page.locator('#btn-book-shelves').click();
    const newRow = popover.locator('.shelf-picker-row', { hasText: 'Popover Shelf' });
    await expect(newRow.locator('input[type="checkbox"]')).toBeChecked();
  });

  test('Popover keyboard nav toggles multiple shelves in a row', async ({ page }) => {
    await page.goto('/');
    for (const name of ['Kbd One', 'Kbd Two']) {
      await createManualShelfFromSidebar(page, name);
    }

    await page.locator('.book-card').first().locator('.book-title').click();
    await page.locator('#btn-book-shelves').click();
    const popover = page.locator('.shelf-popover');
    const first = popover.locator('.shelf-picker-row', { hasText: 'Kbd One' }).locator('input');
    await first.focus();

    // Toggling with Space must not lose focus, so Down + Space can keep going.
    await page.keyboard.press('Space');
    await page.keyboard.press('ArrowDown');
    await page.keyboard.press('Space');

    await expect(
      popover.locator('.shelf-picker-row', { hasText: 'Kbd One' }).locator('input'),
    ).toBeChecked();
    await expect(
      popover.locator('.shelf-picker-row', { hasText: 'Kbd Two' }).locator('input'),
    ).toBeChecked();
  });

  test('Search can be saved as a query shelf', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('.book-card').first()).toBeVisible();

    await page.locator('#search-input').fill('author:"Test Author" tag:"fixture"');
    await expect(page.locator('#save-search-btn')).toBeVisible();
    await page.locator('#save-search-btn').click();
    let dialog = page.locator('.settings-submodal');
    await expect(dialog.getByRole('heading', { name: 'Save search' })).toBeVisible();
    await expect(dialog.getByLabel('Name')).toHaveValue('');
    await expect(dialog.getByRole('button', { name: 'Create shelf' })).toBeDisabled();
    await dialog.getByRole('button', { name: 'Cancel' }).click();
    await expect(dialog).toHaveCount(0);

    await page.locator('#search-input').fill('No Cover');
    await expect(page.locator('#save-search-btn')).toBeVisible();
    await page.locator('#save-search-btn').click();
    dialog = page.locator('.settings-submodal');
    await expect(dialog.getByRole('heading', { name: 'Save search' })).toBeVisible();
    await expect(dialog.getByLabel('Name')).toHaveValue('No Cover');
    await dialog.getByRole('button', { name: 'Create shelf' }).click();
    await expect(dialog).toHaveCount(0);

    await expect(page).toHaveURL(/shelf=/);
    const shelfRow = page.locator('#shelf-nav .shelf-nav-row', { hasText: 'No Cover' });
    await expect(shelfRow.locator('.shelf-nav-item')).toBeVisible();
    await expect(shelfRow.locator('.shelf-kind-marker[data-kind="query"]')).toHaveCount(1);
    await expect(page.locator('.book-card', { hasText: 'No Cover Book' })).toBeVisible();
  });

  test('Shelf can be edited and deleted from the sidebar', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('.book-card').first()).toBeVisible();

    await createManualShelfFromSidebar(page, 'Temp Shelf');
    const row = page.locator('#shelf-nav .shelf-nav-row', { hasText: 'Temp Shelf' });
    await expect(row.locator('.shelf-nav-item')).toBeVisible();

    // Edit via the kebab action menu. The kebab only becomes interactive on
    // hover, so hover the row first.
    await row.hover();
    await row.locator('.shelf-actions-btn').click();
    await page.getByRole('menuitem', { name: 'Edit' }).click();
    const editShelf = page.locator('.settings-submodal');
    await expect(editShelf.getByRole('heading', { name: 'Edit shelf' })).toBeVisible();
    await editShelf.getByLabel('Name').fill('Renamed Shelf');
    await editShelf.getByRole('button', { name: 'Save' }).click();
    await expect(editShelf).toHaveCount(0);
    await expect(
      page.locator('#shelf-nav .shelf-nav-item', { hasText: 'Renamed Shelf' }),
    ).toBeVisible();

    // Delete via the kebab action menu + inline confirm.
    const renamedRow = page.locator('#shelf-nav .shelf-nav-row', { hasText: 'Renamed Shelf' });
    await renamedRow.hover();
    await renamedRow.locator('.shelf-actions-btn').click();
    await page.getByRole('menuitem', { name: 'Delete' }).click();
    await page.locator('#shelf-nav .shelf-delete-yes').click();
    await expect(
      page.locator('#shelf-nav .shelf-nav-item', { hasText: 'Renamed Shelf' }),
    ).toHaveCount(0);
  });

  test('Send dialog adds a device inline and prepares a plan', async ({ page }) => {
    // Sending is off by default, so the button exists only once an admin asks
    // for the feature.
    const enabledRes = await page.request.put('/api/admin/delivery', {
      data: { enabled: true },
    });
    expect(enabledRes.ok()).toBe(true);
    const settingsRes = await page.request.put('/api/admin/email', {
      data: {
        host: 'smtp.example.org',
        port: 587,
        security: 'starttls',
        from_address: 'books@example.org',
        attachment_limit_mb: 25,
      },
    });
    expect(settingsRes.ok()).toBe(true);

    await page.goto('/');
    const card = page.locator('.book-card', { hasText: 'With Cover Book' });
    await expect(card).toBeVisible();
    await card.locator('.book-title').click();
    await expect(page.locator('.detail-title')).toContainText('With Cover Book');

    await page.getByRole('button', { name: 'Send' }).click();
    const dialog = page.locator('.settings-submodal');
    await expect(dialog.getByRole('heading', { name: 'Send to device' })).toBeVisible();
    await expect(dialog.getByText('Add a reader email address')).toBeVisible();

    await dialog.getByRole('textbox', { name: 'Name' }).fill('Kindle');
    await dialog.getByRole('textbox', { name: 'Email' }).fill('reader@kindle.com');
    await expect(dialog.getByRole('combobox', { name: 'Preset' })).toHaveValue('kindle');
    await dialog.getByRole('button', { name: 'Add device' }).click();

    await expect(dialog.getByRole('combobox', { name: 'Device' })).toBeVisible();
    await expect(dialog.locator('.send-device-plan')).toContainText('EPUB');
    await expect(dialog.getByRole('button', { name: 'Send' })).toBeEnabled();
    await page.screenshot({ path: 'screenshots/send-device-inline-add.png', fullPage: true });
  });

  test('Trash: remove a book, restore it, then permanently delete it', async ({ page }) => {
    // Self-contained: upload a uniquely-named book so the flow never disturbs the
    // shared fixture library, and end by purging it (net-zero).
    const title = `Trash Flow ${Date.now().toString(36)}`;
    await page.goto('/');
    await expect(page.locator('.book-card').first()).toBeVisible();
    await page.locator('#book-upload-input').setInputFiles({
      name: `${title}.fb2`,
      mimeType: 'application/xml',
      buffer: Buffer.from(`<FictionBook><body><p>${title}</p></body></FictionBook>`),
    });
    const card = page.locator('.book-card', { hasText: title });
    await expect(card).toBeVisible();

    // Open the book and remove it via the overflow menu + confirm dialog.
    await card.locator('.book-title-link').click();
    await expect(page.locator('.detail-title')).toContainText(title);
    await page.locator('#btn-book-menu').click();
    await page.locator('.menu-item', { hasText: 'Remove from library' }).click();
    await page.locator('.modal-confirm').getByRole('button', { name: 'Remove' }).click();

    // Lands back on the library, and the book is gone from it.
    await page.waitForURL((url) => new URL(url).pathname === '/');
    await expect(page.locator('.book-card', { hasText: title })).toHaveCount(0);

    // It shows in Trash, attributed and with admin actions.
    await page.goto('/trash');
    const trashCard = page.locator('.trash-card', { hasText: title });
    await expect(trashCard).toBeVisible();
    await expect(trashCard.locator('.trash-card-meta')).toContainText('Trashed');
    await expect(trashCard.locator('.btn-purge')).toBeVisible(); // admin only
    await page.screenshot({ path: 'screenshots/trash.png', fullPage: true });

    // Restore brings it back to the library.
    await trashCard.locator('.btn-restore').click();
    await expect(page.locator('.trash-card', { hasText: title })).toHaveCount(0);
    await page.goto('/');
    await expect(page.locator('.book-card', { hasText: title })).toBeVisible();

    // Trash it again and permanently delete it to clean up.
    await page.locator('.book-card', { hasText: title }).locator('.book-title-link').click();
    await page.locator('#btn-book-menu').click();
    await page.locator('.menu-item', { hasText: 'Remove from library' }).click();
    await page.locator('.modal-confirm').getByRole('button', { name: 'Remove' }).click();
    await page.waitForURL((url) => new URL(url).pathname === '/');

    await page.goto('/trash');
    await page.locator('.trash-card', { hasText: title }).locator('.btn-purge').click();
    await page.locator('.modal-confirm').getByRole('button', { name: 'Delete permanently' }).click();
    await expect(page.locator('.trash-card', { hasText: title })).toHaveCount(0);
  });

  test('AZW4 upload exposes a PDF download option', async ({ page }) => {
    const title = `AZW4 Download ${Date.now().toString(36)}`;
    const pdf = Buffer.from('%PDF-1.7\nbrowser test\n%%EOF');

    await page.goto('/');
    await expect(page.locator('.book-card').first()).toBeVisible();
    await page.locator('#book-upload-input').setInputFiles({
      name: `${title}.azw4`,
      mimeType: 'application/vnd.amazon.ebook',
      buffer: testMOBIWithPayload(pdf),
    });

    const card = page.locator('.book-card', { hasText: title });
    await expect(card).toBeVisible();
    await card.locator('.book-title-link').click();
    await expect(page.locator('.detail-title')).toContainText(title);

    const group = page.locator('.detail-download-group', { hasText: 'AZW4' });
    await expect(group).toBeVisible();
    const nativeDownload = group.locator('a.detail-download-main');
    await expect(nativeDownload).toContainText('AZW4');

    await group.locator('.detail-download-menu').click();
    const menu = page.locator('.floating-menu:not([hidden])');
    await expect(menu.locator('.menu-item', { hasText: 'Download AZW4' })).toBeVisible();
    await expect(menu.locator('.menu-item', { hasText: 'Download PDF' })).toBeVisible();

    const nativeHref = await nativeDownload.getAttribute('href');
    if (!nativeHref) throw new Error('missing native download href');
    const pdfResponse = await page.request.get(`${nativeHref}/as/pdf`);
    expect(pdfResponse.status()).toBe(200);
    expect(await pdfResponse.body()).toEqual(pdf);
  });

  test('EPUB exposes a distinct repaired download', async ({ page }) => {
    const title = `EPUB Repair ${Date.now().toString(36)}`;

    await page.goto('/');
    await expect(page.locator('.book-card').first()).toBeVisible();
    await page.locator('#book-upload-input').setInputFiles(epub(title, 'Repair Author', title));

    const card = page.locator('.book-card', { hasText: title });
    await expect(card).toBeVisible();
    await card.locator('.book-title-link').click();
    await expect(page.locator('.detail-title')).toContainText(title);

    const group = page.locator('.detail-download-group', { hasText: 'EPUB' });
    await expect(group).toBeVisible();
    const nativeDownload = group.locator('a.detail-download-main');
    await expect(nativeDownload).toContainText('EPUB');

    await group.locator('.detail-download-menu').click();
    const menu = page.locator('.floating-menu:not([hidden])');
    await expect(menu.locator('.menu-item', { hasText: /^Download EPUB$/ })).toBeVisible();
    await expect(menu.locator('.menu-item', { hasText: 'Download Repaired EPUB' })).toBeVisible();
    await expect(menu.locator('.menu-item', { hasText: 'Download KEPUB' })).toBeVisible();

    const nativeHref = await nativeDownload.getAttribute('href');
    if (!nativeHref) throw new Error('missing native EPUB download href');
    const repairedResponse = await page.request.get(`${nativeHref}/as/epub`);
    expect(repairedResponse.status()).toBe(200);
    expect(repairedResponse.headers()['content-type']).toBe('application/epub+zip');
    expect(await repairedResponse.body()).not.toEqual(epub(title, 'Repair Author', title).buffer);
  });


});
