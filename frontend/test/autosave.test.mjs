import assert from 'node:assert/strict';
import test from 'node:test';
import { setImmediate } from 'node:timers/promises';
import { createAutosavedValue } from '../src/autosave.ts';

test('autosave cancels replaced writes and accepts only the latest result', async () => {
    for (const earlierSucceeds of [true, false]) {
        const writes = [];
        const errors = [];
        const changes = [];
        const setting = createAutosavedValue(
            'A',
            (value, signal) => {
                const request = Promise.withResolvers();
                writes.push({ value, signal, ...request });
                return request.promise;
            },
            (error) => errors.push(error),
        );
        setting.subscribe((value) => changes.push(value));

        setting.set('B');
        setting.set('A');
        assert.equal(setting.value, 'A');
        assert.deepEqual(
            writes.map((write) => write.value),
            ['B', 'A'],
        );
        assert.equal(writes[0].signal.aborted, true);
        assert.equal(writes[1].signal.aborted, false);

        if (earlierSucceeds) writes[0].resolve('B');
        else writes[0].reject(new Error('Earlier write failed'));
        await setImmediate();
        assert.equal(setting.value, 'A');
        assert.deepEqual(errors, []);

        setting.set('C');
        assert.equal(writes[1].signal.aborted, true);
        assert.equal(writes[2].signal.aborted, false);
        writes[2].resolve('C');
        await setImmediate();
        assert.equal(setting.value, 'C');

        if (earlierSucceeds) writes[1].resolve('A');
        else writes[1].reject(new Error('Earlier write failed'));
        await setImmediate();
        assert.equal(setting.value, 'C');
        assert.deepEqual(errors, []);

        setting.set('D');
        const failure = new Error('Latest write failed');
        writes[3].reject(failure);
        await setImmediate();
        assert.equal(setting.value, 'C');
        assert.deepEqual(errors, [failure]);
        assert.deepEqual(changes, ['B', 'A', 'C', 'D', 'C']);
    }
});
