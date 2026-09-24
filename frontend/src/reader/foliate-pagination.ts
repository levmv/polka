import { clampNumber } from '../dom';
import type { ReaderPreferences } from '../types';
import {
    applyFoliateDisplay,
    type FoliateSection,
    type FoliateViewElement,
    foliatePagePosition,
    wireFoliateDocumentStyling,
} from './foliate-engine';
import {
    createPaginationDocumentLoader,
    shareSectionResources,
    waitForFonts,
} from './foliate-resources';
import { createPaginationCache, type PaginationLayout } from './pagination-cache';

// Supplied by frontend/build.mjs from the bundled reader and styles.
declare const POLKA_READER_LAYOUT_VERSION: string;

// Cache screen counts per book and layout; the visible section's count takes
// precedence over cached or background measurements.
export function createReaderPagination(
    page: HTMLElement,
    view: FoliateViewElement,
    preferences: ReaderPreferences,
): {
    start(): void;
    resume(): void;
    suspend(finish: boolean): void;
    setPreferences(next: ReaderPreferences): void;
} {
    const sections = view.book?.sections ?? [];
    const progress = page.querySelector<HTMLElement>('[data-reader-progress]');
    let counts = new Map<number, number>();
    const linearIndices = sections
        .map((_, index) => index)
        .filter((index) => sections[index].linear !== 'no');
    let layout: PaginationLayout | undefined;
    let generation = 0;
    let measureTimer: number | undefined;
    let refreshFrame: number | undefined;
    let saveTimer: number | undefined;
    let dirty = false;
    let running = false;
    let sectionController: AbortController | undefined;
    let started = false;
    let stopped = false;
    let suspended = document.visibilityState !== 'visible';
    let failed = false;
    const source = page.dataset.readerFallback
        ? page.dataset.readerFallbackUrl
        : page.dataset.readerUrl;
    const bookVersion = JSON.stringify([
        new URL(source!, location.href).searchParams.get('v') ?? source!,
        view.book?.metadata?.language,
    ]);
    const cache = createPaginationCache(POLKA_READER_LAYOUT_VERSION, navigator.userAgent);

    if (!view.isFixedLayout) shareSectionResources(sections);

    const currentLayout = (): PaginationLayout => {
        const { width, height } = view.getBoundingClientRect();
        return {
            book: bookVersion,
            reader: [width, height],
            viewport: [window.innerWidth, window.innerHeight],
            style: preferences.reader_style,
            fontScale: preferences.reader_font_size,
            ...(preferences.reader_style === 'custom'
                ? {
                      columnWidth: preferences.reader_column_width,
                      lineHeight: preferences.reader_line_height,
                  }
                : {}),
        };
    };

    const usesScreenPages = (): boolean =>
        !view.isFixedLayout && preferences.reader_flow !== 'scrolled';

    const hasAllCounts = (): boolean => linearIndices.every((index) => counts.has(index));

    const save = (): void => {
        window.clearTimeout(saveTimer);
        saveTimer = undefined;
        // Reopening measures the visible section again; only cache additional work.
        if (dirty && layout && (counts.size > 1 || hasAllCounts())) {
            cache.write(layout, sections.length, counts);
        }
        dirty = false;
    };

    const remember = (index: number, count: number): void => {
        if (counts.get(index) === count) return;
        counts.set(index, count);
        dirty = true;
        saveTimer ??= window.setTimeout(save, 1000);
    };

    const display = (): void => {
        if (!progress) return;
        const position = foliatePagePosition(view);
        const counting = usesScreenPages() && !hasAllCounts() && !failed;
        progress.classList.toggle('reader-progress-counting', counting);
        if (view.isFixedLayout && position) {
            progress.textContent = `${position.current} / ${position.total}`;
        } else if (
            usesScreenPages() &&
            hasAllCounts() &&
            position &&
            linearIndices.includes(position.index)
        ) {
            let currentPage = Math.min(position.current, counts.get(position.index)!);
            let total = 0;
            for (const index of linearIndices) {
                const count = counts.get(index)!;
                if (index < position.index) currentPage += count;
                total += count;
            }
            progress.textContent = `${currentPage} / ${total}`;
        } else {
            const fraction = clampNumber(view.lastLocation?.fraction, 0, 1, 0);
            progress.textContent = `${Math.round(fraction * 100)}%`;
        }
        if (counting) {
            progress.setAttribute('aria-label', `${progress.textContent}, counting pages`);
            progress.title = 'Counting pages';
        } else {
            progress.removeAttribute('aria-label');
            progress.removeAttribute('title');
        }
    };

    const schedule = (): void => {
        window.clearTimeout(measureTimer);
        if (stopped || suspended || running || failed || !usesScreenPages()) return;
        if (hasAllCounts()) return;
        // Let the first screen and quick preference changes settle.
        measureTimer = window.setTimeout(() => void measure(), 200);
    };

    const refresh = (): void => {
        if (!started || stopped || suspended) return;
        const next = usesScreenPages() ? currentLayout() : undefined;
        if (JSON.stringify(next) !== JSON.stringify(layout)) {
            if (hasAllCounts()) save();
            // Keep periodic saves, but don't cache every intermediate resize.
            window.clearTimeout(saveTimer);
            saveTimer = undefined;
            dirty = false;
            layout = next;
            generation++;
            sectionController?.abort();
            counts = layout ? cache.read(layout, sections.length) : new Map();
            failed = false;
        }
        // Wait for Foliate's resize observers before sampling the visible section.
        refreshFrame ??= requestAnimationFrame(() => {
            refreshFrame = undefined;
            // Another resize may have started before this frame.
            const next = usesScreenPages() ? currentLayout() : undefined;
            if (JSON.stringify(next) !== JSON.stringify(layout)) {
                refresh();
                return;
            }
            const content = view.renderer?.getContents?.()[0];
            if (layout && content?.index !== undefined && content.doc?.fonts.status === 'loaded') {
                const count = foliatePagePosition(view)?.total;
                if (count !== undefined) {
                    // Fonts can change without a new book or reader version.
                    if (counts.has(content.index) && counts.get(content.index) !== count) {
                        generation++;
                        sectionController?.abort();
                        counts.clear();
                        cache.write(layout, sections.length, counts);
                        failed = false;
                    }
                    remember(content.index, count);
                }
            }
            if (hasAllCounts()) save();
            display();
            schedule();
        });
    };

    const measure = async (): Promise<void> => {
        if (running || suspended || stopped || !usesScreenPages()) return;
        running = true;
        const runGeneration = generation;
        const obsolete = () => stopped || suspended || generation !== runGeneration;
        const host = document.createElement('div');
        host.className = 'reader-pagination-measure';
        host.inert = true;
        host.setAttribute('aria-hidden', 'true');
        const { width, height } = view.getBoundingClientRect();
        host.style.width = `${width}px`;
        host.style.height = `${height}px`;
        page.append(host);
        const measureView = document.createElement('foliate-view') as FoliateViewElement;
        measureView.className = 'reader-epub-view';
        measureView.dataset.readerWritingMode = 'horizontal';
        host.append(measureView);
        wireFoliateDocumentStyling(page, measureView);
        const loadedSections = new Set<FoliateSection>();
        const release = (section: FoliateSection): void => {
            if (loadedSections.delete(section)) section.unload?.();
        };
        try {
            // Share only loading and layout data. Navigation, CSS transforms
            // and media overlays belong to the visible reader.
            await measureView.open({
                dir: view.book?.dir,
                metadata: view.book?.metadata,
                sections: sections.map((section) => ({
                    id: section.id,
                    linear: section.linear,
                    load: async () => {
                        const source = await section.load();
                        loadedSections.add(section);
                        return source;
                    },
                    unload: () => release(section),
                })),
            });
            const renderer = measureView.renderer!;
            renderer.loadDocument = createPaginationDocumentLoader();
            applyFoliateDisplay(measureView, preferences);
            for (const index of linearIndices) {
                if (obsolete()) break;
                if (counts.has(index)) continue;
                const controller = new AbortController();
                sectionController = controller;
                const { signal } = controller;
                renderer.loadSignal = signal;
                // Abandon sections whose resources never settle; leave their count unknown.
                const loadTimeout = window.setTimeout(() => controller.abort(), 5000);
                try {
                    // Let document-load errors reach our fallback; view.goTo swallows them.
                    await renderer.goTo({ index });
                    const content = renderer.getContents?.()[0];
                    const doc = content?.doc;
                    if (content?.index !== index || !doc?.body)
                        throw new Error('Section did not load');
                    await waitForFonts(doc, signal);
                    // Let Foliate finish its queued styling and resize callbacks.
                    await nextFrame(signal);
                    if (obsolete()) break;
                    const count = foliatePagePosition(measureView)?.total;
                    if (count === undefined) throw new Error('Section has no page layout');
                    // The reader may have visited this section while it was measured.
                    if (!counts.has(index)) remember(index, count);
                    display();
                } finally {
                    window.clearTimeout(loadTimeout);
                    controller.abort();
                    sectionController = undefined;
                }
                await new Promise<void>((resolve) => window.setTimeout(resolve, 0));
            }
        } catch {
            // Stop this run on failure; keep reading available with partial counts.
            if (!obsolete()) failed = true;
        } finally {
            measureView.close();
            // A cancelled document may never reach Foliate's onLoad, which
            // normally releases the previously displayed section.
            for (const section of loadedSections) release(section);
            host.remove();
            running = false;
            save();
            display();
            schedule();
        }
    };

    const resize = new ResizeObserver(refresh);

    return {
        start(): void {
            if (started) return;
            started = true;
            suspended = document.visibilityState !== 'visible';
            view.addEventListener('relocate', refresh);
            resize.observe(view);
            refresh();
        },
        resume(): void {
            suspended = document.visibilityState !== 'visible';
            refresh();
        },
        suspend(finish): void {
            save();
            suspended = true;
            generation++;
            sectionController?.abort();
            window.clearTimeout(measureTimer);
            if (refreshFrame !== undefined) cancelAnimationFrame(refreshFrame);
            refreshFrame = undefined;
            if (finish) {
                stopped = true;
                resize.disconnect();
                view.removeEventListener('relocate', refresh);
            }
        },
        setPreferences(next): void {
            preferences = next;
            refresh();
        },
    };
}

function nextFrame(signal: AbortSignal): Promise<void> {
    return new Promise((resolve, reject) => {
        const abort = () => {
            cancelAnimationFrame(frame);
            reject(new DOMException('Measurement cancelled', 'AbortError'));
        };
        const frame = requestAnimationFrame(() => {
            signal.removeEventListener('abort', abort);
            resolve();
        });
        if (signal.aborted) abort();
        else signal.addEventListener('abort', abort, { once: true });
    });
}
