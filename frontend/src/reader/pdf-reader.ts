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

import { clamp } from '../dom';
import { type AnnotationController, wireAnnotations } from './annotations';
import { PagedReader, type PagedReaderOptions, pagedReaderElements } from './paged-reader';
import { pdfAnnotationSurface } from './pdf-annotations';
import { wirePDFOutline } from './pdf-outline';
import { type PDFSearchController, wirePDFSearch } from './pdf-search';
import { type ReaderSelectionController, wireReaderSelection } from './selection';

const PDF_RESOURCE_ROOT = '/static/pdfjs';

export async function initPDFReader(
    page: HTMLElement,
    assetId: number,
    options: PagedReaderOptions = {},
): Promise<void> {
    const readURL = page.dataset.readerUrl;
    const elements = pagedReaderElements(page);
    if (!readURL || !elements) return;

    GlobalWorkerOptions.workerSrc = '/static/pdf.worker.js';
    const reader = new PDFReader(page, assetId, readURL, elements, options);
    await reader.open();
}

class PDFReader extends PagedReader {
    protected readonly label = 'PDF';
    private document: PDFDocumentProxy | null = null;
    private loadingTask: PDFDocumentLoadingTask | null = null;
    private pageProxy: PDFPageProxy | null = null;
    private renderTask: RenderTask | null = null;
    private textLayer: TextLayer | null = null;
    private searchController: PDFSearchController | null = null;
    private annotations: AnnotationController | null = null;
    private annotationSurface: ReturnType<typeof pdfAnnotationSurface> | null = null;
    private selection: ReaderSelectionController | null = null;
    private annotationID = 0;

    protected async load(): Promise<number> {
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
            // Bundled WOFF2 substitutes use the browser's FontFace loader.
            useSystemFonts: true,
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

        this.document = await this.loadingTask.promise;
        return this.document.numPages;
    }

    protected async setup(): Promise<void> {
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
        this.annotationID = Number(
            new URLSearchParams(window.location.hash.slice(1)).get('annotation'),
        );
        const annotationPage = this.annotations.location(this.annotationID)?.page;
        if (annotationPage) this.pageNumber = clamp(annotationPage, 1, this.pageCount);
        this.searchController = wirePDFSearch(this.root, this.document!, {
            currentPage: () => this.pageNumber,
            navigateTo: (pageNumber) => this.navigateTo(pageNumber),
        });
        wirePDFOutline(this.root, this.document!, {
            navigateTo: (pageNumber) => this.navigateTo(pageNumber),
        });
    }

    protected async renderPage(generation: number): Promise<void> {
        if (!this.document || this.pageCount < 1) return;

        const page = await this.document.getPage(this.pageNumber);
        if (generation !== this.renderGeneration) {
            page.cleanup();
            return;
        }
        this.pageProxy = page;

        const unscaled = page.getViewport({ scale: 1 });
        const size = this.pageSize(unscaled.width, unscaled.height);
        const viewport = page.getViewport({ scale: size.width / unscaled.width });
        const outputScale = size.outputScale;

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
        this.searchController?.markCurrentPage(this.pageNumber, textLayer);
        this.annotationSurface?.setPage(this.pageNumber, viewport);
        if (this.annotationID) {
            this.annotationSurface?.reveal(this.annotationID);
            this.annotationID = 0;
        }
    }

    protected releasePage(): void {
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

    protected resume(): void {
        void this.annotations?.load();
    }

    protected beforeClose(): Promise<boolean> {
        return this.annotations?.savePendingEdits() ?? Promise.resolve(true);
    }

    protected destroy(): void {
        void this.loadingTask?.destroy();
    }
}
