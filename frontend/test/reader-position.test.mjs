import assert from 'node:assert/strict';
import test from 'node:test';

import { ReaderPositionSync } from '../src/reader/position-sync.ts';

function observation(progress) {
    return {
        progress,
        locator: { page: Math.max(1, Math.round(progress * 10)) },
        device_id: 'urn:reader:test',
        device_name: 'Test reader',
    };
}

function savedPosition(progress, revision) {
    return { ...observation(progress), revision };
}

test('a newer observation survives a rejected or unconfirmed write', async () => {
    for (const outcome of ['rejected', 'unconfirmed']) {
        const { promise, resolve } = Promise.withResolvers();
        let saved = savedPosition(0, 0);
        const writes = [];
        const restored = [];
        let retries = 0;
        const sync = new ReaderPositionSync({
            read: async () => saved,
            write: async (payload) => {
                writes.push(payload);
                if (writes.length === 1) {
                    await promise;
                    if (outcome === 'unconfirmed') saved = { ...payload, revision: 1 };
                    return { kind: outcome };
                }
                if (payload.progress !== saved.progress)
                    saved = { ...payload, revision: saved.revision + 1 };
                return { kind: 'saved', state: saved };
            },
            restore: async (state) => restored.push(state),
            onSaved: () => {},
            retryLater: () => retries++,
        });
        sync.initialize(saved);
        sync.queue(observation(0.3));
        const saving = sync.flush();
        sync.queue(observation(0.7));
        resolve();
        await saving;
        if (outcome === 'unconfirmed') {
            assert.equal(retries, 1);
            await sync.refresh(() => true);
            assert.deepEqual(writes[1], writes[0]);
        }
        assert.deepEqual(writes.at(-1), {
            ...observation(0.7),
            revision: outcome === 'unconfirmed' ? 1 : 0,
        });
        assert.equal(saved.progress, 0.7);
        assert.equal(writes.length, outcome === 'unconfirmed' ? 3 : 2);
        assert.deepEqual(restored, []);
    }
});

test('a late refresh defers navigation and a later merge reads the current remote position', async () => {
    const { promise, resolve } = Promise.withResolvers();
    let saved = savedPosition(0.8, 2);
    let firstRead = true;
    let navigated = false;
    const writes = [];
    const restored = [];
    const sync = new ReaderPositionSync({
        read: async () => {
            if (firstRead) {
                firstRead = false;
                await promise;
            }
            return saved;
        },
        write: async (payload) => {
            writes.push(payload);
            saved = { ...saved, ...payload, revision: saved.revision + 1 };
            return { kind: 'saved', state: saved };
        },
        restore: async (state) => {
            restored.push(state);
        },
        onSaved: () => {},
        retryLater: () => {},
    });
    sync.initialize(savedPosition(0.4, 1));
    const refreshing = sync.refresh(() => !navigated);
    navigated = true;
    sync.queue(observation(0.5));
    resolve();
    await refreshing;
    assert.deepEqual(restored, []);
    assert.deepEqual(writes, []);

    saved = savedPosition(0.3, 3);
    sync.queue(observation(0.6));
    await sync.flush();
    assert.deepEqual(writes, [{ ...observation(0.6), revision: 3 }]);
    assert.deepEqual(restored, []);
});

test('only divergent unsaved positions compete by progress', async () => {
    for (const [name, local, base, remote, keep] of [
        ['an old saved copy follows a remote move backwards', null, 1, 0.2, false],
        ['unsaved local progress is farther', 0.8, 1, 0.4, true],
        ['remote progress is farther', 0.3, 1, 0.4, false],
        ['equal progress keeps the saved locator', 0.4, 1, 0.4, false],
        ['without a concurrent change reading backwards is saved', 0.2, 3, 0.8, true],
        ['reading after a failed initial load also competes by progress', 0.8, null, 0.1, true],
        ['an unread initial position can still be restored', null, null, 0.8, false],
    ]) {
        const saved = savedPosition(remote, 3);
        const writes = [];
        const restored = [];
        const sync = new ReaderPositionSync({
            read: async () => saved,
            write: async (payload) => {
                writes.push(payload);
                return { kind: 'saved', state: { ...payload, revision: 4 } };
            },
            restore: async (state) => restored.push(state),
            onSaved: () => {},
            retryLater: () => assert.fail('unexpected retry'),
        });
        sync.initialize(base === null ? null : savedPosition(0, base));
        if (local !== null) sync.queue(observation(local));
        await sync.refresh(() => true);
        assert.deepEqual(writes, keep ? [{ ...observation(local), revision: 3 }] : [], name);
        assert.deepEqual(restored, keep ? [] : [saved], name);
    }
});
