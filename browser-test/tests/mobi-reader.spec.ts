import { readFileSync } from 'node:fs';
import { expect, test } from './fixtures';
import { importTestBook } from './helpers';

test('combo MOBI opens its KF8 text in the reader', async ({ page }) => {
  const bookId = await importTestBook(page, {
    name: 'combo.mobi',
    mimeType: 'application/x-mobipocket-ebook',
    buffer: readFileSync(new URL('../testdata/combo-kf8.mobi', import.meta.url)),
  });
  await page.goto(`/read/${bookId}`);
  await expect(page.locator('.reader-epub-stage')).toHaveAttribute('data-reader-ready', 'true');
  await expect
    .poll(() =>
      page.evaluate(() => {
        const view = document.querySelector('foliate-view') as HTMLElement & {
          renderer?: { getContents?: () => Array<{ doc?: Document }> };
        };
        return view.renderer?.getContents?.()[0]?.doc?.body.textContent;
      }),
    )
    .toContain('Hello Combo KF8');
});
