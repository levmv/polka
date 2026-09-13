import { expect, type Page, test } from './fixtures';
import { openReader } from './helpers';

test.use({ account: 'reader' });

test.describe('Reader local search', () => {
  test('searches an EPUB and navigates to a result', async ({ page }) => {
    await openReader(page, 'With Cover Book');

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
    await trackReaderTurns(page);
    await page.keyboard.press('Escape');
    await expect(panel).toBeHidden();
    await expect.poll(() => hasFoliateSearchHighlight(page)).toBe(false);
    await page.keyboard.press('Space');
    await expect.poll(() => readerTurnCalls(page)).toEqual({ left: 0, right: 1 });
  });
});

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
