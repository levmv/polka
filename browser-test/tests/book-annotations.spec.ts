import { expect, type Page, test } from './fixtures';
import { createReaderTestUser, deleteTestUserAsAdmin, loginByRequest, type TestUser } from './helpers';
import type { Annotation } from '../../frontend/src/types';

test.describe('Book highlights and notes', () => {
  let reader: TestUser | null = null;

  test.beforeEach(async ({ page }) => {
    reader = await createReaderTestUser(page, 'book-notes');
    await loginByRequest(page, reader.username, reader.password);
  });
  test.afterEach(async ({ page }) => {
    if (reader) {
      await deleteTestUserAsAdmin(page, reader);
      reader = null;
    }
  });

  test('keeps drafts through filtering and save failures, then deletes reviewed notes', async ({ page, browserErrors }) => {
    page.on('dialog', (dialog) => dialog.accept());
    const { bookURL, assetId } = await openBook(page);
    const section = page.getByRole('region', { name: 'Highlights & notes', exact: true });
    await expect(section).toBeHidden();
    const early = await createHighlight(page, assetId, 2, 'The question is where reading begins.', 'Return to this idea', 'green');
    await createHighlight(page, assetId, 10, 'A later passage connects the two ideas.', '', 'blue');
    await page.reload();
    const quotes = section.locator('.book-annotation-quote');
    await expect(quotes).toHaveText(['The question is where reading begins.', 'A later passage connects the two ideas.']);
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
    browserErrors.allow((message) => message.includes('503') || message.includes('409') || message.includes('Failed to load resource'));
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
    const rows: Annotation[] = await (await page.request.get(`/api/reader/assets/${assetId}/annotations`)).json();
    const saved = rows.find((row) => row.id === early.id)!;
    expect(saved.revision).toBe(early.revision + 1);
    await page.reload();
    await expect(first.locator('.book-annotation-note')).toHaveText('Draft with <b>literal markup</b>');
    await expect(first).toHaveCSS('--annotation-color', '#bea5e2');
    await expect(first.locator('b')).toHaveCount(0);

    await quotes.first().click();
    const remote = await page.request.patch(`/api/reader/assets/${assetId}/annotations/${early.id}`, {
      data: { revision: saved.revision, note: 'Changed before deletion' },
    });
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

  test('keeps a draft through deletion review and resolves edited or deleted notes', async ({ page, browserErrors }) => {
    page.on('dialog', (dialog) => dialog.accept());
    browserErrors.allow((message) => message.includes('409'));
    const { assetId } = await openBook(page);
    let saved = await createHighlight(page, assetId, 2, 'A thought to return to.', 'Original note', 'yellow');
    await page.reload();
    const section = page.getByRole('region', { name: 'Highlights & notes', exact: true });
    const item = section.locator(`[data-annotation-id="${saved.id}"]`);
    const quote = item.locator('.book-annotation-quote');
    const note = item.getByRole('textbox', { name: 'Note', exact: true });
    const conflict = item.locator('.annotation-saved-note');
    const url = `/api/reader/assets/${assetId}/annotations/${saved.id}`;
    const remoteEdit = async (changes: { note?: string; color?: string }) => {
      const response = await page.request.patch(url, { data: { revision: saved.revision, ...changes } });
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
      note: 'Draft after deletion', color: saved.color, quote: saved.quote, locator: saved.locator,
    });
  });

  test('browses a hundred highlights and exports the whole book while searching', async ({ page }) => {
    test.setTimeout(30000);
    const { assetId } = await openBook(page);
    const passages = [
      ['A useful note leaves a path back to the thought that prompted it.', 'Connect this with the question in the opening chapter. What changes when we read it a second time?'],
      ['Small observations become clearer when we place them next to one another.', 'A possible structure for the discussion: observation, comparison, then a new question.'],
      ['The empty space between two ideas can be as interesting as either idea alone.', ''],
      ['Reading slowly makes room for questions that a quick summary cannot answer.', 'Try this with the reading group next week.'],
      ['We return to a passage because we have changed since the first reading.', 'The final sentence echoes the introduction, but now the emphasis feels different.'],
    ];
    const colors = ['yellow', 'green', 'blue', 'pink', 'purple'];
    for (let index = 0; index < 100; index++) {
      const [quote, note] = passages[index % passages.length];
      await createHighlight(page, assetId, (index + 1) * 2, quote, note ? `${note}\nObservation ${index + 1}.` : '', colors[index % colors.length]);
    }
    await page.reload();
    const section = page.getByRole('region', { name: 'Highlights & notes', exact: true });
    await expect(section.locator('.book-annotations-count')).toHaveText('100');
    const items = section.locator('.book-annotation');
    let loaded = await items.count();
    expect(loaded).toBeGreaterThan(0);
    expect(loaded).toBeLessThan(100);
    const scroll = section.getByRole('region', { name: 'Highlights and notes', exact: true });
    expect(await scroll.evaluate((el) => el.scrollHeight > el.clientHeight)).toBe(true);
    while (loaded < 100) {
      await section.getByRole('button', { name: 'Show more highlights' }).click();
      await expect.poll(() => items.count()).toBeGreaterThan(loaded);
      loaded = await items.count();
    }
    await expect(items).toHaveCount(100);
    await section.getByRole('searchbox').fill('observation 100.');
    await expect(section.locator('.book-annotation')).toHaveCount(1);
    await section.getByRole('button', { name: 'Export', exact: true }).click();
    const download = page.waitForEvent('download');
    await page.getByRole('menuitem', { name: 'Export all as Markdown' }).click();
    const stream = await (await download).createReadStream();
    const chunks: Buffer[] = [];
    for await (const chunk of stream!) chunks.push(Buffer.from(chunk));
    const exported = Buffer.concat(chunks).toString();
    expect(exported.match(/^## Highlight /gm)).toHaveLength(100);
    await section.getByRole('searchbox').fill('');
    await section.locator('.book-annotation-quote').first().click();
    await page.setViewportSize({ width: 390, height: 844 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
  });
});

async function openBook(page: Page): Promise<{ bookURL: string; assetId: number }> {
  await page.goto('/?q=With%20Cover%20Book');
  const bookURL = await page.locator('.book-card', { hasText: 'With Cover Book' }).locator('.book-title-link').getAttribute('href');
  if (!bookURL) throw new Error('missing book URL');
  const annotationsLoaded = page.waitForResponse((response) => /\/api\/books\/\d+\/annotations$/.test(response.url()));
  await page.goto(bookURL);
  await annotationsLoaded;
  const assetId = Number(await page.locator('[data-reader-progress-asset]').getAttribute('data-reader-progress-asset'));
  return { bookURL, assetId };
}

async function createHighlight(page: Page, assetId: number, position: number, quote: string, note: string, color: string): Promise<Annotation> {
  const response = await page.request.post(`/api/reader/assets/${assetId}/annotations`, {
    data: { locator: { cfi: `epubcfi(/6/${position}!/4/2/1:0)` }, quote, note, color },
  });
  expect(response.ok()).toBe(true);
  return response.json();
}
