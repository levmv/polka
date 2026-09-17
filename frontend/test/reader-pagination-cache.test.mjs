import assert from 'node:assert/strict';
import test from 'node:test';
import { createPaginationCache } from '../src/reader/pagination-cache.ts';

test('pagination cache reuses matching layouts, bounds storage and tolerates storage failures', () => {
    let raw = null;
    const previous = Object.getOwnPropertyDescriptor(globalThis, 'localStorage');
    Object.defineProperty(globalThis, 'localStorage', {
        configurable: true,
        value: {
            getItem: () => raw,
            setItem: (_, value) => {
                raw = value;
            },
        },
    });
    try {
        const cache = createPaginationCache('reader-1', 'browser-1');
        const layout = (book, reader = [600, 800]) => ({
            book,
            reader,
            viewport: reader,
            style: 'custom',
            fontScale: 0,
            columnWidth: 760,
            lineHeight: 1.72,
        });
        const portrait = layout('book');
        const landscape = layout('book', [800, 600]);
        const counts = new Map([
            [0, 3],
            [2, 7],
        ]);
        cache.write(portrait, 3, counts);
        cache.write(landscape, 3, new Map([[0, 2]]));
        assert.deepEqual(cache.read(portrait, 3), counts);
        assert.deepEqual(cache.read(landscape, 3), new Map([[0, 2]]));
        assert.equal(cache.read(layout('changed-book'), 3).size, 0);
        assert.equal(cache.read({ ...portrait, fontScale: 2 }, 3).size, 0);
        assert.equal(cache.read(portrait, 4).size, 0);
        assert.equal(createPaginationCache('reader-2', 'browser-1').read(portrait, 3).size, 0);
        assert.equal(createPaginationCache('reader-1', 'browser-2').read(portrait, 3).size, 0);
        counts.set(1, 4);
        cache.write(portrait, 3, counts);
        assert.deepEqual(cache.read(portrait, 3), counts);
        cache.write(portrait, 3, new Map());
        assert.equal(cache.read(portrait, 3).size, 0);
        assert.deepEqual(cache.read(landscape, 3), new Map([[0, 2]]));

        const single = new Map([[0, 1]]);
        raw = null;
        for (let i = 0; i < 256; i++) cache.write(layout(`book-${i}`), 1, single);
        const reopened = createPaginationCache('reader-1', 'browser-1');
        assert.deepEqual(reopened.read(layout('book-0'), 1), single);
        // The oldest entry was just used, so the next unused book is evicted.
        cache.write(layout('book-256'), 1, single);
        assert.deepEqual(cache.read(layout('book-0'), 1), single);
        assert.equal(cache.read(layout('book-1'), 1).size, 0);
        assert.deepEqual(cache.read(layout('book-256'), 1), single);

        raw = null;
        for (let i = 0; i < 8; i++) cache.write(layout(`book-${i}`), 4000, single);
        assert.ok(raw.length <= 128 * 1024);
        assert.equal(cache.read(layout('book-0'), 4000).size, 0);
        assert.deepEqual(cache.read(layout('book-7'), 4000), single);

        cache.write(portrait, 3, counts);
        cache.write(landscape, 3, new Map([[0, 2]]));
        localStorage.setItem = () => {
            throw new Error('storage full');
        };
        assert.deepEqual(cache.read(portrait, 3), counts);
        assert.doesNotThrow(() => cache.write(portrait, 3, counts));

        raw = '{';
        assert.equal(cache.read(portrait, 3).size, 0);
        Object.defineProperty(globalThis, 'localStorage', {
            configurable: true,
            get() {
                throw new Error('storage unavailable');
            },
        });
        assert.equal(cache.read(portrait, 3).size, 0);
        assert.doesNotThrow(() => cache.write(portrait, 3, counts));
    } finally {
        if (previous) Object.defineProperty(globalThis, 'localStorage', previous);
        else delete globalThis.localStorage;
    }
});
