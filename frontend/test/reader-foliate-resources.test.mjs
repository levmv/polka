import assert from 'node:assert/strict';
import test from 'node:test';
import { shareSectionResources } from '../src/reader/foliate-resources.ts';

test('pagination and reading share loads and retain resources until both release them', async () => {
    const first = Promise.withResolvers();
    const events = [];
    const sections = [0, 1].map((index) => ({
        load: async () => {
            events.push(`load ${index}`);
            if (index === 0) await first.promise;
            return `blob:${index}`;
        },
        unload: () => events.push(`unload ${index}`),
    }));
    shareSectionResources(sections);
    const measurement = sections[0].load();
    const reading = sections[0].load();
    const next = sections[1].load();
    await Promise.resolve();
    assert.deepEqual(events, ['load 0']);
    first.resolve();
    assert.deepEqual(await Promise.all([measurement, reading, next]), [
        'blob:0',
        'blob:0',
        'blob:1',
    ]);
    sections[0].unload();
    assert.deepEqual(events, ['load 0', 'load 1']);
    sections[0].unload();
    assert.deepEqual(events, ['load 0', 'load 1', 'unload 0']);
    assert.equal(await sections[0].load(), 'blob:0');
    assert.equal(events.at(-1), 'load 0');
    sections[0].unload();
    sections[1].unload();
});

test('a failed section releases resources and does not block later loads', async () => {
    let fail = true;
    let released = 0;
    const section = {
        load: async () => {
            if (fail) throw new Error('broken section');
            return 'blob:recovered';
        },
        unload: () => released++,
    };
    shareSectionResources([section]);
    const results = await Promise.allSettled([section.load(), section.load()]);
    assert.ok(results.every((result) => result.status === 'rejected'));
    assert.equal(released, 1);
    fail = false;
    assert.equal(await section.load(), 'blob:recovered');
    section.unload();
    assert.equal(released, 2);
});
