import assert from 'node:assert/strict';
import test from 'node:test';

import { retentionForPop, retentionForPush } from '../src/history-state.ts';

test('opening a book from the library retains it, and only then', () => {
    const push = (fromPathname, toPathname, retainedKey = null) =>
        retentionForPush({ fromPathname, toPathname, fromID: 'lib', retainedKey });

    assert.deepEqual(push('/', '/book/1').retention, { mode: 'retain', key: 'lib' });
    assert.equal(push('/', '/book/1').retainedLibraryID, 'lib');
    // Book to book keeps whichever library is already held, and nothing more.
    assert.deepEqual(push('/book/1', '/book/2', 'lib').retention, { mode: 'keep', key: 'lib' });
    assert.deepEqual(push('/book/1', '/book/2').retention, { mode: 'release' });
    // Leaving the relationship, in either direction.
    assert.deepEqual(push('/book/1', '/series', 'lib').retention, { mode: 'release' });
    assert.deepEqual(push('/series', '/book/1').retention, { mode: 'release' });
    assert.deepEqual(push('/', '/series').retention, { mode: 'release' });
});

test('Back and Forward match the retained instance by entry identity', () => {
    const scroll = { x: 0, y: 900 };
    const pop = (args) =>
        retentionForPop({
            targetPathname: '/',
            targetID: 'lib',
            targetRetainedLibraryID: null,
            fromPathname: '/book/1',
            fromID: 'book1',
            retainedKey: 'lib',
            scroll,
            ...args,
        });

    // Back to the exact entry the instance was detached from.
    assert.deepEqual(pop({}), { mode: 'resume', key: 'lib', scroll });
    // A second library entry with the same URL is a different entry.
    assert.deepEqual(pop({ targetID: 'lib2' }), { mode: 'release' });
    // Back from book 2 to book 1 stays inside the relationship.
    assert.deepEqual(
        pop({ targetPathname: '/book/1', targetID: 'book1', targetRetainedLibraryID: 'lib' }),
        { mode: 'keep', key: 'lib' },
    );
    // Forward out of the resumed library into its book retains it again.
    assert.deepEqual(
        pop({
            targetPathname: '/book/1',
            targetID: 'book1',
            targetRetainedLibraryID: 'lib',
            fromPathname: '/',
            fromID: 'lib',
            retainedKey: null,
        }),
        { mode: 'retain', key: 'lib' },
    );
    // A book entry pointing at an instance that is no longer held.
    assert.deepEqual(
        pop({
            targetPathname: '/book/1',
            targetID: 'book1',
            targetRetainedLibraryID: 'gone',
            retainedKey: null,
        }),
        { mode: 'release' },
    );
});
