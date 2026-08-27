import { clamp } from '../dom';
import type { ReaderLocator } from '../types';
import type { FoliateRelocateDetail, FoliateTarget, FoliateViewElement } from './foliate-engine';
import type { ReaderPosition, ReaderStateSaver } from './state-saver';

export interface ReaderPositionSaver {
    enableSaving(): void;
    markUserNavigation(): void;
    flush(): Promise<void>;
    destroy(): void;
}

export async function restoreReaderPosition(
    view: FoliateViewElement,
    state: ReaderPosition | null,
): Promise<void> {
    const lastLocation = state ? storedLocation(state) : null;
    await view.init({ lastLocation, showTextStart: !lastLocation });
}

function storedLocation(state: ReaderPosition): FoliateTarget | null {
    const locator = state.locator;
    if (locator.engine === 'foliate') {
        if (typeof locator.cfi === 'string' && locator.cfi) {
            return locator.cfi;
        }
        if (typeof locator.fraction === 'number') {
            return { fraction: clampFraction(locator.fraction) };
        }
    }
    if (state.progress > 0 && state.progress < 1) {
        return { fraction: clampFraction(state.progress) };
    }
    return null;
}

export function wirePositionSaving(
    page: HTMLElement,
    view: FoliateViewElement,
    stateSaver: ReaderStateSaver,
    options: { savingEnabled?: boolean } = {},
): ReaderPositionSaver {
    let savingEnabled = options.savingEnabled ?? true;
    let skipNextRelocate = !savingEnabled;
    let userNavigationSeen = false;
    let saveTimer: number | undefined;
    let displayLocationSpan = 0;
    let progressLayoutKey = '';

    const flushPending = (options: { keepalive?: boolean } = {}): Promise<void> => {
        window.clearTimeout(saveTimer);
        saveTimer = undefined;
        return stateSaver.flush(options);
    };

    const scheduleSave = (detail: FoliateRelocateDetail, progress: number): void => {
        if (!savingEnabled) return;
        if (skipNextRelocate && !userNavigationSeen) {
            skipNextRelocate = false;
            return;
        }
        if (!userNavigationSeen && progress <= 0.001) return;

        stateSaver.queue({
            progress,
            locator: foliateLocator(detail, progress),
        });
        window.clearTimeout(saveTimer);
        saveTimer = window.setTimeout(() => {
            void flushPending();
        }, 700);
    };

    const relocateHandler = (event: Event) => {
        const detail = (event as CustomEvent<FoliateRelocateDetail>).detail;
        const progress = clampFraction(detail.fraction ?? 0);
        const layoutKey = progressDisplayLayoutKey(page, view);
        if (layoutKey !== progressLayoutKey) {
            progressLayoutKey = layoutKey;
            displayLocationSpan = 0;
        }
        displayLocationSpan = updateProgressText(page, detail, progress, displayLocationSpan);
        scheduleSave(detail, progress);
    };

    const pageHideHandler = () => {
        void flushPending({ keepalive: true });
    };

    const visibilityHandler = () => {
        if (document.visibilityState === 'hidden') {
            void flushPending({ keepalive: true });
        } else {
            void flushPending();
        }
    };

    view.addEventListener('relocate', relocateHandler);
    window.addEventListener('pagehide', pageHideHandler);
    document.addEventListener('visibilitychange', visibilityHandler);

    return {
        enableSaving(): void {
            savingEnabled = true;
            skipNextRelocate = true;
            userNavigationSeen = false;
        },
        markUserNavigation(): void {
            userNavigationSeen = true;
            skipNextRelocate = false;
        },
        flush: flushPending,
        destroy(): void {
            window.clearTimeout(saveTimer);
            view.removeEventListener('relocate', relocateHandler);
            window.removeEventListener('pagehide', pageHideHandler);
            document.removeEventListener('visibilitychange', visibilityHandler);
        },
    };
}

function foliateLocator(detail: FoliateRelocateDetail, progress: number): ReaderLocator {
    const locator: ReaderLocator = {
        engine: 'foliate',
        fraction: progress,
    };
    if (detail.cfi) locator.cfi = detail.cfi;
    return locator;
}

function progressDisplayLayoutKey(page: HTMLElement, view: FoliateViewElement): string {
    const renderer = view.renderer;
    return [
        page.dataset.readerFlow || '',
        page.dataset.readerStyle || '',
        page.dataset.readerFontScale || '',
        page.dataset.readerColumnWidth || '',
        page.dataset.readerLineHeight || '',
        renderer?.getAttribute('flow') || '',
        renderer?.getAttribute('max-inline-size') || '',
    ].join('|');
}

function updateProgressText(
    page: HTMLElement,
    detail: FoliateRelocateDetail,
    progress: number,
    previousLocationSpan: number,
): number {
    const target = page.querySelector<HTMLElement>('[data-reader-progress]');
    if (!target) return previousLocationSpan;

    const current = detail.location?.current;
    const next = detail.location?.next;
    const total = detail.location?.total;
    if (typeof current === 'number' && typeof total === 'number' && total > 0) {
        const span = visualLocationSpan(current, next, previousLocationSpan);
        const pageNumber = Math.min(Math.floor(current / span) + 1, Math.ceil(total / span));
        target.textContent = `${pageNumber} / ${Math.ceil(total / span)}`;
        return span;
    }

    target.textContent = `${Math.round(progress * 100)}%`;
    return previousLocationSpan;
}

function visualLocationSpan(current: number, next: number | undefined, previous: number): number {
    const candidate = typeof next === 'number' ? next - current : 0;
    if (candidate > 0 && (previous <= 0 || candidate > previous)) {
        return candidate;
    }
    return previous > 0 ? previous : 1;
}

function clampFraction(value: number): number {
    if (!Number.isFinite(value)) return 0;
    return clamp(value, 0, 1);
}
