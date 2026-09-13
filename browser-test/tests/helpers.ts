import { expect, type Page } from '@playwright/test';
import type { BookSummary } from '../../frontend/src/types';
import type { UploadFile } from './book-fixtures';

export async function findBook(page: Page, title: string): Promise<BookSummary> {
  const response = await page.request.get('/api/books', { params: { q: title } });
  if (!response.ok()) throw new Error(`books status ${response.status()}`);
  const books: BookSummary[] = await response.json();
  const book = books.find((book) => book.title === title);
  if (!book) throw new Error(`missing book: ${title}`);
  return book;
}

export async function openReader(page: Page, title = 'With Cover Book'): Promise<number> {
  const book = await findBook(page, title);
  await page.goto(`/read/${book.id}`);
  await expect(page.locator('.reader-epub-stage')).toHaveAttribute('data-reader-ready', 'true');
  return Number(await page.locator('.reader-page').getAttribute('data-reader-asset-id'));
}

export async function importTestBook(page: Page, file: UploadFile): Promise<number> {
  const response = await page.request.post('/api/import', { multipart: { book: file } });
  if (!response.ok())
    throw new Error(`import status ${response.status()}: ${await response.text()}`);
  const result = (await response.json()) as { book: { id: number } };
  return result.book.id;
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

export async function login(page: Page, username = 'admin', password = 'devpass'): Promise<void> {
  await page.goto('/login');
  await page.locator('input[name="username"]').fill(username);
  await page.locator('input[name="password"]').fill(password);
  await Promise.all([
    page.waitForURL((url) => new URL(url).pathname !== '/login'),
    page.locator('button[type="submit"]').click(),
  ]);
  await page.locator('.account-name').waitFor();
}

export async function readerMutationFields(page: Page, assetId: number) {
  const response = await page.request.get(`/api/reader/assets/${assetId}/state`);
  if (!response.ok()) throw new Error(`reader state: ${response.status()}`);
  const state = await response.json();
  return {
    revision: state.revision,
    device_id: 'urn:polka:browser-test',
    device_name: 'Browser test',
  };
}
