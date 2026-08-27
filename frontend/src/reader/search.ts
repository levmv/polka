import { focusReaderSurface, shouldIgnoreReaderShortcut } from './controls';
import type { FoliateSearchResult, FoliateSearchYield, FoliateViewElement } from './foliate-engine';
import {
    appendExcerpt,
    createSearchPanel,
    formatResultCount,
    isReaderSearchQueryReady,
    normalizeReaderSearchQuery,
    openSearch,
    READER_SEARCH_DEBOUNCE_MS,
    READER_SEARCH_TOO_SHORT_MESSAGE,
    type ReaderSearchControls,
} from './search-panel';

interface ReaderSearchOptions {
    onNavigate?: () => void;
}

interface SearchState {
    sequence: number;
    searching: boolean;
    resultCount: number;
    groupCount: number;
    debounce: number;
    activeCFI: string;
}

export interface ReaderSearchController {
    openWithQuery: (query: string) => void;
    clear: () => void;
}

export function wireReaderSearch(
    page: HTMLElement,
    view: FoliateViewElement,
    options: ReaderSearchOptions = {},
): ReaderSearchController {
    const actions = page.querySelector<HTMLElement>('.reader-actions');
    if (!actions) {
        return { openWithQuery: () => undefined, clear: () => undefined };
    }

    const controls = createSearchPanel(page);
    actions.prepend(controls.toggle);

    const state: SearchState = {
        sequence: 0,
        searching: false,
        resultCount: 0,
        groupCount: 0,
        debounce: 0,
        activeCFI: '',
    };

    controls.toggle.addEventListener('click', () => {
        if (controls.panel.hidden) openSearch(page, controls, true);
        else closeSearch(page, view, controls, state, true);
    });
    controls.backdrop.addEventListener('click', () =>
        closeSearch(page, view, controls, state, true),
    );
    controls.panel
        .querySelector<HTMLButtonElement>('[data-reader-search-close]')
        ?.addEventListener('click', () => closeSearch(page, view, controls, state, true));
    document.addEventListener('keydown', (event) => {
        if (event.key === '/' && controls.panel.hidden && !shouldIgnoreReaderShortcut(event)) {
            event.preventDefault();
            openSearch(page, controls, true);
            return;
        }
        if (event.key !== 'Escape' || controls.panel.hidden) return;
        event.preventDefault();
        closeSearch(page, view, controls, state, true);
    });

    controls.input.addEventListener('input', () => {
        const query = controls.input.value;
        if (state.debounce) {
            window.clearTimeout(state.debounce);
            state.debounce = 0;
        }
        if (!query.trim()) {
            runSearch(view, controls, state, '', options).catch((e) =>
                console.error('Failed to clear reader search:', e),
            );
            return;
        }
        if (!isReaderSearchQueryReady(normalizeReaderSearchQuery(query))) {
            runSearch(view, controls, state, query, options).catch((e) =>
                console.error('Failed to limit reader search:', e),
            );
            return;
        }
        state.debounce = window.setTimeout(() => {
            state.debounce = 0;
            runSearch(view, controls, state, query, options).catch((e) =>
                console.error('Failed to search reader:', e),
            );
        }, READER_SEARCH_DEBOUNCE_MS);
    });
    controls.input.addEventListener('keydown', (event) => {
        if (event.key !== 'Enter') return;
        event.preventDefault();
        if (state.debounce) {
            window.clearTimeout(state.debounce);
            state.debounce = 0;
        }
        runSearch(view, controls, state, controls.input.value, options).catch((e) =>
            console.error('Failed to search reader:', e),
        );
    });
    view.addEventListener('relocate', () => markVisibleSearchResult(controls, state.activeCFI));

    return {
        openWithQuery: (query: string): void => {
            const normalized = normalizeReaderSearchQuery(query);
            if (!normalized) return;
            openSearch(page, controls, false);
            controls.input.value = normalized;
            runSearch(view, controls, state, normalized, options).catch((e) =>
                console.error('Failed to search reader:', e),
            );
        },
        clear: (): void => clearSearch(view, controls, state),
    };
}

function closeSearch(
    page: HTMLElement,
    view: FoliateViewElement,
    controls: ReaderSearchControls,
    state: SearchState,
    restoreFocus: boolean,
): void {
    page.classList.remove('reader-search-open');
    controls.panel.hidden = true;
    controls.backdrop.hidden = true;
    controls.toggle.setAttribute('aria-expanded', 'false');
    clearSearch(view, controls, state);
    if (restoreFocus) focusReaderSurface(page);
}

async function runSearch(
    view: FoliateViewElement,
    controls: ReaderSearchControls,
    state: SearchState,
    rawQuery: string,
    options: ReaderSearchOptions,
): Promise<void> {
    const query = normalizeReaderSearchQuery(rawQuery);
    const sequence = ++state.sequence;
    state.searching = false;
    state.resultCount = 0;
    state.groupCount = 0;
    state.activeCFI = '';
    controls.results.replaceChildren();

    if (!query) {
        clearSearch(view, controls, state);
        controls.status.textContent = 'Type to search this book.';
        return;
    }
    if (!isReaderSearchQueryReady(query)) {
        view.clearSearch?.();
        controls.panel.dataset.readerSearchResults = '0';
        controls.status.textContent = READER_SEARCH_TOO_SHORT_MESSAGE;
        return;
    }

    if (!view.search) {
        controls.status.textContent = 'Search is not available for this book.';
        state.searching = false;
        return;
    }

    state.searching = true;
    controls.status.textContent = 'Searching...';
    let completed = false;
    try {
        for await (const item of view.search({
            query,
            matchCase: false,
            matchDiacritics: false,
            matchWholeWords: false,
        })) {
            if (sequence !== state.sequence) break;
            if (item === 'done') {
                completed = true;
                break;
            }
            renderSearchYield(controls, state, item, view, options);
        }
    } catch (e) {
        if (sequence !== state.sequence) return;
        console.error('Failed to search reader:', e);
        controls.status.textContent = 'Search failed.';
        state.searching = false;
        return;
    }

    if (sequence !== state.sequence) return;
    state.searching = false;
    updateSearchStatus(controls, state, completed ? 1 : undefined);
}

function renderSearchYield(
    controls: ReaderSearchControls,
    state: SearchState,
    item: Exclude<FoliateSearchYield, 'done'>,
    view: FoliateViewElement,
    options: ReaderSearchOptions,
): void {
    if ('progress' in item) {
        updateSearchStatus(controls, state, item.progress);
        return;
    }
    if ('subitems' in item) {
        appendSearchGroup(controls, state, item.label, item.subitems, view, options);
        updateSearchStatus(controls, state);
        return;
    }
    appendSearchGroup(controls, state, '', [item], view, options);
    updateSearchStatus(controls, state);
}

function appendSearchGroup(
    controls: ReaderSearchControls,
    state: SearchState,
    rawLabel: string | undefined,
    results: FoliateSearchResult[],
    view: FoliateViewElement,
    options: ReaderSearchOptions,
): void {
    if (results.length === 0) return;

    const group = document.createElement('li');
    group.className = 'reader-search-group';

    const title = document.createElement('h3');
    title.className = 'reader-search-group-title';
    title.textContent = rawLabel?.trim() || `Section ${state.groupCount + 1}`;
    group.append(title);

    const list = document.createElement('ol');
    list.className = 'reader-search-group-list';
    for (const result of results) {
        list.append(createSearchResultItem(result, view, options, state, controls));
        state.resultCount++;
    }
    group.append(list);
    controls.results.append(group);
    state.groupCount++;
    controls.results.hidden = false;
}

function createSearchResultItem(
    result: FoliateSearchResult,
    view: FoliateViewElement,
    options: ReaderSearchOptions,
    state: SearchState,
    controls: ReaderSearchControls,
): HTMLLIElement {
    const row = document.createElement('li');
    row.className = 'reader-search-result';

    const button = document.createElement('button');
    button.className = 'reader-search-result-btn';
    button.type = 'button';
    button.dataset.readerSearchCfi = result.cfi;
    appendExcerpt(button, result.excerpt);
    button.addEventListener('click', () => {
        options.onNavigate?.();
        state.activeCFI = result.cfi;
        markVisibleSearchResult(controls, result.cfi);
        view.goTo(result.cfi).catch((e) => console.error('Failed to navigate reader search:', e));
    });

    row.append(button);
    return row;
}

function updateSearchStatus(
    controls: ReaderSearchControls,
    state: SearchState,
    progress?: number,
): void {
    const count = state.resultCount;
    controls.panel.dataset.readerSearchResults = String(count);
    if (state.searching) {
        const percent =
            typeof progress === 'number' && progress >= 0 && progress < 1
                ? ` ${Math.max(1, Math.round(progress * 100))}%`
                : '';
        controls.status.textContent =
            count === 0
                ? `Searching...${percent}`
                : `Searching... ${formatResultCount(count)}${percent}`;
        return;
    }
    controls.status.textContent = count === 0 ? 'No matches.' : formatResultCount(count);
}

function clearSearch(
    view: FoliateViewElement,
    controls: ReaderSearchControls,
    state: SearchState,
): void {
    state.sequence++;
    state.searching = false;
    state.resultCount = 0;
    state.groupCount = 0;
    state.activeCFI = '';
    if (state.debounce) {
        window.clearTimeout(state.debounce);
        state.debounce = 0;
    }
    controls.results.replaceChildren();
    controls.results.hidden = false;
    controls.status.textContent = 'Type to search this book.';
    controls.input.value = '';
    controls.panel.dataset.readerSearchResults = '0';
    view.clearSearch?.();
}

function markVisibleSearchResult(controls: ReaderSearchControls, cfi: string): void {
    for (const button of controls.results.querySelectorAll<HTMLButtonElement>(
        '[data-reader-search-cfi]',
    )) {
        const active = Boolean(cfi) && button.dataset.readerSearchCfi === cfi;
        button.classList.toggle('active', active);
        if (active) button.setAttribute('aria-current', 'location');
        else button.removeAttribute('aria-current');
    }
}
