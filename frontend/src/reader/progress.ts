import { clamp } from '../dom';
import { showReaderError } from './chrome';
import type { FoliateRelocateDetail, FoliateTarget, FoliateViewElement } from './foliate-engine';
import { foliateLocation } from './location';
import type { ReaderPosition, ReaderStateSaver } from './state-saver';

export interface ReaderPositionSaver {
    enableSaving(): void;
    markUserNavigation(): void;
    restorePosition(state: ReaderPosition): Promise<void>;
}

export async function restoreReaderPosition(
    view: FoliateViewElement,
    state: ReaderPosition | null,
    target?: string,
): Promise<void> {
    let lastLocation = target ?? (state ? storedLocation(state) : null);
    if (!target && typeof lastLocation === 'string') {
        let hasChapter = false;
        try {
            hasChapter = !!view.book?.sections?.[view.resolveCFI(lastLocation).index];
        } catch {
            // A position from another copy may have an unusable CFI.
        }
        if (!hasChapter) lastLocation = fractionLocation(state);
    }
    try {
        await view.init({ lastLocation, showTextStart: !lastLocation });
    } catch (error) {
        if (target || typeof lastLocation !== 'string') throw error;
        // Resolving a CFI's chapter does not validate its DOM node or offset.
        // Try the saved percentage once if that anchor fails during loading.
        lastLocation = fractionLocation(state);
        await view.init({ lastLocation, showTextStart: !lastLocation });
    }
}

function storedLocation(state: ReaderPosition): FoliateTarget | null {
    if (state.locator.cfi) return state.locator.cfi;
    return fractionLocation(state);
}

function fractionLocation(state: ReaderPosition | null): FoliateTarget | null {
    if (state && state.progress > 0 && state.progress <= 1) {
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
    let userNavigationSeen = false;
    let saveTimer: number | undefined;
    let displayLocationSpan = 0;
    let progressLayoutKey = '';
    let currentPosition: ReaderPosition | null = null;
    let navigation = 0;

    const flushPending = (options: { keepalive?: boolean } = {}): Promise<void> => {
        window.clearTimeout(saveTimer);
        saveTimer = undefined;
        userNavigationSeen = false;
        return stateSaver.flush(options);
    };

    const scheduleSave = (detail: FoliateRelocateDetail, progress: number): void => {
        if (!savingEnabled) return;
        // Rendering, restoration and layout changes are not new observations.
        if (!userNavigationSeen) return;

        stateSaver.queue({
            progress,
            locator: foliateLocation(page, view, detail.cfi, detail.range),
        });
        window.clearTimeout(saveTimer);
        saveTimer = window.setTimeout(() => {
            void flushPending();
        }, 700);
    };

    const relocateHandler = (event: Event) => {
        const detail = (event as CustomEvent<FoliateRelocateDetail>).detail;
        const progress = clampFraction(detail.fraction ?? 0);
        currentPosition = { progress, locator: { cfi: detail.cfi } };
        const layoutKey = progressDisplayLayoutKey(page, view);
        if (layoutKey !== progressLayoutKey) {
            progressLayoutKey = layoutKey;
            displayLocationSpan = 0;
        }
        displayLocationSpan = updateProgressText(page, detail, progress, displayLocationSpan);
        scheduleSave(detail, progress);
    };

    view.addEventListener('relocate', relocateHandler);

    return {
        enableSaving(): void {
            savingEnabled = true;
            userNavigationSeen = false;
        },
        markUserNavigation(): void {
            navigation++;
            userNavigationSeen = true;
        },
        async restorePosition(state): Promise<void> {
            window.clearTimeout(saveTimer);
            userNavigationSeen = false;
            const previous = currentPosition;
            const beforeRestore = navigation;
            try {
                await restoreReaderPosition(view, state);
            } catch (error) {
                // A bad remote anchor must not leave a working reader blank.
                // Do not undo a page turn made while restoration was loading.
                if (navigation === beforeRestore) {
                    try {
                        await restoreReaderPosition(view, previous);
                    } catch {
                        showReaderError(page, 'Could not open this book.');
                    }
                }
                throw error;
            }
        },
    };
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
