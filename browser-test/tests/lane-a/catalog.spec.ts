import { expect, type Locator, test } from '../fixtures';

async function expectImageLoaded(img: Locator): Promise<void> {
  await expect
    .poll(async () => {
      return await img.evaluate((i: HTMLImageElement) => i.complete && i.naturalWidth > 0);
    })
    .toBe(true);
}

test.describe('Catalog', () => {
  test('Library page renders correctly', async ({ page }) => {
    await page.setViewportSize({ width: 1600, height: 1000 });
    await page.goto('/');

    await expect(page.locator('.book-card').first()).toBeVisible();

    // Grid metadata stays out of the layout until the cover is hovered or focused.
    const firstCard = page.locator('.book-card').first();
    const firstCardInfo = firstCard.locator('.book-info');
    await expect(firstCardInfo).toHaveCSS('opacity', '0');
    await firstCard.hover();
    await expect(firstCardInfo).toHaveCSS('opacity', '1');
    await expect(firstCard.locator('.book-title')).not.toBeEmpty();
    await expect(firstCard.locator('.book-authors')).not.toBeEmpty();

    // Cover rendering: the cover <img> on a card with a cover actually decodes.
    const covers = await page.locator('img.book-cover-image').all();
    if (covers.length > 0) {
      for (const cover of covers) {
        await expectImageLoaded(cover);
      }
    }

    // The book imported without a cover uses the server-generated placeholder image.
    const noCoverCard = page.locator('.book-card', { hasText: 'No Cover Book' });
    await expect(noCoverCard).toBeVisible();
    const noCoverSlot = noCoverCard.locator('.book-cover-slot');
    await expect(noCoverSlot.locator('img.book-cover-image')).toHaveCount(1);
    await expectImageLoaded(noCoverSlot.locator('img.book-cover-image'));

    await page.screenshot({ path: 'screenshots/library.png', fullPage: true });
  });

  test('Library empty states distinguish no matches and empty shelves', async ({ page }) => {
    await page.goto('/?q=zzzz-no-such-book');

    const empty = page.locator('.library-empty-state');
    await expect(empty).toBeVisible();
    await expect(empty.locator('h2')).toHaveText('No matches');
    await expect(empty).toContainText('zzzz-no-such-book');

    await page.screenshot({ path: 'screenshots/no-results.png', fullPage: true });

    await empty.getByRole('button', { name: 'Clear search' }).click();
    await expect(page).not.toHaveURL(/q=zzzz-no-such-book/);
    await expect(page.locator('.book-card').first()).toBeVisible();

    const shelf = await page.evaluate(async () => {
      const res = await fetch('/api/shelves', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name: 'Empty Shelf State', kind: 'manual' }),
      });
      if (!res.ok) throw new Error(await res.text());
      return await res.json();
    });
    await page.goto(`/?shelf=${encodeURIComponent(shelf.id)}`);

    await expect(page.locator('.library-empty-state h2')).toHaveText('Shelf is empty');
    await expect(page.locator('.library-empty-state')).toContainText('No books are on this shelf.');
    await page.evaluate(async (id) => {
      const res = await fetch(`/api/shelves/${encodeURIComponent(id)}`, { method: 'DELETE' });
      if (!res.ok) throw new Error(await res.text());
    }, shelf.id);
    await page.locator('.library-empty-state').getByRole('button', { name: 'Library' }).click();
    await expect(page.locator('.book-card').first()).toBeVisible();
  });

  test('Book page renders correctly', async ({ page }) => {
    await page.goto('/');
    const card = page.locator('.book-card', { hasText: 'With Cover Book' });
    await expect(card).toBeVisible();
    const detailHref = await card.locator('.book-title-link').getAttribute('href');
    const workId = detailHref ? new URL(detailHref, page.url()).pathname.split('/').pop() : '';
    if (!workId) throw new Error('missing book id');
    const detailRes = await page.request.get(`/api/books/${encodeURIComponent(workId)}`);
    expect(detailRes.ok()).toBe(true);
    const detail = (await detailRes.json()) as {
      assets: Array<{ id: string; can_read: boolean; is_primary: boolean }>;
    };
    const readableAsset =
      detail.assets.find((asset) => asset.is_primary && asset.can_read) ??
      detail.assets.find((asset) => asset.can_read);
    if (!readableAsset) throw new Error('missing readable asset');
    const positionRes = await page.request.put(
      `/api/reader/assets/${encodeURIComponent(readableAsset.id)}/state`,
      { data: { progress: 0.42, locator: { engine: 'browser-test', fraction: 0.42 } } },
    );
    expect(positionRes.ok()).toBe(true);
    const resetStatus = await page.request.put(
      `/api/books/${encodeURIComponent(workId)}/reading-status`,
      { data: { status: 'unread' } },
    );
    expect(resetStatus.ok()).toBe(true);

    await card.locator('.book-title').click();

    await expect(page.locator('#book-detail-container')).toBeVisible();

    await expect(page.locator('.detail-title')).toBeVisible();

    const downloadLink = page.locator('.detail-actions a.detail-action[href^="/download/"]');
    await expect(downloadLink.first()).toBeVisible();

    await expect(page.locator('.detail-authors a').first()).toBeVisible();

    await expect(page.locator('.detail-series a').first()).toBeVisible();

    await expect(page.locator('.detail-description strong')).toBeVisible();
    await expect(page.locator('.detail-description script')).toHaveCount(0);

    // A blurb this short never earns a "Show more": the clamp comes off rather
    // than hiding a line or two behind a click.
    await expect(page.locator('.detail-description-more')).toHaveCount(0);
    await expect(page.locator('.detail-description.detail-description--collapsed')).toHaveCount(0);

    await expect(page.locator('.detail-meta').first()).toContainText('Test Publisher · 13 June 2026');

    // The fixture's 3-letter "eng" is normalized to "en" on import and shown as
    // its English display name.
    await expect(page.locator('.detail-meta-top')).toContainText('Language: English');

    // ISBN (bibliographic) shows inline. The store id (google) is link-only —
    // just its label, no opaque value — and tucked behind a quiet inline "…"
    // toggle on the same row, revealed in place on click.
    const idRow = page.locator('.detail-identifiers-row').first();
    await expect(idRow).toContainText('ISBN 1234567890');
    const hiddenIds = idRow.locator('.detail-ids-extra');
    await expect(hiddenIds).toBeHidden();
    await expect(hiddenIds).toContainText('Google Books');
    await expect(hiddenIds).not.toContainText('gBookId123');
    const moreToggle = idRow.locator('.detail-id-more');
    await expect(moreToggle).toBeVisible();
    await moreToggle.click();
    await expect(hiddenIds).toBeVisible();
    await expect(moreToggle).toHaveCount(0);

    await expect(page.locator('#btn-edit-book')).toBeVisible();

    // Reading status stays a quiet line below the action row, but the whole
    // line is an accessible status menu rather than another permanent button.
    const readingStatus = page.locator('#btn-reading-status');
    await expect(readingStatus).toBeVisible();
    await expect(readingStatus).toHaveText('Unread');
    await expect(readingStatus.locator('[data-reader-progress-track]')).toBeHidden();
    await readingStatus.click();
    await expect(page.getByRole('menuitem', { name: 'Unread ✓' })).toBeVisible();
    await expect(page.getByRole('menuitem', { name: 'Dropped' })).toBeVisible();
    await page.screenshot({ path: 'screenshots/book-reading-status.png', fullPage: true });
    await page.getByRole('menuitem', { name: 'Dropped' }).click();
    await expect(readingStatus).toContainText('Dropped · 42%');
    await expect(readingStatus.locator('[data-reader-progress-track]')).toBeVisible();
    await readingStatus.click();
    await page.getByRole('menuitem', { name: 'Finished' }).click();
    await expect(readingStatus).toHaveText('Finished');
    await expect(readingStatus.locator('[data-reader-progress-track]')).toBeHidden();
    await readingStatus.click();
    await page.getByRole('menuitem', { name: 'Unread' }).click();
    await expect(readingStatus).toContainText('Unread');

    await page.screenshot({ path: 'screenshots/book.png', fullPage: true });
  });

  test('Cleanup page renders correctly', async ({ page }) => {
    await page.goto('/cleanup');

    await expect(page.locator('.cleanup-container')).toBeVisible();

    const tiles = page.locator('.cleanup-tile');
    await expect(tiles).toHaveCount(4);
    await expect(tiles.filter({ hasText: 'Missing cover' })).toBeVisible();
    await expect(tiles.filter({ hasText: 'Unknown author' })).toBeVisible();
    await expect(tiles.filter({ hasText: 'No tags' })).toBeVisible();
    await expect(tiles.filter({ hasText: 'No description' })).toBeVisible();

    const duplicatesHeader = page.locator('text=Possible duplicates');
    await expect(duplicatesHeader).toBeVisible();

    const duplicateGroupContainer = page.locator('.duplicate-group');
    await expect(duplicateGroupContainer.first()).toBeVisible();
    await expect(duplicateGroupContainer.first().locator('.cleanup-row')).toHaveCount(2);

    await page.screenshot({ path: 'screenshots/cleanup.png', fullPage: true });
  });

  test('Series page shows series tiles that open the series in the library', async ({ page }) => {
    const booksRes = await page.request.get(
      `/api/books?q=${encodeURIComponent('series:"Test Series"')}&sort=series`,
    );
    expect(booksRes.ok()).toBe(true);
    const seriesBooks = (await booksRes.json()) as Array<{ id: string }>;
    expect(seriesBooks.length).toBe(4);
    const finished = seriesBooks[0].id;
    const finishRes = await page.request.put(
      `/api/books/${encodeURIComponent(finished)}/reading-status`,
      { data: { status: 'finished' } },
    );
    expect(finishRes.ok()).toBe(true);

    try {
      await page.goto('/series');

      await expect(page.locator('.series-container')).toBeVisible();
      await expect(page.locator('#nav-series')).toHaveClass(/active/);

      const card = page.locator('.series-card', { hasText: 'Test Series' });
      await expect(card).toBeVisible();
      await expect(card.locator('.series-card-cover-image')).toBeVisible();
      await expect(card.locator('.series-card-author')).toHaveText('Cover Author');
      // One of the four volumes is finished, so the badge counts progress.
      await expect(card.locator('.series-card-count')).toHaveText('1/4');
      await expect(card.locator('.series-card-progress-fill')).toBeVisible();

      await page.screenshot({ path: 'screenshots/series.png', fullPage: true });

      await page.evaluate(() => document.body.setAttribute('data-spa-marker', 'series'));
      await card.click();
      await page.waitForURL(
        (url) =>
          url.pathname === '/' &&
          url.searchParams.get('q') === 'series:"Test Series"' &&
          url.searchParams.get('sort') === 'series',
      );
      await expect(page.locator('body')).toHaveAttribute('data-spa-marker', 'series');
      await expect(page.locator('.book-card')).toHaveCount(4);
    } finally {
      const resetRes = await page.request.put(
        `/api/books/${encodeURIComponent(finished)}/reading-status`,
        { data: { status: 'unread' } },
      );
      expect(resetRes.ok()).toBe(true);
    }
  });

  test('Book detail opens from the library without full reload', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('.book-card', { hasText: 'With Cover Book' })).toBeVisible();
    await page.evaluate(() => ((window as typeof window & { __polkaNavMarker?: string }).__polkaNavMarker = 'same-doc'));
    await page.route('**/api/books/*', async (route) => {
      if (route.request().method() === 'GET') {
        await new Promise((resolve) => setTimeout(resolve, 650));
      }
      await route.continue();
    });

    await page.locator('.book-card', { hasText: 'With Cover Book' }).locator('.book-title-link').click();
    await page.waitForURL((url) => url.pathname.startsWith('/book/'));
    await expect(page.locator('.book-detail-loading-card')).toContainText('Loading book');
    await expect(page.locator('.detail-title')).toHaveText('With Cover Book');
    expect(await page.evaluate(() => (window as typeof window & { __polkaNavMarker?: string }).__polkaNavMarker)).toBe(
      'same-doc',
    );

    await page.locator('.back-link a').click();
    await page.waitForURL((url) => url.pathname === '/');
    await expect(page.locator('.book-card', { hasText: 'With Cover Book' })).toBeVisible();
    expect(await page.evaluate(() => (window as typeof window & { __polkaNavMarker?: string }).__polkaNavMarker)).toBe(
      'same-doc',
    );

    await page.locator('.book-card', { hasText: 'With Cover Book' }).locator('.book-title-link').click();
    await page.waitForURL((url) => url.pathname.startsWith('/book/'));
    await expect(page.locator('.detail-title')).toHaveText('With Cover Book');
    await page.goBack();
    await expect(page.locator('.book-card', { hasText: 'With Cover Book' })).toBeVisible();
    expect(await page.evaluate(() => (window as typeof window & { __polkaNavMarker?: string }).__polkaNavMarker)).toBe(
      'same-doc',
    );
  });

  test('Shelf active state follows router book navigation', async ({ page }) => {
    await page.goto('/');
    const setup = await page.evaluate(async () => {
      const booksRes = await fetch('/api/books');
      if (!booksRes.ok) throw new Error(await booksRes.text());
      const books = await booksRes.json();
      const book = books.find((b: any) => b.title === 'No Cover Book') || books[0];
      if (!book) throw new Error('missing book');

      const name = `Shell Shelf ${Date.now().toString(36)}`;
      const shelfRes = await fetch('/api/shelves', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name, kind: 'manual' }),
      });
      if (!shelfRes.ok) throw new Error(await shelfRes.text());
      const shelf = await shelfRes.json();

      const addRes = await fetch(`/api/shelves/${encodeURIComponent(shelf.id)}/books/${encodeURIComponent(book.id)}`, {
        method: 'PUT',
      });
      if (!addRes.ok) throw new Error(await addRes.text());
      return { shelfId: shelf.id, shelfName: name, title: book.title };
    });

    try {
      await page.goto(`/?shelf=${encodeURIComponent(setup.shelfId)}`);
      const shelfLink = page.locator('#shelf-nav .shelf-nav-item', { hasText: setup.shelfName });
      await expect(shelfLink).toHaveClass(/active/);
      await expect(page.locator('#nav-library')).not.toHaveClass(/active/);
      await expect(page.locator('.book-card', { hasText: setup.title })).toBeVisible();

      await page.evaluate(() => ((window as typeof window & { __polkaNavMarker?: string }).__polkaNavMarker = 'same-doc'));
      await page.locator('.book-card', { hasText: setup.title }).locator('.book-title-link').click();
      await page.waitForURL(
        (url) => url.pathname.startsWith('/book/') && url.searchParams.get('shelf') === setup.shelfId,
      );
      await expect(page.locator('.detail-title')).toHaveText(setup.title);
      await expect(shelfLink).toHaveClass(/active/);
      await expect(page.locator('#nav-library')).not.toHaveClass(/active/);

      await page.locator('.back-link a').click();
      await page.waitForURL((url) => url.pathname === '/' && url.searchParams.get('shelf') === setup.shelfId);
      await expect(page.locator('.book-card', { hasText: setup.title })).toBeVisible();
      await expect(shelfLink).toHaveClass(/active/);
      await expect(page.locator('#nav-library')).not.toHaveClass(/active/);

      await page.locator('.sidebar-brand a').click();
      await page.waitForURL((url) => url.pathname === '/' && !url.searchParams.has('shelf'));
      await expect(page.locator('#nav-library')).toHaveClass(/active/);
      await expect(shelfLink).not.toHaveClass(/active/);
      expect(await page.evaluate(() => (window as typeof window & { __polkaNavMarker?: string }).__polkaNavMarker)).toBe(
        'same-doc',
      );
    } finally {
      await page.evaluate(async (shelfId) => {
        await fetch(`/api/shelves/${encodeURIComponent(shelfId)}`, { method: 'DELETE' });
      }, setup.shelfId);
    }
  });

  test('Table view renders and supports quick-edit', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('.book-card').first()).toBeVisible();

    await page.locator('#view-table-btn').click();

    const table = page.locator('.library-table');
    await expect(table).toBeVisible();

    await expect(table.locator('th.col-title')).toBeVisible();
    await expect(table.locator('th.col-author')).toBeVisible();

    const firstRow = table.locator('.table-row', { hasText: 'With Cover Book' });
    await expect(firstRow).toBeVisible();
    await expect(firstRow.locator('.table-title-link')).toBeVisible();
    await expect(firstRow.locator('.table-format-badge').first()).toBeVisible();

    await page.screenshot({ path: 'screenshots/table.png', fullPage: true });

    await firstRow.locator('.btn-quick-edit').click();
    await expect(page.locator('.modal-backdrop')).toBeVisible();

    // The lean list/table Book omits edition fields (identifiers/language/
    // publisher); the editor must load the full record, so the Identifiers field
    // is populated even when opened from the table — not empty.
    await expect(page.locator('.modal-backdrop input[name="identifiers"]')).toHaveValue(/1234567890/);

    await page.keyboard.press('Escape');
    await expect(page.locator('.modal-backdrop')).toHaveCount(0);

    await expect(table).toBeVisible();

    const batchRow = table.locator('.table-row').first();
    const batchRowID = (await batchRow.getAttribute('data-id')) || '';
    expect(batchRowID).not.toBe('');
    const originalBook = await page.evaluate(async (id) => {
      const res = await fetch(`/api/books/${encodeURIComponent(id)}`);
      if (!res.ok) throw new Error(await res.text());
      return await res.json();
    }, batchRowID);
    const originalTitle = originalBook.title as string;
    const editedTitle = `${originalTitle} Batch Nav`;
    const sequenceRequests: string[] = [];
    page.on('request', (request) => {
      const url = new URL(request.url());
      if (request.method() === 'GET' && url.pathname.endsWith('/sequence')) {
        sequenceRequests.push(url.pathname + url.search);
      }
    });

    try {
      await batchRow.locator('.btn-quick-edit').click();
      await expect(page.locator('.modal-backdrop')).toBeVisible();

      const titleInput = page.locator('.edit-modal input[name="title"]');
      const nextButton = page.locator('.edit-modal button[id^="btn-edit-next-"]');
      await expect(titleInput).toHaveValue(originalTitle);
      await expect(nextButton).toBeEnabled();
      await expect(nextButton).toHaveAttribute('aria-label', /Next:/);
      expect(sequenceRequests).toEqual([]);

      await titleInput.fill(editedTitle);
      await expect(nextButton).toBeEnabled();
      await expect(nextButton).toHaveAttribute('aria-label', /Save & Next:/);
      await nextButton.click();

      await expect(page.locator('.edit-modal')).toBeVisible();
      await expect(page.locator('.edit-modal input[name="title"]')).not.toHaveValue(editedTitle);
      await expect
        .poll(async () => {
          return await page.evaluate((id) => {
            const row = Array.from(document.querySelectorAll<HTMLElement>('.table-row')).find(
              (el) => el.dataset.id === id,
            );
            return row?.querySelector('.table-title-link')?.textContent?.trim() || '';
          }, batchRowID);
        })
        .toBe(editedTitle);
      expect(sequenceRequests).toEqual([]);

      await page.keyboard.press('Escape');
      await expect(page.locator('.modal-backdrop')).toHaveCount(0);
    } finally {
      await page.evaluate(
        async ({ id, title, sortTitle }) => {
          const res = await fetch(`/api/books/${encodeURIComponent(id)}`, {
            method: 'PATCH',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ title, sort_title: sortTitle || title }),
          });
          if (!res.ok) throw new Error(await res.text());
        },
        { id: batchRowID, title: originalTitle, sortTitle: originalBook.sort_title },
      );
    }

    await page.reload();
    await expect(page.locator('.library-table')).toBeVisible();

    await page.locator('#view-grid-btn').click();
    await expect(page.locator('.book-card').first()).toBeVisible();

  });


});
