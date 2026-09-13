import { Buffer } from 'node:buffer';
import { epubWithScripts } from './book-fixtures';
import { expect, test } from './fixtures';

test('an EPUB cannot execute an uploaded text file as a same-origin script', async ({
  page,
  browserErrors,
}) => {
  browserErrors.allow(
    (message) =>
      message.includes('Executing inline script violates the following Content Security Policy') ||
      (message.includes('Refused to execute script from') && message.includes('MIME type')),
  );
  const textResponse = await page.request.post('/api/import', {
    multipart: {
      book: {
        name: 'script-probe.txt',
        mimeType: 'text/plain',
        buffer: Buffer.from("parent.document.documentElement.dataset.bookScript = 'external';"),
      },
    },
  });
  expect(textResponse.ok()).toBeTruthy();
  const textBook = await textResponse.json();
  const scriptURL = new URL(`/download/${textBook.asset_id}`, textResponse.url()).href;
  const epubResponse = await page.request.post('/api/import', {
    multipart: {
      book: epubWithScripts('Script probe', scriptURL, 'script-probe'),
    },
  });
  expect(epubResponse.ok()).toBeTruthy();
  const book = await epubResponse.json();
  const scriptResponse = page.waitForResponse(
    (response) => response.url() === scriptURL && response.request().resourceType() === 'script',
  );
  await page.goto(`/read/${book.book.id}`);
  const resource = await scriptResponse;
  expect(resource.status()).toBe(200);
  await resource.finished();
  await expect(page.locator('.reader-epub-stage')).toHaveAttribute('data-reader-ready', 'true');
  expect(await page.evaluate(() => document.documentElement.dataset.bookScript)).toBeUndefined();
  // Blocking execution must not strip or rewrite the user's downloaded file.
  const download = await page.request.get(scriptURL);
  expect(download.ok()).toBeTruthy();
  expect(await download.text()).toBe(
    "parent.document.documentElement.dataset.bookScript = 'external';",
  );
});
