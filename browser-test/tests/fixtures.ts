import { expect, test as base } from '@playwright/test';
import { collectBrowserErrors } from './helpers';

type BrowserErrorPredicate = (message: string) => boolean;

export type BrowserErrors = {
  allow(predicate: BrowserErrorPredicate): void;
};

export const test = base.extend<{ browserErrors: BrowserErrors }>({
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

export { expect };
export type { Locator, Page } from '@playwright/test';
