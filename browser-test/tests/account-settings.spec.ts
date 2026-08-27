import { expect, type Locator, type Page, test } from './fixtures';

async function createQueryShelf(page: Page, name: string, query: string): Promise<string> {
  const response = await page.request.post('/api/shelves', {
    data: { name, kind: 'query', query, shared: true },
  });
  expect(response.ok()).toBe(true);
  return ((await response.json()) as { id: string }).id;
}

async function deleteShelf(page: Page, id: string): Promise<void> {
  const response = await page.request.delete(`/api/shelves/${encodeURIComponent(id)}`);
  expect(response.status()).toBe(204);
}

async function openSettings(page: Page, tab: string): Promise<Locator> {
  await page.goto('/');
  await expect(page.locator('.account-trigger')).toBeVisible();
  await page.locator('.account-trigger').click();
  await page.getByRole('menuitem', { name: 'Settings' }).click();

  const modal = page.locator('.settings-modal');
  await expect(modal).toBeVisible();
  await modal.getByRole('tab', { name: tab }).click();
  return modal;
}

test.describe('Account settings', () => {
  test('shows reading app setup and manages credentials', async ({ page }) => {
    const stamp = Date.now().toString(36);
    const shelfName = `Kobo Query ${stamp}`;
    const shelfID = await createQueryShelf(page, shelfName, 'author:"Noise Author" tag:"private"');
    const modal = await openSettings(page, 'Reading apps');

    await expect(modal.getByRole('heading', { name: 'App passwords' })).toBeVisible();
    await expect(modal).toContainText('Connect reading apps without using your account password');
    await expect(modal.getByRole('heading', { name: 'Kobo sync' })).toBeVisible();
    await expect(modal).toContainText('Experimental');
    const opdsSetup = modal.locator('.settings-opds-setup');
    await expect(opdsSetup).toContainText('Browse and download');
    await expect(opdsSetup.locator('.settings-opds-url')).toHaveValue(/\/opds$/);
    await expect(opdsSetup.getByRole('textbox', { name: 'Username' })).toHaveValue('admin');
    await expect(opdsSetup.getByRole('button', { name: 'Copy OPDS catalog URL' })).toBeVisible();
    await expect(modal.locator('.settings-kosync-setup')).toContainText(
      'complete custom sync server URL',
    );
    await page.screenshot({ path: 'screenshots/settings-account.png', fullPage: true });

    await modal.getByRole('button', { name: 'Set up Kobo' }).click();
    let submodal = page.locator('.settings-submodal');
    await expect(submodal.getByRole('heading', { name: 'Set up Kobo' })).toBeVisible();
    await submodal.getByLabel('Shelf').selectOption({ label: `${shelfName} · smart shelf` });
    await submodal.getByRole('button', { name: 'Create' }).click();

    submodal = page.locator('.settings-submodal');
    await expect(submodal.getByRole('heading', { name: 'Connect Kobo' })).toBeVisible();
    const koboSetupURL = await submodal.getByRole('textbox', { name: 'Kobo setup URL' }).inputValue();
    expect(koboSetupURL).toMatch(/\/kobo\/[A-Za-z0-9_-]{32}$/);
    await expect(submodal.getByRole('button', { name: 'Copy Kobo setup URL' })).toBeVisible();
    await page.screenshot({
      path: 'screenshots/settings-kobo-setup.png',
      animations: 'disabled',
    });
    await submodal.getByRole('button', { name: 'Done' }).click();
    await expect(modal.locator('.settings-kobo-row')).toContainText(shelfName);
    await expect(modal).not.toContainText(koboSetupURL.slice(-16));

    await modal.locator('.settings-kobo-row').getByRole('button', { name: 'Revoke' }).click();
    await page.locator('.modal-confirm').getByRole('button', { name: 'Revoke' }).click();
    await expect(modal.getByRole('button', { name: 'Set up Kobo' })).toBeVisible();

    const tokenName = `koreader-${stamp}`;
    await modal.getByRole('button', { name: 'New app password' }).click();
    submodal = page.locator('.settings-submodal');
    await expect(submodal.getByRole('heading', { name: 'New app password' })).toBeVisible();
    await submodal.getByLabel('Name').fill(tokenName);
    await submodal.getByRole('button', { name: 'Create' }).click();

    submodal = page.locator('.settings-submodal');
    await expect(submodal.getByRole('heading', { name: `Connect ${tokenName}` })).toBeVisible();
    const secretValue = await submodal
      .getByRole('textbox', { name: 'App password', exact: true })
      .inputValue();
    expect(secretValue).toMatch(/^[0-9a-f]{32}$/);
    await expect(submodal.getByRole('textbox', { name: 'Catalog URL' })).toHaveValue(/\/opds$/);
    await expect(submodal.getByRole('textbox', { name: 'Username' })).toHaveValue('admin');
    await expect(submodal.getByRole('textbox', { name: 'Sync server URL' })).toHaveValue(
      new RegExp(`/kosync/${secretValue}$`),
    );
    await expect(
      submodal.getByRole('button', { name: 'Copy KOReader sync server URL' }),
    ).toBeVisible();
    await page.screenshot({
      path: 'screenshots/settings-reading-app-setup.png',
      animations: 'disabled',
    });
    await submodal.getByRole('button', { name: 'Done' }).click();
    await expect(submodal).toHaveCount(0);

    const tokenList = modal.locator('.settings-item-list');
    await expect(tokenList).not.toContainText(secretValue.slice(0, 16));
    const tokenRow = tokenList.locator('.settings-item-row', { hasText: tokenName });
    await expect(tokenRow).toBeVisible();
    await tokenRow.getByRole('button', { name: 'Revoke' }).click();
    await page.locator('.modal-confirm').getByRole('button', { name: 'Revoke' }).click();
    await expect(tokenRow).toHaveCount(0);
    await expect(page.locator('.toast')).toHaveCount(0, { timeout: 5000 });

    await deleteShelf(page, shelfID);
  });

  test('manages scoped users across desktop and mobile', async ({ page }) => {
    const stamp = Date.now().toString(36);
    const newUser = `alice-${stamp}`;
    const accessShelfName = `Scoped Query ${stamp}`;
    const accessShelfQuery = 'author:"Noise Author" tag:"private"';
    const filteredShelfName = `Filtered Query ${stamp}`;
    const accessShelfID = await createQueryShelf(page, accessShelfName, accessShelfQuery);
    const filteredShelfID = await createQueryShelf(
      page,
      filteredShelfName,
      `${accessShelfQuery} no:cover`,
    );
    const modal = await openSettings(page, 'Users');

    await expect(modal.locator('.settings-user-row', { hasText: 'admin' })).toBeVisible();
    await expect(
      modal.locator('.settings-user-row', { hasText: 'admin' }).getByRole('button', {
        name: 'Change password',
      }),
    ).toBeVisible();
    await page.screenshot({ path: 'screenshots/settings.png', fullPage: true });
    await page.setViewportSize({ width: 390, height: 720 });
    await expect(modal).toBeVisible();
    await page.screenshot({ path: 'screenshots/settings-mobile.png' });
    await page.setViewportSize({ width: 1280, height: 720 });

    await modal.getByRole('button', { name: 'Add user' }).click();
    const submodal = page.locator('.settings-submodal');
    await expect(submodal.getByRole('heading', { name: 'Add user' })).toBeVisible();
    await submodal.getByLabel('Content scope').click();
    await page.getByRole('option', { name: 'Selected shelves' }).click();
    const scopeShelf = submodal.locator('.settings-shelf-checkbox-field', {
      hasText: accessShelfName,
    });
    await expect(scopeShelf).toBeVisible();
    await expect(scopeShelf).not.toContainText(accessShelfQuery);
    await expect(scopeShelf.locator('.shelf-kind-marker[data-kind="query"]')).toHaveCount(1);
    await expect(
      submodal.locator('.settings-shelf-checkbox-field', { hasText: filteredShelfName }),
    ).toBeVisible();
    await submodal.getByLabel('Username').fill(newUser);
    await submodal.getByLabel('Password').fill('secret');
    await submodal.getByRole('button', { name: 'Add user' }).click();

    const userRow = modal.locator('.settings-user-row', { hasText: newUser });
    await expect(userRow).toBeVisible();
    await userRow.getByRole('button', { name: `Remove ${newUser}` }).click();
    await page.locator('.modal-confirm').getByRole('button', { name: 'Remove' }).click();
    await expect(userRow).toHaveCount(0);

    await deleteShelf(page, filteredShelfID);
    await deleteShelf(page, accessShelfID);
  });
});
