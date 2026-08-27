export interface Identifier {
    type: string;
    value: string;
}

export function parseIdentifiers(s: string): Identifier[] {
    if (!s) return [];
    const parts = s.split(',');
    const ids: Identifier[] = [];
    for (let p of parts) {
        p = p.trim();
        if (!p) continue;
        ids.push(parseIdentifierToken(p));
    }
    return ids;
}

function parseIdentifierToken(value: string): Identifier {
    const doiURL = doiFromURL(value);
    if (doiURL) return { type: 'doi', value: doiURL };

    const lower = value.toLowerCase();
    if (lower.startsWith('http://') || lower.startsWith('https://')) {
        return { type: 'url', value };
    }

    const labelled = identifierFromLabelledToken(value);
    if (labelled) return labelled;

    const idx = value.indexOf(':');
    if (idx === -1) {
        return { type: 'isbn', value };
    }

    let typ = value.substring(0, idx).trim().toLowerCase();
    let val = value.substring(idx + 1).trim();
    if (typ === 'urn') {
        const nextIdx = val.indexOf(':');
        if (nextIdx !== -1) {
            typ = val.substring(0, nextIdx).trim().toLowerCase();
            val = val.substring(nextIdx + 1).trim();
        }
    }
    return { type: typ, value: val };
}

function identifierFromLabelledToken(value: string): Identifier | null {
    const parts = value.trim().split(/\s+/);
    if (parts.length < 2) return null;
    const label = parts[0].replace(/[: ]+$/g, '').toLowerCase();
    const rest = value.slice(parts[0].length).trim();
    if (label === 'isbn' || label === 'isbn-10' || label === 'isbn-13') {
        return { type: 'isbn', value: rest };
    }
    if (label === 'doi') {
        return { type: 'doi', value: rest };
    }
    return null;
}

function doiFromURL(value: string): string {
    const lower = value.toLowerCase();
    for (const prefix of [
        'https://doi.org/',
        'http://doi.org/',
        'https://dx.doi.org/',
        'http://dx.doi.org/',
    ]) {
        if (!lower.startsWith(prefix)) continue;
        const raw = value.slice(prefix.length).trim();
        const cut = raw.search(/[?#]/);
        return cut >= 0 ? raw.slice(0, cut) : raw;
    }
    return '';
}

export function formatIdentifiers(ids: Identifier[]): string {
    return ids.map((id) => `${id.type}:${id.value}`).join(', ');
}

// Real bibliographic identifiers worth showing inline in the reading flow.
// Everything else (store/retailer/URL ids: amazon, google, goodreads,
// barnesnoble, litres, epubbud, unknown, …) is kept but tucked behind a
// disclosure so the book page isn't a dump of store-link noise.
const PRIMARY_SCHEMES = new Set(['isbn', 'doi']);

export function isPrimaryIdentifier(id: Identifier): boolean {
    return PRIMARY_SCHEMES.has(id.type.toLowerCase());
}

// identifierLabel is the human display name for an identifier scheme.
export function identifierLabel(type: string): string {
    switch (type.toLowerCase()) {
        case 'isbn':
            return 'ISBN';
        case 'doi':
            return 'DOI';
        case 'amazon':
        case 'asin':
            return 'Amazon';
        case 'google':
            return 'Google Books';
        case 'goodreads':
            return 'Goodreads';
        case 'barnesnoble':
            return 'Barnes & Noble';
        default:
            return type.toUpperCase();
    }
}

// identifierLink returns an out-link for an identifier, or '' if none applies.
// URL-valued ids (e.g. epubbud:http://…) link to themselves.
export function identifierLink(id: Identifier): string {
    const v = id.value.trim();
    if (/^https?:\/\//i.test(v)) return v;
    switch (id.type.toLowerCase()) {
        case 'doi':
            return `https://doi.org/${v}`;
        case 'amazon':
        case 'asin':
            return `https://www.amazon.com/dp/${v}`;
        case 'google':
            return `https://books.google.com/books?id=${v}`;
        case 'goodreads':
            return `https://www.goodreads.com/book/show/${v}`;
        default:
            return '';
    }
}

export function validISBN(value: string): boolean {
    const clean = value.replace(/[-\s]/g, '');
    if (clean.length === 10) {
        let sum = 0;
        for (let i = 0; i < 9; i++) {
            const d = parseInt(clean[i], 10);
            if (Number.isNaN(d)) return false;
            sum += d * (10 - i);
        }
        const last = clean[9].toLowerCase();
        let check = 0;
        if (last === 'x') {
            check = 10;
        } else {
            check = parseInt(last, 10);
            if (Number.isNaN(check)) return false;
        }
        sum += check;
        return sum % 11 === 0;
    } else if (clean.length === 13) {
        let sum = 0;
        for (let i = 0; i < 13; i++) {
            const d = parseInt(clean[i], 10);
            if (Number.isNaN(d)) return false;
            const weight = i % 2 !== 0 ? 3 : 1;
            sum += d * weight;
        }
        return sum % 10 === 0;
    }
    return false;
}
