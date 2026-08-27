// Builders for the search mini-language (author:/series:/tag:/title:). Kept in
// one place so every filter link quotes values the same way the backend parser
// expects.

// queryTerm builds a qualified term (e.g. series:"Name"), doubling embedded
// quotes so the backend parser reconstructs the literal value. Mirror of Go's
// db.QueryTerm; without it a name containing a quote yields a broken query and
// an empty result.
export function queryTerm(field: string, value: string): string {
    return `${field}:"${value.replace(/"/g, '""')}"`;
}

// A series has no page of its own: every series link opens the library filtered
// to that series and sorted by volume, which is the whole series in order.
export function seriesLibraryURL(name: string): string {
    const params = new URLSearchParams({ q: queryTerm('series', name), sort: 'series' });
    return `/?${params.toString()}`;
}
