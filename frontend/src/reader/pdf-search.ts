import type { PDFDocumentProxy, PDFPageProxy, TextLayer } from 'pdfjs-dist/legacy/build/pdf.mjs';

import { focusReaderSurface, shouldIgnoreReaderShortcut } from './controls';
import {
    appendExcerpt,
    createSearchPanel,
    formatResultCount,
    isReaderSearchQueryReady,
    normalizeReaderSearchQuery,
    openSearch,
    READER_SEARCH_DEBOUNCE_MS,
    READER_SEARCH_TOO_SHORT_MESSAGE,
    type ReaderSearchControls,
} from './search-panel';

const MAX_PAGE_TEXT_CHARS = 2_000_000;
const MAX_MATCHES_PER_PAGE = 1_000;
const MAX_RENDERED_RESULTS = 200;
const EXCERPT_CONTEXT_CHARS = 72;

interface PDFTextItem {
    str: string;
    dir: string;
    transform: number[];
    width: number;
    height: number;
    hasEOL: boolean;
}

interface PDFTextContentChunk {
    items: Array<PDFTextItem | { type: string }>;
}

interface PDFSearchOptions {
    currentPage: () => number;
    navigateTo: (pageNumber: number) => Promise<void>;
}

interface PDFSearchState {
    sequence: number;
    debounce: number;
    searching: boolean;
    resultCount: number;
    renderedCount: number;
    resultsLimited: boolean;
    activeResultID: string;
    activeReader: ReadableStreamDefaultReader<PDFTextContentChunk> | null;
    query: string;
    highlight: PDFSearchHighlight | null;
    renderedTextLayer: PDFRenderedTextLayer | null;
}

interface PDFSearchMatch {
    pre: string;
    match: string;
    post: string;
}

interface PDFPageText {
    text: string;
    truncated: boolean;
}

interface PDFSearchHighlight {
    resultID: string;
    query: string;
    pageNumber: number;
    occurrence: number;
}

interface PDFDOMTextMap {
    text: string;
    runs: PDFDOMTextRun[];
}

interface PDFDOMTextItem {
    node: Text;
    text: string;
    rect: DOMRect;
    direction: string;
}

interface PDFDOMTextRun {
    start: number;
    end: number;
    node: Text;
    nodeOffset: number;
}

interface PDFRenderedTextLayer {
    pageNumber: number;
    textDivs: readonly HTMLElement[];
    textContentItemsStr: readonly string[];
}

interface PDFTextMatch {
    index: number;
    length: number;
}

export interface PDFSearchController {
    markCurrentPage(pageNumber: number, textLayer: TextLayer): void;
    clearPage(): void;
}

export function wirePDFSearch(
    page: HTMLElement,
    pdfDocument: PDFDocumentProxy,
    options: PDFSearchOptions,
): PDFSearchController {
    const actions = page.querySelector<HTMLElement>('.reader-actions');
    if (!actions) {
        return {
            markCurrentPage: () => undefined,
            clearPage: () => undefined,
        };
    }

    const controls = createSearchPanel(page);
    actions.prepend(controls.toggle);
    const state: PDFSearchState = {
        sequence: 0,
        debounce: 0,
        searching: false,
        resultCount: 0,
        renderedCount: 0,
        resultsLimited: false,
        activeResultID: '',
        activeReader: null,
        query: '',
        highlight: null,
        renderedTextLayer: null,
    };

    const close = (restoreFocus: boolean): void => {
        page.classList.remove('reader-search-open');
        controls.panel.hidden = true;
        controls.backdrop.hidden = true;
        controls.toggle.setAttribute('aria-expanded', 'false');
        clearPDFSearch(page, controls, state);
        if (restoreFocus) focusReaderSurface(page);
    };
    const run = (query: string): void => {
        void runPDFSearch(page, pdfDocument, controls, state, query, options).catch((error) => {
            if (!state.searching) return;
            console.error('Failed to search PDF:', error);
            state.searching = false;
            controls.status.textContent = 'Search failed.';
        });
    };

    controls.toggle.addEventListener('click', () => {
        if (controls.panel.hidden) openSearch(page, controls, true);
        else close(true);
    });
    controls.backdrop.addEventListener('click', () => close(true));
    controls.panel
        .querySelector<HTMLButtonElement>('[data-reader-search-close]')
        ?.addEventListener('click', () => close(true));
    document.addEventListener('keydown', (event) => {
        if (event.key === '/' && controls.panel.hidden && !shouldIgnoreReaderShortcut(event)) {
            event.preventDefault();
            openSearch(page, controls, true);
            return;
        }
        if (event.key !== 'Escape' || controls.panel.hidden) return;
        event.preventDefault();
        close(true);
    });
    controls.input.addEventListener('input', () => {
        window.clearTimeout(state.debounce);
        state.debounce = 0;
        const query = controls.input.value;
        if (!query.trim()) {
            clearPDFSearch(page, controls, state);
            return;
        }
        if (!isReaderSearchQueryReady(normalizeReaderSearchQuery(query))) {
            run(query);
            return;
        }
        state.debounce = window.setTimeout(() => {
            state.debounce = 0;
            run(query);
        }, READER_SEARCH_DEBOUNCE_MS);
    });
    controls.input.addEventListener('keydown', (event) => {
        if (event.key !== 'Enter') return;
        event.preventDefault();
        window.clearTimeout(state.debounce);
        state.debounce = 0;
        run(controls.input.value);
    });

    return {
        markCurrentPage: (pageNumber: number, textLayer: TextLayer): void => {
            state.renderedTextLayer = {
                pageNumber,
                textDivs: textLayer.textDivs,
                textContentItemsStr: textLayer.textContentItemsStr,
            };
            markCurrentPDFSearchResult(page, controls, state, pageNumber);
        },
        clearPage: (): void => {
            state.renderedTextLayer = null;
            clearPDFSearchHighlight(page);
        },
    };
}

async function runPDFSearch(
    readerPage: HTMLElement,
    pdfDocument: PDFDocumentProxy,
    controls: ReaderSearchControls,
    state: PDFSearchState,
    rawQuery: string,
    options: PDFSearchOptions,
): Promise<void> {
    const query = normalizeReaderSearchQuery(rawQuery);
    const sequence = ++state.sequence;
    cancelActiveSearch(state);
    state.searching = false;
    state.resultCount = 0;
    state.renderedCount = 0;
    state.resultsLimited = false;
    state.activeResultID = '';
    state.query = query;
    state.highlight = null;
    clearPDFSearchHighlight(readerPage);
    controls.results.replaceChildren();
    controls.panel.dataset.readerSearchResults = '0';

    if (!query) {
        controls.status.textContent = 'Type to search this book.';
        return;
    }
    if (!isReaderSearchQueryReady(query)) {
        controls.status.textContent = READER_SEARCH_TOO_SHORT_MESSAGE;
        return;
    }

    state.searching = true;
    controls.status.textContent = 'Searching...';
    try {
        for (let pageNumber = 1; pageNumber <= pdfDocument.numPages; pageNumber++) {
            if (sequence !== state.sequence) return;
            const pdfPage = await pdfDocument.getPage(pageNumber);
            try {
                if (sequence !== state.sequence) return;
                const pageText = await extractPDFPageText(pdfPage, state, sequence);
                if (pageText === null || sequence !== state.sequence) return;
                const matches = findPDFMatches(pageText.text, query);
                state.resultCount += matches.length;
                if (pageText.truncated || matches.length >= MAX_MATCHES_PER_PAGE) {
                    state.resultsLimited = true;
                }
                appendPDFSearchPage(readerPage, controls, state, pageNumber, matches, options);
                updatePDFSearchStatus(controls, state, pageNumber / pdfDocument.numPages);
            } finally {
                pdfPage.cleanup();
            }
        }
    } catch (error) {
        if (sequence !== state.sequence) return;
        throw error;
    }

    if (sequence !== state.sequence) return;
    state.searching = false;
    updatePDFSearchStatus(controls, state);
    markCurrentPDFSearchResult(readerPage, controls, state, options.currentPage());
}

async function extractPDFPageText(
    page: PDFPageProxy,
    state: PDFSearchState,
    sequence: number,
): Promise<PDFPageText | null> {
    const reader = page.streamTextContent({ includeMarkedContent: false }).getReader();
    state.activeReader = reader;
    const parts: string[] = [];
    let length = 0;
    let previous: PDFTextItem | null = null;
    let truncated = false;

    try {
        while (length < MAX_PAGE_TEXT_CHARS) {
            const { value, done } = await reader.read();
            if (sequence !== state.sequence) {
                cancelPDFTextReader(reader, 'PDF search cancelled.');
                return null;
            }
            if (done) break;
            for (const item of value.items) {
                if (!('str' in item) || !item.str) continue;
                const separator = pdfTextSeparator(previous, item);
                const remaining = MAX_PAGE_TEXT_CHARS - length;
                const addition = `${separator}${item.str}`.slice(0, remaining);
                parts.push(addition);
                length += addition.length;
                previous = item;
                if (length >= MAX_PAGE_TEXT_CHARS) {
                    truncated = true;
                    cancelPDFTextReader(reader, 'PDF page text limit reached.');
                    break;
                }
            }
        }
    } finally {
        if (state.activeReader === reader) state.activeReader = null;
        reader.releaseLock();
    }

    return { text: parts.join('').replace(/\s+/g, ' ').trim(), truncated };
}

function pdfTextSeparator(previous: PDFTextItem | null, current: PDFTextItem): string {
    if (!previous) return '';
    if (previous.hasEOL) return '\n';
    if (/\s$/.test(previous.str) || /^\s/.test(current.str)) return '';

    const previousY = previous.transform[5];
    const currentY = current.transform[5];
    const height = Math.max(previous.height, current.height, 1);
    if (Math.abs(previousY - currentY) > height * 0.5) return '\n';

    if (previous.dir === 'rtl' || current.dir === 'rtl') {
        const gap = previous.transform[4] - (current.transform[4] + current.width);
        return gap > height * 0.12 ? ' ' : '';
    }
    const gap = current.transform[4] - (previous.transform[4] + previous.width);
    return gap > height * 0.12 ? ' ' : '';
}

function findPDFMatches(text: string, query: string): PDFSearchMatch[] {
    const matcher = caseInsensitiveLiteralMatcher(query);
    const matches: PDFSearchMatch[] = [];
    while (matches.length < MAX_MATCHES_PER_PAGE) {
        const result = matcher.exec(text);
        if (!result) break;
        const index = result.index;
        const length = result[0].length;
        const before = Math.max(0, index - EXCERPT_CONTEXT_CHARS);
        const after = Math.min(text.length, index + length + EXCERPT_CONTEXT_CHARS);
        matches.push({
            pre: `${before > 0 ? '…' : ''}${text.slice(before, index)}`,
            match: text.slice(index, index + length),
            post: `${text.slice(index + length, after)}${after < text.length ? '…' : ''}`,
        });
    }
    return matches;
}

function caseInsensitiveLiteralMatcher(query: string): RegExp {
    return new RegExp(query.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'), 'giu');
}

function appendPDFSearchPage(
    readerPage: HTMLElement,
    controls: ReaderSearchControls,
    state: PDFSearchState,
    pageNumber: number,
    matches: PDFSearchMatch[],
    options: PDFSearchOptions,
): void {
    const remaining = MAX_RENDERED_RESULTS - state.renderedCount;
    if (remaining <= 0) {
        if (matches.length > 0) state.resultsLimited = true;
        return;
    }
    const visibleMatches = matches.slice(0, remaining);
    if (visibleMatches.length < matches.length) state.resultsLimited = true;
    if (visibleMatches.length === 0) return;

    const group = document.createElement('li');
    group.className = 'reader-search-group';
    const title = document.createElement('h3');
    title.className = 'reader-search-group-title';
    title.textContent = `Page ${pageNumber}`;
    const list = document.createElement('ol');
    list.className = 'reader-search-group-list';

    for (const [index, match] of visibleMatches.entries()) {
        const resultID = `${pageNumber}:${index}`;
        const row = document.createElement('li');
        row.className = 'reader-search-result';
        const button = document.createElement('button');
        button.className = 'reader-search-result-btn';
        button.type = 'button';
        button.dataset.readerSearchPdfPage = String(pageNumber);
        button.dataset.readerSearchResultId = resultID;
        button.dataset.readerSearchPdfOccurrence = String(index);
        appendExcerpt(button, match);
        button.addEventListener('click', () => {
            const alreadyOnPage = options.currentPage() === pageNumber;
            state.activeResultID = resultID;
            state.highlight = {
                resultID,
                query: state.query,
                pageNumber,
                occurrence: index,
            };
            markPDFSearchResult(controls, state.activeResultID);
            void options
                .navigateTo(pageNumber)
                .then(() => {
                    if (alreadyOnPage && state.highlight?.resultID === resultID) {
                        renderPDFSearchHighlight(
                            readerPage,
                            state.highlight,
                            state.renderedTextLayer,
                        );
                    }
                })
                .catch((error) => {
                    console.error('Failed to navigate PDF search:', error);
                });
        });
        row.append(button);
        list.append(row);
        state.renderedCount++;
    }
    group.append(title, list);
    controls.results.append(group);
}

function updatePDFSearchStatus(
    controls: ReaderSearchControls,
    state: PDFSearchState,
    progress?: number,
): void {
    controls.panel.dataset.readerSearchResults = String(state.resultCount);
    if (state.searching) {
        const percent =
            typeof progress === 'number' ? ` ${Math.max(1, Math.round(progress * 100))}%` : '';
        controls.status.textContent =
            state.resultCount === 0
                ? `Searching...${percent}`
                : `Searching... ${formatResultCount(state.resultCount)}${percent}`;
        return;
    }
    if (state.resultCount === 0) {
        controls.status.textContent = 'No matches.';
        return;
    }
    const limited = state.resultsLimited ? `; showing first ${state.renderedCount}` : '';
    controls.status.textContent = `${formatResultCount(state.resultCount)}${limited}`;
}

function markCurrentPDFSearchResult(
    readerPage: HTMLElement,
    controls: ReaderSearchControls,
    state: PDFSearchState,
    pageNumber: number,
): void {
    const active = state.activeResultID
        ? controls.results.querySelector<HTMLButtonElement>(
              `[data-reader-search-result-id="${CSS.escape(state.activeResultID)}"]`,
          )
        : null;
    if (active?.dataset.readerSearchPdfPage === String(pageNumber)) {
        markPDFSearchResult(controls, state.activeResultID);
        setPDFSearchHighlightFromResult(readerPage, state, active);
        return;
    }
    const first = controls.results.querySelector<HTMLButtonElement>(
        `[data-reader-search-pdf-page="${pageNumber}"]`,
    );
    state.activeResultID = first?.dataset.readerSearchResultId || '';
    markPDFSearchResult(controls, state.activeResultID);
    if (first) {
        setPDFSearchHighlightFromResult(readerPage, state, first);
        return;
    }
    if (state.highlight?.pageNumber === pageNumber) {
        renderPDFSearchHighlight(readerPage, state.highlight, state.renderedTextLayer);
    } else {
        clearPDFSearchHighlight(readerPage);
    }
}

function markPDFSearchResult(controls: ReaderSearchControls, resultID: string): void {
    for (const button of controls.results.querySelectorAll<HTMLButtonElement>(
        '[data-reader-search-result-id]',
    )) {
        const active = Boolean(resultID) && button.dataset.readerSearchResultId === resultID;
        button.classList.toggle('active', active);
        if (active) button.setAttribute('aria-current', 'location');
        else button.removeAttribute('aria-current');
    }
}

function clearPDFSearch(
    readerPage: HTMLElement,
    controls: ReaderSearchControls,
    state: PDFSearchState,
): void {
    window.clearTimeout(state.debounce);
    state.debounce = 0;
    state.sequence++;
    state.searching = false;
    state.resultCount = 0;
    state.renderedCount = 0;
    state.resultsLimited = false;
    state.activeResultID = '';
    state.query = '';
    state.highlight = null;
    clearPDFSearchHighlight(readerPage);
    controls.results.replaceChildren();
    controls.status.textContent = 'Type to search this book.';
    controls.input.value = '';
    controls.panel.dataset.readerSearchResults = '0';
    cancelActiveSearch(state);
}

function setPDFSearchHighlightFromResult(
    readerPage: HTMLElement,
    state: PDFSearchState,
    result: HTMLButtonElement,
): void {
    const pageNumber = Number.parseInt(result.dataset.readerSearchPdfPage || '', 10);
    const occurrence = Number.parseInt(result.dataset.readerSearchPdfOccurrence || '', 10);
    const resultID = result.dataset.readerSearchResultId || '';
    if (!state.query || !resultID || !Number.isFinite(pageNumber) || !Number.isFinite(occurrence)) {
        clearPDFSearchHighlight(readerPage);
        return;
    }
    state.highlight = { resultID, query: state.query, pageNumber, occurrence };
    renderPDFSearchHighlight(readerPage, state.highlight, state.renderedTextLayer);
}

function renderPDFSearchHighlight(
    readerPage: HTMLElement,
    highlight: PDFSearchHighlight,
    renderedTextLayer: PDFRenderedTextLayer | null,
): void {
    clearPDFSearchHighlight(readerPage);
    const page = readerPage.querySelector<HTMLElement>('[data-pdf-page]');
    if (
        !page ||
        !renderedTextLayer ||
        renderedTextLayer.pageNumber !== highlight.pageNumber ||
        page.dataset.pdfRenderedPage !== String(highlight.pageNumber)
    ) {
        return;
    }

    const textMap = buildPDFDOMTextMap(renderedTextLayer);
    const match = findPDFDOMMatch(textMap.text, highlight.query, highlight.occurrence);
    if (!match) return;

    const start = pdfDOMTextPosition(textMap, match.index, false);
    const end = pdfDOMTextPosition(textMap, match.index + match.length - 1, true);
    if (!start || !end) return;
    const range = document.createRange();
    try {
        range.setStart(start.node, start.offset);
        range.setEnd(end.node, end.offset);
    } catch {
        return;
    }
    const rectangles = Array.from(range.getClientRects()).filter(
        (rect) => rect.width > 0.5 && rect.height > 0.5,
    );
    if (rectangles.length === 0) return;

    const pageRect = page.getBoundingClientRect();
    const overlay = document.createElement('div');
    overlay.className = 'reader-pdf-search-highlights';
    overlay.dataset.pdfSearchHighlights = highlight.resultID;
    overlay.setAttribute('aria-hidden', 'true');
    for (const rect of rectangles) {
        const left = Math.max(0, rect.left - pageRect.left);
        const top = Math.max(0, rect.top - pageRect.top);
        const right = Math.min(pageRect.width, rect.right - pageRect.left);
        const bottom = Math.min(pageRect.height, rect.bottom - pageRect.top);
        if (right <= left || bottom <= top) continue;
        const marker = document.createElement('span');
        marker.className = 'reader-pdf-search-highlight';
        marker.style.left = `${left}px`;
        marker.style.top = `${top}px`;
        marker.style.width = `${right - left}px`;
        marker.style.height = `${bottom - top}px`;
        overlay.append(marker);
    }
    if (!overlay.hasChildNodes()) return;
    page.append(overlay);
    scrollPDFSearchHighlight(readerPage, rectangles, overlay);
}

function clearPDFSearchHighlight(readerPage: HTMLElement): void {
    readerPage.querySelector('[data-pdf-search-highlights]')?.remove();
}

function buildPDFDOMTextMap(textLayer: PDFRenderedTextLayer): PDFDOMTextMap {
    const parts: string[] = [];
    const runs: PDFDOMTextRun[] = [];
    let length = 0;
    let pendingSpace = false;
    let previous: PDFDOMTextItem | null = null;
    const count = Math.min(textLayer.textDivs.length, textLayer.textContentItemsStr.length);
    for (let index = 0; index < count; index++) {
        const element = textLayer.textDivs[index];
        const node = element.firstChild;
        if (!(node instanceof Text) || !node.data) continue;
        const current: PDFDOMTextItem = {
            node,
            text: textLayer.textContentItemsStr[index] || node.data,
            rect: element.getBoundingClientRect(),
            direction: element.dir,
        };
        const separator = pdfDOMTextSeparator(previous, current);
        if (separator && length > 0) pendingSpace = true;

        const sourceMatcher = /\s+|\S+/g;
        for (
            let match = sourceMatcher.exec(node.data);
            match;
            match = sourceMatcher.exec(node.data)
        ) {
            const value = match[0];
            if (/^\s+$/.test(value)) {
                if (length > 0) pendingSpace = true;
                continue;
            }
            if (pendingSpace) {
                parts.push(' ');
                length++;
                pendingSpace = false;
            }
            const nodeOffset = match.index;
            parts.push(value);
            runs.push({
                start: length,
                end: length + value.length,
                node,
                nodeOffset,
            });
            length += value.length;
        }
        previous = current;
    }
    return { text: parts.join(''), runs };
}

function pdfDOMTextSeparator(previous: PDFDOMTextItem | null, current: PDFDOMTextItem): string {
    if (!previous || /\s$/.test(previous.text) || /^\s/.test(current.text)) return '';
    const height = Math.max(previous.rect.height, current.rect.height, 1);
    if (Math.abs(previous.rect.top - current.rect.top) > height * 0.5) return ' ';
    if (previous.direction === 'rtl' || current.direction === 'rtl') {
        return previous.rect.left - current.rect.right > height * 0.12 ? ' ' : '';
    }
    return current.rect.left - previous.rect.right > height * 0.12 ? ' ' : '';
}

function findPDFDOMMatch(text: string, query: string, occurrence: number): PDFTextMatch | null {
    const matcher = caseInsensitiveLiteralMatcher(query);
    for (let index = 0; index <= occurrence; index++) {
        const match = matcher.exec(text);
        if (!match) return null;
        if (index === occurrence) return { index: match.index, length: match[0].length };
    }
    return null;
}

function pdfDOMTextPosition(
    textMap: PDFDOMTextMap,
    index: number,
    afterCharacter: boolean,
): { node: Text; offset: number } | null {
    let low = 0;
    let high = textMap.runs.length - 1;
    while (low <= high) {
        const middle = Math.floor((low + high) / 2);
        const run = textMap.runs[middle];
        if (index < run.start) {
            high = middle - 1;
        } else if (index >= run.end) {
            low = middle + 1;
        } else {
            return {
                node: run.node,
                offset: run.nodeOffset + index - run.start + (afterCharacter ? 1 : 0),
            };
        }
    }
    return null;
}

function scrollPDFSearchHighlight(
    readerPage: HTMLElement,
    rectangles: DOMRect[],
    overlay: HTMLElement,
): void {
    const stage = readerPage.querySelector<HTMLElement>('[data-pdf-stage]');
    if (!stage) return;
    const canScrollStage =
        stage.scrollWidth > stage.clientWidth || stage.scrollHeight > stage.clientHeight;
    if (!canScrollStage) {
        overlay.firstElementChild?.scrollIntoView({
            behavior: 'auto',
            block: 'center',
            inline: 'center',
        });
        return;
    }
    const stageRect = stage.getBoundingClientRect();
    const left = Math.min(...rectangles.map((rect) => rect.left));
    const right = Math.max(...rectangles.map((rect) => rect.right));
    const top = Math.min(...rectangles.map((rect) => rect.top));
    const bottom = Math.max(...rectangles.map((rect) => rect.bottom));
    const targetLeft =
        stage.scrollLeft + (left + right) / 2 - (stageRect.left + stage.clientWidth / 2);
    const targetTop =
        stage.scrollTop + (top + bottom) / 2 - (stageRect.top + stage.clientHeight / 2);
    stage.scrollTo({
        left: stage.scrollWidth > stage.clientWidth ? targetLeft : stage.scrollLeft,
        top: stage.scrollHeight > stage.clientHeight ? targetTop : stage.scrollTop,
        behavior: 'auto',
    });
}

function cancelPDFTextReader(
    reader: ReadableStreamDefaultReader<PDFTextContentChunk>,
    reason: string,
): void {
    // PDF.js requires an Error reason; undefined closes the browser stream
    // before its MessageHandler marks the worker producer closed.
    // Do not await the acknowledgement here: real OCR/text-heavy documents can
    // leave that promise unresolved even though cancellation has synchronously
    // closed the consumer and been posted to the worker.
    void reader.cancel(new Error(reason)).catch(() => undefined);
}

function cancelActiveSearch(state: PDFSearchState): void {
    const reader = state.activeReader;
    state.activeReader = null;
    if (reader) cancelPDFTextReader(reader, 'PDF search cancelled.');
}
