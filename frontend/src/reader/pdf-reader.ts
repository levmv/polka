import {
    AnnotationMode,
    GlobalWorkerOptions,
    getDocument,
    type PDFDocumentLoadingTask,
    type PDFDocumentProxy,
    type PDFPageProxy,
    type RenderTask,
    TextLayer,
} from 'pdfjs-dist/legacy/build/pdf.mjs';

import { fetchReaderState, touchReaderState } from '../api';
import { clamp } from '../dom';
import type { Locator, ReaderState } from '../types';
import { createReadingActivity } from './activity';
import { type AnnotationController, wireAnnotations } from './annotations';
import {
    closeReader,
    focusReaderSurface,
    revealChrome,
    showReaderError,
    toggleReaderChrome,
} from './chrome';
import { type ReaderLifecycle, wireReaderLifecycle } from './lifecycle';
import { pdfAnnotationSurface } from './pdf-annotations';
import { wirePDFOutline } from './pdf-outline';
import { type PDFSearchController, wirePDFSearch } from './pdf-search';
import { type ReaderSelectionController, wireReaderSelection } from './selection';
import { createReaderStateSaver, type ReaderPosition, type ReaderStateSaver } from './state-saver';

const PDF_RESOURCE_ROOT = '/static/pdfjs';
const MAX_CANVAS_PIXELS = 16_000_000;
const MAX_OUTPUT_SCALE = 2;
const PDF_ZOOM_KEY = 'polka-pdf-zoom';
const MIN_ZOOM = 0.5;
const MAX_ZOOM = 3;
const ZOOM_STEP = 1.2;
const SAVE_DELAY_MS = 600;
const RESIZE_DELAY_MS = 120;
const MAX_CLICK_MOVEMENT = 6;

interface PDFReaderOptions {
    onStateSaved?: (state: ReaderState) => void;
}

interface PDFReaderElements {
    stage: HTMLElement;
    page: HTMLElement;
    canvas: HTMLCanvasElement;
    textLayer: HTMLElement;
    loading: HTMLElement;
    previous: HTMLButtonElement;
    next: HTMLButtonElement;
    pageInput: HTMLInputElement;
    pageTotal: HTMLElement;
    progress: HTMLElement;
    zoomOut: HTMLButtonElement;
    zoomIn: HTMLButtonElement;
    zoomFit: HTMLButtonElement;
}

interface PDFPointerGesture {
    pointerId: number;
    clientX: number;
    clientY: number;
}

export async function initPDFReader(
    page: HTMLElement,
    assetId: number,
    options: PDFReaderOptions = {},
): Promise<void> {
    const readURL = page.dataset.readerUrl;
    const elements = pdfReaderElements(page);
    if (!readURL || !elements) return;

    GlobalWorkerOptions.workerSrc = '/static/pdf.worker.js';
    const reader = new PDFReader(page, assetId, readURL, elements, options);
    await reader.open();
}

class PDFReader {
    private readonly lifecycle: ReaderLifecycle;
    private document: PDFDocumentProxy | null = null;
    private loadingTask: PDFDocumentLoadingTask | null = null;
    private pageProxy: PDFPageProxy | null = null;
    private renderTask: RenderTask | null = null;
    private textLayer: TextLayer | null = null;
    private pageNumber = 1;
    private pageCount = 0;
    private zoom = loadPDFZoom();
    private renderGeneration = 0;
    private saveTimer: number | undefined;
    private resizeTimer: number | undefined;
    private readonly stateSaver: ReaderStateSaver;
    private searchController: PDFSearchController | null = null;
    private pointerGesture: PDFPointerGesture | null = null;
    private annotations: AnnotationController | null = null;
    private annotationSurface: ReturnType<typeof pdfAnnotationSurface> | null = null;
    private selection: ReaderSelectionController | null = null;

    constructor(
        private readonly root: HTMLElement,
        private readonly assetId: number,
        private readonly readURL: string,
        private readonly elements: PDFReaderElements,
        private readonly options: PDFReaderOptions,
    ) {
        this.stateSaver = createReaderStateSaver(assetId, {
            ...options,
            restorePosition: async (state) => {
                window.clearTimeout(this.saveTimer);
                const previousPage = this.pageNumber;
                const generation = this.renderGeneration + 1;
                this.pageNumber = storedPDFPage(state, this.pageCount);
                this.updateControls();
                try {
                    await this.renderCurrentPage();
                } catch (error) {
                    if (generation === this.renderGeneration) {
                        this.pageNumber = previousPage;
                        this.updateControls();
                        try {
                            await this.renderCurrentPage();
                        } catch {
                            showReaderError(this.root, 'Could not open this PDF.');
                        }
                    }
                    throw error;
                }
            },
        });
        this.lifecycle = wireReaderLifecycle(
            root,
            elements.stage,
            this.stateSaver,
            createReadingActivity(assetId),
            { onResume: () => void this.annotations?.load() },
        );
    }

    async open(): Promise<void> {
        const statePromise = fetchReaderState(this.assetId).catch((error) => {
            console.error('Failed to fetch PDF reader state:', error);
            return null;
        });

        this.loadingTask = getDocument({
            url: this.readURL,
            withCredentials: true,
            cMapUrl: `${PDF_RESOURCE_ROOT}/cmaps/`,
            cMapPacked: true,
            iccUrl: `${PDF_RESOURCE_ROOT}/iccs/`,
            standardFontDataUrl: `${PDF_RESOURCE_ROOT}/standard_fonts/`,
            wasmUrl: `${PDF_RESOURCE_ROOT}/wasm/`,
            useWasm: true,
            useWorkerFetch: true,
            enableXfa: false,
            disableRange: false,
            // Range loading without speculative auto-fetch keeps large files
            // bounded.
            disableStream: true,
            disableAutoFetch: true,
            rangeChunkSize: 256 * 1024,
            // One visible page is enough for this reader. Avoid browser-specific
            // offscreen/image-decoder paths until the iPad corpus proves them.
            isOffscreenCanvasSupported: false,
            isImageDecoderSupported: false,
            canvasMaxAreaInBytes: 64 * 1024 * 1024,
        });

        const state = await statePromise;
        this.stateSaver.initialize(state);
        this.document = await this.loadingTask.promise;
        this.pageCount = this.document.numPages;
        this.pageNumber = storedPDFPage(state, this.pageCount);
        this.annotationSurface = pdfAnnotationSurface(
            this.elements.page,
            this.elements.textLayer,
            (number) => this.navigateTo(number),
        );
        this.annotations = wireAnnotations(this.root, this.assetId, this.annotationSurface, {
            onShowActions: (target) => this.selection?.showAnnotationActions(target),
        });
        this.selection = wireReaderSelection(this.root, {
            locate: (range) => this.annotationSurface?.locate(range),
            containsRange: (range) =>
                this.elements.textLayer.contains(range.startContainer) &&
                this.elements.textLayer.contains(range.endContainer),
            contextRoot: this.elements.textLayer,
            onHighlightSelection: this.annotations.createHighlight,
            onNoteSelection: (payload) => this.annotations?.createHighlight(payload, true),
            onEditAnnotation: this.annotations.editNote,
            onDeleteAnnotation: this.annotations.deleteHighlight,
            annotationAt: this.annotations.annotationAt,
        });
        this.selection.attachDocument(document);
        await this.annotations.load();
        const annotationID = Number(
            new URLSearchParams(window.location.hash.slice(1)).get('annotation'),
        );
        const annotationPage = this.annotations.location(annotationID)?.page;
        if (annotationPage) this.pageNumber = clamp(annotationPage, 1, this.pageCount);
        this.wireControls();
        this.searchController = wirePDFSearch(this.root, this.document, {
            currentPage: () => this.pageNumber,
            navigateTo: (pageNumber) => this.navigateTo(pageNumber),
        });
        wirePDFOutline(this.root, this.document, {
            navigateTo: (pageNumber) => this.navigateTo(pageNumber),
        });
        this.updateControls();
        await this.renderCurrentPage();
        if (annotationID) this.annotationSurface.reveal(annotationID);

        this.elements.loading.remove();
        this.elements.stage.dataset.readerReady = 'true';
        this.root.classList.add('reader-ready');
        this.elements.stage.focus({ preventScroll: true });
        revealChrome(this.root);
        this.lifecycle.start();
        void this.stateSaver.flush();

        touchReaderState(this.assetId)
            .then((saved) => this.options.onStateSaved?.(saved))
            .catch((error) => {
                console.error('Failed to update PDF reader state:', error);
            });
    }

    private wireControls(): void {
        this.root.querySelector('.reader-close')?.addEventListener('click', (event) => {
            event.preventDefault();
            closeReader(
                this.root,
                () => this.annotations?.savePendingEdits() ?? Promise.resolve(true),
            );
        });
        this.elements.previous.addEventListener('click', () => {
            void this.navigateTo(this.pageNumber - 1);
        });
        this.elements.next.addEventListener('click', () => {
            void this.navigateTo(this.pageNumber + 1);
        });
        this.elements.pageInput.addEventListener('change', () => {
            void this.navigateFromInput();
        });
        this.elements.pageInput.addEventListener('keydown', (event) => {
            if (event.key !== 'Enter') return;
            event.preventDefault();
            this.elements.pageInput.blur();
            void this.navigateFromInput();
        });

        this.elements.zoomOut.addEventListener('click', () => {
            void this.setZoom(this.zoom / ZOOM_STEP);
        });
        this.elements.zoomIn.addEventListener('click', () => {
            void this.setZoom(this.zoom * ZOOM_STEP);
        });
        this.elements.zoomFit.addEventListener('click', () => {
            void this.setZoom(1);
        });
        this.elements.stage.addEventListener('pointerdown', this.handlePointerDown);
        this.elements.stage.addEventListener('pointerup', this.handlePointerUp);
        this.elements.stage.addEventListener('pointercancel', this.handlePointerCancel);

        window.addEventListener('keydown', this.handleKeydown, true);
        window.addEventListener('resize', this.handleResize);
    }

    private readonly handleKeydown = (event: KeyboardEvent): void => {
        const target = event.target instanceof Element ? event.target : null;
        if (target?.closest('a, button, input, textarea, select, [contenteditable="true"]')) {
            return;
        }

        if (event.key === 'Escape') {
            if (
                this.root.querySelector(
                    '.reader-annotation-popover:not([hidden]), #reader-annotations-panel:not([hidden])',
                )
            )
                return;
            event.preventDefault();
            if (this.root.classList.contains('reader-chrome-hidden')) {
                revealChrome(this.root, false);
                focusReaderSurface(this.root);
                return;
            }
            closeReader(
                this.root,
                () => this.annotations?.savePendingEdits() ?? Promise.resolve(true),
            );
        } else if (event.key === 'ArrowLeft' || event.key === 'PageUp') {
            event.preventDefault();
            void this.navigateTo(this.pageNumber - 1);
        } else if (
            event.key === 'ArrowRight' ||
            event.key === 'PageDown' ||
            event.code === 'Space' ||
            event.key === ' ' ||
            event.key === 'Spacebar'
        ) {
            event.preventDefault();
            const direction = event.shiftKey ? -1 : 1;
            void this.navigateTo(this.pageNumber + direction);
        } else if (event.key === 'Home') {
            event.preventDefault();
            void this.navigateTo(1);
        } else if (event.key === 'End') {
            event.preventDefault();
            void this.navigateTo(this.pageCount);
        }
    };

    private readonly handlePointerDown = (event: PointerEvent): void => {
        this.pointerGesture = {
            pointerId: event.pointerId,
            clientX: event.clientX,
            clientY: event.clientY,
        };
    };

    private readonly handlePointerUp = (event: PointerEvent): void => {
        const gesture =
            this.pointerGesture?.pointerId === event.pointerId ? this.pointerGesture : null;
        this.pointerGesture = null;
        if (!gesture) return;
        const moved =
            Math.hypot(event.clientX - gesture.clientX, event.clientY - gesture.clientY) >
            MAX_CLICK_MOVEMENT;
        if (moved) return;

        const target = event.target instanceof Element ? event.target : null;
        if (
            target?.closest('a, button, input, textarea, select, [contenteditable="true"]') ||
            hasPDFTextSelection()
        ) {
            return;
        }

        const isMouse = event.pointerType === 'mouse';
        if (isMouse && this.root.classList.contains('reader-chrome-hidden')) {
            revealChrome(this.root, false);
        } else {
            toggleReaderChrome(this.root);
        }
        focusReaderSurface(this.root);
    };

    private readonly handlePointerCancel = (): void => {
        this.pointerGesture = null;
    };

    private readonly handleResize = (): void => {
        window.clearTimeout(this.resizeTimer);
        this.resizeTimer = window.setTimeout(() => {
            void this.renderCurrentPage();
        }, RESIZE_DELAY_MS);
    };

    private async navigateFromInput(): Promise<void> {
        const requested = Number.parseInt(this.elements.pageInput.value, 10);
        if (!Number.isFinite(requested)) {
            this.updateControls();
            return;
        }
        await this.navigateTo(requested);
    }

    private async navigateTo(pageNumber: number): Promise<void> {
        const nextPage = clamp(Math.round(pageNumber), 1, this.pageCount);
        if (nextPage === this.pageNumber) {
            this.updateControls();
            return;
        }

        this.pageNumber = nextPage;
        this.lifecycle.markUserNavigation();
        this.updateControls();
        this.scheduleSave();
        await this.renderCurrentPage();
    }

    private async setZoom(zoom: number): Promise<void> {
        const normalized = clamp(zoom, MIN_ZOOM, MAX_ZOOM);
        if (Math.abs(normalized - this.zoom) < 0.001) return;
        this.zoom = normalized;
        try {
            localStorage.setItem(PDF_ZOOM_KEY, String(this.zoom));
        } catch {
            // Keep the zoom in memory when browser storage is unavailable.
        }
        this.updateControls();
        revealChrome(this.root);
        await this.renderCurrentPage();
    }

    private async renderCurrentPage(): Promise<void> {
        if (!this.document || this.pageCount < 1) return;
        const generation = ++this.renderGeneration;
        this.releasePage();
        delete this.elements.page.dataset.pdfRenderedPage;

        const page = await this.document.getPage(this.pageNumber);
        if (generation !== this.renderGeneration) {
            page.cleanup();
            return;
        }
        this.pageProxy = page;

        const unscaled = page.getViewport({ scale: 1 });
        const availableWidth = Math.max(this.elements.stage.clientWidth - 32, 1);
        const availableHeight = Math.max(this.elements.stage.clientHeight - 32, 1);
        const fitScale = Math.min(
            availableWidth / unscaled.width,
            availableHeight / unscaled.height,
        );
        const viewport = page.getViewport({ scale: fitScale * this.zoom });
        const outputScale = boundedOutputScale(viewport.width, viewport.height);

        const canvas = this.elements.canvas;
        canvas.width = Math.max(1, Math.floor(viewport.width * outputScale));
        canvas.height = Math.max(1, Math.floor(viewport.height * outputScale));
        canvas.style.width = `${viewport.width}px`;
        canvas.style.height = `${viewport.height}px`;
        this.elements.page.style.width = `${viewport.width}px`;
        this.elements.page.style.height = `${viewport.height}px`;
        this.elements.page.hidden = false;

        const context = canvas.getContext('2d', { alpha: false });
        if (!context) throw new Error('Could not allocate a PDF canvas.');
        // A newly allocated opaque canvas is black until PDF.js paints it.
        // Prime it white so zooming and rotation never flash a black page.
        context.fillStyle = '#ffffff';
        context.fillRect(0, 0, canvas.width, canvas.height);
        const transform = outputScale === 1 ? undefined : [outputScale, 0, 0, outputScale, 0, 0];
        const renderTask = page.render({
            canvas,
            canvasContext: context,
            viewport,
            transform,
            annotationMode: AnnotationMode.DISABLE,
            background: '#ffffff',
        });
        this.renderTask = renderTask;
        try {
            await renderTask.promise;
        } catch (error) {
            if (generation !== this.renderGeneration) {
                page.cleanup();
                return;
            }
            throw error;
        } finally {
            if (this.renderTask === renderTask) this.renderTask = null;
        }
        if (generation !== this.renderGeneration) {
            page.cleanup();
            return;
        }

        const textContent = await page
            .getTextContent({ includeMarkedContent: true })
            .catch((error) => {
                if (generation !== this.renderGeneration) return null;
                throw error;
            });
        if (!textContent || generation !== this.renderGeneration) {
            page.cleanup();
            return;
        }
        this.elements.textLayer.replaceChildren();
        const textLayer = new TextLayer({
            textContentSource: textContent,
            container: this.elements.textLayer,
            viewport,
        });
        this.textLayer = textLayer;
        // Use explicit sizes for browsers without CSS round(). TextLayer uses
        // unrotated dimensions; CSS rotates the layer to match the canvas.
        const sideways = viewport.rotation % 180 !== 0;
        this.elements.textLayer.style.width = `${sideways ? viewport.height : viewport.width}px`;
        this.elements.textLayer.style.height = `${sideways ? viewport.width : viewport.height}px`;
        this.elements.textLayer.style.setProperty(
            '--total-scale-factor',
            String(viewport.scale * viewport.userUnit),
        );
        const minFontSize = Number.parseFloat(
            this.elements.textLayer.style.getPropertyValue('--min-font-size'),
        );
        this.elements.textLayer.style.setProperty(
            '--min-font-size-inv',
            String(minFontSize > 0 ? 1 / minFontSize : 1),
        );
        try {
            await textLayer.render();
        } catch (error) {
            if (generation !== this.renderGeneration) {
                page.cleanup();
                return;
            }
            throw error;
        } finally {
            if (this.textLayer === textLayer) this.textLayer = null;
        }
        if (generation !== this.renderGeneration) {
            page.cleanup();
            return;
        }

        page.cleanup();
        this.pageProxy = null;
        this.elements.page.dataset.pdfRenderedPage = String(this.pageNumber);
        this.searchController?.markCurrentPage(this.pageNumber, textLayer);
        this.annotationSurface?.setPage(this.pageNumber, viewport);
    }

    private releasePage(): void {
        this.selection?.relocate();
        this.annotationSurface?.clearPage();
        this.searchController?.clearPage();
        this.renderTask?.cancel();
        this.renderTask = null;
        this.textLayer?.cancel();
        this.textLayer = null;
        this.pageProxy?.cleanup();
        this.pageProxy = null;
        this.elements.textLayer.replaceChildren();
        this.elements.canvas.width = 0;
        this.elements.canvas.height = 0;
    }

    private updateControls(): void {
        this.elements.previous.disabled = this.pageNumber <= 1;
        this.elements.next.disabled = this.pageNumber >= this.pageCount;
        this.elements.pageInput.value = String(this.pageNumber);
        this.elements.pageInput.max = String(this.pageCount);
        this.elements.pageTotal.textContent = String(this.pageCount);
        this.elements.progress.textContent = `${this.pageNumber} / ${this.pageCount}`;
        this.elements.zoomOut.disabled = this.zoom <= MIN_ZOOM;
        this.elements.zoomIn.disabled = this.zoom >= MAX_ZOOM;
        this.elements.zoomFit.disabled = Math.abs(this.zoom - 1) < 0.001;
        this.elements.zoomFit.textContent =
            Math.abs(this.zoom - 1) < 0.001 ? 'Fit' : `${Math.round(this.zoom * 100)}%`;
        this.root.dataset.readerPdfZoom = this.zoom.toFixed(3);
    }

    private scheduleSave(): void {
        const locator: Locator = {
            page: this.pageNumber,
        };
        const progress = this.pageCount > 0 ? this.pageNumber / this.pageCount : 0;
        this.stateSaver.queue({ progress, locator });
        window.clearTimeout(this.saveTimer);
        this.saveTimer = window.setTimeout(() => {
            void this.flushPending();
        }, SAVE_DELAY_MS);
    }

    private flushPending(saveOptions: { keepalive?: boolean } = {}): Promise<void> {
        window.clearTimeout(this.saveTimer);
        this.saveTimer = undefined;
        return this.stateSaver.flush(saveOptions);
    }
}

function pdfReaderElements(page: HTMLElement): PDFReaderElements | null {
    const stage = page.querySelector<HTMLElement>('[data-pdf-stage]');
    const pdfPage = page.querySelector<HTMLElement>('[data-pdf-page]');
    const canvas = page.querySelector<HTMLCanvasElement>('[data-pdf-canvas]');
    const textLayer = page.querySelector<HTMLElement>('[data-pdf-text-layer]');
    const loading = page.querySelector<HTMLElement>('[data-reader-loading]');
    const previous = page.querySelector<HTMLButtonElement>('[data-pdf-previous]');
    const next = page.querySelector<HTMLButtonElement>('[data-pdf-next]');
    const pageInput = page.querySelector<HTMLInputElement>('[data-pdf-page-input]');
    const pageTotal = page.querySelector<HTMLElement>('[data-pdf-page-total]');
    const progress = page.querySelector<HTMLElement>('[data-reader-progress]');
    const zoomOut = page.querySelector<HTMLButtonElement>('[data-pdf-zoom-out]');
    const zoomIn = page.querySelector<HTMLButtonElement>('[data-pdf-zoom-in]');
    const zoomFit = page.querySelector<HTMLButtonElement>('[data-pdf-zoom-fit]');
    if (
        !stage ||
        !pdfPage ||
        !canvas ||
        !textLayer ||
        !loading ||
        !previous ||
        !next ||
        !pageInput ||
        !pageTotal ||
        !progress ||
        !zoomOut ||
        !zoomIn ||
        !zoomFit
    ) {
        return null;
    }
    return {
        stage,
        page: pdfPage,
        canvas,
        textLayer,
        loading,
        previous,
        next,
        pageInput,
        pageTotal,
        progress,
        zoomOut,
        zoomIn,
        zoomFit,
    };
}

function storedPDFPage(state: ReaderPosition | null, pageCount: number): number {
    if (!state || pageCount < 1) return 1;
    if (typeof state.locator.page === 'number') {
        return clamp(Math.round(state.locator.page), 1, pageCount);
    }
    if (state.progress > 0) {
        return clamp(Math.ceil(state.progress * pageCount), 1, pageCount);
    }
    return 1;
}

function loadPDFZoom(): number {
    try {
        const zoom = Number(localStorage.getItem(PDF_ZOOM_KEY));
        if (Number.isFinite(zoom) && zoom >= MIN_ZOOM && zoom <= MAX_ZOOM) return zoom;
    } catch {
        // Use the default when browser storage is unavailable.
    }
    return 1;
}

function hasPDFTextSelection(): boolean {
    const selection = window.getSelection();
    return Boolean(selection && !selection.isCollapsed && selection.toString().trim());
}

function boundedOutputScale(width: number, height: number): number {
    const pixelRatio = Math.min(window.devicePixelRatio || 1, MAX_OUTPUT_SCALE);
    const area = Math.max(width * height, 1);
    return Math.max(0.25, Math.min(pixelRatio, Math.sqrt(MAX_CANVAS_PIXELS / area)));
}
