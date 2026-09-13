import { expect, test } from './fixtures';

test.use({ seed: 'pagination' });

test.describe('Library pagination', () => {
  for (const view of ['grid', 'table'] as const) {
    test(`${view} automatically appends near the bottom without duplicate requests`, async ({
      page,
    }) => {
      await page.addInitScript((mode) => localStorage.setItem('polka-view-mode', mode), view);
      let nextPageRequests = 0;
      let release!: () => void;
      const pending = new Promise<void>((resolve) => {
        release = resolve;
      });
      await page.route('**/api/books?*', async (route) => {
        if (new URL(route.request().url()).searchParams.get('offset') === '50') {
          nextPageRequests += 1;
          await pending;
        }
        await route.continue();
      });
      await page.goto('/');
      const books = page.locator(view === 'grid' ? '.book-card' : '.table-row');
      await expect(books).toHaveCount(50);
      expect(nextPageRequests).toBe(0);

      try {
        // Enter the prefetch margin while the sentinel is still below the viewport.
        await page.locator('#load-more-container').evaluate((element) => {
          window.scrollTo(
            0,
            element.getBoundingClientRect().top + window.scrollY - window.innerHeight - 300,
          );
        });
        await expect.poll(() => nextPageRequests).toBe(1);
        await expect(page.locator('#load-more-status')).toHaveText('Loading more books…');
      } finally {
        release();
      }
      await expect(books).toHaveCount(55);
      await expect(page.locator('#load-more-container')).toBeHidden();
      expect(nextPageRequests).toBe(1);
      const ids = await books.evaluateAll((elements) =>
        elements.map((element) => element.getAttribute('data-id')),
      );
      expect(new Set(ids).size).toBe(55);
    });
  }

  test('A failed automatic page waits for retry and keeps the existing books', async ({
    page,
    browserErrors,
  }) => {
    browserErrors.allow((message) => /Failed to load more books|503/.test(message));
    let attempts = 0;
    await page.route('**/api/books?*', async (route) => {
      if (new URL(route.request().url()).searchParams.get('offset') === '50' && ++attempts === 1) {
        await route.fulfill({ status: 503, body: 'Unavailable' });
      } else {
        await route.continue();
      }
    });
    await page.goto('/');
    await expect(page.locator('.book-card')).toHaveCount(50);
    await page.locator('.book-card').last().scrollIntoViewIfNeeded();
    const retry = page.getByRole('button', { name: 'Try again', exact: true });
    await expect(retry).toBeVisible();
    await expect(page.locator('.book-card')).toHaveCount(50);
    // Moving out and back into the margin must not retry a failed request.
    await page.evaluate(() => window.scrollTo(0, 0));
    await page.locator('.book-card').last().scrollIntoViewIfNeeded();
    await expect(retry).toBeVisible();
    expect(attempts).toBe(1);
    await retry.click();
    await expect(page.locator('.book-card')).toHaveCount(55);
    expect(attempts).toBe(2);
  });

  test('Changing search cancels an automatic page still in flight', async ({ page }) => {
    let requestStarted!: () => void;
    const inFlight = new Promise<void>((resolve) => {
      requestStarted = resolve;
    });
    let release!: () => void;
    const pending = new Promise<void>((resolve) => {
      release = resolve;
    });
    await page.route('**/api/books?*', async (route) => {
      if (new URL(route.request().url()).searchParams.get('offset') === '50') {
        const response = await route.fetch();
        requestStarted();
        await pending;
        await route.fulfill({ response });
      } else {
        await route.continue();
      }
    });
    await page.goto('/');
    await expect(page.locator('.book-card')).toHaveCount(50);
    const cancelled = page.waitForEvent('requestfailed', {
      predicate: (request) => new URL(request.url()).searchParams.get('offset') === '50',
    });
    try {
      await page.locator('.book-card').last().scrollIntoViewIfNeeded();
      await expect(page.locator('#load-more-status')).toHaveText('Loading more books…');
      // The loading control appears before the browser dispatches fetch.
      // Wait for the intercepted request before testing its cancellation.
      await inFlight;
      await page.locator('#search-input').fill('Filler 001');
      await expect(page.locator('.book-card')).toHaveCount(1);
      await cancelled;
    } finally {
      release();
    }
    await expect(page.locator('#load-more-container')).toBeHidden();
    await expect(page.locator('.book-card')).toHaveCount(1);
    await expect(page.locator('.book-title')).toHaveText('Filler Book 001');
  });

  test('Leaving during an automatic request cancels it and resumes from the retained list', async ({
    page,
  }) => {
    let release!: () => void;
    const pending = new Promise<void>((resolve) => {
      release = resolve;
    });
    const offsets: string[] = [];
    await page.route('**/api/books?*', async (route) => {
      const offset = new URL(route.request().url()).searchParams.get('offset') || '0';
      offsets.push(offset);
      if (offset === '50' && offsets.length === 2) {
        const response = await route.fetch();
        await pending;
        await route.fulfill({ response });
        return;
      }
      await route.continue();
    });
    await page.goto('/');
    await expect(page.locator('.book-card')).toHaveCount(50);
    const cancelled = page.waitForEvent('requestfailed', {
      predicate: (request) => new URL(request.url()).searchParams.get('offset') === '50',
    });
    try {
      await page.locator('.book-card').last().scrollIntoViewIfNeeded();
      await expect(page.locator('#load-more-status')).toHaveText('Loading more books…');
      await page.locator('.book-card').last().locator('.book-title-link').click();
      await expect(page.locator('.detail-title')).toBeVisible();
      await expect(page.locator('#library-grid')).toHaveCount(0);
      expect(offsets).toEqual(['0', '50']);
    } finally {
      release();
    }
    await cancelled;
    await page.goBack();
    await expect(page.locator('.book-card')).toHaveCount(55);
    await expect(page.locator('#load-more-container')).toBeHidden();
    expect(offsets).toEqual(['0', '50', '50']);
    expect(await page.evaluate(() => window.scrollY)).toBeGreaterThan(0);
  });

  test('Duplicate-only pages advance the server offset without ending the list', async ({
    page,
  }) => {
    await page.addInitScript(() => {
      Object.defineProperty(window, 'IntersectionObserver', { value: undefined });
    });
    const source = await page.request.get('/api/books?limit=50');
    const first = await source.json();
    const offsets: number[] = [];
    await page.route('**/api/books?*', async (route) => {
      const offset = Number(new URL(route.request().url()).searchParams.get('offset') || 0);
      offsets.push(offset);
      const books =
        offset < 100 ? first : [{ ...first[0], id: 1000000, title: 'Final unique book' }];
      await route.fulfill({ json: books });
    });
    await page.route('**/covers/1000000?*', (route) =>
      route.fulfill({
        contentType: 'image/svg+xml',
        body: '<svg xmlns="http://www.w3.org/2000/svg" width="80" height="120"/>',
      }),
    );
    await page.goto('/');
    await expect(page.locator('.book-card')).toHaveCount(50);
    await page.getByRole('button', { name: 'Load more', exact: true }).click();
    await expect(page.getByRole('button', { name: 'Load more', exact: true })).toBeEnabled();
    await expect(page.locator('.book-card')).toHaveCount(50);
    await page.getByRole('button', { name: 'Load more', exact: true }).click();
    await expect(page.locator('.book-card')).toHaveCount(51);
    expect(offsets).toEqual([0, 50, 100]);
    await expect(page.locator('#load-more-container')).toBeHidden();
  });

  test('Paging after bulk removal does not skip the next book', async ({ page }) => {
    await page.addInitScript(() => {
      Object.defineProperty(window, 'IntersectionObserver', { value: undefined });
    });
    await page.goto('/');
    await expect(page.locator('.book-card')).toHaveCount(50);
    const card = page.locator('.book-card').first();
    const id = Number(await card.getAttribute('data-id'));
    await card.hover();
    await card.locator('.card-select').click();
    await page.locator('.bulk-bar-action[data-action="delete"]').click();
    await page
      .locator('.modal-confirm')
      .getByRole('button', { name: 'Remove', exact: true })
      .click();
    await expect(page.locator('.book-card')).toHaveCount(49);
    await page.getByRole('button', { name: 'Load more', exact: true }).click();
    await expect(page.locator('.book-card')).toHaveCount(54);
    await expect(page.locator(`.book-card[data-id="${id}"]`)).toHaveCount(0);
  });

  test('Title jump replaces the page at its bounded offset and hides for search', async ({
    page,
  }) => {
    await page.route('**/api/books/jumps?sort=title', async (route) => {
      await route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({
          total: 1000,
          items: [
            { label: 'F', offset: 0 },
            { label: 'G', offset: 20 },
            { label: 'H', offset: 40 },
          ],
        }),
      });
    });

    await page.goto('/?sort=title');
    const rail = page.getByRole('navigation', { name: 'Jump through books' });
    await expect(rail).toBeVisible();

    const requestPromise = page.waitForRequest((request) => {
      const url = new URL(request.url());
      return url.pathname === '/api/books' && url.searchParams.get('offset') === '20';
    });
    await page.getByRole('button', { name: 'Jump to titles starting with G' }).click();
    await requestPromise;
    await expect(page).toHaveURL(/offset=20/);
    await expect(page.locator('.book-card')).toHaveCount(35);
    await expect(
      page.getByRole('button', { name: 'Jump to titles starting with G' }),
    ).toHaveAttribute('aria-current', 'true');

    await page.reload();
    await expect(page.locator('.book-card')).toHaveCount(35);
    await expect(page).toHaveURL(/offset=20/);
    const firstBook = page.locator('.book-card').first().locator('.book-title-link');
    await expect(firstBook).toHaveAttribute('href', /[?&]offset=20(?:&|$)/);
    await firstBook.click();
    await expect(page.locator('.back-link a')).toHaveAttribute('href', /[?&]offset=20(?:&|$)/);
    await page.locator('.back-link a').click();
    await expect(page.locator('.book-card')).toHaveCount(35);

    await page.locator('#search-input').fill('Filler 001');
    await expect(rail).toBeHidden();
    await expect(page).not.toHaveURL(/offset=/);
  });
});
