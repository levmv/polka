import {
    fetchAdminStorageStatus,
    fetchBookJumps,
    fetchBooks,
    fetchCurrentUser,
    fetchUserSettings,
} from '../api';
import { type BookListContext, bookURL, libraryBookListContext } from '../book-list-context';
import { CATALOG_CHANGED } from '../catalog-events';
import { createBookCard } from '../components/book-card';
import { createSelect } from '../components/select';
import { coverUrl } from '../cover';
import { debounce, escapeHtml } from '../dom';
import { errorMessage } from '../errors';
import { icon } from '../icons';
import { beginGlobalLoading } from '../loading-indicator';
import type { RouteCleanup } from '../router';
import { queryTerm } from '../search-query';
import { openSettingsModal } from '../settings';
import { openCreateShelfDialog } from '../shelf-dialog';
import { notifyShelvesChanged } from '../sidebar-shelves';
import { BOOK_UPLOAD_FINISHED, type BookUploadFinishedDetail } from '../sidebar-upload';
import { showToast } from '../toast';
import type {
    AdminStorageStatus,
    BookJump,
    BookSequenceWindow,
    BookSummary,
    UserSettings,
} from '../types';
import { openEditModal } from './book-view';
import {
    initContinueReadingRail,
    resetContinueReadingRail,
    syncContinueReadingRail,
} from './continue-reading';
import { createLibrarySelection, type LibrarySelection } from './library-selection';

const PAGE_SIZE = 50;
const SORT_OPTIONS = [
    { value: 'added', label: 'Recently added' },
    { value: 'title', label: 'Title' },
    { value: 'author', label: 'Author' },
    { value: 'year', label: 'Year' },
    { value: 'series', label: 'Series order' },
];

type LibraryViewMode = 'grid' | 'table';

interface LibraryViewState {
    books: BookSummary[];
    view: LibraryViewMode;
    query: string;
    sort: string;
    shelfId: string;
    pageOffset: number;
    hasMore: boolean;
    loadingMore: boolean;
    userSettings: UserSettings | null;
    canCurateCatalog: boolean;
    canManageStorage: boolean;
    canWriteback: boolean;
    loadToken: number;
    loadingBooks: boolean;
    active: boolean;
    selection: LibrarySelection | null;
    jumpKey: string;
    jumps: BookJump[];
    jumpTotal: number | null;
}

let navigatingAway = false;

if (typeof window !== 'undefined') {
    window.addEventListener('pagehide', () => {
        navigatingAway = true;
    });
    window.addEventListener('pageshow', () => {
        navigatingAway = false;
    });
}

export function initLibrary(): RouteCleanup | undefined {
    const searchInput = document.getElementById('search-input') as HTMLInputElement;
    const params = new URLSearchParams(window.location.search);
    const state: LibraryViewState = {
        books: [],
        view: readLibraryViewMode(),
        query: '',
        sort: '',
        shelfId: params.get('shelf') || '',
        pageOffset: 0,
        hasMore: false,
        loadingMore: false,
        userSettings: null,
        canCurateCatalog: false,
        canManageStorage: false,
        canWriteback: false,
        loadToken: 0,
        loadingBooks: false,
        active: true,
        selection: null,
        jumpKey: '',
        jumps: [],
        jumpTotal: null,
    };
    const cleanup: RouteCleanup[] = [];
    const addCleanup = (fn: RouteCleanup | undefined) => {
        if (fn) cleanup.push(fn);
    };

    addCleanup(initContinueReadingRail());

    let sortValue = normalizeSort(params.get('sort') || 'added');
    const initialOffset = initialLibraryOffset(params, sortValue, state.shelfId);

    const reload = (offset = 0) => {
        if (!state.active) return;
        if (state.shelfId && searchInput?.value.trim()) {
            state.shelfId = '';
            const url = new URL(window.location.href);
            url.searchParams.delete('shelf');
            url.searchParams.set('q', searchInput.value.trim());
            window.history.replaceState(null, '', url);
        }
        updateLibraryBrowseURL(sortValue, offset);
        return loadBooks(state, searchInput?.value || '', sortValue, offset);
    };

    const loadMoreBtn = document.getElementById('load-more-btn');
    const handleLoadMoreClick = () => loadMore(state);
    loadMoreBtn?.addEventListener('click', handleLoadMoreClick);
    addCleanup(() => loadMoreBtn?.removeEventListener('click', handleLoadMoreClick));

    if (searchInput) {
        const handleSearchInput = debounce((_e: Event) => {
            reload();
        }, 200);
        searchInput.addEventListener('input', handleSearchInput);
        addCleanup(() => {
            searchInput.removeEventListener('input', handleSearchInput);
            handleSearchInput.cancel();
        });

        const q = params.get('q');
        if (q) {
            searchInput.value = q;
        }
        addCleanup(setupLibrarySearchShortcuts(searchInput));
        addCleanup(setupSaveSearchButton(searchInput));
    }

    const sortControl = document.getElementById('sort-control');
    if (sortControl) {
        const select = createSelect({
            ariaLabel: 'Sort books',
            value: sortValue,
            options: SORT_OPTIONS,
            onChange: (value) => {
                sortValue = value;
                reload();
            },
        });
        sortControl.appendChild(select.el);
        addCleanup(() => select.destroy());
    }

    const libraryGrid = document.getElementById('library-grid');
    if (libraryGrid) {
        state.selection = createLibrarySelection({
            container: libraryGrid,
            getBooks: () => state.books,
            canWriteback: () => state.canWriteback,
            onApplied: (updated) => {
                for (const b of updated) replaceRenderedBook(state, b);
            },
            onRemoved: (ids) => removeRenderedBooks(state, ids),
        });
        addCleanup(() => state.selection?.destroy());
    }

    void fetchCurrentUser().then((me) => {
        if (!state.active) return;
        if (me.role === 'admin' || me.role === 'member') {
            state.canCurateCatalog = true;
        } else {
            state.canCurateCatalog = false;
        }
        state.canWriteback = false;
        // Selection (bulk edit) is catalog curation; enable only for member/admin.
        state.selection?.setEnabled(state.canCurateCatalog);
        if (me.role === 'admin') {
            state.canManageStorage = true;
            void fetchAdminStorageStatus()
                .then((status) => {
                    if (!state.active) return;
                    applyAdminStorageStatus(state, status);
                })
                .catch(() => {
                    if (!state.active) return;
                    applyAdminStorageStatus(state, null);
                });
        }
        if (!state.loadingBooks && state.books.length === 0) {
            renderBooks(state, state.books);
        }
    });

    const handleBookUploadFinished = (event: Event) => {
        const detail = (event as CustomEvent<BookUploadFinishedDetail>).detail;
        if (!detail) return;
        if (detail.imported.length > 0 || detail.duplicates.length > 0) {
            state.jumpKey = '';
            void reload();
        }
    };
    document.addEventListener(BOOK_UPLOAD_FINISHED, handleBookUploadFinished);
    addCleanup(() => document.removeEventListener(BOOK_UPLOAD_FINISHED, handleBookUploadFinished));

    const handleCatalogChanged = () => {
        state.jumpKey = '';
        void reload();
    };
    document.addEventListener(CATALOG_CHANGED, handleCatalogChanged);
    addCleanup(() => document.removeEventListener(CATALOG_CHANGED, handleCatalogChanged));

    const handleUserSettings = (event: Event) => {
        const settings = (event as CustomEvent<UserSettings>).detail;
        if (!settings || typeof settings.hide_continue_reading !== 'boolean') return;
        applyUserSettings(state, settings, true);
    };
    window.addEventListener('polka:user-settings', handleUserSettings);
    addCleanup(() => window.removeEventListener('polka:user-settings', handleUserSettings));

    const handleAdminStorage = (event: Event) => {
        applyAdminStorageStatus(state, (event as CustomEvent<AdminStorageStatus>).detail || null);
    };
    window.addEventListener('polka:admin-storage', handleAdminStorage);
    addCleanup(() => window.removeEventListener('polka:admin-storage', handleAdminStorage));

    const handleResize = () => renderBookJumpRail(state);
    window.addEventListener('resize', handleResize);
    addCleanup(() => window.removeEventListener('resize', handleResize));
    addCleanup(() => document.body.classList.remove('has-library-jump-rail'));

    void fetchUserSettings()
        .then((settings) => {
            if (!state.active) return;
            applyUserSettings(state, settings, false);
        })
        .catch(() => {
            /* main bootstrap keeps the default theme/settings behavior */
        });

    const gridBtn = document.getElementById('view-grid-btn');
    const tableBtn = document.getElementById('view-table-btn');

    const updateViewButtons = () => {
        if (state.view === 'grid') {
            gridBtn?.classList.add('active');
            tableBtn?.classList.remove('active');
        } else {
            tableBtn?.classList.add('active');
            gridBtn?.classList.remove('active');
        }
    };

    const handleGridClick = () => {
        if (state.view === 'grid') return;
        state.view = 'grid';
        localStorage.setItem('polka-view-mode', 'grid');
        updateViewButtons();
        renderBooks(state, state.books);
    };
    gridBtn?.addEventListener('click', handleGridClick);
    addCleanup(() => gridBtn?.removeEventListener('click', handleGridClick));

    const handleTableClick = () => {
        if (state.view === 'table') return;
        state.view = 'table';
        localStorage.setItem('polka-view-mode', 'table');
        updateViewButtons();
        renderBooks(state, state.books);
    };
    tableBtn?.addEventListener('click', handleTableClick);
    addCleanup(() => tableBtn?.removeEventListener('click', handleTableClick));

    updateViewButtons();
    reload(initialOffset);
    return () => {
        state.active = false;
        state.loadingBooks = false;
        state.loadingMore = false;
        for (let i = cleanup.length - 1; i >= 0; i--) cleanup[i]();
    };
}

function applyAdminStorageStatus(state: LibraryViewState, status: AdminStorageStatus | null): void {
    state.canWriteback = status?.writeback.mode === 'manual';
    state.selection?.refreshActions();
}

function applyUserSettings(
    state: LibraryViewState,
    settings: UserSettings,
    resetRail: boolean,
): void {
    state.userSettings = settings;
    if (resetRail) resetContinueReadingRail();
    syncContinueReadingRail(
        shouldShowContinueReading(state) && !settings.hide_continue_reading,
        isExpectedFetchCancel,
    );
}

function readLibraryViewMode(): LibraryViewMode {
    return localStorage.getItem('polka-view-mode') === 'table' ? 'table' : 'grid';
}

function normalizeSort(value: string): string {
    return SORT_OPTIONS.some((option) => option.value === value) ? value : 'added';
}

function initialLibraryOffset(params: URLSearchParams, sort: string, shelfId: string): number {
    if (params.get('q')?.trim() || shelfId || (sort !== 'title' && sort !== 'author')) return 0;
    const value = Number(params.get('offset'));
    return Number.isSafeInteger(value) && value >= 0 ? value : 0;
}

function updateLibraryBrowseURL(sort: string, offset: number): void {
    const url = new URL(window.location.href);
    if (sort === 'added') {
        url.searchParams.delete('sort');
    } else {
        url.searchParams.set('sort', sort);
    }
    if (offset > 0) {
        url.searchParams.set('offset', String(offset));
    } else {
        url.searchParams.delete('offset');
    }
    window.history.replaceState(null, '', url);
}

function setupLibrarySearchShortcuts(searchInput: HTMLInputElement): RouteCleanup {
    const handleDocumentKeydown = (event: KeyboardEvent) => {
        if (event.defaultPrevented || event.key !== '/') return;
        if (event.altKey || event.ctrlKey || event.metaKey) return;
        const target = event.target instanceof Element ? event.target : null;
        if (isEditableTarget(target) || hasOpenTransientUI()) return;

        event.preventDefault();
        searchInput.focus();
        searchInput.select();
    };
    document.addEventListener('keydown', handleDocumentKeydown);

    const handleSearchKeydown = (event: KeyboardEvent) => {
        if (event.key !== 'Escape') return;
        event.preventDefault();
        if (searchInput.value !== '') {
            clearLibrarySearch(searchInput);
            return;
        }
        searchInput.blur();
    };
    searchInput.addEventListener('keydown', handleSearchKeydown);

    return () => {
        document.removeEventListener('keydown', handleDocumentKeydown);
        searchInput.removeEventListener('keydown', handleSearchKeydown);
    };
}

function clearLibrarySearch(searchInput: HTMLInputElement): void {
    const url = new URL(window.location.href);
    url.searchParams.delete('q');
    window.history.replaceState(null, '', url);
    searchInput.value = '';
    searchInput.dispatchEvent(new Event('input', { bubbles: true }));
}

function setupSaveSearchButton(searchInput: HTMLInputElement): RouteCleanup | undefined {
    const btn = document.getElementById('save-search-btn') as HTMLButtonElement | null;
    if (!btn) return undefined;

    const update = () => {
        btn.hidden = searchInput.value.trim() === '';
    };
    const handleClick = async () => {
        const query = searchInput.value.trim();
        if (!query) return;
        btn.disabled = true;
        try {
            const me = await fetchCurrentUser();
            const shared = me.role === 'admin' || me.role === 'member';
            const shelf = await openCreateShelfDialog({
                currentUser: me,
                kind: 'query',
                initialQuery: query,
                defaultShared: shared,
            });
            if (!shelf) {
                btn.disabled = false;
                return;
            }
            notifyShelvesChanged();
            window.location.href = `/?shelf=${encodeURIComponent(shelf.id)}`;
        } catch (e) {
            console.error('Failed to save search shelf:', e);
            showToast(errorMessage(e, 'Save search failed'), { type: 'error' });
            btn.disabled = false;
        }
    };

    searchInput.addEventListener('input', update);
    btn.addEventListener('click', handleClick);
    update();

    return () => {
        searchInput.removeEventListener('input', update);
        btn.removeEventListener('click', handleClick);
    };
}

function isEditableTarget(target: Element | null): boolean {
    const editable = target?.closest('input, textarea, select, [contenteditable]');
    if (!editable) return false;
    if (editable instanceof HTMLElement && editable.isContentEditable) return true;
    return editable.matches('input, textarea, select');
}

function hasOpenTransientUI(): boolean {
    return Boolean(
        document.querySelector(
            '.modal-backdrop[aria-hidden="false"], .floating-menu:not([hidden]), .floating-panel:not([hidden])',
        ),
    );
}

async function loadBooks(
    state: LibraryViewState,
    query: string = '',
    sort: string = '',
    offset = 0,
) {
    const token = ++state.loadToken;
    // A new query/sort/shelf replaces the loaded set; selection must not linger.
    state.selection?.clearSelection();
    const finishGlobalLoading = beginGlobalLoading();
    state.loadingBooks = true;
    state.loadingMore = false;
    setLibraryResultsLoading(true);
    updateLoadMore(state);
    state.query = query;
    state.sort = sort;
    state.pageOffset = offset;
    syncBookJumps(state);
    syncContinueReadingRail(
        shouldShowContinueReading(state, query) &&
            state.userSettings?.hide_continue_reading === false,
        isExpectedFetchCancel,
    );
    try {
        const books = await fetchBooks(query, sort, PAGE_SIZE, offset, state.shelfId);
        if (!state.active || token !== state.loadToken) return;
        state.books = books;
        state.hasMore = hasMoreBooks(state, state.books.length);
        renderBooks(state, state.books);
        renderBookJumpRail(state);
        updateLoadMore(state);
    } catch (e: unknown) {
        // An aborted fetch is the expected outcome of cancelling a stale
        // request (newer query/sort superseded it). A full-page navigation can
        // also reject in-flight fetches as a TypeError in Chromium.
        if (state.active && token === state.loadToken && !isExpectedFetchCancel(e)) {
            console.error('Failed to fetch books:', e);
        }
    } finally {
        finishGlobalLoading();
        if (state.active && token === state.loadToken) {
            state.loadingBooks = false;
            setLibraryResultsLoading(false);
            updateLoadMore(state);
        }
    }
}

function setLibraryResultsLoading(loading: boolean): void {
    const container = document.getElementById('library-grid');
    if (!container) return;
    container.classList.toggle('library-results-loading', loading);
    container.setAttribute('aria-busy', loading ? 'true' : 'false');
}

function shouldShowContinueReading(state: LibraryViewState, query: string = state.query): boolean {
    return state.shelfId === '' && query.trim() === '';
}

async function loadMore(state: LibraryViewState) {
    if (!state.active || state.loadingBooks || state.loadingMore || !state.hasMore) return;
    const token = state.loadToken;
    state.loadingMore = true;
    updateLoadMore(state);
    try {
        const offset = state.pageOffset + state.books.length;
        const books = await fetchBooks(state.query, state.sort, PAGE_SIZE, offset, state.shelfId);
        if (!state.active || token !== state.loadToken) return;
        const newBooks = books;
        state.books = state.books.concat(newBooks);
        appendBooks(state, newBooks);
        state.hasMore = hasMoreBooks(state, newBooks.length);
    } catch (e: unknown) {
        if (state.active && token === state.loadToken && !isExpectedFetchCancel(e)) {
            console.error('Failed to load more books:', e);
            showToast(errorMessage(e, 'Failed to load more books'), { type: 'error' });
        }
    } finally {
        if (state.active && token === state.loadToken) {
            state.loadingMore = false;
            updateLoadMore(state);
        }
    }
}

function hasMoreBooks(state: LibraryViewState, received: number): boolean {
    if (received < PAGE_SIZE) return false;
    if (state.jumpTotal == null) return true;
    return state.pageOffset + state.books.length < state.jumpTotal;
}

function isExpectedFetchCancel(e: unknown): boolean {
    const name =
        typeof e === 'object' && e !== null && 'name' in e ? (e as { name?: unknown }).name : '';
    return name === 'AbortError' || navigatingAway;
}

function updateLoadMore(state: LibraryViewState) {
    const container = document.getElementById('load-more-container');
    const btn = document.getElementById('load-more-btn') as HTMLButtonElement | null;
    if (!container || !btn) return;
    container.hidden = !state.hasMore || state.books.length === 0;
    btn.disabled = state.loadingBooks || state.loadingMore;
    btn.textContent = state.loadingMore ? 'Loading…' : 'Load more';
}

function renderBooks(state: LibraryViewState, books: BookSummary[]) {
    const container = document.getElementById('library-grid');
    if (!container) return;

    if (state.view === 'grid') {
        container.className = 'library-grid';
        renderGrid(state, container, books);
    } else {
        container.className = 'library-table-container';
        renderTable(state, container, books);
    }
    // container.className was just reset; re-apply selection styling if armed.
    state.selection?.syncAfterRender();
}

function currentLibraryContext(state: LibraryViewState): BookListContext {
    return libraryBookListContext(state.query, state.sort, state.shelfId, state.pageOffset);
}

function currentLibrarySequence(
    state: LibraryViewState,
    workID: string,
): BookSequenceWindow | null {
    // A jumped page does not contain the preceding slice, so let the edit
    // controller fetch its bounded server-side window instead of temporarily
    // presenting the first visible book as the first book in the library.
    if (state.pageOffset > 0) return null;
    const currentIndex = state.books.findIndex((book) => book.id === workID);
    if (currentIndex < 0) return null;
    return {
        items: state.books.map((book) => ({ id: book.id, title: book.title })),
        current_index: currentIndex,
        total:
            state.jumpTotal ?? (state.hasMore ? undefined : state.pageOffset + state.books.length),
    };
}

function syncBookJumps(state: LibraryViewState): void {
    const key =
        state.shelfId === '' &&
        state.query.trim() === '' &&
        (state.sort === 'title' || state.sort === 'author')
            ? state.sort
            : '';
    if (key === state.jumpKey) {
        renderBookJumpRail(state);
        return;
    }

    state.jumpKey = key;
    state.jumps = [];
    state.jumpTotal = null;
    renderBookJumpRail(state);
    if (!key) return;

    void fetchBookJumps(key)
        .then((result) => {
            if (!state.active || state.jumpKey !== key) return;
            state.jumps = result.items;
            state.jumpTotal = result.total;
            state.hasMore = hasMoreBooks(state, state.books.length);
            renderBookJumpRail(state);
            updateLoadMore(state);
        })
        .catch((error) => {
            if (!state.active || state.jumpKey !== key) return;
            console.error('Failed to fetch book jumps:', error);
            state.jumps = [];
            state.jumpTotal = null;
            renderBookJumpRail(state);
        });
}

function renderBookJumpRail(state: LibraryViewState): void {
    const rail = document.getElementById('library-jump-rail');
    if (!rail) return;
    const jumps = sampleBookJumps(state.jumps, jumpRailCapacity());
    const visible = state.jumpKey !== '' && jumps.length > 1;
    rail.hidden = !visible;
    document.body.classList.toggle('has-library-jump-rail', visible);
    rail.replaceChildren();
    if (!visible) return;

    let activeOffset = jumps[0].offset;
    for (const jump of jumps) {
        if (jump.offset <= state.pageOffset) activeOffset = jump.offset;
    }
    const kind = state.sort === 'author' ? 'authors' : 'titles';
    for (const jump of jumps) {
        const button = document.createElement('button');
        button.type = 'button';
        button.className = 'library-jump-button';
        button.textContent = jump.label;
        button.classList.toggle('active', jump.offset === activeOffset);
        button.setAttribute('aria-label', `Jump to ${kind} starting with ${jump.label}`);
        button.setAttribute('aria-current', jump.offset === activeOffset ? 'true' : 'false');
        button.addEventListener('click', () => {
            if (jump.offset === state.pageOffset || state.loadingBooks) return;
            updateLibraryBrowseURL(state.sort, jump.offset);
            window.scrollTo({ top: 0, behavior: 'auto' });
            void loadBooks(state, '', state.sort, jump.offset);
        });
        rail.appendChild(button);
    }
}

function jumpRailCapacity(): number {
    return Math.max(6, Math.min(28, Math.floor((window.innerHeight - 180) / 32)));
}

function sampleBookJumps(jumps: BookJump[], capacity: number): BookJump[] {
    if (jumps.length <= capacity) return jumps;
    const out: BookJump[] = [];
    for (let i = 0; i < capacity; i += 1) {
        const index = Math.round((i * (jumps.length - 1)) / (capacity - 1));
        const jump = jumps[index];
        if (jump && out[out.length - 1]?.offset !== jump.offset) out.push(jump);
    }
    return out;
}

// Swap only the edited book's element so an edit never re-renders (and reflows
// the covers of) every other book on screen. Both views carry the book id on
// their element; if it isn't currently rendered, there is nothing to do.
function replaceRenderedBook(state: LibraryViewState, updated: BookSummary): void {
    state.books = state.books.map((book) => (book.id === updated.id ? updated : book));

    if (state.view === 'table') {
        const row = document.querySelector<HTMLTableRowElement>(
            `.table-row[data-id=${CSS.escape(updated.id)}]`,
        );
        row?.replaceWith(createBookRow(state, updated));
        state.selection?.syncAfterRender();
        return;
    }

    const card = document.querySelector<HTMLElement>(
        `.book-card[data-id=${CSS.escape(updated.id)}]`,
    );
    card?.replaceWith(createBookCard(updated, currentLibraryContext(state)));
    state.selection?.syncAfterRender();
}

// removeRenderedBooks drops the given works from view state and the DOM without
// re-rendering the rest, then re-syncs selection so no trashed id lingers.
function removeRenderedBooks(state: LibraryViewState, ids: string[]): void {
    if (ids.length === 0) return;
    const drop = new Set(ids);
    state.books = state.books.filter((book) => !drop.has(book.id));
    const container = document.getElementById('library-grid');
    const selector = state.view === 'table' ? '.table-row' : '.book-card';
    for (const id of ids) {
        container?.querySelector<HTMLElement>(`${selector}[data-id=${CSS.escape(id)}]`)?.remove();
    }
    state.selection?.syncAfterRender();
    if (state.books.length === 0) renderBooks(state, state.books);
}

// appendBooks adds only the newly-loaded books to the existing DOM, so a
// "Load more" never re-renders or reflows the books already on screen.
function appendBooks(state: LibraryViewState, newBooks: BookSummary[]) {
    const container = document.getElementById('library-grid');
    if (!container || newBooks.length === 0) return;

    if (state.view === 'grid') {
        for (const b of newBooks) {
            container.appendChild(createBookCard(b, currentLibraryContext(state)));
        }
        return;
    }

    const tbody = container.querySelector('tbody');
    if (tbody) {
        for (const b of newBooks) {
            tbody.appendChild(createBookRow(state, b));
        }
    } else {
        renderBooks(state, state.books);
    }
    state.selection?.syncAfterRender();
}

function renderGrid(state: LibraryViewState, container: HTMLElement, books: BookSummary[]) {
    container.replaceChildren();

    if (books.length === 0) {
        container.appendChild(createLibraryEmptyState(state));
        return;
    }

    books.forEach((b) => {
        container.appendChild(createBookCard(b, currentLibraryContext(state)));
    });
}

function renderTable(state: LibraryViewState, container: HTMLElement, books: BookSummary[]) {
    container.replaceChildren();

    if (books.length === 0) {
        container.appendChild(createLibraryEmptyState(state));
        return;
    }

    const table = document.createElement('table');
    table.className = 'library-table';
    table.innerHTML = `
        <thead>
            <tr>
                <th class="col-select"><label class="table-select-label"><input type="checkbox" class="table-select-all" aria-label="Select all on page"></label></th>
                <th class="col-cover"></th>
                <th class="col-title">Title</th>
                <th class="col-author">Author</th>
                <th class="col-series">Series</th>
                <th class="col-year">Year</th>
                <th class="col-tags">Tags</th>
                <th class="col-format">Format</th>
                <th class="col-actions"></th>
            </tr>
        </thead>
        <tbody></tbody>
    `;

    const tbody = table.querySelector('tbody')!;
    books.forEach((b) => {
        tbody.appendChild(createBookRow(state, b));
    });

    container.appendChild(table);
}

function createLibraryEmptyState(state: LibraryViewState): HTMLElement {
    const el = document.createElement('section');
    el.className = 'library-empty-state';
    el.setAttribute('aria-live', 'polite');

    const title = document.createElement('h2');
    const body = document.createElement('p');
    const actions = document.createElement('div');
    actions.className = 'library-empty-actions';
    const action = document.createElement('button');
    action.type = 'button';
    action.className = 'library-empty-action';

    const query = state.query.trim();
    if (query) {
        title.textContent = 'No matches';
        body.textContent = `No books match “${query}”.`;
        action.textContent = 'Clear search';
        action.addEventListener('click', () => {
            const input = document.getElementById('search-input') as HTMLInputElement | null;
            if (!input) return;
            const url = new URL(window.location.href);
            url.searchParams.delete('q');
            window.history.replaceState(null, '', url);
            input.value = '';
            input.dispatchEvent(new Event('input'));
            input.focus();
        });
    } else if (state.shelfId) {
        title.textContent = 'Shelf is empty';
        body.textContent = 'No books are on this shelf.';
        action.textContent = 'Library';
        action.addEventListener('click', () => {
            window.location.href = '/';
        });
    } else {
        title.textContent = 'No books yet';
        if (state.canCurateCatalog) {
            body.textContent = state.canManageStorage
                ? 'Add a book here, or import a folder from this server.'
                : 'Add a book here.';
            action.textContent = 'Add book';
            action.addEventListener('click', () => {
                document.getElementById('book-upload-input')?.click();
            });
            actions.append(action);
            if (state.canManageStorage) {
                const importFolder = document.createElement('button');
                importFolder.type = 'button';
                importFolder.className = 'library-empty-action library-empty-secondary-action';
                importFolder.textContent = 'Import a folder';
                importFolder.addEventListener('click', async () => {
                    try {
                        const me = await fetchCurrentUser();
                        openSettingsModal(me, 'storage');
                    } catch (err) {
                        showToast(errorMessage(err, 'Failed to open settings'), { type: 'error' });
                    }
                });
                actions.append(importFolder);
            }
        } else {
            body.textContent = 'No books are available in this library yet.';
            action.hidden = true;
        }
    }

    if (!actions.childElementCount && !action.hidden) actions.append(action);
    el.append(title, body);
    if (actions.childElementCount) el.append(actions);
    return el;
}

// How many tags a table row shows before collapsing the rest behind a "+N".
const TABLE_TAG_LIMIT = 3;

// Add (or keep) an author:"Name" filter on the current search and reload. The
// author cells are clickable but not styled as links, to avoid table noise.
function applyAuthorFilter(name: string): void {
    const input = document.getElementById('search-input') as HTMLInputElement | null;
    if (!input) return;
    const token = queryTerm('author', name);
    const current = input.value.trim();
    const next = !current ? token : current.includes(token) ? current : `${current} ${token}`;
    if (next === input.value) return;
    input.value = next;
    input.dispatchEvent(new Event('input'));
}

function authorCellHtml(b: BookSummary): string {
    const names = b.authors_list.map((author) => author.name).filter(Boolean);
    if (names.length === 0) return '';
    return names
        .map(
            (n) =>
                `<span class="table-author-link" role="button" tabindex="0" data-author="${escapeHtml(n)}">${escapeHtml(n)}</span>`,
        )
        .join(' &amp; ');
}

function tagsCellHtml(b: BookSummary): string {
    const tags = b.tags
        ? b.tags
              .split(',')
              .map((t) => t.trim())
              .filter(Boolean)
        : [];
    if (tags.length === 0) return '';

    const shown = tags.slice(0, TABLE_TAG_LIMIT);
    const hidden = tags.slice(TABLE_TAG_LIMIT);
    // Plain, compact text — a table row isn't the book page, so no pills. Each
    // hidden tag carries its own leading separator so revealing reads cleanly.
    const sep = '<span class="table-tag-sep">·</span>';
    const tag = (t: string) => `<span class="table-tag-text">${escapeHtml(t)}</span>`;

    let html = `<span class="table-tags-text">${shown.map(tag).join(sep)}`;
    html += hidden
        .map((t) => `<span class="table-tag-hidden" hidden>${sep}${tag(t)}</span>`)
        .join('');
    html += '</span>';
    if (hidden.length > 0) {
        html += ` <button type="button" class="table-tag-more" aria-label="Show ${hidden.length} more tags">+${hidden.length}</button>`;
    }
    return html;
}

function createBookRow(state: LibraryViewState, b: BookSummary): HTMLTableRowElement {
    const tr = document.createElement('tr');
    tr.className = 'table-row';
    tr.dataset.id = b.id;

    const href = escapeHtml(bookURL(b.id, currentLibraryContext(state)));
    const coverHtml = `<a href="${href}" class="table-cover-link"><img loading="lazy" src="${coverUrl(b.id, b.cover_version, 'thumb')}" class="table-cover-image" alt=""></a>`;

    let seriesHtml = '';
    if (b.series) {
        seriesHtml = escapeHtml(b.series);
        if (b.series_index) {
            seriesHtml += ` #${b.series_index}`;
        }
    }

    const formats = b.assets
        ? b.assets
              .map(
                  (a) =>
                      `<a href="/download/${a.id}" class="table-format-badge" target="_blank" rel="noopener noreferrer">${escapeHtml(a.extension.toUpperCase().replace('.', ''))}</a>`,
              )
              .join('')
        : '';

    tr.innerHTML = `
        <td class="col-select"><label class="table-select-label"><input type="checkbox" class="table-select-row" aria-label="Select ${escapeHtml(b.title)}"></label></td>
        <td class="col-cover">${coverHtml}</td>
        <td class="col-title"><a href="${href}" class="table-title-link">${escapeHtml(b.title)}</a></td>
        <td class="col-author">${authorCellHtml(b)}</td>
        <td class="col-series">${seriesHtml}</td>
        <td class="col-year">${escapeHtml(b.year || '')}</td>
        <td class="col-tags">${tagsCellHtml(b)}</td>
        <td class="col-format">${formats}</td>
        <td class="col-actions">
            <button class="btn-quick-edit" title="Quick Edit" aria-label="Quick Edit">
                ${icon('edit', 18)}
            </button>
        </td>
    `;
    for (const el of tr.querySelectorAll<HTMLElement>('.table-author-link')) {
        const author = el.dataset.author || '';
        el.addEventListener('click', () => applyAuthorFilter(author));
        el.addEventListener('keydown', (e) => {
            if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault();
                applyAuthorFilter(author);
            }
        });
    }

    const moreBtn = tr.querySelector('.table-tag-more');
    moreBtn?.addEventListener('click', () => {
        for (const el of tr.querySelectorAll<HTMLElement>('.table-tag-hidden')) {
            el.hidden = false;
        }
        moreBtn.remove();
    });

    const editBtn = tr.querySelector('.btn-quick-edit') as HTMLButtonElement;
    editBtn.addEventListener('click', (e) => {
        e.preventDefault();
        openEditModal(
            b,
            (updated) => replaceRenderedBook(state, updated),
            currentLibraryContext(state),
            currentLibrarySequence(state, b.id),
        );
    });

    return tr;
}
