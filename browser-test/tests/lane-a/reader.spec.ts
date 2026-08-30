import {
  epubWithNonstandardZIPSignature,
  epubWithUnmarkedUTF8Entry,
} from '../book-fixtures';
import { expect, test } from '../fixtures';

test.describe('Reader', () => {
  test('EPUB reader supports controls and persists reading state', async ({ page }) => {
    const reader = page.locator('.reader-page');
    const displayToggle = page.locator('[data-reader-display-toggle]');
    let workId = '';
    let assetId = '';

    await test.step('opens with the default layout', async () => {
      await page.goto('/');
      const card = page.locator('.book-card', { hasText: 'With Cover Book' });
      await expect(card).toBeVisible();

      const href = await card.locator('.book-title-link').getAttribute('href');
      if (!href) throw new Error('missing book link');
      // The href carries the ?from= context; the id is the path alone.
      workId = (href.split('/').pop() || '').split('?')[0];
      if (!workId) throw new Error('missing work id');

      await page.goto(`/read/${workId}`);
      await expect(reader).toBeVisible();
      await expect(page.locator('.reader-epub-stage')).toBeVisible();
      await expect(page.locator('foliate-view')).toBeVisible();
      await expect
        .poll(async () => page.locator('.reader-epub-stage').getAttribute('data-reader-ready'))
        .toBe('true');
      await expect(displayToggle).toBeVisible();
      const closeReader = page.getByRole('link', { name: 'Close reader' });
      await expect(closeReader).toBeVisible();
      await expect(closeReader).toHaveAttribute('href', `/book/${workId}`);
      await expect
        .poll(async () => {
          return page.evaluate(() => {
            const actions = document.querySelector('.reader-actions')?.getBoundingClientRect();
            const close = document.querySelector('.reader-close')?.getBoundingClientRect();
            if (!actions || !close) return false;
            return actions.left < close.left;
          });
        })
        .toBe(true);
      await expect(reader).toHaveAttribute('data-reader-flow', 'paginated');
      await expect(reader).toHaveAttribute('data-reader-style', 'paper');
      await expect
        .poll(async () =>
          page.evaluate(() => {
            const view = document.querySelector('foliate-view') as HTMLElement & {
              renderer?: HTMLElement;
            };
            return Number.parseFloat(view?.renderer?.getAttribute('margin') || '');
          }),
        )
        .toBeLessThanOrEqual(48);

      const progressLabel = page.locator('[data-reader-progress]');
      await expect(progressLabel).toHaveText('1 / 2');
    });

    await test.step('handles page turns and reader chrome', async () => {
      await expect
        .poll(async () => {
          return await page.evaluate(() => {
            const reader = document.querySelector<HTMLElement>('.reader-page');
            const view = document.querySelector<HTMLElement>('foliate-view');
            if (!reader || !view) return false;
            reader.classList.remove('reader-chrome-hidden');
            const visible = view.getBoundingClientRect();
            reader.classList.add('reader-chrome-hidden');
            const hidden = view.getBoundingClientRect();
            reader.classList.remove('reader-chrome-hidden');
            return (
              Math.abs(visible.top - hidden.top) < 1 &&
              Math.abs(visible.height - hidden.height) < 1 &&
              Math.abs(visible.bottom - hidden.bottom) < 1
            );
          });
        })
        .toBe(true);
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
      const nearTextLeft = await page.locator('.reader-epub-stage').evaluate((stage) => {
        const rect = stage.getBoundingClientRect();
        const view = stage.querySelector('foliate-view') as HTMLElement & {
          renderer?: HTMLElement;
        };
        const maxInline = Number.parseFloat(
          view.renderer?.getAttribute('max-inline-size') || '760',
        );
        const contentWidth = Math.min(Number.isFinite(maxInline) ? maxInline : 760, rect.width);
        return {
          x: rect.left + (rect.width - contentWidth) / 2 + 8,
          y: rect.top + rect.height / 2,
        };
      });
      await page.mouse.click(nearTextLeft.x, nearTextLeft.y);
      await expect
        .poll(() =>
          page.evaluate(
            () =>
              (window as unknown as { __readerTurnCalls: { left: number; right: number } })
                .__readerTurnCalls,
          ),
        )
        .toEqual({ left: 0, right: 0 });
      await page.mouse.click(80, nearTextLeft.y);
      await expect
        .poll(() =>
          page.evaluate(
            () =>
              (window as unknown as { __readerTurnCalls: { left: number; right: number } })
                .__readerTurnCalls,
          ),
        )
        .toEqual({ left: 1, right: 0 });
      await page.keyboard.press('Space');
      await expect
        .poll(() =>
          page.evaluate(
            () =>
              (window as unknown as { __readerTurnCalls: { left: number; right: number } })
                .__readerTurnCalls,
          ),
        )
        .toEqual({ left: 1, right: 1 });
      await page.keyboard.press('ArrowLeft');
      await expect
        .poll(() =>
          page.evaluate(
            () =>
              (window as unknown as { __readerTurnCalls: { left: number; right: number } })
                .__readerTurnCalls,
          ),
        )
        .toEqual({ left: 2, right: 1 });

      await reader.evaluate((el) => el.classList.add('reader-chrome-hidden'));
      await page.keyboard.press('ArrowRight');
      await expect
        .poll(() =>
          page.evaluate(
            () =>
              (window as unknown as { __readerTurnCalls: { left: number; right: number } })
                .__readerTurnCalls,
          ),
        )
        .toEqual({ left: 2, right: 2 });
      await expect
        .poll(async () =>
          page
            .locator('.reader-page')
            .evaluate((el) => el.classList.contains('reader-chrome-hidden')),
        )
        .toBe(true);
      await page.mouse.click(80, nearTextLeft.y);
      await expect
        .poll(() =>
          page.evaluate(
            () =>
              (window as unknown as { __readerTurnCalls: { left: number; right: number } })
                .__readerTurnCalls,
          ),
        )
        .toEqual({ left: 3, right: 2 });
      await expect
        .poll(async () =>
          page
            .locator('.reader-page')
            .evaluate((el) => el.classList.contains('reader-chrome-hidden')),
        )
        .toBe(true);
      await page.keyboard.press('Escape');
      await expect
        .poll(async () =>
          page
            .locator('.reader-page')
            .evaluate((el) => el.classList.contains('reader-chrome-hidden')),
        )
        .toBe(false);
    });

    await test.step('persists display preferences after reload', async () => {
      await displayToggle.click();
      const displayPanel = page.locator('#reader-display-panel');
      await expect(displayPanel).toBeVisible();
      await expect
        .poll(async () => {
          return page.evaluate(() => {
            const panel = document.querySelector('#reader-display-panel')?.getBoundingClientRect();
            const close = document.querySelector('.reader-close')?.getBoundingClientRect();
            if (!panel || !close) return false;
            return panel.left <= 1 && panel.right < close.left;
          });
        })
        .toBe(true);
      await page.screenshot({ path: 'screenshots/reader-display-panel.png', fullPage: true });
      await displayPanel.getByRole('button', { name: 'Scroll' }).click();
      await expect(reader).toHaveAttribute('data-reader-flow', 'scrolled');
      await displayPanel.getByRole('button', { name: 'Larger text' }).click();
      await expect(reader).toHaveAttribute('data-reader-font-scale', '1');
      await displayPanel.getByRole('button', { name: 'Custom' }).click();
      await expect(reader).toHaveAttribute('data-reader-style', 'custom');
      await expect(displayPanel.locator('.reader-display-custom')).toBeVisible();
      await expect
        .poll(async () => {
          const res = await page.evaluate(async () => {
            const response = await fetch('/api/reader/preferences');
            if (!response.ok) return '';
            const prefs = await response.json();
            return `${prefs.epub_flow}:${prefs.display_style}:${prefs.font_scale}`;
          });
          return res;
        })
        .toBe('scrolled:custom:1');
      await page.keyboard.press('Escape');
      await expect(displayPanel).toBeHidden();
      await page.keyboard.press('Space');
      await expect
        .poll(() =>
          page.evaluate(
            () =>
              (window as unknown as { __readerTurnCalls: { left: number; right: number } })
                .__readerTurnCalls,
          ),
        )
        .toEqual({ left: 3, right: 3 });

      await page.reload();
      await expect(reader).toBeVisible();
      await expect(page.locator('.reader-epub-stage')).toBeVisible();
      await expect
        .poll(async () => page.locator('.reader-epub-stage').getAttribute('data-reader-ready'))
        .toBe('true');
      await expect(reader).toHaveAttribute('data-reader-flow', 'scrolled');
      await expect(reader).toHaveAttribute('data-reader-style', 'custom');
      await expect(reader).toHaveAttribute('data-reader-font-scale', '1');

      await reader.evaluate((el) => el.classList.add('reader-chrome-hidden'));
      await page.mouse.click(640, 360);
      await expect
        .poll(async () =>
          page
            .locator('.reader-page')
            .evaluate((el) => el.classList.contains('reader-chrome-hidden')),
        )
        .toBe(false);
    });

    await test.step('records the last-read state', async () => {
      assetId = (await reader.getAttribute('data-reader-asset-id')) || '';
      if (!assetId) throw new Error('missing reader asset id');

      await expect
        .poll(async () => {
          return await page.evaluate(async (id) => {
            const res = await fetch(`/api/reader/assets/${id}/state`);
            if (!res.ok) return 0;
            const state = await res.json();
            return state.last_read_at || 0;
          }, assetId);
        })
        .toBeGreaterThan(0);

      await page.screenshot({ path: 'screenshots/reader-epub.png', fullPage: true });
    });

    await test.step('projects saved progress onto book detail', async () => {
      await page.goto(`/book/${workId}`);
      await page.evaluate(async (id) => {
        const res = await fetch(`/api/reader/assets/${encodeURIComponent(id)}/state`, {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            progress: 0.42,
            locator: { engine: 'browser-test', id: 'progress' },
          }),
        });
        if (!res.ok) throw new Error(`save reader state status ${res.status}`);
      }, assetId);

      await page.reload();
      const progress = page.locator('#btn-reading-status');
      await expect(progress).toBeVisible();
      await expect(progress).toContainText('Reading · 42%');
      await expect(progress.locator('.detail-reading-progress-fill')).toHaveAttribute(
        'style',
        /42%/,
      );
      await page.screenshot({ path: 'screenshots/book-progress.png', fullPage: true });
    });
  });

  test('Reader normalizes a recoverable EPUB only after direct opening fails', async ({
    page,
  }) => {
    const stamp = Date.now().toString(36);
    const title = `Tolerated EPUB ${stamp}`;
    const author = `Fallback Author ${stamp}`;
    await page.goto('/');
    await page.locator('#book-upload-input').setInputFiles(
      epubWithNonstandardZIPSignature(title, author, `tolerated-epub-${stamp}`),
    );

    const card = page.locator('.book-card', { hasText: title });
    await expect(card).toBeVisible();
    const href = await card.locator('.book-title-link').getAttribute('href');
    const workId = href?.split('/').pop()?.split('?')[0];
    if (!workId) throw new Error('missing tolerated EPUB work id');

    try {
      await page.goto(`/read/${workId}`);
      const reader = page.locator('.reader-page');
      await expect(reader).toHaveAttribute('data-reader-fallback', 'epub-to-kepub');
      await expect
        .poll(async () => page.locator('.reader-epub-stage').getAttribute('data-reader-ready'))
        .toBe('true');
      await expect(page.locator('foliate-view')).toHaveCount(1);
      await expect
        .poll(() =>
          page.evaluate(() => {
            const view = document.querySelector('foliate-view') as HTMLElement & {
              renderer?: { getContents?: () => Array<{ doc?: Document }> };
            };
            return (view.renderer?.getContents?.() || [])
              .map((entry) => entry.doc?.body?.textContent || '')
              .join(' ');
          }),
        )
        .toContain(author);
    } finally {
      const trash = await page.request.post('/api/books/bulk/trash', {
        data: { ids: [workId] },
      });
      expect(trash.ok()).toBeTruthy();
      const purge = await page.request.delete(`/api/books/${encodeURIComponent(workId)}/purge`);
      expect(purge.status()).toBe(204);
    }
  });

  test('Reader resolves an unmarked UTF-8 EPUB entry without rewriting the source', async ({
    page,
  }) => {
    const stamp = Date.now().toString(36);
    const title = `UTF-8 path EPUB ${stamp}`;
    const author = `Unicode Path Author ${stamp}`;
    await page.goto('/');
    await page.locator('#book-upload-input').setInputFiles(
      epubWithUnmarkedUTF8Entry(title, author, `utf8-path-epub-${stamp}`),
    );

    const card = page.locator('.book-card', { hasText: title });
    await expect(card).toBeVisible();
    const href = await card.locator('.book-title-link').getAttribute('href');
    const workId = href?.split('/').pop()?.split('?')[0];
    if (!workId) throw new Error('missing UTF-8 path EPUB work id');

    try {
      await page.goto(`/read/${workId}`);
      const reader = page.locator('.reader-page');
      await expect
        .poll(async () => page.locator('.reader-epub-stage').getAttribute('data-reader-ready'))
        .toBe('true');
      await expect(reader).not.toHaveAttribute('data-reader-fallback', 'epub-to-kepub');
      await expect
        .poll(() =>
          page.evaluate(() => {
            const view = document.querySelector('foliate-view') as HTMLElement & {
              renderer?: { getContents?: () => Array<{ doc?: Document }> };
            };
            return (view.renderer?.getContents?.() || [])
              .map((entry) => entry.doc?.body?.textContent || '')
              .join(' ');
          }),
        )
        .toContain(author);
    } finally {
      const trash = await page.request.post('/api/books/bulk/trash', {
        data: { ids: [workId] },
      });
      expect(trash.ok()).toBeTruthy();
      const purge = await page.request.delete(`/api/books/${encodeURIComponent(workId)}/purge`);
      expect(purge.status()).toBe(204);
    }
  });

  test('FB2 book opens in the foliate reader', async ({ page }) => {
    await page.goto('/');
    const card = page.locator('.book-card', { hasText: 'FB2 Reader Book' });
    await expect(card).toBeVisible();

    const href = await card.locator('.book-title-link').getAttribute('href');
    const workId = href?.split('/').pop();
    if (!workId) throw new Error('missing work id');

    await page.goto(`/read/${workId}`);
    const reader = page.locator('.reader-page');
    await expect(reader).toHaveAttribute('data-reader-format', 'fb2');
    // FB2 shares the foliate stage with EPUB.
    await expect(page.locator('.reader-epub-stage')).toBeVisible();
    await expect(page.locator('foliate-view')).toBeVisible();
    await expect
      .poll(async () => page.locator('.reader-epub-stage').getAttribute('data-reader-ready'))
      .toBe('true');

    // The display panel is available for FB2 too.
    await expect(page.locator('[data-reader-display-toggle]')).toBeVisible();
    const tocToggle = page.locator('[data-reader-toc-toggle]');
    await expect(tocToggle).toBeVisible();
    await tocToggle.click();
    const tocPanel = page.locator('#reader-toc-panel');
    await expect(tocPanel).toBeVisible();
    await tocPanel.getByRole('button', { name: 'FB2 Reader Book' }).click();
    await expect(tocPanel).toBeHidden();

    const assetId = await reader.getAttribute('data-reader-asset-id');
    if (!assetId) throw new Error('missing reader asset id');
    await expect
      .poll(async () =>
        page.evaluate(async (id) => {
          const res = await fetch(`/api/reader/assets/${id}/state`);
          if (!res.ok) return 0;
          return (await res.json()).last_read_at || 0;
        }, assetId),
      )
      .toBeGreaterThan(0);
    await page.screenshot({ path: 'screenshots/reader-fb2.png', fullPage: true });
  });


});
