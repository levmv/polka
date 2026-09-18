import { clamp } from '../dom';
import type { ReaderPosition } from '../types';
import { showReaderError } from './chrome';
import type { FoliateRelocateDetail, FoliateTarget, FoliateViewElement } from './foliate-engine';
import { foliateLocation } from './location';
import type { PositionSaver } from './position-saver';

export interface FoliatePositionController {
    enableSaving(): void;
    markUserNavigation(): void;
    restorePosition(state: ReaderPosition): Promise<void>;
}

export async function restoreFoliatePosition(
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

export function wireFoliatePosition(
    page: HTMLElement,
    view: FoliateViewElement,
    positionSaver: PositionSaver,
    options: { savingEnabled?: boolean } = {},
): FoliatePositionController {
    let savingEnabled = options.savingEnabled ?? true;
    let userNavigationSeen = false;
    let saveTimer: number | undefined;
    let currentPosition: ReaderPosition | null = null;
    let navigation = 0;

    const scheduleSave = (detail: FoliateRelocateDetail, progress: number): void => {
        if (!savingEnabled) return;
        // Rendering, restoration and layout changes are not new observations.
        if (!userNavigationSeen) return;

        positionSaver.queue({
            progress,
            locator: foliateLocation(page, view, detail.cfi, detail.range),
        });
        window.clearTimeout(saveTimer);
        saveTimer = window.setTimeout(() => {
            saveTimer = undefined;
            userNavigationSeen = false;
            void positionSaver.flush();
        }, 700);
    };

    const relocateHandler = (event: Event) => {
        const detail = (event as CustomEvent<FoliateRelocateDetail>).detail;
        const progress = clampFraction(detail.fraction ?? 0);
        currentPosition = { progress, locator: { cfi: detail.cfi } };
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
                await restoreFoliatePosition(view, state);
            } catch (error) {
                // A bad remote anchor must not leave a working reader blank.
                // Do not undo a page turn made while restoration was loading.
                if (navigation === beforeRestore) {
                    try {
                        await restoreFoliatePosition(view, previous);
                    } catch {
                        showReaderError(page, 'Could not open this book.');
                    }
                }
                throw error;
            }
        },
    };
}

function clampFraction(value: number): number {
    if (!Number.isFinite(value)) return 0;
    return clamp(value, 0, 1);
}
