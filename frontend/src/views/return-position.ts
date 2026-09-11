import type { ScrollPosition } from '../router';

// Retained navigation and same-query refreshes preserve the visible neighbourhood.
// Several visible books are recorded so removing one need not lose the anchor.
interface ReturnAnchor {
    id: string;
    viewportOffset: number;
}

export interface ReturnPosition {
    // Called while the root is still laid out: geometry is unreadable once it
    // leaves the document.
    capture(): void;
    // Called once the root is back in the document.
    restore(pixelFallback: ScrollPosition | null): void;
    // Called after a rebuild has replaced the DOM under the same anchor.
    settle(pixelFallback: ScrollPosition | null): void;
    // Ends any settling in progress; safe to call at any time.
    stop(): void;
}

// Covers may change card heights after rendering. Preserve the neighbourhood
// until layout settles, but let user scrolling take over immediately.
const QUIET_MS = 250;
const MAX_MS = 5000;

export function createReturnPosition(opts: {
    root: HTMLElement;
    // The rendered book element, which differs between the grid and the table.
    renderedBookSelector: () => string;
    // Whether the root is the page on screen. Settling stops when it is not.
    isActive: () => boolean;
}): ReturnPosition {
    const { root, isActive } = opts;
    let anchors: ReturnAnchor[] = [];
    let capturedPixels: ScrollPosition | null = null;
    let focusedSelector: string | null = null;
    let settleCleanup: (() => void) | null = null;

    const bookSelector = () => opts.renderedBookSelector();

    // Prefer a surviving visible book. If none remain, fall back to pixels
    // rather than chasing a book that has left the browsed range.
    const target = (pixelFallback: ScrollPosition | null): ScrollPosition | null => {
        for (const anchor of anchors) {
            const el = root.querySelector<HTMLElement>(
                `${bookSelector()}[data-id=${CSS.escape(anchor.id)}]`,
            );
            if (el) {
                const y = Math.max(
                    0,
                    el.getBoundingClientRect().top + window.scrollY - anchor.viewportOffset,
                );
                return { x: pixelFallback?.x ?? capturedPixels?.x ?? 0, y };
            }
        }
        return pixelFallback ?? capturedPixels;
    };

    const apply = (pixelFallback: ScrollPosition | null): void => {
        const to = target(pixelFallback);
        if (!to) return;
        if (Math.abs(window.scrollY - to.y) > 1) window.scrollTo(to.x, to.y);
    };

    // Focus is lost when the root leaves the document, so the control that had
    // it is recorded by a root-scoped selector rather than by node reference.
    // The usual case is the link that was just activated: a keyboard reader who
    // opened a book and came back should land on it again, not at the top.
    const captureFocus = (): string | null => {
        const active = document.activeElement;
        if (!(active instanceof HTMLElement) || !root.contains(active)) return null;
        if (active.id) return `#${CSS.escape(active.id)}`;
        const bookId = active.closest<HTMLElement>('.book-card, .table-row')?.dataset.id;
        if (!bookId) return null;
        const book = `${bookSelector()}[data-id=${CSS.escape(bookId)}]`;
        // A table row carries several controls, so the class says which one it
        // was: the reader comes back to the link they left from rather than to
        // whichever link that row happens to render first.
        const control = active.classList.item(0);
        return control ? `${book} .${CSS.escape(control)}` : `${book} a`;
    };

    const captureAnchors = (): ReturnAnchor[] => {
        const found: ReturnAnchor[] = [];
        for (const el of root.querySelectorAll<HTMLElement>(bookSelector())) {
            const rect = el.getBoundingClientRect();
            if (rect.bottom <= 0) continue;
            if (rect.top >= window.innerHeight) break;
            const id = el.dataset.id;
            if (id) found.push({ id, viewportOffset: rect.top });
        }
        return found;
    };

    const stop = (): void => {
        settleCleanup?.();
    };

    return {
        capture(): void {
            anchors = captureAnchors();
            capturedPixels = { x: window.scrollX, y: window.scrollY };
            focusedSelector = captureFocus();
            stop();
        },
        // One pass is enough here: the rendered list comes back exactly as it
        // was left, so the document is already its old height and the target is
        // reachable. Only a rebuild needs the settling below.
        restore(pixelFallback: ScrollPosition | null): void {
            apply(pixelFallback);
            const selector = focusedSelector;
            focusedSelector = null;
            if (selector) root.querySelector<HTMLElement>(selector)?.focus({ preventScroll: true });
        },
        settle(pixelFallback: ScrollPosition | null): void {
            stop();
            apply(pixelFallback);

            const grid = root.querySelector<HTMLElement>('#library-grid');
            if (!grid || typeof ResizeObserver === 'undefined') return;

            const events = ['wheel', 'touchstart', 'keydown'] as const;
            let quiet = 0;
            const finish = () => {
                settleCleanup = null;
                observer.disconnect();
                window.clearTimeout(quiet);
                window.clearTimeout(backstop);
                for (const name of events) window.removeEventListener(name, finish);
            };
            const waitForQuiet = () => {
                window.clearTimeout(quiet);
                quiet = window.setTimeout(finish, QUIET_MS);
            };
            const observer = new ResizeObserver(() => {
                if (!isActive()) return finish();
                apply(pixelFallback);
                waitForQuiet();
            });
            const backstop = window.setTimeout(finish, MAX_MS);
            for (const name of events) window.addEventListener(name, finish, { passive: true });
            observer.observe(grid);
            waitForQuiet();
            settleCleanup = finish;
        },
        stop,
    };
}
