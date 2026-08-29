import { Buffer } from 'node:buffer';
import { expect, test } from './fixtures';

function seriesItem(name: string, bookCount: number) {
  return {
    name,
    author: 'Stub Author',
    book_count: bookCount,
    finished_count: 0,
    cover_work_id: 'w_1',
    cover_version: 0,
  };
}

// These observations do not need server-side mutations. They run serially on
// lane B alongside its stateful desktop checks.
test.describe('Polka read-only browser tests', () => {
  test('App bootstraps account and settings on first load', async ({ page }) => {
    const bootstrapRequests: string[] = [];
    page.on('request', (request) => {
      const path = new URL(request.url()).pathname;
      if (path === '/api/me' || path === '/api/settings') bootstrapRequests.push(path);
    });

    await page.goto('/');
    await expect(page.locator('.book-card').first()).toBeVisible();
    await expect(page.getByRole('button', { name: 'Account menu for admin' })).toBeVisible();

    expect(bootstrapRequests).toEqual([]);
  });

  test('Explicit themes override the operating-system color scheme', async ({ page }) => {
    await page.emulateMedia({ colorScheme: 'dark' });
    await page.goto('/');
    const libraryNav = page.locator('#nav-library');
    await expect(libraryNav).toBeVisible();

    const expectColors = async (expected: {
      background: string;
      hover: string;
      danger: string;
    }) => {
      await libraryNav.hover();
      await expect(page.locator('body')).toHaveCSS('background-color', expected.background);
      await expect(libraryNav).toHaveCSS('background-color', expected.hover);
      await expect
        .poll(() =>
          page.evaluate(() =>
            getComputedStyle(document.documentElement).getPropertyValue('--danger').trim(),
          ),
        )
        .toBe(expected.danger);
    };

    await expectColors({
      background: 'rgb(18, 18, 18)',
      hover: 'rgba(255, 255, 255, 0.07)',
      danger: '#ef9a9a',
    });

    await page.evaluate(() => {
      document.documentElement.dataset.theme = 'light';
    });
    await expectColors({
      background: 'rgb(252, 252, 252)',
      hover: 'rgba(0, 0, 0, 0.05)',
      danger: '#c62828',
    });

    await page.evaluate(() => {
      document.documentElement.dataset.theme = 'sepia';
    });
    await expectColors({
      background: 'rgb(244, 239, 230)',
      hover: 'rgba(0, 0, 0, 0.05)',
      danger: '#c62828',
    });

    await page.emulateMedia({ colorScheme: 'light' });
    await page.evaluate(() => {
      document.documentElement.dataset.theme = 'dark';
    });
    await expectColors({
      background: 'rgb(18, 18, 18)',
      hover: 'rgba(255, 255, 255, 0.07)',
      danger: '#ef9a9a',
    });
  });

  test('Library search has shortcuts and stale-result loading state', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('.book-card').first()).toBeVisible();

    const input = page.locator('#search-input');
    await expect(input).toHaveAttribute('aria-keyshortcuts', '/');

    await page.evaluate(() => {
      if (document.activeElement instanceof HTMLElement) document.activeElement.blur();
    });
    await page.keyboard.press('/');
    await expect(input).toBeFocused();

    await page.keyboard.type('No Cover');
    await expect(input).toHaveValue('No Cover');
    await expect(page.locator('#save-search-btn')).toBeVisible();

    await page.keyboard.press('Escape');
    await expect(input).toHaveValue('');
    await expect(input).toBeFocused();
    await expect(page.locator('#save-search-btn')).toBeHidden();

    await page.keyboard.type('/');
    await expect(input).toHaveValue('/');
    await page.keyboard.press('Escape');
    await expect(input).toHaveValue('');
    await page.keyboard.press('Escape');
    await expect(input).not.toBeFocused();

    await page.route('**/api/books**', async (route) => {
      const url = new URL(route.request().url());
      if (url.searchParams.has('q')) {
        await new Promise((resolve) => setTimeout(resolve, 650));
      }
      await route.continue();
    });

    await page.locator('#search-input').fill('No Cover');

    await expect(page.locator('#library-grid')).toHaveAttribute('aria-busy', 'true');
    await expect(page.locator('.book-card', { hasText: 'No Cover Book' })).toBeVisible();

  });

  test('Sidebar upload remains visible across app views', async ({ page }) => {
    await page.goto('/');
    const upload = page.locator('#sidebar-upload #book-upload-btn');
    await expect(upload).toBeVisible();

    await page.getByRole('link', { name: 'Series' }).click();
    await expect(page.locator('.series-container')).toBeVisible();
    await expect(upload).toBeVisible();

    await page.locator('#nav-library').click();
    await expect(page.locator('.book-card').first()).toBeVisible();
    await page.locator('.book-title').first().click();
    await expect(page.locator('.detail-title')).toBeVisible();
    await expect(upload).toBeVisible();

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

  test('Book edit keeps title sort quiet until it differs', async ({ page }) => {
    const authorInfoRequests: string[] = [];
    page.on('request', (request) => {
      if (request.url().includes('/api/authors/info')) authorInfoRequests.push(request.url());
    });

    await page.goto('/');
    const card = page.locator('.book-card', { hasText: 'No Cover Book' });
    await expect(card).toBeVisible();
    await card.locator('.book-title').click();
    await expect(page.locator('.detail-title')).toContainText('No Cover Book');

    await page.locator('#btn-edit-book').click();
    const modal = page.locator('.edit-modal');
    await expect(modal).toBeVisible();
    const formID = await modal.locator('.edit-form').getAttribute('id');
    expect(formID).not.toBeNull();
    const uiID = formID?.replace('edit-book-form-', '') || '';

    const titleInput = modal.locator('input[name="title"]');
    const sortReveal = modal.locator(`#title-sort-reveal-${uiID}`);
    const sortEditor = modal.locator(`#title-sort-editor-${uiID}`);
    const sortNote = modal.locator(`#title-sort-note-${uiID}`);
    const sortInput = modal.locator(`#title-sort-input-${uiID}`);
    const sortAuto = modal.locator(`#title-sort-auto-${uiID}`);
    const sortSame = modal.locator(`#title-sort-same-${uiID}`);

    await expect(titleInput).toHaveValue('No Cover Book');
    await expect(sortReveal).toBeVisible();
    await expect(sortReveal).toHaveText('Sort');
    await expect(sortEditor).toBeHidden();
    await expect(sortNote).toBeHidden();

    await titleInput.fill('No Cover Book Revised');
    await expect(sortReveal).toBeVisible();
    await expect(sortEditor).toBeHidden();
    await expect(sortNote).toBeHidden();
    await expect(modal.locator('.save-indicator')).toContainText('1 unsaved change');

    await titleInput.fill('No Cover Book');
    await expect(sortReveal).toBeVisible();
    await expect(sortEditor).toBeHidden();
    await expect(modal.locator('.edit-save-btn')).toBeDisabled();

    await titleInput.hover();
    await sortReveal.click();
    await expect(sortEditor).toBeVisible();
    await expect(sortInput).toBeVisible();
    await expect(sortInput).toHaveValue('No Cover Book');

    await titleInput.click();
    await titleInput.fill('The No Cover Book');
    await titleInput.hover();
    await sortReveal.click();
    await expect(sortInput).toHaveValue('The No Cover Book');
    await sortAuto.click();
    await expect(sortInput).toHaveValue('No Cover Book, The');
    await expect(modal.locator('.save-indicator')).toContainText('unsaved change');

    await sortInput.fill('Cover Book, No');
    await expect(modal.locator('.save-indicator')).toContainText('unsaved change');
    await titleInput.click();
    await expect(sortEditor).toBeHidden();
    await expect(sortNote).toBeVisible();
    await expect(sortNote).toContainText('sorts as “Cover Book, No”');

    await sortNote.click();
    await expect(sortEditor).toBeVisible();
    await sortSame.click();
    await titleInput.fill('No Cover Book');
    await expect(sortReveal).toBeVisible();
    await expect(sortEditor).toBeHidden();
    await expect(sortNote).toBeHidden();
    await expect(modal.locator('.edit-save-btn')).toBeDisabled();
    expect(authorInfoRequests).toHaveLength(0);

    const modalBox = await modal.boundingBox();
    const titleBox = await titleInput.boundingBox();
    if (!modalBox || !titleBox) throw new Error('missing modal/title box');
    const outsideX = Math.max(8, modalBox.x - 24);
    const outsideY = modalBox.y + 48;
    await page.mouse.move(titleBox.x + titleBox.width / 2, titleBox.y + titleBox.height / 2);
    await page.mouse.down();
    await page.mouse.move(outsideX, outsideY, { steps: 4 });
    await page.mouse.up();
    await expect(modal).toBeVisible();

    await page.mouse.click(outsideX, outsideY);
    await expect(page.locator('.modal-backdrop')).toHaveCount(0);
  });

  test('Book edit opens immediately while the full record loads', async ({ page }) => {
    await page.goto('/');
    const card = page.locator('.book-card', { hasText: 'No Cover Book' });
    await expect(card).toBeVisible();
    await card.locator('.book-title').click();
    await expect(page.locator('.detail-title')).toContainText('No Cover Book');

    await page.route('**/api/books/*', async (route) => {
      if (route.request().method() === 'GET') {
        await new Promise((resolve) => setTimeout(resolve, 650));
      }
      await route.continue();
    });

    await page.locator('#btn-edit-book').click();
    const modal = page.locator('.edit-modal');
    await expect(modal).toBeVisible();
    await expect(modal.locator('.edit-modal-loading-state')).toContainText('Loading book');

    await expect(modal.locator('input[name="title"]')).toHaveValue('No Cover Book');

  });

  test('Metadata fetch handles cover-only and dirty draft edge cases', async ({ page }) => {
    let candidateRequests = 0;
    await page.route(/\/api\/books\/[^/]+\/metadata-candidates\?provider=openlibrary$/, async route => {
      candidateRequests++;
      const firstFetch = [
        {
          provider: 'openlibrary',
          provider_name: 'Cover Provider',
          provider_id: '',
          cover_url: 'https://covers.openlibrary.org/b/id/edge-cover-1-L.jpg?default=false',
        },
        {
          provider: 'openlibrary',
          provider_name: 'Description Provider',
          provider_id: '/works/DESCEDGE',
        },
      ];
      const secondFetch = [
        {
          provider: 'openlibrary',
          provider_name: 'Dirty Provider',
          provider_id: '',
          title: 'Remote Dirty Title',
          cover_url: 'https://covers.openlibrary.org/b/id/edge-cover-2-L.jpg?default=false',
        },
      ];
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(candidateRequests === 1 ? firstFetch : secondFetch),
      });
    });
    await page.route(/https:\/\/covers\.openlibrary\.org\/b\/id\/edge-cover-\d-L\.jpg\?default=false$/, async route => {
      await route.fulfill({
        status: 200,
        contentType: 'image/svg+xml',
        body: '<svg xmlns="http://www.w3.org/2000/svg" width="96" height="144"><rect width="96" height="144" fill="#6f6ab7"/><text x="48" y="72" text-anchor="middle" fill="white" font-size="11">Edge</text></svg>',
      });
    });

    await page.goto('/');
    await page.locator('.book-card', { hasText: 'No Cover Book' }).locator('.book-title').click();
    await expect(page.locator('.detail-title')).toContainText('No Cover Book');
    await page.locator('#btn-edit-book').click();
    await expect(page.locator('.edit-modal')).toBeVisible();

    await page.locator('.edit-modal .metadata-fetch-action').click();
    await expect(page.locator('.metadata-modal')).toBeVisible();
    await page.locator('.metadata-modal .metadata-fetch-action', { hasText: 'Fetch' }).click();

    const coverCandidate = page.locator('.metadata-candidate', { hasText: 'Cover Provider' });
    await expect(coverCandidate).toBeVisible();
    await expect(coverCandidate.locator('.metadata-candidate-fields')).toContainText('Cover');
    await expect(coverCandidate.locator('.metadata-candidate-impact')).toContainText('adds cover');
    await expect(coverCandidate.locator('.metadata-fill-btn')).toHaveText('Use cover');

    const descriptionCandidate = page.locator('.metadata-candidate', {
      hasText: 'Description Provider',
    });
    await expect(descriptionCandidate.locator('.metadata-candidate-fields')).toContainText(
      'Description',
    );
    await expect(descriptionCandidate.locator('.metadata-candidate-impact')).toContainText(
      'would replace 1 field',
    );

    await coverCandidate.locator('.metadata-fill-btn').click();
    await expect(page.locator('.metadata-modal')).toHaveCount(0);
    await expect(page.locator('.edit-cover-container')).toHaveClass(/is-fetched/);
    await expect(page.locator('.edit-cover-revert')).toBeVisible();

    const titleInput = page.locator('.edit-modal input[name="title"]');
    await titleInput.fill('Manual No Cover Title');
    await page.locator('.edit-modal .metadata-fetch-action').click();
    await expect(page.locator('.metadata-modal')).toBeVisible();
    await page.locator('.metadata-modal .metadata-fetch-action', { hasText: 'Fetch' }).click();

    const dirtyCandidate = page.locator('.metadata-candidate', { hasText: 'Dirty Provider' });
    await expect(dirtyCandidate.locator('.metadata-candidate-impact')).toContainText(
      'skips 1 edited field',
    );
    await expect(dirtyCandidate.locator('.metadata-candidate-impact')).toContainText(
      'keeps selected cover',
    );
    await expect(dirtyCandidate.locator('.metadata-noop-btn')).toHaveText('No changes');
    await expect(dirtyCandidate.locator('.metadata-fill-btn')).toHaveCount(0);
    await expect(dirtyCandidate.locator('.metadata-replace-btn')).toHaveCount(0);
    expect(candidateRequests).toBe(2);
  });

  test('Metadata fetch ignores stale provider responses', async ({ page }) => {
    let openLibraryRequests = 0;
    let googleRequests = 0;
    let resolveOpenLibrarySettled: () => void = () => {};
    const openLibrarySettled = new Promise<void>((resolve) => {
      resolveOpenLibrarySettled = resolve;
    });
    await page.route(/\/api\/books\/[^/]+\/metadata-candidates\?provider=openlibrary$/, async route => {
      openLibraryRequests++;
      await new Promise((resolve) => setTimeout(resolve, 500));
      try {
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify([
            {
              provider: 'openlibrary',
              provider_name: 'Open Library',
              provider_id: '',
              title: 'Stale Open Library Title',
            },
          ]),
        });
      } catch {
        // The browser may abort the stale request before Playwright fulfills it.
      } finally {
        resolveOpenLibrarySettled();
      }
    });
    await page.route(/\/api\/books\/[^/]+\/metadata-candidates\?provider=google$/, async route => {
      googleRequests++;
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([
          {
            provider: 'google',
            provider_name: 'Google Books',
            provider_id: '',
            title: 'Fresh Google Title',
          },
        ]),
      });
    });

    await page.goto('/');
    await page.locator('.book-card').first().locator('.book-title').click();
    await expect(page.locator('.detail-title')).toBeVisible();
    await page.locator('#btn-edit-book').click();
    await expect(page.locator('.edit-modal')).toBeVisible();

    await page.locator('.edit-modal .metadata-fetch-action').click();
    await expect(page.locator('.metadata-modal')).toBeVisible();
    const fetchBtn = page.locator('.metadata-modal .metadata-fetch-action', { hasText: 'Fetch' });
    await fetchBtn.click();
    await expect(page.locator('.metadata-status')).toContainText('Loading candidates');

    await page.locator('.metadata-provider-select').click();
    await page.getByRole('option', { name: 'Google Books' }).click();
    await expect(page.locator('.metadata-status')).toContainText('Choose a provider');
    await expect(fetchBtn).toBeEnabled();
    await fetchBtn.click();

    await expect(page.locator('.metadata-candidate', { hasText: 'Fresh Google Title' })).toBeVisible();
    await expect(page.locator('.metadata-status')).toContainText('1 candidate found.');
    await openLibrarySettled;
    await expect(page.locator('.metadata-candidate', { hasText: 'Stale Open Library Title' })).toHaveCount(0);
    expect(openLibraryRequests).toBe(1);
    expect(googleRequests).toBe(1);
  });

  test('Authors page renders table and opens inline editors', async ({ page }) => {
    await page.goto('/authors');

    await expect(page.locator('.authors-container')).toBeVisible();

    const table = page.locator('.authors-table');
    await expect(table).toBeVisible();
    const firstRow = table.locator('tbody tr').first();
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
    await expect(page.locator('.floating-menu .menu-item', { hasText: 'Rename / merge' })).toBeVisible();
    await page.keyboard.press('Escape');


    await page.screenshot({ path: 'screenshots/authors.png', fullPage: true });
  });

  test('Authors page fetches the next server page on demand', async ({ page }) => {
    const authors = Array.from({ length: 205 }, (_, i) => {
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
            ? { items: authors.slice(200) }
            : { items: authors.slice(0, 200), next_cursor: 'authors-page-2' },
        ),
      });
    });

    await page.goto('/authors');
    const rows = page.locator('.authors-table tbody tr');
    await expect(rows).toHaveCount(200);
    await expect(page.locator('.author-row-name', { hasText: 'Author 200' })).toBeVisible();
    await expect(page.locator('.author-row-name', { hasText: 'Author 201' })).toHaveCount(0);

    const showMore = page.getByRole('button', { name: 'Show more authors' });
    await expect(showMore).toBeVisible();
    await showMore.click();

    await expect(rows).toHaveCount(205);
    await expect(page.locator('.author-row-name', { hasText: 'Author 205' })).toBeVisible();
    await expect(showMore).toBeHidden();
    expect(requests).toEqual(['', 'authors-page-2']);
  });

  test('Series page fetches the next server page on demand', async ({ page }) => {
    const requests: string[] = [];
    // The stubbed series carry a made-up cover work, so serve a pixel for it.
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
    await expect(page.locator('.book-card').last()).toBeVisible();
    await page.locator('.book-card').last().scrollIntoViewIfNeeded();
    const savedScrollY = await page.evaluate(() => window.scrollY);
    expect(savedScrollY).toBeGreaterThan(0);

    await page.evaluate(() => ((window as typeof window & { __polkaNavMarker?: string }).__polkaNavMarker = 'same-doc'));

    await page.locator('#nav-series').click();
    await page.waitForURL((url) => url.pathname === '/series');
    await expect(page.locator('.series-container')).toBeVisible();
    expect(await page.evaluate(() => (window as typeof window & { __polkaNavMarker?: string }).__polkaNavMarker)).toBe(
      'same-doc',
    );

    await page.locator('#nav-authors').click();
    await page.waitForURL((url) => url.pathname === '/authors');
    await expect(page.locator('.authors-container')).toBeVisible();
    expect(await page.evaluate(() => (window as typeof window & { __polkaNavMarker?: string }).__polkaNavMarker)).toBe(
      'same-doc',
    );

    await page.goBack();
    await expect(page.locator('.series-container')).toBeVisible();
    await page.goBack();
    await expect(page.locator('#library-grid')).toBeVisible();
    await expect.poll(async () => await page.evaluate(() => window.scrollY)).toBeGreaterThanOrEqual(savedScrollY - 80);
    expect(await page.evaluate(() => (window as typeof window & { __polkaNavMarker?: string }).__polkaNavMarker)).toBe(
      'same-doc',
    );

  });

  test('Returning from a book restores the list position', async ({ page }) => {
    // A short viewport makes the small fixture library scroll at all.
    await page.setViewportSize({ width: 1280, height: 360 });
    await page.goto('/');
    const card = page.locator('.book-card').last();
    await expect(card).toBeVisible();
    await card.scrollIntoViewIfNeeded();
    const scrolled = await page.evaluate(() => window.scrollY);
    expect(scrolled).toBeGreaterThan(0);

    // Opening the book saves the position itself, so this does not depend on
    // the debounced scroll listener having fired.
    await card.locator('.book-title-link').click();
    await expect(page.locator('.detail-title')).toBeVisible();

    // The in-page Back control returns through history rather than re-entering
    // the list at the top, so the position comes back with it.
    const entries = await page.evaluate(() => history.length);
    await page.locator('.back-link a').click();
    await expect(page.locator('.book-card').first()).toBeVisible();
    await expect.poll(async () => await page.evaluate(() => window.scrollY)).toBe(scrolled);
    expect(await page.evaluate(() => history.length)).toBe(entries);
  });

  test('Table author click filters the search and reveals save-search', async ({ page }) => {
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

  });
});
