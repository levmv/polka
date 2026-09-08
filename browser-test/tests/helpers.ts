import type { Page } from '@playwright/test';
import type { UploadFile } from './book-fixtures';

export async function importTestBook(page: Page, file: UploadFile): Promise<number> {
  const response = await page.request.post('/api/import', { multipart: { book: file } });
  if (!response.ok()) throw new Error(`import status ${response.status()}: ${await response.text()}`);
  const result = (await response.json()) as { book: { id: number } };
  return result.book.id;
}

export interface TestUser {
  id: number;
  username: string;
  password: string;
}

export function collectBrowserErrors(page: Page): string[] {
  const errors: string[] = [];
  page.on('console', (msg) => {
    if (msg.type() === 'error' && !isExpectedConsoleNoise(msg)) {
      const source = msg.location().url;
      errors.push(source ? `${msg.text()} [${source}]` : msg.text());
    }
  });
  page.on('pageerror', (error) => errors.push(error.stack || error.message));
  return errors;
}

function isExpectedConsoleNoise(msg: { text(): string; location(): { url?: string } }): boolean {
  const text = msg.text();
  const url = msg.location().url || '';
  return text.includes('favicon.ico') || url.endsWith('/favicon.ico');
}

export async function login(
  page: Page,
  username = process.env.POLKA_TEST_USER || 'admin',
  password = process.env.POLKA_TEST_PASSWORD || 'devpass',
): Promise<void> {
  await page.goto('/login');
  await page.locator('input[name="username"]').fill(username);
  await page.locator('input[name="password"]').fill(password);
  await Promise.all([
    page.waitForURL((url) => new URL(url).pathname !== '/login'),
    page.locator('button[type="submit"]').click(),
  ]);
  await page.locator('.account-name').waitFor();
}

export async function loginByRequest(
  page: Page,
  username = process.env.POLKA_TEST_USER || 'admin',
  password = process.env.POLKA_TEST_PASSWORD || 'devpass',
): Promise<void> {
  const res = await page.request.post('/login', {
    form: { username, password },
  });
  if (!res.ok()) throw new Error(`login status ${res.status()}: ${await res.text()}`);
}

export async function createReaderTestUser(page: Page, prefix: string): Promise<TestUser> {
  const username = `${prefix}-${Date.now().toString(36)}-${Math.random()
    .toString(36)
    .slice(2, 8)}`;
  const password = 'reader-test-pass';
  const res = await page.request.post('/api/users', {
    data: {
      username,
      password,
      role: 'reader',
      content_scope: 'all',
    },
  });
  if (!res.ok()) throw new Error(`create user status ${res.status()}: ${await res.text()}`);
  const body = (await res.json()) as { id: number; username: string };
  return { id: body.id, username: body.username, password };
}

export async function deleteTestUserAsAdmin(page: Page, user: TestUser): Promise<void> {
  // Stop reader saves before changing the browser's session to the admin account.
  await page.goto('about:blank');
  await loginByRequest(page);
  const res = await page.request.delete(`/api/users/${user.id}`);
  if (!res.ok() && res.status() !== 404) {
    throw new Error(`delete user status ${res.status()}: ${await res.text()}`);
  }
}
