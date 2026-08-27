// Sort-title helpers.
//
// The edit form's explicit Auto action removes a leading article and appends it
// to the end ("The Hobbit" -> "Hobbit, The"), with paired wrapping quotes kept
// out of the decision. This local table covers common articles for languages we
// already normalize elsewhere; it is intentionally not run silently at import.

const ARTICLE_PATTERNS: Record<string, RegExp[]> = {
    en: [/^(the)\s+/i, /^(a)\s+/i, /^(an)\s+/i],
    de: [
        /^(ein)\s+/i,
        /^(eine)\s+/i,
        /^(einer)\s+/i,
        /^(eines)\s+/i,
        /^(einen)\s+/i,
        /^(einem)\s+/i,
        /^(der)\s+/i,
        /^(die)\s+/i,
        /^(das)\s+/i,
        /^(den)\s+/i,
        /^(dem)\s+/i,
    ],
    es: [
        /^(el)\s+/i,
        /^(la)\s+/i,
        /^(los)\s+/i,
        /^(las)\s+/i,
        /^(un)\s+/i,
        /^(una)\s+/i,
        /^(unos)\s+/i,
        /^(unas)\s+/i,
    ],
    fr: [
        /^(l['’])\s*/i,
        /^(le)\s+/i,
        /^(la)\s+/i,
        /^(les)\s+/i,
        /^(un)\s+/i,
        /^(une)\s+/i,
        /^(des)\s+/i,
        /^(du)\s+/i,
        /^(de\s+la)\s+/i,
        /^(de\s+l['’])\s*/i,
        /^(d['’])\s*/i,
    ],
    it: [
        /^(il)\s+/i,
        /^(lo)\s+/i,
        /^(la)\s+/i,
        /^(gli)\s+/i,
        /^(i)\s+/i,
        /^(le)\s+/i,
        /^(l['’])\s*/i,
        /^(un)\s+/i,
        /^(uno)\s+/i,
        /^(una)\s+/i,
        /^(un['’])\s*/i,
        /^(del)\s+/i,
        /^(della)\s+/i,
        /^(dello)\s+/i,
        /^(dell['’])\s*/i,
        /^(dei)\s+/i,
        /^(degli)\s+/i,
        /^(delle)\s+/i,
    ],
    nl: [/^(de)\s+/i, /^(het)\s+/i, /^(een)\s+/i, /^('n)\s+/i, /^('t)\s+/i],
    pt: [
        /^(a)\s+/i,
        /^(o)\s+/i,
        /^(as)\s+/i,
        /^(os)\s+/i,
        /^(um)\s+/i,
        /^(uma)\s+/i,
        /^(umas)\s+/i,
        /^(uns)\s+/i,
    ],
    sv: [/^(en)\s+/i, /^(ett)\s+/i, /^(det)\s+/i, /^(den)\s+/i, /^(de)\s+/i],
};

const LANGUAGE_ALIASES: Record<string, string> = {
    eng: 'en',
    deu: 'de',
    spa: 'es',
    fra: 'fr',
    ita: 'it',
    nld: 'nl',
    por: 'pt',
    swe: 'sv',
};

const QUOTE_PAIRS = new Map([
    ['"', '"'],
    ["'", "'"],
    ['`', '`'],
    ['«', '»'],
    ['“', '”'],
    ['‘', '’'],
    ['「', '」'],
    ['『', '』'],
    ['《', '》'],
    ['〈', '〉'],
]);

export function titleSort(title: string, language?: string | null): string {
    const stripped = stripWrappingQuotes(title.trim());
    if (!stripped) return '';

    for (const pattern of articlePatternsForLanguage(language)) {
        const match = stripped.match(pattern);
        if (!match?.[1]) continue;
        const article = match[1];
        const rest = stripped.slice(match[0].length).trim();
        if (rest) return stripWrappingQuotes(`${rest}, ${article}`).trim();
    }

    return stripped;
}

function articlePatternsForLanguage(language?: string | null): RegExp[] {
    const lang = (language || '').trim().toLowerCase();
    if (!lang) return ARTICLE_PATTERNS.en;
    const base = lang.split(/[-_]/, 1)[0];
    const key = LANGUAGE_ALIASES[base] || base;
    return ARTICLE_PATTERNS[key] || ARTICLE_PATTERNS.en;
}

function stripWrappingQuotes(value: string): string {
    let result = value.trim();
    let changed = true;
    while (changed && result.length >= 2) {
        changed = false;
        for (const [open, close] of QUOTE_PAIRS) {
            if (result.startsWith(open) && result.endsWith(close)) {
                result = result.slice(open.length, result.length - close.length).trim();
                changed = true;
                break;
            }
        }
    }
    return result;
}
