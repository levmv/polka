import type { BookSummary } from './types';

export const CATALOG_CHANGED = 'polka:catalog-changed';

// What a successful mutation tells the rest of the app.
//
// A list can patch a book it already renders, and it can drop books that are
// gone. Anything coarser it cannot place safely — where would a newly imported
// book go in a sequence the reader is part-way through? — so it rebuilds from
// the server instead, on its own terms. Keeping the payload to exactly these
// three shapes is what stops this channel from growing into a cache-consistency
// system: there is no fourth case to reach for.
export type CatalogChange =
    | { kind: 'books-updated'; books: BookSummary[] }
    | { kind: 'books-removed'; ids: string[] }
    | { kind: 'coarse' };

export function notifyCatalogChanged(change: CatalogChange = { kind: 'coarse' }): void {
    document.dispatchEvent(new CustomEvent<CatalogChange>(CATALOG_CHANGED, { detail: change }));
}
