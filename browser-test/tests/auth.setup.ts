import { mkdir } from 'node:fs/promises';
import { dirname } from 'node:path';
import { test as setup } from '@playwright/test';
import {
  laneAAdminStorageState,
  laneBAdminStorageState,
  pagerAdminStorageState,
} from '../auth-state';
import { login } from './helpers';

setup('authenticate admin', async ({ page }, testInfo) => {
  await login(page);

  const storageStatePath =
    testInfo.project.name === 'setup-lane-b'
      ? laneBAdminStorageState
      : testInfo.project.name === 'setup-pager'
        ? pagerAdminStorageState
        : laneAAdminStorageState;
  await mkdir(dirname(storageStatePath), { recursive: true });
  await page.context().storageState({ path: storageStatePath });
});
