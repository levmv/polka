import { expect, type Locator, type Page, test } from './fixtures';

async function createQueryShelf(page: Page, name: string, query: string): Promise<void> {
  const response = await page.request.post('/api/shelves', {
    data: { name, kind: 'query', query, shared: true },
  });
  expect(response.ok()).toBe(true);
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

async function holdNextSave(page: Page, url: string) {
  let release!: () => void;
  let saved!: () => void;
  const released = new Promise<void>((resolve) => {
    release = resolve;
  });
  const started = new Promise<void>((resolve) => {
    saved = resolve;
  });
  let held = false;
  await page.route(url, async (route) => {
    if (route.request().method() !== 'PUT' || held) return route.continue();
    held = true;
    const response = await route.fetch();
    saved();
    await released;
    await route.fulfill({ response });
  });
  return { started, release };
}

test.describe('Account settings', () => {
  test('loading library settings preserves a personal setting being edited', async ({ page }) => {
    let release!: () => void;
    const storageReady = new Promise<void>((resolve) => {
      release = resolve;
    });
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

  test('keeps current choices across independent saves and reopening Settings', async ({
    page,
  }) => {
    const modal = await openSettings(page, 'General');
    const zone = modal.getByRole('combobox', { name: 'Time zone', exact: true });
    const initialZone = await zone.inputValue();
    const nextZone = initialZone === 'Europe/Berlin' ? 'UTC' : 'Europe/Berlin';
    const hold = await holdNextSave(page, '**/api/settings');
    try {
      await zone.fill(nextZone);
      await zone.press('Tab');
      await hold.started;

      await modal.getByLabel('Theme', { exact: true }).click();
      await page.getByRole('option', { name: 'Light', exact: true }).click();
      await expect
        .poll(async () => (await page.request.get('/api/settings')).json())
        .toMatchObject({ theme: 'light', time_zone: nextZone });

      await page.keyboard.press('Escape');
      await expect(modal).toHaveCount(0);
      await page.locator('.account-settings').click();
      await expect(zone).toHaveValue(nextZone);
      await expect(modal.getByLabel('Theme', { exact: true })).toContainText('Light');

      const cancelled = page.waitForEvent('requestfailed', {
        predicate: (request) =>
          request.url().endsWith('/api/settings') &&
          request.method() === 'PUT' &&
          request.postDataJSON().time_zone === nextZone,
      });
      await zone.fill(initialZone);
      await zone.press('Tab');
      await cancelled;
      await expect
        .poll(async () => (await page.request.get('/api/settings')).json())
        .toMatchObject({ theme: 'light', time_zone: initialZone });

      hold.release();
      await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
      await page.reload();
      await page.locator('.account-settings').click();
      await expect(zone).toHaveValue(initialZone);
      await expect(modal.getByLabel('Theme', { exact: true })).toContainText('Light');
    } finally {
      hold.release();
    }
  });

  test('shows reading app setup and manages app passwords', async ({ page, context }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write']);
    let modal = await openSettings(page, 'Reading apps');

    const opdsSetup = modal.locator('.settings-opds-setup');
    await expect(opdsSetup.locator('.settings-opds-url')).toHaveValue(/\/opds$/);
    await expect(opdsSetup.getByRole('textbox', { name: 'Username' })).toHaveValue('polka');

    const tokenName = 'koreader';
    await modal.getByRole('button', { name: 'New app password' }).click();
    const submodal = page.locator('.modal-compact');
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
    await page.setViewportSize({ width: 390, height: 844 });
    await tokenRow.getByText(tokenName, { exact: true }).click();
    await expect(submodal.getByRole('textbox', { name: 'App password', exact: true })).toHaveValue(
      secretValue,
    );
    await expect(submodal.getByRole('textbox', { name: 'Catalog URL' })).toHaveValue(/\/opds$/);
    await expect(submodal.getByRole('textbox', { name: 'Username' })).toHaveValue('polka');
    const completeURL = new URL(
      await submodal
        .getByRole('textbox', { name: 'Complete URL (includes password)' })
        .inputValue(),
    );
    expect(completeURL.username).toBe('polka');
    expect(completeURL.password).toBe(secretValue);
    expect(completeURL.pathname).toBe('/opds');
    await expect(submodal.getByRole('textbox', { name: 'Sync server URL' })).toHaveValue(
      new URL(`/kosync/${secretValue}`, page.url()).toString(),
    );
    const copyPassword = submodal.getByRole('button', { name: 'Copy app password', exact: true });
    // Exercise the real legacy copy path when the Clipboard API is blocked.
    await page.evaluate(() => {
      navigator.clipboard.writeText = async () => {
        throw new DOMException('Clipboard API blocked', 'NotAllowedError');
      };
    });
    await copyPassword.click();
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(secretValue);
    await expect(copyPassword).toBeFocused();
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
  });

  test('sets up Kobo, changes its shelf, and recovers from shelf deletion', async ({ page }) => {
    const shelfIDs: number[] = [];
    for (const name of ['On Kobo', 'Next books']) {
      const response = await page.request.post('/api/shelves', {
        data: { name, kind: 'query', query: 'author:"Noise Author"' },
      });
      expect(response.ok()).toBe(true);
      shelfIDs.push((await response.json()).id);
    }
    let modal = await openSettings(page, 'Reading apps');
    await modal.getByRole('button', { name: 'Set up Kobo' }).click();
    const dialog = page.locator('.modal-compact');
    await dialog.getByLabel('Shelf').selectOption({ label: 'On Kobo · smart shelf' });
    await dialog.getByRole('button', { name: 'Create' }).click();
    await expect(dialog.getByRole('heading', { name: 'Connect Kobo' })).toBeVisible();
    const setupURL = await dialog.getByRole('textbox', { name: 'Kobo setup URL' }).inputValue();
    await dialog.getByRole('button', { name: 'Done' }).click();

    modal = await openSettings(page, 'Reading apps');
    const row = modal.locator('.settings-kobo-row');
    await row.getByText('On Kobo', { exact: true }).click();
    await expect(dialog.getByRole('textbox', { name: 'Kobo setup URL' })).toHaveValue(setupURL);
    await dialog.getByRole('button', { name: 'Done' }).click();

    await row.getByRole('button', { name: 'Change shelf' }).click();
    await expect(dialog).toContainText('Books outside the new shelf will be removed');
    await dialog.getByLabel('Shelf').selectOption(String(shelfIDs[1]));
    await dialog.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(dialog).toHaveCount(0);
    await expect(row).toContainText('Next books');
    await row.getByRole('button', { name: 'Details', exact: true }).click();
    await expect(dialog.getByRole('textbox', { name: 'Kobo setup URL' })).toHaveValue(setupURL);
    await dialog.getByRole('button', { name: 'Done' }).click();

    const deleted = await page.request.delete(`/api/shelves/${shelfIDs[1]}`);
    expect(deleted.ok()).toBe(true);
    modal = await openSettings(page, 'Reading apps');
    await expect(row).toContainText('No shelf selected');
    await expect(row).toContainText('Its books will be removed from Kobo on the next sync');
    await row.getByRole('button', { name: 'Details', exact: true }).click();
    await expect(dialog.getByRole('textbox', { name: 'Kobo setup URL' })).toHaveValue(setupURL);
    await dialog.getByRole('button', { name: 'Done' }).click();

    await row.getByRole('button', { name: 'Change shelf' }).click();
    await dialog.getByLabel('Shelf').selectOption(String(shelfIDs[0]));
    await dialog.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(dialog).toHaveCount(0);
    await expect(row).toContainText('On Kobo');

    await row.getByRole('button', { name: 'Revoke' }).click();
    await expect(dialog).toHaveCount(0);
    await page.locator('.modal-confirm').getByRole('button', { name: 'Revoke' }).click();
    await expect(modal.getByRole('button', { name: 'Set up Kobo' })).toBeVisible();
  });

  test('a failed section stops retrying automatically and offers a manual retry', async ({
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
    const error = modal.locator('.dialog-note-error');
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

  test('reveals email settings only once an admin turns sending on', async ({ page }) => {
    const modal = await openSettings(page, 'Email delivery');
    const sendingSwitch = modal.getByRole('switch', { name: 'Sending books by email' });
    const hold = await holdNextSave(page, '**/api/admin/delivery');

    await expect(sendingSwitch).toHaveAttribute('aria-checked', 'false');
    await expect(modal.getByRole('heading', { name: 'Mail server' })).toHaveCount(0);
    await expect(modal.getByRole('heading', { name: 'Send devices' })).toHaveCount(0);

    try {
      await sendingSwitch.click();
      await hold.started;
      await expect(sendingSwitch).toHaveAttribute('aria-checked', 'true');
      await expect(modal.getByRole('heading', { name: 'Mail server' })).toBeVisible();
      await expect(modal.getByRole('heading', { name: 'Send devices' })).toBeVisible();
      await expect(modal.getByRole('heading', { name: 'Recent sends' })).toBeVisible();
      await expect(sendingSwitch).toBeFocused();

      const saved = page.waitForResponse(
        (response) =>
          response.url().endsWith('/api/admin/delivery') &&
          response.request().postDataJSON().enabled === false,
      );
      await page.keyboard.press('Space');
      await expect(modal.getByRole('heading', { name: 'Mail server' })).toHaveCount(0);
      await expect(sendingSwitch).toBeFocused();
      await saved;
      hold.release();
      await expect(sendingSwitch).toHaveAttribute('aria-checked', 'false');
      await openSettings(page, 'Email delivery');
      await expect(sendingSwitch).toHaveAttribute('aria-checked', 'false');
    } finally {
      hold.release();
    }
  });

  test('manages scoped users', async ({ page }) => {
    const newUser = 'alice';
    const accessShelfName = 'Scoped Query';
    const accessShelfQuery = 'author:"Noise Author" tag:"private"';
    const filteredShelfName = 'Filtered Query';
    await createQueryShelf(page, accessShelfName, accessShelfQuery);
    await createQueryShelf(page, filteredShelfName, `${accessShelfQuery} no:cover`);
    const modal = await openSettings(page, 'Users');

    await expect(modal.locator('.settings-user-row', { hasText: 'admin' })).toBeVisible();
    await expect(
      modal.locator('.settings-user-row', { hasText: 'admin' }).getByRole('button', {
        name: 'Change password',
      }),
    ).toBeVisible();

    await modal.getByRole('button', { name: 'Add user' }).click();
    const submodal = page.locator('.modal-compact');
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
  });

  test('Explicit themes override the operating-system color scheme', async ({ page }) => {
    await page.emulateMedia({ colorScheme: 'dark' });
    await page.goto('/');
    const libraryNav = page.locator('#nav-library');
    await expect(libraryNav).toBeVisible();

    const expectColors = async (expected: {
      background: string;
      hover: string;
      danger: string;
    }) => {
      await libraryNav.hover();
      await expect(page.locator('body')).toHaveCSS('background-color', expected.background);
      await expect(libraryNav).toHaveCSS('background-color', expected.hover);
      await expect
        .poll(() =>
          page.evaluate(() =>
            getComputedStyle(document.documentElement).getPropertyValue('--danger').trim(),
          ),
        )
        .toBe(expected.danger);
    };

    await expectColors({
      background: 'rgb(18, 18, 18)',
      hover: 'rgba(255, 255, 255, 0.07)',
      danger: '#ef9a9a',
    });

    await page.evaluate(() => {
      document.documentElement.dataset.theme = 'light';
    });
    await expectColors({
      background: 'rgb(252, 252, 252)',
      hover: 'rgba(0, 0, 0, 0.05)',
      danger: '#c62828',
    });

    await page.evaluate(() => {
      document.documentElement.dataset.theme = 'sepia';
    });
    await expectColors({
      background: 'rgb(244, 239, 230)',
      hover: 'rgba(0, 0, 0, 0.05)',
      danger: '#c62828',
    });

    await page.emulateMedia({ colorScheme: 'light' });
    await page.evaluate(() => {
      document.documentElement.dataset.theme = 'dark';
    });
    await expectColors({
      background: 'rgb(18, 18, 18)',
      hover: 'rgba(255, 255, 255, 0.07)',
      danger: '#ef9a9a',
    });
  });
});
