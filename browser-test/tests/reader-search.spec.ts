import { expect, type Page, test } from './fixtures';
import {
  createReaderTestUser,
  deleteTestUserAsAdmin,
  loginByRequest,
  type TestUser,
} from './helpers';

test.describe('Reader local search', () => {
  let openedAssets: string[] = [];
  let readerUser: TestUser | null = null;

  test.beforeEach(async ({ page }) => {
    openedAssets = [];
    readerUser = await createReaderTestUser(page, 'reader-search');
    await loginByRequest(page, readerUser.username, readerUser.password);
  });

  test.afterEach(async ({ page }) => {
    for (const assetId of openedAssets) {
      await page.request.put(`/api/reader/assets/${encodeURIComponent(assetId)}/state`, {
        data: { progress: 1, locator: { engine: 'browser-test', id: 'search-cleanup' } },
      });
    }
    if (readerUser) {
      await deleteTestUserAsAdmin(page, readerUser);
      readerUser = null;
    }
  });

  test('searches an EPUB and navigates to a result', async ({ page, browserName }) => {
    await openReader(page, 'With Cover Book', openedAssets);

    await page.getByRole('button', { name: 'Search in book' }).click();
    const panel = page.locator('#reader-search-panel');
    await expect(panel).toBeVisible();
    const searchInput = panel.getByRole('searchbox', { name: 'Search this book' });

    await searchInput.fill('a');
    await expect(panel.locator('.reader-search-status')).toHaveText(
      'Type 2 characters, or one Han ideograph.',
    );
    await searchInput.fill('本');
    await searchInput.press('Enter');
    await expect(panel.locator('.reader-search-status')).toHaveText('No matches.');

    const query = await firstSearchableWord(page);
    expect(query.length).toBeGreaterThan(2);
    await searchInput.fill(query);
    await searchInput.press('Enter');

    const result = panel.locator('.reader-search-result-btn').first();
    await expect(result).toBeVisible();
    await expect(panel.locator('.reader-search-status')).toContainText(/result/);

    await result.click();
    await expect(result).toHaveClass(/active/);
    await expect(panel).toHaveAttribute('data-reader-search-results', /[1-9]/);
    await expect.poll(() => hasFoliateSearchHighlight(page)).toBe(true);
    await page.screenshot({
      path: `screenshots/reader-search-highlight-${browserName}.png`,
      fullPage: true,
    });
    await page.keyboard.press('Escape');
    await expect(panel).toBeHidden();
    await expect.poll(() => hasFoliateSearchHighlight(page)).toBe(false);
  });

  test('opens search from selected text', async ({ page }) => {
    await openReader(page, 'With Cover Book', openedAssets);

    const selected = await selectFirstText(page);
    const word = selected.trim().split(/\s+/)[0];
    expect(word.length).toBeGreaterThan(2);

    const toolbar = page.locator('.reader-selection-toolbar');
    await expect(toolbar).toBeVisible();
    await toolbar.getByRole('button', { name: 'Search' }).click();

    const panel = page.locator('#reader-search-panel');
    await expect(panel).toBeVisible();
    await expect(panel.getByRole('searchbox', { name: 'Search this book' })).toHaveValue(
      selected.replace(/\s+/g, ' ').trim().slice(0, 120),
    );
    await expect(panel.locator('.reader-search-result-btn').first()).toBeVisible();
  });

  test('restores page-turn keys after closing search', async ({ page }) => {
    await openReader(page, 'With Cover Book', openedAssets);
    await trackReaderTurns(page);

    await page.getByRole('button', { name: 'Search in book' }).click();
    const panel = page.locator('#reader-search-panel');
    await expect(panel).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(panel).toBeHidden();

    await page.keyboard.press('Space');
    await expect.poll(() => readerTurnCalls(page)).toEqual({ left: 0, right: 1 });
  });
});

async function openReader(page: Page, title: string, openedAssets: string[]): Promise<void> {
  await page.goto('/');
  const card = page.locator('.book-card', { hasText: title });
  await expect(card).toBeVisible();
  const href = await card.locator('.book-title-link').getAttribute('href');
  const workId = href?.split('/').pop();
  if (!workId) throw new Error(`missing work id for ${title}`);

  await page.goto(`/read/${workId}`);
  await expect(page.locator('.reader-epub-stage')).toBeVisible();
  await expect
    .poll(async () => page.locator('.reader-epub-stage').getAttribute('data-reader-ready'))
    .toBe('true');

  const assetId = await page.locator('.reader-page').getAttribute('data-reader-asset-id');
  if (assetId) {
    openedAssets.push(assetId);
    await page.request.put(`/api/reader/assets/${encodeURIComponent(assetId)}/state`, {
      data: { progress: 1, locator: { engine: 'browser-test', id: 'search-open-cleanup' } },
    });
  }
}

async function firstSearchableWord(page: Page): Promise<string> {
  return page.evaluate(() => {
    const view = document.querySelector('foliate-view') as HTMLElement & {
      renderer?: { getContents?: () => Array<{ doc?: Document }> };
    };
    for (const content of view.renderer?.getContents?.() || []) {
      const doc = content.doc;
      if (!doc) continue;
      const text = Array.from(doc.querySelectorAll<HTMLElement>('p, li, h1, h2, blockquote'))
        .map((el) => el.textContent || '')
        .join(' ');
      const word = text.match(/[A-Za-z]{4,}/)?.[0];
      if (word) return word;
    }
    return '';
  });
}

async function trackReaderTurns(page: Page): Promise<void> {
  await page.evaluate(() => {
    const view = document.querySelector('foliate-view') as HTMLElement & {
      goLeft: () => Promise<void>;
      goRight: () => Promise<void>;
    };
    const calls = { left: 0, right: 0 };
    const goLeft = view.goLeft.bind(view);
    const goRight = view.goRight.bind(view);
    view.goLeft = async () => {
      calls.left += 1;
      await goLeft();
    };
    view.goRight = async () => {
      calls.right += 1;
      await goRight();
    };
    (window as unknown as { __readerTurnCalls: typeof calls }).__readerTurnCalls = calls;
  });
}

async function readerTurnCalls(page: Page): Promise<{ left: number; right: number }> {
  return page.evaluate(
    () =>
      (window as unknown as { __readerTurnCalls: { left: number; right: number } })
        .__readerTurnCalls,
  );
}

async function hasFoliateSearchHighlight(page: Page): Promise<boolean> {
  return page.evaluate(() => {
    const view = document.querySelector('foliate-view') as HTMLElement & {
      renderer?: {
        getContents?: () => Array<{
          overlayer?: { element?: SVGSVGElement };
        }>;
      };
    };
    for (const content of view.renderer?.getContents?.() || []) {
      const marker = content.overlayer?.element?.querySelector<SVGGElement>(
        '[data-polka-search-highlight="true"]',
      );
      if (marker) return true;
    }
    return false;
  });
}

async function selectFirstText(page: Page): Promise<string> {
  return page.evaluate(() => {
    const view = document.querySelector('foliate-view') as HTMLElement & {
      renderer?: { getContents?: () => Array<{ doc?: Document }> };
    };
    for (const content of view.renderer?.getContents?.() || []) {
      const doc = content.doc;
      if (!doc) continue;
      const candidates = doc.querySelectorAll<HTMLElement>('p, li, h1, h2, blockquote');
      for (const el of candidates) {
        if (!el.textContent || el.textContent.trim().length <= 3) continue;
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
  });
}
