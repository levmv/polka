export const READING_IDLE_MS = 5 * 60_000;

export interface ActivitySample {
    elapsed_ms: number;
    last_activity_ms: number;
    finished: boolean;
}

// One visible segment; sampling leaves the idle deadline unchanged. Wall time
// covers platforms whose monotonic clock stops during sleep. Either clock is
// bounded by the last user action's idle deadline.
export class ReadingActivityClock {
    private elapsed = 0;
    private lastActivity = 0;
    private readonly monotonicStart: number;
    private readonly wallStart: number;

    constructor(monotonic: number, wall: number) {
        this.monotonicStart = monotonic;
        this.wallStart = wall;
    }

    sample(monotonic: number, wall: number): ActivitySample {
        // Compare totals from one origin. Taking the larger of each small
        // delta would accumulate the clocks' rounding differences.
        this.elapsed = Math.max(
            this.elapsed,
            monotonic - this.monotonicStart,
            wall - this.wallStart,
        );
        const deadline = this.lastActivity + READING_IDLE_MS;
        return {
            elapsed_ms: Math.floor(Math.min(this.elapsed, deadline)),
            last_activity_ms: Math.floor(this.lastActivity),
            finished: this.elapsed >= deadline,
        };
    }

    // An expired clock cannot resume; the caller must start a new segment.
    recordAction(monotonic: number, wall: number): boolean {
        if (this.sample(monotonic, wall).finished) return false;
        this.lastActivity = this.elapsed;
        return true;
    }
}
