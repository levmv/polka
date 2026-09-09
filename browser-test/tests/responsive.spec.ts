import { expect, type Page, test } from './fixtures';

// Tablet layout and touch interactions run in Chromium and WebKit.
test.describe('Responsive layout (iPad viewport)', () => {
  test('Drawer controls work across tablet breakpoints', async ({
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

    await toggle.click();
    await page.locator('.account-settings').click();
    await expect(page.locator('.settings-modal')).toBeVisible();
    await expect(sidebar).not.toHaveClass(/open/);
    await expect(overlay).not.toHaveClass(/open/);
    await page.keyboard.press('Escape');
    await expect(page.locator('.settings-modal')).toHaveCount(0);

    await page.setViewportSize({ width: 820, height: 1180 });
    await expect(toggle).toBeVisible();
    await expect.poll(async () => (await sidebar.boundingBox())!.x).toBeLessThan(0);
    await page.setViewportSize({ width: 1024, height: 1180 });
    await expect(toggle).toBeHidden();
    await expect.poll(async () => (await sidebar.boundingBox())!.x).toBeGreaterThanOrEqual(0);
  });

  test('Reader panels restore touch controls and selection survives a page snap', async ({ page }) => {
    await openReadOnlyReader(page);
    const reader = page.locator('.reader-page');

    const panels = [
      {
        toggle: 'Display settings',
        panel: '#reader-display-panel',
        close: 'Close display settings',
      },
      { toggle: 'Search in book', panel: '#reader-search-panel', close: 'Close search' },
      { toggle: 'Highlights', panel: '#reader-annotations-panel', close: 'Close highlights' },
    ];
    for (const item of panels) {
      await page.getByRole('button', { name: item.toggle }).click();
      const panel = page.locator(item.panel);
      await expect(panel).toBeVisible();
      await reader.evaluate((element) => element.classList.add('reader-chrome-hidden'));
      await panel.getByRole('button', { name: item.close }).click();
      await expect(panel).toBeHidden();
      await expect(reader).not.toHaveClass(/reader-chrome-hidden/);
    }

    const selected = await page.evaluate(() => {
      const view = document.querySelector('foliate-view') as HTMLElement & {
        renderer?: { getContents?: () => Array<{ doc?: Document }> };
      };
      const doc = view.renderer?.getContents?.().find(({ doc }) => doc)?.doc;
      const target = doc?.querySelector('p');
      if (!doc || !target) return '';

      const range = doc.createRange();
      range.selectNodeContents(target);
      const selection = doc.getSelection();
      selection?.removeAllRanges();
      selection?.addRange(range);
      doc.dispatchEvent(new doc.defaultView!.Event('selectionchange'));
      return selection?.toString() || '';
    });

    expect(selected.trim().length).toBeGreaterThan(3);
    const toolbar = page.locator('.reader-selection-toolbar');
    await expect(toolbar).toBeVisible();

    await page.evaluate(() => {
      const view = document.querySelector('foliate-view') as HTMLElement & {
        renderer?: { snap?: (vx: number, vy: number) => void };
      };
      view.renderer?.snap?.(0, 0);
    });

    await expect(toolbar).toBeVisible();
  });

  test('The table keeps its width and selection controls inside the viewport', async ({ page }) => {
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

    const overflow = await page.evaluate(() => {
      const grid = document.getElementById('library-grid')!;
      return {
        page: document.documentElement.scrollWidth - document.documentElement.clientWidth,
        table: grid.scrollWidth > grid.clientWidth,
      };
    });
    expect(overflow).toEqual({ page: 0, table: true });

    const rows = page.locator('.table-row');
    const rowCount = await rows.count();
    await page.locator('.table-select-all').check();
    await expect(page.locator('.bulk-bar-count')).toHaveText(`${rowCount} selected`);
    const bar = page.locator('.bulk-bar');
    await expect(bar).toBeInViewport({ ratio: 1 });
    await expect(page.locator('.table-select-row:checked')).toHaveCount(rowCount);
    await page.locator('.table-select-all').uncheck();
    await expect(bar).toHaveCount(0);
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

  test('Book details and tag disclosures fit narrow viewports', async ({ page, browserName }) => {
    const tags = ['Literature', 'Essays', 'Reading', 'Creativity', 'Memory', 'Culture', 'Language', 'Art', 'Philosophy', 'History', 'Education', 'Criticism', 'Nonfiction', 'Writing'];
    await page.route(/\/api\/books\/\d+$/, async (route) => {
      const response = await route.fetch();
      const book = await response.json();
      await route.fulfill({ response, json: {
        ...book,
        tags: tags.join(', '),
        assets: [
          { ...book.assets[0], size: 768 * 1024, page_count: 920 },
          { id: 999999, extension: '.pdf', size: 12.5 * 1024 * 1024, page_count: 48, is_primary: false, can_read: true },
        ],
      } });
    });
    await page.goto('/?q=With%20Cover');
    const card = page.locator('.book-card', { hasText: 'With Cover Book' });
    await expect(card).toBeVisible();
    await card.locator('.book-title').click();
    await expect(page.locator('.detail-title')).toBeVisible();
    await expect(page.locator('.detail-file')).toHaveText(['EPUB (768 KB, ≈ 920 pages)', 'PDF (12.5 MB)']);
    await expect(page.locator('.detail-meta-bottom')).toHaveText(/^Added /);

    const download = page.locator('.detail-actions a.detail-action[href^="/download/"]').first();
    await expect(download).toBeVisible();
    const box = await download.boundingBox();
    expect(box).not.toBeNull();
    // Not clipped off either horizontal edge of the 768px-wide screen.
    expect(box!.x).toBeGreaterThanOrEqual(0);
    expect(box!.x + box!.width).toBeLessThanOrEqual(769);

    // Stacked order below the breakpoint: cover, then title/authors, then the
    // reading state, then publication details, tags, and the description.
    // This guards the display: contents + order rules, which silently lose to
    // the base layout if their @media block is placed before it (equal
    // specificity, source order wins) — and an element left out of the order
    // list keeps the initial 0 and jumps ahead of the cover.
    const coverBox = await page.locator('.detail-cover-image').boundingBox();
    const titleBox = await page.locator('.detail-title').boundingBox();
    const readingBox = await page.locator('.detail-reading-state').boundingBox();
    const railBox = await page.locator('.detail-rail').boundingBox();
    const tagsBox = await page.locator('.detail-tags').boundingBox();
    const descBox = await page.locator('.detail-description').boundingBox();
    expect(coverBox).not.toBeNull();
    expect(titleBox).not.toBeNull();
    expect(readingBox).not.toBeNull();
    expect(railBox).not.toBeNull();
    expect(tagsBox).not.toBeNull();
    expect(descBox).not.toBeNull();
    expect(coverBox!.y).toBeLessThan(titleBox!.y);
    expect(titleBox!.y).toBeLessThan(readingBox!.y);
    expect(readingBox!.y).toBeLessThan(railBox!.y);
    expect(railBox!.y).toBeLessThan(tagsBox!.y);
    expect(tagsBox!.y).toBeLessThan(descBox!.y);

    const tagRow = page.locator('.detail-tags');
    const tagsMore = tagRow.getByRole('button');
    await expect(tagsMore).toBeVisible();
    await page.setViewportSize({ width: 1280, height: 720 });
    await page.screenshot({ animations: 'disabled', path: `screenshots/book-tags-desktop-${browserName}.png`, fullPage: true });
    await tagsMore.click();
    await expect(tagRow.locator('.detail-tag:visible')).toHaveCount(tags.length);
    await page.setViewportSize({ width: 390, height: 844 });
    await expect(tagRow.locator('.detail-tag:visible')).toHaveCount(tags.length);
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
    await page.screenshot({ animations: 'disabled', path: `screenshots/book-tags-mobile-${browserName}.png`, fullPage: true });
  });
});

async function openReadOnlyReader(page: Page): Promise<void> {
  await page.goto('/?q=With%20Cover');
  const href = await page
    .locator('.book-card', { hasText: 'With Cover Book' })
    .locator('.book-title-link')
    .getAttribute('href');
  const bookId = href?.split('/').pop()?.split('?')[0];
  if (!bookId) throw new Error('missing reader book id');

  // Responsive projects share a read-only catalog. Return the current state
  // for the reader's best-effort touch instead of updating it.
  await page.route('**/api/reader/assets/*/touch', async (route) => {
    const stateURL = route.request().url().replace(/\/touch$/, '/state');
    const response = await page.request.get(stateURL);
    await route.fulfill({ response });
  });
  await page.goto(`/read/${bookId}`);
  await expect
    .poll(async () => page.locator('.reader-epub-stage').getAttribute('data-reader-ready'))
    .toBe('true');
}
