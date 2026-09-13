import { Buffer } from 'node:buffer';
import { expect, type Locator, test } from './fixtures';
import { findBook } from './helpers';

async function expectImageLoaded(img: Locator): Promise<void> {
  await expect
    .poll(async () => {
      return await img.evaluate((i: HTMLImageElement) => i.complete && i.naturalWidth > 0);
    })
    .toBe(true);
}

test.describe('Book editor', () => {
  test('Book edit stages primary author sort through Save', async ({ page }) => {
    const book = await findBook(page, 'No Cover Book');
    await page.goto(`/book/${book.id}`);

    await page.locator('#btn-edit-book').click();
    const modal = page.locator('.edit-modal');
    await expect(modal).toBeVisible();

    const formID = await modal.locator('.edit-form').getAttribute('id');
    expect(formID).not.toBeNull();
    const uiID = formID?.replace('edit-book-form-', '') || '';
    const authorsInput = modal.locator('input[name="authors"]');
    const authorSortReveal = modal.locator(`#author-sort-reveal-${uiID}`);
    const authorSortEditor = modal.locator(`#author-sort-editor-${uiID}`);
    const authorSortInput = modal.locator(`#author-sort-input-${uiID}`);
    const authorSortHint = modal.locator(`#author-sort-hint-${uiID}`);

    await expect(authorSortReveal).toBeVisible();
    await expect(authorSortReveal).toHaveText('Sort');

    await authorsInput.hover();
    await authorSortReveal.click();
    await expect(authorSortEditor).toBeVisible();
    await expect(authorSortInput).toBeVisible();
    await expect(authorSortInput).toHaveValue('Author, Test');
    await expect(authorSortHint).toHaveText('');

    await authorSortInput.fill('Author Sort, Test');
    await expect(modal.locator('.edit-save-btn')).toBeEnabled();
    await modal.locator('.edit-save-btn').click();

    await expect
      .poll(async () => {
        return await page.evaluate(async () => {
          const res = await fetch('/api/authors/info?name=Test%20Author');
          if (!res.ok) return '';
          return (await res.json()).sort_name;
        });
      })
      .toBe('Author Sort, Test');
  });

  test('Book edit PATCH sends only dirty fields and preserves a concurrent change', async ({
    page,
  }) => {
    await page.goto('/');
    const titleLink = page.getByRole('link', { name: 'Foundation', exact: true });
    await expect(titleLink).toBeVisible();
    await titleLink.click();
    await expect(page.locator('.detail-title')).toHaveText('Foundation');

    const bookID = Number(new URL(page.url()).pathname.split('/').pop());
    if (!bookID) throw new Error('missing book id');
    const concurrentPublisher = 'Concurrent Press';

    await page.locator('#btn-edit-book').click();
    const modal = page.locator('.edit-modal');
    const authorsInput = modal.locator('input[name="authors"]');
    await expect(authorsInput).not.toHaveValue('');

    // Change a field behind the already-open form, then save a different field
    // from that stale draft. The form must not echo its old publisher value.
    await page.evaluate(
      async ({ id, publisher }) => {
        const res = await fetch(`/api/books/${id}`, {
          method: 'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ publisher }),
        });
        if (!res.ok) throw new Error(await res.text());
      },
      { id: bookID, publisher: concurrentPublisher },
    );

    const patchRequest = page.waitForRequest((request) => {
      const url = new URL(request.url());
      return request.method() === 'PATCH' && url.pathname === `/api/books/${bookID}`;
    });
    await authorsInput.fill('');
    await modal.locator('.edit-save-btn').click();
    expect((await patchRequest).postDataJSON()).toEqual({ authors: null });
    await expect(modal.locator('.save-indicator')).toContainText('Saved');

    await expect
      .poll(async () => {
        const response = await page.request.get(`/api/books/${bookID}`);
        const book = await response.json();
        return { publisher: book.publisher, authors: book.authors_list };
      })
      .toEqual({ publisher: concurrentPublisher, authors: [] });
    await page.keyboard.press('Escape');
    await expect(modal).not.toBeVisible();
  });

  test('Book edit reuses selected autocomplete author sort name', async ({ page }) => {
    await page.goto('/');
    await page.evaluate(async () => {
      const res = await fetch('/api/authors/sort-name', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name: 'Test Author', sort_name: 'Author Custom, Test' }),
      });
      if (!res.ok) throw new Error(await res.text());
    });

    const card = page.locator('.book-card', { hasText: 'With Cover Book' });
    await expect(card).toBeVisible();
    await card.locator('.book-title').click();
    await expect(page.locator('.detail-title')).toContainText('With Cover Book');

    await page.locator('#btn-edit-book').click();
    const modal = page.locator('.edit-modal');
    await expect(modal).toBeVisible();

    const formID = await modal.locator('.edit-form').getAttribute('id');
    expect(formID).not.toBeNull();
    const uiID = formID?.replace('edit-book-form-', '') || '';
    const authorsInput = modal.locator('input[name="authors"]');
    const authorSortNote = modal.locator(`#author-sort-note-${uiID}`);
    const authorSortEditor = modal.locator(`#author-sort-editor-${uiID}`);
    const authorSortInput = modal.locator(`#author-sort-input-${uiID}`);

    await authorsInput.fill('Test');
    const suggestion = page.locator('.text-list-ac-item', { hasText: 'Test Author' }).first();
    await expect(suggestion).toBeVisible();
    await suggestion.click();

    await expect(authorsInput).toHaveValue('Test Author');
    await expect(authorSortNote).toBeVisible();
    await expect(authorSortNote).toContainText('Author Custom, Test');
    await authorSortNote.click();
    await expect(authorSortEditor).toBeVisible();
    await expect(authorSortInput).toHaveValue('Author Custom, Test');
    await expect(modal.locator('.save-indicator')).toContainText('1 unsaved change');
  });

  test('Book detail edit Next switches inside the same modal', async ({ page }) => {
    await page.goto('/');
    const firstCard = page.locator('.book-card').first();
    await expect(firstCard).toBeVisible();

    const href = await firstCard.locator('.book-title-link').getAttribute('href');
    if (!href) throw new Error('missing book href');
    await page.goto(href);
    await expect(page.locator('.detail-title')).toBeVisible();

    const currentURL = new URL(page.url());
    const initialID = currentURL.pathname.split('/').pop() || '';
    expect(initialID).not.toBe('');
    const contextQuery = currentURL.searchParams.toString();
    const sequence = await page.evaluate(
      async ({ id, query }) => {
        const res = await fetch(`/api/books/${id}/sequence?${query}&before=0&after=1`);
        if (!res.ok) throw new Error(await res.text());
        return await res.json();
      },
      { id: initialID, query: contextQuery },
    );
    const nextBook = sequence.items[sequence.current_index + 1];
    expect(nextBook).toBeTruthy();

    await page.locator('#btn-edit-book').click();
    await expect(page.locator('.edit-modal')).toBeVisible();
    const form = page.locator('.edit-form');
    const formID = await form.getAttribute('id');
    expect(formID).not.toBeNull();
    const readerStateRequests: string[] = [];
    page.on('request', (request) => {
      const path = new URL(request.url()).pathname;
      if (path.startsWith('/api/reader/assets/') && path.endsWith('/state')) {
        readerStateRequests.push(path);
      }
    });
    await page.evaluate(() => {
      document.querySelector('.modal-backdrop')?.setAttribute('data-stable-test', 'same');
    });
    const nextButton = page.locator('.edit-modal button[id^="btn-edit-next-"]');
    await expect(nextButton).toBeEnabled();
    let releaseBook!: () => void;
    const bookReady = new Promise<void>((resolve) => {
      releaseBook = resolve;
    });
    let nextAssetID = 0;
    await page.route(`**/api/books/${nextBook.id}`, async (route) => {
      await bookReady;
      const response = await route.fetch();
      const book = await response.json();
      const primary = book.assets.find((asset: { is_primary: boolean }) => asset.is_primary);
      nextAssetID = primary.id;
      delete primary.page_count;
      await route.fulfill({ response, json: book });
    });
    await page.route(`**/api/books/${nextBook.id}/page-count`, async (route) => {
      await route.fulfill({ json: { asset_id: nextAssetID, page_count: 4205 } });
    });
    try {
      await nextButton.click();
      await expect(page.locator('.edit-form-loading-overlay')).toBeVisible();
      await expect(page.locator('.edit-modal .save-indicator')).not.toContainText('Loading');
    } finally {
      releaseBook();
    }
    await expect(page.locator('.edit-modal input[name="title"]')).toHaveValue(nextBook.title);
    await expect(page.locator('.detail-title')).toContainText(nextBook.title);
    expect(readerStateRequests).toEqual([]);
    await expect(form).toHaveAttribute('id', formID || '');
    await expect(page.locator('.modal-backdrop')).toHaveAttribute('data-stable-test', 'same');
    await expect.poll(() => new URL(page.url()).pathname).toBe(`/book/${nextBook.id}`);
    await expect.poll(() => new URL(page.url()).searchParams.get('from')).toBe('library');

    await page.keyboard.press('Escape');
    await expect(page.locator('.modal-backdrop')).toHaveCount(0);
    await expect(page.locator('.detail-page-count')).toHaveText(/^(≈ )?4205 pages$/);
  });

  test('Edit cover upload is staged and can retry after metadata saves', async ({
    page,
    browserErrors,
  }) => {
    browserErrors.allow(
      (message) => message.includes('/cover]') && message.includes('503 (Service Unavailable)'),
    );
    let uploadRequests = 0;
    let metadataRequests = 0;
    page.on('request', (request) => {
      if (request.method() === 'PATCH' && /\/api\/books\/[^/]+$/.test(request.url()))
        metadataRequests++;
    });
    const pendingCover = {
      name: 'pending-cover.png',
      mimeType: 'image/png',
      buffer: Buffer.from(
        'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR42mP4//8/AAX+Av7czFnnAAAAAElFTkSuQmCC',
        'base64',
      ),
    };

    await page.route(/\/api\/books\/[^/]+\/cover$/, async (route) => {
      if (route.request().method() === 'POST') uploadRequests++;
      if (uploadRequests === 1) {
        await route.fulfill({
          status: 503,
          contentType: 'text/plain',
          body: 'Cover storage unavailable',
        });
        return;
      }
      const reqURL = new URL(route.request().url());
      const bookPath = reqURL.pathname.replace(/\/cover$/, '');
      const bookRes = await page.request.get(`${reqURL.origin}${bookPath}`);
      const book = await bookRes.json();
      book.has_cover = true;
      book.cover_version = (book.cover_version || 0) + 1;

      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(book),
      });
    });
    const book = await findBook(page, 'No Cover Book');
    await page.goto(`/book/${book.id}`);

    await page.locator('#btn-edit-book').click();
    await expect(page.locator('.edit-modal')).toBeVisible();

    const uploadInput = page.locator('input[id^="edit-cover-upload-"]');
    const coverContainer = page.locator('.edit-cover-container');
    await coverContainer.click();
    const coverPicker = page.locator('.cover-picker-modal');
    await uploadInput.setInputFiles(pendingCover);

    expect(uploadRequests).toBe(0);
    await expect(coverPicker).toBeVisible();
    await expect(coverPicker.locator('.cover-picker-primary img')).toHaveAttribute('src', /^blob:/);
    await expect(coverPicker.locator('.cover-picker-reference')).toBeVisible();
    await expect(coverContainer).toHaveClass(/is-dirty/);
    await expect(coverContainer).not.toHaveClass(/is-fetched/);
    await expect(coverContainer.locator('img')).toHaveAttribute('src', /^blob:/);
    await expect(page.locator('.edit-cover-revert')).toBeVisible();
    await expect(page.locator('.edit-modal .save-indicator')).toContainText('unsaved change');
    await expect(page.locator('.edit-modal .edit-save-btn')).toBeEnabled();

    await coverPicker.getByRole('button', { name: 'Use saved cover' }).click();
    await expect(coverContainer).not.toHaveClass(/is-dirty/);
    await expect(page.locator('.edit-cover-revert')).toBeHidden();
    expect(uploadRequests).toBe(0);

    await uploadInput.setInputFiles(pendingCover);
    await coverPicker.getByLabel('Close').click();
    const publisherInput = page.locator('.edit-modal input[name="publisher"]');
    await publisherInput.fill('Cover Retry Press');
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(page.locator('.toast:not(.toast-leaving) .toast-text')).toHaveText(
      'Cover save failed: Cover storage unavailable',
    );
    await expect(publisherInput).toHaveValue('Cover Retry Press');
    await expect(page.locator('.edit-modal .save-indicator')).toContainText('1 unsaved change');
    await expect(coverContainer).toHaveClass(/is-dirty/);
    await expect(coverContainer.locator('img')).toHaveAttribute('src', /^blob:/);
    expect(metadataRequests).toBe(1);

    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(page.locator('.edit-modal .save-indicator')).toContainText('Saved');
    await expect(coverContainer).not.toHaveClass(/is-dirty/);
    await expect(page.locator('.edit-cover-revert')).toBeHidden();
    expect(uploadRequests).toBe(2);
    expect(metadataRequests).toBe(1);
  });

  test('Generated cover is staged until Save', async ({ page }) => {
    let previewRequests = 0;
    let uploadRequests = 0;
    let countStoredCoverRequests = false;
    let storedCoverRequests = 0;
    const previewPayloads: Array<{ title: string; author: string; seed?: number; style?: string }> =
      [];
    const sortPreviewPayloads = (
      items: Array<{ title: string; author: string; seed?: number; style?: string }>,
    ) => [...items].sort((a, b) => (a.style || '').localeCompare(b.style || ''));
    const generatedCover =
      '<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"><rect width="1" height="1" fill="#d66a4a"/></svg>';

    await page.route(/\/api\/books\/[^/]+\/cover-generated-preview$/, async (route) => {
      previewRequests++;
      previewPayloads.push(
        route.request().postDataJSON() as {
          title: string;
          author: string;
          seed?: number;
          style?: string;
        },
      );
      await route.fulfill({
        status: 200,
        contentType: 'image/svg+xml',
        body: generatedCover,
      });
    });
    await page.route(/\/api\/books\/[^/]+\/cover$/, async (route) => {
      if (route.request().method() === 'POST') uploadRequests++;
      const reqURL = new URL(route.request().url());
      const bookPath = reqURL.pathname.replace(/\/cover$/, '');
      const bookRes = await page.request.get(`${reqURL.origin}${bookPath}`);
      const book = await bookRes.json();
      book.has_cover = true;
      book.cover_version = (book.cover_version || 0) + 1;

      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(book),
      });
    });
    await page.route(/\/covers\/[^/]+(?:\?.*)?$/, async (route) => {
      if (countStoredCoverRequests && route.request().resourceType() === 'image') {
        storedCoverRequests++;
      }
      await route.continue();
    });

    const book = await findBook(page, 'With Cover Book');
    await page.goto(`/book/${book.id}`);

    await page.locator('#btn-edit-book').click();
    await expect(page.locator('.edit-modal')).toBeVisible();

    const title = await page.locator('.edit-modal input[name="title"]').inputValue();
    const author = (await page.locator('.edit-modal input[name="authors"]').inputValue())
      .split(';')[0]
      .trim();
    const coverContainer = page.locator('.edit-cover-container');

    await page.locator('.edit-modal').getByRole('button', { name: 'Change cover' }).click();
    const coverPicker = page.locator('.cover-picker-modal');
    await coverPicker.getByRole('button', { name: 'Generate', exact: true }).click();
    await expect(coverPicker).toBeVisible();
    await expect(coverPicker.locator('.cover-picker-primary img')).toHaveAttribute('src', /^blob:/);
    await expectImageLoaded(coverPicker.locator('.cover-picker-primary img'));
    await expect(coverPicker.locator('.cover-picker-reference')).toBeVisible();
    await expect(coverPicker.getByRole('button', { name: 'Use saved cover' })).toBeVisible();
    await expectImageLoaded(coverPicker.locator('.cover-picker-reference img'));
    await expect(coverPicker.locator('.cover-picker-variant')).toHaveCount(4);
    await expect(coverPicker.locator('.cover-picker-variant-image')).toHaveCount(4);
    await expect(coverContainer).toHaveClass(/is-dirty/);
    await expect(coverContainer).not.toHaveClass(/is-fetched/);
    await expect(coverContainer.locator('img')).toHaveAttribute('src', /^blob:/);
    await expect(page.locator('.edit-cover-revert')).toBeVisible();
    await expect(page.locator('.edit-modal .save-indicator')).toContainText('unsaved change');
    expect(previewRequests).toBe(4);
    expect(sortPreviewPayloads(previewPayloads)).toEqual([
      { title, author, seed: 1, style: 'bands' },
      { title, author, seed: 1, style: 'classic' },
      { title, author, seed: 1, style: 'label' },
      { title, author, seed: 1, style: 'quiet' },
    ]);
    await expect(coverPicker.getByRole('button', { name: 'Generate', exact: true })).toBeVisible();
    countStoredCoverRequests = true;
    const labelVariant = coverPicker.locator('[data-cover-style="label"]');
    await labelVariant.click();
    await expect(labelVariant).toHaveClass(/is-selected/);
    await expectImageLoaded(coverPicker.locator('.cover-picker-primary img'));
    expect(storedCoverRequests).toBe(0);
    expect(uploadRequests).toBe(0);

    await coverPicker.getByRole('button', { name: 'Use saved cover' }).click();
    await expect(coverContainer).not.toHaveClass(/is-dirty/);
    await expect(page.locator('.edit-cover-revert')).toBeHidden();
    await expect(coverPicker.locator('.cover-picker-reference')).toBeVisible();
    await expect(coverPicker.getByRole('button', { name: 'Use saved cover' })).toHaveClass(
      /is-selected/,
    );
    await expect(
      coverPicker.locator('.cover-picker-primary .cover-picker-preview-label'),
    ).toHaveText('Current');
    await expect(coverPicker.locator('.cover-picker-variant')).toHaveCount(4);
    await expect(coverPicker.getByRole('button', { name: 'Generate', exact: true })).toBeVisible();
    expect(uploadRequests).toBe(0);

    await coverPicker.getByRole('button', { name: 'Generate', exact: true }).click();
    await coverPicker.getByLabel('Close').click();
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(page.locator('.edit-modal .save-indicator')).toContainText('Saved');
    await expect(coverContainer).not.toHaveClass(/is-dirty/);
    await expect(page.locator('.edit-cover-revert')).toBeHidden();
    expect(previewRequests).toBe(8);
    expect(sortPreviewPayloads(previewPayloads.slice(4))).toEqual([
      { title, author, seed: 2, style: 'bands' },
      { title, author, seed: 2, style: 'classic' },
      { title, author, seed: 2, style: 'label' },
      { title, author, seed: 2, style: 'quiet' },
    ]);
    expect(uploadRequests).toBe(1);
  });

  test('Web cover search recovers from an error and stages the choice until Save', async ({
    page,
    browserErrors,
  }) => {
    browserErrors.allow(
      (message) => message.includes('/cover-search?') && message.includes('502 (Bad Gateway)'),
    );
    let searchRequests = 0;
    let applyRequests = 0;
    let appliedToken = '';
    const searchQueries: Array<{ title: string | null; author: string | null }> = [];
    const previewSVG = (fill: string) =>
      `<svg xmlns="http://www.w3.org/2000/svg" width="120" height="180"><rect width="120" height="180" fill="${fill}"/></svg>`;

    await page.route(
      /\/api\/books\/[^/]+\/cover-search\/preview\?token=web-token-\d+$/,
      async (route) => {
        const token = new URL(route.request().url()).searchParams.get('token');
        await route.fulfill({
          status: 200,
          contentType: 'image/svg+xml',
          body: previewSVG(token === 'web-token-1' ? '#5c8f9f' : '#c47a45'),
        });
      },
    );
    await page.route(/\/api\/books\/[^/]+\/cover-search(?:\?.*)?$/, async (route) => {
      const reqURL = new URL(route.request().url());
      if (route.request().method() === 'GET') {
        searchRequests++;
        if (searchRequests === 1) {
          await route.fulfill({
            status: 502,
            contentType: 'text/plain',
            body: 'Cover provider unavailable',
          });
          return;
        }
        searchQueries.push({
          title: reqURL.searchParams.get('title'),
          author: reqURL.searchParams.get('author'),
        });
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify([
            {
              token: 'web-token-1',
              preview_url: `${reqURL.origin}${reqURL.pathname}/preview?token=web-token-1`,
              source: 'Goodreads',
              width: 600,
              height: 900,
            },
            {
              token: 'web-token-2',
              preview_url: `${reqURL.origin}${reqURL.pathname}/preview?token=web-token-2`,
              source: 'covers.example',
              width: 700,
              height: 1050,
            },
          ]),
        });
        return;
      }

      applyRequests++;
      appliedToken = (route.request().postDataJSON() as { token: string }).token;
      const bookPath = reqURL.pathname.replace('/cover-search', '');
      const bookRes = await page.request.get(`${reqURL.origin}${bookPath}`);
      const book = await bookRes.json();
      book.has_cover = true;
      book.cover_version = (book.cover_version || 0) + 1;
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(book),
      });
    });

    const book = await findBook(page, 'With Cover Book');
    await page.goto(`/book/${book.id}`);

    await page.locator('#btn-edit-book').click();
    await expect(page.locator('.edit-modal')).toBeVisible();

    const coverContainer = page.locator('.edit-cover-container');
    const title = await page.locator('.edit-modal input[name="title"]').inputValue();
    const author = (await page.locator('.edit-modal input[name="authors"]').inputValue())
      .split(';')[0]
      .trim();
    await page.locator('.edit-modal').getByRole('button', { name: 'Find cover online' }).click();
    const searchModal = page.locator('.cover-search-modal');
    await expect(searchModal).toBeVisible();
    await expect(searchModal.getByLabel('Title')).toHaveValue(title);
    await expect(searchModal.getByLabel('Author')).toHaveValue(author);

    await expect(page.locator('.toast:not(.toast-leaving) .toast-text')).toHaveText(
      'Cover search failed: Cover provider unavailable',
    );
    await searchModal.getByRole('button', { name: 'Search', exact: true }).click();
    await expect(searchModal.locator('.cover-search-result')).toHaveCount(2);
    await expect(searchModal.getByRole('button', { name: 'Search' })).toBeEnabled();
    await expect(searchModal.locator('.cover-search-result').first()).toContainText('Goodreads');
    await searchModal.getByRole('button', { name: 'Use cover 1 from Goodreads' }).click();

    expect(searchRequests).toBe(2);
    expect(searchQueries).toEqual([{ title, author }]);
    expect(applyRequests).toBe(0);
    await expect(searchModal).toHaveCount(0);
    await expect(coverContainer).toHaveClass(/is-dirty/);
    await expect(coverContainer).not.toHaveClass(/is-fetched/);
    await expect(coverContainer.locator('img')).toHaveAttribute(
      'src',
      /\/api\/books\/[^/]+\/cover-search\/preview\?token=web-token-1$/,
    );
    await expectImageLoaded(coverContainer.locator('img'));
    await expect(page.locator('.edit-cover-revert')).toBeVisible();
    await expect(page.locator('.edit-modal .save-indicator')).toContainText('unsaved change');

    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(page.locator('.edit-modal .save-indicator')).toContainText('Saved');
    await expect(coverContainer).not.toHaveClass(/is-dirty/);
    await expect(page.locator('.edit-cover-revert')).toBeHidden();
    expect(applyRequests).toBe(1);
    expect(appliedToken).toBe('web-token-1');
  });

  test('Edit form renders cover and normalizes helper fields', async ({ page }) => {
    const book = await findBook(page, 'With Cover Book');
    await page.goto(`/book/${book.id}`);

    await page.locator('#btn-edit-book').click();
    const coverContainer = page.locator('.edit-cover-container');
    await expect(coverContainer).toBeVisible();

    const img = coverContainer.locator('img');
    await expect(img).toHaveCount(1);
    await expectImageLoaded(img);

    // A loaded edit cover is image-only; no inline placeholder text should sit beside it.
    const containerText = (await coverContainer.textContent())?.trim() ?? '';
    expect(containerText).toBe('');

    // A recognized ISO date is displayed in human form without a warning.
    const dateInput = page.locator('[id^="date-input-"]');
    await expect(dateInput).toHaveValue('13 June 2026');
    const dateHint = page.locator('[id^="date-validation-"]');
    await expect(dateHint).not.toContainText('Unrecognized');

    await page.locator('.date-picker-trigger').click();
    const datePicker = page.locator('.date-picker-popover');
    await expect(datePicker).toBeVisible();
    await expect(datePicker.getByRole('button', { name: 'Day' })).toHaveAttribute(
      'aria-pressed',
      'true',
    );
    await expect(datePicker.locator('.date-picker-current')).toHaveText('June 2026');
    await expect(datePicker.getByRole('button', { name: '13' })).toHaveClass(/is-active/);
    await datePicker.getByRole('button', { name: 'Month', exact: true }).click();
    await expect(datePicker.getByRole('button', { name: 'Month', exact: true })).toHaveAttribute(
      'aria-pressed',
      'true',
    );
    await expect(datePicker.locator('.date-picker-current')).toHaveText('2026');
    await datePicker.getByRole('button', { name: 'June' }).click();
    await expect(dateInput).toHaveValue('June 2026');
    await expect(datePicker).toBeHidden();
    await expect(page.locator('.save-indicator')).toContainText('unsaved change');
    await expect(page.getByRole('button', { name: 'Save', exact: true })).toBeEnabled();

    const authorsInput = page.locator('.edit-modal input[name="authors"]');
    await expect(authorsInput).toHaveValue('Cover Author');
    await authorsInput.fill('Le Guin, Ursula K.; Cover');
    const authorSuggestions = page.locator('.author-list-input .text-list-ac-list');
    await expect(authorSuggestions).toBeVisible();
    await expect(authorSuggestions.getByRole('option', { name: 'Cover Author' })).toBeVisible();
    await page.keyboard.press('Enter');
    await expect(authorsInput).toHaveValue('Le Guin, Ursula K.; Cover Author');

    const tagsInput = page.locator('.edit-modal input[name="tags"]');
    await tagsInput.fill('new, s');
    const tagSuggestions = page.locator('.tag-list-input .text-list-ac-list');
    await expect(tagSuggestions).toBeVisible();
    // Other fixture tags can also match "s", so choose the exact suggestion.
    await tagSuggestions.getByRole('option', { name: 'sf', exact: true }).click();
    await expect(tagsInput).toHaveValue('new, sf');

    const identifiersInput = page.locator('.edit-modal input[name="identifiers"]');
    await expect(identifiersInput).toHaveAttribute('role', 'combobox');
    await expect(identifiersInput).toHaveAttribute('aria-haspopup', 'listbox');
    await identifiersInput.fill('ISBN 978-0-306-40615-7, https://doi.org/10.1000/182?tracked=true');
    await page.locator('.edit-modal input[name="publisher"]').click();
    await expect(identifiersInput).toHaveValue('isbn:978-0-306-40615-7, doi:10.1000/182');
    await identifiersInput.fill('');
    const identifierSuggestions = page.locator('.identifier-list-input .text-list-ac-list');
    await expect(identifierSuggestions).toBeVisible();
    await expect(identifierSuggestions.locator('.text-list-ac-item').nth(0)).toHaveClass(/active/);
    await page.keyboard.press('ArrowDown');
    await expect(identifierSuggestions.locator('.text-list-ac-item').nth(1)).toHaveClass(/active/);
    await page.keyboard.press('ArrowUp');
    await expect(identifierSuggestions.locator('.text-list-ac-item').nth(0)).toHaveClass(/active/);
    await page.keyboard.press('ArrowDown');
    await page.keyboard.press('Enter');
    await expect(identifiersInput).toHaveValue('doi:');
    await identifiersInput.fill('');
    await expect(identifierSuggestions).toBeVisible();
    await page.keyboard.press('Tab');
    await expect(identifierSuggestions).toBeHidden();
    await identifiersInput.focus();
    await page.keyboard.press('ArrowUp');
    await expect(identifierSuggestions).toBeVisible();
    await expect(identifierSuggestions.locator('.text-list-ac-item').last()).toHaveClass(/active/);
    await page.keyboard.press('Enter');
    await expect(identifiersInput).toHaveValue('uuid:');

    // Esc with a dirty draft asks before discarding.
    await page.keyboard.press('Escape');
    await expect(page.getByRole('heading', { name: 'Discard changes?' })).toBeVisible();
    await page.getByRole('button', { name: 'Discard' }).click();
    await expect(page.locator('.modal-backdrop')).toHaveCount(0);
    await expect(page.locator('.date-picker-popover')).toHaveCount(0);
    await expect(page.locator('.detail-title')).toBeVisible();
  });

  test('Rich description editor formats text and validates links', async ({ page }) => {
    const book = await findBook(page, 'With Cover Book');
    await page.goto(`/book/${book.id}`);

    await page.locator('#btn-edit-book').click();

    await expect(page.locator('.rich-editor-toolbar')).toBeVisible();
    const editor = page.locator('.rich-editor-content');
    await expect(editor).toBeVisible();

    const hiddenDesc = page.locator('textarea[name="description"]');
    await expect(hiddenDesc).toBeAttached();

    await editor.click();
    await page.keyboard.press('Control+A');
    await page.keyboard.press('Meta+A'); // For Mac
    await page.keyboard.press('Backspace');
    await page.keyboard.type('Test description.');

    await page.keyboard.press('Home');
    await page.keyboard.press('Shift+ArrowRight');
    await page.keyboard.press('Shift+ArrowRight');
    await page.keyboard.press('Shift+ArrowRight');
    await page.keyboard.press('Shift+ArrowRight');

    const boldBtn = page.locator('.rich-editor-btn[data-command="bold"]');
    await boldBtn.click();

    await expect(editor.locator('b, strong')).toBeVisible();

    const hiddenValue = await hiddenDesc.inputValue();
    expect(hiddenValue).toMatch(/<(b|strong)>Test<\/(b|strong)>/i);

    // Link insertion uses an inline bar (no blocking prompt/alert). Fail the
    // test if any native dialog appears.
    let nativeDialog = false;
    page.on('dialog', async (d) => {
      nativeDialog = true;
      await d.dismiss();
    });

    await editor.click();
    await page.keyboard.press('End');
    for (let i = 0; i < 12; i++) await page.keyboard.press('Shift+ArrowLeft');
    await page.locator('.rich-editor-btn[data-command="createLink"]').click();
    const linkBar = page.locator('.rich-editor-linkbar');
    await expect(linkBar).toBeVisible();

    // An invalid scheme shows an inline error, not an alert, and inserts nothing.
    await linkBar.locator('.rich-editor-linkbar-input').fill('ftp://nope');
    await linkBar.locator('.rich-editor-linkbar-add').click();
    await expect(linkBar.locator('.rich-editor-linkbar-error')).toBeVisible();
    await expect(editor.locator('a')).toHaveCount(0);

    // A valid URL inserts an <a href> and closes the bar.
    await linkBar.locator('.rich-editor-linkbar-input').fill('https://example.com');
    await linkBar.locator('.rich-editor-linkbar-add').click();
    await expect(linkBar).toBeHidden();
    await expect(editor.locator('a[href="https://example.com"]')).toBeVisible();
    expect(await hiddenDesc.inputValue()).toContain('href="https://example.com"');
    expect(nativeDialog).toBe(false);

    await expect(page.locator('.save-indicator')).toContainText('unsaved change');
    await expect(page.getByRole('button', { name: 'Save', exact: true })).toBeEnabled();
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(page.locator('.save-indicator')).toContainText('Saved');

    // Esc closes the clean modal.
    await page.keyboard.press('Escape');
    await expect(page.locator('.modal-backdrop')).toHaveCount(0);
  });

  test('Book edit keeps title sort quiet until it differs', async ({ page }) => {
    const authorInfoRequests: string[] = [];
    page.on('request', (request) => {
      if (request.url().includes('/api/authors/info')) authorInfoRequests.push(request.url());
    });

    const book = await findBook(page, 'No Cover Book');
    await page.goto(`/book/${book.id}`);

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
});
