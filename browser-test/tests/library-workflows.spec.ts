import { Buffer } from 'node:buffer';
import { readFileSync } from 'node:fs';
import { expect, test } from './fixtures';
import { findBook, login } from './helpers';

test.describe('Library workflows', () => {
  test('Sidebar log out posts to /logout', async ({ page }) => {
    await page.context().clearCookies();
    await login(page);

    const logoutMethods: string[] = [];
    page.on('request', (request) => {
      const path = new URL(request.url()).pathname;
      if (path === '/logout') logoutMethods.push(request.method());
    });

    await page.goto('/');
    const logout = page.getByRole('button', { name: 'Log out' });
    await expect(logout).toBeVisible();

    // The bar takes the row's place, so a resize would nudge the whole footer.
    const rowBox = await logout.boundingBox();
    await logout.click();
    await expect(page.locator('.account-confirm')).toBeVisible();
    const barBox = await page.locator('.account-confirm').boundingBox();
    expect(barBox!.height).toBe(rowBox!.height);
    expect(barBox!.width).toBe(rowBox!.width);

    await page.locator('.account-confirm-no').click();
    await expect(logout).toBeVisible();
    expect(logoutMethods).toEqual([]);

    await logout.click();
    await expect(page.locator('.account-confirm')).toBeVisible();
    await Promise.all([
      page.waitForURL((url) => new URL(url).pathname === '/login'),
      page.locator('.account-confirm-yes').click(),
    ]);

    expect(logoutMethods).toEqual(['POST']);

    await page.goto('/');
    await expect(page).toHaveURL(/\/login$/);
  });

  test('Storage settings shows the books folder health line and scans on demand', async ({
    page,
  }) => {
    await page.goto('/');
    await expect(page.locator('.account-settings')).toBeVisible();

    await page.locator('.account-settings').click();

    const modal = page.locator('.settings-modal');
    await expect(modal).toBeVisible();

    await modal.getByRole('tab', { name: 'Storage' }).click();
    await expect(modal.getByRole('heading', { name: 'Storage' })).toBeVisible();

    const health = modal.locator('.settings-health');
    await expect(health).toContainText('Reachable');
    await expect(health).toContainText('book');
    const addFromFolder = modal.getByRole('button', { name: 'Add from folder…' });
    await expect(addFromFolder).toHaveAttribute('aria-expanded', 'false');
    await expect(modal.getByPlaceholder('/srv/books')).toHaveCount(0);

    await addFromFolder.click();
    await expect(modal.getByPlaceholder('/srv/books')).toBeVisible();
    await expect(modal.getByRole('button', { name: 'Hide' })).toHaveAttribute(
      'aria-expanded',
      'true',
    );
    await modal.getByRole('button', { name: 'Hide' }).click();
    await expect(modal.getByPlaceholder('/srv/books')).toHaveCount(0);

    // The fixture's incoming folder is empty.
    await modal.getByRole('button', { name: 'Scan now' }).click();
    await expect(page.locator('.toast')).toContainText(/No new files|imported|already in library/);
  });

  test('Metadata write-back: settings mode plus the book-page action', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('.account-settings')).toBeVisible();
    await page.locator('.account-settings').click();
    const settings = page.locator('.settings-modal');
    const writebackRow = settings.locator('.settings-row', { hasText: 'Metadata write-back' });
    await expect(writebackRow).toBeVisible();
    await expect(writebackRow.locator('.settings-writeback-control')).toContainText('Manual');
    await settings.getByRole('tab', { name: 'Storage' }).click();
    const layoutRow = settings.locator('.settings-row', { hasText: 'File layout' });
    await expect(layoutRow).toBeVisible();
    await expect(layoutRow.locator('input')).toHaveValue(
      '{author_bucket}/{author_sort}/{title} [a{asset_id}]{dot_ext}',
    );
    await expect(layoutRow.locator('.settings-note')).toContainText('CLI');

    await page.goto('/?q=Writeback%20Fixture');
    const card = page.locator('.book-card', { hasText: 'Writeback Fixture' });
    await expect(card).toBeVisible();
    await card.locator('.book-title').click();
    await expect(page.locator('.detail-title')).toContainText('Writeback Fixture');

    // Editing metadata makes the file eligible for write-back.
    await page.locator('#btn-edit-book').click();
    await expect(page.locator('.edit-modal')).toBeVisible();
    const titleInput = page.locator('.edit-modal input[name="title"]');
    const saveBtn = page.locator('.edit-modal .edit-save-btn');
    await titleInput.fill('Writeback Fixture Revised');
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
    const book = await findBook(page, 'Writeback Fixture');
    const writebackID = book.id;
    const edit = await page.request.patch(`/api/books/${writebackID}`, {
      data: { title: 'Writeback Fixture Revised' },
    });
    expect(edit.ok()).toBe(true);
    await page.goto(`/book/${writebackID}`);

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

      // A's late response must not replace the book opened in the meantime.
      await page.locator('#nav-library').click();
      await expect(page).toHaveURL(/\/$/);
      const nextBook = page.locator('.book-card', { hasText: 'No Cover Book' });
      await expect(nextBook).toBeVisible();
      await nextBook.locator('.book-title-link').click();
      await expect(page.locator('.detail-title')).toHaveText('No Cover Book');
      const nextBookURL = page.url();

      releaseResponse();
      await expect(
        page.locator('.toast:not(.toast-leaving)', {
          hasText: /Metadata written|already up to date/,
        }),
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

  test('Upload opens an existing duplicate and restores it after removal', async ({ page }) => {
    const title = 'Upload Restore';
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
    await page.locator('#book-upload-input').setInputFiles(file);
    const duplicate = page.locator('.toast:not(.toast-leaving)', {
      hasText: `Already in library: ${title}`,
    });
    await expect(duplicate).toBeVisible();
    await duplicate.getByRole('button', { name: 'Open' }).click();
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

    const title = 'Batch Upload';
    await page.locator('#book-upload-input').setInputFiles([
      {
        name: 'without-cover.epub',
        mimeType: 'application/epub+zip',
        buffer: readFileSync(new URL('../fixtures/without-cover.epub', import.meta.url)),
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

  test('A manual shelf can be created, filled, renamed and deleted', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('.book-card').first()).toBeVisible();

    const firstCard = page.locator('.book-card').first();
    const title = ((await firstCard.locator('.book-title').textContent()) || '').trim();
    expect(title).not.toBe('');

    await page.locator('#new-shelf-btn').click();
    const dialog = page.locator('.settings-submodal');
    await expect(dialog.getByRole('heading', { name: 'New shelf' })).toBeVisible();
    await dialog.getByLabel('Name').fill('Browser Shelf');
    await dialog.getByRole('button', { name: 'Create shelf' }).click();
    await expect(dialog).toHaveCount(0);
    await expect(
      page.locator('#shelf-nav .shelf-nav-item', { hasText: 'Browser Shelf' }),
    ).toBeVisible();

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

    const shelfLink = page.locator('#shelf-nav .shelf-nav-item', { hasText: 'Browser Shelf' });
    await shelfLink.click();
    await expect(shelfLink).toHaveClass(/active/);
    await expect(page.locator('#nav-library')).not.toHaveClass(/active/);
    const shelfURL = page.url();
    await page.locator('.book-card', { hasText: title }).locator('.book-title-link').click();
    await expect(page.locator('.detail-title')).toHaveText(title);
    await expect(shelfLink).toHaveClass(/active/);
    await page.locator('.back-link a').click();
    await expect(page).toHaveURL(shelfURL);
    await expect(page.locator('.book-card', { hasText: title })).toBeVisible();
    await expect(shelfLink).toHaveClass(/active/);

    await page.locator('.sidebar-brand a').click();
    await expect(page).toHaveURL((url) => url.pathname === '/' && !url.searchParams.has('shelf'));
    await expect(page.locator('#nav-library')).toHaveClass(/active/);
    await expect(shelfLink).not.toHaveClass(/active/);
    await shelfLink.click();

    const row = page.locator('#shelf-nav .shelf-nav-row', { hasText: 'Browser Shelf' });
    await expect(row.locator('.shelf-nav-item')).toBeVisible();

    // The action button becomes interactive on hover.
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

    const renamedRow = page.locator('#shelf-nav .shelf-nav-row', { hasText: 'Renamed Shelf' });
    await renamedRow.hover();
    await renamedRow.locator('.shelf-actions-btn').click();
    const shelfRowH = (await page.locator('#shelf-nav .shelf-nav-item').first().boundingBox())!
      .height;
    await page.getByRole('menuitem', { name: 'Delete' }).click();
    const shelfBarH = (await page.locator('#shelf-nav .shelf-delete-confirm').boundingBox())!
      .height;
    expect(shelfBarH).toBe(shelfRowH);
    await page.locator('#shelf-nav .shelf-delete-yes').click();
    await expect(
      page.locator('#shelf-nav .shelf-nav-item', { hasText: 'Renamed Shelf' }),
    ).toHaveCount(0);
  });

  test('The book shelf picker creates shelves and toggles membership from the keyboard', async ({
    page,
  }) => {
    const created = await page.request.post('/api/shelves', {
      data: { name: 'Another Shelf', kind: 'manual' },
    });
    expect(created.ok()).toBe(true);
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

    const first = popover.getByLabel('Another Shelf', { exact: true });
    await first.focus();
    await page.keyboard.press('Space');
    await page.keyboard.press('ArrowDown');
    await page.keyboard.press('Space');
    await expect(first).toBeChecked();
    await expect(newRow.locator('input')).not.toBeChecked();
    await expect(newRow.locator('input')).toBeFocused();
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
  });

  test('Trash: remove a book, restore it, then permanently delete it', async ({ page }) => {
    const title = 'No Cover Book';
    await page.goto('/');
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
    await page.getByRole('button', { name: 'Manage library' }).click();
    await page.getByRole('menuitem', { name: 'Trash', exact: true }).click();
    await expect(page).toHaveURL(/\/trash$/);
    const trashCard = page.locator('.trash-card', { hasText: title });
    await expect(trashCard).toBeVisible();
    await expect(trashCard.locator('.trash-card-meta')).toContainText('Trashed');
    await expect(trashCard.locator('.btn-purge')).toBeVisible(); // admin only

    // Restore brings it back to the library.
    await trashCard.locator('.btn-restore').click();
    await expect(page.locator('.trash-card', { hasText: title })).toHaveCount(0);
    await page.goto('/');
    await expect(page.locator('.book-card', { hasText: title })).toBeVisible();

    // Permanent deletion is a separate action after moving the book to Trash.
    await page.locator('.book-card', { hasText: title }).locator('.book-title-link').click();
    await page.locator('#btn-book-menu').click();
    await page.locator('.menu-item', { hasText: 'Remove from library' }).click();
    await page.locator('.modal-confirm').getByRole('button', { name: 'Remove' }).click();
    await page.waitForURL((url) => new URL(url).pathname === '/');

    await page.getByRole('button', { name: 'Manage library' }).click();
    await page.getByRole('menuitem', { name: 'Trash', exact: true }).click();
    await expect(page).toHaveURL(/\/trash$/);
    await page.locator('.trash-card', { hasText: title }).locator('.btn-purge').click();
    await page
      .locator('.modal-confirm')
      .getByRole('button', { name: 'Delete permanently' })
      .click();
    await expect(page.locator('.trash-card', { hasText: title })).toHaveCount(0);
  });

  test('Book details stay compact and expand in place', async ({ page }) => {
    const title = 'With Cover Book';
    const fixture = await findBook(page, title);
    // Rich-text margins count toward the space above the annotations.
    const paragraph =
      'This publisher blurb runs long on purpose so the book page has something to clamp. '.repeat(
        4,
      );
    const description = `<h3>About the book</h3>${`<p>${paragraph}</p>`.repeat(4)}`;

    const tags = [
      'Literature',
      'Essays',
      'Reading',
      'Creativity',
      'Memory',
      'Culture',
      'Language',
      'Art',
      'Philosophy',
      'History',
      'Education',
      'Criticism',
      'Nonfiction',
      'Writing',
    ];
    const update = await page.request.patch(`/api/books/${fixture.id}`, {
      data: { description, tags: tags.join(', ') },
    });
    expect(update.ok()).toBe(true);
    const book = await update.json();
    const highlight = await page.request.post(
      `/api/reader/assets/${book.assets[0].id}/annotations`,
      {
        data: {
          locator: { cfi: 'epubcfi(/6/2!/4/2/1:0)' },
          quote: 'A passage worth returning to.',
          note: 'Keep this question for the next discussion.',
        },
      },
    );
    expect(highlight.ok()).toBe(true);
    await page.goto('/');
    const card = page.locator('.book-card', { hasText: title });
    await card.locator('.book-title-link').click();
    await expect(page.locator('.detail-title')).toHaveText(title);

    const blurb = page.locator('.detail-description');
    const more = page.locator('.detail-description-more');
    await expect(more).toBeVisible();
    const clampedHeight = await blurb.evaluate((el) => el.clientHeight);
    const tagRow = page.locator('.detail-tags');
    const visibleTags = tagRow.locator('.detail-tag:visible');
    const tagsMore = tagRow.getByRole('button');
    await expect(visibleTags).toHaveText(tags.slice(0, 5));
    await expect(tagsMore).toHaveText(`+${tags.length - 5}`);
    await expect(page.getByRole('heading', { name: 'Highlights & notes' })).toBeInViewport({
      ratio: 1,
    });

    await more.click();
    await expect(more).toHaveText('Show less');
    expect(await blurb.evaluate((el) => el.clientHeight)).toBeGreaterThan(clampedHeight);
    await tagsMore.click();
    await expect(visibleTags).toHaveCount(tags.length);
    await more.click();
    expect(await blurb.evaluate((el) => el.clientHeight)).toBe(clampedHeight);

    await page.setViewportSize({ width: 390, height: 844 });
    await expect(visibleTags).toHaveCount(tags.length);
    await page.goBack();
    await expect(card).toBeVisible();
    await card.locator('.book-title-link').click();
    await expect(visibleTags).toHaveCount(5);
    await tagsMore.click();
    await expect(visibleTags).toHaveCount(tags.length);
  });
});
