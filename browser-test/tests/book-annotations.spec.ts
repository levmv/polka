import type { Annotation } from '../../frontend/src/types';
import { expect, type Page, test } from './fixtures';
import { findBook } from './helpers';

test.use({ account: 'reader' });

test.describe('Book highlights and notes', () => {
  test('keeps drafts through filtering and save failures, then deletes reviewed notes', async ({
    page,
    browserErrors,
  }) => {
    page.on('dialog', (dialog) => dialog.accept());
    const book = await findBook(page, 'With Cover Book');
    const bookURL = `/book/${book.id}`;
    const assetId = book.assets[0].id;
    const loaded = page.waitForResponse(`**/api/books/${book.id}/annotations`);
    await page.goto(bookURL);
    await loaded;
    const section = page.getByRole('region', { name: 'Highlights & notes', exact: true });
    await expect(section).toBeHidden();
    const early = await createHighlight(
      page,
      assetId,
      2,
      'The question is where reading begins.',
      'Return to this idea',
      'green',
    );
    await createHighlight(page, assetId, 10, 'A later passage connects the two ideas.', '', 'blue');
    await page.reload();
    const quotes = section.locator('.book-annotation-quote');
    await expect(quotes).toHaveText([
      'The question is where reading begins.',
      'A later passage connects the two ideas.',
    ]);
    const first = section.locator(`[data-annotation-id="${early.id}"]`);
    await first.locator('.book-annotation-quote').click();
    const note = first.getByRole('textbox', { name: 'Note', exact: true });
    await note.fill('Draft with <b>literal markup</b>');
    await first.getByRole('radio', { name: 'Purple', exact: true }).check();
    await section.getByRole('button', { name: 'Sort highlights' }).click();
    await page.getByRole('option', { name: 'Newest first' }).click();
    await expect(quotes.first()).toHaveText('A later passage connects the two ideas.');
    const search = section.getByRole('searchbox');
    await search.fill('later');
    await expect(quotes).toHaveCount(1);
    await search.fill('return idea');
    await expect(note).toHaveValue('Draft with <b>literal markup</b>');
    await expect(first.getByRole('radio', { name: 'Purple', exact: true })).toBeChecked();
    await search.fill('');

    const updateURL = `**/api/reader/assets/${assetId}/annotations/${early.id}`;
    browserErrors.allow(
      (message) =>
        message.includes('503') ||
        message.includes('409') ||
        message.includes('Failed to load resource'),
    );
    let rejectSave = true;
    let lostResponse = false;
    await page.route(updateURL, async (route) => {
      if (rejectSave) return route.fulfill({ status: 503, body: 'Please try again' });
      const response = await route.fetch();
      if (!lostResponse) {
        lostResponse = true;
        await route.abort('failed');
      } else await route.fulfill({ response });
    });
    await first.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(first.locator('.annotation-feedback')).toContainText('Please try again');
    await expect(note).toHaveValue('Draft with <b>literal markup</b>');
    rejectSave = false;
    await first.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(note).toBeHidden();
    await page.unroute(updateURL);
    const rows: Annotation[] = await (
      await page.request.get(`/api/reader/assets/${assetId}/annotations`)
    ).json();
    const saved = rows.find((row) => row.id === early.id)!;
    expect(saved.revision).toBe(early.revision + 1);
    await page.reload();
    await expect(first.locator('.book-annotation-note')).toHaveText(
      'Draft with <b>literal markup</b>',
    );
    await expect(first).toHaveCSS('--annotation-color', '#bea5e2');
    await expect(first.locator('b')).toHaveCount(0);

    await quotes.first().click();
    const remote = await page.request.patch(
      `/api/reader/assets/${assetId}/annotations/${early.id}`,
      {
        data: { revision: saved.revision, note: 'Changed before deletion' },
      },
    );
    expect(remote.ok()).toBe(true);
    await first.getByRole('button', { name: 'Delete', exact: true }).click();
    await expect(note).toHaveValue('Changed before deletion');
    await first.getByRole('button', { name: 'Delete', exact: true }).click();
    await expect(quotes).toHaveCount(1);
    await quotes.first().click();
    await section.getByRole('button', { name: 'Delete', exact: true }).click();
    await expect(section).toBeHidden();
    await page.goto(bookURL);
    await expect(section).toBeHidden();
  });

  test('keeps a draft through deletion review and resolves edited or deleted notes', async ({
    page,
    browserErrors,
  }) => {
    page.on('dialog', (dialog) => dialog.accept());
    browserErrors.allow((message) => message.includes('409'));
    const book = await findBook(page, 'With Cover Book');
    const assetId = book.assets[0].id;
    let saved = await createHighlight(
      page,
      assetId,
      2,
      'A thought to return to.',
      'Original note',
      'yellow',
    );
    await page.goto(`/book/${book.id}`);
    const section = page.getByRole('region', { name: 'Highlights & notes', exact: true });
    const item = section.locator(`[data-annotation-id="${saved.id}"]`);
    const quote = item.locator('.book-annotation-quote');
    const note = item.getByRole('textbox', { name: 'Note', exact: true });
    const conflict = item.locator('.annotation-saved-note');
    const url = `/api/reader/assets/${assetId}/annotations/${saved.id}`;
    const remoteEdit = async (changes: { note?: string; color?: string }) => {
      const response = await page.request.patch(url, {
        data: { revision: saved.revision, ...changes },
      });
      expect(response.ok()).toBe(true);
      saved = await response.json();
    };
    const readSaved = async (): Promise<Annotation> => {
      const response = await page.request.get(`/api/reader/assets/${assetId}/annotations`);
      expect(response.ok()).toBe(true);
      return (await response.json())[0];
    };

    // One independent edit checks the HTTP retry path. The merge combinations
    // and sparse-field rules are covered by frontend/test/annotations.test.mjs.
    await quote.click();
    await note.fill('Local note');
    await remoteEdit({ color: 'blue' });
    await item.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(note).toBeHidden();
    saved = await readSaved();
    expect(saved).toMatchObject({ note: 'Local note', color: 'blue' });

    await quote.click();
    await note.fill('Draft to keep');
    await remoteEdit({ note: 'A different interpretation.', color: 'purple' });
    await item.getByRole('button', { name: 'Delete', exact: true }).click();
    await expect(conflict.locator('blockquote')).toHaveText(saved.note!);
    await expect(note).toHaveValue('Draft to keep');
    await item.getByRole('button', { name: 'Overwrite', exact: true }).click();
    await expect(note).toBeHidden();
    saved = await readSaved();
    expect(saved).toMatchObject({ note: 'Draft to keep', color: 'purple' });

    await quote.click();
    await note.fill('Draft to discard');
    await remoteEdit({ note: 'The saved version to use' });
    await item.getByRole('button', { name: 'Save', exact: true }).click();
    await item.getByRole('button', { name: 'Cancel', exact: true }).click();
    await expect(note).toBeHidden();
    await expect(item.locator('.book-annotation-note')).toHaveText(saved.note!);
    expect(await readSaved()).toEqual(saved);

    await quote.click();
    await note.fill('Draft after deletion');
    const deleted = await page.request.delete(url, { data: { revision: saved.revision } });
    expect(deleted.ok()).toBe(true);
    await item.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(section.locator('.book-annotation-note')).toHaveText('Draft after deletion');
    const recreated = await readSaved();
    expect(recreated.id).not.toBe(saved.id);
    expect(recreated).toMatchObject({
      note: 'Draft after deletion',
      color: saved.color,
      quote: saved.quote,
      locator: saved.locator,
    });
  });

  test('pages highlights and exports the whole book while searching', async ({ page }) => {
    const book = await findBook(page, 'With Cover Book');
    const assetId = book.assets[0].id;
    const total = 60;
    for (let index = 0; index < total; index++) {
      await createHighlight(
        page,
        assetId,
        (index + 1) * 2,
        'A passage worth returning to after finishing the book.',
        `Observation ${index + 1}.`,
        'yellow',
      );
    }
    await page.goto(`/book/${book.id}`);
    const section = page.getByRole('region', { name: 'Highlights & notes', exact: true });
    await expect(section.locator('.book-annotations-count')).toHaveText(String(total));
    const items = section.locator('.book-annotation');
    let loaded = await items.count();
    expect(loaded).toBeGreaterThan(0);
    expect(loaded).toBeLessThan(total);
    const scroll = section.getByRole('region', { name: 'Highlights and notes', exact: true });
    expect(await scroll.evaluate((el) => el.scrollHeight > el.clientHeight)).toBe(true);
    // Search includes notes beyond the first rendered page; export ignores the filter.
    await section.getByRole('searchbox').fill(`observation ${total}.`);
    await expect(items).toHaveCount(1);
    await section.getByRole('button', { name: 'Export', exact: true }).click();
    const download = page.waitForEvent('download');
    await page.getByRole('menuitem', { name: 'Export all as Markdown' }).click();
    const stream = await (await download).createReadStream();
    const chunks: Buffer[] = [];
    for await (const chunk of stream!) chunks.push(Buffer.from(chunk));
    const exported = Buffer.concat(chunks).toString();
    expect(exported.match(/^## Highlight /gm)).toHaveLength(total);
    await section.getByRole('searchbox').fill('');
    await expect(items).toHaveCount(loaded);
    while (loaded < total) {
      await section.getByRole('button', { name: 'Show more highlights' }).click();
      await expect.poll(() => items.count()).toBeGreaterThan(loaded);
      loaded = await items.count();
    }
    await expect(items).toHaveCount(total);
    await expect(section.getByRole('button', { name: 'Show more highlights' })).toBeHidden();
    await section.locator('.book-annotation-quote').first().click();
    await page.setViewportSize({ width: 390, height: 844 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(
      390,
    );
  });
});

async function createHighlight(
  page: Page,
  assetId: number,
  position: number,
  quote: string,
  note: string,
  color: string,
): Promise<Annotation> {
  const response = await page.request.post(`/api/reader/assets/${assetId}/annotations`, {
    data: { locator: { cfi: `epubcfi(/6/${position}!/4/2/1:0)` }, quote, note, color },
  });
  expect(response.ok()).toBe(true);
  return response.json();
}
