import assert from 'node:assert/strict';
import test from 'node:test';
import {
    annotationMatches,
    compareAnnotationPosition,
    sortAnnotations,
} from '../src/annotations.ts';

const annotation = (id, cfi, extra = {}) => ({
    id,
    asset_id: 1,
    cfi,
    quote: 'A quiet library',
    note: 'Вернуться к этой мысли',
    created_at: id * 10,
    updated_at: id * 10,
    ...extra,
});

test('book order compares numeric CFI paths and range starts, ignoring assertions', () => {
    for (const [before, after] of [
        ['epubcfi(/6/2!/4/2/1:0)', 'epubcfi(/6/10!/4/2/1:0)'],
        ['epubcfi(/6/2!/4/2/1:9)', 'epubcfi(/6/2!/4/2/1:10)'],
        ['epubcfi(/6/2[z99]!/4/2/1:9)', 'epubcfi(/6/2[a1]!/4/2/1:10)'],
        ['epubcfi(/6/2!/4/2,/1:9,/1:99)', 'epubcfi(/6/2!/4/2,/1:10,/1:15)'],
        ['epubcfi(/6/2[id^]1^,2]!/4/2,/1:0[hello,world],/1:10)', 'epubcfi(/6/2!/4/2/1:1)'],
        ['epubcfi(/6/2!/4/2/1:0)', 'unknown location'],
    ]) {
        const a = annotation(2, before);
        const b = annotation(1, after);
        assert.ok(compareAnnotationPosition(a, b) < 0, `${before} before ${after}`);
        assert.ok(compareAnnotationPosition(b, a) > 0);
    }
});

test('sorts by creation or editing date and searches both quotes and notes', () => {
    const rows = [
        annotation(1, 'epubcfi(/6/2)', { updated_at: 50 }),
        annotation(2, 'epubcfi(/6/4)', { quote: 'Second passage', note: '' }),
    ];
    assert.deepEqual(
        sortAnnotations(rows, 'created').map((row) => row.id),
        [2, 1],
    );
    assert.deepEqual(
        sortAnnotations(rows, 'updated').map((row) => row.id),
        [1, 2],
    );
    assert.ok(annotationMatches(rows[0], '  LIBRARY  МЫСЛИ '));
    assert.ok(!annotationMatches(rows[1], 'мысли'));
});
