import { APIError, fetchReaderState, saveReaderState } from '../api';
import { showToast } from '../toast';
import type { Locator, ReaderState, ReaderStateWrite } from '../types';
import { READING_IDLE_MS } from './activity-clock';
import { type PositionWriteResult, ReaderPositionSync } from './position-sync';

const RETRY_DELAYS_MS = [250, 750];
const BACKGROUND_RETRY_MS = 5_000;
const MAX_BACKGROUND_RETRY_MS = 60_000;

export interface ReaderPosition {
    progress: number;
    locator: Locator;
}

export interface ReaderStateSaver {
    initialize(state: ReaderState | null): void;
    start(): void;
    resume(): Promise<void>;
    suspend(): void;
    reconnect(): void;
    recordActivity(): void;
    markUserNavigation(): void;
    queue(position: ReaderPosition): void;
    flush(options?: { keepalive?: boolean }): Promise<void>;
}

// Retries and feedback are shared by both reader engines.
// Pending position and merge rules live in ReaderPositionSync, without DOM or HTTP.
export function createReaderStateSaver(
    assetId: number,
    options: {
        onStateSaved?: (state: ReaderState) => void;
        restorePosition: (state: ReaderState) => Promise<void>;
    },
): ReaderStateSaver {
    let initialized = false;
    let ready = false;
    let suspended = false;
    let navigation = 0;
    let lastActivity = Date.now();
    let retryTimer: number | undefined;
    let retryDelay = BACKGROUND_RETRY_MS;
    let dismissLoadError: (() => void) | undefined;
    const sync = new ReaderPositionSync({
        read: async () => {
            const state = await fetchReaderState(assetId);
            initialized = true;
            dismissLoadError?.();
            dismissLoadError = undefined;
            resetRetry();
            return state;
        },
        write: async (payload, keepalive) => {
            const result = await writePosition(assetId, payload, keepalive);
            if (result.kind === 'saved') resetRetry();
            return result;
        },
        restore: options.restorePosition,
        onSaved: (state) => options.onStateSaved?.(state),
        retryLater: () => {
            if (retryTimer !== undefined || !ready || suspended) return;
            retryTimer = window.setTimeout(() => {
                retryTimer = undefined;
                if (document.visibilityState !== 'visible') return;
                // navigator.onLine does not tell us whether a local server is reachable.
                // A background retry must not move someone who kept reading.
                if (canResumeInBackground()) void resume();
                else void sync.flush();
            }, retryDelay);
            retryDelay = Math.min(retryDelay * 2, MAX_BACKGROUND_RETRY_MS);
        },
    });

    const clearRetry = (): void => {
        window.clearTimeout(retryTimer);
        retryTimer = undefined;
    };

    const resetRetry = (): void => {
        clearRetry();
        retryDelay = BACKGROUND_RETRY_MS;
    };

    const resume = (): Promise<void> => {
        suspended = false;
        lastActivity = Date.now();
        const beforeRead = navigation;
        return sync.refresh(
            () => navigation === beforeRead && document.visibilityState === 'visible',
        );
    };

    const canResumeInBackground = (): boolean =>
        (!initialized && navigation === 0) || Date.now() - lastActivity >= READING_IDLE_MS;

    const recordActivity = (): void => {
        if (!ready) return;
        const now = Date.now();
        const idle = now - lastActivity >= READING_IDLE_MS;
        lastActivity = now;
        if (idle) void resume();
    };

    return {
        initialize(state): void {
            initialized = state !== null;
            sync.initialize(state);
        },
        start(): void {
            if (ready) return;
            ready = true;
            lastActivity = Date.now();
            if (!initialized) {
                dismissLoadError = showToast('Could not load the saved position.', {
                    type: 'error',
                    action: { label: 'Retry', onClick: () => void resume() },
                });
                void resume();
            }
        },
        resume,
        suspend(): void {
            suspended = true;
            clearRetry();
            void sync.flush(true);
        },
        reconnect(): void {
            if (canResumeInBackground()) void resume();
            else void sync.refresh();
        },
        recordActivity,
        markUserNavigation(): void {
            recordActivity();
            // Also invalidates a refresh started by the first action after idle.
            navigation++;
        },
        queue(position): void {
            sync.queue({
                ...position,
                ...readingDevice(),
            });
        },
        flush: (saveOptions = {}) => sync.flush(saveOptions.keepalive),
    };
}

async function writePosition(
    assetId: number,
    payload: ReaderStateWrite,
    keepalive: boolean,
): Promise<PositionWriteResult> {
    const delays = keepalive ? [] : RETRY_DELAYS_MS;
    for (let attempt = 0; attempt <= delays.length; attempt++) {
        try {
            return { kind: 'saved', state: await saveReaderState(assetId, payload, { keepalive }) };
        } catch (error) {
            if (error instanceof APIError && error.status === 409) return { kind: 'conflict' };
            if (error instanceof APIError && error.status < 500 && error.status !== 429) {
                return { kind: 'rejected' };
            }
            if (attempt < delays.length) {
                await new Promise((resolve) => window.setTimeout(resolve, delays[attempt]));
            }
        }
    }
    return { kind: 'unconfirmed' };
}

let deviceID = '';

function readingDevice(): { device_id: string; device_name: string } {
    if (!deviceID) {
        try {
            deviceID = localStorage.getItem('polka-reading-device') || '';
        } catch {
            // Generate an ID for this page when browser storage is unavailable.
        }
        if (!/^[a-f0-9]{32}$/.test(deviceID)) deviceID = readingID();
        try {
            localStorage.setItem('polka-reading-device', deviceID);
        } catch {
            // Reuse the ID in memory for the rest of this page's lifetime.
        }
    }
    const uuid = deviceID.replace(/^(.{8})(.{4})(.{4})(.{4})(.{12})$/, '$1-$2-$3-$4-$5');
    return { device_id: `urn:uuid:${uuid}`, device_name: 'polka web reader' };
}

// getRandomValues works on self-hosted HTTP as well as HTTPS.
function readingID(): string {
    const bytes = crypto.getRandomValues(new Uint8Array(16));
    bytes[6] = (bytes[6] & 15) | 64;
    bytes[8] = (bytes[8] & 63) | 128;
    return Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');
}
