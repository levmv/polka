import assert from 'node:assert/strict';
import test from 'node:test';

import { queryTerm } from '../src/search-query.ts';

test('queryTerm preserves qualified values containing quotes', () => {
    const cases = [
        ['series', 'Foundation', 'series:"Foundation"'],
        ['series', 'The "Best" Books', 'series:"The ""Best"" Books"'],
    ];

    for (const [field, value, want] of cases) {
        assert.equal(queryTerm(field, value), want);
    }
});
