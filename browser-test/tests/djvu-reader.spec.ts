import { readFileSync } from 'node:fs';
import { expect, test } from './fixtures';
import { importTestBook } from './helpers';

test('DjVu renders pages, follows contents and remembers the reading position', async ({
  page,
  browserName,
  browserErrors,
}) => {
  test.setTimeout(45_000);
  browserErrors.allow(
    (message) =>
      browserName === 'webkit' && message.includes('/position due to access control checks'),
  );
  const bookId = await importTestBook(page, {
    name: 'DjVu reader.djvu',
    mimeType: 'image/vnd.djvu',
    buffer: readFileSync(new URL('../../internal/testfixture/reader.djvu', import.meta.url)),
  });
  const requests: string[] = [];
  page.on('request', (request) => {
    if (request.url().includes('/read/assets/') && request.method() === 'GET') {
      requests.push(request.headers().range ?? 'whole file');
    }
  });
  await page.goto(`/read/${bookId}`);
  const reader = page.locator('.reader-page');
  const stage = page.locator('.reader-paged-stage');
  const surface = page.locator('[data-page-surface]');
  await expect(stage).toHaveAttribute('data-reader-ready', 'true');
  await expect(surface).toHaveAttribute('data-rendered-page', '1');
  await expect(page.locator('[data-page-total]')).toHaveText('3');
  await expect(page.locator('[data-page-text-layer]')).toContainText('First DjVu page');
  expect(requests.length).toBeGreaterThan(0);
  expect(requests.every((range) => range.startsWith('bytes='))).toBe(true);

  // The fixture has a black frame on a white page.
  expect(
    await page.locator('canvas').evaluate((canvas: HTMLCanvasElement) => {
      const context = canvas.getContext('2d')!;
      const frame = context.getImageData(
        Math.round(canvas.width * 0.05),
        Math.round(canvas.height * 0.05),
        1,
        1,
      ).data;
      const paper = context.getImageData(5, 5, 1, 1).data;
      return frame[0] < 80 && paper[0] > 230;
    }),
  ).toBe(true);

  await page.getByRole('button', { name: 'Next page' }).click();
  await expect(surface).toHaveAttribute('data-rendered-page', '2');
  await expect(page.locator('[data-page-text-layer]')).toContainText('Second DjVu page');
  const assetId = Number(await reader.getAttribute('data-reader-asset-id'));
  await expect
    .poll(
      async () =>
        (await (await page.request.get(`/api/reader/assets/${assetId}/position`)).json()).locator,
    )
    .toEqual({ page: 2 });
  await page.getByRole('button', { name: 'Zoom in' }).click();
  await expect(reader).toHaveAttribute('data-reader-zoom', '1.200');
  await expect(surface).toHaveAttribute('data-rendered-page', '2');
  await page.reload();
  await expect(stage).toHaveAttribute('data-reader-ready', 'true');
  await expect(surface).toHaveAttribute('data-rendered-page', '2');
  await expect(reader).toHaveAttribute('data-reader-zoom', '1.200');

  await page.getByRole('button', { name: 'Contents', exact: true }).click();
  await page.getByRole('button', { name: 'Final DjVu page', exact: true }).click();
  await expect(surface).toHaveAttribute('data-rendered-page', '3');
  await expect(page.locator('[data-page-text-layer]')).toContainText('Third DjVu page');
  await page.keyboard.press('Home');
  await page.keyboard.press('ArrowRight');
  await page.keyboard.press('Home');
  await expect(surface).toHaveAttribute('data-rendered-page', '1');
  await expect(page.locator('[data-page-text-layer]')).toContainText('First DjVu page');

  await expect
    .poll(
      async () =>
        (await (await page.request.get(`/api/reader/assets/${assetId}/position`)).json()).locator,
    )
    .toEqual({ page: 1 });
});
