import { ANNOTATION_COLORS, sortAnnotations } from '../annotations';
import { APIError, createAnnotation, fetchAnnotations } from '../api';
import { createAnnotationEditor } from '../components/annotation-editor';
import { clamp } from '../dom';
import { errorMessage } from '../errors';
import { showToast } from '../toast';
import type { Annotation, Locator } from '../types';
import type { AnnotationAnchor, AnnotationSurface } from './annotation-surface';
import { focusReaderSurface, revealChrome } from './chrome';
import { createReaderPanel, type ReaderPanelElements } from './panel';
import type {
    ReaderAnnotationActionTarget,
    ReaderAnnotationReference,
    ReaderSelectionPayload,
} from './selection';

const POPOVER_GAP = 8;
const POPOVER_MARGIN = 8;

interface AnnotationOptions {
    onNavigate?: () => void;
    onShowActions?: (target: ReaderAnnotationActionTarget) => void;
}

interface AnnotationPanel extends ReaderPanelElements {
    status: HTMLElement;
    list: HTMLOListElement;
    retry: HTMLButtonElement;
}

export interface AnnotationController {
    load(): Promise<void>;
    location(annotationID: number): Locator | undefined;
    savePendingEdits(): Promise<boolean>;
    createHighlight(payload: ReaderSelectionPayload, editNote?: boolean): void;
    editNote(id: number): void;
    deleteHighlight(id: number): void;
    annotationAt(doc: Document, range: Range): ReaderAnnotationReference | undefined;
}

export function wireAnnotations(
    page: HTMLElement,
    assetId: number,
    surface: AnnotationSurface,
    options: AnnotationOptions = {},
): AnnotationController {
    const annotations = new Map<number, Annotation>();
    const panel = createAnnotationPanel(page);
    const popover = buildPopover();
    page.append(popover);
    let editor: ReturnType<typeof createAnnotationEditor> | null = null;
    let pendingNoteEditorID: number | null = null;
    let loadState: 'loading' | 'ready' | 'error' = 'loading';
    let loading: Promise<void> | null = null;
    let changedDuringLoad: Set<number> | null = null;

    const sortedAnnotations = (): Annotation[] =>
        sortAnnotations([...annotations.values()], 'position');

    const hidePopover = (): void => {
        editor?.destroy();
        editor = null;
        popover.hidden = true;
    };

    const renderList = (): void => {
        if (!panel) return;
        renderAnnotationList(page, panel, sortedAnnotations(), options, requestNoteEditor);
        panel.status.textContent =
            loadState === 'loading'
                ? 'Loading highlights…'
                : loadState === 'error'
                  ? 'Could not load all highlights.'
                  : annotations.size === 0
                    ? 'No highlights yet.'
                    : `${annotations.size} ${annotations.size === 1 ? 'highlight' : 'highlights'}`;
        panel.retry.hidden = loadState !== 'error';
    };

    const replaceAnnotation = (annotation: Annotation): Annotation => {
        const previous = annotations.get(annotation.id);
        if (previous && previous.revision > annotation.revision) return previous;
        changedDuringLoad?.add(annotation.id);
        annotations.set(annotation.id, annotation);
        // An incoming record must not silently change an open draft's base.
        // Its original revision still protects text edited in another reader.
        return annotation;
    };

    const renderAnnotation = (annotation: Annotation): Promise<void> => surface.add(annotation);

    const acceptSavedAnnotation = (annotation: Annotation, previous: Annotation): void => {
        const known = annotations.get(annotation.id);
        if (known && known.revision > annotation.revision) return;
        if (previous.id !== annotation.id) {
            changedDuringLoad?.add(previous.id);
            annotations.delete(previous.id);
            void surface
                .remove(previous)
                .catch((e) => console.error('Failed to remove annotation:', e));
        }
        replaceAnnotation(annotation);
        renderList();
        if (annotation.color !== known?.color) {
            void renderAnnotation(annotation).catch((e) =>
                console.error('Failed to draw annotation:', e),
            );
        }
    };

    const saveAndClosePopover = async (restoreFocus = false): Promise<boolean> => {
        const editing = editor;
        if (!editing) return true;
        const saved = await editing.save();
        if (saved && editor === editing) {
            hidePopover();
            if (restoreFocus) focusReaderSurface(page);
        }
        return saved;
    };

    function createNoteEditor(annotation: Annotation) {
        hidePopover();
        editor = createAnnotationEditor(annotation, {
            layout: 'popover',
            onChange: acceptSavedAnnotation,
            onDelete(deleted) {
                changedDuringLoad?.add(deleted.id);
                annotations.delete(deleted.id);
                hidePopover();
                renderList();
                void surface
                    .remove(deleted)
                    .catch((e) => console.error('Failed to remove annotation:', e));
                focusReaderSurface(page);
            },
            onClose() {
                hidePopover();
                focusReaderSurface(page);
            },
            onLayout() {
                if (!popover.hidden) constrainPopover(page, popover);
            },
        });
        popover.append(editor.el);
        return editor;
    }

    function showNoteEditor(annotation: Annotation, anchor: AnnotationAnchor): void {
        createNoteEditor(annotation);
        showPopover(page, popover, anchor);
    }

    function openNoteEditor(id: number, navigate = false): void {
        const annotation = annotations.get(id);
        if (!annotation) return;
        const rendered = surface.anchor(id);
        if (!navigate && rendered) {
            pendingNoteEditorID = null;
            markActiveAnnotation(panel, annotation.id);
            showNoteEditor(annotation, rendered);
            return;
        }

        pendingNoteEditorID = id;
        const navigation = surface.show(annotation);
        void Promise.resolve(navigation).then(
            () => {
                if (pendingNoteEditorID === id) pendingNoteEditorID = null;
            },
            (e) => {
                if (pendingNoteEditorID === id) pendingNoteEditorID = null;
                console.error('Failed to navigate annotation:', e);
            },
        );
    }

    function requestNoteEditor(id: number, navigate = false): void {
        if (!editor) {
            openNoteEditor(id, navigate);
            return;
        }
        void saveAndClosePopover().then((saved) => {
            if (saved) openNoteEditor(id, navigate);
        });
    }

    surface.connect({
        leave: () => void saveAndClosePopover(),
        show(id, anchor): void {
            const annotation = annotations.get(id);
            if (!annotation) return;
            const showActions = options.onShowActions;
            const editRequested = pendingNoteEditorID === id || !showActions;
            if (pendingNoteEditorID === id) pendingNoteEditorID = null;
            const show = (): void => {
                const current = annotations.get(id);
                if (!current) return;
                markActiveAnnotation(panel, id);
                if (editRequested || !showActions) {
                    showNoteEditor(current, anchor);
                } else {
                    showActions({
                        id,
                        locator: current.locator,
                        quote: current.quote,
                        hasNote: Boolean(current.note),
                        anchor,
                    });
                }
            };
            if (editor) {
                void saveAndClosePopover().then((saved) => {
                    if (saved) show();
                });
            } else show();
        },
    });

    document.addEventListener(
        'pointerdown',
        (event) => {
            const target = event.target;
            if (target instanceof Node && popover.contains(target)) return;
            void saveAndClosePopover();
        },
        true,
    );
    document.addEventListener('keydown', (event) => {
        if (event.key !== 'Escape' || popover.hidden) return;
        event.preventDefault();
        void saveAndClosePopover(true);
    });
    const hidePopoverForViewportChange = (): void => {
        const focused = document.activeElement;
        if (focused instanceof Node && popover.contains(focused)) return;
        void saveAndClosePopover();
    };
    window.addEventListener('resize', hidePopoverForViewportChange);
    window.addEventListener('scroll', hidePopoverForViewportChange, true);

    function deleteHighlight(id: number): void {
        void saveAndClosePopover().then((saved) => {
            const annotation = annotations.get(id);
            if (!saved || !annotation) return;
            const editing = createNoteEditor(annotation);
            const request = editing.requestDelete();
            if (!request) {
                hidePopover();
                return;
            }
            void request.then((deleted) => {
                if (deleted || editor !== editing) return;
                showPopover(
                    page,
                    popover,
                    surface.anchor(id) ?? {
                        doc: document,
                        getBoundingClientRect: () => page.getBoundingClientRect(),
                    },
                );
            });
        });
    }

    const persistHighlight = (payload: ReaderSelectionPayload, editNote: boolean): void => {
        createAnnotation(assetId, {
            locator: payload.locator,
            quote: payload.quote,
            context_before: payload.context_before,
            context_after: payload.context_after,
        })
            .then(async (annotation) => {
                annotation = replaceAnnotation(annotation);
                renderList();
                await renderAnnotation(annotation);
                if (editNote) openNoteEditor(annotation.id);
            })
            .catch((e) =>
                showToast(errorMessage(e, 'Could not save highlight.'), {
                    type: 'error',
                    action:
                        e instanceof APIError && e.status < 500 && e.status !== 429
                            ? undefined
                            : {
                                  label: 'Retry',
                                  onClick: () => persistHighlight(payload, editNote),
                              },
                }),
            );
    };

    // Load once and share concurrent calls. A failed load can be retried
    // without reopening the reader.
    function load(): Promise<void> {
        if (loading) return loading;
        if (loadState === 'ready') return Promise.resolve();
        loadState = 'loading';
        const changed = new Set<number>();
        changedDuringLoad = changed;
        renderList();
        loading = fetchAnnotations(assetId)
            .then(async (rows) => {
                const ids = new Set(rows.map((row) => row.id));
                const rendering: Promise<void>[] = [];
                // Preserve edits and deletions confirmed after this GET started.
                for (const annotation of annotations.values()) {
                    if (!ids.has(annotation.id) && !changed.has(annotation.id)) {
                        annotations.delete(annotation.id);
                        rendering.push(surface.remove(annotation));
                    }
                }
                for (const annotation of rows) {
                    if (changed.has(annotation.id)) continue;
                    const previous = annotations.get(annotation.id);
                    if (previous && previous.revision > annotation.revision) continue;
                    annotations.set(annotation.id, annotation);
                    rendering.push(renderAnnotation(annotation));
                }
                await Promise.all(
                    rendering.map((operation) =>
                        operation.catch((error) =>
                            console.error('Failed to draw annotation:', error),
                        ),
                    ),
                );
                loadState = 'ready';
            })
            .catch((error) => {
                console.error('Failed to load annotations:', error);
                loadState = 'error';
            })
            .finally(() => {
                loading = null;
                changedDuringLoad = null;
                renderList();
            });
        return loading;
    }

    if (panel) {
        wireAnnotationPanel(page, panel, renderList);
        panel.retry.addEventListener('click', () => void load());
    }
    void load();

    return {
        location(annotationID: number): Locator | undefined {
            const annotation = annotations.get(annotationID);
            return annotation?.locator;
        },
        load,
        savePendingEdits(): Promise<boolean> {
            return saveAndClosePopover();
        },
        createHighlight(payload: ReaderSelectionPayload, editNote = false): void {
            if (!editor) {
                persistHighlight(payload, editNote);
                return;
            }
            void saveAndClosePopover().then((saved) => {
                if (saved) persistHighlight(payload, editNote);
            });
        },
        editNote(id: number): void {
            requestNoteEditor(id);
        },
        deleteHighlight,
        annotationAt(doc: Document, range: Range): ReaderAnnotationReference | undefined {
            const id = surface.atRange(doc, range);
            const annotation = id === undefined ? undefined : annotations.get(id);
            if (annotation)
                return {
                    id: annotation.id,
                    locator: annotation.locator,
                    hasNote: Boolean(annotation.note),
                };
            return undefined;
        },
    };
}

function createAnnotationPanel(page: HTMLElement): AnnotationPanel | null {
    const actions = page.querySelector<HTMLElement>('.reader-actions');
    if (!actions) return null;
    const elements = createReaderPanel(page, {
        name: 'annotations',
        title: 'Highlights',
        closeLabel: 'Close highlights',
        icon: 'ink_highlighter',
    });

    const status = document.createElement('div');
    status.className = 'reader-annotations-status';
    status.setAttribute('aria-live', 'polite');
    const retry = document.createElement('button');
    retry.type = 'button';
    retry.className = 'detail-action reader-annotations-retry';
    retry.textContent = 'Try again';
    retry.hidden = true;

    const list = document.createElement('ol');
    list.className = 'reader-annotations-list';
    list.setAttribute('aria-label', 'Highlights');

    elements.panel.append(status, retry, list);
    actions.prepend(elements.toggle);

    return { ...elements, status, list, retry };
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
    controls.closeButton.addEventListener('click', () =>
        closeAnnotationPanel(page, controls, true),
    );
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
    controls.closeButton.focus();
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
    openNoteEditor: (id: number, navigate?: boolean) => void,
): void {
    controls.list.replaceChildren();
    controls.panel.dataset.readerAnnotationsCount = String(rows.length);

    for (const annotation of rows) {
        const item = document.createElement('li');
        item.className = 'reader-annotations-row';

        const button = document.createElement('button');
        button.className = 'reader-annotations-item';
        button.type = 'button';
        button.dataset.readerAnnotationId = String(annotation.id);
        button.style.setProperty('--annotation-color', ANNOTATION_COLORS[annotation.color]);

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
            openNoteEditor(annotation.id, true);
        });
        item.append(button);
        controls.list.append(item);
    }
}

function markActiveAnnotation(controls: AnnotationPanel | null, annotationID: number): void {
    if (!controls) return;
    for (const button of controls.list.querySelectorAll<HTMLButtonElement>(
        '[data-reader-annotation-id]',
    )) {
        const active = button.dataset.readerAnnotationId === String(annotationID);
        button.classList.toggle('active', active);
        if (active) button.setAttribute('aria-current', 'location');
        else button.removeAttribute('aria-current');
    }
}

function buildPopover(): HTMLElement {
    const root = document.createElement('div');
    root.className = 'reader-annotation-popover';
    root.hidden = true;
    root.setAttribute('role', 'dialog');
    root.setAttribute('aria-label', 'Highlight note');

    return root;
}

function showPopover(page: HTMLElement, popover: HTMLElement, anchor: AnnotationAnchor): void {
    popover.hidden = false;

    const frame = anchor.doc.defaultView?.frameElement as HTMLElement | null;
    const frameRect = frame?.getBoundingClientRect();
    const offsetX = frameRect?.left ?? 0;
    const offsetY = frameRect?.top ?? 0;
    const rangeRect = anchor.getBoundingClientRect();
    const targetCenter = rangeRect.left + rangeRect.width / 2 + offsetX;
    const targetTop = rangeRect.top + offsetY;
    const targetBottom = rangeRect.bottom + offsetY;

    const pageRect = page.getBoundingClientRect();
    const popoverRect = popover.getBoundingClientRect();
    const half = popoverRect.width / 2;
    const centerX = clamp(
        targetCenter,
        pageRect.left + POPOVER_MARGIN + half,
        pageRect.right - POPOVER_MARGIN - half,
    );
    let top = targetTop - POPOVER_GAP - popoverRect.height;
    if (top < pageRect.top + POPOVER_MARGIN) top = targetBottom + POPOVER_GAP;

    popover.style.left = `${centerX - pageRect.left}px`;
    popover.style.top = `${top - pageRect.top}px`;
    constrainPopover(page, popover);
}

function constrainPopover(page: HTMLElement, popover: HTMLElement): void {
    const pageRect = page.getBoundingClientRect();
    const rect = popover.getBoundingClientRect();
    const bottom = Math.min(pageRect.bottom, window.innerHeight) - POPOVER_MARGIN;
    if (rect.bottom > bottom) {
        popover.style.top = `${Math.max(POPOVER_MARGIN, bottom - rect.height - pageRect.top)}px`;
    }
}
