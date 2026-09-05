import assert from 'node:assert/strict';
import test from 'node:test';

import { ReadingActivityClock } from '../src/reader/activity-clock.ts';

test('checkpoints never extend the idle deadline', () => {
    const clock = new ReadingActivityClock(0, 0);
    for (let minute = 1; minute < 5; minute++) {
        assert.deepEqual(clock.sample(minute * 60_000, minute * 60_000), {
            elapsed_ms: minute * 60_000,
            last_activity_ms: 0,
            finished: false,
        });
    }
    assert.deepEqual(clock.sample(3_600_000, 3_600_000), {
        elapsed_ms: 300_000,
        last_activity_ms: 0,
        finished: true,
    });
    assert.equal(clock.recordAction(3_600_001, 3_600_001), false);
    assert.equal(clock.sample(3_660_000, 3_660_000).elapsed_ms, 300_000);
});

test('real activity extends a live interval but cannot credit an idle gap', () => {
    const clock = new ReadingActivityClock(0, 0);
    assert.equal(clock.recordAction(240_000, 240_000), true);
    assert.equal(clock.sample(300_000, 300_000).finished, false);
    assert.deepEqual(clock.sample(600_000, 600_000), {
        elapsed_ms: 540_000,
        last_activity_ms: 240_000,
        finished: true,
    });
    const resumed = new ReadingActivityClock(600_000, 600_000);
    assert.deepEqual(resumed.sample(660_000, 660_000), {
        elapsed_ms: 60_000,
        last_activity_ms: 0,
        finished: false,
    });
});

test('sleep is bounded even when the monotonic clock pauses', () => {
    const clock = new ReadingActivityClock(0, 0);
    clock.sample(60_000, 60_000);
    assert.deepEqual(clock.sample(61_000, 3_660_000), {
        elapsed_ms: 300_000,
        last_activity_ms: 0,
        finished: true,
    });
});

test('moving the wall clock backwards neither removes nor duplicates time', () => {
    const clock = new ReadingActivityClock(0, 3_600_000);
    assert.equal(clock.sample(60_000, 60_000).elapsed_ms, 60_000);
    assert.equal(clock.sample(120_000, 120_000).elapsed_ms, 120_000);
});

test('sampling frequency and wall-clock rounding do not accumulate reading time', () => {
    const clock = new ReadingActivityClock(0, 0);
    for (let i = 1; i <= 10_000; i++) {
        const monotonic = i * 0.6;
        clock.sample(monotonic, Math.floor(monotonic));
    }
    assert.equal(clock.sample(6_000, 6_000).elapsed_ms, 6_000);
});
