import type { Book, BookSummary, BookUpdate } from './types';

export const CATALOG_CHANGED = 'polka:catalog-changed';

// Wire names shared with internal/db/book_list_dependencies.go.
export type CatalogField =
    | keyof BookUpdate
    | 'author_sort'
    | 'cover'
    | 'assets'
    | 'reading_status'
    | 'added_at'
    | 'shelves';

// Missing fields mean an unknown effect; lists refresh conservatively.
export type CatalogChange =
    | { kind: 'books-updated'; books: BookSummary[]; fields?: CatalogField[] }
    | { kind: 'books-removed'; ids: number[] }
    | { kind: 'author-sort'; name: string; sortName: string }
    | { kind: 'shelf-membership'; shelfId: number }
    | { kind: 'reading-state' }
    | { kind: 'coarse' };

export function notifyCatalogChanged(change: CatalogChange = { kind: 'coarse' }): void {
    document.dispatchEvent(new CustomEvent<CatalogChange>(CATALOG_CHANGED, { detail: change }));
}

// Compare saved values, not submitted controls: normalization and partial saves
// must not make unrelated edits look like list changes.
export function changedBookFields(before: BookSummary, after: BookSummary): CatalogField[] {
    const fields: CatalogField[] = [];
    const previous = before as Partial<Book>;
    const saved = after as Partial<Book>;
    const compare = (field: CatalogField, a: unknown, b: unknown) => {
        if (JSON.stringify(a) !== JSON.stringify(b)) fields.push(field);
    };
    for (const key of ['title', 'series', 'series_index', 'tags', 'date'] as const) {
        compare(key, previous[key], saved[key]);
    }
    // Full Book records always include reading_status; bulk summaries omit it.
    // Missing detail metadata in a summary must not be mistaken for cleared fields.
    if ('reading_status' in before && 'reading_status' in after) {
        for (const key of [
            'sort_title',
            'language',
            'publisher',
            'identifiers',
            'added_at',
        ] as const) {
            compare(key, previous[key], saved[key]);
        }
        compare('description', previous.description_source, saved.description_source);
        compare('reading_status', previous.reading_status?.status, saved.reading_status?.status);
    }
    compare(
        'authors',
        before.authors_list.map((author) => author.name),
        after.authors_list.map((author) => author.name),
    );
    compare('author_sort', before.authors_list[0]?.sort_name, after.authors_list[0]?.sort_name);
    compare('cover', before.has_cover, after.has_cover);
    // Byte size and page counts affect display, not filename search.
    const assetKeys = (book: BookSummary) =>
        book.assets.map(({ id, extension, is_primary }) => [id, extension, is_primary]);
    compare('assets', assetKeys(before), assetKeys(after));
    return fields;
}

export function notifyBooksUpdated(before: BookSummary[], books: BookSummary[]): void {
    if (books.length === 0) return;
    const previous = new Map(before.map((book) => [book.id, book]));
    const fields = new Set<CatalogField>();
    for (const book of books) {
        const old = previous.get(book.id);
        if (!old) {
            notifyCatalogChanged({ kind: 'books-updated', books });
            return;
        }
        for (const field of changedBookFields(old, book)) fields.add(field);
    }
    notifyCatalogChanged({ kind: 'books-updated', books, fields: [...fields] });
}
