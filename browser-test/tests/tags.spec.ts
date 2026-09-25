import { expect, test } from './fixtures';
import { findBook } from './helpers';

test('Tags connect catalog filtering, saved shelves and shared edits', async ({ page }) => {
  const first = await findBook(page, 'Foundation');
  const second = await findBook(page, 'With Cover Book');
  for (const [id, tags] of [
    [first.id, 'Speculative, Favourites'],
    [second.id, 'speculative'],
  ] as const) {
    const response = await page.request.patch(`/api/books/${id}`, { data: { tags } });
    expect(response.ok()).toBe(true);
  }
  const created = await page.request.post('/api/shelves', {
    data: { name: 'Tagged books', kind: 'query', query: 'tag:"Speculative"' },
  });
  expect(created.ok()).toBe(true);
  const shelf = await created.json();

  await page.goto('/');
  await page.getByRole('link', { name: 'Genres', exact: true }).click();
  await page
    .getByRole('navigation', { name: 'Dictionary' })
    .getByRole('link', { name: 'Tags', exact: true })
    .click();
  const search = page.getByRole('searchbox', { name: 'Search tags' });
  await search.fill('spec');
  const row = page
    .getByRole('row')
    .filter({ has: page.getByRole('link', { name: 'Speculative', exact: true }) });
  await expect(row.locator('.tag-count')).toHaveText('2');
  await row.getByRole('link').click();
  await expect(page.locator('.book-card')).toHaveCount(2);
  await expect(page).toHaveURL(/q=tag/);
  await page.goBack();
  await expect(search).toBeVisible();

  await page.getByRole('button', { name: 'Edit Speculative', exact: true }).click();
  const rename = page.getByRole('dialog', { name: 'Rename tag' });
  await rename.getByRole('combobox', { name: 'Name', exact: true }).fill('Speculative fiction');
  await rename.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(rename).toBeHidden();
  await expect(page.getByRole('link', { name: 'Speculative fiction', exact: true })).toBeVisible();
  const shelfRow = page.locator('#shelf-nav .shelf-nav-row', { hasText: 'Tagged books' });
  await shelfRow.hover();
  await shelfRow.getByRole('button', { name: 'Actions for Tagged books' }).click();
  await page.getByRole('menuitem', { name: 'Edit', exact: true }).click();
  const editShelf = page.getByRole('dialog', { name: 'Edit saved search' });
  await expect(editShelf.getByLabel('Search query')).toHaveValue('tag:"Speculative fiction"');
  // A query changed in another tab must survive saving only the shelf's name.
  const currentQuery = 'tag:"Speculative fiction" title:"Foundation"';
  const changed = await page.request.patch(`/api/shelves/${shelf.id}`, {
    data: { query: currentQuery },
  });
  expect(changed.ok()).toBe(true);
  await editShelf.getByLabel('Name', { exact: true }).fill('Shared tags');
  await editShelf.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(editShelf).toBeHidden();
  const shelves = await page.request.get('/api/shelves');
  expect(await shelves.json()).toEqual(
    expect.arrayContaining([
      expect.objectContaining({
        id: shelf.id,
        name: 'Shared tags',
        query: currentQuery,
      }),
    ]),
  );

  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole('button', { name: 'Edit Speculative fiction', exact: true }).click();
  await rename.getByRole('combobox', { name: 'Name', exact: true }).fill('Fav');
  await rename.getByRole('option', { name: 'Favourites', exact: true }).click();
  await rename.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(rename).toBeHidden();
  await search.fill('fav');
  const merged = page
    .getByRole('row')
    .filter({ has: page.getByRole('link', { name: 'Favourites', exact: true }) });
  await expect(merged.locator('.tag-count')).toHaveText('2');

  await merged.getByRole('button', { name: 'Delete Favourites', exact: true }).click();
  await page
    .getByRole('dialog', { name: 'Delete tag?' })
    .getByRole('button', { name: 'Delete tag', exact: true })
    .click();
  await expect(page.getByText('No matching tags.', { exact: true })).toBeVisible();
});

test('Genres and tags stay separate through editing and navigation', async ({ page }) => {
  const book = await findBook(page, 'Foundation');
  const other = await findBook(page, 'With Cover Book');
  for (const [id, genres, tags] of [
    [book.id, 'Space Opera', 'Favourite'],
    [other.id, 'Fantasy', 'Space Opera'],
  ] as const) {
    const response = await page.request.patch(`/api/books/${id}`, { data: { genres, tags } });
    expect(response.ok()).toBe(true);
  }
  await page.goto(`/book/${book.id}`);
  await page.locator('#btn-edit-book').click();
  const editor = page.locator('.edit-modal');
  const genres = editor.getByRole('combobox', { name: 'Genres', exact: true });
  const tags = editor.getByRole('combobox', { name: 'Tags', exact: true });
  await expect(genres).toHaveValue('Space Opera');
  await expect(tags).toHaveValue('Favourite');
  await genres.fill('Fantasy, Adventure');
  await editor.locator('.edit-save-btn').click();
  await expect(editor.locator('.save-indicator')).toContainText('Saved');
  await page.keyboard.press('Escape');
  await expect(editor).toBeHidden();
  await expect(
    page.locator(`#book-tags-${book.id}`).getByRole('link', { name: 'Favourite', exact: true }),
  ).toBeVisible();
  await page.locator(`#book-genres-${book.id}`).getByRole('link', { name: 'Fantasy' }).click();
  await expect(page).toHaveURL(/q=genre/);
  await expect(page.locator('.book-card')).toHaveCount(2);

  await page.locator('#view-table-btn').click();
  const tableRow = page.locator(`.table-row[data-id="${book.id}"]`);
  await expect(page.getByRole('columnheader', { name: 'Genres', exact: true })).toBeVisible();
  await expect(tableRow).not.toContainText('Favourite');
  await tableRow.getByRole('button', { name: 'Adventure', exact: true }).click();
  await expect(page.locator('#search-input')).toHaveValue('genre:"Fantasy" genre:"Adventure"');
  await expect(page.locator('.table-row')).toHaveCount(1);
  await expect(tableRow).toContainText('Foundation');
  await page.goto('/tags');
  const dictionary = page.getByRole('navigation', { name: 'Dictionary' });
  await expect(dictionary.getByRole('link', { name: 'Genres' })).toHaveAttribute(
    'aria-current',
    'page',
  );
  await page.getByRole('searchbox', { name: 'Search genres' }).fill('fantasy');
  await expect(
    page
      .getByRole('row')
      .filter({ has: page.getByRole('link', { name: 'Fantasy', exact: true }) })
      .locator('.tag-count'),
  ).toHaveText('2');
  await expect(page.locator('.tags-table tbody tr')).toHaveCount(1);
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.locator('#sidebar')).not.toBeInViewport();
  await page.getByRole('button', { name: 'Edit Fantasy', exact: true }).click();
  const rename = page.getByRole('dialog', { name: 'Rename genre' });
  await rename.getByRole('combobox', { name: 'Name', exact: true }).fill('Speculative fiction');
  await rename.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(rename).toBeHidden();
  await page.getByRole('searchbox', { name: 'Search genres' }).fill('speculative');
  await expect(page.getByRole('link', { name: 'Speculative fiction', exact: true })).toBeVisible();
  await dictionary.getByRole('link', { name: 'Tags', exact: true }).click();
  await page.getByRole('searchbox', { name: 'Search tags' }).fill('space');
  await expect(page.getByRole('link', { name: 'Space Opera', exact: true })).toBeVisible();
  await page.goBack();
  await expect(page.getByRole('searchbox', { name: 'Search genres' })).toBeVisible();
});
