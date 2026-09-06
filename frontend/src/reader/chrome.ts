const READER_CHROME_AUTO_HIDE_MS = 3_000;

function eventTargetElement(event: Event): Element | null {
    const target = event.target;
    if (target && typeof (target as Element).closest === 'function') return target as Element;
    return null;
}

export function shouldIgnoreReaderShortcut(event: Event): boolean {
    return Boolean(
        eventTargetElement(event)?.closest(
            'a, button, input, textarea, select, [contenteditable="true"]',
        ),
    );
}

export function toggleReaderChrome(page: HTMLElement): void {
    if (page.classList.contains('reader-chrome-hidden')) {
        revealChrome(page);
        return;
    }
    hideChrome(page);
}

export function focusReaderSurface(page: HTMLElement): void {
    page.querySelector<HTMLElement>('.reader-epub-stage, .reader-pdf-stage')?.focus({
        preventScroll: true,
    });
}

export function revealChrome(page: HTMLElement, autoHide = true): void {
    page.classList.remove('reader-chrome-hidden');
    const oldTimer = Number(page.dataset.chromeTimer || 0);
    if (oldTimer) window.clearTimeout(oldTimer);
    delete page.dataset.chromeTimer;
    if (!autoHide || !shouldAutoHideChrome()) return;

    const timer = window.setTimeout(() => {
        hideChrome(page);
    }, READER_CHROME_AUTO_HIDE_MS);
    page.dataset.chromeTimer = String(timer);
}

function hideChrome(page: HTMLElement): void {
    const oldTimer = Number(page.dataset.chromeTimer || 0);
    if (oldTimer) window.clearTimeout(oldTimer);
    delete page.dataset.chromeTimer;
    page.classList.add('reader-chrome-hidden');
}

export function shouldAutoHideChrome(): boolean {
    return window.matchMedia('(hover: none), (pointer: coarse)').matches;
}

export function closeReader(page: HTMLElement, beforeClose?: () => Promise<boolean>): void {
    const closeLink = page.querySelector<HTMLAnchorElement>('.reader-close[href]');
    if (!closeLink) return;
    if (!beforeClose) {
        window.location.href = closeLink.href;
        return;
    }
    void beforeClose().then((ready) => {
        if (ready) window.location.href = closeLink.href;
    });
}

export function showReaderError(page: HTMLElement, message: string): void {
    const stage = page.querySelector<HTMLElement>('.reader-epub-stage, .reader-pdf-stage');
    if (!stage) return;
    stage.innerHTML = '';
    const error = document.createElement('div');
    error.className = 'reader-loading reader-loading-error';
    error.textContent = message;
    stage.append(error);
}
