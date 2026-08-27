import { chromium } from '@playwright/test';

const DEFAULT_BASE_URL = process.env.POLKA_BASE_URL || 'http://127.0.0.1:8099';
const DEFAULT_VIEWPORT = '1280x720';
const DEFAULT_DELAY = 500;

const args = process.argv.slice(2);
if (args.includes('--help') || args.includes('-h')) {
    printUsage();
    process.exit(0);
}
if (args.length < 2) {
    printUsage();
    process.exit(1);
}

const [target, output, ...flags] = args;
const options = parseFlags(flags);
const viewport = parseViewport(options.viewport || DEFAULT_VIEWPORT);
const url =
    target.startsWith('http://') || target.startsWith('https://')
        ? target
        : new URL(target, DEFAULT_BASE_URL).toString();

const browser = await chromium.launch({ headless: true });
const page = await browser.newPage({ viewport });

try {
    const username = options.username || process.env.POLKA_SCREENSHOT_USER;
    const password = options.password || process.env.POLKA_SCREENSHOT_PASSWORD;
    if (username && password) {
        const loginURL = new URL('/login', url).toString();
        await page.goto(loginURL);
        await page.fill('input[name=username]', username);
        await page.fill('input[name=password]', password);
        await Promise.all([
            page.waitForURL((nextURL) => new URL(nextURL).pathname !== '/login'),
            page.click('button[type=submit]'),
        ]);
    }

    await page.goto(url);
    if (options.wait) {
        await page.locator(options.wait).first().waitFor();
    } else {
        await page.waitForLoadState('networkidle');
    }
    if (options.click) {
        await page.locator(options.click).first().click();
    }
    await page.waitForTimeout(parseDelay(options.delay || String(DEFAULT_DELAY)));
    await page.screenshot({ path: output, fullPage: options.fullPage !== 'false' });
} finally {
    await browser.close();
}

function parseFlags(flags) {
    const options = {};
    for (const flag of flags) {
        const match = flag.match(/^--([^=]+)=(.*)$/);
        if (!match) {
            throw new Error(`Unsupported flag ${flag}; use --name=value`);
        }
        options[toCamel(match[1])] = match[2];
    }
    return options;
}

function parseViewport(value) {
    const match = value.match(/^(\d+)x(\d+)$/);
    if (!match) {
        throw new Error(`Invalid viewport ${value}; expected WIDTHxHEIGHT`);
    }
    return { width: Number(match[1]), height: Number(match[2]) };
}

function parseDelay(value) {
    const delay = Number(value);
    if (!Number.isFinite(delay) || delay < 0) {
        throw new Error(`Invalid delay ${value}; expected milliseconds`);
    }
    return delay;
}

function toCamel(name) {
    return name.replace(/-([a-z])/g, (_, letter) => letter.toUpperCase());
}

function printUsage() {
    console.error(`Usage:
  npm run screenshot -- <path-or-url> <output.png> [--viewport=1280x720] [--wait=selector] [--click=selector] [--delay=ms]

Env:
  POLKA_BASE_URL                 Base URL for relative paths; default ${DEFAULT_BASE_URL}
  POLKA_SCREENSHOT_USER          Optional login username
  POLKA_SCREENSHOT_PASSWORD      Optional login password

Examples:
  npm run screenshot -- / browser-test/screenshots/library.png --wait=.book-card
  POLKA_BASE_URL=http://127.0.0.1:8080 npm run screenshot -- / /tmp/menu.png --click=.account-trigger
`);
}
