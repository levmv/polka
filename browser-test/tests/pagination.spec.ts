import { expect, test } from './fixtures';

// Runs against the filler-only library (:8098, 55 books) — see playwright.config
// `pager-chromium` and the Makefile. The main fixture suite stays below the
// 50/page threshold, so the pager can only be exercised here.
test.describe('Library pagination', () => {
  test('Load more appends the next page, then hides', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('.book-card').first()).toBeVisible();

    await expect(page.locator('.book-card')).toHaveCount(50);

    const loadMore = page.locator('#load-more-btn');
    await expect(loadMore).toBeVisible();

    await loadMore.click();

    await expect(page.locator('.book-card')).toHaveCount(55);
    await expect(page.locator('#load-more-container')).toBeHidden();

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
