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
  let openedAssets: string[] = [];
  let createdAnnotations: Array<{ assetId: string; annotationId: string }> = [];
  let readerUser: TestUser | null = null;

  test.beforeEach(async ({ page }) => {
    openedAssets = [];
    createdAnnotations = [];
    readerUser = await createReaderTestUser(page, 'reader-selection');
    await loginByRequest(page, readerUser.username, readerUser.password);
  });

  test.afterEach(async ({ page }) => {
    for (const annotation of createdAnnotations) {
      await page.request.delete(
        `/api/reader/assets/${encodeURIComponent(annotation.assetId)}/annotations/${encodeURIComponent(annotation.annotationId)}`,
      );
    }
    for (const assetId of openedAssets) {
      await page.request.put(`/api/reader/assets/${encodeURIComponent(assetId)}/state`, {
        data: { progress: 1, locator: { engine: 'browser-test', id: 'selection-cleanup' } },
      });
    }
    if (readerUser) {
      await deleteTestUserAsAdmin(page, readerUser);
      readerUser = null;
    }
  });

  for (const book of [
    { title: 'With Cover Book', format: 'EPUB' },
    { title: 'FB2 Reader Book', format: 'FB2' },
  ]) {
    test(`copies a ${book.format} selection and dismisses it`, async ({ page }) => {
      await openReader(page, book.title, openedAssets);
      await stubClipboard(page);

      const selected = await selectFirstText(page);
      expect(selected.length).toBeGreaterThan(3);

      const toolbar = page.locator('.reader-selection-toolbar');
      await expect(toolbar).toBeVisible();
      await expect(toolbar).toHaveAttribute('data-reader-selection-cfi', /.+/);

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
  }

  test('creates a persisted highlight from selected text', async ({ page }) => {
    const assetId = await openReader(page, 'With Cover Book', openedAssets);

    const selected = await selectFirstText(page);
    expect(selected.length).toBeGreaterThan(3);

    const toolbar = page.locator('.reader-selection-toolbar');
    await expect(toolbar).toBeVisible();
    await toolbar.getByRole('button', { name: 'Highlight' }).click();
    await expect(toolbar).toBeHidden();

    await expect
      .poll(async () => {
        const res = await page.request.get(
          `/api/reader/assets/${encodeURIComponent(assetId)}/annotations`,
        );
        const rows = (await res.json()) as Array<{ id: string; quote: string }>;
        return rows.length;
      })
      .toBe(1);
    const list = await fetchAnnotations(page, assetId);
    const annotation = list[0];
    createdAnnotations.push({ assetId, annotationId: annotation.id });
    expect(annotation.quote).toBe(selected.replace(/\s+/g, ' ').trim().slice(0, 1200));

    await expect.poll(() => renderedHighlightCount(page)).toBeGreaterThan(0);

    await page.reload();
    await expect
      .poll(async () => page.locator('.reader-epub-stage').getAttribute('data-reader-ready'))
      .toBe('true');
    await expect.poll(() => renderedHighlightCount(page)).toBeGreaterThan(0);

    await showAnnotationPopover(page, annotation.cfi);
    const popover = page.locator('.reader-annotation-popover');
    await expect(popover).toBeVisible();
    await popover.locator('.reader-annotation-note').fill('Reader note');
    await popover.getByRole('button', { name: 'Save' }).click();
    await expect(popover.locator('.reader-annotation-status')).toHaveText('Saved');
    await expect.poll(async () => (await fetchAnnotations(page, assetId))[0]?.note).toBe(
      'Reader note',
    );

    await page.locator('.reader-annotations-toggle').click();
    const panel = page.locator('.reader-annotations-panel');
    await expect(panel).toBeVisible();
    await expect(panel.locator('.reader-annotations-item')).toContainText(annotation.quote);
    await expect(panel.locator('.reader-annotations-item')).toContainText('Reader note');

    const bookURL = await page.locator('.reader-close').getAttribute('href');
    if (!bookURL) throw new Error('missing book detail URL');
    await page.goto(bookURL);
    await expect(page.locator('.detail-title')).toHaveText('With Cover Book');
    await page.getByRole('button', { name: 'More actions' }).click();
    const exportItem = page.getByRole('menuitem', { name: 'Export highlights as HTML' });
    await expect(exportItem).toBeVisible();
    await expect(
      page.getByRole('menuitem', { name: 'Export highlights as Markdown' }),
    ).toBeVisible();
    await page.screenshot({ path: 'screenshots/annotation-export-menu.png', fullPage: true });

    const downloadPromise = page.waitForEvent('download');
    await exportItem.click();
    const download = await downloadPromise;
    expect(download.suggestedFilename()).toBe('With Cover Book - highlights.html');
    const downloadPath = await download.path();
    if (!downloadPath) throw new Error('missing annotation export download');
    const exported = await readFile(downloadPath, 'utf8');
    expect(exported).toContain(annotation.quote);
    expect(exported).toContain('Reader note');
    expect(exported).toContain('<meta http-equiv="Content-Security-Policy"');

    const exportPage = await page.context().newPage();
    await exportPage.setContent(exported);
    await expect(exportPage.getByRole('heading', { name: 'With Cover Book' })).toBeVisible();
    await expect(exportPage.locator('.note')).toHaveText('Reader note');
    await exportPage.screenshot({ path: 'screenshots/annotation-export.png', fullPage: true });
    await exportPage.close();

    await page.getByRole('button', { name: 'More actions' }).click();
    const markdownItem = page.getByRole('menuitem', { name: 'Export highlights as Markdown' });
    const markdownDownloadPromise = page.waitForEvent('download');
    await markdownItem.click();
    const markdownDownload = await markdownDownloadPromise;
    expect(markdownDownload.suggestedFilename()).toBe('With Cover Book - highlights.md');
    const markdownDownloadPath = await markdownDownload.path();
    if (!markdownDownloadPath) throw new Error('missing Markdown annotation export download');
    const markdown = await readFile(markdownDownloadPath, 'utf8');
    expect(markdown).toContain('# With Cover Book');
    expect(markdown).toContain(`> ${annotation.quote}`);
    expect(markdown).toContain('### Note\n\nReader note');
  });

  test('keeps a completed note save out of another highlight popover', async ({ page }) => {
    const assetId = await openReader(page, 'With Cover Book', openedAssets);
    const selectedQuotes: string[] = [];
    for (const textIndex of [0, 1]) {
      selectedQuotes.push(await selectFirstText(page, textIndex));
      const toolbar = page.locator('.reader-selection-toolbar');
      await expect(toolbar).toBeVisible();
      await toolbar.getByRole('button', { name: 'Highlight' }).click();
      await expect.poll(async () => (await fetchAnnotations(page, assetId)).length).toBe(
        textIndex + 1,
      );
      await expect.poll(() => renderedHighlightCount(page)).toBeGreaterThan(textIndex);
    }

    const annotations = await fetchAnnotations(page, assetId);
    for (const annotation of annotations) {
      createdAnnotations.push({ assetId, annotationId: annotation.id });
    }
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
    const noteURL = `**/api/reader/assets/${encodeURIComponent(assetId)}/annotations/${encodeURIComponent(first.id)}`;
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
    await popover.getByRole('button', { name: 'Save' }).click();
    await expect.poll(() => noteRequestHeld).toBe(true);

    await showAnnotationPopover(page, second.cfi);
    await expect(popover.locator('.reader-annotation-quote')).toHaveText(second.quote);
    await expect(popover.locator('.reader-annotation-note')).toHaveValue('');

    releaseResponse();
    await responseDelivered;
    await page.waitForTimeout(50);
    await expect(popover.locator('.reader-annotation-quote')).toHaveText(second.quote);
    await expect(popover.locator('.reader-annotation-note')).toHaveValue('');
    await page.unroute(noteURL);

    const saved = await fetchAnnotations(page, assetId);
    expect(saved.find((annotation) => annotation.id === first.id)?.note).toBe('First note');
    expect(saved.find((annotation) => annotation.id === second.id)?.note || '').toBe('');
  });

  test('dismisses the toolbar on Escape without copying', async ({ page }) => {
    await openReader(page, 'With Cover Book', openedAssets);
    await stubClipboard(page);

    await selectFirstText(page);
    const toolbar = page.locator('.reader-selection-toolbar');
    await expect(toolbar).toBeVisible();

    await page.keyboard.press('Escape');
    await expect(toolbar).toBeHidden();

    const copied = await page.evaluate(
      () => (window as unknown as { __copiedText: string[] }).__copiedText,
    );
    expect(copied).toEqual([]);
  });
});

async function openReader(page: Page, title: string, openedAssets: string[]): Promise<string> {
  await page.goto('/');
  const card = page.locator('.book-card', { hasText: title });
  await expect(card).toBeVisible();
  const href = await card.locator('.book-title-link').getAttribute('href');
  const workId = href?.split('/').pop()?.split('?')[0];
  if (!workId) throw new Error(`missing work id for ${title}`);

  await page.goto(`/read/${workId}`);
  await expect(page.locator('.reader-epub-stage')).toBeVisible();
  await expect
    .poll(async () => page.locator('.reader-epub-stage').getAttribute('data-reader-ready'))
    .toBe('true');

  const assetId = await page.locator('.reader-page').getAttribute('data-reader-asset-id');
  if (!assetId) throw new Error(`missing asset id for ${title}`);
  openedAssets.push(assetId);
  await page.request.put(`/api/reader/assets/${encodeURIComponent(assetId)}/state`, {
    data: { progress: 1, locator: { engine: 'browser-test', id: 'selection-open-cleanup' } },
  });
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
  assetId: string,
): Promise<Array<{ id: string; cfi: string; quote: string; note?: string }>> {
  const res = await page.request.get(
    `/api/reader/assets/${encodeURIComponent(assetId)}/annotations`,
  );
  if (!res.ok()) throw new Error(`annotations status ${res.status()}`);
  return (await res.json()) as Array<{ id: string; cfi: string; quote: string; note?: string }>;
}

async function showAnnotationPopover(page: Page, cfi: string): Promise<void> {
  await page.evaluate(async (value) => {
    const view = document.querySelector('foliate-view') as HTMLElement & {
      showAnnotation?: (annotation: { value: string; kind: string }) => Promise<unknown>;
    };
    await view.showAnnotation?.({ value, kind: 'highlight' });
  }, cfi);
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
