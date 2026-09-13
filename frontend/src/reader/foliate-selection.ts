import type {
    FoliateLoadDetail,
    FoliateRendererRelocateDetail,
    FoliateViewElement,
} from './foliate-engine';
import { foliateSectionPath } from './location';
import {
    type ReaderSelectionController,
    type ReaderSelectionOptions,
    wireReaderSelection,
} from './selection';

export function wireFoliateSelection(
    page: HTMLElement,
    view: FoliateViewElement,
    options: Omit<ReaderSelectionOptions, 'locate'>,
): ReaderSelectionController {
    const selection = wireReaderSelection(page, {
        ...options,
        locate(range, index) {
            try {
                const cfi = view.getCFI?.(index ?? 0, range);
                if (!cfi) return undefined;
                const path = foliateSectionPath(page, view, index, range);
                return { cfi, ...(path ? { path } : {}) };
            } catch (error) {
                console.error('Failed to locate selected text:', error);
                return undefined;
            }
        },
    });
    view.addEventListener('load', (event) => {
        const { doc, index } = (event as CustomEvent<FoliateLoadDetail>).detail;
        selection.attachDocument(doc, index);
    });
    view.renderer?.addEventListener('relocate', (event) => {
        const { reason } = (event as CustomEvent<FoliateRendererRelocateDetail>).detail;
        const content =
            reason === 'snap'
                ? view.renderer?.getContents?.().find(({ doc }) => {
                      const selected = doc?.getSelection();
                      return selected && selected.rangeCount > 0 && !selected.isCollapsed;
                  })
                : undefined;
        selection.relocate(
            content?.doc ? { doc: content.doc, index: content.index } : undefined,
            reason === 'snap',
        );
    });
    for (const { doc, index } of view.renderer?.getContents?.() ?? []) {
        if (doc) selection.attachDocument(doc, index);
    }
    return selection;
}
