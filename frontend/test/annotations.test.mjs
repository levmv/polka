import assert from 'node:assert/strict';
import test from 'node:test';
import {
    AnnotationDraft,
    annotationChanges,
    annotationMatches,
    annotationNoteConflicts,
    compareAnnotationPosition,
    sortAnnotations,
} from '../src/annotations.ts';

test('conflict resolution preserves only edited fields and uses the latest revision', () => {
    const base = { note: 'Original', color: 'yellow', revision: 1 };
    for (const [changes, current, expected] of [
        [
            { note: 'Draft' },
            { note: 'Remote', color: 'blue', revision: 2 },
            { note: 'Draft', color: 'blue' },
        ],
        [
            { color: 'green' },
            { note: 'Remote', color: 'blue', revision: 2 },
            { note: 'Remote', color: 'green' },
        ],
        [
            { note: '' },
            { note: 'Remote', color: 'yellow', revision: 2 },
            { note: '', color: 'yellow' },
        ],
    ]) {
        const draft = new AnnotationDraft(base);
        const fields = draft.rebase({ kind: 'conflict', current }, changes);
        assert.deepEqual(fields, expected);
        assert.equal(draft.base.revision, 2);
        assert.equal(draft.changes(fields.note, fields.color), null);
        assert.deepEqual(draft.changes(fields.note, fields.color, true), changes);
    }

    const draft = new AnnotationDraft(base);
    draft.rebase(
        { kind: 'conflict', current: { ...base, note: 'Remote', revision: 2 } },
        { note: 'Draft' },
    );
    // Returning to the original text now intentionally changes the remote note.
    assert.deepEqual(draft.changes('Original', 'yellow', true), { note: 'Original' });
});

test('annotation edits include only changed fields, including clearing a note', () => {
    const base = { note: 'Original note', color: 'yellow' };
    assert.deepEqual(annotationChanges(base, base.note, base.color), {});
    assert.deepEqual(annotationChanges(base, base.note, 'blue'), { color: 'blue' });
    assert.deepEqual(annotationChanges(base, '', base.color), { note: '' });
    assert.deepEqual(annotationChanges({ color: 'yellow' }, '', 'yellow'), {});
});

test('only divergent note text requires a choice when merging annotation edits', () => {
    const base = { note: 'Original', color: 'yellow' };
    for (const [name, current, changes, conflict] of [
        ['remote color and local note', { ...base, color: 'blue' }, { note: 'Draft' }, false],
        ['remote note and local color', { ...base, note: 'Remote' }, { color: 'blue' }, false],
        [
            'same note written twice',
            { ...base, note: 'Draft' },
            { note: 'Draft', color: 'blue' },
            false,
        ],
        ['different note text', { ...base, note: 'Remote' }, { note: 'Draft' }, true],
        ['clearing an edited note', { ...base, note: 'Remote' }, { note: '' }, true],
        ['editing a cleared note', { color: 'yellow' }, { note: 'Draft' }, true],
        ['same color changed twice', { ...base, color: 'blue' }, { color: 'purple' }, false],
    ]) {
        assert.equal(annotationNoteConflicts(base, current, changes), conflict, name);
    }
    assert.equal(
        annotationNoteConflicts(
            { color: 'yellow' },
            { note: '', color: 'blue' },
            { note: 'Draft' },
        ),
        false,
    );
});

const annotation = (id, cfi, extra = {}) => ({
    id,
    asset_id: 1,
    locator: { cfi },
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

test('PDF highlights sort by page and region independently of creation order', () => {
    const rows = [
        annotation(1, '', {
            locator: { page: 2, rects: [{ x: 72, y: 100, width: 90, height: 12 }] },
        }),
        annotation(2, '', {
            locator: { page: 1, rects: [{ x: 72, y: 100, width: 90, height: 12 }] },
        }),
        annotation(3, '', {
            locator: { page: 1, rects: [{ x: 72, y: 140, width: 90, height: 12 }] },
        }),
    ];
    assert.deepEqual(
        sortAnnotations(rows, 'position').map((row) => row.id),
        [3, 2, 1],
    );
});
