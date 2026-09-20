import { fetchReaderPosition, touchReader } from '../api';
import { clamp } from '../dom';
import { showToast } from '../toast';
import type { Locator, ReaderPosition, ReaderPositionSaveResult } from '../types';
import { createReadingActivity } from './activity';
import {
    closeReader,
    focusReaderSurface,
    revealChrome,
    showReaderError,
    toggleReaderChrome,
} from './chrome';
import { type ReaderLifecycle, wireReaderLifecycle } from './lifecycle';
import { createPositionSaver, type PositionSaver } from './position-saver';

const MAX_CANVAS_PIXELS = 16_000_000;
const MAX_OUTPUT_SCALE = 2;
const MIN_ZOOM = 0.5;
const MAX_ZOOM = 3;
const ZOOM_STEP = 1.2;
const SAVE_DELAY_MS = 600;
const RESIZE_DELAY_MS = 120;
const MAX_CLICK_MOVEMENT = 6;

export interface PagedReaderOptions {
    onPositionSaved?: (result: ReaderPositionSaveResult) => void;
}

export interface PagedReaderElements {
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

interface PointerGesture {
    pointerId: number;
    clientX: number;
    clientY: number;
}

// Page navigation, zoom and reading state are shared by PDF and DjVu.
export abstract class PagedReader {
    protected pageNumber = 1;
    protected pageCount = 0;
    protected zoom: number;
    protected renderGeneration = 0;
    protected abstract readonly label: string;
    private readonly lifecycle: ReaderLifecycle;
    private readonly positionSaver: PositionSaver;
    private saveTimer: number | undefined;
    private resizeTimer: number | undefined;
    private pointerGesture: PointerGesture | null = null;
    private readonly zoomKey: string;
    private navigationPending = false;

    constructor(
        protected readonly root: HTMLElement,
        protected readonly assetId: number,
        protected readonly readURL: string,
        protected readonly elements: PagedReaderElements,
        options: PagedReaderOptions,
    ) {
        this.zoomKey = `polka-${root.dataset.readerFormat}-zoom`;
        this.zoom = loadZoom(this.zoomKey);
        this.positionSaver = createPositionSaver(assetId, {
            ...options,
            restorePosition: async (state) => {
                window.clearTimeout(this.saveTimer);
                this.navigationPending = false;
                const previousPage = this.pageNumber;
                const generation = this.renderGeneration + 1;
                this.pageNumber = storedPage(state, this.pageCount);
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
                            showReaderError(this.root, `Could not open this ${this.label}.`);
                        }
                    }
                    throw error;
                }
            },
        });
        this.lifecycle = wireReaderLifecycle(
            root,
            elements.stage,
            this.positionSaver,
            createReadingActivity(assetId),
            { onResume: () => this.resume() },
        );
    }

    async open(): Promise<void> {
        window.addEventListener('pagehide', (event) => {
            if (!event.persisted) {
                this.renderGeneration++;
                window.clearTimeout(this.saveTimer);
                window.clearTimeout(this.resizeTimer);
                this.releasePage();
                this.destroy();
            }
        });
        try {
            const [state, count] = await Promise.all([
                fetchReaderPosition(this.assetId).catch((error) => {
                    console.error('Failed to fetch reading position:', error);
                    return null;
                }),
                this.load(),
            ]);
            this.positionSaver.initialize(state);
            this.pageCount = count;
            this.pageNumber = storedPage(state, count);
            await this.setup();
            this.wireControls();
            this.updateControls();
            await this.renderCurrentPage();
            this.elements.loading.remove();
            this.elements.stage.dataset.readerReady = 'true';
            this.root.classList.add('reader-ready');
            this.elements.stage.focus({ preventScroll: true });
            revealChrome(this.root);
            this.lifecycle.start();
            void touchReader(this.assetId).catch((error) => {
                console.error('Failed to record book opening:', error);
            });
        } catch (error) {
            this.releasePage();
            this.destroy();
            throw error;
        }
    }

    protected abstract load(): Promise<number>;
    protected abstract renderPage(generation: number): Promise<void>;
    protected abstract releasePage(): void;
    protected abstract destroy(): void;
    protected async setup(): Promise<void> {}
    protected resume(): void {}
    protected async beforeClose(): Promise<boolean> {
        return true;
    }

    private wireControls(): void {
        this.root.querySelector('.reader-close')?.addEventListener('click', (event) => {
            event.preventDefault();
            closeReader(this.root, () => this.beforeClose());
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
                    '.reader-annotation-popover:not([hidden]), #reader-annotations-panel:not([hidden]), #reader-search-panel:not([hidden]), #reader-toc-panel:not([hidden])',
                )
            )
                return;
            event.preventDefault();
            if (this.root.classList.contains('reader-chrome-hidden')) {
                revealChrome(this.root, false);
                focusReaderSurface(this.root);
                return;
            }
            closeReader(this.root, () => this.beforeClose());
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
            hasTextSelection()
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
            void this.tryRender();
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

    protected async navigateTo(pageNumber: number): Promise<void> {
        const nextPage = clamp(Math.round(pageNumber), 1, this.pageCount);
        if (nextPage === this.pageNumber) {
            this.updateControls();
            return;
        }

        this.pageNumber = nextPage;
        this.navigationPending = true;
        this.lifecycle.markUserNavigation();
        this.updateControls();
        await this.tryRender();
    }

    private async setZoom(zoom: number): Promise<void> {
        const normalized = clamp(zoom, MIN_ZOOM, MAX_ZOOM);
        if (Math.abs(normalized - this.zoom) < 0.001) return;
        this.zoom = normalized;
        try {
            localStorage.setItem(this.zoomKey, String(this.zoom));
        } catch {
            // Keep the zoom in memory when browser storage is unavailable.
        }
        this.updateControls();
        revealChrome(this.root);
        await this.tryRender();
    }

    private async tryRender(): Promise<void> {
        try {
            await this.renderCurrentPage();
        } catch (error) {
            console.error(`Failed to render ${this.label} page:`, error);
            showToast('Could not open this page.', {
                type: 'error',
                action: {
                    label: 'Retry',
                    onClick: () => void this.tryRender(),
                },
            });
        }
    }

    private async renderCurrentPage(): Promise<void> {
        if (this.pageCount < 1) return;
        const generation = ++this.renderGeneration;
        this.releasePage();
        delete this.elements.page.dataset.renderedPage;
        try {
            await this.renderPage(generation);
        } catch (error) {
            if (generation !== this.renderGeneration) return;
            throw error;
        }
        if (generation !== this.renderGeneration) return;
        this.elements.page.dataset.renderedPage = String(this.pageNumber);
        // A zoom or resize can replace the render started by a page turn.
        // Save once that page is visible, whichever render completed it.
        if (this.navigationPending) {
            this.navigationPending = false;
            this.scheduleSave();
        }
    }

    protected pageSize(
        width: number,
        height: number,
    ): { width: number; height: number; outputScale: number } {
        const scale =
            Math.min(
                Math.max(this.elements.stage.clientWidth - 32, 1) / width,
                Math.max(this.elements.stage.clientHeight - 32, 1) / height,
            ) * this.zoom;
        width *= scale;
        height *= scale;
        return { width, height, outputScale: boundedOutputScale(width, height) };
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
        this.root.dataset.readerZoom = this.zoom.toFixed(3);
    }

    private scheduleSave(): void {
        const locator: Locator = {
            page: this.pageNumber,
        };
        const progress = this.pageCount > 0 ? this.pageNumber / this.pageCount : 0;
        this.positionSaver.queue({ progress, locator });
        window.clearTimeout(this.saveTimer);
        this.saveTimer = window.setTimeout(() => {
            this.saveTimer = undefined;
            void this.positionSaver.flush();
        }, SAVE_DELAY_MS);
    }
}

export function pagedReaderElements(page: HTMLElement): PagedReaderElements | null {
    const stage = page.querySelector<HTMLElement>('[data-page-stage]');
    const rasterPage = page.querySelector<HTMLElement>('[data-page-surface]');
    const canvas = page.querySelector<HTMLCanvasElement>('[data-page-canvas]');
    const textLayer = page.querySelector<HTMLElement>('[data-page-text-layer]');
    const loading = page.querySelector<HTMLElement>('[data-reader-loading]');
    const previous = page.querySelector<HTMLButtonElement>('[data-page-previous]');
    const next = page.querySelector<HTMLButtonElement>('[data-page-next]');
    const pageInput = page.querySelector<HTMLInputElement>('[data-page-input]');
    const pageTotal = page.querySelector<HTMLElement>('[data-page-total]');
    const progress = page.querySelector<HTMLElement>('[data-reader-progress]');
    const zoomOut = page.querySelector<HTMLButtonElement>('[data-page-zoom-out]');
    const zoomIn = page.querySelector<HTMLButtonElement>('[data-page-zoom-in]');
    const zoomFit = page.querySelector<HTMLButtonElement>('[data-page-zoom-fit]');
    if (
        !stage ||
        !rasterPage ||
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
        page: rasterPage,
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

function storedPage(state: ReaderPosition | null, pageCount: number): number {
    if (!state || pageCount < 1) return 1;
    if (typeof state.locator.page === 'number') {
        return clamp(Math.round(state.locator.page), 1, pageCount);
    }
    if (state.progress > 0) {
        return clamp(Math.ceil(state.progress * pageCount), 1, pageCount);
    }
    return 1;
}

function loadZoom(key: string): number {
    try {
        const zoom = Number(localStorage.getItem(key));
        if (Number.isFinite(zoom) && zoom >= MIN_ZOOM && zoom <= MAX_ZOOM) return zoom;
    } catch {
        // Use the default when browser storage is unavailable.
    }
    return 1;
}

function hasTextSelection(): boolean {
    const selection = window.getSelection();
    return Boolean(selection && !selection.isCollapsed && selection.toString().trim());
}

function boundedOutputScale(width: number, height: number): number {
    const pixelRatio = Math.min(window.devicePixelRatio || 1, MAX_OUTPUT_SCALE);
    const area = Math.max(width * height, 1);
    return Math.max(0.25, Math.min(pixelRatio, Math.sqrt(MAX_CANVAS_PIXELS / area)));
}
