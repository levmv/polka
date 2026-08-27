import { clamp } from '../dom';
import { iconElement } from '../icons';
import type { FoliateLoadDetail, FoliateViewElement } from './foliate-engine';

// First reader interaction slice: a tiny copy-only toolbar over live text
// selections inside the Foliate section iframes. It proves the selection
// plumbing (real in-iframe selection detection, viewport-mapped popup
// coordinates, and a Foliate CFI for the range) that later annotation/search
// slices build on, but it persists nothing yet.

const TOOLBAR_GAP = 8;
const TOOLBAR_MARGIN = 8;
const QUOTE_MAX_LENGTH = 1200;
const CONTEXT_MAX_LENGTH = 500;

interface ActiveSelection {
    doc: Document;
    index?: number;
    range: Range;
    text: string;
    payload?: ReaderSelectionPayload;
}

export interface ReaderSelectionPayload {
    cfi: string;
    quote: string;
    context_before: string;
    context_after: string;
}

export interface ReaderSelectionOptions {
    onSearchSelection?: (text: string) => void;
    onHighlightSelection?: (payload: ReaderSelectionPayload) => void;
}

export function wireReaderSelection(
    page: HTMLElement,
    view: FoliateViewElement,
    options: ReaderSelectionOptions = {},
): void {
    const wiredDocuments = new WeakSet<Document>();
    const { toolbar, highlightButton, copyButton, searchButton } = buildToolbar({
        includeHighlight: Boolean(options.onHighlightSelection),
        includeSearch: Boolean(options.onSearchSelection),
    });
    page.append(toolbar);

    let active: ActiveSelection | null = null;
    let pointerDown = false;
    let refreshHandle = 0;

    const hide = (): void => {
        if (refreshHandle) {
            window.cancelAnimationFrame(refreshHandle);
            refreshHandle = 0;
        }
        if (!active) return;
        active = null;
        toolbar.classList.remove('reader-selection-toolbar--visible');
        toolbar.dataset.readerSelectionActive = 'false';
        delete toolbar.dataset.readerSelectionCfi;
    };

    const refresh = (doc: Document, index?: number): void => {
        const selection = doc.getSelection();
        if (!selection || selection.isCollapsed || selection.rangeCount === 0) {
            hide();
            return;
        }
        const text = selection.toString();
        if (!text.trim()) {
            hide();
            return;
        }
        const range = selection.getRangeAt(0).cloneRange();
        active = {
            doc,
            index,
            range,
            text,
            payload: selectionPayload(doc, view, index, range, text),
        };
        showToolbar(page, toolbar, active);
    };

    const scheduleRefresh = (doc: Document, index?: number): void => {
        if (refreshHandle) window.cancelAnimationFrame(refreshHandle);
        refreshHandle = window.requestAnimationFrame(() => {
            refreshHandle = 0;
            refresh(doc, index);
        });
    };

    const attach = (doc: Document, index?: number): void => {
        if (wiredDocuments.has(doc)) return;
        wiredDocuments.add(doc);
        doc.addEventListener(
            'pointerdown',
            () => {
                pointerDown = true;
                hide();
            },
            true,
        );
        doc.addEventListener(
            'pointerup',
            () => {
                pointerDown = false;
                scheduleRefresh(doc, index);
            },
            true,
        );
        doc.addEventListener('touchend', () => scheduleRefresh(doc, index), true);
        doc.addEventListener('selectionchange', () => {
            if (!pointerDown) scheduleRefresh(doc, index);
        });
        doc.addEventListener('scroll', hide, true);
    };

    copyButton.addEventListener('click', () => {
        if (!active) return;
        const { doc, text } = active;
        copyText(text).catch((e) => console.error('Failed to copy selection:', e));
        doc.getSelection()?.removeAllRanges();
        hide();
    });
    searchButton?.addEventListener('click', () => {
        if (!active) return;
        const { doc, text } = active;
        options.onSearchSelection?.(text);
        doc.getSelection()?.removeAllRanges();
        hide();
    });
    highlightButton?.addEventListener('click', () => {
        if (!active?.payload) return;
        const { doc, payload } = active;
        options.onHighlightSelection?.(payload);
        doc.getSelection()?.removeAllRanges();
        hide();
    });

    view.addEventListener('load', (event) => {
        const detail = (event as CustomEvent<FoliateLoadDetail>).detail;
        attach(detail.doc, detail.index);
    });
    view.addEventListener('relocate', hide);

    document.addEventListener(
        'pointerdown',
        (event) => {
            const target = event.target;
            if (target instanceof Node && toolbar.contains(target)) return;
            hide();
        },
        true,
    );
    window.addEventListener(
        'keydown',
        (event) => {
            if (event.key !== 'Escape' || toolbar.dataset.readerSelectionActive !== 'true') return;
            event.preventDefault();
            event.stopImmediatePropagation();
            hide();
        },
        true,
    );
    window.addEventListener('scroll', hide, true);
    window.addEventListener('resize', hide);

    // Sections already mounted before wiring (e.g. the first one on open).
    for (const content of view.renderer?.getContents?.() || []) {
        if (content.doc) attach(content.doc, content.index);
    }
}

function buildToolbar(options: { includeHighlight: boolean; includeSearch: boolean }): {
    toolbar: HTMLElement;
    highlightButton?: HTMLButtonElement;
    copyButton: HTMLButtonElement;
    searchButton?: HTMLButtonElement;
} {
    const toolbar = document.createElement('div');
    toolbar.className = 'reader-selection-toolbar';
    toolbar.setAttribute('role', 'toolbar');
    toolbar.setAttribute('aria-label', 'Selection actions');
    toolbar.dataset.readerSelectionActive = 'false';

    let highlightButton: HTMLButtonElement | undefined;
    if (options.includeHighlight) {
        highlightButton = document.createElement('button');
        highlightButton.className = 'reader-selection-action';
        highlightButton.type = 'button';
        highlightButton.dataset.readerSelectionHighlight = 'true';
        highlightButton.append(iconElement('bookmark'));
        const highlightLabel = document.createElement('span');
        highlightLabel.textContent = 'Highlight';
        highlightButton.append(highlightLabel);
        toolbar.append(highlightButton);
    }

    let searchButton: HTMLButtonElement | undefined;
    if (options.includeSearch) {
        searchButton = document.createElement('button');
        searchButton.className = 'reader-selection-action';
        searchButton.type = 'button';
        searchButton.dataset.readerSelectionSearch = 'true';
        searchButton.append(iconElement('search'));
        const searchLabel = document.createElement('span');
        searchLabel.textContent = 'Search';
        searchButton.append(searchLabel);
        toolbar.append(searchButton);
    }

    const copyButton = document.createElement('button');
    copyButton.className = 'reader-selection-action';
    copyButton.type = 'button';
    copyButton.dataset.readerSelectionCopy = 'true';
    copyButton.append(iconElement('content_copy'));
    const label = document.createElement('span');
    label.textContent = 'Copy';
    copyButton.append(label);

    toolbar.append(copyButton);
    return { toolbar, highlightButton, copyButton, searchButton };
}

function showToolbar(page: HTMLElement, toolbar: HTMLElement, active: ActiveSelection): void {
    const frame = active.doc.defaultView?.frameElement as HTMLElement | null;
    const frameRect = frame?.getBoundingClientRect();
    const offsetX = frameRect?.left ?? 0;
    const offsetY = frameRect?.top ?? 0;
    const selRect = active.range.getBoundingClientRect();
    const selTop = selRect.top + offsetY;
    const selBottom = selRect.bottom + offsetY;
    const selCenter = selRect.left + selRect.width / 2 + offsetX;

    // Measure while the toolbar is laid out but not yet interactive, then place
    // it above the selection, flipping below when there is no room up top.
    const pageRect = page.getBoundingClientRect();
    const toolbarRect = toolbar.getBoundingClientRect();
    const half = toolbarRect.width / 2;
    const centerX = clamp(
        selCenter,
        pageRect.left + TOOLBAR_MARGIN + half,
        pageRect.right - TOOLBAR_MARGIN - half,
    );
    let top = selTop - TOOLBAR_GAP - toolbarRect.height;
    if (top < pageRect.top + TOOLBAR_MARGIN) top = selBottom + TOOLBAR_GAP;

    toolbar.style.left = `${centerX - pageRect.left}px`;
    toolbar.style.top = `${top - pageRect.top}px`;
    toolbar.classList.add('reader-selection-toolbar--visible');
    toolbar.dataset.readerSelectionActive = 'true';

    if (active.payload?.cfi) toolbar.dataset.readerSelectionCfi = active.payload.cfi;
}

function selectionPayload(
    doc: Document,
    view: FoliateViewElement,
    index: number | undefined,
    range: Range,
    text: string,
): ReaderSelectionPayload | undefined {
    let cfi = '';
    try {
        cfi = view.getCFI?.(index ?? 0, range) || '';
    } catch (e) {
        console.error('Failed to compute selection CFI:', e);
    }
    if (!cfi) return undefined;

    return {
        cfi,
        quote: clipSnippet(normalizeSnippet(text), QUOTE_MAX_LENGTH, 'end'),
        context_before: clipSnippet(
            normalizeSnippet(contextBefore(doc, range)),
            CONTEXT_MAX_LENGTH,
            'start',
        ),
        context_after: clipSnippet(
            normalizeSnippet(contextAfter(doc, range)),
            CONTEXT_MAX_LENGTH,
            'end',
        ),
    };
}

function contextBefore(doc: Document, range: Range): string {
    const root = doc.body || doc.documentElement;
    if (!root) return '';
    try {
        const before = doc.createRange();
        before.selectNodeContents(root);
        before.setEnd(range.startContainer, range.startOffset);
        return before.toString();
    } catch {
        return '';
    }
}

function contextAfter(doc: Document, range: Range): string {
    const root = doc.body || doc.documentElement;
    if (!root) return '';
    try {
        const after = doc.createRange();
        after.selectNodeContents(root);
        after.setStart(range.endContainer, range.endOffset);
        return after.toString();
    } catch {
        return '';
    }
}

function normalizeSnippet(text: string): string {
    return text.replace(/\s+/g, ' ').trim();
}

function clipSnippet(text: string, maxLength: number, keep: 'start' | 'end'): string {
    if (text.length <= maxLength) return text;
    return keep === 'start' ? text.slice(text.length - maxLength) : text.slice(0, maxLength);
}

async function copyText(text: string): Promise<void> {
    if (navigator.clipboard?.writeText) {
        try {
            await navigator.clipboard.writeText(text);
            return;
        } catch {
            // Fall through to the execCommand path below.
        }
    }
    const area = document.createElement('textarea');
    area.value = text;
    area.setAttribute('readonly', '');
    area.style.position = 'fixed';
    area.style.top = '-1000px';
    area.style.opacity = '0';
    document.body.append(area);
    area.select();
    document.execCommand('copy');
    area.remove();
}
