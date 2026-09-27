import { readEntryID, readPredecessorURL } from './history-state';

const ORIGIN_KEY = 'polka-reader-origin';

// The reader is a separate document. Only an ordinary same-tab navigation
// hands it a predecessor; a new tab or direct link keeps the book fallback.
export function rememberReaderOrigin(url: URL): void {
    if (url.origin !== location.origin || !url.pathname.startsWith('/read/')) return;
    sessionStorage.setItem(
        ORIGIN_KEY,
        JSON.stringify({
            url: url.href,
            from: location.href.split('#')[0],
            index: (history.state?.polkaIndex ?? 0) + 1,
        }),
    );
}

export function adoptReaderOrigin(): void {
    const pending = sessionStorage.getItem(ORIGIN_KEY);
    if (!pending) return;
    sessionStorage.removeItem(ORIGIN_KEY);
    try {
        const origin = JSON.parse(pending);
        if (
            origin.url === location.href &&
            origin.from === document.referrer &&
            !readEntryID(history.state)
        ) {
            history.replaceState({ polkaFrom: origin.from, polkaIndex: origin.index }, '');
        }
    } catch {
        // An incomplete handoff leaves the direct-link fallback in place.
    }
}

export function closeReaderHistory(fallback: string): void {
    if (readPredecessorURL(history.state)) history.back();
    else location.replace(fallback);
}
