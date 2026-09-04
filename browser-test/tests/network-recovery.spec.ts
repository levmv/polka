import { expect, test } from './fixtures';

test.describe('Network recovery', () => {
  test('Leaving a stalled route does not rely on fetch honoring abort', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('.book-card').first()).toBeVisible();
    await expect(page.locator('body')).not.toHaveClass(/busy/);

    await page.evaluate(() => {
      const nativeFetch = window.fetch.bind(window);
      window.fetch = (input: RequestInfo | URL, init?: RequestInit) => {
        const value = input instanceof Request ? input.url : input.toString();
        if (new URL(value, window.location.href).pathname === '/api/series') {
          document.documentElement.dataset.seriesFetchStarted = 'true';
          // Model the iOS/WebKit failure mode: abort is signalled, but the
          // networking promise itself never settles.
          return new Promise<Response>(() => {});
        }
        return nativeFetch(input, init);
      };
    });

    await page.locator('#nav-series').click();
    await expect(page.getByRole('heading', { name: 'Series' })).toBeVisible();
    await expect(page.locator('html')).toHaveAttribute('data-series-fetch-started', 'true');

    await page.locator('#nav-authors').click();

    await expect(page.getByRole('heading', { name: 'Authors' })).toBeVisible();
    await expect(page.locator('#authors-content')).not.toBeEmpty();
    await expect(page.locator('body')).not.toHaveClass(/busy/);
  });
});
