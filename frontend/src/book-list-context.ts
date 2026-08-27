// Where a book was opened from, so the detail view can offer "back to the list"
// and walk the same ordered set. The library list is the only such source: the
// Series page hands its series to the library search rather than keeping a list
// of its own.
export interface BookListContext {
    source: 'library';
    q?: string;
    sort?: string;
    shelf?: string;
    offset?: number;
}

const CONTEXT_SOURCE_PARAM = 'from';

export function libraryBookListContext(
    query: string,
    sort: string,
    shelf?: string,
    offset = 0,
): BookListContext {
    const context: BookListContext = { source: 'library' };
    const q = query.trim();
    if (q) context.q = q;
    if (sort) context.sort = sort;
    if (shelf) context.shelf = shelf;
    if (offset > 0) context.offset = offset;
    return context;
}

export function bookURL(workId: string, context?: BookListContext | null): string {
    const url = new URL(`/book/${encodeURIComponent(workId)}`, window.location.origin);
    if (context) writeContextParams(url.searchParams, context);
    return `${url.pathname}${url.search}`;
}

export function bookListContextParams(context: BookListContext): URLSearchParams {
    const params = new URLSearchParams();
    writeContextParams(params, context);
    return params;
}

export function listURLForContext(context: BookListContext | null): string {
    if (!context) return '/';
    const params = new URLSearchParams();
    writeListParams(params, context);
    const qs = params.toString();
    return qs ? `/?${qs}` : '/';
}

export function readBookListContextFromLocation(): BookListContext | null {
    return readContextParams(new URLSearchParams(window.location.search));
}

function writeContextParams(params: URLSearchParams, context: BookListContext): void {
    params.set(CONTEXT_SOURCE_PARAM, context.source);
    writeListParams(params, context);
}

function writeListParams(params: URLSearchParams, context: BookListContext): void {
    if (context.q) params.set('q', context.q);
    if (context.sort) params.set('sort', context.sort);
    if (context.shelf) params.set('shelf', context.shelf);
    if (context.offset) params.set('offset', String(context.offset));
}

function readContextParams(params: URLSearchParams): BookListContext | null {
    if (params.get(CONTEXT_SOURCE_PARAM) !== 'library') return null;
    return libraryBookListContext(
        params.get('q') || '',
        params.get('sort') || '',
        params.get('shelf') || '',
        parseLibraryOffset(params.get('offset')),
    );
}

function parseLibraryOffset(value: string | null): number {
    const offset = Number(value);
    return Number.isSafeInteger(offset) && offset > 0 ? offset : 0;
}
