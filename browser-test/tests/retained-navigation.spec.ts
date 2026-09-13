import type { BookSummary } from '../../frontend/src/types';
import { expect, type Page, test } from './fixtures';

test.use({ seed: 'pagination' });

// Back reuses the library view, preserving loaded books, scroll and focus
// without another list request.

// The top of what the reader can see, and where it sits in the viewport. A
// return preserves this pair; which book it happens to be does not matter.
async function firstVisibleBook(
  page: Page,
  selector: string,
): Promise<{ id: string; top: number }> {
  const found = await page.evaluate((sel) => {
    for (const el of document.querySelectorAll<HTMLElement>(sel)) {
      const rect = el.getBoundingClientRect();
      if (rect.bottom > 0 && el.dataset.id) return { id: el.dataset.id, top: rect.top };
    }
    return null;
  }, selector);
  expect(found).not.toBeNull();
  return found as { id: string; top: number };
}

async function bookTop(page: Page, selector: string, id: string): Promise<number> {
  return await page.evaluate(
    ([sel, bookId]) => {
      const el = document.querySelector<HTMLElement>(`${sel}[data-id="${bookId}"]`);
      if (!el) throw new Error(`no rendered book ${bookId}`);
      return el.getBoundingClientRect().top;
    },
    [selector, id] as const,
  );
}

test.describe('Retained library navigation', () => {
  test('The visible Back control resumes the live library instead of rebuilding it', async ({
    page,
  }) => {
    await page.goto('/');
    await expect(page.locator('.book-card').first()).toBeVisible();
    const firstPage = await page.locator('.book-card').count();

    await page.locator('.book-card').last().scrollIntoViewIfNeeded();
    await expect(page.locator('.book-card')).toHaveCount(55);
    // What the reader accumulated, whatever the page size happens to be.
    const extent = await page.locator('.book-card').count();
    expect(extent).toBeGreaterThan(firstPage);

    await page.locator('.book-card').nth(firstPage).scrollIntoViewIfNeeded();
    const anchor = await firstVisibleBook(page, '.book-card');
    const scrollBefore = await page.evaluate(() => window.scrollY);
    expect(scrollBefore).toBeGreaterThan(0);

    let listRequests = 0;
    await page.route('**/api/books?*', async (route) => {
      listRequests += 1;
      await route.continue();
    });

    let releaseBook!: () => void;
    const bookReady = new Promise<void>((resolve) => {
      releaseBook = resolve;
    });
    await page.route(/\/api\/books\/\d+$/, async (route) => {
      await bookReady;
      await route.continue();
    });
    try {
      await page.locator('.book-card').nth(firstPage).locator('.book-title-link').click();
      await expect(page).toHaveURL(/\/book\//);
      await expect(page.locator('.book-detail-loading-card')).toContainText('Loading book');
    } finally {
      releaseBook();
    }
    await expect(page.locator('.detail-title')).toBeFocused();
    // The library is detached, not merely hidden.
    await expect(page.locator('#library-grid')).toHaveCount(0);
    const entries = await page.evaluate(() => history.length);

    // The in-page Back control returns through history rather than pushing a
    // fresh entry, which is what lets the previous page come back as it was.
    await page.locator('.back-link a').click();
    await expect(page).toHaveURL(/^[^?]*\/(\?.*)?$/);
    await expect(page.locator('.book-card')).toHaveCount(extent);
    expect(listRequests).toBe(0);
    expect(await page.evaluate(() => history.length)).toBe(entries);

    // The reader is looking at the same book in the same place.
    expect(Math.abs((await bookTop(page, '.book-card', anchor.id)) - anchor.top)).toBeLessThan(2);
    // Focus lives only in the instance: the root left the document entirely, so
    // it has to be captured and restored rather than merely surviving.
    await expect(
      page.locator('.book-card').nth(firstPage).locator('.book-title-link'),
    ).toBeFocused();

    // Forward parks the same instance again; Back still resumes it.
    await page.goForward();
    await expect(page.locator('#book-detail-container')).toBeVisible();
    await page.goBack();
    await expect(page.locator('.book-card')).toHaveCount(extent);
    expect(listRequests).toBe(0);
  });

  test('A Back taken before the book page settles keeps the restored position', async ({
    page,
  }) => {
    await page.goto('/');
    await expect(page.locator('.book-card').first()).toBeVisible();
    await page.locator('.book-card').nth(30).scrollIntoViewIfNeeded();
    const scrollBefore = await page.evaluate(() => window.scrollY);
    expect(scrollBefore).toBeGreaterThan(0);

    // Leaving again as soon as the book has rendered, before the frames its own
    // scroll restore was scheduled on have run. That restore belongs to a
    // navigation this one supersedes and must not land on the resumed library.
    await page.evaluate((index) => {
      const observer = new MutationObserver(() => {
        if (!document.querySelector('.detail-title')) return;
        observer.disconnect();
        window.history.back();
      });
      const host = document.getElementById('app-content');
      if (host) observer.observe(host, { childList: true, subtree: true });
      document
        .querySelectorAll<HTMLElement>('.book-card')
        [index].querySelector<HTMLElement>('.book-title-link')
        ?.click();
    }, 30);

    await expect(page.locator('#library-grid')).toBeVisible();
    // Three frames is past both of the frames a pending restore waits for.
    await page.evaluate(
      () =>
        new Promise<void>((resolve) =>
          requestAnimationFrame(() =>
            requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
          ),
        ),
    );
    expect(await page.evaluate(() => window.scrollY)).toBeGreaterThan(scrollBefore - 50);
  });

  test('Leaving the relationship destroys the retained library', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('.book-card').first()).toBeVisible();
    const firstPage = await page.locator('.book-card').count();
    await page.locator('.book-card').last().scrollIntoViewIfNeeded();
    await expect(page.locator('.book-card')).toHaveCount(55);
    expect(await page.locator('.book-card').count()).toBeGreaterThan(firstPage);

    await page.locator('.book-card').first().locator('.book-title-link').click();
    await expect(page.locator('#book-detail-container')).toBeVisible();

    // The sidebar Library link is the deliberate escape hatch: an ordinary
    // navigation that rebuilds the list from its first page.
    await page.locator('#nav-library').click();
    await expect(page.locator('.book-card').first()).toBeVisible();
    await expect(page.locator('.book-card')).toHaveCount(firstPage);

    // A destroyed instance still holding its subscriptions would answer this
    // too, and duplicate work is how that shows up.
    let listRequests = 0;
    await page.route('**/api/books?*', async (route) => {
      listRequests += 1;
      await route.continue();
    });
    await page.evaluate(() =>
      document.dispatchEvent(
        new CustomEvent('polka:catalog-changed', { detail: { kind: 'coarse' } }),
      ),
    );
    await expect(page.locator('.book-card')).toHaveCount(firstPage);
    expect(listRequests).toBe(1);
  });

  test('An edit made on the book page patches the retained card in place', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('.book-card').first()).toBeVisible();
    const extent = await page.locator('.book-card').count();

    const card = page.locator('.book-card').nth(3);
    const originalTitle = await card.locator('.book-title').innerText();
    const renamed = `${originalTitle} Renamed`;

    await card.locator('.book-title-link').click();
    await expect(page.locator('.detail-title')).toContainText(originalTitle);

    await page.locator('#btn-edit-book').click();
    await expect(page.locator('.edit-modal')).toBeVisible();
    await page.locator('.edit-modal input[name="title"]').fill(renamed);
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(page.locator('.edit-modal .save-indicator')).toHaveText('Saved');
    await page.locator('.edit-modal .modal-close').click();
    await expect(page.locator('.detail-title')).toContainText(renamed);

    let listRequests = 0;
    await page.route('**/api/books?*', async (route) => {
      listRequests += 1;
      await route.continue();
    });

    await page.goBack();
    // Recently added order is independent of the changed title.
    await expect(page.locator('.book-card').nth(3).locator('.book-title')).toHaveText(renamed);
    await expect(page.locator('.book-card')).toHaveCount(extent);
    expect(listRequests).toBe(0);
  });

  test('A retained refresh preserves the range and follows scrolling while it loads', async ({
    page,
  }) => {
    await page.route('**/api/books/jumps?sort=title', (route) =>
      route.fulfill({
        json: {
          total: 55,
          items: [
            { label: 'F', offset: 0 },
            { label: 'G', offset: 40 },
          ],
        },
      }),
    );
    await page.goto('/?sort=title');
    await expect(page.locator('#library-jump-rail')).toBeVisible();
    await expect(page.locator('.book-card').first()).toBeVisible();
    await page.locator('.book-card').last().scrollIntoViewIfNeeded();
    await expect(page.locator('.book-card')).toHaveCount(55);
    const extent = await page.locator('.book-card').count();

    await page.locator('.book-card').nth(50).scrollIntoViewIfNeeded();
    const anchor = await firstVisibleBook(page, '.book-card');

    await page.locator('.book-card').nth(50).locator('.book-title-link').click();
    await expect(page.locator('#book-detail-container')).toBeVisible();

    let listRequests = 0;
    let release!: () => void;
    const pending = new Promise<void>((resolve) => {
      release = resolve;
    });
    await page.route('**/api/books?*', async (route) => {
      listRequests += 1;
      const response = await route.fetch();
      await pending;
      await route.fulfill({ response });
    });
    let currentAnchor = anchor;
    try {
      // A change the detached library cannot place in its sequence.
      await page.evaluate(() =>
        document.dispatchEvent(
          new CustomEvent('polka:catalog-changed', { detail: { kind: 'coarse' } }),
        ),
      );
      expect(listRequests).toBe(0);

      await page.goBack();
      await expect.poll(() => listRequests).toBe(1);
      await expect(page.locator('#library-grid')).toHaveAttribute('aria-busy', 'true');
      await expect(page.locator('.book-card')).toHaveCount(extent);
      expect(Math.abs((await bookTop(page, '.book-card', anchor.id)) - anchor.top)).toBeLessThan(2);
      await page.locator('.book-card').nth(15).scrollIntoViewIfNeeded();
      currentAnchor = await firstVisibleBook(page, '.book-card');
      expect(currentAnchor.id).not.toBe(anchor.id);
    } finally {
      release();
    }

    await expect(page.locator('#library-grid')).toHaveAttribute('aria-busy', 'false');
    await expect(page.locator('.book-card')).toHaveCount(extent);
    expect(
      Math.abs((await bookTop(page, '.book-card', currentAnchor.id)) - currentAnchor.top),
    ).toBeLessThan(2);
  });

  test('A retained list beyond the API cap survives a failed refresh and continues after retry', async ({
    page,
    browserErrors,
  }) => {
    browserErrors.allow((message) => /Failed to fetch books|503/.test(message));
    await page.addInitScript(() => {
      // Stop at a known extent; automatic paging and anchoring have real-server
      // coverage above and in pagination.spec.ts.
      Object.defineProperty(window, 'IntersectionObserver', { value: undefined });
    });
    const response = await page.request.get('/api/books?limit=1');
    const [sample] = (await response.json()) as BookSummary[];
    const books = Array.from({ length: 1260 }, (_, index) => ({
      ...sample,
      id: index === 1125 ? sample.id : 1000000 + index,
      title: `Retained book ${String(index).padStart(3, '0')}`,
    }));
    await page.route('**/covers/100*', (route) =>
      route.fulfill({
        contentType: 'image/svg+xml',
        body: '<svg xmlns="http://www.w3.org/2000/svg" width="80" height="120"/>',
      }),
    );
    let refreshing = false;
    let failed = false;
    await page.route('**/api/books?*', async (route) => {
      const params = new URL(route.request().url()).searchParams;
      const offset = Number(params.get('offset') || 0);
      // Match the server's cap, including when the caller requests more.
      const limit = Math.min(Number(params.get('limit') || 50), 1000);
      if (refreshing && !failed && offset > 0) {
        failed = true;
        await route.fulfill({ status: 503, body: 'Unavailable' });
        return;
      }
      await route.fulfill({ json: books.slice(offset, offset + limit) });
    });
    await page.goto('/?sort=added');
    const cards = page.locator('.book-card');
    for (let count = 50; count < 1250; count += 50) {
      await expect(cards).toHaveCount(count);
      await page
        .getByRole('button', { name: 'Load more', exact: true })
        .evaluate((button: HTMLButtonElement) => button.click());
    }
    await expect(cards).toHaveCount(1250);
    await cards.nth(1125).locator('.book-title-link').click();
    await expect(page.locator('#book-detail-container')).toBeVisible();
    refreshing = true;
    books[0].title = 'Updated retained book';
    await page.evaluate(() =>
      document.dispatchEvent(
        new CustomEvent('polka:catalog-changed', { detail: { kind: 'coarse' } }),
      ),
    );
    await page.goBack();
    const retry = page.getByRole('button', { name: 'Try again', exact: true });
    await expect(retry).toBeVisible();
    // A failed intermediate page must leave the entire previous range intact.
    await expect(cards).toHaveCount(1250);
    await expect(cards.first().locator('.book-title')).toHaveText('Retained book 000');
    await retry.click();
    await expect(cards.first().locator('.book-title')).toHaveText('Updated retained book');
    await expect(cards).toHaveCount(1250);
    await page.getByRole('button', { name: 'Load more', exact: true }).click();
    await expect(cards).toHaveCount(1260);
    await expect(page.locator('#load-more-container')).toBeHidden();
    const ids = await cards.evaluateAll((elements) =>
      elements.map((el) => el.getAttribute('data-id')),
    );
    expect(ids).toEqual(books.map((book) => String(book.id)));
  });

  test('A removal reaches the retained view and keeps the old neighbourhood', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('.book-card').first()).toBeVisible();
    const extent = await page.locator('.book-card').count();
    const doomedId = await page.locator('.book-card').nth(10).getAttribute('data-id');

    await page.locator('.book-card').nth(20).scrollIntoViewIfNeeded();
    const anchor = await firstVisibleBook(page, '.book-card');
    expect(anchor.id).not.toBe(doomedId);

    await page.locator('.book-card').nth(20).locator('.book-title-link').click();
    await expect(page.locator('#book-detail-container')).toBeVisible();

    await page.evaluate((id) => {
      document.dispatchEvent(
        new CustomEvent('polka:catalog-changed', {
          detail: { kind: 'books-removed', ids: [Number(id)] },
        }),
      );
    }, doomedId);

    await page.goBack();
    await expect(page.locator('.book-card')).toHaveCount(extent - 1);
    await expect(page.locator(`.book-card[data-id="${doomedId}"]`)).toHaveCount(0);
    // A book vanishing above the fold does not move what the reader is looking
    // at: the surviving neighbourhood keeps its place in the viewport.
    expect(Math.abs((await bookTop(page, '.book-card', anchor.id)) - anchor.top)).toBeLessThan(2);
  });

  test('Table view returns to the same rows and position', async ({ page }) => {
    await page.goto('/');
    await page.locator('#view-table-btn').click();
    await expect(page.locator('.library-table')).toBeVisible();

    // Only the rendered row differs from the grid: the table owns its own
    // anchor and focus selector, so a short return is enough here.
    await page.locator('.table-row').nth(30).scrollIntoViewIfNeeded();
    const anchor = await firstVisibleBook(page, '.table-row');

    await page.locator('.table-row').nth(30).locator('.table-title-link').click();
    await expect(page.locator('#book-detail-container')).toBeVisible();
    await page.goBack();

    await expect(page.locator('.library-table')).toBeVisible();
    expect(Math.abs((await bookTop(page, '.table-row', anchor.id)) - anchor.top)).toBeLessThan(2);
    await expect(page.locator('.table-row').nth(30).locator('.table-title-link')).toBeFocused();

    await page.locator('#view-grid-btn').click();
  });
});
