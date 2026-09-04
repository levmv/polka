import { clamp } from '../dom';
import { iconElement } from '../icons';
import type {
    FoliateLoadDetail,
    FoliateRendererRelocateDetail,
    FoliateViewElement,
} from './foliate-engine';

const TOOLBAR_GAP = 8;
const TOOLBAR_MARGIN = 8;
// WebKit can finalize a touch selection over several event-loop turns. Retry
// until one refresh sees the range; a successful refresh cancels the rest.
const TOUCH_SELECTION_SETTLE_DELAYS = [100, 300, 700];
const TOUCH_SELECTION_FALLBACK_DELAYS = [250, 600, 1000];
const QUOTE_MAX_LENGTH = 1200;
const CONTEXT_MAX_LENGTH = 500;

interface ActiveSelection {
    doc: Document;
    index?: number;
    range: Range;
    text: string;
    payload?: ReaderSelectionPayload;
    annotation?: ReaderAnnotationReference;
}

export interface ReaderSelectionPayload {
    cfi: string;
    quote: string;
    context_before: string;
    context_after: string;
}

export interface ReaderAnnotationReference {
    cfi: string;
    hasNote: boolean;
}

export interface ReaderAnnotationActionTarget extends ReaderAnnotationReference {
    doc: Document;
    index?: number;
    range: Range;
    quote: string;
}

export interface ReaderSelectionOptions {
    onSearchSelection?: (text: string) => void;
    onHighlightSelection?: (payload: ReaderSelectionPayload) => void;
    onNoteSelection?: (payload: ReaderSelectionPayload) => void;
    onEditAnnotation?: (cfi: string) => void;
    onDeleteAnnotation?: (cfi: string) => void;
    annotationAt?: (doc: Document, range: Range) => ReaderAnnotationReference | undefined;
}

export interface ReaderSelectionController {
    showAnnotationActions(target: ReaderAnnotationActionTarget): void;
}

export function wireReaderSelection(
    page: HTMLElement,
    view: FoliateViewElement,
    options: ReaderSelectionOptions = {},
): ReaderSelectionController {
    const wiredDocuments = new WeakSet<Document>();
    const { toolbar, highlightButton, noteButton, deleteButton, copyButton, searchButton } =
        buildToolbar({
            includeHighlight: Boolean(options.onHighlightSelection),
            includeNote: Boolean(options.onNoteSelection || options.onEditAnnotation),
            includeDelete: Boolean(options.onDeleteAnnotation),
            includeSearch: Boolean(options.onSearchSelection),
        });
    page.append(toolbar);

    let active: ActiveSelection | null = null;
    let pointerDown = false;
    let touchGesture = false;
    let refreshHandle = 0;
    let touchRefreshTimers: number[] = [];
    let annotationActionsPending = false;

    const clearTouchRefreshes = (): void => {
        for (const timer of touchRefreshTimers) window.clearTimeout(timer);
        touchRefreshTimers = [];
    };

    const clearToolbar = (): void => {
        if (!active) return;
        active = null;
        toolbar.classList.remove('reader-selection-toolbar--visible');
        toolbar.dataset.readerSelectionActive = 'false';
        delete toolbar.dataset.readerSelectionCfi;
    };

    const hide = (): void => {
        if (refreshHandle) {
            window.cancelAnimationFrame(refreshHandle);
            refreshHandle = 0;
        }
        clearTouchRefreshes();
        annotationActionsPending = false;
        clearToolbar();
    };

    const refresh = (doc: Document, index?: number): void => {
        const selection = doc.getSelection();
        if (!selection || selection.isCollapsed || selection.rangeCount === 0) {
            clearToolbar();
            return;
        }
        const text = selection.toString();
        if (!text.trim()) {
            clearToolbar();
            return;
        }
        const range = selection.getRangeAt(0).cloneRange();
        clearTouchRefreshes();
        const annotation = options.annotationAt?.(doc, range);
        active = {
            doc,
            index,
            range,
            text,
            annotation,
            payload: annotation ? undefined : selectionPayload(doc, view, index, range, text),
        };
        showToolbar(page, toolbar, active, highlightButton, noteButton, deleteButton);
    };

    const scheduleRefresh = (doc: Document, index?: number): void => {
        if (refreshHandle) window.cancelAnimationFrame(refreshHandle);
        refreshHandle = window.requestAnimationFrame(() => {
            refreshHandle = 0;
            refresh(doc, index);
        });
    };

    const scheduleTouchRefreshes = (
        doc: Document,
        index?: number,
        delays = TOUCH_SELECTION_SETTLE_DELAYS,
    ): void => {
        clearTouchRefreshes();
        touchRefreshTimers = delays.map((delay) =>
            window.setTimeout(() => scheduleRefresh(doc, index), delay),
        );
    };

    const attach = (doc: Document, index?: number): void => {
        if (wiredDocuments.has(doc)) return;
        wiredDocuments.add(doc);
        doc.addEventListener(
            'pointerdown',
            (event) => {
                pointerDown = true;
                touchGesture = event.pointerType === 'touch';
                hide();
                if (touchGesture)
                    scheduleTouchRefreshes(doc, index, TOUCH_SELECTION_FALLBACK_DELAYS);
            },
            true,
        );
        doc.addEventListener(
            'pointerup',
            (event) => {
                pointerDown = false;
                touchGesture = false;
                if (event.pointerType === 'touch') scheduleTouchRefreshes(doc, index);
                else scheduleRefresh(doc, index);
            },
            true,
        );
        doc.addEventListener(
            'pointercancel',
            () => {
                pointerDown = false;
                touchGesture = false;
                scheduleTouchRefreshes(doc, index);
            },
            true,
        );
        // Keep Touch Events as a fallback: WebKit does not consistently end
        // native selection gestures with the matching Pointer Event.
        doc.addEventListener(
            'touchstart',
            () => {
                pointerDown = true;
                touchGesture = true;
                hide();
                scheduleTouchRefreshes(doc, index, TOUCH_SELECTION_FALLBACK_DELAYS);
            },
            true,
        );
        doc.addEventListener(
            'touchend',
            () => {
                pointerDown = false;
                touchGesture = false;
                scheduleTouchRefreshes(doc, index);
            },
            true,
        );
        doc.addEventListener(
            'touchcancel',
            () => {
                pointerDown = false;
                touchGesture = false;
                scheduleTouchRefreshes(doc, index);
            },
            true,
        );
        doc.addEventListener('selectionchange', () => {
            if (annotationActionsPending || (active?.annotation && active.doc === doc)) return;
            if (!pointerDown) {
                scheduleRefresh(doc, index);
                return;
            }
            if (!touchGesture) return;
            clearToolbar();
            scheduleTouchRefreshes(doc, index, TOUCH_SELECTION_FALLBACK_DELAYS);
        });
        doc.addEventListener(
            'contextmenu',
            (event) => {
                const selection = doc.getSelection();
                const touch = doc.defaultView?.matchMedia(
                    '(hover: none), (pointer: coarse)',
                ).matches;
                if (touch && selection && !selection.isCollapsed) event.preventDefault();
            },
            true,
        );
        doc.addEventListener(
            'scroll',
            () => {
                if (pointerDown) clearToolbar();
                else if (active?.annotation && active.doc === doc)
                    showToolbar(page, toolbar, active, highlightButton, noteButton, deleteButton);
                else scheduleRefresh(doc, index);
            },
            true,
        );
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
    noteButton?.addEventListener('click', () => {
        if (!active) return;
        const { annotation, doc, payload } = active;
        if (annotation) {
            if (!options.onEditAnnotation) return;
            options.onEditAnnotation(annotation.cfi);
        } else if (payload && options.onNoteSelection) {
            options.onNoteSelection(payload);
        } else return;
        doc.getSelection()?.removeAllRanges();
        hide();
    });
    deleteButton?.addEventListener('click', () => {
        if (!active?.annotation) return;
        if (!window.confirm('Delete this highlight?')) return;
        const { cfi } = active.annotation;
        active.doc.getSelection()?.removeAllRanges();
        hide();
        options.onDeleteAnnotation?.(cfi);
    });

    view.addEventListener('load', (event) => {
        const detail = (event as CustomEvent<FoliateLoadDetail>).detail;
        attach(detail.doc, detail.index);
    });
    view.renderer?.addEventListener('relocate', (event) => {
        if (annotationActionsPending) return;
        const detail = (event as CustomEvent<FoliateRendererRelocateDetail>).detail;
        if (detail.reason === 'snap') {
            if (active?.annotation) return;
            const content = (view.renderer?.getContents?.() || []).find(({ doc }) => {
                const selection = doc?.getSelection();
                return selection && selection.rangeCount > 0 && !selection.isCollapsed;
            });
            if (content?.doc) {
                scheduleRefresh(content.doc, content.index);
                return;
            }
        }
        hide();
    });

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
    const refreshActive = (): void => {
        if (!active) return;
        if (active.annotation)
            showToolbar(page, toolbar, active, highlightButton, noteButton, deleteButton);
        else scheduleRefresh(active.doc, active.index);
    };
    window.addEventListener('scroll', refreshActive, true);
    window.addEventListener('resize', refreshActive);
    window.visualViewport?.addEventListener('scroll', refreshActive);
    window.visualViewport?.addEventListener('resize', refreshActive);

    // Sections already mounted before wiring (e.g. the first one on open).
    for (const content of view.renderer?.getContents?.() || []) {
        if (content.doc) attach(content.doc, content.index);
    }

    return {
        showAnnotationActions(target: ReaderAnnotationActionTarget): void {
            hide();
            annotationActionsPending = true;
            refreshHandle = window.requestAnimationFrame(() => {
                refreshHandle = 0;
                annotationActionsPending = false;
                active = {
                    doc: target.doc,
                    index: target.index,
                    range: target.range.cloneRange(),
                    text: target.quote,
                    annotation: { cfi: target.cfi, hasNote: target.hasNote },
                };
                showToolbar(page, toolbar, active, highlightButton, noteButton, deleteButton);
            });
        },
    };
}

function buildToolbar(options: {
    includeHighlight: boolean;
    includeNote: boolean;
    includeDelete: boolean;
    includeSearch: boolean;
}): {
    toolbar: HTMLElement;
    highlightButton?: HTMLButtonElement;
    noteButton?: HTMLButtonElement;
    deleteButton?: HTMLButtonElement;
    copyButton: HTMLButtonElement;
    searchButton?: HTMLButtonElement;
} {
    const toolbar = document.createElement('div');
    toolbar.className = 'reader-selection-toolbar';
    toolbar.setAttribute('role', 'toolbar');
    toolbar.setAttribute('aria-label', 'Selection actions');
    toolbar.dataset.readerSelectionActive = 'false';

    const copyButton = document.createElement('button');
    copyButton.className = 'reader-selection-action';
    copyButton.type = 'button';
    copyButton.dataset.readerSelectionCopy = 'true';
    copyButton.title = 'Copy';
    copyButton.setAttribute('aria-label', 'Copy');
    copyButton.append(iconElement('content_copy'));
    toolbar.append(copyButton);

    let highlightButton: HTMLButtonElement | undefined;
    if (options.includeHighlight) {
        highlightButton = document.createElement('button');
        highlightButton.className = 'reader-selection-action';
        highlightButton.type = 'button';
        highlightButton.dataset.readerSelectionHighlight = 'true';
        highlightButton.title = 'Highlight';
        highlightButton.setAttribute('aria-label', 'Highlight');
        highlightButton.append(iconElement('ink_highlighter'));
        toolbar.append(highlightButton);
    }

    let noteButton: HTMLButtonElement | undefined;
    if (options.includeNote) {
        noteButton = document.createElement('button');
        noteButton.className = 'reader-selection-action';
        noteButton.type = 'button';
        noteButton.dataset.readerSelectionNote = 'true';
        noteButton.title = 'Add note';
        noteButton.setAttribute('aria-label', 'Add note');
        const noteLabel = document.createElement('span');
        noteLabel.className = 'reader-selection-action-label';
        noteLabel.textContent = 'Note';
        noteButton.append(noteLabel);
        toolbar.append(noteButton);
    }

    let deleteButton: HTMLButtonElement | undefined;
    if (options.includeDelete) {
        deleteButton = document.createElement('button');
        deleteButton.className = 'reader-selection-action';
        deleteButton.type = 'button';
        deleteButton.hidden = true;
        deleteButton.dataset.readerSelectionDelete = 'true';
        deleteButton.title = 'Delete highlight';
        deleteButton.setAttribute('aria-label', 'Delete highlight');
        deleteButton.append(iconElement('delete'));
    }

    let searchButton: HTMLButtonElement | undefined;
    if (options.includeSearch) {
        searchButton = document.createElement('button');
        searchButton.className = 'reader-selection-action';
        searchButton.type = 'button';
        searchButton.dataset.readerSelectionSearch = 'true';
        searchButton.title = 'Search';
        searchButton.setAttribute('aria-label', 'Search');
        searchButton.append(iconElement('search'));
        toolbar.append(searchButton);
    }

    if (deleteButton) toolbar.append(deleteButton);
    return { toolbar, highlightButton, noteButton, deleteButton, copyButton, searchButton };
}

function showToolbar(
    page: HTMLElement,
    toolbar: HTMLElement,
    active: ActiveSelection,
    highlightButton?: HTMLButtonElement,
    noteButton?: HTMLButtonElement,
    deleteButton?: HTMLButtonElement,
): void {
    const annotation = active.annotation;
    if (highlightButton) highlightButton.hidden = Boolean(annotation);
    if (deleteButton) deleteButton.hidden = !annotation;
    if (noteButton) {
        const action = annotation?.hasNote ? 'Edit note' : 'Add note';
        noteButton.title = action;
        noteButton.setAttribute('aria-label', action);
        const visibleLabel = noteButton.querySelector('.reader-selection-action-label');
        if (visibleLabel) visibleLabel.textContent = annotation ? action : 'Note';
    }
    toolbar.setAttribute('aria-label', annotation ? 'Highlight actions' : 'Selection actions');

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

    const cfi = active.payload?.cfi || active.annotation?.cfi;
    if (cfi) toolbar.dataset.readerSelectionCfi = cfi;
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
