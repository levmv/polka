import { Overlayer } from 'foliate-js/overlayer.js';

import { createAnnotation, deleteAnnotation, fetchAnnotations, updateAnnotationNote } from '../api';
import { clamp } from '../dom';
import { iconElement } from '../icons';
import type { Annotation } from '../types';
import { focusReaderSurface } from './controls';
import type { FoliateAnnotation, FoliateViewElement } from './foliate-engine';
import type { ReaderSelectionPayload } from './selection';

const HIGHLIGHT_COLOR = '#f2d46b';
const POPOVER_GAP = 8;
const POPOVER_MARGIN = 8;

interface AnnotationOptions {
    onNavigate?: () => void;
}

interface AnnotationPopover {
    root: HTMLElement;
    quote: HTMLElement;
    note: HTMLTextAreaElement;
    status: HTMLElement;
    saveButton: HTMLButtonElement;
    deleteButton: HTMLButtonElement;
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
    createHighlight(payload: ReaderSelectionPayload): void;
}

export function wireAnnotations(
    page: HTMLElement,
    assetId: string,
    view: FoliateViewElement,
    options: AnnotationOptions = {},
): AnnotationController {
    const annotations = new Map<string, Annotation>();
    const sections = new Map<string, number>();
    const panel = createAnnotationPanel(page);
    const popover = buildPopover();
    page.append(popover.root);
    let activePopoverAnnotation: Annotation | null = null;
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
        renderAnnotationList(page, panel, sortedAnnotations(), view, options);
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

    view.addEventListener('draw-annotation', (event) => {
        const detail = (
            event as CustomEvent<{
                draw?: (func: unknown, options?: unknown) => void;
                annotation?: FoliateAnnotation;
            }>
        ).detail;
        if (!detail?.draw || detail.annotation?.kind !== 'highlight') return;
        detail.draw(Overlayer.highlight, { color: HIGHLIGHT_COLOR, padding: 1 });
    });
    view.addEventListener('show-annotation', (event) => {
        const detail = (event as CustomEvent<{ value?: string; index?: number; range?: Range }>)
            .detail;
        if (!detail?.value || !detail.range) return;
        const annotation = annotations.get(detail.value);
        if (!annotation) return;
        activePopoverAnnotation = annotation;
        markActiveAnnotation(panel, annotation.id);
        showPopover(page, view, popover, annotation, detail.range, detail.index);
    });
    view.addEventListener('create-overlay', (event) => {
        const index = (event as CustomEvent<{ index?: number }>).detail?.index;
        if (typeof index === 'number') renderSection(index);
    });
    view.addEventListener('relocate', hidePopover);

    document.addEventListener(
        'pointerdown',
        (event) => {
            const target = event.target;
            if (target instanceof Node && popover.root.contains(target)) return;
            hidePopover();
        },
        true,
    );
    window.addEventListener('resize', hidePopover);
    window.addEventListener('scroll', hidePopover, true);

    popover.note.addEventListener('input', () => {
        popover.status.textContent = '';
    });
    popover.saveButton.addEventListener('click', () => {
        if (!activePopoverAnnotation) return;
        const annotation = activePopoverAnnotation;
        setPopoverBusy(popover, true, 'Saving...');
        updateAnnotationNote(assetId, annotation.id, popover.note.value)
            .then((updated) => {
                replaceAnnotation(updated);
                if (activePopoverAnnotation?.id !== annotation.id) return;
                popover.note.value = updated.note || '';
                popover.status.textContent = 'Saved';
            })
            .catch((e) => {
                console.error('Failed to update annotation:', e);
                if (activePopoverAnnotation?.id === annotation.id) {
                    popover.status.textContent = 'Could not save';
                }
            })
            .finally(() => {
                if (activePopoverAnnotation?.id === annotation.id) {
                    setPopoverBusy(popover, false);
                }
            });
    });
    popover.deleteButton.addEventListener('click', () => {
        if (!activePopoverAnnotation) return;
        const annotation = activePopoverAnnotation;
        hidePopover();
        deleteAnnotation(assetId, annotation.id)
            .then(() => {
                annotations.delete(annotation.cfi);
                sections.delete(annotation.id);
                renderList();
                return view.deleteAnnotation?.(foliateAnnotation(annotation));
            })
            .catch((e) => console.error('Failed to delete annotation:', e));
    });

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
        createHighlight(payload: ReaderSelectionPayload): void {
            createAnnotation(assetId, {
                kind: 'highlight',
                cfi: payload.cfi,
                quote: payload.quote,
                context_before: payload.context_before,
                context_after: payload.context_after,
                color: 'yellow',
            })
                .then((annotation) => {
                    hidePopover();
                    const previous = annotations.get(annotation.cfi);
                    if (previous) {
                        sections.delete(previous.id);
                        void view.deleteAnnotation?.(foliateAnnotation(previous));
                    }
                    return renderAnnotation(annotation);
                })
                .catch((e) => console.error('Failed to create annotation:', e));
        },
    };
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
    toggle.append(iconElement('bookmark'));

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
    if (restoreFocus) focusReaderSurface(page);
}

function renderAnnotationList(
    page: HTMLElement,
    controls: AnnotationPanel,
    rows: Annotation[],
    view: FoliateViewElement,
    options: AnnotationOptions,
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
            const target = foliateAnnotation(annotation);
            const navigate = view.showAnnotation
                ? view.showAnnotation(target)
                : view.goTo(annotation.cfi);
            Promise.resolve(navigate).catch((e) =>
                console.error('Failed to navigate annotation:', e),
            );
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
    root.setAttribute('aria-label', 'Highlight');

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

    const actions = document.createElement('div');
    actions.className = 'reader-annotation-actions';

    const saveButton = document.createElement('button');
    saveButton.className = 'reader-annotation-action reader-annotation-action--primary';
    saveButton.type = 'button';
    saveButton.append(iconElement('check'));
    const saveLabel = document.createElement('span');
    saveLabel.textContent = 'Save';
    saveButton.append(saveLabel);

    const deleteButton = document.createElement('button');
    deleteButton.className = 'reader-annotation-action';
    deleteButton.type = 'button';
    deleteButton.append(iconElement('delete'));
    const deleteLabel = document.createElement('span');
    deleteLabel.textContent = 'Delete';
    deleteButton.append(deleteLabel);

    actions.append(saveButton, deleteButton);
    root.append(quote, note, status, actions);
    return { root, quote, note, status, saveButton, deleteButton };
}

function setPopoverBusy(popover: AnnotationPopover, busy: boolean, status?: string): void {
    popover.saveButton.disabled = busy;
    popover.deleteButton.disabled = busy;
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
