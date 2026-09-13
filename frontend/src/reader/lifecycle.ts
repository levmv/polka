import type { ReadingActivity } from './activity';
import type { ReaderStateSaver } from './state-saver';

export interface ReaderLifecycle {
    start(): void;
    observeDocument(doc: Document): void;
    markUserNavigation(): void;
}

// A reader owns the document, including its bfcache lifetime. Connect browser
// events here; position sync, annotations and time accounting own their policies.
export function wireReaderLifecycle(
    page: HTMLElement,
    stage: HTMLElement,
    position: ReaderStateSaver,
    activity: ReadingActivity,
    options: { onNavigate?: () => void; onResume?: () => void } = {},
): ReaderLifecycle {
    const { onNavigate, onResume } = options;
    const documents = new WeakSet<Document>();
    let started = false;
    const resume = (): void => {
        void position.resume();
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
    // Foliate scrolling can change the stored location. A PDF scroll only pans
    // the current page; its explicit page changes call markUserNavigation.
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
                    position.suspend();
                    activity.suspend(false);
                }
            });
            window.addEventListener('pagehide', (event) => {
                position.suspend();
                activity.suspend(!event.persisted);
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
