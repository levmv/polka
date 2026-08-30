import { expect, test } from './fixtures';

// Runs under the iPad-sized projects against the small fixture library on
// :8099. Covers responsive layout/JS below the tablet breakpoints in both
// Chromium and Playwright's WebKit build.
test.describe('Responsive layout (iPad viewport)', () => {
  test('Sidebar toggles open via hamburger and closed via overlay', async ({
    page,
    browserName,
  }) => {
    await page.goto('/?q=With%20Cover');

    // The hamburger only renders below the breakpoint, so its visibility also
    // confirms we are in the mobile layout.
    const toggle = page.locator('#sidebar-toggle');
    await expect(toggle).toBeVisible();

    const sidebar = page.locator('#app-sidebar');
    const overlay = page.locator('#sidebar-overlay');

    await expect(sidebar).not.toHaveClass(/open/);
    const closedBox = await sidebar.boundingBox();
    expect(closedBox).not.toBeNull();
    expect(closedBox!.x).toBeLessThan(0);

    // Hamburger opens it and reveals the overlay. The slide-in is a CSS
    // transition, so poll until the transform settles on-screen.
    await toggle.click();
    await expect(sidebar).toHaveClass(/open/);
    await expect(overlay).toHaveClass(/open/);
    await expect.poll(async () => (await sidebar.boundingBox())!.x).toBeGreaterThanOrEqual(0);
    await expect(page.locator('#nav-authors')).toBeVisible();

    const libraryActions = page.getByRole('button', { name: 'Manage library' });
    await expect(libraryActions).toBeVisible();
    await libraryActions.click();
    await expect(page.getByRole('menuitem', { name: 'Cleanup' })).toBeVisible();
    await expect(page.getByRole('menuitem', { name: 'Trash' })).toBeVisible();
    await page.keyboard.press('Escape');
    await page.screenshot({ path: `screenshots/sidebar-ipad-${browserName}.png` });

    await overlay.click();
    await expect(sidebar).not.toHaveClass(/open/);
    await expect(overlay).not.toHaveClass(/open/);

  });

  test('Settings opens from the drawer and closes it on the way', async ({ page }) => {
    await page.goto('/');

    const sidebar = page.locator('#app-sidebar');
    const overlay = page.locator('#sidebar-overlay');
    await page.locator('#sidebar-toggle').click();
    await expect(sidebar).toHaveClass(/open/);

    // Or the modal covers a drawer still open behind it.
    await page.locator('.account-settings').click();
    await expect(page.locator('.settings-modal')).toBeVisible();
    await expect(sidebar).not.toHaveClass(/open/);
    await expect(overlay).not.toHaveClass(/open/);
  });

  test('Sidebar drawer breakpoint covers portrait iPad Air but not large portrait tablets', async ({
    page,
  }) => {
    await page.setViewportSize({ width: 820, height: 1180 });
    await page.goto('/?q=With%20Cover');

    const toggle = page.locator('#sidebar-toggle');
    const sidebar = page.locator('#app-sidebar');

    await expect(toggle).toBeVisible();
    const drawerBox = await sidebar.boundingBox();
    expect(drawerBox).not.toBeNull();
    expect(drawerBox!.x).toBeLessThan(0);

    await page.setViewportSize({ width: 1024, height: 1180 });
    await expect(toggle).toBeHidden();
    await expect.poll(async () => (await sidebar.boundingBox())!.x).toBeGreaterThanOrEqual(0);

  });

  test('Switching library views keeps portrait tablet content width stable', async ({ page }) => {
    await page.setViewportSize({ width: 820, height: 1180 });
    await page.addInitScript(() => localStorage.setItem('polka-view-mode', 'grid'));
    await page.goto('/');
    await expect(page.locator('#library-grid.library-grid')).toBeVisible();

    const measure = () =>
      page.evaluate(() => {
        const rect = (selector: string) => {
          const el = document.querySelector<HTMLElement>(selector);
          if (!el) throw new Error(`missing ${selector}`);
          const r = el.getBoundingClientRect();
          return { x: r.x, width: r.width };
        };
        return {
          main: rect('.app-main'),
          content: rect('.app-content'),
          search: rect('.search-row'),
        };
      });

    const grid = await measure();
    await page.locator('#view-table-btn').click();
    await expect(page.locator('.library-table')).toBeVisible();
    const table = await measure();

    for (const key of ['main', 'content', 'search'] as const) {
      expect(Math.abs(table[key].x - grid[key].x)).toBeLessThanOrEqual(1);
      expect(Math.abs(table[key].width - grid[key].width)).toBeLessThanOrEqual(1);
    }

  });

  test('Table view scrolls horizontally instead of squashing columns', async ({ page }) => {
    await page.goto('/?q=With%20Cover');
    await expect(page.locator('.book-card', { hasText: 'With Cover Book' })).toBeVisible();

    await page.locator('#view-table-btn').click();
    await expect(page.locator('.library-table-container')).toBeVisible();

    // The table keeps its min-width rather than squashing columns: it stays
    // wider than the 768px viewport, so the row scrolls horizontally instead of
    // collapsing into unreadable columns.
    const table = page.locator('.library-table');
    const box = await table.boundingBox();
    expect(box).not.toBeNull();
    expect(box!.width).toBeGreaterThanOrEqual(800);
    expect(box!.width).toBeGreaterThan(768);

  });

  test('Large-library title jumps stay beside the grid', async ({ page, browserName }) => {
    await page.route('**/api/books/jumps?sort=title', async (route) => {
      await route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({
          total: 50000,
          items: [
            { label: 'A', offset: 0 },
            { label: 'C', offset: 10000 },
            { label: 'L', offset: 15000 },
            { label: 'N', offset: 20000 },
            { label: 'S', offset: 25000 },
            { label: 'T', offset: 30000 },
          ],
        }),
      });
    });
    await page.addInitScript(() => localStorage.setItem('polka-view-mode', 'grid'));
    await page.goto('/?sort=title');

    const rail = page.getByRole('navigation', { name: 'Jump through books' });
    await expect(rail).toBeVisible();
    const railBox = await rail.boundingBox();
    const rightCardBox = await page.locator('.book-card').nth(2).boundingBox();
    expect(railBox).not.toBeNull();
    expect(rightCardBox).not.toBeNull();
    expect(railBox!.x + railBox!.width).toBeLessThanOrEqual(768);
    expect(rightCardBox!.x + rightCardBox!.width).toBeLessThan(railBox!.x);

    await page.screenshot({
      path: `screenshots/jump-rail-ipad-${browserName}.png`,
      fullPage: true,
    });
  });

  test('Book page download button stays within the viewport', async ({ page }) => {
    await page.goto('/?q=With%20Cover');
    const card = page.locator('.book-card', { hasText: 'With Cover Book' });
    await expect(card).toBeVisible();
    await card.locator('.book-title').click();
    await expect(page.locator('.detail-title')).toBeVisible();

    const download = page.locator('.detail-actions a.detail-action[href^="/download/"]').first();
    await expect(download).toBeVisible();
    const box = await download.boundingBox();
    expect(box).not.toBeNull();
    // Not clipped off either horizontal edge of the 768px-wide screen.
    expect(box!.x).toBeGreaterThanOrEqual(0);
    expect(box!.x + box!.width).toBeLessThanOrEqual(769);

    // Stacked order below the breakpoint: cover, then title/authors, then the
    // reading state, then the cover rail (Details + tags), then the description.
    // This guards the display: contents + order rules, which silently lose to
    // the base layout if their @media block is placed before it (equal
    // specificity, source order wins) — and an element left out of the order
    // list keeps the initial 0 and jumps ahead of the cover.
    const coverBox = await page.locator('.detail-cover-image').boundingBox();
    const titleBox = await page.locator('.detail-title').boundingBox();
    const readingBox = await page.locator('.detail-reading-state').boundingBox();
    const railBox = await page.locator('.detail-rail').boundingBox();
    const descBox = await page.locator('.detail-description').boundingBox();
    expect(coverBox).not.toBeNull();
    expect(titleBox).not.toBeNull();
    expect(readingBox).not.toBeNull();
    expect(railBox).not.toBeNull();
    expect(descBox).not.toBeNull();
    expect(coverBox!.y).toBeLessThan(titleBox!.y);
    expect(titleBox!.y).toBeLessThan(readingBox!.y);
    expect(readingBox!.y).toBeLessThan(railBox!.y);
    expect(railBox!.y).toBeLessThan(descBox!.y);
  });
});
