import { fetchReaderPreferences, fetchReaderState, touchReaderState } from '../api';
import { errorMessage } from '../errors';
import type { ReaderPreferences } from '../types';
import { wireAnnotations } from './annotations';
import {
    revealChrome,
    showReaderError,
    wireEPUBDocumentControls,
    wireReaderControls,
} from './controls';
import {
    applyFoliateDisplay,
    createFoliateView,
    type FoliateLoadDetail,
    type FoliateViewElement,
    fetchFoliateBookFile,
    fitFoliateCoverDocument,
    openFoliateBookFile,
    setFoliateDocumentJustification,
    suppressTransientFoliateRenderErrors,
    waitForRendererContents,
    wireCurrentFoliateDocuments,
} from './foliate-engine';
import {
    DEFAULT_READER_PREFERENCES,
    normalizeReaderPreferences,
    wireReaderPreferences,
} from './preferences';
import { restoreReaderPosition, wirePositionSaving } from './progress';
import { handleReadingStatusChange } from './reading-status';
import { wireReaderSearch } from './search';
import { wireReaderSelection } from './selection';
import { createReaderStateSaver } from './state-saver';
import { wireReaderTOC } from './toc';

export function initReader(): void {
    const page = document.querySelector<HTMLElement>('.reader-page');
    const assetId = page?.dataset.readerAssetId;
    if (!assetId) return;

    const format = page.dataset.readerFormat || '';
    initFoliateReader(page, assetId, format).catch((e) => {
        console.error('Failed to initialize reader:', e);
        showReaderError(page, 'Could not open this book.');
    });
}

async function initFoliateReader(
    page: HTMLElement,
    assetId: string,
    format: string,
): Promise<void> {
    const stage = page.querySelector<HTMLElement>('.reader-epub-stage');
    const loading = page.querySelector<HTMLElement>('[data-reader-loading]');
    const readURL = page.dataset.readerUrl;
    if (!stage || !readURL) return;
    const fallbackURL = page.dataset.readerFallbackUrl || '';
    const stateSaver = createReaderStateSaver(page, assetId, {
        onStateSaved: handleReadingStatusChange,
    });

    const statePromise = fetchReaderState(assetId).catch((e) => {
        console.error('Failed to fetch reader state:', e);
        return null;
    });
    const preferencesPromise = fetchReaderPreferences().catch((e) => {
        console.error('Failed to fetch reader preferences:', e);
        return DEFAULT_READER_PREFERENCES satisfies ReaderPreferences;
    });

    // The Foliate paginator can briefly render an empty iframe document on any
    // section change. Keep its narrow upstream-race guard for this reader page.
    suppressTransientFoliateRenderErrors();
    const preferences = normalizeReaderPreferences(await preferencesPromise);
    page.dataset.readerFlow = preferences.epub_flow;
    page.dataset.readerStyle = preferences.display_style;
    page.dataset.readerFontScale = String(preferences.font_scale);
    const view: FoliateViewElement = await openFoliateBookWithFallback(
        page,
        stage,
        readURL,
        format,
        fallbackURL,
    );
    const positionSaver = wirePositionSaving(page, view, stateSaver, { savingEnabled: false });
    const search = wireReaderSearch(page, view, {
        onNavigate: () => positionSaver.markUserNavigation(),
    });
    const annotations = wireAnnotations(page, assetId, view, {
        onNavigate: () => positionSaver.markUserNavigation(),
    });
    wireReaderSelection(page, view, {
        onHighlightSelection: annotations.createHighlight,
        onSearchSelection: search.openWithQuery,
    });
    applyFoliateDisplay(view, preferences);
    await annotations.hydrate();
    wireReaderControls(page, stage, view, {
        onNavigate: () => positionSaver.markUserNavigation(),
    });
    wireReaderTOC(page, view, {
        onNavigate: () => positionSaver.markUserNavigation(),
    });

    const state = await statePromise;
    // FB2 mounts its first document more reliably in scrolled flow. Apply the
    // user's saved preference immediately after Foliate finishes init.
    if (format === 'fb2') {
        view.renderer?.setAttribute('flow', 'scrolled');
    }
    await restoreReaderPosition(view, state);
    positionSaver.enableSaving();
    void stateSaver.flush();
    wireReaderPreferences(page, view, preferences);
    await waitForRendererContents(view);
    wireCurrentFoliateDocuments(view, (doc) =>
        wireEPUBDocumentControls(page, view, doc, {
            onNavigate: () => positionSaver.markUserNavigation(),
        }),
    );

    loading?.remove();
    page.classList.add('reader-ready');
    stage.dataset.readerReady = 'true';
    stage.focus({ preventScroll: true });
    revealChrome(page);
    touchReaderState(assetId)
        .then(handleReadingStatusChange)
        .catch((e) => {
            console.error('Failed to update reader state:', e);
        });
}

async function openFoliateBookWithFallback(
    page: HTMLElement,
    stage: HTMLElement,
    readURL: string,
    format: string,
    fallbackURL: string,
): Promise<FoliateViewElement> {
    // Transport/storage failures should surface as-is. Only a file that was
    // fetched successfully but rejected by Foliate earns the normalization
    // retry, otherwise an ordinary 404 or interrupted request would start a
    // needless conversion.
    const sourceFile = await fetchFoliateBookFile(readURL, format);
    let view = createReaderFoliateView(page, stage);
    try {
        await openFoliateBookFile(view, sourceFile);
        return view;
    } catch (sourceError) {
        view.remove();
        if (!fallbackURL) throw sourceError;

        // Foliate is intentionally stricter than Polka's EPUB package reader.
        // If direct opening fails, retry through the existing bounded KEPUB
        // normalization path. The original asset remains untouched and remains
        // the identity used for progress and annotations.
        view = createReaderFoliateView(page, stage);
        try {
            const fallbackFile = await fetchFoliateBookFile(fallbackURL, 'kepub');
            await openFoliateBookFile(view, fallbackFile);
        } catch (fallbackError) {
            view.remove();
            throw new Error(
                `Could not open EPUB directly (${errorMessage(sourceError)}) or through KEPUB fallback (${errorMessage(fallbackError)}).`,
            );
        }
        page.dataset.readerFallback = 'epub-to-kepub';
        return view;
    }
}

function createReaderFoliateView(page: HTMLElement, stage: HTMLElement): FoliateViewElement {
    const view = createFoliateView(stage);
    wireFoliateDocumentStyling(page, view);
    return view;
}

function wireFoliateDocumentStyling(page: HTMLElement, view: FoliateViewElement): void {
    view.addEventListener('load', (event) => {
        const detail = (event as CustomEvent<FoliateLoadDetail>).detail;
        const sectionID = view.book?.sections?.[detail.index ?? -1]?.id;
        fitFoliateCoverDocument(detail.doc, sectionID, detail.index);
        setFoliateDocumentJustification(detail.doc, page.dataset.readerStyle !== 'original');
    });
}
