import { paginationEPUB } from './book-fixtures';
import { expect, type Page, test } from './fixtures';
import { importTestBook, openReader } from './helpers';

interface TestReader extends HTMLElement {
  renderer: HTMLElement & { atEnd: boolean; getContents(): Array<{ doc: Document }> };
  goTo(index: number): Promise<unknown>;
  next(): Promise<void>;
  lastLocation?: { range?: Range };
}

test.use({ reducedMotion: 'reduce' });

test('fixed-layout progress counts document pages in portrait and landscape', async ({ page }) => {
  await page.setViewportSize({ width: 600, height: 800 });
  await openReader(page, 'CBZ Reader Book');
  const progress = page.locator('[data-reader-progress]');
  await expect(progress).toHaveText('1 / 4');
  for (let current = 2; current <= 4; current++) {
    await page.evaluate(() => (document.querySelector('foliate-view') as TestReader).next());
    await expect(progress).toHaveText(`${current} / 4`);
  }
  // Direct navigation within one spread must report the selected page.
  for (const index of [1, 2, 1]) {
    await page.evaluate(
      (index) => (document.querySelector('foliate-view') as TestReader).goTo(index),
      index,
    );
    await expect(progress).toHaveText(`${index + 1} / 4`);
  }
  await page.setViewportSize({ width: 1000, height: 700 });
  await page.evaluate(
    () =>
      new Promise<void>((resolve) =>
        requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
      ),
  );
  await expect(progress).toHaveText('2 / 4');
  await page.evaluate(() => (document.querySelector('foliate-view') as TestReader).next());
  await expect(progress).toHaveText('4 / 4');
  await expect(page.locator('.reader-pagination-measure')).toHaveCount(0);
});

test('screen count follows every turn across sections and reflows around the same text', async ({
  page,
}) => {
  await trackMeasurements(page);
  const book = await importTestBook(page, paginationEPUB());
  await page.goto(`/read/${book}`);
  await expect(page.locator('.reader-epub-stage')).toHaveAttribute('data-reader-ready', 'true');
  const progress = page.locator('[data-reader-progress]');
  await expect(progress).toHaveText(/^1 \/ \d+$/);
  const total = Number((await progress.textContent())?.split(' / ')[1]);
  expect(total).toBeGreaterThan(3);
  expect(total).toBeLessThan(20);
  for (let current = 2; current <= total; current++) {
    // Wait for Foliate's navigation lock before the next turn.
    await page.evaluate(() => (document.querySelector('foliate-view') as TestReader).next());
    await expect(progress).toHaveText(`${current} / ${total}`);
  }
  expect(
    await page.evaluate(
      () => (document.querySelector('foliate-view') as TestReader).renderer.atEnd,
    ),
  ).toBe(true);
  // Lazy images affect pagination even before their section is visited.
  expect(
    await page.evaluate(() => {
      const view = document.querySelector('foliate-view') as TestReader;
      const doc = view.renderer.getContents()[0].doc;
      const image = doc.querySelector<HTMLImageElement>('#last-illustration');
      return (
        image?.complete && image.naturalWidth > 0 && view.lastLocation?.range?.intersectsNode(image)
      );
    }),
  ).toBe(true);
  await expect(page.locator('.reader-pagination-measure')).toHaveCount(0);

  await page.reload();
  await expect(progress).toHaveText(new RegExp(`^\\d+ / ${total}$`));
  expect(await measuredSections(page)).toEqual([]);

  await page.evaluate(async () => {
    const view = document.querySelector('foliate-view') as TestReader;
    await view.goTo(1);
    await view.next();
    const anchor = view.lastLocation?.range?.cloneRange();
    anchor?.collapse(true);
    (window as Window & { paginationAnchor?: Range }).paginationAnchor = anchor;
  });
  await page.locator('[data-reader-display-toggle]').click();
  await page.getByRole('button', { name: 'Larger text', exact: true }).click({ clickCount: 3 });
  await page.keyboard.press('Escape');
  await expect
    .poll(async () => Number((await progress.textContent())?.split(' / ')[1]))
    .toBeGreaterThan(total);
  await expect.poll(() => anchorVisible(page)).toBe(true);

  const enlarged = Number((await progress.textContent())?.split(' / ')[1]);
  const originalViewport = page.viewportSize();
  await expect(page.locator('.reader-pagination-measure')).toHaveCount(0);
  // Resize immediately after navigation to expose stale page geometry.
  await page.evaluate(async () => {
    const view = document.querySelector('foliate-view') as TestReader;
    await view.goTo(1);
    const anchor = view.lastLocation?.range?.cloneRange();
    anchor?.collapse(true);
    (window as Window & { paginationAnchor?: Range }).paginationAnchor = anchor;
  });
  await page.setViewportSize({ width: 600, height: 500 });
  await expect
    .poll(async () => Number((await progress.textContent())?.split(' / ')[1]))
    .toBeGreaterThan(enlarged);
  await expect.poll(() => anchorVisible(page)).toBe(true);
  await expect(page.locator('.reader-pagination-measure')).toHaveCount(0);
  const measuredBeforeReturning = await measuredSections(page);
  if (originalViewport) await page.setViewportSize(originalViewport);
  await expect(progress).toHaveText(new RegExp(`^\\d+ / ${enlarged}$`));
  expect(await measuredSections(page)).toEqual(measuredBeforeReturning);

  await page.locator('[data-reader-display-toggle]').click();
  await page.getByRole('button', { name: 'Scroll', exact: true }).click();
  await expect(progress).toHaveText(/^\d+%$/);
});

test('a superseded background layout cannot replace the new count, including vertical text', async ({
  page,
}) => {
  await trackMeasurements(page, 1);
  const book = await importTestBook(page, paginationEPUB({ vertical: true }));
  await page.goto(`/read/${book}`);
  await expect(page.locator('.reader-epub-stage')).toHaveAttribute('data-reader-ready', 'true');
  await expect(page.locator('.reader-pagination-measure')).toHaveCount(1);
  const progress = page.locator('[data-reader-progress]');
  await expect(progress).toHaveText(/^\d+%$/);
  await expect(progress).toHaveClass(/reader-progress-counting/);
  await page.evaluate(() => (document.querySelector('foliate-view') as TestReader).next());
  await expect(progress).toHaveText(/^\d+%$/);

  await page.setViewportSize({ width: 700, height: 600 });
  await page.evaluate(() =>
    (window as Window & { releasePagination?: () => void }).releasePagination?.(),
  );
  await expect(progress).toHaveText(/^\d+ \/ \d+$/);
  await expect(page.locator('.reader-pagination-measure')).toHaveCount(0);
  const total = Number((await progress.textContent())?.split(' / ')[1]);
  await page.evaluate(() => (document.querySelector('foliate-view') as TestReader).goTo(0));
  await expect(progress).toHaveText(`1 / ${total}`);
  for (let current = 2; current <= total; current++) {
    await page.evaluate(() => (document.querySelector('foliate-view') as TestReader).next());
    await expect(progress).toHaveText(`${current} / ${total}`);
  }
  expect(
    await page.evaluate(
      () => (document.querySelector('foliate-view') as TestReader).renderer.atEnd,
    ),
  ).toBe(true);
});

for (const status of [200, 404]) {
  test(`background pagination waits for imported CSS to settle (${status})`, async ({
    page,
    libraryURL,
    browserErrors,
  }) => {
    const cssURL = `${libraryURL}/pagination-delayed.css`;
    if (status === 404)
      browserErrors.allow(
        (message) => message.includes('pagination-delayed.css') && message.includes('404'),
      );
    let requested = false;
    let release!: () => void;
    const pending = new Promise<void>((resolve) => {
      release = resolve;
    });
    await page.route(cssURL, async (route) => {
      requested = true;
      await pending;
      await route.fulfill({
        status,
        contentType: 'text/css',
        body: status === 200 ? 'p { padding-top: 160px; }' : '',
      });
    });
    try {
      const book = await importTestBook(page, paginationEPUB({ importedStylesheet: cssURL }));
      await page.goto(`/read/${book}`);
      await expect.poll(() => requested).toBe(true);
      const progress = page.locator('[data-reader-progress]');
      await expect(progress).toHaveText(/^\d+%$/);
      await expect(progress).toHaveClass(/reader-progress-counting/);
      // A pending background section must not block page turns.
      await page.evaluate(() => (document.querySelector('foliate-view') as TestReader).next());
      release();
      await expect(progress).toHaveText(/^\d+ \/ \d+$/);
      const total = (await progress.textContent())?.split(' / ')[1];
      await page.evaluate(() => (document.querySelector('foliate-view') as TestReader).goTo(2));
      await expect(progress).toHaveText(new RegExp(`^\\d+ / ${total}$`));
      await expect(page.locator('.reader-pagination-measure')).toHaveCount(0);
    } finally {
      release();
    }
  });
}

test('suspension cancels resource waits, and a deadline leaves unfinished pages uncached', async ({
  page,
  libraryURL,
}) => {
  await page.clock.install();
  const cssURL = `${libraryURL}/pagination-stalled.css`;
  let requests = 0;
  let release!: () => void;
  const pending = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route(cssURL, async (route) => {
    requests++;
    await pending;
    await route.fulfill({ contentType: 'text/css', body: 'p { padding-top: 160px; }' });
  });
  try {
    const book = await importTestBook(page, paginationEPUB({ importedStylesheet: cssURL }));
    await page.goto(`/read/${book}`);
    await expect.poll(() => requests).toBe(1);
    const progress = page.locator('[data-reader-progress]');
    await page.evaluate(() =>
      window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true })),
    );
    await expect(page.locator('.reader-pagination-measure')).toHaveCount(0);
    await page.evaluate(() =>
      window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })),
    );
    await expect.poll(() => requests).toBe(2);
    await page.clock.fastForward(5100);
    await expect(page.locator('.reader-pagination-measure')).toHaveCount(0);
    await expect(progress).toHaveText(/^\d+%$/);
    await expect(progress).not.toHaveClass(/reader-progress-counting/);
    expect(
      await page.evaluate(() => {
        const cache = JSON.parse(localStorage.getItem('polka-pagination') || '{}');
        return cache.entries.every(
          (entry: { counts: Array<number | null> }) => entry.counts[2] === null,
        );
      }),
    ).toBe(true);
    // Visiting the section can finish the count after the background load times out.
    release();
    await page.evaluate(() => (document.querySelector('foliate-view') as TestReader).goTo(2));
    await expect(progress).toHaveText(/^\d+ \/ \d+$/);
  } finally {
    release();
  }
});

test.describe('pagination cache', () => {
  test.skip(
    ({ browserName }) => browserName !== 'chromium',
    'Cache policy is shared by both engines',
  );

  test('unfinished counts survive reopening, and a changed visible layout invalidates the cache', async ({
    page,
  }) => {
    await trackMeasurements(page, 2);
    const book = await importTestBook(page, paginationEPUB());
    await page.goto(`/read/${book}`);
    await expect(page.locator('.reader-epub-stage')).toHaveAttribute('data-reader-ready', 'true');
    await expect.poll(() => measuredSections(page)).toEqual([1, 2]);
    // Only interrupt the first visit.
    await page.evaluate(() => sessionStorage.setItem('pagination-unblocked', 'true'));
    await page.reload();
    const progress = page.locator('[data-reader-progress]');
    await expect(progress).toHaveText(/^\d+ \/ \d+$/);
    expect(await measuredSections(page)).toEqual([2]);
    await expect(page.locator('.reader-pagination-measure')).toHaveCount(0);
    const total = Number((await progress.textContent())?.split(' / ')[1]);

    // Simulate changed system-font metrics: the visible chapter no longer agrees
    // with the stored layout. Counts for the other chapters must be discarded too.
    await page.evaluate(() => {
      const cache = JSON.parse(localStorage.getItem('polka-pagination') || '{}');
      for (const entry of cache.entries)
        entry.counts = entry.counts.map((count: number) => count + 10);
      localStorage.setItem('polka-pagination', JSON.stringify(cache));
    });
    await page.reload();
    await expect(progress).toHaveText(new RegExp(`^\\d+ / ${total}$`));
    expect(await measuredSections(page)).toEqual([1, 2]);
  });
});

async function trackMeasurements(page: Page, pauseBefore?: number): Promise<void> {
  await page.addInitScript((pauseBefore) => {
    const measured: number[] = [];
    Object.assign(window, { measuredPaginationSections: measured });
    const pending = new Promise<void>((resolve) => {
      Object.assign(window, { releasePagination: resolve });
    });
    void customElements.whenDefined('foliate-paginator').then(() => {
      const prototype = customElements.get('foliate-paginator')?.prototype as HTMLElement & {
        goTo(target: { index: number }): Promise<void>;
      };
      const goTo = prototype.goTo;
      prototype.goTo = async function (target) {
        const view = (this.getRootNode() as ShadowRoot).host;
        if (view.closest('.reader-pagination-measure')) {
          measured.push(target.index);
          if (target.index === pauseBefore && !sessionStorage.getItem('pagination-unblocked'))
            await pending;
        }
        return goTo.call(this, target);
      };
    });
  }, pauseBefore);
}

function measuredSections(page: Page): Promise<number[]> {
  return page.evaluate(
    () =>
      (window as Window & { measuredPaginationSections?: number[] }).measuredPaginationSections ??
      [],
  );
}

function anchorVisible(page: Page): Promise<boolean> {
  return page.evaluate(() => {
    const view = document.querySelector('foliate-view') as TestReader;
    const anchor = (window as Window & { paginationAnchor?: Range }).paginationAnchor;
    return (
      !!anchor &&
      !!view.lastLocation?.range?.isPointInRange(anchor.startContainer, anchor.startOffset)
    );
  });
}
