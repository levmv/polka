import assert from 'node:assert/strict';
import test from 'node:test';
import { changedBookFields } from '../src/catalog-events.ts';

test('saved book changes distinguish list fields from display details', () => {
    const book = {
        id: 1,
        title: 'Book',
        sort_title: 'Book',
        series: 'Series',
        series_index: 1,
        authors_list: [{ name: 'Some Author', sort_name: 'Author, Some' }],
        tags: null,
        date: null,
        has_cover: true,
        cover_version: 1,
        assets: [{ id: 1, extension: '.epub', is_primary: true, size: 100 }],
        description_source: null,
        reading_status: { status: 'unread' },
    };
    for (const [patch, expected] of [
        [{ cover_version: 2, updated_at: 100 }, []],
        [{ has_cover: false, cover_version: 0 }, ['cover']],
        [{ series_index: 2 }, ['series_index']],
        [{ authors_list: [{ name: 'Some Author', sort_name: 'Other sort' }] }, ['author_sort']],
        [{ reading_status: { status: 'finished', updated_at: 100 } }, ['reading_status']],
        [{ assets: [{ ...book.assets[0], size: 200, page_count: 10 }] }, []],
        [{ assets: [] }, ['assets']],
    ]) {
        assert.deepEqual(changedBookFields(book, { ...book, ...patch }), expected);
    }
    // A bulk summary omits detail fields; omission is not an edit to those fields.
    const { sort_title, description_source, reading_status, ...summary } = book;
    assert.deepEqual(changedBookFields(book, { ...summary, tags: 'favorite' }), ['tags']);
});
