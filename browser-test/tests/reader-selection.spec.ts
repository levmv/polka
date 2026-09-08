import { readFile } from 'node:fs/promises';
import { expect, type Page, test } from './fixtures';
import {
  createReaderTestUser,
  deleteTestUserAsAdmin,
  loginByRequest,
  type TestUser,
} from './helpers';

// Reader selection actions exercise real in-iframe selections. Opening the
// reader records per-user last-read state, so these tests use a temporary
// reader account instead of the shared admin session.
test.describe('Reader selection toolbar', () => {
  let readerUser: TestUser | null = null;

  test.beforeEach(async ({ page }) => {
    readerUser = await createReaderTestUser(page, 'reader-selection');
    await loginByRequest(page, readerUser.username, readerUser.password);
  });

  test.afterEach(async ({ page }) => {
    if (readerUser) {
      await deleteTestUserAsAdmin(page, readerUser);
      readerUser = null;
    }
  });

  test('FB2 contents open and selections can be dismissed or copied', async ({ page }) => {
    await openReader(page, 'FB2 Reader Book');
    await expect(page.locator('.reader-page')).toHaveAttribute('data-reader-format', 'fb2');
    await page.locator('[data-reader-toc-toggle]').click();
    const toc = page.locator('#reader-toc-panel');
    await toc.getByRole('button', { name: 'FB2 Reader Book' }).click();
    await expect(toc).toBeHidden();
    await page.screenshot({ path: 'screenshots/reader-fb2.png', fullPage: true });
    await stubClipboard(page);

    const selected = await selectFirstText(page);
    expect(selected.length).toBeGreaterThan(3);

    const toolbar = page.locator('.reader-selection-toolbar');
    await expect(toolbar).toBeVisible();
    await expect(toolbar).toHaveAttribute('data-reader-selection-cfi', /.+/);

    await page.keyboard.press('Escape');
    await expect(toolbar).toBeHidden();
    expect(await page.evaluate(() => (window as unknown as { __copiedText: string[] }).__copiedText)).toEqual([]);
    await selectFirstText(page);
    await toolbar.getByRole('button', { name: 'Copy' }).click();
    await expect(toolbar).toBeHidden();

    const copied = await page.evaluate(
      () => (window as unknown as { __copiedText: string[] }).__copiedText,
    );
    expect(copied).toEqual([selected]);

    // Copy clears the selection so the next tap turns the page again.
    const stillSelected = await page.evaluate(() => {
      const view = document.querySelector('foliate-view') as HTMLElement & {
        renderer?: { getContents?: () => Array<{ doc?: Document }> };
      };
      return (view.renderer?.getContents?.() || []).some(
        (c) => c.doc && !(c.doc.getSelection()?.isCollapsed ?? true),
      );
    });
    expect(stillSelected).toBe(false);
  });

  test('creates a persisted highlight from selected text', async ({ page }) => {
    test.setTimeout(30000);
    const assetId = await openReader(page, 'With Cover Book');

    const selected = await selectFirstText(page);
    expect(selected.length).toBeGreaterThan(3);

    const toolbar = page.locator('.reader-selection-toolbar');
    await expect(toolbar).toBeVisible();
    await toolbar.getByRole('button', { name: 'Highlight', exact: true }).click();
    await expect(toolbar).toBeHidden();

    await expect
      .poll(async () => {
        const res = await page.request.get(
          `/api/reader/assets/${assetId}/annotations`,
        );
        const rows = (await res.json()) as Array<{ id: number; quote: string }>;
        return rows.length;
      })
      .toBe(1);
    const list = await fetchAnnotations(page, assetId);
    const annotation = list[0];
    expect(annotation.quote).toBe(selected.replace(/\s+/g, ' ').trim().slice(0, 1200));

    await expect.poll(() => renderedHighlightCount(page)).toBeGreaterThan(0);

    await page.reload();
    await expect
      .poll(async () => page.locator('.reader-epub-stage').getAttribute('data-reader-ready'))
      .toBe('true');
    await expect.poll(() => renderedHighlightCount(page)).toBeGreaterThan(0);

    await selectFirstText(page);
    await expect(toolbar).toBeVisible();
    await expect(toolbar.getByRole('button', { name: 'Highlight', exact: true })).toBeHidden();
    await expect(toolbar.getByRole('button', { name: 'Add note' })).toBeVisible();

    await showAnnotationActions(page, annotation.cfi);
    const popover = page.locator('.reader-annotation-popover');
    await expect(popover).toBeHidden();
    await expect(toolbar.getByRole('button', { name: 'Highlight', exact: true })).toBeHidden();
    await expect(toolbar.getByRole('button', { name: 'Search' })).toBeVisible();
    await toolbar.getByRole('button', { name: 'Add note' }).click();
    await expect(popover).toBeVisible();
    const note = popover.locator('.reader-annotation-note');
    await note.focus();
    await page.evaluate(() => {
      window.dispatchEvent(new Event('resize'));
      window.dispatchEvent(new Event('scroll'));
    });
    await expect(popover).toBeVisible();
    await note.fill('Reader note');
    await popover.getByRole('radio', { name: 'Blue', exact: true }).check();
    await pointerDownInReaderDocument(page);
    await expect(popover).toBeHidden();
    await expect.poll(async () => (await fetchAnnotations(page, assetId))[0]?.note).toBe(
      'Reader note',
    );
    await expect.poll(async () => (await fetchAnnotations(page, assetId))[0]?.color).toBe('blue');

    await showAnnotationActions(page, annotation.cfi);
    await expect(toolbar.getByRole('button', { name: 'Edit note' })).toBeVisible();
    await expect(toolbar).toHaveCSS('opacity', '1');
    await page.screenshot({ path: 'screenshots/reader-highlight-actions.png', fullPage: true });

    await page.locator('.reader-annotations-toggle').click();
    const panel = page.locator('.reader-annotations-panel');
    await expect(panel).toBeVisible();
    await expect(panel.locator('.reader-annotations-item')).toContainText(annotation.quote);
    await expect(panel.locator('.reader-annotations-item')).toContainText('Reader note');

    const bookURL = await page.locator('.reader-close').getAttribute('href');
    if (!bookURL) throw new Error('missing book detail URL');
    await page.goto(bookURL);
    await expect(page.locator('.detail-title')).toHaveText('With Cover Book');
    const highlights = page.getByRole('region', { name: 'Highlights & notes', exact: true });
    await expect(highlights.locator('.book-annotation-note')).toHaveText('Reader note');
    await highlights.getByRole('button', { name: 'Export', exact: true }).click();
    const exportItem = page.getByRole('menuitem', { name: 'Export all as HTML' });
    await expect(exportItem).toBeVisible();
    await expect(
      page.getByRole('menuitem', { name: 'Export all as Markdown' }),
    ).toBeVisible();
    await page.screenshot({ path: 'screenshots/annotation-export-menu.png', fullPage: true });

    const downloadPromise = page.waitForEvent('download');
    await exportItem.click();
    const download = await downloadPromise;
    expect(download.suggestedFilename()).toBe('With Cover Book - highlights.html');
    const downloadPath = await download.path();
    if (!downloadPath) throw new Error('missing annotation export download');
    const exported = await readFile(downloadPath, 'utf8');

    const exportPage = await page.context().newPage();
    await exportPage.setContent(exported);
    await expect(exportPage.getByRole('heading', { name: 'With Cover Book' })).toBeVisible();
    await expect(exportPage.locator('.note')).toHaveText('Reader note');
    await exportPage.screenshot({ path: 'screenshots/annotation-export.png', fullPage: true });
    await exportPage.close();

    // A different saved position makes the passage link exercise navigation.
    await page.setViewportSize({ width: 390, height: 600 });
    const saved = await page.request.put(`/api/reader/assets/${assetId}/state`, {
      data: { progress: 0.8, locator: { engine: 'foliate', fraction: 0.8 } },
    });
    expect(saved.ok()).toBe(true);
    await highlights.locator('.book-annotation-quote').click();
    await highlights.getByRole('textbox', { name: 'Note', exact: true }).fill('Updated from book page');
    await highlights.getByRole('radio', { name: 'Green', exact: true }).check();
    await highlights.getByRole('button', { name: 'Open in reader' }).click();
    await expect(page).toHaveURL(new RegExp(`/read/asset/${assetId}#annotation=${annotation.id}$`));
    await expect(page.locator('.reader-epub-stage')).toHaveAttribute('data-reader-ready', 'true');
    await expect.poll(() => page.evaluate((cfi) => {
      const view = document.querySelector('foliate-view') as HTMLElement & {
        lastLocation?: { range?: Range };
        resolveNavigation: (cfi: string) => { index: number; anchor: (doc: Document) => Range };
        renderer?: { getContents?: () => Array<{ index?: number; doc?: Document }> };
      };
      const target = view.resolveNavigation(cfi);
      const doc = view.renderer?.getContents?.().find((content) => content.index === target.index)?.doc;
      if (!doc || !view.lastLocation?.range) return false;
      const range = target.anchor(doc);
      return view.lastLocation.range.isPointInRange(range.startContainer, range.startOffset);
    }, annotation.cfi)).toBe(true);
    await expect.poll(() => renderedHighlightCount(page)).toBeGreaterThan(0);
    await showAnnotationPopover(page, annotation.cfi);
    await expect(popover.locator('.reader-annotation-note')).toHaveValue('Updated from book page');
    await expect(popover.getByRole('radio', { name: 'Green', exact: true })).toBeChecked();
  });

  test('saves a directly-created note before closing the reader', async ({ page }) => {
    const assetId = await openReader(page, 'With Cover Book');
    const selected = await selectFirstText(page);
    const toolbar = page.locator('.reader-selection-toolbar');
    await expect(toolbar).toBeVisible();

    await toolbar.getByRole('button', { name: 'Add note' }).click();
    const popover = page.locator('.reader-annotation-popover');
    await expect(popover).toBeVisible();
    await expect(popover.locator('.reader-annotation-quote')).toHaveText(
      selected.replace(/\s+/g, ' ').trim().slice(0, 1200),
    );
    await page.screenshot({ path: 'screenshots/reader-note-popover.png', fullPage: true });

    const annotations = await fetchAnnotations(page, assetId);
    expect(annotations).toHaveLength(1);
    const annotation = annotations[0];

    let releaseResponse = () => {};
    const responseReleased = new Promise<void>((resolve) => {
      releaseResponse = resolve;
    });
    let noteRequestHeld = false;
    const noteURL = `**/api/reader/assets/${assetId}/annotations/${annotation.id}`;
    await page.route(noteURL, async (route) => {
      if (route.request().method() !== 'PATCH') {
        await route.continue();
        return;
      }
      noteRequestHeld = true;
      const response = await route.fetch();
      await responseReleased;
      await route.fulfill({ response });
    });

    await popover.locator('.reader-annotation-note').fill('Direct note');
    const closeClick = page.locator('.reader-close').click();
    await expect.poll(() => noteRequestHeld).toBe(true);
    await expect(page.locator('.reader-page')).toBeVisible();

    releaseResponse();
    await closeClick;
    await expect(page.locator('.detail-title')).toHaveText('With Cover Book');
    await expect.poll(async () => (await fetchAnnotations(page, assetId))[0]?.note).toBe(
      'Direct note',
    );
  });

  test('deletes a highlight from its action toolbar after confirmation', async ({ page }) => {
    const assetId = await openReader(page, 'With Cover Book');
    await selectFirstText(page);
    const toolbar = page.locator('.reader-selection-toolbar');
    await expect(toolbar).toBeVisible();
    await toolbar.getByRole('button', { name: 'Highlight', exact: true }).click();
    await expect.poll(async () => (await fetchAnnotations(page, assetId)).length).toBe(1);
    await expect.poll(() => renderedHighlightCount(page)).toBeGreaterThan(0);

    const [annotation] = await fetchAnnotations(page, assetId);
    await showAnnotationActions(page, annotation.cfi);
    const deleteButton = toolbar.getByRole('button', { name: 'Delete highlight' });
    await expect(deleteButton).toBeVisible();

    page.once('dialog', async (dialog) => {
      expect(dialog.message()).toBe('Delete this highlight?');
      await dialog.dismiss();
    });
    await deleteButton.click();
    await expect(toolbar).toBeVisible();
    await expect.poll(async () => (await fetchAnnotations(page, assetId)).length).toBe(1);

    page.once('dialog', async (dialog) => {
      expect(dialog.message()).toBe('Delete this highlight?');
      await dialog.accept();
    });
    await deleteButton.click();
    await expect(toolbar).toBeHidden();
    await expect.poll(async () => (await fetchAnnotations(page, assetId)).length).toBe(0);
    await expect.poll(() => renderedHighlightCount(page)).toBe(0);
  });

  test('waits for one note save before opening another editor', async ({ page }) => {
    const assetId = await openReader(page, 'With Cover Book');
    const selectedQuotes: string[] = [];
    for (const textIndex of [0, 1]) {
      selectedQuotes.push(await selectFirstText(page, textIndex));
      const toolbar = page.locator('.reader-selection-toolbar');
      await expect(toolbar).toBeVisible();
      await toolbar.getByRole('button', { name: 'Highlight', exact: true }).click();
      await expect.poll(async () => (await fetchAnnotations(page, assetId)).length).toBe(
        textIndex + 1,
      );
      await expect.poll(() => renderedHighlightCount(page)).toBeGreaterThan(textIndex);
    }

    const annotations = await fetchAnnotations(page, assetId);
    const first = annotations.find((annotation) => annotation.quote === selectedQuotes[0]);
    const second = annotations.find((annotation) => annotation.quote === selectedQuotes[1]);
    if (!first || !second) throw new Error('missing selected annotations');

    // Let the normal reader bootstrap hydrate both persisted overlays before
    // driving their popovers; showAnnotation may otherwise relocate while the
    // just-created overlay is still settling and immediately hide the popover.
    await page.reload();
    await expect
      .poll(async () => page.locator('.reader-epub-stage').getAttribute('data-reader-ready'))
      .toBe('true');

    let releaseResponse = () => {};
    const responseReleased = new Promise<void>((resolve) => {
      releaseResponse = resolve;
    });
    let noteRequestHeld = false;
    let noteResponseDelivered = () => {};
    const responseDelivered = new Promise<void>((resolve) => {
      noteResponseDelivered = resolve;
    });
    const noteURL = `**/api/reader/assets/${assetId}/annotations/${first.id}`;
    await page.route(noteURL, async (route) => {
      if (route.request().method() !== 'PATCH') {
        await route.continue();
        return;
      }
      noteRequestHeld = true;
      const response = await route.fetch();
      await responseReleased;
      await route.fulfill({ response });
      noteResponseDelivered();
    });

    await showAnnotationPopover(page, first.cfi);
    const popover = page.locator('.reader-annotation-popover');
    await popover.locator('.reader-annotation-note').fill('First note');
    await popover.getByRole('button', { name: 'Done' }).click();
    await expect.poll(() => noteRequestHeld).toBe(true);

    await showAnnotationActions(page, second.cfi);
    const toolbar = page.locator('.reader-selection-toolbar');
    await expect(toolbar).toBeHidden();

    releaseResponse();
    await responseDelivered;
    await expect(toolbar).toBeVisible();
    await toolbar.getByRole('button', { name: 'Add note' }).click();
    await expect(popover.locator('.reader-annotation-quote')).toHaveText(second.quote);
    await expect(popover.locator('.reader-annotation-note')).toHaveValue('');
    await page.unroute(noteURL);

    const saved = await fetchAnnotations(page, assetId);
    expect(saved.find((annotation) => annotation.id === first.id)?.note).toBe('First note');
    expect(saved.find((annotation) => annotation.id === second.id)?.note || '').toBe('');
  });
});

async function openReader(page: Page, title: string): Promise<number> {
  await page.goto('/');
  const card = page.locator('.book-card', { hasText: title });
  await expect(card).toBeVisible();
  const href = await card.locator('.book-title-link').getAttribute('href');
  const bookId = href?.split('/').pop()?.split('?')[0];
  if (!bookId) throw new Error(`missing book id for ${title}`);

  await page.goto(`/read/${bookId}`);
  await expect(page.locator('.reader-epub-stage')).toBeVisible();
  await expect
    .poll(async () => page.locator('.reader-epub-stage').getAttribute('data-reader-ready'))
    .toBe('true');

  const assetId = Number(await page.locator('.reader-page').getAttribute('data-reader-asset-id'));
  if (!assetId) throw new Error(`missing asset id for ${title}`);
  return assetId;
}

async function stubClipboard(page: Page): Promise<void> {
  await page.evaluate(() => {
    const win = window as unknown as { __copiedText: string[] };
    win.__copiedText = [];
    if (navigator.clipboard) {
      navigator.clipboard.writeText = async (text: string) => {
        win.__copiedText.push(text);
      };
    }
  });
}

async function pointerDownInReaderDocument(page: Page): Promise<void> {
  await page.evaluate(() => {
    const view = document.querySelector('foliate-view') as HTMLElement & {
      renderer?: { getContents?: () => Array<{ doc?: Document }> };
    };
    const doc = view.renderer?.getContents?.().find((content) => content.doc)?.doc;
    if (!doc) return;
    doc.body.dispatchEvent(
      new doc.defaultView!.PointerEvent('pointerdown', {
        bubbles: true,
        pointerType: 'touch',
        isPrimary: true,
      }),
    );
  });
}

// Select the first meaningful block of text inside a mounted Foliate section
// document, mirroring a real user drag closely enough to trigger the toolbar.
async function selectFirstText(page: Page, targetIndex = 0): Promise<string> {
  return page.evaluate((requestedIndex) => {
    let candidateIndex = 0;
    const view = document.querySelector('foliate-view') as HTMLElement & {
      renderer?: { getContents?: () => Array<{ doc?: Document }> };
    };
    for (const content of view.renderer?.getContents?.() || []) {
      const doc = content.doc;
      if (!doc) continue;
      const candidates = doc.querySelectorAll<HTMLElement>('p, li, h1, h2, blockquote');
      for (const el of candidates) {
        if (!el.textContent || el.textContent.trim().length <= 3) continue;
        if (candidateIndex++ !== requestedIndex) continue;
        const textNode = firstTextNode(doc, el);
        if (!textNode?.textContent?.trim()) continue;
        const range = doc.createRange();
        range.setStart(textNode, 0);
        range.setEnd(textNode, textNode.textContent.length);
        const selection = doc.getSelection();
        if (!selection) continue;
        selection.removeAllRanges();
        selection.addRange(range);
        const text = selection.toString();
        if (text.trim()) return text;
      }
    }
    return '';

    function firstTextNode(doc: Document, root: Node): Text | null {
      const walker = doc.createTreeWalker(root, NodeFilter.SHOW_TEXT);
      let node = walker.nextNode();
      while (node) {
        if (node.textContent?.trim()) return node as Text;
        node = walker.nextNode();
      }
      return null;
    }
  }, targetIndex);
}

async function fetchAnnotations(
  page: Page,
  assetId: number,
): Promise<Array<{ id: number; cfi: string; quote: string; note?: string; color: string }>> {
  const res = await page.request.get(
    `/api/reader/assets/${assetId}/annotations`,
  );
  if (!res.ok()) throw new Error(`annotations status ${res.status()}`);
  return (await res.json()) as Array<{ id: number; cfi: string; quote: string; note?: string; color: string }>;
}

async function showAnnotationActions(page: Page, cfi: string): Promise<void> {
  await page.evaluate(async (value) => {
    const view = document.querySelector('foliate-view') as HTMLElement & {
      showAnnotation?: (annotation: { value: string; kind: string }) => Promise<unknown>;
    };
    await view.showAnnotation?.({ value, kind: 'highlight' });
  }, cfi);
}

async function showAnnotationPopover(page: Page, cfi: string): Promise<void> {
  await showAnnotationActions(page, cfi);
  const toolbar = page.locator('.reader-selection-toolbar');
  await expect(toolbar).toBeVisible();
  await toolbar.getByRole('button', { name: /^(Add|Edit) note$/ }).click();
  await expect(page.locator('.reader-annotation-popover')).toBeVisible();
}

async function renderedHighlightCount(page: Page): Promise<number> {
  return page.evaluate(() => {
    const view = document.querySelector('foliate-view') as HTMLElement & {
      renderer?: {
        getContents?: () => Array<{ doc?: Document; overlayer?: { element?: Element } }>;
      };
    };
    let count = 0;
    for (const content of view.renderer?.getContents?.() || []) {
      count += content.overlayer?.element?.querySelectorAll('rect').length || 0;
      count +=
        content.doc?.querySelectorAll('svg[style*="pointer-events: none"] rect').length || 0;
    }
    return count;
  });
}
