import type { PageViewport } from 'pdfjs-dist';
import { ANNOTATION_COLORS } from '../annotations';
import type { Annotation, Locator, LocatorRect } from '../types';
import type { AnnotationAnchor, AnnotationSurface } from './annotation-surface';

export function pdfAnnotationSurface(
    page: HTMLElement,
    textLayer: HTMLElement,
    navigate: (page: number) => Promise<void>,
): AnnotationSurface & {
    setPage(number: number, viewport: PageViewport): void;
    clearPage(): void;
    reveal(id: number): void;
    locate(range: Range): Locator | undefined;
} {
    const annotations = new Map<number, Annotation>();
    const layer = document.createElement('div');
    layer.className = 'reader-pdf-highlights';
    layer.setAttribute('aria-hidden', 'true');
    page.append(layer);
    let pageNumber = 0;
    let viewport: PageViewport | null = null;
    let handlers: Parameters<AnnotationSurface['connect']>[0] | undefined;

    const screenRect = (rect: LocatorRect): DOMRect => {
        const [x1, y1] = viewport!.convertToViewportPoint(rect.x, rect.y);
        const [x2, y2] = viewport!.convertToViewportPoint(
            rect.x + rect.width,
            rect.y + rect.height,
        );
        return new DOMRect(
            Math.min(x1, x2),
            Math.min(y1, y2),
            Math.abs(x2 - x1),
            Math.abs(y2 - y1),
        );
    };
    const visible = (): Annotation[] =>
        [...annotations.values()].filter((row) => row.locator.page === pageNumber);
    const draw = (): void => {
        layer.replaceChildren();
        if (!viewport) return;
        for (const annotation of visible()) {
            for (const rect of annotation.locator.rects ?? []) {
                const bounds = screenRect(rect);
                const highlight = document.createElement('span');
                highlight.dataset.readerAnnotationId = String(annotation.id);
                highlight.style.left = `${bounds.x}px`;
                highlight.style.top = `${bounds.y}px`;
                highlight.style.width = `${bounds.width}px`;
                highlight.style.height = `${bounds.height}px`;
                highlight.style.backgroundColor = ANNOTATION_COLORS[annotation.color];
                layer.append(highlight);
            }
        }
    };
    const anchor = (id: number): AnnotationAnchor | undefined => {
        const annotation = annotations.get(id);
        if (
            !viewport ||
            annotation?.locator.page !== pageNumber ||
            !annotation.locator.rects?.length
        )
            return undefined;
        return {
            doc: page.ownerDocument,
            getBoundingClientRect(): DOMRect {
                if (!viewport || annotation.locator.page !== pageNumber) return new DOMRect();
                const boxes = annotation.locator.rects!.map(screenRect);
                const left = Math.min(...boxes.map((r) => r.left));
                const top = Math.min(...boxes.map((r) => r.top));
                const right = Math.max(...boxes.map((r) => r.right));
                const bottom = Math.max(...boxes.map((r) => r.bottom));
                const offset = page.getBoundingClientRect();
                return new DOMRect(
                    left + offset.left,
                    top + offset.top,
                    right - left,
                    bottom - top,
                );
            },
        };
    };
    const reveal = (id: number): void => {
        layer
            .querySelector<HTMLElement>(`[data-reader-annotation-id="${id}"]`)
            ?.scrollIntoView({ block: 'nearest', inline: 'nearest' });
    };
    return {
        connect(callbacks): void {
            handlers = callbacks;
            let pressed: { x: number; y: number } | null = null;
            page.addEventListener('pointerdown', (event) => {
                pressed = { x: event.clientX, y: event.clientY };
            });
            page.addEventListener('pointercancel', () => {
                pressed = null;
            });
            page.addEventListener('pointerup', (event) => {
                const start = pressed;
                pressed = null;
                if (
                    !viewport ||
                    !start ||
                    Math.hypot(event.clientX - start.x, event.clientY - start.y) > 6
                )
                    return;
                const selection = page.ownerDocument.getSelection();
                if (selection && !selection.isCollapsed) return;
                const offset = page.getBoundingClientRect();
                const x = event.clientX - offset.left;
                const y = event.clientY - offset.top;
                for (const annotation of visible()) {
                    if (
                        !annotation.locator.rects?.some((rect) => {
                            const box = screenRect(rect);
                            return (
                                x >= box.left && x <= box.right && y >= box.top && y <= box.bottom
                            );
                        })
                    )
                        continue;
                    const target = anchor(annotation.id);
                    if (target) {
                        event.stopPropagation();
                        handlers?.show(annotation.id, target);
                    }
                    break;
                }
            });
        },
        async add(annotation): Promise<void> {
            annotations.set(annotation.id, annotation);
            draw();
        },
        async remove(annotation): Promise<void> {
            annotations.delete(annotation.id);
            draw();
        },
        async show(annotation): Promise<void> {
            if (!annotation.locator.page) return;
            await navigate(annotation.locator.page);
            reveal(annotation.id);
            const target = anchor(annotation.id);
            if (target) handlers?.show(annotation.id, target);
        },
        anchor,
        reveal,
        atRange(doc, range): number | undefined {
            if (!viewport || doc !== page.ownerDocument) return undefined;
            const offset = page.getBoundingClientRect();
            const selected = [...range.getClientRects()];
            return visible().find((annotation) =>
                annotation.locator.rects?.some((rect) => {
                    const box = screenRect(rect);
                    return selected.some(
                        (part) =>
                            part.left < box.right + offset.left &&
                            part.right > box.left + offset.left &&
                            part.top < box.bottom + offset.top &&
                            part.bottom > box.top + offset.top,
                    );
                }),
            )?.id;
        },
        setPage(number, nextViewport): void {
            pageNumber = number;
            viewport = nextViewport;
            draw();
        },
        clearPage(): void {
            handlers?.leave();
            viewport = null;
            layer.replaceChildren();
        },
        locate(range): Locator | undefined {
            if (!viewport) return undefined;
            const rects = selectionRects(range, textLayer, viewport);
            return rects.length ? { page: pageNumber, rects } : undefined;
        },
    };
}

function selectionRects(
    range: Range,
    textLayer: HTMLElement,
    viewport: PageViewport,
): LocatorRect[] {
    const bounds = textLayer.getBoundingClientRect();
    const rects: LocatorRect[] = [];
    const walker = textLayer.ownerDocument.createTreeWalker(textLayer, NodeFilter.SHOW_TEXT);
    // Reading text nodes avoids duplicate element/text rectangles from a range
    // spanning several PDF.js spans, including spans changed by search markers.
    for (let node = walker.nextNode(); node; node = walker.nextNode()) {
        if (!range.intersectsNode(node)) continue;
        const part = textLayer.ownerDocument.createRange();
        part.selectNodeContents(node);
        if (node === range.startContainer) part.setStart(node, range.startOffset);
        if (node === range.endContainer) part.setEnd(node, range.endOffset);
        for (const box of part.getClientRects()) {
            if (box.width <= 0 || box.height <= 0) continue;
            const [x1, y1] = viewport.convertToPdfPoint(
                box.left - bounds.left,
                box.top - bounds.top,
            );
            const [x2, y2] = viewport.convertToPdfPoint(
                box.right - bounds.left,
                box.bottom - bounds.top,
            );
            const rounded = (value: number): number => Math.round(value * 1000) / 1000;
            const rect = {
                x: rounded(Math.min(x1, x2)),
                y: rounded(Math.min(y1, y2)),
                width: rounded(Math.abs(x2 - x1)),
                height: rounded(Math.abs(y2 - y1)),
            };
            if (rect.width > 0 && rect.height > 0) rects.push(rect);
        }
    }
    return rects;
}
