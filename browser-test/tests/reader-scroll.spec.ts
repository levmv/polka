import { paginationEPUB } from './book-fixtures';
import { expect, type Page, test } from './fixtures';
import { importTestBook } from './helpers';

interface ScrollReader extends HTMLElement {
  renderer: HTMLElement & {
    index: number;
    atEnd: boolean;
    start: number;
    size: number;
    getContents(): Array<{ index: number; doc: Document; overlayer?: { element: Element } }>;
    getCurrentContent(): { index: number; doc: Document };
  };
  lastLocation: { cfi: string; range: Range; fraction: number };
  goTo(target: number | string): Promise<unknown>;
  resolveCFI(cfi: string): { index: number; anchor(doc: Document): Range };
  showAnnotation(annotation: { value: string; kind: string }): Promise<void>;
}

test.use({ reducedMotion: 'reduce' });

async function openScrollingBook(page: Page): Promise<void> {
  const book = await importTestBook(page, paginationEPUB({ chapterCount: 18 }));
  const settings = await page.request.put('/api/settings', { data: { reader_flow: 'scrolled' } });
  expect(settings.ok()).toBe(true);
  await page.goto(`/read/${book}`);
  await expect(page.locator('.reader-epub-stage')).toHaveAttribute('data-reader-ready', 'true');
  await expect(page.locator('.reader-page')).toHaveAttribute('data-reader-flow', 'scrolled');
}

async function scrollToLoadedEnd(page: Page): Promise<void> {
  await page.evaluate(() => {
    const view = document.querySelector('foliate-view') as ScrollReader;
    const doc = view.renderer.getCurrentContent().doc;
    const container = doc.defaultView!.frameElement!.parentElement!.parentElement!;
    container.scrollTop = container.scrollHeight;
  });
  await page.waitForTimeout(450);
}

async function currentIndex(page: Page): Promise<number> {
  return page.evaluate(
    () => (document.querySelector('foliate-view') as ScrollReader).renderer.index,
  );
}

async function expectVisiblePassage(
  page: Page,
  passage: { cfi: string; text: string },
): Promise<void> {
  // Check the saved text on screen independently of the reader's reported
  // range. A CFI may begin with whitespace or at an element boundary.
  await expect
    .poll(() =>
      page.evaluate((cfi) => {
        const view = document.querySelector('foliate-view') as ScrollReader;
        const doc = view.renderer.getCurrentContent().doc;
        const anchor = view.resolveCFI(cfi).anchor(doc);
        const walker = doc.createTreeWalker(doc.body, NodeFilter.SHOW_TEXT);
        let visible = false;
        for (let node = walker.nextNode(); node; node = walker.nextNode()) {
          if (!anchor.intersectsNode(node)) continue;
          const text = node as Text;
          const start = node === anchor.startContainer ? anchor.startOffset : 0;
          const end = node === anchor.endContainer ? anchor.endOffset : text.length;
          const offset = text.data.slice(start, end).search(/\S/);
          if (offset < 0) continue;
          const point = doc.createRange();
          point.setStart(node, start + offset);
          point.setEnd(node, start + offset + 1);
          const rect = point.getBoundingClientRect();
          const frame = doc.defaultView!.frameElement!.getBoundingClientRect();
          const stage = view.getBoundingClientRect();
          visible =
            rect.height > 0 &&
            frame.top + rect.top >= stage.top - 1 &&
            frame.top + rect.bottom <= stage.bottom + 1;
          break;
        }
        return {
          visible,
          text: anchor.toString().trim().slice(0, 40),
        };
      }, passage.cfi),
    )
    .toEqual({ visible: true, text: passage.text });
}

test('scrolling crosses chapters, releases documents and restores the saved passage', async ({
  page,
  browserName,
}) => {
  test.setTimeout(30000);
  await openScrollingBook(page);
  const rect = await page.locator('foliate-view').boundingBox();
  expect(rect).not.toBeNull();
  await page.mouse.move(rect!.x + rect!.width / 2, rect!.y + rect!.height / 2);
  // Exercise the browser's wheel path, including Polka's navigation tracking.
  for (let i = 0; i < 4; i++) {
    if (browserName === 'webkit') {
      // Playwright cannot dispatch wheel input to mobile WebKit. A real key
      // starts navigation tracking; native geometry is still exercised below.
      await page.keyboard.press('PageDown');
      await scrollToLoadedEnd(page);
    } else {
      await page.mouse.wheel(0, 5000);
      await page.waitForTimeout(450);
    }
  }
  await expect.poll(() => currentIndex(page)).toBeGreaterThan(1);
  const index = await currentIndex(page);
  const before = await page.evaluate(() => {
    const view = document.querySelector('foliate-view') as ScrollReader;
    return {
      live: view.renderer.getContents().map((x) => x.index),
      cfi: view.lastLocation.cfi,
      text: view.lastLocation.range.toString().trim().slice(0, 40),
    };
  });
  expect(before.live.length).toBeLessThanOrEqual(3);
  expect(before.live[0]).toBeGreaterThan(0);
  await expect(page.locator('[data-reader-progress]')).toHaveText(/^\d+%$/);
  const asset = await page.locator('.reader-page').getAttribute('data-reader-asset-id');
  await expect
    .poll(async () => {
      const response = await page.request.get(`/api/reader/assets/${asset}/position`);
      return (await response.json()).locator.cfi;
    })
    .toBe(before.cfi);
  await page.reload();
  await expect(page.locator('.reader-epub-stage')).toHaveAttribute('data-reader-ready', 'true');
  await expect.poll(() => currentIndex(page)).toBe(index);
  await expectVisiblePassage(page, before);
});

test('ArrowUp/ArrowDown scroll small steps from the stage and book, including key repeats', async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: 'no-preference' });
  await openScrollingBook(page);
  const stage = page.locator('.reader-epub-stage');
  const offset = () =>
    page.evaluate(() => (document.querySelector('foliate-view') as ScrollReader).renderer.start);
  const size = await page.evaluate(
    () => (document.querySelector('foliate-view') as ScrollReader).renderer.size,
  );
  for (const focus of ['stage', 'text']) {
    if (focus === 'stage') await stage.focus();
    else
      await page.evaluate(() => {
        (document.querySelector('foliate-view') as ScrollReader).renderer
          .getCurrentContent()
          .doc.defaultView!.focus();
      });
    const before = await offset();
    await page.keyboard.press('ArrowDown');
    // Native key scrolling settles asynchronously, including after keyup.
    await page.waitForTimeout(350);
    await expect.poll(offset).toBeGreaterThan(before);
    const step = (await offset()) - before;
    expect(step).toBeLessThan(size / 4);
    await page.keyboard.press('ArrowUp');
    await expect.poll(async () => Math.abs((await offset()) - before)).toBeLessThan(1);
  }
  await stage.focus();
  const before = await offset();
  for (let i = 0; i < 6; i++) {
    await page.keyboard.down('ArrowDown');
    await page.waitForTimeout(35);
  }
  await page.keyboard.up('ArrowDown');
  // Allow both the native animation and Foliate's debounced relocation to finish.
  await page.waitForTimeout(650);
  await expect.poll(offset).toBeGreaterThan(before + 150);
  const passage = await page.evaluate(() => {
    const view = document.querySelector('foliate-view') as ScrollReader;
    return {
      cfi: view.lastLocation.cfi,
      text: view.lastLocation.range.toString().trim().slice(0, 40),
    };
  });
  const asset = await page.locator('.reader-page').getAttribute('data-reader-asset-id');
  await expect
    .poll(async () => {
      const response = await page.request.get(`/api/reader/assets/${asset}/position`);
      return (await response.json()).locator.cfi;
    })
    .toBe(passage.cfi);
  await page.reload();
  await expect(stage).toHaveAttribute('data-reader-ready', 'true');
  await expectVisiblePassage(page, passage);
  await page.getByRole('button', { name: 'Search in book' }).click();
  const search = page.getByRole('searchbox', { name: 'Search this book' });
  await search.fill('paragraph');
  const searchOffset = await offset();
  await search.press('ArrowDown');
  await search.press('ArrowUp');
  await expect(search).toBeFocused();
  expect(await offset()).toBe(searchOffset);
});

for (const flow of ['scrolled', 'paginated']) {
  test(`PageUp/PageDown follow reading order in an RTL book (${flow})`, async ({ page }) => {
    const book = await importTestBook(page, paginationEPUB({ rtl: true }));
    const settings = await page.request.put('/api/settings', { data: { reader_flow: flow } });
    expect(settings.ok()).toBe(true);
    await page.goto(`/read/${book}`);
    const stage = page.locator('.reader-epub-stage');
    await expect(stage).toHaveAttribute('data-reader-ready', 'true');
    const progress = () =>
      page.evaluate(
        () => (document.querySelector('foliate-view') as ScrollReader).lastLocation.fraction,
      );
    for (const focus of ['stage', 'text']) {
      for (const key of ['PageDown', 'PageUp']) {
        if (focus === 'stage') await stage.focus();
        else
          await page.evaluate(() => {
            const view = document.querySelector('foliate-view') as ScrollReader;
            view.renderer.getCurrentContent().doc.defaultView!.focus();
          });
        const before = await progress();
        // Foliate retains a short navigation lock after each turn.
        await page.keyboard.press(key, { delay: 150 });
        if (key === 'PageDown') await expect.poll(progress).toBeGreaterThan(before);
        else await expect.poll(progress).toBeLessThan(before);
      }
    }
    await page.getByRole('button', { name: 'Search in book' }).click();
    const search = page.getByRole('searchbox', { name: 'Search this book' });
    await search.fill('paragraph');
    const before = await progress();
    await search.press('PageDown');
    await expect(search).toBeFocused();
    expect(await progress()).toBe(before);
  });
}

test('the final scroll screen saves 100% and reopens at the same passage', async ({ page }) => {
  await openScrollingBook(page);
  await page.evaluate(() => (document.querySelector('foliate-view') as ScrollReader).goTo(17));
  await page.locator('.reader-epub-stage').focus();
  const atEnd = () =>
    page.evaluate(() => (document.querySelector('foliate-view') as ScrollReader).renderer.atEnd);
  for (let i = 0; i < 12 && !(await atEnd()); i++) {
    await page.keyboard.press('PageDown');
    await page.waitForTimeout(150);
  }
  await expect.poll(atEnd).toBe(true);
  await expect(page.locator('[data-reader-progress]')).toHaveText('100%');
  const before = await page.evaluate(() => {
    const view = document.querySelector('foliate-view') as ScrollReader;
    return {
      cfi: view.lastLocation.cfi,
      text: view.lastLocation.range.toString().trim().slice(0, 40),
    };
  });
  expect(before.text.trim()).not.toBe('');
  const asset = await page.locator('.reader-page').getAttribute('data-reader-asset-id');
  await expect
    .poll(async () => {
      const response = await page.request.get(`/api/reader/assets/${asset}/position`);
      const saved = await response.json();
      return { cfi: saved.locator.cfi, progress: saved.progress };
    })
    .toEqual({ cfi: before.cfi, progress: 1 });
  await page.reload();
  await expect(page.locator('.reader-epub-stage')).toHaveAttribute('data-reader-ready', 'true');
  await expectVisiblePassage(page, before);
  await expect(page.locator('[data-reader-progress]')).toHaveText('100%');
});

test('a highlight survives neighbour loading, unloading and a CFI jump back', async ({ page }) => {
  test.setTimeout(30000);
  await openScrollingBook(page);
  await page.evaluate(async () => {
    const view = document.querySelector('foliate-view') as ScrollReader;
    await view.goTo(1);
    const doc = view.renderer.getCurrentContent().doc;
    const text = doc.getElementById('p0')!.firstChild!;
    const range = doc.createRange();
    range.setStart(text, 0);
    range.setEnd(text, Math.min(text.textContent!.length, 70));
    doc.getSelection()!.removeAllRanges();
    doc.getSelection()!.addRange(range);
  });
  const toolbar = page.locator('.reader-selection-toolbar');
  await expect(toolbar).toBeVisible();
  await toolbar.getByRole('button', { name: 'Highlight', exact: true }).click();
  await expect(toolbar).toBeHidden();
  const asset = await page.locator('.reader-page').getAttribute('data-reader-asset-id');
  let cfi = '';
  await expect
    .poll(async () => {
      const response = await page.request.get(`/api/reader/assets/${asset}/annotations`);
      const annotations = await response.json();
      cfi = annotations[0]?.locator.cfi ?? '';
      return annotations.length;
    })
    .toBe(1);
  const rectangles = () =>
    page.evaluate(() => {
      const view = document.querySelector('foliate-view') as ScrollReader;
      return (
        view.renderer
          .getContents()
          .find((x) => x.index === 1)
          ?.overlayer?.element.querySelectorAll('rect').length ?? 0
      );
    });
  await expect.poll(rectangles).toBeGreaterThan(0);
  for (let i = 0; i < 3; i++) await scrollToLoadedEnd(page);
  await expect.poll(rectangles).toBe(0);
  // Reload the highlight's chapter through background preparation as well as
  // explicit navigation. An attached SVG must line up before the first scroll.
  await page.evaluate(() => (document.querySelector('foliate-view') as ScrollReader).goTo(0));
  await scrollToLoadedEnd(page);
  await expect.poll(rectangles).toBeGreaterThan(0);
  await expect
    .poll(() =>
      page.evaluate((cfi) => {
        const view = document.querySelector('foliate-view') as ScrollReader;
        const { doc, overlayer } = view.renderer.getContents().find((x) => x.index === 1)!;
        const range = view.resolveCFI(cfi).anchor(doc);
        const text = range.getClientRects()[0];
        const frame = doc.defaultView!.frameElement!.getBoundingClientRect();
        const painted = overlayer!.element.querySelector('rect')!.getBoundingClientRect();
        return Math.max(
          Math.abs(painted.top - frame.top - text.top),
          Math.abs(painted.left - frame.left - text.left),
        );
      }, cfi),
    )
    .toBeLessThan(2);
  await page.evaluate(
    (value) =>
      (document.querySelector('foliate-view') as ScrollReader).showAnnotation({
        value,
        kind: 'highlight',
      }),
    cfi,
  );
  await expect(toolbar).toBeVisible();
  await expect(toolbar.getByRole('button', { name: 'Add note', exact: true })).toBeVisible();
  await expect(toolbar.getByRole('button', { name: 'Highlight', exact: true })).toBeHidden();
});

test('unopened chapters and their images remain readable when HTTP stops working', async ({
  page,
  browserErrors,
}) => {
  await openScrollingBook(page);
  browserErrors.allow((message) =>
    /Failed to (load resource|fetch)|Load failed|NetworkError/.test(message),
  );
  const attempted: string[] = [];
  await page.route(/^https?:/, (route) => {
    attempted.push(route.request().url());
    return route.abort('internetdisconnected');
  });
  await page.evaluate(() => (document.querySelector('foliate-view') as ScrollReader).goTo(2));
  await expect
    .poll(() =>
      page.evaluate(() => {
        const view = document.querySelector('foliate-view') as ScrollReader;
        const doc = view.renderer.getCurrentContent().doc;
        const image = doc.getElementById('last-illustration') as HTMLImageElement | null;
        return view.renderer.index === 2 && image?.complete && image.naturalWidth > 0;
      }),
    )
    .toBe(true);
  await page.evaluate(() => (document.querySelector('foliate-view') as ScrollReader).goTo(15));
  await expect.poll(() => currentIndex(page)).toBe(15);
  await scrollToLoadedEnd(page);
  await scrollToLoadedEnd(page);
  await expect.poll(() => currentIndex(page)).toBeGreaterThan(15);
  expect(attempted.filter((url) => !url.includes('/api/'))).toEqual([]);
});
