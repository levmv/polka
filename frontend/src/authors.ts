type AuthorLike = {
    name: string;
};

export function isAuthorListDelimiter(value: string, index: number): boolean {
    const ch = value[index];
    if (ch === ';') return true;
    if (ch !== '&') return false;
    if (value[index - 1] === '&' || value[index + 1] === '&') return false;
    const prevSpace = index === 0 || /\s/.test(value[index - 1]);
    const nextSpace = index === value.length - 1 || /\s/.test(value[index + 1]);
    return prevSpace && nextSpace;
}

export function parseAuthorList(value: string): string[] {
    const authors: string[] = [];
    let token = '';

    const flush = () => {
        const name = token.trim();
        token = '';
        if (name) authors.push(name);
    };

    for (let i = 0; i < value.length; i++) {
        const ch = value[i];
        if (ch === '&' && value[i + 1] === '&') {
            token += '&';
            i++;
        } else if (isAuthorListDelimiter(value, i)) {
            flush();
        } else {
            token += ch;
        }
    }
    flush();

    return authors;
}

export function formatAuthorList(names: string[]): string {
    return names
        .map((name) => name.trim())
        .filter(Boolean)
        .map((name) => name.split('&').join('&&'))
        .join('; ');
}

export function formatAuthorsForEdit(authors: AuthorLike[]): string {
    return formatAuthorList(authors.map((author) => author.name));
}

// Keep these author-sort heuristic sets synchronized with internal/bookmeta/metadata.go.
// They are duplicated deliberately: Go uses them during import/maintenance, while
// the frontend uses them for the edit form's explicit Auto action.
const AUTHOR_COPY_WORDS = new Set([
    'agency',
    'association',
    'bureau',
    'center',
    'centre',
    'co',
    'co.',
    'collective',
    'college',
    'committee',
    'company',
    'corp',
    'corp.',
    'corporation',
    'council',
    'department',
    'foundation',
    'group',
    'guild',
    'inc',
    'inc.',
    'institute',
    'laboratory',
    'labs',
    'llc',
    'ltd',
    'ltd.',
    'media',
    'ministry',
    'office',
    'organization',
    'organisation',
    'press',
    'project',
    'publisher',
    'publishers',
    'publishing',
    'society',
    'studio',
    'studios',
    'team',
    'university',
]);

const AUTHOR_PREFIXES = new Set([
    'mr',
    'mr.',
    'mrs',
    'mrs.',
    'ms',
    'ms.',
    'miss',
    'miss.',
    'dr',
    'dr.',
    'prof',
    'prof.',
    'sir',
    'dame',
]);

const AUTHOR_SUFFIXES = new Set([
    'jr',
    'jr.',
    'sr',
    'sr.',
    'esq',
    'esq.',
    'ph.d',
    'ph.d.',
    'phd',
    'phd.',
    'md',
    'md.',
    'm.d',
    'm.d.',
    'i',
    'i.',
    'ii',
    'ii.',
    'iii',
    'iii.',
    'iv',
    'iv.',
    'v',
    'v.',
    'junior',
    'junior.',
    'senior',
    'senior.',
]);

// authorSort mirrors bookmeta.AuthorSort (internal/bookmeta/metadata.go): common
// library "Last, First" behavior, with obvious organization names copied and
// common honorifics/suffixes handled. It is still an explicit "Auto" action in
// the UI.
export function authorSort(name: string): string {
    const author = name.trim();
    if (!author) return '';

    const sortSource = removeBracketedAuthorText(author).trim();
    if (sortSource.includes(',')) return author;

    const tokens = sortSource.split(/\s+/).filter(Boolean);
    if (tokens.length <= 1) return author;
    if (tokens.some((token) => AUTHOR_COPY_WORDS.has(token.toLowerCase()))) return author;

    let first = 0;
    while (first < tokens.length && AUTHOR_PREFIXES.has(tokens[first].toLowerCase())) first++;
    if (first >= tokens.length) return author;

    let last = tokens.length - 1;
    while (last >= first && AUTHOR_SUFFIXES.has(tokens[last].toLowerCase())) last--;
    if (last < first) return author;

    const suffix = tokens.slice(last + 1).join(' ');
    const sortTokens = [tokens[last], ...tokens.slice(first, last)];
    if (sortTokens.length > 1) sortTokens[0] += ',';
    if (suffix) sortTokens.push(suffix);
    return sortTokens.join(' ');
}

function removeBracketedAuthorText(value: string): string {
    const pairs: Record<string, string> = { '(': ')', '[': ']', '{': '}' };
    const reverse: Record<string, string> = { ')': '(', ']': '[', '}': '{' };
    const counts: Record<string, number> = {};
    let depth = 0;
    let result = '';
    for (const ch of value) {
        if (pairs[ch]) {
            counts[ch] = (counts[ch] || 0) + 1;
            depth++;
            continue;
        }
        const opener = reverse[ch];
        if (opener && counts[opener] > 0) {
            counts[opener]--;
            depth--;
            continue;
        }
        if (depth === 0) result += ch;
    }
    return result;
}
