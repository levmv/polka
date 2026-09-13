import { expect, type Locator, test } from './fixtures';
import { readerMutationFields } from './helpers';

function seriesItem(name: string, bookCount: number) {
  return {
    name,
    author: 'Stub Author',
    book_count: bookCount,
    finished_count: 0,
    cover_book_id: 1,
    cover_version: 0,
  };
}

async function expectImageLoaded(img: Locator): Promise<void> {
  await expect
    .poll(async () => {
      return await img.evaluate((i: HTMLImageElement) => i.complete && i.naturalWidth > 0);
    })
    .toBe(true);
}

test.describe('Catalog', () => {
  test('Tag links select a whole tag while unquoted search stays broad', async ({ page }) => {
    await page.goto('/');
    const bookIDs: string[] = [];
    const titles = ['With Cover Book', 'No Cover Book'];
    for (const [i, title] of titles.entries()) {
      const card = page.locator('.book-card', { hasText: title });
      await expect(card).toBeVisible();
      const href = await card.locator('.book-title-link').getAttribute('href');
      const id = href ? new URL(href, page.url()).pathname.split('/').pop() : '';
      if (!id) throw new Error('missing book id');
      bookIDs.push(id);
      const update = await page.request.patch(`/api/books/${id}`, {
        data: { tags: i === 0 ? 'История' : 'История искусства' },
      });
      expect(update.ok()).toBe(true);
    }

    await page.goto(`/book/${bookIDs[0]}`);
    await page
      .locator('.detail-tag')
      .filter({ hasText: /^История$/ })
      .click();
    await expect(page).toHaveURL((url) => url.searchParams.get('q') === 'tag:"История"');
    await expect(page.locator('.book-card')).toHaveCount(1);
    await expect(page.locator('.book-card')).toContainText(titles[0]);

    await page.locator('#search-input').fill('tag:Ист');
    await expect(page.locator('.book-card')).toHaveCount(2);
    for (const title of titles) {
      await expect(page.locator('.book-card', { hasText: title })).toBeVisible();
    }
  });

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
  });

  test('Library empty states distinguish no matches and empty shelves', async ({ page }) => {
    await page.goto('/?q=zzzz-no-such-book');

    const empty = page.locator('.library-empty-state');
    await expect(empty).toBeVisible();
    await expect(empty.locator('h2')).toHaveText('No matches');
    await expect(empty).toContainText('zzzz-no-such-book');

    await page.locator('#search-input').fill('');
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
    await page.goto(`/?shelf=${shelf.id}`);

    await expect(page.locator('.library-empty-state h2')).toHaveText('Shelf is empty');
    await expect(page.locator('.library-empty-state')).toContainText('No books are on this shelf.');
    await page.locator('.library-empty-state').getByRole('button', { name: 'Library' }).click();
    await expect(page.locator('.book-card').first()).toBeVisible();
  });

  test('Book page renders correctly', async ({ page }) => {
    await page.goto('/');
    const card = page.locator('.book-card', { hasText: 'With Cover Book' });
    await expect(card).toBeVisible();
    const detailHref = await card.locator('.book-title-link').getAttribute('href');
    const bookId = detailHref ? new URL(detailHref, page.url()).pathname.split('/').pop() : '';
    if (!bookId) throw new Error('missing book id');
    const detailRes = await page.request.get(`/api/books/${encodeURIComponent(bookId)}`);
    expect(detailRes.ok()).toBe(true);
    const detail = (await detailRes.json()) as {
      assets: Array<{ id: number; can_read: boolean; is_primary: boolean }>;
    };
    const readableAsset =
      detail.assets.find((asset) => asset.is_primary && asset.can_read) ??
      detail.assets.find((asset) => asset.can_read);
    if (!readableAsset) throw new Error('missing readable asset');
    const positionRes = await page.request.put(`/api/reader/assets/${readableAsset.id}/state`, {
      data: {
        ...(await readerMutationFields(page, readableAsset.id)),
        progress: 0.42,
        locator: {},
      },
    });
    expect(positionRes.ok()).toBe(true);
    const resetStatus = await page.request.put(
      `/api/books/${encodeURIComponent(bookId)}/reading-status`,
      { data: { status: 'unread' } },
    );
    expect(resetStatus.ok()).toBe(true);

    await page.goto(`/book/${bookId}`);

    await expect(page.locator('#book-detail-container')).toBeVisible();

    await expect(page.locator('.detail-title')).toBeVisible();
    await expect(page.locator('.detail-title')).not.toBeFocused();

    const downloadLink = page.locator('.detail-actions a.detail-action[href^="/download/"]');
    await expect(downloadLink.first()).toBeVisible();
    await page
      .locator('.detail-download-group', { hasText: 'EPUB' })
      .locator('.detail-download-menu')
      .click();
    const downloads = page.locator('.floating-menu:not([hidden])');
    for (const label of ['Download EPUB', 'Download Repaired EPUB', 'Download KEPUB']) {
      await expect(downloads.getByRole('menuitem', { name: label, exact: true })).toBeVisible();
    }
    await page.keyboard.press('Escape');

    await expect(page.locator('.detail-authors a').first()).toBeVisible();

    await expect(page.locator('.detail-series a').first()).toBeVisible();

    await expect(page.locator('.detail-description strong')).toBeVisible();
    await expect(page.locator('.detail-description script')).toHaveCount(0);

    await expect(page.locator('.detail-description-more')).toBeHidden();

    await expect(page.locator('.detail-meta').first()).toContainText(
      'Test Publisher · 13 June 2026',
    );

    // The fixture's 3-letter "eng" is normalized to "en" on import and shown as
    // its English display name.
    await expect(page.locator('.detail-meta-top')).toContainText('Language: English');

    // ISBN is visible directly; store identifiers appear as links behind "…".
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
    await expect(page.locator('#sidebar-upload #book-upload-btn')).toBeVisible();

    const readingStatus = page.locator('#btn-reading-status');
    await expect(readingStatus).toBeVisible();
    await expect(readingStatus).toHaveText('Unread');
    await expect(readingStatus.locator('[data-reader-progress-track]')).toBeHidden();
    await readingStatus.click();
    await expect(page.getByRole('menuitem', { name: 'Unread ✓' })).toBeVisible();
    await expect(page.getByRole('menuitem', { name: 'Dropped' })).toBeVisible();
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

    await expect(page.locator('.back-link a')).toHaveAttribute('href', '/');
    await page.locator('.back-link a').click();
    await expect(page.locator('.book-card').first()).toBeVisible();
  });

  test('Series page shows series tiles that open the series in the library', async ({ page }) => {
    const booksRes = await page.request.get(
      `/api/books?q=${encodeURIComponent('series:"Test Series"')}&sort=series`,
    );
    expect(booksRes.ok()).toBe(true);
    const seriesBooks = (await booksRes.json()) as Array<{ id: number }>;
    expect(seriesBooks.length).toBe(4);
    const finished = seriesBooks[0].id;
    const finishRes = await page.request.put(
      `/api/books/${encodeURIComponent(finished)}/reading-status`,
      { data: { status: 'finished' } },
    );
    expect(finishRes.ok()).toBe(true);

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

    await firstRow.locator('.btn-quick-edit').click();
    await expect(page.locator('.modal-backdrop')).toBeVisible();

    // BookSummary omits identifiers, so quick-edit must fetch the full record.
    await expect(page.locator('.modal-backdrop input[name="identifiers"]')).toHaveValue(
      /1234567890/,
    );

    await page.keyboard.press('Escape');
    await expect(page.locator('.modal-backdrop')).toHaveCount(0);

    await expect(table).toBeVisible();

    const batchRow = table.locator('.table-row').first();
    const batchRowID = (await batchRow.getAttribute('data-id')) || '';
    expect(batchRowID).not.toBe('');
    const originalTitle = await batchRow.locator('.table-title-link').innerText();
    const editedTitle = `${originalTitle} Batch Nav`;
    const sequenceRequests: string[] = [];
    page.on('request', (request) => {
      const url = new URL(request.url());
      if (request.method() === 'GET' && url.pathname.endsWith('/sequence')) {
        sequenceRequests.push(url.pathname + url.search);
      }
    });

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

    await page.reload();
    await expect(page.locator('.library-table')).toBeVisible();

    await page.locator('#view-grid-btn').click();
    await expect(page.locator('.book-card').first()).toBeVisible();
  });

  test('Library search has shortcuts and stale-result loading state', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('.book-card').first()).toBeVisible();

    const input = page.locator('#search-input');
    const sort = page.getByRole('button', { name: 'Sort books' });
    await expect(input).toHaveAttribute('aria-keyshortcuts', '/');
    await expect(sort).toContainText('Recently added');

    await page.evaluate(() => {
      if (document.activeElement instanceof HTMLElement) document.activeElement.blur();
    });
    await page.keyboard.press('/');
    await expect(input).toBeFocused();

    const relevanceRequest = page.waitForRequest((request) => {
      const url = new URL(request.url());
      return (
        url.pathname === '/api/books' &&
        url.searchParams.get('q') === 'No Cov' &&
        url.searchParams.get('sort') === 'relevance'
      );
    });
    await page.keyboard.type('No Cov');
    await relevanceRequest;
    await expect(input).toHaveValue('No Cov');
    await expect(page).toHaveURL((url) => url.searchParams.get('q') === 'No Cov');
    await expect(sort).toContainText('Relevance');
    await expect(page.locator('.book-card', { hasText: 'No Cover Book' })).toBeVisible();
    await expect(page.locator('.book-card')).toHaveCount(1);
    await expect(page.locator('#save-search-btn')).toBeVisible();

    await page.keyboard.press('Escape');
    await expect(input).toHaveValue('');
    await expect(sort).toContainText('Recently added');
    await expect(input).toBeFocused();
    await expect(page.locator('#save-search-btn')).toBeHidden();

    await page.keyboard.type('/');
    await expect(input).toHaveValue('/');
    await page.keyboard.press('Escape');
    await expect(input).toHaveValue('');
    await page.keyboard.press('Escape');
    await expect(input).not.toBeFocused();

    const grid = page.locator('#library-grid');
    const cards = page.locator('.book-card');
    await expect(grid).toHaveAttribute('aria-busy', 'false');
    const previousCount = await cards.count();
    expect(previousCount).toBeGreaterThan(1);

    let releaseSearch!: () => void;
    const searchReady = new Promise<void>((resolve) => {
      releaseSearch = resolve;
    });
    await page.route('**/api/books?*', async (route) => {
      if (new URL(route.request().url()).searchParams.get('q') === 'No Cover') await searchReady;
      await route.continue();
    });
    try {
      await input.fill('No Cover');
      await expect(grid).toHaveAttribute('aria-busy', 'true');
      await expect(cards).toHaveCount(previousCount);
    } finally {
      releaseSearch();
    }
    await expect(grid).toHaveAttribute('aria-busy', 'false');
    await expect(cards).toHaveCount(1);
    await expect(cards.first()).toContainText('No Cover Book');
    await expect(page).toHaveURL((url) => url.searchParams.get('q') === 'No Cover');

    await input.fill('');
    await expect(page).toHaveURL((url) => !url.searchParams.has('q'));
    await expect(cards).toHaveCount(previousCount);
  });

  test('Internal cover drags do not trigger book upload', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('.book-card').first()).toBeVisible();

    await page.evaluate(() => {
      const cover = document.querySelector('.book-cover-image');
      if (!cover) throw new Error('missing cover image');

      const transfer = new DataTransfer();
      transfer.items.add(new File(['cover'], 'cover.png', { type: 'image/png' }));

      const dispatchDrag = (target: EventTarget, type: string) => {
        const event = new DragEvent(type, {
          bubbles: true,
          cancelable: true,
          dataTransfer: transfer,
        });
        target.dispatchEvent(event);
      };

      dispatchDrag(cover, 'dragstart');
      dispatchDrag(document, 'dragenter');
      dispatchDrag(document, 'drop');
    });

    await expect(page.locator('.toast')).toHaveCount(0);
    await expect(page.locator('.app-main')).not.toHaveClass(/library-drop-active/);
  });

  test('Authors page opens inline editors and appends the next page', async ({ page }) => {
    const authors = Array.from({ length: 3 }, (_, i) => {
      const n = String(i + 1).padStart(3, '0');
      return {
        name: `Author ${n}`,
        sort_name: `Author ${n}`,
        book_count: i + 1,
      };
    });
    const requests: string[] = [];
    await page.route('**/api/authors/list*', async (route) => {
      const cursor = new URL(route.request().url()).searchParams.get('cursor') || '';
      requests.push(cursor);
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(
          cursor
            ? { items: authors.slice(2) }
            : { items: authors.slice(0, 2), next_cursor: 'authors-page-2' },
        ),
      });
    });

    await page.goto('/authors');
    const rows = page.locator('.authors-table tbody tr');
    await expect(rows).toHaveCount(2);
    const firstRow = rows.first();
    await expect(firstRow).toBeVisible();
    await expect(firstRow.locator('.author-row-count')).toBeVisible();

    await firstRow.locator('.author-name-edit').click();
    await expect(firstRow.locator('.author-edit-input')).toBeVisible();
    await firstRow.locator('.author-cancel-btn').click();
    await expect(firstRow.locator('.author-name-edit')).toBeVisible();

    await firstRow.locator('.author-sort-edit').click();
    await expect(firstRow.locator('.author-edit-input')).toBeVisible();
    await firstRow.locator('.author-cancel-btn').click();
    await expect(firstRow.locator('.author-sort-edit')).toBeVisible();

    await firstRow.locator('.author-actions-btn').click();
    await expect(
      page.locator('.floating-menu .menu-item', { hasText: 'Rename / merge' }),
    ).toBeVisible();
    await page.keyboard.press('Escape');

    const showMore = page.getByRole('button', { name: 'Show more authors' });
    await expect(showMore).toBeVisible();
    await showMore.click();

    await expect(rows).toHaveCount(3);
    await expect(page.locator('.author-row-name', { hasText: 'Author 003' })).toBeVisible();
    await expect(showMore).toBeHidden();
    expect(requests).toEqual(['', 'authors-page-2']);
  });

  test('Series page fetches the next server page on demand', async ({ page }) => {
    const requests: string[] = [];
    // The stubbed series carry a made-up cover book, so serve a pixel for it.
    await page.route(/\/covers\//, async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'image/gif',
        body: Buffer.from('R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw==', 'base64'),
      });
    });
    await page.route(/\/api\/series(?:\?.*)?$/, async (route) => {
      const cursor = new URL(route.request().url()).searchParams.get('cursor') || '';
      requests.push(cursor);
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(
          cursor
            ? { items: [seriesItem('Beta Series', 1)] }
            : {
                items: [seriesItem('Test Series', 4), seriesItem('Alpha Series', 2)],
                next_cursor: 'series-page-2',
              },
        ),
      });
    });

    await page.goto('/series');
    const items = page.locator('.series-card');
    await expect(items).toHaveCount(2);
    await expect(page.getByRole('link', { name: /Beta Series/ })).toHaveCount(0);

    const showMore = page.getByRole('button', { name: 'Show more series' });
    await expect(showMore).toBeVisible();
    await showMore.click();
    await expect(items).toHaveCount(3);
    await expect(page.getByRole('link', { name: /Beta Series/ })).toBeVisible();
    await expect(showMore).toBeHidden();
    expect(requests).toEqual(['', 'series-page-2']);
  });

  test('Sidebar app nav switches top-level pages without full reload', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 360 });
    await page.goto('/');
    const upload = page.locator('#sidebar-upload #book-upload-btn');
    await expect(upload).toBeVisible();
    await expect(page.locator('.book-card').last()).toBeVisible();
    await page.locator('.book-card').last().scrollIntoViewIfNeeded();
    const savedScrollY = await page.evaluate(() => window.scrollY);
    expect(savedScrollY).toBeGreaterThan(0);

    await page.evaluate(
      () =>
        ((window as typeof window & { __polkaNavMarker?: string }).__polkaNavMarker = 'same-doc'),
    );

    await page.locator('#nav-series').click();
    await page.waitForURL((url) => url.pathname === '/series');
    await expect(page.locator('.series-container')).toBeVisible();
    await expect(upload).toBeVisible();
    expect(
      await page.evaluate(
        () => (window as typeof window & { __polkaNavMarker?: string }).__polkaNavMarker,
      ),
    ).toBe('same-doc');

    await page.locator('#nav-authors').click();
    await page.waitForURL((url) => url.pathname === '/authors');
    await expect(page.locator('.authors-container')).toBeVisible();
    expect(
      await page.evaluate(
        () => (window as typeof window & { __polkaNavMarker?: string }).__polkaNavMarker,
      ),
    ).toBe('same-doc');

    await page.goBack();
    await expect(page.locator('.series-container')).toBeVisible();
    // Let the empty catalog's debounced scroll save run before books arrive.
    await page.route('**/api/books?*', async (route) => {
      await new Promise((resolve) => setTimeout(resolve, 500));
      await route.continue();
    });
    await page.goBack();
    await expect(page.locator('#library-grid')).toBeVisible();
    await expect
      .poll(async () => await page.evaluate(() => window.scrollY))
      .toBeGreaterThanOrEqual(savedScrollY - 80);
    expect(
      await page.evaluate(
        () => (window as typeof window & { __polkaNavMarker?: string }).__polkaNavMarker,
      ),
    ).toBe('same-doc');
  });

  test('Table author click filters the search, preserves prefixes, and can be saved', async ({
    page,
  }) => {
    await page.goto('/');
    await page.locator('#view-table-btn').click();
    await expect(page.locator('.library-table')).toBeVisible();

    const author = page.locator('.table-author-link').first();
    const name = ((await author.textContent()) || '').trim();
    expect(name).not.toBe('');
    await author.click();

    await expect(page.locator('#search-input')).toHaveValue(`author:"${name}"`);
    await expect(page.locator('#save-search-btn')).toBeVisible();
    await page.locator('#save-search-btn').click();
    const dialog = page.locator('.settings-submodal');
    await expect(dialog.getByRole('heading', { name: 'Save search' })).toBeVisible();
    await expect(dialog.getByLabel('Name')).toHaveValue(name);
    await expect(dialog.getByLabel('Search query')).toHaveValue(`author:"${name}"`);
    await dialog.getByRole('button', { name: 'Cancel' }).click();

    await page.locator('#search-input').fill('co');
    const selected = page.locator('.table-row', { hasText: 'No Cover Book' });
    await expect(selected).toBeVisible();
    await expect(page.locator('.table-row', { hasText: 'With Cover Book' })).toBeVisible();
    await selected.locator('.table-author-link').click();
    await expect(page.locator('.table-row')).toHaveCount(1);
    await expect(selected).toBeVisible();
  });
});
