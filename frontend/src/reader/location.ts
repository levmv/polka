import type { Locator } from '../types';
import type { FoliateViewElement } from './foliate-engine';

function clipText(text: string, limit: number, end = false): string {
    const bounded = end ? text.slice(-limit * 2) : text.slice(0, limit * 2);
    const points = Array.from(bounded);
    return (end ? points.slice(-limit) : points.slice(0, limit)).join('');
}

// Walk only the neighboring text, with a node budget for pathological markup.
// Do not normalize whitespace: these strings identify the actual selected text.
export function rangeContext(
    range: Range,
    limit = 500,
    root?: Node,
): { before: string; after: string } {
    return {
        before: adjacentText(range.startContainer, range.startOffset, -1, limit, root),
        after: adjacentText(range.endContainer, range.endOffset, 1, limit, root),
    };
}

function adjacentText(
    container: Node,
    offset: number,
    direction: -1 | 1,
    limit: number,
    boundary?: Node,
): string {
    const root =
        boundary || container.ownerDocument?.body || container.ownerDocument?.documentElement;
    let node: Node | null = container;
    let result = '';
    let inspect = false;
    if (container.nodeType === Node.TEXT_NODE) {
        const text = container.textContent || '';
        result = direction === -1 ? text.slice(0, offset) : text.slice(offset);
        result = clipText(result, limit, direction === -1);
    } else {
        const child = container.childNodes[direction === -1 ? offset - 1 : offset];
        if (child) {
            node = child;
            inspect = true;
        }
    }
    for (let visited = 0; node && visited < 2048 && Array.from(result).length < limit; visited++) {
        if (!inspect) {
            while (
                node &&
                node !== root &&
                !(direction === -1 ? node.previousSibling : node.nextSibling)
            )
                node = node.parentNode;
            if (!node || node === root) break;
            node = direction === -1 ? node.previousSibling : node.nextSibling;
        }
        inspect = false;
        if (!node) break;
        while (direction === -1 ? node.lastChild : node.firstChild) {
            node = (direction === -1 ? node.lastChild : node.firstChild)!;
        }
        if (node.nodeType === Node.TEXT_NODE) {
            const text = clipText(node.textContent || '', limit, direction === -1);
            result = clipText(
                direction === -1 ? text + result : result + text,
                limit,
                direction === -1,
            );
        }
    }
    return result;
}

function foliateSection(
    page: HTMLElement,
    view: FoliateViewElement,
    index?: number,
    range?: Range,
) {
    // Other Foliate importers generate documents without source chapter paths.
    if (
        !['epub', 'kepub'].includes(page.dataset.readerFormat || '') &&
        !page.dataset.readerFallback
    )
        return undefined;
    if (index === undefined && range) {
        index = view.renderer
            ?.getContents?.()
            .find((item) => item.doc === range.startContainer.ownerDocument)?.index;
    }
    return view.book?.sections?.[index ?? -1];
}

export function foliateSectionPath(
    page: HTMLElement,
    view: FoliateViewElement,
    index?: number,
    range?: Range,
): string {
    return foliateSection(page, view, index, range)?.id || '';
}

export function foliateLocation(
    page: HTMLElement,
    view: FoliateViewElement,
    cfi?: string,
    range?: Range,
): Locator {
    const path = foliateSection(page, view, undefined, range)?.id;
    return { ...(cfi ? { cfi } : {}), ...(path ? { path } : {}) };
}
