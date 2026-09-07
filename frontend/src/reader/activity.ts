import { fetchUserSettings, sendReadingActivity } from '../api';
import { type ActivitySample, ReadingActivityClock } from './activity-clock';

const CHECKPOINT_MS = 60_000;
const ACTION_SAVE_DELAY_MS = 1_000;

// One visible interval. Short pauses keep sessionId and advance number.
interface ActivitySegment {
    sessionId: string;
    number: number;
    clock: ReadingActivityClock | null;
    pending: { sample: ActivitySample; userAction: boolean } | null;
    sending: boolean;
}

export interface ReadingActivity {
    start(): void;
    recordAction(): void;
    observeScrolling(target: EventTarget): void;
}

export function createReadingActivity(assetId: number): ReadingActivity {
    let current: ActivitySegment | null = null;
    let paused: {
        segment: ActivitySegment;
        sample: ActivitySample;
        flushed: Promise<unknown>;
    } | null = null;
    let ready = false;
    let timer: number | undefined;
    let actionTimer: number | undefined;
    const observed = new WeakSet<EventTarget>();

    const visible = () => document.visibilityState === 'visible';
    const sample = (clock: ReadingActivityClock) => clock.sample(performance.now(), Date.now());

    const clearTimers = () => {
        window.clearTimeout(timer);
        window.clearTimeout(actionTimer);
        timer = undefined;
        actionTimer = undefined;
    };

    const begin = () => {
        if (!ready || !visible() || current) return;
        const previous = paused;
        const resume = previous?.segment.clock && !sample(previous.segment.clock).finished;
        paused = null;
        const segment: ActivitySegment = {
            sessionId: resume ? previous.segment.sessionId : newSessionID(),
            number: resume ? previous.segment.number + 1 : 0,
            clock: null,
            pending: null,
            sending: false,
        };
        const clock = new ReadingActivityClock(performance.now(), Date.now());
        current = segment;
        // Flush the old segment before replacing its server clock; resuming
        // first would make the server discard that segment's last report.
        void Promise.all([fetchUserSettings(), resume ? previous.flushed : undefined])
            .then(([settings]) => {
                if (
                    current !== segment ||
                    !visible() ||
                    !settings.time_zone ||
                    sample(clock).finished
                )
                    return null;
                return sendReadingActivity(assetId, segment.sessionId, segment.number);
            })
            .then((result) => {
                if (!result) {
                    if (current === segment) current = null;
                    return;
                }
                if (current !== segment || !visible()) {
                    // A late start response must not leave a hidden reader
                    // holding time accounting. Close it without adding time.
                    void sendReadingActivity(
                        assetId,
                        segment.sessionId,
                        segment.number,
                        {
                            elapsed_ms: 0,
                            last_activity_ms: 0,
                            finished: true,
                        },
                        true,
                    ).catch(() => undefined);
                    return;
                }
                if (!result.active) {
                    current = null;
                    // An expired or displaced resume can start fresh once.
                    // A rejected initial start must not enter a retry loop.
                    if (segment.number > 0 && !sample(clock).finished) begin();
                    return;
                }
                segment.clock = clock;
                if (sample(clock).finished) checkpoint(false, true);
                else scheduleCheckpoint();
            })
            .catch(() => {
                if (current === segment) {
                    current = null;
                    // Retry on the next real action. A delayed reconnection
                    // must not start counting an abandoned visible reader.
                }
            });
    };

    const sendPending = async (segment: ActivitySegment): Promise<void> => {
        if (segment.sending || !segment.pending) return;
        segment.sending = true;
        const pending = segment.pending;
        try {
            const result = await sendReadingActivity(
                assetId,
                segment.sessionId,
                segment.number,
                pending.sample,
            );
            if (segment.pending === pending) segment.pending = null;
            if (!result.active && current === segment) {
                const reclaim = pending.userAction || segment.pending?.userAction;
                current = null;
                clearTimers();
                // A heartbeat cannot steal accounting back from another
                // reader. A real action can start a fresh identity and claim it.
                if (reclaim && segment.clock && !sample(segment.clock).finished) begin();
            }
        } catch {
            // Retain the checkpoint for retry, but an old action must not
            // reclaim accounting from another reader after reconnection.
            if (segment.pending) segment.pending.userAction = false;
        } finally {
            segment.sending = false;
            if (current === segment && segment.pending && segment.pending !== pending) {
                void sendPending(segment);
            }
        }
    };

    const checkpoint = (userAction = false, finish = false) => {
        const segment = current;
        if (!segment?.clock) return;
        const value = sample(segment.clock);
        value.finished ||= finish;
        if (value.finished) {
            current = null;
            clearTimers();
            // Do not queue this behind an in-flight fetch: the document may be
            // leaving. The server accepts reordered cumulative checkpoints.
            void sendReadingActivity(assetId, segment.sessionId, segment.number, value, true).catch(
                () => undefined,
            );
            return;
        }
        segment.pending = {
            sample: value,
            userAction: userAction || !!segment.pending?.userAction,
        };
        void sendPending(segment);
    };

    const scheduleCheckpoint = () => {
        window.clearTimeout(timer);
        timer = window.setTimeout(() => {
            checkpoint();
            if (current?.clock) scheduleCheckpoint();
        }, CHECKPOINT_MS);
    };

    const recordAction = () => {
        if (!ready || !visible()) return;
        if (!current) {
            begin();
            return;
        }
        if (!current.clock) return;
        if (!current.clock.recordAction(performance.now(), Date.now())) {
            checkpoint(false, true);
            begin();
            return;
        }
        // A burst of scroll events needs one request, not one per event.
        if (actionTimer === undefined) {
            actionTimer = window.setTimeout(() => {
                actionTimer = undefined;
                checkpoint(true);
            }, ACTION_SAVE_DELAY_MS);
        }
    };

    const stop = (finish: boolean) => {
        const segment = current;
        if (segment?.clock) {
            const value = sample(segment.clock);
            value.finished ||= finish;
            const flushed = sendReadingActivity(
                assetId,
                segment.sessionId,
                segment.number,
                value,
                true,
            ).catch(() => undefined);
            paused = value.finished ? null : { segment, sample: value, flushed };
        } else if (finish && paused) {
            // visibilitychange can precede pagehide. Close at the saved visible
            // boundary, never at a later time spent in the hidden page.
            const previous = paused;
            paused = null;
            void sendReadingActivity(
                assetId,
                previous.segment.sessionId,
                previous.segment.number,
                { ...previous.sample, finished: true },
                true,
            ).catch(() => undefined);
        }
        current = null;
        clearTimers();
    };

    document.addEventListener('visibilitychange', () => {
        if (visible()) begin();
        else stop(false);
    });
    window.addEventListener('pagehide', (event) => stop(!event.persisted));
    window.addEventListener('pageshow', begin);

    return {
        start() {
            ready = true;
            begin();
        },
        recordAction,
        observeScrolling(target) {
            if (observed.has(target)) return;
            observed.add(target);
            const scrolling = (event: Event) => {
                if (event.isTrusted) recordAction();
            };
            target.addEventListener('wheel', scrolling, { passive: true });
            target.addEventListener('touchmove', scrolling, { passive: true });
            target.addEventListener('keydown', (event) => {
                const key = event as KeyboardEvent;
                const element = key.target as Element | null;
                if (element?.closest('input, textarea, select, [contenteditable="true"]')) return;
                if (
                    key.isTrusted &&
                    ['ArrowUp', 'ArrowDown', 'PageUp', 'PageDown', 'Home', 'End', ' '].includes(
                        key.key,
                    )
                )
                    recordAction();
            });
        },
    };
}

function newSessionID(): string {
    return Array.from(crypto.getRandomValues(new Uint8Array(16)), (byte) =>
        byte.toString(16).padStart(2, '0'),
    ).join('');
}
