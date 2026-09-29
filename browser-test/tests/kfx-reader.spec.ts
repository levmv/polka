import { readFileSync } from 'node:fs';
import { expect, test } from './fixtures';
import { importTestBook } from './helpers';

test('KFX Japanese text keeps vertical layout, ruby, and navigation', async ({ page }) => {
  await page.setViewportSize({ width: 600, height: 500 });
  const bookId = await importTestBook(page, {
    name: 'japanese.kfx',
    mimeType: 'application/vnd.amazon.ebook',
    buffer: readFileSync(new URL('../testdata/japanese.kfx', import.meta.url)),
  });
  await page.goto(`/read/${bookId}`);
  await expect(page.locator('.reader-epub-stage')).toHaveAttribute('data-reader-ready', 'true');
  await expect
    .poll(() =>
      page.evaluate(() => {
        const view = document.querySelector('foliate-view') as HTMLElement & {
          renderer: { getContents(): Array<{ doc: Document }> };
        };
        const doc = view.renderer.getContents()[0].doc;
        const digits = [...doc.querySelectorAll('span')].find(
          (node) => node.textContent === '12' && node.style.textCombineUpright === 'all',
        );
        const marked = [...doc.querySelectorAll('span')].find(
          (node) => node.textContent === '圏点' && node.style.textEmphasisStyle,
        );
        const emphasis = marked && doc.defaultView?.getComputedStyle(marked);
        return {
          writing: doc.defaultView?.getComputedStyle(doc.body).writingMode,
          ruby: doc.querySelector('ruby rt')?.textContent,
          combined: digits ? doc.defaultView?.getComputedStyle(digits).textCombineUpright : null,
          emphasis: emphasis && [
            emphasis.textEmphasisStyle,
            emphasis.textEmphasisColor,
            emphasis.textEmphasisPosition,
          ],
        };
      }),
    )
    .toEqual({
      writing: 'vertical-rl',
      ruby: 'にほん',
      combined: 'all',
      emphasis: ['sesame', 'rgb(176, 0, 64)', 'under left'],
    });

  await page.locator('[data-reader-toc-toggle]').click();
  await page
    .locator('#reader-toc-panel')
    .getByRole('button', { name: '終わり', exact: true })
    .click();
  await expect
    .poll(() =>
      page.evaluate(() => {
        const view = document.querySelector('foliate-view') as HTMLElement & {
          renderer: { getContents(): Array<{ doc: Document }> };
          lastLocation?: { range: Range };
        };
        const heading = [...view.renderer.getContents()[0].doc.querySelectorAll('h1')].find(
          (node) => node.textContent === '終わり',
        );
        return heading ? view.lastLocation?.range.intersectsNode(heading) : false;
      }),
    )
    .toBe(true);
});

test('KFX imports, reads through EPUB, and keeps its original download', async ({ page }) => {
  await page.setViewportSize({ width: 600, height: 400 });
  const source = readFileSync(new URL('../testdata/reflowable.kfx', import.meta.url));
  const bookId = await importTestBook(page, {
    name: 'reflowable.kfx',
    mimeType: 'application/vnd.amazon.ebook',
    buffer: source,
  });
  await page.goto(`/read/${bookId}`);
  await expect(page.locator('.reader-epub-stage')).toHaveAttribute('data-reader-ready', 'true');
  await expect
    .poll(() =>
      page.evaluate(() => {
        const view = document.querySelector('foliate-view') as HTMLElement & {
          renderer: { getContents(): Array<{ doc: Document }> };
        };
        const doc = view.renderer.getContents()[0].doc;
        return {
          text: doc.body.textContent?.includes('😀 Bold & text. Note'),
          image: doc.querySelector('img')?.naturalWidth,
        };
      }),
    )
    .toEqual({ text: true, image: 4 });

  const atSecondChapter = () =>
    page.evaluate(() => {
      const view = document.querySelector('foliate-view') as HTMLElement & {
        renderer: { getContents(): Array<{ doc: Document }> };
        lastLocation?: { range: Range };
      };
      const heading = [...view.renderer.getContents()[0].doc.querySelectorAll('h1')].find(
        (node) => node.textContent === 'Second chapter',
      );
      return heading ? view.lastLocation?.range.intersectsNode(heading) : false;
    });
  await expect.poll(atSecondChapter).toBe(false);
  await page.locator('[data-reader-toc-toggle]').click();
  await page.locator('#reader-toc-panel').getByRole('button', { name: 'Second chapter' }).click();
  await expect.poll(atSecondChapter).toBe(true);

  const assetId = await page.locator('.reader-page').getAttribute('data-reader-asset-id');
  const original = await page.request.get(`/download/${assetId}`);
  expect(original.ok()).toBe(true);
  expect(await original.body()).toEqual(source);
});

test('KFX comic opens RTL spreads and a centered wide page', async ({ page }) => {
  await page.setViewportSize({ width: 1000, height: 700 });
  const bookId = await importTestBook(page, {
    name: 'fixed.kfx',
    mimeType: 'application/vnd.amazon.ebook',
    buffer: readFileSync(new URL('../testdata/fixed.kfx', import.meta.url)),
  });
  await page.goto(`/read/${bookId}`);
  await expect(page.locator('.reader-epub-stage')).toHaveAttribute('data-reader-ready', 'true');
  const progress = page.locator('[data-reader-progress]');
  await expect(progress).toHaveText('1 / 4');
  for (const number of [2, 4]) {
    await page.locator('[data-reader-toc-toggle]').click();
    await page
      .locator('#reader-toc-panel')
      .getByRole('button', { name: `Page ${number}`, exact: true })
      .click();
    await expect(progress).toHaveText(`${number} / 4`);
    await expect
      .poll(() =>
        page.evaluate(() => {
          const view = document.querySelector('foliate-view') as HTMLElement & {
            renderer: { getContents(): Array<{ doc: Document }> };
          };
          return view.renderer
            .getContents()
            .map(({ doc }) => {
              const image = doc.querySelector('img');
              const frame = doc.defaultView?.frameElement?.getBoundingClientRect();
              return {
                label: image?.naturalWidth && frame?.width ? image.alt : null,
                left: frame?.left ?? 0,
              };
            })
            .sort((a, b) => a.left - b.left)
            .map(({ label }) => label);
        }),
      )
      .toEqual(number === 2 ? ['Page 3', 'Page 2'] : ['Page 4']);
  }
});
