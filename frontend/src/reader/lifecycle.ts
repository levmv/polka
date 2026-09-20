import type { ReadingActivity } from './activity';
import type { PositionSaver } from './position-saver';

export interface ReaderLifecycle {
    start(): void;
    observeDocument(doc: Document): void;
    markUserNavigation(): void;
}

// Share browser lifecycle events across reader components, including bfcache restores.
export function wireReaderLifecycle(
    page: HTMLElement,
    stage: HTMLElement,
    position: PositionSaver,
    activity: ReadingActivity,
    options: {
        onNavigate?: () => void;
        onResume?: () => void;
        onSuspend?: (finish: boolean) => void;
    } = {},
): ReaderLifecycle {
    const { onNavigate, onResume, onSuspend } = options;
    const documents = new WeakSet<Document>();
    let started = false;
    let resuming: Promise<void> | undefined;
    const resume = (): void => {
        // Focus and visibility can both announce the same return to the reader.
        if (resuming) return;
        const pending = position.resume();
        resuming = pending;
        void pending.finally(() => {
            if (resuming === pending) resuming = undefined;
        });
        onResume?.();
    };
    const markNavigation = (): void => {
        onNavigate?.();
        position.markUserNavigation();
    };
    const observeInput = (doc: Document): void => {
        for (const type of ['pointerdown', 'keydown', 'wheel', 'touchmove']) {
            doc.addEventListener(
                type,
                (event) => {
                    if (event.isTrusted) position.recordActivity();
                },
                { capture: true, passive: true },
            );
        }
    };
    const observeReading = (target: EventTarget, navigateOnScroll: boolean): void => {
        for (const type of ['wheel', 'touchmove']) {
            target.addEventListener(
                type,
                (event) => {
                    if (!event.isTrusted) return;
                    if (navigateOnScroll) markNavigation();
                    activity.recordAction();
                },
                { passive: true },
            );
        }
        target.addEventListener('keydown', (event) => {
            const key = event as KeyboardEvent;
            const element = key.target as Element | null;
            if (element?.closest('input, textarea, select, [contenteditable="true"]')) return;
            if (
                key.isTrusted &&
                ['ArrowUp', 'ArrowDown', 'PageUp', 'PageDown', 'Home', 'End', ' '].includes(key.key)
            ) {
                activity.recordAction();
            }
        });
    };

    observeReading(stage, false);
    // Foliate scrolling can change the stored location. PDF/DjVu scrolling only
    // pans the current page; explicit page changes call markUserNavigation.
    if (onNavigate) {
        for (const type of ['wheel', 'touchmove']) {
            page.addEventListener(
                type,
                (event) => {
                    if (event.isTrusted) markNavigation();
                },
                { passive: true },
            );
        }
    }

    return {
        start(): void {
            if (started) return;
            started = true;
            observeInput(document);
            window.addEventListener('focus', () => {
                if (document.visibilityState === 'visible') resume();
            });
            document.addEventListener('visibilitychange', () => {
                if (document.visibilityState === 'visible') {
                    resume();
                    activity.resume();
                } else {
                    resuming = undefined;
                    position.suspend();
                    activity.suspend(false);
                    onSuspend?.(false);
                }
            });
            window.addEventListener('pagehide', (event) => {
                resuming = undefined;
                position.suspend();
                activity.suspend(!event.persisted);
                onSuspend?.(!event.persisted);
            });
            window.addEventListener('pageshow', (event) => {
                if (event.persisted) resume();
                activity.resume();
            });
            window.addEventListener('online', () => {
                position.reconnect();
                if (document.visibilityState === 'visible') onResume?.();
            });
            position.start();
            activity.start();
        },
        observeDocument(doc): void {
            if (documents.has(doc)) return;
            documents.add(doc);
            observeInput(doc);
            observeReading(doc, !!onNavigate);
        },
        markUserNavigation(): void {
            markNavigation();
            activity.recordAction();
        },
    };
}
