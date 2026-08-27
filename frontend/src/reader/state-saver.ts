import { saveReaderState } from '../api';
import type { ReaderLocator, ReaderState } from '../types';

const RETRY_DELAYS_MS = [250, 750];

export interface ReaderPosition {
    progress: number;
    locator: ReaderLocator;
}

export interface ReaderStateSaver {
    queue(position: ReaderPosition): void;
    flush(options?: { keepalive?: boolean }): Promise<void>;
}

export function createReaderStateSaver(
    page: HTMLElement,
    assetId: string,
    options: { onStateSaved?: (state: ReaderState) => void } = {},
): ReaderStateSaver {
    let pending: ReaderPosition | null = null;
    let inFlight: { snapshot: ReaderPosition; promise: Promise<void> } | null = null;
    const status = page.querySelector<HTMLElement>('[data-reader-save-status]');

    const queue = (position: ReaderPosition): void => {
        pending = position;
    };

    const flush = (saveOptions: { keepalive?: boolean } = {}): Promise<void> => {
        const snapshot = pending;
        if (!snapshot) return inFlight?.promise ?? Promise.resolve();
        if (inFlight?.snapshot === snapshot) return inFlight.promise;

        const previous = inFlight?.promise ?? Promise.resolve();
        const promise = previous
            .catch(() => undefined)
            .then(async () => {
                // A newer relocation supersedes a queued-but-not-started one.
                // Only the latest position has product value.
                if (pending !== snapshot) return;
                await persistWithRetry(snapshot, saveOptions);
            })
            .finally(() => {
                if (inFlight?.snapshot === snapshot) inFlight = null;
            });
        inFlight = { snapshot, promise };
        return promise;
    };

    const persistWithRetry = async (
        snapshot: ReaderPosition,
        saveOptions: { keepalive?: boolean },
    ): Promise<void> => {
        let lastError: unknown;
        const delays = saveOptions.keepalive ? [] : RETRY_DELAYS_MS;
        for (let attempt = 0; attempt <= delays.length; attempt++) {
            if (pending !== snapshot) return;
            try {
                const saved = await saveReaderState(
                    assetId,
                    { progress: snapshot.progress, locator: snapshot.locator },
                    saveOptions,
                );
                if (pending === snapshot) pending = null;
                setSaveFailure(status, false);
                if (!saveOptions.keepalive) options.onStateSaved?.(saved);
                return;
            } catch (error) {
                lastError = error;
                if (attempt < delays.length) await wait(delays[attempt]);
            }
        }

        if (pending === snapshot) {
            setSaveFailure(status, true);
            console.warn('Reader position was not saved:', lastError);
        }
    };

    return { queue, flush };
}

function setSaveFailure(status: HTMLElement | null, failed: boolean): void {
    if (!status) return;
    status.hidden = !failed;
}

function wait(delay: number): Promise<void> {
    return new Promise((resolve) => window.setTimeout(resolve, delay));
}
