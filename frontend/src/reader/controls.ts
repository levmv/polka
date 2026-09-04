import { clamp } from '../dom';
import {
    DEFAULT_READER_COLUMN_WIDTH,
    type FoliateLoadDetail,
    type FoliateViewElement,
    readerTextColumnWidth,
} from './foliate-engine';

const SYNTHETIC_MOUSE_DEDUPE_MS = 500;
const READER_TAP_SLOP_PX = 10;
const READER_CHROME_AUTO_HIDE_MS = 3_000;
const MIN_SIDE_MARGIN_FOR_CLICK_TURN = 96;
const MIN_PAGE_TURN_ZONE = 48;
const MAX_PAGE_TURN_ZONE = 220;
const MIN_TEXT_GUARD_ZONE = 56;
const MAX_TEXT_GUARD_ZONE = 180;
const wiredEPUBDocuments = new WeakSet<Document>();

export interface ReaderControlOptions {
    onNavigate?: () => void;
}

export function wireReaderControls(
    page: HTMLElement,
    stage: HTMLElement,
    view: FoliateViewElement,
    options: ReaderControlOptions = {},
): void {
    const press = createPressTracker();
    stage.addEventListener('pointerdown', (event) => press.start(event), true);

    view.addEventListener('pointerup', (event) => {
        const target = event.target instanceof Element ? event.target : null;
        if (isInteractiveTarget(target) || !press.isTap(event)) return;

        const rect = view.getBoundingClientRect();
        const handled = handleReaderPointer(page, view, event.clientX - rect.left, rect.width, {
            onNavigate: options.onNavigate,
            pointerType: event.pointerType,
        });
        if (handled) {
            event.preventDefault();
            event.stopPropagation();
        }
    });

    stage.addEventListener('pointerup', (event) => {
        const target = event.target instanceof Element ? event.target : null;
        if (isInteractiveTarget(target) || !press.isTap(event)) return;

        const rect = stage.getBoundingClientRect();
        const handled = handleReaderPointer(page, view, event.clientX - rect.left, rect.width, {
            onNavigate: options.onNavigate,
            pointerType: event.pointerType,
        });
        if (handled) event.preventDefault();
    });

    view.addEventListener('load', (event) => {
        const detail = (event as CustomEvent<FoliateLoadDetail>).detail;
        wireEPUBDocumentControls(page, view, detail.doc, options);
    });

    window.addEventListener(
        'keydown',
        (event) => {
            handleReaderKey(event, page, view, options);
        },
        true,
    );
}

export function wireEPUBDocumentControls(
    page: HTMLElement,
    view: FoliateViewElement,
    doc: Document,
    options: ReaderControlOptions = {},
): void {
    if (wiredEPUBDocuments.has(doc)) return;
    wiredEPUBDocuments.add(doc);

    // Deduplicate the immediate Pointer/Mouse/Click sequence by timestamp and
    // iOS's delayed synthetic mouse events by elapsed time.
    let lastHandledStamp = Number.NaN;
    let lastHandledTouchAt = -SYNTHETIC_MOUSE_DEDUPE_MS;
    const press = createPressTracker();
    const handlePoint = (event: Event, point: ReaderPressPoint, pointerType?: string): void => {
        const target = event.target instanceof Element ? event.target : null;
        if (isInteractiveTarget(target) || hasTextSelection(doc)) return;
        if (!press.isTap(point)) return;
        const now = window.performance.now();
        if (event.timeStamp === lastHandledStamp) return;
        if (now - lastHandledTouchAt < SYNTHETIC_MOUSE_DEDUPE_MS) return;

        const rect = view.getBoundingClientRect();
        const x = documentXToViewX(doc, point.clientX, rect);
        const handled = handleReaderPointer(page, view, x, rect.width, {
            onNavigate: options.onNavigate,
            pointerType,
        });
        if (handled) {
            lastHandledStamp = event.timeStamp;
            if (pointerType === 'touch') lastHandledTouchAt = now;
            event.preventDefault();
        }
    };

    doc.addEventListener('pointerdown', (event) => press.start(event), true);

    doc.addEventListener(
        'touchstart',
        (event) => {
            const touch = event.changedTouches[0];
            if (touch) press.start(touch);
        },
        true,
    );

    // The full-stage iframe includes the visual side margins. Prevent selection
    // from starting there and clear an existing selection before handling a click.
    doc.addEventListener(
        'mousedown',
        (event) => {
            if (event.button !== 0 || isInteractiveTarget(event.target as Element | null)) return;
            const rect = view.getBoundingClientRect();
            const x = documentXToViewX(doc, event.clientX, rect);
            if (x === null || !isReaderPageMargin(view, x, rect.width)) return;
            doc.getSelection()?.removeAllRanges();
            event.preventDefault();
        },
        true,
    );

    doc.addEventListener(
        'pointerup',
        (event) => {
            handlePoint(event, event, event.pointerType);
        },
        true,
    );

    doc.addEventListener(
        'mouseup',
        (event) => {
            handlePoint(event, event, 'mouse');
        },
        true,
    );

    doc.addEventListener(
        'click',
        (event) => {
            handlePoint(event, event, 'mouse');
        },
        true,
    );

    doc.addEventListener(
        'touchend',
        (event) => {
            const touch = event.changedTouches[0];
            if (!touch) return;
            handlePoint(event, touch, 'touch');
        },
        true,
    );

    doc.addEventListener(
        'keydown',
        (event) => {
            handleReaderKey(event, page, view, options);
        },
        true,
    );
}

interface ReaderPressPoint {
    clientX: number;
    screenX: number;
    screenY: number;
}

// Use screen coordinates because the iframe itself moves during a swipe,
// making document-relative movement unreliable.
function createPressTracker(): {
    start(point: ReaderPressPoint): void;
    isTap(point: ReaderPressPoint): boolean;
} {
    let origin: { screenX: number; screenY: number } | null = null;
    return {
        start(point) {
            origin = { screenX: point.screenX, screenY: point.screenY };
        },
        isTap(point) {
            if (!origin) return true;
            return (
                Math.abs(point.screenX - origin.screenX) <= READER_TAP_SLOP_PX &&
                Math.abs(point.screenY - origin.screenY) <= READER_TAP_SLOP_PX
            );
        },
    };
}

// Convert an iframe-relative x coordinate to the outer reader view.
function documentXToViewX(doc: Document, clientX: number, viewRect: DOMRect): number | null {
    const frame = doc.defaultView?.frameElement;
    if (!frame) return null;
    return clientX + frame.getBoundingClientRect().left - viewRect.left;
}

function handleReaderPointer(
    page: HTMLElement,
    view: FoliateViewElement,
    x: number | null,
    width: number,
    options: { onNavigate?: () => void; pointerType?: string },
): boolean {
    if (width <= 0) return false;

    if (x !== null && options.pointerType !== 'touch') {
        const direction = readerTurnDirection(view, x, width);
        if (direction) {
            options.onNavigate?.();
            const turn = direction === 'left' ? turnLeft : turnRight;
            turn(view).catch((e) => console.error('Failed to turn page:', e));
            focusReaderSurface(page);
            return true;
        }
    }

    const isMouse = options.pointerType === 'mouse' || !shouldAutoHideChrome();
    if (isMouse && page.classList.contains('reader-chrome-hidden')) {
        revealChrome(page, false);
        focusReaderSurface(page);
        return true;
    }

    toggleReaderChrome(page);
    focusReaderSurface(page);
    return true;
}

function isInteractiveTarget(target: Element | null): boolean {
    return Boolean(target?.closest('a, button, input, select, textarea'));
}

function handleReaderKey(
    event: KeyboardEvent,
    page: HTMLElement,
    view: FoliateViewElement,
    options: ReaderControlOptions,
): void {
    if (shouldIgnoreReaderShortcut(event)) return;

    if (event.key === 'ArrowLeft') {
        event.preventDefault();
        options.onNavigate?.();
        turnLeft(view).catch((e) => console.error('Failed to turn page:', e));
        focusReaderSurface(page);
    } else if (event.key === 'ArrowRight' || isSpaceKey(event)) {
        event.preventDefault();
        options.onNavigate?.();
        const turn = event.shiftKey && isSpaceKey(event) ? turnLeft : turnRight;
        turn(view).catch((e) => console.error('Failed to turn page:', e));
        focusReaderSurface(page);
    } else if (event.key === 'Escape') {
        event.preventDefault();
        if (page.classList.contains('reader-chrome-hidden')) {
            revealChrome(page, false);
            focusReaderSurface(page);
            return;
        }
        closeReader(page);
    }
}

function eventTargetElement(event: Event): Element | null {
    const target = event.target;
    if (target && typeof (target as Element).closest === 'function') return target as Element;
    return null;
}

export function shouldIgnoreReaderShortcut(event: Event): boolean {
    return Boolean(
        eventTargetElement(event)?.closest(
            'a, button, input, textarea, select, [contenteditable="true"]',
        ),
    );
}

function isSpaceKey(event: KeyboardEvent): boolean {
    return event.code === 'Space' || event.key === ' ' || event.key === 'Spacebar';
}

function hasTextSelection(doc: Document): boolean {
    const selection = doc.getSelection();
    return Boolean(selection && !selection.isCollapsed && selection.toString().trim());
}

function readerTurnDirection(
    view: FoliateViewElement,
    x: number,
    width: number,
): 'left' | 'right' | null {
    const zones = readerTurnZones(view, width);
    if (!zones) return null;
    if (x <= zones.leftEnd) return 'left';
    if (x >= zones.rightStart) return 'right';
    return null;
}

function isReaderPageMargin(view: FoliateViewElement, x: number, width: number): boolean {
    const sideMargin = readerSideMargin(view, width);
    return x < sideMargin || x > width - sideMargin;
}

function readerSideMargin(view: FoliateViewElement, width: number): number {
    if (view.dataset.readerWritingMode === 'vertical') {
        // max-inline-size describes column height in vertical writing. The
        // mounted iframe is the reliable horizontal extent in that mode.
        const frame = view.renderer?.getContents?.().find(({ doc }) => doc)?.doc
            ?.defaultView?.frameElement;
        const frameWidth = frame?.getBoundingClientRect().width || 0;
        return frameWidth > 0 ? Math.max(0, (width - Math.min(width, frameWidth)) / 2) : 0;
    }
    return Math.max(0, (width - currentReaderContentWidth(view, width)) / 2);
}

function readerTurnZones(
    view: FoliateViewElement,
    width: number,
): { leftEnd: number; rightStart: number } | null {
    const sideMargin = readerSideMargin(view, width);
    if (sideMargin < MIN_SIDE_MARGIN_FOR_CLICK_TURN) return null;

    const textGuard = clamp(sideMargin * 0.35, MIN_TEXT_GUARD_ZONE, MAX_TEXT_GUARD_ZONE);
    const candidate = clamp(sideMargin * 0.45, MIN_PAGE_TURN_ZONE, MAX_PAGE_TURN_ZONE);
    const zoneWidth = Math.min(candidate, sideMargin - textGuard);
    if (zoneWidth < MIN_PAGE_TURN_ZONE) return null;

    return {
        leftEnd: zoneWidth,
        rightStart: width - zoneWidth,
    };
}

function currentReaderContentWidth(view: FoliateViewElement, width: number): number {
    const raw = view.renderer?.getAttribute('max-inline-size') || '';
    const parsed = Number.parseFloat(raw);
    const columnWidth =
        Number.isFinite(parsed) && parsed > 0 ? parsed : DEFAULT_READER_COLUMN_WIDTH;
    return readerTextColumnWidth(width, columnWidth);
}

async function turnLeft(view: FoliateViewElement): Promise<void> {
    if (view.goLeft) {
        await view.goLeft();
        return;
    }
    await view.prev();
}

async function turnRight(view: FoliateViewElement): Promise<void> {
    if (view.goRight) {
        await view.goRight();
        return;
    }
    await view.next();
}

export function toggleReaderChrome(page: HTMLElement): void {
    if (page.classList.contains('reader-chrome-hidden')) {
        revealChrome(page);
        return;
    }
    hideChrome(page);
}

export function focusReaderSurface(page: HTMLElement): void {
    page.querySelector<HTMLElement>('.reader-epub-stage, .reader-pdf-stage')?.focus({
        preventScroll: true,
    });
}

export function revealChrome(page: HTMLElement, autoHide = true): void {
    page.classList.remove('reader-chrome-hidden');
    const oldTimer = Number(page.dataset.chromeTimer || 0);
    if (oldTimer) window.clearTimeout(oldTimer);
    delete page.dataset.chromeTimer;
    if (!autoHide || !shouldAutoHideChrome()) return;

    const timer = window.setTimeout(() => {
        hideChrome(page);
    }, READER_CHROME_AUTO_HIDE_MS);
    page.dataset.chromeTimer = String(timer);
}

function hideChrome(page: HTMLElement): void {
    const oldTimer = Number(page.dataset.chromeTimer || 0);
    if (oldTimer) window.clearTimeout(oldTimer);
    delete page.dataset.chromeTimer;
    page.classList.add('reader-chrome-hidden');
}

function shouldAutoHideChrome(): boolean {
    return window.matchMedia('(hover: none), (pointer: coarse)').matches;
}

export function closeReader(page: HTMLElement): void {
    const closeLink = page.querySelector<HTMLAnchorElement>('.reader-close[href]');
    if (!closeLink) return;
    window.location.href = closeLink.href;
}

export function showReaderError(page: HTMLElement, message: string): void {
    const stage = page.querySelector<HTMLElement>('.reader-epub-stage, .reader-pdf-stage');
    if (!stage) return;
    stage.innerHTML = '';
    const error = document.createElement('div');
    error.className = 'reader-loading reader-loading-error';
    error.textContent = message;
    stage.append(error);
}
