import type { ScrollPosition } from '../router';

// Putting the reader back where they were, for a list that was detached and
// then brought back. It is a unit of its own because none of it is about
// listing books: it needs only the route root, the selector for a rendered
// book, and whether that root is on screen.
//
// The window position alone cannot do this. Detaching shortens the document and
// the browser clamps the scroll away, and by the time the list is back its
// geometry may have changed — a resize, a font or theme change, covers that
// settled late, a book removed while the reader was away. So the position is
// remembered in the list's own terms and recomputed from them on return.

// Where the reader was: the first book at least partly on screen, and where its
// top sat in the viewport. It records the neighbourhood that was visible, not a
// book to chase — if that book is gone, the saved pixels are the better answer.
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

// A rebuilt list is rendered before its covers are: the cards start at the
// placeholder ratio and take their real height only as each image decodes, so
// the anchored book keeps moving for as long as that takes. Re-applying the
// position on every size change keeps that book under the reader's eye instead
// of letting the list slide past it, and stops once the list has been quiet
// long enough to call the covers arrived — or as soon as the reader scrolls,
// whose position always wins.
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
    let anchor: ReturnAnchor | null = null;
    let focusedSelector: string | null = null;
    let settleCleanup: (() => void) | null = null;

    const bookSelector = () => opts.renderedBookSelector();

    // An anchored book that is still rendered wins: pixels are only exact while
    // the geometry is unchanged. A book that is gone leaves the saved pixel
    // position, which keeps the viewport in the old neighbourhood instead of
    // chasing it.
    const target = (pixelFallback: ScrollPosition | null): ScrollPosition | null => {
        if (anchor) {
            const el = root.querySelector<HTMLElement>(
                `${bookSelector()}[data-id=${CSS.escape(anchor.id)}]`,
            );
            if (el) {
                const y = Math.max(
                    0,
                    el.getBoundingClientRect().top + window.scrollY - anchor.viewportOffset,
                );
                return { x: pixelFallback?.x ?? 0, y };
            }
        }
        return pixelFallback;
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

    const captureAnchor = (): ReturnAnchor | null => {
        for (const el of root.querySelectorAll<HTMLElement>(bookSelector())) {
            const rect = el.getBoundingClientRect();
            if (rect.bottom <= 0) continue;
            const id = el.dataset.id;
            if (id) return { id, viewportOffset: rect.top };
        }
        return null;
    };

    const stop = (): void => {
        settleCleanup?.();
    };

    return {
        capture(): void {
            anchor = captureAnchor();
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
