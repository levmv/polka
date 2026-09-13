import { execFile } from 'node:child_process';
import { cp, mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { promisify } from 'node:util';
import { type APIRequestContext, test as base, expect } from '@playwright/test';
import { binary, browserTestDir, serveLibrary } from '../library';
import { collectBrowserErrors } from './helpers';

const exec = promisify(execFile);
type StorageState = Awaited<ReturnType<APIRequestContext['storageState']>>;
type Account = 'admin' | 'reader';
type LibraryTemplate = { directory: string; storageStates: Record<Account, StorageState> };
type WorkerFixtures = {
  seed: 'library' | 'pagination';
  libraryTemplate: LibraryTemplate;
};
type TestFixtures = {
  account: Account;
  libraryURL: string;
  browserErrors: BrowserErrors;
};

type BrowserErrorPredicate = (message: string) => boolean;

export type BrowserErrors = {
  allow(predicate: BrowserErrorPredicate): void;
};

export const test = base.extend<TestFixtures, WorkerFixtures>({
  seed: ['library', { scope: 'worker', option: true }],
  account: ['admin', { option: true }],
  libraryTemplate: [
    async ({ seed, playwright }, use) => {
      const directory = await mkdtemp(join(tmpdir(), 'polka-browser-seed-'));
      try {
        let input = join(browserTestDir, 'fixtures');
        if (seed === 'pagination') {
          input = join(directory, 'filler');
          await exec('python3', [
            join(browserTestDir, 'fixtures', 'generate.py'),
            '--filler',
            '55',
            input,
          ]);
        }
        const data = join(directory, 'data');
        await exec(binary, ['import', input, '--data', data]);
        const storageStates = await serveLibrary(data, async (baseURL) => {
          const request = await playwright.request.newContext({ baseURL });
          try {
            const response = await request.post('/login', {
              form: { username: 'admin', password: 'devpass' },
            });
            expect(response.ok(), 'authenticate template admin').toBe(true);
            const admin = await request.storageState();
            const user = { username: 'reader', password: 'reader-test-pass' };
            const created = await request.post('/api/users', {
              data: { ...user, role: 'reader', content_scope: 'all' },
            });
            expect(created.ok(), 'create template reader').toBe(true);
            const login = await request.post('/login', { form: user });
            expect(login.ok(), 'authenticate template reader').toBe(true);
            return { admin, reader: await request.storageState() };
          } finally {
            await request.dispose();
          }
        });
        // Copy only a stopped library, including both persisted sessions.
        await use({ directory: data, storageStates });
      } finally {
        await rm(directory, { recursive: true, force: true });
      }
    },
    { scope: 'worker', timeout: 30_000 },
  ],
  libraryURL: async ({ libraryTemplate }, use, testInfo) => {
    const directory = await mkdtemp(join(tmpdir(), 'polka-browser-test-'));
    try {
      await cp(libraryTemplate.directory, directory, { recursive: true });
      await serveLibrary(directory, async (baseURL, logs) => {
        await use(baseURL);
        if (testInfo.status !== testInfo.expectedStatus) {
          await testInfo.attach('server.log', { body: logs(), contentType: 'text/plain' });
        }
      });
    } finally {
      await rm(directory, { recursive: true, force: true });
    }
  },
  baseURL: async ({ libraryURL }, use) => use(libraryURL),
  storageState: async ({ account, libraryTemplate }, use) => {
    await use(libraryTemplate.storageStates[account]);
  },
  browserErrors: [
    async ({ page }, use) => {
      const errors = collectBrowserErrors(page);
      const allowed: BrowserErrorPredicate[] = [];
      await use({
        allow(predicate) {
          allowed.push(predicate);
        },
      });
      const unexpected = errors.filter((message) => !allowed.some((accept) => accept(message)));
      expect(unexpected, 'unexpected browser console/page errors').toEqual([]);
    },
    { auto: true },
  ],
});

export type { Locator, Page } from '@playwright/test';
export { expect };
