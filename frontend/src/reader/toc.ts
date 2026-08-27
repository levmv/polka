import type { FoliateRelocateDetail, FoliateTOCItem, FoliateViewElement } from './foliate-engine';
import { createReaderTOCPanel } from './toc-panel';

export interface ReaderTOCOptions {
    onNavigate?: () => void;
}

interface TOCBuildContext {
    view: FoliateViewElement;
    panel: HTMLElement;
    close: () => void;
    options: ReaderTOCOptions;
}

export function wireReaderTOC(
    page: HTMLElement,
    view: FoliateViewElement,
    options: ReaderTOCOptions = {},
): void {
    const toc = view.book?.toc?.filter(hasTOCContent) || [];
    if (toc.length === 0) return;

    const controls = createReaderTOCPanel(page);
    if (!controls) return;
    appendTOCItems(controls.list, toc, 0, {
        view,
        panel: controls.panel,
        close: controls.close,
        options,
    });
    view.addEventListener('relocate', (event) => {
        const href = (event as CustomEvent<FoliateRelocateDetail>).detail.tocItem?.href;
        if (href) markCurrentTOCItem(controls.panel, href);
    });
}

function appendTOCItems(
    list: HTMLOListElement,
    items: FoliateTOCItem[],
    depth: number,
    context: TOCBuildContext,
): void {
    for (const item of items) {
        const subitems = item.subitems?.filter(hasTOCContent) || [];
        const label = item.label?.trim();
        if (!label) {
            appendTOCItems(list, subitems, depth, context);
            continue;
        }

        const row = document.createElement('li');
        row.className = 'reader-toc-row';
        const href = item.href?.trim();
        if (href) {
            const button = document.createElement('button');
            button.className = 'reader-toc-item';
            button.type = 'button';
            button.textContent = label;
            button.dataset.readerTocHref = href;
            button.style.paddingInlineStart = `${0.65 + depth}rem`;
            button.addEventListener('click', () => {
                context.options.onNavigate?.();
                context.view
                    .goTo(href)
                    .catch((e) => console.error('Failed to navigate reader TOC:', e));
                markCurrentTOCItem(context.panel, href);
                context.close();
            });
            row.append(button);
        } else {
            const groupLabel = document.createElement('span');
            groupLabel.className = 'reader-toc-group-label';
            groupLabel.textContent = label;
            groupLabel.style.paddingInlineStart = `${0.65 + depth}rem`;
            row.append(groupLabel);
        }

        if (subitems.length > 0) {
            const nested = document.createElement('ol');
            nested.className = 'reader-toc-list';
            appendTOCItems(nested, subitems, depth + 1, context);
            row.append(nested);
        }
        list.append(row);
    }
}

function hasTOCContent(item: FoliateTOCItem): boolean {
    return Boolean(item.label?.trim() || item.subitems?.some(hasTOCContent));
}

function markCurrentTOCItem(panel: HTMLElement, href: string): void {
    const items = panel.querySelectorAll<HTMLButtonElement>('[data-reader-toc-href]');
    for (const item of items) {
        const current = item.dataset.readerTocHref === href;
        item.classList.toggle('active', current);
        if (current) item.setAttribute('aria-current', 'location');
        else item.removeAttribute('aria-current');
    }
}
