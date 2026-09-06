import { createReaderPanel, type ReaderPanelElements } from './panel';

export const READER_SEARCH_DEBOUNCE_MS = 260;
export const READER_SEARCH_TOO_SHORT_MESSAGE = 'Type 2 characters, or one Han ideograph.';

export interface ReaderSearchControls extends ReaderPanelElements {
    input: HTMLInputElement;
    status: HTMLElement;
    results: HTMLOListElement;
}

interface ReaderSearchExcerpt {
    pre?: string;
    match?: string;
    post?: string;
}

export function createSearchPanel(page: HTMLElement): ReaderSearchControls {
    const elements = createReaderPanel(page, {
        name: 'search',
        title: 'Search',
        label: 'Search in book',
        closeLabel: 'Close search',
        icon: 'search',
    });

    const form = document.createElement('div');
    form.className = 'reader-search-form';
    const input = document.createElement('input');
    input.className = 'reader-search-input';
    input.type = 'search';
    input.placeholder = 'Search this book';
    input.autocomplete = 'off';
    input.spellcheck = false;
    input.setAttribute('aria-label', 'Search this book');
    form.append(input);

    const status = document.createElement('div');
    status.className = 'reader-search-status';
    status.setAttribute('aria-live', 'polite');
    status.textContent = 'Type to search this book.';

    const results = document.createElement('ol');
    results.className = 'reader-search-results';
    results.setAttribute('aria-label', 'Search results');

    elements.panel.append(form, status, results);

    return { ...elements, input, status, results };
}

export function openSearch(
    page: HTMLElement,
    controls: ReaderSearchControls,
    selectText: boolean,
): void {
    page.classList.add('reader-search-open');
    controls.panel.hidden = false;
    controls.backdrop.hidden = false;
    controls.toggle.setAttribute('aria-expanded', 'true');
    controls.input.focus({ preventScroll: true });
    if (selectText) controls.input.select();
}

export function appendExcerpt(parent: HTMLElement, excerpt: ReaderSearchExcerpt): void {
    const pre = excerpt.pre || '';
    const match = excerpt.match || '';
    const post = excerpt.post || '';
    if (pre) parent.append(document.createTextNode(pre));

    const mark = document.createElement('mark');
    mark.textContent = match;
    parent.append(mark);

    if (post) parent.append(document.createTextNode(post));
}

export function formatResultCount(count: number): string {
    return `${count} ${count === 1 ? 'result' : 'results'}`;
}

export function normalizeReaderSearchQuery(text: string): string {
    const normalized = text.replace(/\s+/g, ' ').trim();
    return Array.from(normalized).slice(0, 120).join('');
}

export function isReaderSearchQueryReady(query: string): boolean {
    const characters = Array.from(query);
    return characters.length >= 2 || (characters.length === 1 && isHanIdeograph(characters[0]));
}

function isHanIdeograph(character: string): boolean {
    const codePoint = character.codePointAt(0);
    if (codePoint === undefined) return false;
    return (
        codePoint === 0x3006 ||
        codePoint === 0x3007 ||
        (codePoint >= 0x3400 && codePoint <= 0x4dbf) ||
        (codePoint >= 0x4e00 && codePoint <= 0x9fff) ||
        (codePoint >= 0xf900 && codePoint <= 0xfaff) ||
        (codePoint >= 0x20000 && codePoint <= 0x323af)
    );
}
