import { fetchReaderPosition, fetchUserSettings, touchReader } from '../api';
import { errorMessage } from '../errors';
import type { ReaderPreferences } from '../types';
import { createReadingActivity } from './activity';
import { wireAnnotations } from './annotations';
import { revealChrome, showReaderError } from './chrome';
import { foliateAnnotationSurface } from './foliate-annotations';
import { wireEPUBDocumentControls, wireReaderControls } from './foliate-controls';
import {
    applyFoliateDisplay,
    applyReaderCanvasColor,
    createFoliateView,
    type FoliateLoadDetail,
    type FoliateViewElement,
    fetchFoliateBookFile,
    openFoliateBookFile,
    readerDisplayPalette,
    suppressTransientFoliateRenderErrors,
    waitForRendererContents,
    wireCurrentFoliateDocuments,
    wireFoliateDocumentStyling,
} from './foliate-engine';
import { createReaderPagination } from './foliate-pagination';
import { restoreFoliatePosition, wireFoliatePosition } from './foliate-progress';
import { wireReaderSearch } from './foliate-search';
import { wireFoliateSelection } from './foliate-selection';
import { wireReaderTOC } from './foliate-toc';
import { wireReaderLifecycle } from './lifecycle';
import { createPositionSaver } from './position-saver';
import {
    DEFAULT_READER_PREFERENCES,
    normalizeReaderPreferences,
    wireReaderPreferences,
} from './preferences';
import { handleReadingStatusChange } from './reading-status';

export function initReader(): void {
    const page = document.querySelector<HTMLElement>('.reader-page');
    const assetId = Number(page?.dataset.readerAssetId);
    if (!page || !assetId) return;

    const format = page.dataset.readerFormat || '';
    initFoliateReader(page, assetId, format).catch((e) => {
        console.error('Failed to initialize reader:', e);
        showReaderError(page, 'Could not open this book.');
    });
}

async function initFoliateReader(
    page: HTMLElement,
    assetId: number,
    format: string,
): Promise<void> {
    const stage = page.querySelector<HTMLElement>('.reader-epub-stage');
    const loading = page.querySelector<HTMLElement>('[data-reader-loading]');
    const readURL = page.dataset.readerUrl;
    if (!stage || !readURL) return;
    const fallbackURL = page.dataset.readerFallbackUrl || '';
    const positionSaver = createPositionSaver(assetId, {
        onPositionSaved: handleReadingStatusChange,
        restorePosition: (state) => foliatePosition.restorePosition(state),
    });
    const activity = createReadingActivity(assetId);

    const statePromise = fetchReaderPosition(assetId).catch((e) => {
        console.error('Failed to fetch reading position:', e);
        return null;
    });
    const preferencesPromise = fetchUserSettings().catch((e) => {
        console.error('Failed to fetch reader preferences:', e);
        return DEFAULT_READER_PREFERENCES satisfies ReaderPreferences;
    });

    // The Foliate paginator can briefly render an empty iframe document on any
    // section change. Keep its narrow upstream-race guard for this reader page.
    suppressTransientFoliateRenderErrors();
    const preferences = normalizeReaderPreferences(await preferencesPromise);
    page.dataset.readerFlow = preferences.reader_flow;
    page.dataset.readerStyle = preferences.reader_style;
    page.dataset.readerFontScale = String(preferences.reader_font_size);
    // Apply before loading the book to avoid flashing the app background.
    applyReaderCanvasColor(readerDisplayPalette(preferences.reader_style).background);
    const view: FoliateViewElement = await openFoliateBookWithFallback(
        page,
        stage,
        readURL,
        format,
        fallbackURL,
    );
    const foliatePosition = wireFoliatePosition(page, view, positionSaver, {
        savingEnabled: false,
    });
    const pagination = createReaderPagination(page, view, preferences);
    const lifecycle = wireReaderLifecycle(page, stage, positionSaver, activity, {
        onNavigate: foliatePosition.markUserNavigation,
        onResume: () => {
            void annotations.load();
            pagination.resume();
        },
        onSuspend: pagination.suspend,
    });
    const onNavigate = lifecycle.markUserNavigation;
    view.addEventListener('link', onNavigate);
    view.addEventListener('load', (event) => {
        lifecycle.observeDocument((event as CustomEvent<FoliateLoadDetail>).detail.doc);
    });
    const search = wireReaderSearch(page, view, {
        onNavigate,
    });
    let selectionController: ReturnType<typeof wireFoliateSelection> | undefined;
    const annotations = wireAnnotations(page, assetId, foliateAnnotationSurface(view), {
        onNavigate,
        onShowActions: (target) => selectionController?.showAnnotationActions(target),
    });
    selectionController = wireFoliateSelection(page, view, {
        onHighlightSelection: annotations.createHighlight,
        onNoteSelection: (payload) => annotations.createHighlight(payload, true),
        onEditAnnotation: annotations.editNote,
        onDeleteAnnotation: annotations.deleteHighlight,
        annotationAt: annotations.annotationAt,
        onSearchSelection: search.openWithQuery,
    });
    applyFoliateDisplay(view, preferences);
    await annotations.load();
    wireReaderControls(page, stage, view, {
        onNavigate,
        beforeClose: annotations.savePendingEdits,
    });
    wireReaderTOC(page, view, {
        onNavigate,
    });

    const state = await statePromise;
    positionSaver.initialize(state);
    // FB2 mounts its first document more reliably in scrolled flow. Apply the
    // user's saved preference immediately after Foliate finishes init.
    if (format === 'fb2') {
        view.renderer?.setAttribute('flow', 'scrolled');
    }
    const annotationID = Number(
        new URLSearchParams(window.location.hash.slice(1)).get('annotation'),
    );
    await restoreFoliatePosition(view, state, annotations.location(annotationID)?.cfi);
    foliatePosition.enableSaving();
    void positionSaver.flush();
    wireReaderPreferences(page, view, preferences, pagination.setPreferences);
    await waitForRendererContents(view);
    wireCurrentFoliateDocuments(view, (doc) => {
        lifecycle.observeDocument(doc);
        wireEPUBDocumentControls(page, view, doc, {
            onNavigate,
        });
    });

    loading?.remove();
    page.classList.add('reader-ready');
    stage.dataset.readerReady = 'true';
    stage.focus({ preventScroll: true });
    revealChrome(page);
    lifecycle.start();
    pagination.start();
    touchReader(assetId).catch((e) => {
        console.error('Failed to record book opening:', e);
    });
}

async function openFoliateBookWithFallback(
    page: HTMLElement,
    stage: HTMLElement,
    readURL: string,
    format: string,
    fallbackURL: string,
): Promise<FoliateViewElement> {
    // Only retry files rejected by Foliate. Network and storage errors should
    // propagate without triggering conversion.
    const sourceFile = await fetchFoliateBookFile(readURL, format);
    let view = createReaderFoliateView(page, stage);
    try {
        await openFoliateBookFile(view, sourceFile);
        return view;
    } catch (sourceError) {
        view.remove();
        if (!fallbackURL) throw sourceError;

        // KEPUB conversion can repair package defects that Foliate rejects.
        // Progress and annotations remain attached to the original asset.
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
