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
  await expect(page.locator('.account-settings')).toBeVisible();
  await page.locator('.account-settings').click();

  const modal = page.locator('.settings-modal');
  await expect(modal).toBeVisible();
  await modal.getByRole('tab', { name: tab }).click();
  return modal;
}

test.describe('Account settings', () => {
  test('loading library settings preserves a personal setting being edited', async ({ page }) => {
    let release!: () => void;
    const storageReady = new Promise<void>((resolve) => { release = resolve; });
    await page.route('**/api/admin/storage', async (route) => {
      await storageReady;
      await route.continue();
    });
    try {
      const modal = await openSettings(page, 'General');
      const input = modal.getByRole('combobox', { name: 'Time zone', exact: true });
      await expect(input).toBeVisible();
      // Returning while the request is pending also replaces its original host.
      await modal.getByRole('tab', { name: 'Users', exact: true }).click();
      await modal.getByRole('tab', { name: 'General', exact: true }).click();
      const previous = await input.inputValue();
      await input.fill('Europe/');
      release();
      await expect(modal.getByLabel('Metadata write-back mode')).toBeVisible();
      await expect(input).toHaveValue('Europe/');
      await expect(input).toBeFocused();
      await input.fill(previous);
    } finally {
      release();
    }
  });

  test('shows reading app setup and manages credentials', async ({ page, context }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write']);
    const stamp = Date.now().toString(36);
    const shelfName = `Kobo Query ${stamp}`;
    const shelfID = await createQueryShelf(page, shelfName, 'author:"Noise Author" tag:"private"');
    let modal = await openSettings(page, 'Reading apps');

    const opdsSetup = modal.locator('.settings-opds-setup');
    await expect(opdsSetup.locator('.settings-opds-url')).toHaveValue(/\/opds$/);
    await expect(opdsSetup.getByRole('textbox', { name: 'Username' })).toHaveValue('polka');

    await modal.getByRole('button', { name: 'Set up Kobo' }).click();
    const submodal = page.locator('.settings-submodal');
    await submodal.getByLabel('Shelf').selectOption({ label: `${shelfName} · smart shelf` });
    await submodal.getByRole('button', { name: 'Create' }).click();

    await expect(submodal.getByRole('heading', { name: 'Connect Kobo' })).toBeVisible();
    const koboSetupURL = await submodal.getByRole('textbox', { name: 'Kobo setup URL' }).inputValue();
    await submodal.getByRole('button', { name: 'Done' }).click();

    modal = await openSettings(page, 'Reading apps');
    await modal.locator('.settings-kobo-row').getByText(shelfName, { exact: true }).click();
    await expect(submodal.getByRole('textbox', { name: 'Kobo setup URL' })).toHaveValue(koboSetupURL);
    await page.screenshot({ path: 'screenshots/settings-kobo-setup.png', animations: 'disabled' });
    await submodal.getByRole('button', { name: 'Done' }).click();

    const tokenName = `koreader-${stamp}`;
    await modal.getByRole('button', { name: 'New app password' }).click();
    await submodal.getByLabel('Name').fill(tokenName);
    await submodal.getByRole('button', { name: 'Create' }).click();

    await expect(submodal.getByRole('heading', { name: `Connect ${tokenName}` })).toBeVisible();
    const secretValue = await submodal
      .getByRole('textbox', { name: 'App password', exact: true })
      .inputValue();
    await submodal.getByRole('button', { name: 'Done' }).click();

    modal = await openSettings(page, 'Reading apps');
    const tokenList = modal.locator('.settings-item-list');
    const tokenRow = tokenList.locator('.settings-item-row', { hasText: tokenName });
    await expect(tokenRow).toBeVisible();
    await page.screenshot({
      path: 'screenshots/settings-saved-app-password.png',
      animations: 'disabled',
    });
    await page.setViewportSize({ width: 390, height: 844 });
    await page.screenshot({
      path: 'screenshots/settings-reading-apps-mobile.png',
      animations: 'disabled',
    });
    await tokenRow.getByText(tokenName, { exact: true }).click();
    await expect(submodal.getByRole('textbox', { name: 'App password', exact: true })).toHaveValue(
      secretValue,
    );
    await expect(submodal.getByRole('textbox', { name: 'Catalog URL' })).toHaveValue(/\/opds$/);
    await expect(submodal.getByRole('textbox', { name: 'Username' })).toHaveValue('polka');
    const completeURL = new URL(
      await submodal.getByRole('textbox', { name: 'Complete URL (includes password)' }).inputValue(),
    );
    expect(completeURL.username).toBe('polka');
    expect(completeURL.password).toBe(secretValue);
    expect(completeURL.pathname).toBe('/opds');
    await expect(submodal.getByRole('textbox', { name: 'Sync server URL' })).toHaveValue(
      new URL(`/kosync/${secretValue}`, page.url()).toString(),
    );
    const copyPassword = submodal.getByRole('button', { name: 'Copy app password', exact: true });
    await copyPassword.click();
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(secretValue);
    await page.screenshot({
      path: 'screenshots/settings-reading-app-details-mobile.png',
      animations: 'disabled',
    });
    await submodal.getByRole('button', { name: 'Done' }).click();
    const details = tokenRow.getByRole('button', { name: 'Details' });
    await expect(details).toBeFocused();
    await details.press('Enter');
    await expect(submodal.getByRole('heading', { name: `Connect ${tokenName}` })).toBeVisible();
    await submodal.getByRole('button', { name: 'Done' }).click();
    await tokenRow.getByRole('button', { name: 'Revoke' }).click();
    await expect(submodal).toHaveCount(0);
    await page.locator('.modal-confirm').getByRole('button', { name: 'Revoke' }).click();
    await expect(tokenRow).toHaveCount(0);

    await modal.locator('.settings-kobo-row').getByRole('button', { name: 'Revoke' }).click();
    await expect(submodal).toHaveCount(0);
    await page.locator('.modal-confirm').getByRole('button', { name: 'Revoke' }).click();
    await expect(modal.getByRole('button', { name: 'Set up Kobo' })).toBeVisible();

    await deleteShelf(page, shelfID);
  });

  test('a section that fails to load rests on its error and offers a retry', async ({
    page,
    browserErrors,
  }) => {
    browserErrors.allow((message) => message.includes('/api/users'));
    let attempts = 0;
    await page.route('**/api/users', async (route) => {
      attempts += 1;
      await route.abort('failed');
    });

    const modal = await openSettings(page, 'Users');
    const error = modal.locator('.settings-note-error');
    await expect(error).toContainText('Cannot reach server');

    // One automatic retry is bounded. Once both connection attempts fail, the
    // panel must rest on its explicit Retry instead of entering a render loop.
    await page.waitForTimeout(400);
    expect(attempts).toBe(2);
    await expect(error).toBeVisible();

    await page.unroute('**/api/users');
    await error.getByRole('button', { name: 'Retry' }).click();
    await expect(modal.locator('.settings-user-row', { hasText: 'admin' })).toBeVisible();
  });

  test('reveals device sending only once an admin turns it on', async ({ page }) => {
    const modal = await openSettings(page, 'Devices');
    const sendingSwitch = modal.getByRole('switch', { name: 'Sending books to a device' });

    await expect(sendingSwitch).toHaveAttribute('aria-checked', 'false');
    await expect(modal.getByRole('heading', { name: 'Email delivery' })).toHaveCount(0);
    await expect(modal.getByRole('heading', { name: 'Send devices' })).toHaveCount(0);
    await page.screenshot({ path: 'screenshots/settings-devices-off.png', fullPage: true });

    await sendingSwitch.click();
    await expect(sendingSwitch).toHaveAttribute('aria-checked', 'true');
    await expect(modal.getByRole('heading', { name: 'Email delivery' })).toBeVisible();
    await expect(modal.getByRole('heading', { name: 'Send devices' })).toBeVisible();
    await expect(modal.getByRole('heading', { name: 'Recent sends' })).toBeVisible();
    await page.screenshot({ path: 'screenshots/settings-devices-on.png', fullPage: true });

    // Leave the shared catalog as it was found.
    await sendingSwitch.click();
    await expect(modal.getByRole('heading', { name: 'Email delivery' })).toHaveCount(0);
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
