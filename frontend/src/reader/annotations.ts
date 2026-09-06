import { Overlayer } from 'foliate-js/overlayer.js';

import { createAnnotation, deleteAnnotation, fetchAnnotations, updateAnnotationNote } from '../api';
import { clamp } from '../dom';
import { iconElement } from '../icons';
import type { Annotation } from '../types';
import { focusReaderSurface, revealChrome } from './chrome';
import type { FoliateAnnotation, FoliateLoadDetail, FoliateViewElement } from './foliate-engine';
import type {
    ReaderAnnotationActionTarget,
    ReaderAnnotationReference,
    ReaderSelectionPayload,
} from './selection';

const HIGHLIGHT_COLOR = '#f2d46b';
const POPOVER_GAP = 8;
const POPOVER_MARGIN = 8;

interface AnnotationOptions {
    onNavigate?: () => void;
    onShowActions?: (target: ReaderAnnotationActionTarget) => void;
}

interface AnnotationPopover {
    root: HTMLElement;
    quote: HTMLElement;
    note: HTMLTextAreaElement;
    status: HTMLElement;
    doneButton: HTMLButtonElement;
}

interface RenderedAnnotation {
    doc: Document;
    index?: number;
    range: Range;
}

interface AnnotationPanel {
    backdrop: HTMLButtonElement;
    panel: HTMLElement;
    toggle: HTMLButtonElement;
    status: HTMLElement;
    list: HTMLOListElement;
}

export interface AnnotationController {
    hydrate(): Promise<void>;
    savePendingNote(): Promise<boolean>;
    createHighlight(payload: ReaderSelectionPayload, editNote?: boolean): void;
    editNote(cfi: string): void;
    deleteHighlight(cfi: string): void;
    annotationAt(doc: Document, range: Range): ReaderAnnotationReference | undefined;
}

export function wireAnnotations(
    page: HTMLElement,
    assetId: string,
    view: FoliateViewElement,
    options: AnnotationOptions = {},
): AnnotationController {
    const annotations = new Map<string, Annotation>();
    const sections = new Map<string, number>();
    const renderedAnnotations = new Map<string, RenderedAnnotation>();
    const wiredDocuments = new WeakSet<Document>();
    const panel = createAnnotationPanel(page);
    const popover = buildPopover();
    page.append(popover.root);
    let activePopoverAnnotation: Annotation | null = null;
    let pendingNoteSave: Promise<boolean> | null = null;
    let pendingNoteEditorCFI: string | null = null;
    const persistedAnnotations = fetchAnnotations(assetId).then(
        (rows) => ({ ok: true as const, rows }),
        (error: unknown) => ({ ok: false as const, error }),
    );
    let hydration: Promise<void> | null = null;

    const sortedAnnotations = (): Annotation[] =>
        [...annotations.values()].sort(
            (a, b) => a.created_at - b.created_at || a.id.localeCompare(b.id),
        );

    const hidePopover = (): void => {
        activePopoverAnnotation = null;
        popover.root.hidden = true;
    };

    const renderList = (): void => {
        if (!panel) return;
        renderAnnotationList(page, panel, sortedAnnotations(), options, requestNoteEditor);
    };

    const replaceAnnotation = (annotation: Annotation): void => {
        annotations.set(annotation.cfi, annotation);
        if (activePopoverAnnotation?.id === annotation.id) activePopoverAnnotation = annotation;
        renderList();
    };

    const renderAnnotation = async (annotation: Annotation): Promise<void> => {
        replaceAnnotation(annotation);
        const result = await view.addAnnotation?.(foliateAnnotation(annotation));
        if (typeof result?.index === 'number') sections.set(annotation.id, result.index);
    };

    const renderSection = (index: number): void => {
        for (const annotation of annotations.values()) {
            if (sections.get(annotation.id) === index) {
                renderAnnotation(annotation).catch((e) =>
                    console.error('Failed to draw annotation:', e),
                );
            }
        }
    };

    const saveAndClosePopover = (restoreFocus = false): Promise<boolean> => {
        if (!activePopoverAnnotation) return Promise.resolve(true);
        if (pendingNoteSave) return pendingNoteSave;
        const annotation = activePopoverAnnotation;
        const note = popover.note.value;
        if (note === (annotation.note || '')) {
            hidePopover();
            if (restoreFocus) focusReaderSurface(page);
            return Promise.resolve(true);
        }

        setPopoverBusy(popover, true, 'Saving...');
        const request = updateAnnotationNote(assetId, annotation.id, note)
            .then((updated) => {
                replaceAnnotation(updated);
                if (activePopoverAnnotation?.id === annotation.id) {
                    hidePopover();
                    if (restoreFocus) focusReaderSurface(page);
                }
                return true;
            })
            .catch((e) => {
                console.error('Failed to update annotation:', e);
                if (activePopoverAnnotation?.id === annotation.id) {
                    popover.status.textContent = 'Could not save note.';
                }
                return false;
            })
            .finally(() => {
                if (activePopoverAnnotation?.id === annotation.id) {
                    setPopoverBusy(popover, false);
                }
                if (pendingNoteSave === request) pendingNoteSave = null;
            });
        pendingNoteSave = request;
        return request;
    };

    function openNoteEditor(cfi: string, navigate = false): void {
        const annotation = annotations.get(cfi);
        if (!annotation) return;
        const rendered = renderedAnnotations.get(cfi);
        if (!navigate && rendered) {
            pendingNoteEditorCFI = null;
            activePopoverAnnotation = annotation;
            markActiveAnnotation(panel, annotation.id);
            showPopover(page, view, popover, annotation, rendered.range, rendered.index);
            return;
        }

        pendingNoteEditorCFI = cfi;
        const target = foliateAnnotation(annotation);
        const navigation = view.showAnnotation
            ? view.showAnnotation(target)
            : view.goTo(annotation.cfi);
        void Promise.resolve(navigation).then(
            () => {
                if (pendingNoteEditorCFI === cfi) pendingNoteEditorCFI = null;
            },
            (e) => {
                if (pendingNoteEditorCFI === cfi) pendingNoteEditorCFI = null;
                console.error('Failed to navigate annotation:', e);
            },
        );
    }

    function requestNoteEditor(cfi: string, navigate = false): void {
        if (!activePopoverAnnotation) {
            openNoteEditor(cfi, navigate);
            return;
        }
        void saveAndClosePopover().then((saved) => {
            if (saved) openNoteEditor(cfi, navigate);
        });
    }

    view.addEventListener('draw-annotation', (event) => {
        const detail = (
            event as CustomEvent<{
                draw?: (func: unknown, options?: unknown) => void;
                annotation?: FoliateAnnotation;
                doc?: Document;
                range?: Range;
            }>
        ).detail;
        if (
            !detail?.draw ||
            detail.annotation?.kind !== 'highlight' ||
            !detail.doc ||
            !detail.range
        )
            return;
        const index = view.renderer?.getContents?.().find((item) => item.doc === detail.doc)?.index;
        renderedAnnotations.set(detail.annotation.value, {
            doc: detail.doc,
            index,
            range: detail.range.cloneRange(),
        });
        detail.draw(Overlayer.highlight, { color: HIGHLIGHT_COLOR, padding: 1 });
    });
    view.addEventListener('show-annotation', (event) => {
        const detail = (event as CustomEvent<{ value?: string; index?: number; range?: Range }>)
            .detail;
        if (!detail?.value || !detail.range) return;
        const value = detail.value;
        const annotation = annotations.get(value);
        if (!annotation) return;
        const range = detail.range;
        const doc = annotationDocument(view, range, detail.index);
        if (!doc) return;
        const showAnnotationActions = options.onShowActions;
        const editRequested = pendingNoteEditorCFI === annotation.cfi || !showAnnotationActions;
        if (pendingNoteEditorCFI === annotation.cfi) pendingNoteEditorCFI = null;
        const showAnnotationUI = (): void => {
            const current = annotations.get(value);
            if (!current) return;
            markActiveAnnotation(panel, current.id);
            if (editRequested || !showAnnotationActions) {
                activePopoverAnnotation = current;
                showPopover(page, view, popover, current, range, detail.index);
                return;
            }
            showAnnotationActions({
                cfi: current.cfi,
                quote: current.quote,
                hasNote: Boolean(current.note),
                doc,
                index: detail.index,
                range,
            });
        };
        if (activePopoverAnnotation) {
            void saveAndClosePopover().then((saved) => {
                if (saved) showAnnotationUI();
            });
            return;
        }
        showAnnotationUI();
    });
    view.addEventListener('create-overlay', (event) => {
        const index = (event as CustomEvent<{ index?: number }>).detail?.index;
        if (typeof index === 'number') renderSection(index);
    });
    view.addEventListener('relocate', () => void saveAndClosePopover());

    document.addEventListener(
        'pointerdown',
        (event) => {
            const target = event.target;
            if (target instanceof Node && popover.root.contains(target)) return;
            void saveAndClosePopover();
        },
        true,
    );
    const wireDocument = (doc: Document): void => {
        if (wiredDocuments.has(doc)) return;
        wiredDocuments.add(doc);
        doc.addEventListener('pointerdown', () => void saveAndClosePopover(), true);
    };
    view.addEventListener('load', (event) => {
        // Reflowable books replace their section iframe on every load. Do not
        // retain ranges (and their detached documents) from earlier sections.
        if (!view.isFixedLayout) renderedAnnotations.clear();
        wireDocument((event as CustomEvent<FoliateLoadDetail>).detail.doc);
    });
    for (const content of view.renderer?.getContents?.() || []) {
        if (content.doc) wireDocument(content.doc);
    }
    document.addEventListener('keydown', (event) => {
        if (event.key !== 'Escape' || popover.root.hidden) return;
        event.preventDefault();
        void saveAndClosePopover(true);
    });
    const hidePopoverForViewportChange = (): void => {
        const focused = document.activeElement;
        if (focused instanceof Node && popover.root.contains(focused)) return;
        void saveAndClosePopover();
    };
    window.addEventListener('resize', hidePopoverForViewportChange);
    window.addEventListener('scroll', hidePopoverForViewportChange, true);

    popover.note.addEventListener('input', () => {
        popover.status.textContent = '';
    });
    popover.doneButton.addEventListener('click', () => void saveAndClosePopover(true));
    const deleteHighlight = (cfi: string): void => {
        const annotation = annotations.get(cfi);
        if (!annotation) return;
        if (activePopoverAnnotation?.id === annotation.id) hidePopover();
        deleteAnnotation(assetId, annotation.id)
            .then(() => {
                annotations.delete(annotation.cfi);
                sections.delete(annotation.id);
                renderedAnnotations.delete(annotation.cfi);
                renderList();
                return view.deleteAnnotation?.(foliateAnnotation(annotation));
            })
            .catch((e) => console.error('Failed to delete annotation:', e));
    };

    const persistHighlight = (payload: ReaderSelectionPayload, editNote: boolean): void => {
        createAnnotation(assetId, {
            kind: 'highlight',
            cfi: payload.cfi,
            quote: payload.quote,
            context_before: payload.context_before,
            context_after: payload.context_after,
            color: 'yellow',
        })
            .then(async (annotation) => {
                const previous = annotations.get(annotation.cfi);
                if (previous) {
                    sections.delete(previous.id);
                    renderedAnnotations.delete(previous.cfi);
                    void view.deleteAnnotation?.(foliateAnnotation(previous));
                }
                await renderAnnotation(annotation);
                if (editNote) openNoteEditor(annotation.cfi);
            })
            .catch((e) => console.error('Failed to create annotation:', e));
    };

    if (panel) wireAnnotationPanel(page, panel, () => renderList());

    return {
        hydrate(): Promise<void> {
            hydration ??= persistedAnnotations.then(async (result) => {
                if (!result.ok) {
                    console.error('Failed to load annotations:', result.error);
                    if (panel) panel.status.textContent = 'Could not load highlights.';
                    return;
                }
                await Promise.all(
                    result.rows.map((annotation) =>
                        renderAnnotation(annotation).catch((e) =>
                            console.error('Failed to draw annotation:', e),
                        ),
                    ),
                );
                renderList();
            });
            return hydration;
        },
        savePendingNote(): Promise<boolean> {
            return saveAndClosePopover();
        },
        createHighlight(payload: ReaderSelectionPayload, editNote = false): void {
            if (!activePopoverAnnotation) {
                persistHighlight(payload, editNote);
                return;
            }
            void saveAndClosePopover().then((saved) => {
                if (saved) persistHighlight(payload, editNote);
            });
        },
        editNote(cfi: string): void {
            requestNoteEditor(cfi);
        },
        deleteHighlight,
        annotationAt(doc: Document, range: Range): ReaderAnnotationReference | undefined {
            for (const [cfi, rendered] of renderedAnnotations) {
                if (rendered.doc !== doc || !rangesIntersect(range, rendered.range)) continue;
                const annotation = annotations.get(cfi);
                if (annotation) return { cfi, hasNote: Boolean(annotation.note) };
            }
            return undefined;
        },
    };
}

function rangesIntersect(a: Range, b: Range): boolean {
    const doc = a.startContainer.ownerDocument;
    if (!doc || b.startContainer.ownerDocument !== doc) return false;

    const aStart = collapsedRange(doc, a.startContainer, a.startOffset);
    const aEnd = collapsedRange(doc, a.endContainer, a.endOffset);
    const bStart = collapsedRange(doc, b.startContainer, b.startOffset);
    const bEnd = collapsedRange(doc, b.endContainer, b.endOffset);
    return (
        aStart.compareBoundaryPoints(Range.START_TO_START, bEnd) < 0 &&
        aEnd.compareBoundaryPoints(Range.START_TO_START, bStart) > 0
    );
}

function collapsedRange(doc: Document, node: Node, offset: number): Range {
    const range = doc.createRange();
    range.setStart(node, offset);
    range.collapse(true);
    return range;
}

function foliateAnnotation(annotation: Annotation): FoliateAnnotation {
    return {
        ...annotation,
        value: annotation.cfi,
    };
}

function createAnnotationPanel(page: HTMLElement): AnnotationPanel | null {
    const actions = page.querySelector<HTMLElement>('.reader-actions');
    if (!actions) return null;
    const panelID = 'reader-annotations-panel';

    const toggle = document.createElement('button');
    toggle.className = 'reader-annotations-toggle';
    toggle.type = 'button';
    toggle.dataset.readerAnnotationsToggle = 'true';
    toggle.title = 'Highlights';
    toggle.setAttribute('aria-label', 'Highlights');
    toggle.setAttribute('aria-controls', panelID);
    toggle.setAttribute('aria-expanded', 'false');
    toggle.append(iconElement('ink_highlighter'));

    const backdrop = document.createElement('button');
    backdrop.className = 'reader-annotations-backdrop';
    backdrop.type = 'button';
    backdrop.hidden = true;
    backdrop.tabIndex = -1;
    backdrop.setAttribute('aria-label', 'Close highlights');

    const panel = document.createElement('aside');
    panel.id = panelID;
    panel.className = 'reader-annotations-panel';
    panel.hidden = true;
    panel.setAttribute('aria-label', 'Highlights');

    const header = document.createElement('div');
    header.className = 'reader-annotations-header';
    const title = document.createElement('h2');
    title.className = 'reader-annotations-title';
    title.textContent = 'Highlights';
    const close = document.createElement('button');
    close.className = 'reader-annotations-close';
    close.type = 'button';
    close.title = 'Close highlights';
    close.dataset.readerAnnotationsClose = 'true';
    close.setAttribute('aria-label', 'Close highlights');
    close.append(iconElement('close'));
    header.append(title, close);

    const status = document.createElement('div');
    status.className = 'reader-annotations-status';
    status.setAttribute('aria-live', 'polite');
    status.textContent = 'No highlights yet.';

    const list = document.createElement('ol');
    list.className = 'reader-annotations-list';
    list.setAttribute('aria-label', 'Highlights');

    panel.append(header, status, list);
    actions.prepend(toggle);
    page.append(backdrop, panel);

    return { backdrop, panel, toggle, status, list };
}

function wireAnnotationPanel(
    page: HTMLElement,
    controls: AnnotationPanel,
    beforeOpen: () => void,
): void {
    controls.toggle.addEventListener('click', () => {
        if (controls.panel.hidden) openAnnotationPanel(page, controls, beforeOpen);
        else closeAnnotationPanel(page, controls, true);
    });
    controls.backdrop.addEventListener('click', () => closeAnnotationPanel(page, controls, true));
    controls.panel
        .querySelector<HTMLButtonElement>('[data-reader-annotations-close]')
        ?.addEventListener('click', () => closeAnnotationPanel(page, controls, true));
    document.addEventListener('keydown', (event) => {
        if (event.key !== 'Escape' || controls.panel.hidden) return;
        event.preventDefault();
        closeAnnotationPanel(page, controls, true);
    });
}

function openAnnotationPanel(
    page: HTMLElement,
    controls: AnnotationPanel,
    beforeOpen: () => void,
): void {
    beforeOpen();
    page.classList.add('reader-annotations-open');
    controls.panel.hidden = false;
    controls.backdrop.hidden = false;
    controls.toggle.setAttribute('aria-expanded', 'true');
    controls.panel
        .querySelector<HTMLElement>('.reader-annotations-item, .reader-annotations-close')
        ?.focus();
}

function closeAnnotationPanel(
    page: HTMLElement,
    controls: AnnotationPanel,
    restoreFocus: boolean,
): void {
    page.classList.remove('reader-annotations-open');
    controls.panel.hidden = true;
    controls.backdrop.hidden = true;
    controls.toggle.setAttribute('aria-expanded', 'false');
    revealChrome(page);
    if (restoreFocus) focusReaderSurface(page);
}

function renderAnnotationList(
    page: HTMLElement,
    controls: AnnotationPanel,
    rows: Annotation[],
    options: AnnotationOptions,
    openNoteEditor: (cfi: string, navigate?: boolean) => void,
): void {
    controls.list.replaceChildren();
    controls.panel.dataset.readerAnnotationsCount = String(rows.length);
    controls.status.textContent =
        rows.length === 0
            ? 'No highlights yet.'
            : `${rows.length} ${rows.length === 1 ? 'highlight' : 'highlights'}`;

    for (const annotation of rows) {
        const item = document.createElement('li');
        item.className = 'reader-annotations-row';

        const button = document.createElement('button');
        button.className = 'reader-annotations-item';
        button.type = 'button';
        button.dataset.readerAnnotationId = annotation.id;

        const quote = document.createElement('span');
        quote.className = 'reader-annotations-quote';
        quote.textContent = annotation.quote;
        button.append(quote);

        if (annotation.note) {
            const note = document.createElement('span');
            note.className = 'reader-annotations-note';
            note.textContent = annotation.note;
            button.append(note);
        }

        button.addEventListener('click', () => {
            options.onNavigate?.();
            markActiveAnnotation(controls, annotation.id);
            closeAnnotationPanel(page, controls, false);
            openNoteEditor(annotation.cfi, true);
        });
        item.append(button);
        controls.list.append(item);
    }
}

function markActiveAnnotation(controls: AnnotationPanel | null, annotationID: string): void {
    if (!controls) return;
    for (const button of controls.list.querySelectorAll<HTMLButtonElement>(
        '[data-reader-annotation-id]',
    )) {
        const active = button.dataset.readerAnnotationId === annotationID;
        button.classList.toggle('active', active);
        if (active) button.setAttribute('aria-current', 'location');
        else button.removeAttribute('aria-current');
    }
}

function buildPopover(): AnnotationPopover {
    const root = document.createElement('div');
    root.className = 'reader-annotation-popover';
    root.hidden = true;
    root.setAttribute('role', 'dialog');
    root.setAttribute('aria-label', 'Highlight note');

    const quote = document.createElement('div');
    quote.className = 'reader-annotation-quote';

    const note = document.createElement('textarea');
    note.className = 'reader-annotation-note';
    note.rows = 4;
    note.maxLength = 4000;
    note.placeholder = 'Add a note';
    note.setAttribute('aria-label', 'Note');

    const status = document.createElement('div');
    status.className = 'reader-annotation-status';
    status.setAttribute('aria-live', 'polite');

    const doneButton = document.createElement('button');
    doneButton.className = 'reader-annotation-action';
    doneButton.type = 'button';
    doneButton.title = 'Done';
    doneButton.setAttribute('aria-label', 'Done');
    doneButton.append(iconElement('check'));

    root.append(quote, note, status, doneButton);
    return { root, quote, note, status, doneButton };
}

function setPopoverBusy(popover: AnnotationPopover, busy: boolean, status?: string): void {
    popover.note.disabled = busy;
    popover.doneButton.disabled = busy;
    if (status !== undefined) popover.status.textContent = status;
}

function showPopover(
    page: HTMLElement,
    view: FoliateViewElement,
    popover: AnnotationPopover,
    annotation: Annotation,
    range: Range,
    index: number | undefined,
): void {
    popover.quote.textContent = annotation.quote;
    popover.note.value = annotation.note || '';
    popover.status.textContent = '';
    setPopoverBusy(popover, false);
    popover.root.hidden = false;

    const frame = annotationDocument(view, range, index)?.defaultView
        ?.frameElement as HTMLElement | null;
    const frameRect = frame?.getBoundingClientRect();
    const offsetX = frameRect?.left ?? 0;
    const offsetY = frameRect?.top ?? 0;
    const rangeRect = range.getBoundingClientRect();
    const targetCenter = rangeRect.left + rangeRect.width / 2 + offsetX;
    const targetTop = rangeRect.top + offsetY;
    const targetBottom = rangeRect.bottom + offsetY;

    const pageRect = page.getBoundingClientRect();
    const popoverRect = popover.root.getBoundingClientRect();
    const half = popoverRect.width / 2;
    const centerX = clamp(
        targetCenter,
        pageRect.left + POPOVER_MARGIN + half,
        pageRect.right - POPOVER_MARGIN - half,
    );
    let top = targetTop - POPOVER_GAP - popoverRect.height;
    if (top < pageRect.top + POPOVER_MARGIN) top = targetBottom + POPOVER_GAP;

    popover.root.style.left = `${centerX - pageRect.left}px`;
    popover.root.style.top = `${top - pageRect.top}px`;
}

function annotationDocument(
    view: FoliateViewElement,
    range: Range,
    index: number | undefined,
): Document | null {
    if (typeof index === 'number') {
        const content = view.renderer?.getContents?.().find((item) => item.index === index);
        if (content?.doc) return content.doc;
    }
    const node = range.startContainer;
    return node.ownerDocument || null;
}
