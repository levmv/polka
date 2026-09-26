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
    .locator('.tag-row')
    .filter({ has: page.getByRole('link', { name: 'Speculative', exact: true }) });
  await expect(row.locator('.tag-count')).toHaveText('2');
  await row.locator('.tag-name-link').click();
  await expect(page.locator('.book-card')).toHaveCount(2);
  await page.goBack();

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
    .locator('.tag-row')
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
    [book.id, 'Fiction.Science Fiction.Space Opera', 'Status.Favourite, .NET, A..B'],
    [other.id, 'Fiction.Fantasy', 'Space Opera'],
  ] as const) {
    const response = await page.request.patch(`/api/books/${id}`, { data: { genres, tags } });
    expect(response.ok()).toBe(true);
  }
  await page.goto(`/book/${book.id}`);
  await page.locator('#btn-edit-book').click();
  const editor = page.locator('.edit-modal');
  const genres = editor.getByRole('combobox', { name: 'Genres', exact: true });
  const tags = editor.getByRole('combobox', { name: 'Tags', exact: true });
  await expect(genres).toHaveValue('Fiction.Science Fiction.Space Opera');
  await expect(tags).toHaveValue('Status.Favourite, .NET, A..B');
  await genres.fill('Fiction.Fantasy, Fiction.Adventure, History.France, Travel.france');
  await editor.locator('.edit-save-btn').click();
  await expect(editor.locator('.save-indicator')).toContainText('Saved');
  await page.keyboard.press('Escape');
  await expect(editor).toBeHidden();
  await expect(page.locator(`#book-tags-${book.id} .detail-tag`)).toHaveText([
    'Favourite',
    '.NET',
    'A..B',
  ]);
  await page.locator(`#book-genres-${book.id}`).getByRole('link', { name: 'Fantasy' }).click();
  await expect(page).toHaveURL((url) => url.searchParams.get('q') === 'genre:"Fiction.Fantasy"');
  await expect(page.locator('.book-card')).toHaveCount(2);

  await page.getByRole('button', { name: 'Table view', exact: true }).click();
  const tableRow = page.locator(`.table-row[data-id="${book.id}"]`);
  await expect(tableRow).not.toContainText('Favourite');
  await tableRow.getByRole('button', { name: 'Show 1 more genre', exact: true }).click();
  await expect(tableRow.locator('.table-genre-text')).toHaveText([
    'Fantasy',
    'Adventure',
    'History.France',
    'Travel.france',
  ]);
  const adventure = tableRow.getByRole('button', { name: 'Adventure', exact: true });
  await expect(adventure).toHaveAttribute('title', 'Fiction.Adventure');
  await adventure.click();
  await expect(page.locator('#search-input')).toHaveValue(
    'genre:"Fiction.Fantasy" genre:"Fiction.Adventure"',
  );
  await expect(page.locator('.table-row')).toHaveCount(1);
  await expect(tableRow).toContainText('Foundation');
  await page.goto('/tags');
  const dictionary = page.getByRole('navigation', { name: 'Dictionary' });
  await page.getByRole('searchbox', { name: 'Search genres' }).fill('fantasy');
  await expect(
    page
      .locator('.tag-row')
      .filter({ has: page.getByRole('link', { name: 'Fiction.Fantasy', exact: true }) })
      .locator('.tag-count'),
  ).toHaveText('2');
  await expect(page.locator('.tag-row')).toHaveCount(1);
  await dictionary.getByRole('link', { name: 'Tags', exact: true }).click();
  await page.getByRole('searchbox', { name: 'Search tags' }).fill('space');
  await expect(page.getByRole('link', { name: 'Space Opera', exact: true })).toBeVisible();
});

test('Genre count links filter branches and edits refresh the tree', async ({ page }) => {
  const first = await findBook(page, 'Foundation');
  const second = await findBook(page, 'With Cover Book');
  for (const [id, genres] of [
    [first.id, 'Fiction.Science Fiction.Space Opera'],
    [second.id, 'Fiction.Fantasy'],
  ] as const) {
    const response = await page.request.patch(`/api/books/${id}`, { data: { genres } });
    expect(response.ok()).toBe(true);
  }
  await page.goto('/tags');
  const fiction = page.locator('.tag-row', {
    has: page.locator('.tag-main').filter({ hasText: /^Fiction$/ }),
  });
  await fiction.locator('.tag-count').click();
  await expect(page.locator('#search-input')).toHaveValue('genre:"Fiction"');
  await expect(page.locator('.book-card')).toHaveCount(2);

  await page.goto('/tags');
  await page.getByRole('button', { name: 'List view', exact: true }).click();
  await page.getByRole('button', { name: 'Expand Fiction', exact: true }).click();
  await page.getByRole('button', { name: 'Edit Fiction.Science Fiction', exact: true }).click();
  const rename = page.getByRole('dialog', { name: 'Rename genre' });
  await rename
    .getByRole('combobox', { name: 'Name', exact: true })
    .fill('Literature.Science Fiction');
  await rename.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(rename).toBeHidden();
  await page.getByRole('button', { name: 'Expand Literature', exact: true }).click();
  await page
    .getByRole('button', { name: 'Expand Literature.Science Fiction', exact: true })
    .click();
  await page.getByRole('link', { name: 'Space Opera', exact: true }).click();
  await expect(page.locator('#search-input')).toHaveValue(
    'genre:"Literature.Science Fiction.Space Opera"',
  );
  await expect(page.locator('.book-title')).toHaveText('Foundation');
});

test('Collapsing a loading branch does not lose nested genres', async ({ page }) => {
  const book = await findBook(page, 'Foundation');
  const updated = await page.request.patch(`/api/books/${book.id}`, {
    data: { genres: 'Fiction.Science Fiction.Space Opera' },
  });
  expect(updated.ok()).toBe(true);
  const response = await page.request.get('/api/tags/list?kind=genre&q=Science');
  expect(response.ok()).toBe(true);
  const { items } = await response.json();
  const scienceID = items.find(
    (tag: { name: string }) => tag.name === 'Fiction.Science Fiction',
  ).id;

  let release!: () => void;
  const pending = new Promise<void>((resolve) => {
    release = resolve;
  });
  let loading = false;
  await page.route('**/api/tags/list?**', async (route) => {
    if (new URL(route.request().url()).searchParams.get('parent') === String(scienceID)) {
      loading = true;
      await pending;
    }
    await route.continue();
  });
  try {
    await page.goto('/tags');
    await page.getByRole('button', { name: 'List view', exact: true }).click();
    await page.getByRole('button', { name: 'Expand Fiction', exact: true }).click();
    await page.getByRole('button', { name: 'Expand Fiction.Science Fiction', exact: true }).click();
    await expect.poll(() => loading).toBe(true);

    await page.getByRole('button', { name: 'Collapse Fiction', exact: true }).click();
    await page.getByRole('button', { name: 'Expand Fiction', exact: true }).click();
    await page
      .getByRole('button', { name: 'Collapse Fiction.Science Fiction', exact: true })
      .click();
    await page.getByRole('button', { name: 'Expand Fiction.Science Fiction', exact: true }).click();
    release();

    await expect(page.getByRole('link', { name: 'Space Opera', exact: true })).toBeVisible();
  } finally {
    release();
    await page.unrouteAll({ behavior: 'wait' });
  }
});

test('Tag grid navigation and list expansion keep separate positions', async ({ page }) => {
  const first = await findBook(page, 'Foundation');
  const second = await findBook(page, 'With Cover Book');
  for (const [id, genres] of [
    [first.id, 'Fiction.Science Fiction.Space Opera, Fiction.Mystery, History.Medieval, Adventure'],
    [second.id, 'Fiction.Fantasy, Poetry'],
  ] as const) {
    const response = await page.request.patch(`/api/books/${id}`, { data: { genres } });
    expect(response.ok()).toBe(true);
  }
  await page.goto('/tags');
  await page.getByRole('button', { name: 'Open Fiction', exact: true }).click();
  const path = page.getByRole('navigation', { name: 'Genre path' });
  await page.getByRole('button', { name: 'Open Fiction.Science Fiction', exact: true }).click();
  await expect(page.getByRole('link', { name: 'Space Opera', exact: true })).toBeVisible();

  await page.getByRole('button', { name: 'List view', exact: true }).click();
  await expect(path).toBeHidden();
  await expect(page.getByRole('link', { name: 'Space Opera', exact: true })).toBeHidden();
  await page.getByRole('button', { name: 'Expand Fiction', exact: true }).click();
  await page.getByRole('button', { name: 'Expand Fiction.Science Fiction', exact: true }).click();
  await page.getByRole('button', { name: 'Expand History', exact: true }).click();
  await expect(page.getByRole('link', { name: 'Space Opera', exact: true })).toBeVisible();
  await expect(page.locator('.tags-table .tag-main').first()).toHaveText('Fiction');
  await page.getByRole('button', { name: 'Sort genres', exact: true }).click();
  await page.getByRole('option', { name: 'Name', exact: true }).click();
  await expect(page.locator('.tags-table .tag-main').first()).toHaveText('Adventure');
  await expect(page.getByRole('link', { name: 'Medieval', exact: true })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Space Opera', exact: true })).toBeVisible();

  await page.getByRole('button', { name: 'Grid view', exact: true }).click();
  await expect(path.getByRole('button', { name: 'Science Fiction', exact: true })).toHaveAttribute(
    'aria-current',
    'page',
  );
  await page.getByRole('link', { name: 'Space Opera', exact: true }).click();
  await expect(page.locator('#search-input')).toHaveValue(
    'genre:"Fiction.Science Fiction.Space Opera"',
  );
  await expect(page.locator('.book-card')).toHaveCount(1);
  await page.goBack();
  await expect(path.getByRole('button', { name: 'Science Fiction', exact: true })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Space Opera', exact: true })).toBeVisible();

  const search = page.getByRole('searchbox', { name: 'Search genres' });
  await search.fill('Fiction');
  await expect(path).toBeHidden();
  await expect(page.getByRole('link', { name: 'Fiction.Fantasy', exact: true })).toBeVisible();
  await search.fill('');
  await expect(path.getByRole('button', { name: 'Science Fiction', exact: true })).toBeVisible();
  await path.getByRole('button', { name: 'All genres', exact: true }).click();
  await page.getByRole('button', { name: 'List view', exact: true }).click();
  await expect(page.getByRole('link', { name: 'Space Opera', exact: true })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Medieval', exact: true })).toBeVisible();
});
