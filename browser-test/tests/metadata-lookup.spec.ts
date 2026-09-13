import { epub } from './book-fixtures';
import { expect, test } from './fixtures';
import { importTestBook } from './helpers';

test.describe('Metadata lookup', () => {
  test('Metadata fetch dialog applies candidates to the edit draft', async ({ page }) => {
    let candidateRequests = 0;
    let descriptionRequests = 0;
    let updateRequests = 0;
    let coverApplyRequests = 0;
    let appliedCoverURL = '';
    await page.route(/\/api\/books\/[^/?]+$/, async (route) => {
      if (route.request().method() === 'PATCH') updateRequests++;
      await route.continue();
    });
    await page.route(
      /\/api\/books\/[^/]+\/metadata-candidates\?provider=openlibrary$/,
      async (route) => {
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
              authors: 'Fetched Author; Research && Editing',
              publisher: 'Fetched Press',
              date: '2026',
              tags: 'Fetched, Metadata',
              identifiers: 'isbn:9780000000002, openlibrary:/works/OL1W',
              cover_url: 'https://covers.openlibrary.org/b/id/1-L.jpg?default=false',
            },
          ]),
        });
      },
    );
    await page.route(
      /\/api\/metadata\/description\?provider=openlibrary&ref=%2Fworks%2FOL1W$/,
      async (route) => {
        descriptionRequests++;
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ description: 'Fetched Description' }),
        });
      },
    );
    await page.route(/\/api\/books\/[^/]+\/cover-url$/, async (route) => {
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
    await page.route('https://covers.openlibrary.org/b/id/1-L.jpg?default=false', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'image/svg+xml',
        body: '<svg xmlns="http://www.w3.org/2000/svg" width="96" height="144"><rect width="96" height="144" fill="#2f8f77"/><text x="48" y="72" text-anchor="middle" fill="white" font-size="12">Fetched</text></svg>',
      });
    });

    const bookId = await importTestBook(
      page,
      epub('Metadata Fetch Draft', 'Editor Author', 'editor-draft'),
    );
    await page.goto(`/book/${bookId}`);

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
    await expect(page.locator('.metadata-candidate-main')).toContainText(
      'Fetched Author, Research & Editing',
    );
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
    await expect(authorsInput).toHaveValue('Fetched Author; Research && Editing');
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

  test('Metadata fetch handles cover-only and dirty draft edge cases', async ({ page }) => {
    let candidateRequests = 0;
    await page.route(
      /\/api\/books\/[^/]+\/metadata-candidates\?provider=openlibrary$/,
      async (route) => {
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
      },
    );
    await page.route(
      /https:\/\/covers\.openlibrary\.org\/b\/id\/edge-cover-\d-L\.jpg\?default=false$/,
      async (route) => {
        await route.fulfill({
          status: 200,
          contentType: 'image/svg+xml',
          body: '<svg xmlns="http://www.w3.org/2000/svg" width="96" height="144"><rect width="96" height="144" fill="#6f6ab7"/><text x="48" y="72" text-anchor="middle" fill="white" font-size="11">Edge</text></svg>',
        });
      },
    );

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
    let releaseOpenLibrary!: () => void;
    const openLibraryReady = new Promise<void>((resolve) => {
      releaseOpenLibrary = resolve;
    });
    let resolveOpenLibrarySettled: () => void = () => {};
    const openLibrarySettled = new Promise<void>((resolve) => {
      resolveOpenLibrarySettled = resolve;
    });
    await page.route(
      /\/api\/books\/[^/]+\/metadata-candidates\?provider=openlibrary$/,
      async (route) => {
        openLibraryRequests++;
        await openLibraryReady;
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
      },
    );
    await page.route(
      /\/api\/books\/[^/]+\/metadata-candidates\?provider=google$/,
      async (route) => {
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
      },
    );

    await page.goto('/');
    await page.locator('.book-card').first().locator('.book-title').click();
    await expect(page.locator('.detail-title')).toBeVisible();
    await page.locator('#btn-edit-book').click();
    await expect(page.locator('.edit-modal')).toBeVisible();

    await page.locator('.edit-modal .metadata-fetch-action').click();
    await expect(page.locator('.metadata-modal')).toBeVisible();
    const fetchBtn = page.locator('.metadata-modal .metadata-fetch-action', { hasText: 'Fetch' });
    try {
      await fetchBtn.click();
      await expect(page.locator('.metadata-status')).toContainText('Loading candidates');
      await expect.poll(() => openLibraryRequests).toBe(1);

      await page.locator('.metadata-provider-select').click();
      await page.getByRole('option', { name: 'Google Books' }).click();
      await expect(page.locator('.metadata-status')).toContainText('Choose a provider');
      await expect(fetchBtn).toBeEnabled();
      await fetchBtn.click();

      await expect(
        page.locator('.metadata-candidate', { hasText: 'Fresh Google Title' }),
      ).toBeVisible();
      await expect(page.locator('.metadata-status')).toContainText('1 candidate found.');
    } finally {
      releaseOpenLibrary();
    }
    await openLibrarySettled;
    await expect(
      page.locator('.metadata-candidate', { hasText: 'Stale Open Library Title' }),
    ).toHaveCount(0);
    expect(openLibraryRequests).toBe(1);
    expect(googleRequests).toBe(1);
  });
});
