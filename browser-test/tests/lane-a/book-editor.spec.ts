import { Buffer } from 'node:buffer';
import { epub } from '../book-fixtures';
import { expect, type Locator, type Page, test } from '../fixtures';

let disposableWorkIDs: string[] = [];

test.beforeEach(() => {
  disposableWorkIDs = [];
});

test.afterEach(async ({ page }) => {
  for (const workID of disposableWorkIDs) {
    const trash = await page.request.post('/api/books/bulk/trash', { data: { ids: [workID] } });
    expect(trash.ok()).toBeTruthy();
    const purge = await page.request.delete(`/api/books/${encodeURIComponent(workID)}/purge`);
    expect(purge.status()).toBe(204);
  }
});

async function uploadDisposableBook(page: Page, prefix: string): Promise<string> {
  const stamp = Date.now().toString(36);
  const title = `${prefix} ${stamp}`;
  const fixtureName = prefix.toLowerCase().replace(/[^a-z0-9]+/g, '-');
  await page.goto('/');
  await page.locator('#book-upload-input').setInputFiles(
    epub(title, 'Disposable Editor Author', `editor-${fixtureName}-${stamp}`),
  );
  const card = page.locator('.book-card', { hasText: title });
  await expect(card).toBeVisible();
  const href = await card.locator('.book-title-link').getAttribute('href');
  const workID = href ? new URL(href, page.url()).pathname.split('/').pop() : '';
  if (!workID) throw new Error('missing disposable editor work id');
  disposableWorkIDs.push(workID);
  return title;
}

async function expectImageLoaded(img: Locator): Promise<void> {
  await expect
    .poll(async () => {
      return await img.evaluate((i: HTMLImageElement) => i.complete && i.naturalWidth > 0);
    })
    .toBe(true);
}

test.describe('Book editor', () => {
  test('Book edit stages primary author sort through Save', async ({ page }) => {
    await page.goto('/');
    const card = page.locator('.book-card', { hasText: 'No Cover Book' });
    await expect(card).toBeVisible();
    await card.locator('.book-title').click();
    await expect(page.locator('.detail-title')).toContainText('No Cover Book');

    try {
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

      await expect.poll(async () => {
        return await page.evaluate(async () => {
          const res = await fetch('/api/authors/info?name=Test%20Author');
          if (!res.ok) return '';
          return (await res.json()).sort_name;
        });
      }).toBe('Author Sort, Test');
    } finally {
      await page.evaluate(async () => {
        await fetch('/api/authors/sort-name', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ name: 'Test Author', sort_name: 'Author, Test' }),
        });
      });
    }
  });

  test('Book edit PATCH sends only dirty fields and preserves a concurrent change', async ({ page }) => {
    await page.goto('/');
    const titleLink = page.getByRole('link', { name: 'Foundation', exact: true });
    await expect(titleLink).toBeVisible();
    await titleLink.click();
    await expect(page.locator('.detail-title')).toHaveText('Foundation');

    const bookID = new URL(page.url()).pathname.split('/').pop();
    if (!bookID) throw new Error('missing book id');
    const baselineResponse = await page.request.get(`/api/books/${encodeURIComponent(bookID)}`);
    expect(baselineResponse.ok()).toBe(true);
    const baseline = await baselineResponse.json();
    const concurrentPublisher = `Concurrent Press ${Date.now().toString(36)}`;

    try {
      await page.locator('#btn-edit-book').click();
      const modal = page.locator('.edit-modal');
      const seriesInput = modal.locator('input[name="series"]');
      await expect(seriesInput).toHaveValue('Test Series');

      // Change a field behind the already-open form, then save a different field
      // from that stale draft. The form must not echo its old publisher value.
      await page.evaluate(
        async ({ id, publisher }) => {
          const res = await fetch(`/api/books/${encodeURIComponent(id)}`, {
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
      await seriesInput.fill('');
      await modal.locator('.edit-save-btn').click();
      expect((await patchRequest).postDataJSON()).toEqual({ series: null });
      await expect(modal.locator('.save-indicator')).toContainText('Saved');

      await expect
        .poll(async () => {
          const response = await page.request.get(`/api/books/${encodeURIComponent(bookID)}`);
          const book = await response.json();
          return { publisher: book.publisher, series: book.series };
        })
        .toEqual({ publisher: concurrentPublisher, series: null });
    } finally {
      await page.evaluate(
        async ({ id, publisher, series }) => {
          const res = await fetch(`/api/books/${encodeURIComponent(id)}`, {
            method: 'PATCH',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ publisher, series }),
          });
          if (!res.ok) throw new Error(await res.text());
        },
        { id: bookID, publisher: baseline.publisher ?? null, series: baseline.series ?? null },
      );
    }
  });

  test('Book edit reuses selected autocomplete author sort name', async ({ page }) => {
    await page.goto('/');
    try {
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
    } finally {
      await page.evaluate(async () => {
        await fetch('/api/authors/sort-name', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ name: 'Test Author', sort_name: 'Author, Test' }),
        });
      });
    }
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
        const res = await fetch(`/api/books/${encodeURIComponent(id)}/sequence?${query}&before=0&after=1`);
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
    await page.route('**/api/books/*', async (route) => {
      const url = new URL(route.request().url());
      if (route.request().method() === 'GET' && !url.pathname.endsWith('/sequence')) {
        await new Promise((resolve) => setTimeout(resolve, 650));
      }
      await route.continue();
    });

    await nextButton.click();
    await expect(page.locator('.edit-form-loading-overlay')).toBeVisible();
    await expect(page.locator('.edit-modal .save-indicator')).not.toContainText('Loading');
    await expect(page.locator('.edit-modal input[name="title"]')).toHaveValue(nextBook.title);
    await expect(page.locator('.detail-title')).toContainText(nextBook.title);
    expect(readerStateRequests).toEqual([]);
    await expect(form).toHaveAttribute('id', formID || '');
    await expect(page.locator('.modal-backdrop')).toHaveAttribute('data-stable-test', 'same');
    await expect.poll(() => new URL(page.url()).pathname).toBe(`/book/${nextBook.id}`);
    await expect.poll(() => new URL(page.url()).searchParams.get('from')).toBe('library');

    await page.keyboard.press('Escape');
    await expect(page.locator('.modal-backdrop')).toHaveCount(0);
  });

  test('Metadata fetch dialog applies candidates to the edit draft', async ({ page }) => {
    let candidateRequests = 0;
    let descriptionRequests = 0;
    let updateRequests = 0;
    let coverApplyRequests = 0;
    let appliedCoverURL = '';
    await page.route(/\/api\/books\/[^/?]+$/, async route => {
      if (route.request().method() === 'PATCH') updateRequests++;
      await route.continue();
    });
    await page.route(/\/api\/books\/[^/]+\/metadata-candidates\?provider=openlibrary$/, async route => {
      candidateRequests++;
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([
          {
            provider: 'openlibrary',
            provider_name: 'Open Library',
            provider_id: '/works/OL1W',
            title: 'Fetched Title',
            authors: 'Fetched Author',
            publisher: 'Fetched Press',
            date: '2026',
            tags: 'Fetched, Metadata',
            identifiers: 'isbn:9780000000002, openlibrary:/works/OL1W',
            cover_url: 'https://covers.openlibrary.org/b/id/1-L.jpg?default=false',
          },
        ]),
      });
    });
    await page.route(/\/api\/metadata\/description\?provider=openlibrary&ref=%2Fworks%2FOL1W$/, async route => {
      descriptionRequests++;
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ description: 'Fetched Description' }),
      });
    });
    await page.route(/\/api\/books\/[^/]+\/cover-url$/, async route => {
      coverApplyRequests++;
      const payload = route.request().postDataJSON() as { url: string };
      appliedCoverURL = payload.url;

      const reqURL = new URL(route.request().url());
      const bookPath = reqURL.pathname.replace('/cover-url', '');
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
    await page.route('https://covers.openlibrary.org/b/id/1-L.jpg?default=false', async route => {
      await route.fulfill({
        status: 200,
        contentType: 'image/svg+xml',
        body: '<svg xmlns="http://www.w3.org/2000/svg" width="96" height="144"><rect width="96" height="144" fill="#2f8f77"/><text x="48" y="72" text-anchor="middle" fill="white" font-size="12">Fetched</text></svg>',
      });
    });

    const disposableTitle = await uploadDisposableBook(page, 'Metadata Fetch Draft');
    await page
      .locator('.book-card', { hasText: disposableTitle })
      .locator('.book-title')
      .click();
    await expect(page.locator('.detail-title')).toBeVisible();

    await page.locator('#btn-edit-book').click();
    await expect(page.locator('.edit-modal')).toBeVisible();
    const titleInput = page.locator('.edit-modal input[name="title"]');
    const authorsInput = page.locator('.edit-modal input[name="authors"]');
    const identifiersInput = page.locator('.edit-modal input[name="identifiers"]');
    const originalTitle = await titleInput.inputValue();
    const originalAuthors = await authorsInput.inputValue();
    const originalIdentifiers = await identifiersInput.inputValue();

    await page.locator('.edit-modal .metadata-fetch-action').click();
    await expect(page.locator('.metadata-modal')).toBeVisible();
    expect(candidateRequests).toBe(0);
    await expect(page.locator('.metadata-provider-select')).toContainText('Open Library');
    await expect(page.locator('.metadata-status')).toContainText('Choose a provider');

    await page.locator('.metadata-modal .metadata-fetch-action', { hasText: 'Fetch' }).click();
    await expect(page.locator('.metadata-candidate', { hasText: 'Fetched Title' })).toBeVisible();
    await expect(page.locator('.metadata-status')).toContainText('1 candidate found.');
    await expect(page.locator('.metadata-candidate-fields')).toContainText('Cover');
    await expect(page.locator('.metadata-candidate-fields')).toContainText('Identifiers');
    await expect(page.locator('.metadata-candidate-impact')).toContainText('would replace');

    await page.locator('.metadata-replace-btn').click();
    await expect(page.locator('.metadata-modal')).toHaveCount(0);
    expect(descriptionRequests).toBe(1);
    expect(updateRequests).toBe(0);
    expect(coverApplyRequests).toBe(0);

    await expect(titleInput).toHaveValue('Fetched Title');
    await expect(authorsInput).toHaveValue('Fetched Author');
    const titleField = page.locator('.edit-modal [data-edit-field="title"]');
    await expect(titleField).toHaveClass(/is-fetched/);
    await expect(titleField.locator('.edit-field-revert')).toHaveAttribute(
      'title',
      `Revert to ${originalTitle}`,
    );
    await expect(page.locator('.edit-modal input[name="publisher"]')).toHaveValue('Fetched Press');
    const appliedIdentifiers = await identifiersInput.inputValue();
    if (originalIdentifiers) expect(appliedIdentifiers).toContain(originalIdentifiers);
    expect(appliedIdentifiers).toContain('isbn:9780000000002');
    expect(appliedIdentifiers).toContain('openlibrary:/works/OL1W');
    await expect(page.locator('.edit-modal textarea[name="description"]')).toHaveValue(
      'Fetched Description',
    );
    const coverContainer = page.locator('.edit-cover-container');
    await expect(coverContainer).toHaveClass(/is-fetched/);
    await expect(coverContainer.locator('img')).toHaveAttribute(
      'src',
      'https://covers.openlibrary.org/b/id/1-L.jpg?default=false',
    );
    await expect(page.locator('.edit-cover-revert')).toBeVisible();
    await expect(page.locator('.edit-modal .save-indicator')).toContainText('unsaved');
    await expect(page.locator('.edit-modal .edit-save-btn')).toBeEnabled();
    await page.screenshot({ path: 'screenshots/metadata-fetch.png', fullPage: true });

    await titleField.locator('.edit-field-revert').click();
    await expect(titleInput).toHaveValue(originalTitle);
    await expect(titleField).not.toHaveClass(/is-fetched/);
    const authorsField = page.locator('.edit-modal [data-edit-field="authors"]');
    await authorsField.locator('.edit-field-revert').click();
    await expect(authorsInput).toHaveValue(originalAuthors);
    const identifiersField = page.locator('.edit-modal [data-edit-field="identifiers"]');
    await identifiersField.locator('.edit-field-revert').click();
    await expect(identifiersInput).toHaveValue(originalIdentifiers);
    await expect(page.locator('.identifier-list-input .text-list-ac-list')).toBeHidden();
    await page.locator('.edit-modal input[name="publisher"]').click();
    await expect(page.locator('.identifier-list-input .text-list-ac-list')).toBeHidden();
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(page.locator('.edit-modal .save-indicator')).toContainText('Saved');
    await expect(coverContainer).not.toHaveClass(/is-fetched/);
    await expect(page.locator('.edit-cover-revert')).toBeHidden();
    expect(updateRequests).toBe(1);
    expect(coverApplyRequests).toBe(1);
    expect(appliedCoverURL).toBe('https://covers.openlibrary.org/b/id/1-L.jpg?default=false');
  });

  test('Edit cover upload is staged until Save', async ({ page }) => {
    let uploadRequests = 0;
    const pendingCover = {
      name: 'pending-cover.png',
      mimeType: 'image/png',
      buffer: Buffer.from(
        'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR42mP4//8/AAX+Av7czFnnAAAAAElFTkSuQmCC',
        'base64',
      ),
    };

    await page.route(/\/api\/books\/[^/]+\/cover$/, async route => {
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
    await page.goto('/');
    const card = page.locator('.book-card', { hasText: 'With Cover Book' });
    await expect(card).toBeVisible();
    await card.locator('.book-title').click();
    await expect(page.locator('.detail-title')).toContainText('With Cover Book');

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
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(page.locator('.edit-modal .save-indicator')).toContainText('Saved');
    await expect(coverContainer).not.toHaveClass(/is-dirty/);
    await expect(page.locator('.edit-cover-revert')).toBeHidden();
    expect(uploadRequests).toBe(1);
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

    await page.route(/\/api\/books\/[^/]+\/cover-generated-preview$/, async route => {
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
    await page.route(/\/api\/books\/[^/]+\/cover$/, async route => {
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
    await page.route(/\/covers\/[^/]+(?:\?.*)?$/, async route => {
      if (countStoredCoverRequests && route.request().resourceType() === 'image') {
        storedCoverRequests++;
      }
      await route.continue();
    });

    await page.goto('/');
    const card = page.locator('.book-card', { hasText: 'With Cover Book' });
    await expect(card).toBeVisible();
    await card.locator('.book-title').click();
    await expect(page.locator('.detail-title')).toContainText('With Cover Book');

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
    await expect(
      coverPicker.getByRole('button', { name: 'Generate', exact: true }),
    ).toBeVisible();
    countStoredCoverRequests = true;
    const labelVariant = coverPicker.locator('[data-cover-style="label"]');
    await labelVariant.click();
    await expect(labelVariant).toHaveClass(/is-selected/);
    await expectImageLoaded(coverPicker.locator('.cover-picker-primary img'));
    expect(storedCoverRequests).toBe(0);
    await page.screenshot({ path: 'screenshots/generated-cover-variants.png', fullPage: true });
    expect(uploadRequests).toBe(0);

    await coverPicker.getByRole('button', { name: 'Use saved cover' }).click();
    await expect(coverContainer).not.toHaveClass(/is-dirty/);
    await expect(page.locator('.edit-cover-revert')).toBeHidden();
    await expect(coverPicker.locator('.cover-picker-reference')).toBeVisible();
    await expect(coverPicker.getByRole('button', { name: 'Use saved cover' })).toHaveClass(
      /is-selected/,
    );
    await expect(coverPicker.locator('.cover-picker-primary .cover-picker-preview-label')).toHaveText(
      'Current',
    );
    await expect(coverPicker.locator('.cover-picker-variant')).toHaveCount(4);
    await expect(
      coverPicker.getByRole('button', { name: 'Generate', exact: true }),
    ).toBeVisible();
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

  test('Cover search error composes its action and cause once', async ({ page, browserErrors }) => {
    browserErrors.allow(
      message => message.includes('/cover-search?') && message.includes('502 (Bad Gateway)'),
    );
    await page.route(/\/api\/books\/[^/]+\/cover-search(?:\?.*)?$/, async route => {
      await route.fulfill({
        status: 502,
        contentType: 'text/plain',
        body: 'Cover provider unavailable',
      });
    });

    await page.goto('/');
    const card = page.locator('.book-card', { hasText: 'With Cover Book' });
    await expect(card).toBeVisible();
    await card.locator('.book-title').click();
    await page.locator('#btn-edit-book').click();
    await page.locator('.edit-modal').getByRole('button', { name: 'Find cover online' }).click();

    const toast = page.locator('.toast:not(.toast-leaving) .toast-text');
    await expect(toast).toHaveText('Cover search failed: Cover provider unavailable');
  });

  test('Web cover search is staged until Save', async ({ page }) => {
    let searchRequests = 0;
    let applyRequests = 0;
    let appliedToken = '';
    const searchQueries: Array<{ title: string | null; author: string | null }> = [];
    const previewSVG = (fill: string) =>
      `<svg xmlns="http://www.w3.org/2000/svg" width="120" height="180"><rect width="120" height="180" fill="${fill}"/></svg>`;

    await page.route(/\/api\/books\/[^/]+\/cover-search\/preview\?token=web-token-\d+$/, async route => {
      const token = new URL(route.request().url()).searchParams.get('token');
      await route.fulfill({
        status: 200,
        contentType: 'image/svg+xml',
        body: previewSVG(token === 'web-token-1' ? '#5c8f9f' : '#c47a45'),
      });
    });
    await page.route(/\/api\/books\/[^/]+\/cover-search(?:\?.*)?$/, async route => {
      const reqURL = new URL(route.request().url());
      if (route.request().method() === 'GET') {
        searchRequests++;
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

    await page.goto('/');
    const card = page.locator('.book-card', { hasText: 'With Cover Book' });
    await expect(card).toBeVisible();
    await card.locator('.book-title').click();
    await expect(page.locator('.detail-title')).toContainText('With Cover Book');

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

    await expect(searchModal.locator('.cover-search-result')).toHaveCount(2);
    await expect(searchModal.getByRole('button', { name: 'Search' })).toBeEnabled();
    await expect(searchModal.locator('.cover-search-result').first()).toContainText('Goodreads');
    await page.waitForTimeout(220);
    await page.screenshot({ path: 'screenshots/cover-search-results.png', fullPage: true });
    await searchModal.getByRole('button', { name: 'Use cover 1 from Goodreads' }).click();

    expect(searchRequests).toBe(1);
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
    await page.goto('/');

    const card = page.locator('.book-card', { hasText: 'With Cover Book' });
    await expect(card).toBeVisible();
    await card.locator('.book-title').click();
    await expect(page.locator('.detail-title')).toContainText('With Cover Book');

    await page.locator('#btn-edit-book').click();
    const coverContainer = page.locator('.edit-cover-container');
    await expect(coverContainer).toBeVisible();

    const img = coverContainer.locator('img');
    await expect(img).toHaveCount(1);
    await expectImageLoaded(img);

    // A loaded edit cover is image-only; no inline placeholder text should sit beside it.
    const containerText = (await coverContainer.textContent())?.trim() ?? '';
    expect(containerText).toBe('');

    // The recognized date shows in the field itself (parsed from a full ISO
    // string); the hint stays silent — only an unparseable value flags now.
    const dateInput = page.locator('[id^="date-input-"]');
    await expect(dateInput).toHaveValue('13 June 2026');
    const dateHint = page.locator('[id^="date-validation-"]');
    await expect(dateHint).not.toContainText('Unrecognized');

    await page.locator('.date-picker-trigger').click();
    const datePicker = page.locator('.date-picker-popover');
    await expect(datePicker).toBeVisible();
    await expect(datePicker.getByRole('button', { name: 'Day' })).toHaveAttribute('aria-pressed', 'true');
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
    // Click the specific suggestion rather than pressing Enter on the first one:
    // a tag created by a parallel test can also match "s" and would otherwise be
    // the highlighted pick, making this assertion flaky on the shared library.
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

    await page.screenshot({ path: 'screenshots/edit.png', fullPage: true });

    // Esc with a dirty draft asks before discarding.
    await page.keyboard.press('Escape');
    await expect(page.getByRole('heading', { name: 'Discard changes?' })).toBeVisible();
    await page.getByRole('button', { name: 'Discard' }).click();
    await expect(page.locator('.modal-backdrop')).toHaveCount(0);
    await expect(page.locator('.date-picker-popover')).toHaveCount(0);
    await expect(page.locator('.detail-title')).toBeVisible();
  });

  test('Edit view rich editor works correctly', async ({ page }) => {
    const disposableTitle = await uploadDisposableBook(page, 'Rich Editor Draft');

    const card = page.locator('.book-card', { hasText: disposableTitle });
    await expect(card).toBeVisible();
    await card.locator('.book-title').click();

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
    page.on('dialog', async d => { nativeDialog = true; await d.dismiss(); });

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


});
