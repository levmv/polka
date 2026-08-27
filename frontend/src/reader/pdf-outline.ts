import type { PDFDocumentProxy } from 'pdfjs-dist/legacy/build/pdf.mjs';

import { createReaderTOCPanel } from './toc-panel';

const MAX_OUTLINE_ENTRIES = 1_000;
const MAX_OUTLINE_DEPTH = 16;

type PDFOutlineItem = Awaited<ReturnType<PDFDocumentProxy['getOutline']>>[number];
type PDFPageRef = Parameters<PDFDocumentProxy['getPageIndex']>[0];

interface PDFOutlineOptions {
    navigateTo: (pageNumber: number) => Promise<void>;
}

interface OutlineBuildState {
    visited: number;
    rendered: number;
    limited: boolean;
}

interface OutlineBuildContext {
    state: OutlineBuildState;
    pdfDocument: PDFDocumentProxy;
    navigateTo: PDFOutlineOptions['navigateTo'];
    close: () => void;
}

export function wirePDFOutline(
    page: HTMLElement,
    pdfDocument: PDFDocumentProxy,
    options: PDFOutlineOptions,
): void {
    void pdfDocument
        .getOutline()
        .then((outline) => {
            if (!outline?.length) return;
            const controls = createReaderTOCPanel(page);
            if (!controls) return;

            const state: OutlineBuildState = { visited: 0, rendered: 0, limited: false };
            appendPDFOutlineItems(controls.list, outline, 0, {
                state,
                pdfDocument,
                navigateTo: options.navigateTo,
                close: controls.close,
            });
            if (state.rendered === 0) {
                controls.destroy();
                return;
            }
            if (state.limited) {
                const note = document.createElement('p');
                note.className = 'reader-search-note';
                note.textContent = 'This unusually large outline has been shortened.';
                controls.nav.append(note);
            }
        })
        .catch((error) => console.error('Failed to load PDF outline:', error));
}

function appendPDFOutlineItems(
    list: HTMLOListElement,
    items: PDFOutlineItem[],
    depth: number,
    context: OutlineBuildContext,
): void {
    if (depth >= MAX_OUTLINE_DEPTH) {
        if (items.length > 0) context.state.limited = true;
        return;
    }

    for (const item of items) {
        if (context.state.visited >= MAX_OUTLINE_ENTRIES) {
            context.state.limited = true;
            return;
        }
        context.state.visited++;

        const children = Array.isArray(item.items) ? (item.items as PDFOutlineItem[]) : [];
        const label = item.title?.trim();
        if (!label) {
            appendPDFOutlineItems(list, children, depth, context);
            continue;
        }
        const hasDestination = hasPDFOutlineDestination(item.dest);
        if (!hasDestination && children.length === 0) continue;

        context.state.rendered++;
        const row = document.createElement('li');
        row.className = 'reader-toc-row';
        if (hasDestination) {
            const button = document.createElement('button');
            button.className = 'reader-toc-item';
            button.type = 'button';
            button.textContent = label;
            button.style.paddingInlineStart = `${0.65 + depth}rem`;
            button.addEventListener('click', () => {
                void navigatePDFOutlineItem(button, item.dest, context)
                    .then((navigated) => {
                        if (navigated) context.close();
                    })
                    .catch((error) => console.error('Failed to navigate PDF outline:', error));
            });
            row.append(button);
        } else {
            const groupLabel = document.createElement('span');
            groupLabel.className = 'reader-toc-group-label';
            groupLabel.textContent = label;
            groupLabel.style.paddingInlineStart = `${0.65 + depth}rem`;
            row.append(groupLabel);
        }

        if (children.length > 0) {
            const nested = document.createElement('ol');
            nested.className = 'reader-toc-list';
            appendPDFOutlineItems(nested, children, depth + 1, context);
            if (nested.childElementCount > 0) row.append(nested);
        }
        list.append(row);
    }
}

function hasPDFOutlineDestination(destination: PDFOutlineItem['dest']): boolean {
    if (typeof destination === 'string') return Boolean(destination);
    return Array.isArray(destination) && destination.length > 0;
}

async function navigatePDFOutlineItem(
    button: HTMLButtonElement,
    destination: PDFOutlineItem['dest'],
    context: OutlineBuildContext,
): Promise<boolean> {
    button.disabled = true;
    button.setAttribute('aria-busy', 'true');
    try {
        const explicitDestination =
            typeof destination === 'string'
                ? await context.pdfDocument.getDestination(destination)
                : destination;
        if (!explicitDestination?.length) return false;

        const reference = explicitDestination[0];
        let pageIndex: number;
        if (Number.isInteger(reference)) {
            pageIndex = reference;
        } else if (isPDFPageRef(reference)) {
            pageIndex = await context.pdfDocument.getPageIndex(reference);
        } else {
            return false;
        }
        if (pageIndex < 0 || pageIndex >= context.pdfDocument.numPages) return false;
        await context.navigateTo(pageIndex + 1);
        return true;
    } finally {
        button.disabled = false;
        button.removeAttribute('aria-busy');
    }
}

function isPDFPageRef(value: unknown): value is PDFPageRef {
    if (!value || typeof value !== 'object') return false;
    const candidate = value as { num?: unknown; gen?: unknown };
    return Number.isInteger(candidate.num) && Number.isInteger(candidate.gen);
}
