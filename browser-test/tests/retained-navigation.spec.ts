import { expect, type Page, test } from './fixtures';

// Runs against the filler-only library (:8098, 55 books) so "Load more" and a
// scrollable document are available — see playwright.config `pager-chromium`.
//
// What is under test is that Back returns the *same* library instance: the
// accumulated extent, the window position inside it, and keyboard focus all
// survive, and no list request is made. None of that is in the URL.

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
    await expect(page.locator('#load-more-btn')).toBeVisible();
    const firstPage = await page.locator('.book-card').count();

    await page.locator('#load-more-btn').click();
    await expect(page.locator('#load-more-btn')).toBeHidden();
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

    await page.locator('.book-card').nth(firstPage).locator('.book-title-link').click();
    await expect(page).toHaveURL(/\/book\//);
    await expect(page.locator('#book-detail-container')).toBeVisible();
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
    await expect(page.locator('.book-card').nth(firstPage).locator('.book-title-link')).toBeFocused();

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
    await expect(page.locator('#load-more-btn')).toBeVisible();
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
    await expect(page.locator('#load-more-btn')).toBeVisible();
    const firstPage = await page.locator('.book-card').count();
    await page.locator('#load-more-btn').click();
    await expect(page.locator('#load-more-btn')).toBeHidden();
    expect(await page.locator('.book-card').count()).toBeGreaterThan(firstPage);

    await page.locator('.book-card').first().locator('.book-title-link').click();
    await expect(page.locator('#book-detail-container')).toBeVisible();

    // The sidebar Library link is the deliberate escape hatch: an ordinary
    // navigation that rebuilds the list from its first page.
    await page.locator('#nav-library').click();
    await expect(page.locator('#load-more-btn')).toBeVisible();
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
    await expect(page.locator('#load-more-btn')).toBeVisible();
    const extent = await page.locator('.book-card').count();

    const card = page.locator('.book-card').nth(3);
    const bookId = await card.getAttribute('data-id');
    const originalTitle = await card.locator('.book-title').innerText();
    const renamed = `${originalTitle} Renamed`;

    await card.locator('.book-title-link').click();
    await expect(page.locator('.detail-title')).toContainText(originalTitle);

    try {
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
      // The card is updated where it stood: an edit does not reorder or refilter
      // the sequence the reader was browsing, and does not refetch it.
      await expect(page.locator('.book-card').nth(3).locator('.book-title')).toHaveText(renamed);
      await expect(page.locator('.book-card')).toHaveCount(extent);
      expect(listRequests).toBe(0);
    } finally {
      // The fixture library is shared by every test in this project, so the
      // rename is undone even when an assertion above fails.
      await page.request.patch(`/api/books/${bookId}`, { data: { title: originalTitle } });
    }
  });

  test('A coarse change rebuilds the retained view without shrinking or jumping it', async ({
    page,
  }) => {
    await page.goto('/');
    await expect(page.locator('#load-more-btn')).toBeVisible();
    await page.locator('#load-more-btn').click();
    await expect(page.locator('#load-more-btn')).toBeHidden();
    const extent = await page.locator('.book-card').count();

    await page.locator('.book-card').nth(50).scrollIntoViewIfNeeded();
    const anchor = await firstVisibleBook(page, '.book-card');

    await page.locator('.book-card').nth(50).locator('.book-title-link').click();
    await expect(page.locator('#book-detail-container')).toBeVisible();

    let listRequests = 0;
    await page.route('**/api/books?*', async (route) => {
      listRequests += 1;
      await route.continue();
    });

    // A change the detached library cannot place in its sequence.
    await page.evaluate(() =>
      document.dispatchEvent(
        new CustomEvent('polka:catalog-changed', { detail: { kind: 'coarse' } }),
      ),
    );
    // Nothing is fetched while the library is not on screen.
    expect(listRequests).toBe(0);

    await page.goBack();
    // One deferred rebuild, asking for everything that had been loaded: the
    // sequence the reader was browsing does not shrink back to one page, and
    // the position does not jump while it is replaced.
    await expect(page.locator('.book-card')).toHaveCount(extent);
    expect(listRequests).toBe(1);
    expect(Math.abs((await bookTop(page, '.book-card', anchor.id)) - anchor.top)).toBeLessThan(2);
  });

  test('A removal reaches the retained view and keeps the old neighbourhood', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('#load-more-btn')).toBeVisible();
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
          detail: { kind: 'books-removed', ids: [id] },
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

  test('An in-app book opening takes focus; a reload leaves it alone', async ({ page }) => {
    await page.goto('/');
    await page.locator('.book-card').first().locator('.book-title-link').click();
    await expect(page.locator('.detail-title')).toBeVisible();
    // The document did not change under the reader, so nothing but the page
    // itself can say where they now are.
    await expect(page.locator('.detail-title')).toBeFocused();

    await page.reload();
    await expect(page.locator('.detail-title')).toBeVisible();
    // A load the browser performed needs none of that, and taking focus here
    // only paints a focus ring on a heading nobody moved to.
    await expect(page.locator('.detail-title')).not.toBeFocused();
  });

  test('A direct book URL keeps working without a retained parent', async ({ page }) => {
    await page.goto('/');
    const href = await page.locator('.book-card').first().locator('.book-title-link').getAttribute('href');
    await page.goto(href ?? '/');
    await expect(page.locator('#book-detail-container')).toBeVisible();
    // No known predecessor, so Back falls back to the list the URL describes.
    await expect(page.locator('.back-link a')).toHaveAttribute('href', /^\//);
    await page.locator('.back-link a').click();
    await expect(page.locator('.book-card').first()).toBeVisible();
  });
});
