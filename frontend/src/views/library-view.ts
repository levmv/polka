import {
    fetchAdminStorageStatus,
    fetchBookJumps,
    fetchBooks,
    fetchCurrentUser,
    fetchUserSettings,
} from '../api';
import { type BookListContext, bookURL, libraryBookListContext } from '../book-list-context';
import { CATALOG_CHANGED, type CatalogChange } from '../catalog-events';
import { createBookCard } from '../components/book-card';
import { createSelect, type ManagedSelect } from '../components/select';
import { coverUrl } from '../cover';
import { debounce, escapeHtml } from '../dom';
import { errorMessage } from '../errors';
import { icon } from '../icons';
import { beginGlobalLoading } from '../loading-indicator';
import {
    navigateApp,
    type RouteCleanup,
    type RouteController,
    replaceLocationURL,
    type ScrollPosition,
} from '../router';
import { queryTerm } from '../search-query';
import { openSettingsModal } from '../settings';
import { openCreateShelfDialog } from '../shelf-dialog';
import { notifyShelvesChanged } from '../sidebar-shelves';
import { showToast } from '../toast';
import type {
    AdminStorageStatus,
    BookJump,
    BookSequenceWindow,
    BookSummary,
    UserSettings,
} from '../types';
import { openEditModal } from './book-view';
import { type ContinueReadingRail, createContinueReadingRail } from './continue-reading';
import { createLibrarySelection, type LibrarySelection } from './library-selection';
import { createReturnPosition, type ReturnPosition } from './return-position';

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
    // Router-owned route root. Every lookup this view makes is scoped to it.
    root: HTMLElement;
    // 'suspended' means the root is detached: state and DOM are intact, but this
    // instance owns nothing on the visible page and must not touch its URL.
    phase: 'active' | 'suspended' | 'destroyed';
    // Putting the reader back where they were; see return-position.ts.
    returnPosition: ReturnPosition;
    // A change arrived that cannot be patched into the retained result. The
    // authoritative rebuild happens on resume, never as a jump on return.
    dirty: boolean;
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
    // Owned by this instance: a stale load is cancelled here, never by whatever
    // view happens to request books next.
    booksAbort: AbortController | null;
    rail: ContinueReadingRail;
    // Completion for the page-level busy indicator, released on suspend.
    finishLoading: (() => void) | null;
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

// The saved position for the current history entry, written by the navigation
// layer in main.ts. A view whose content arrives asynchronously has to apply it
// itself: main.ts restores as soon as mount() resolves, and scrolling against a
// document that is still one empty viewport tall clamps to the top.
function restoreSavedScroll(): void {
    const state = window.history.state as { polkaScroll?: { x: number; y: number } } | null;
    const scroll = state?.polkaScroll;
    if (!scroll || typeof scroll.x !== 'number' || typeof scroll.y !== 'number') return;
    window.scrollTo(scroll.x, scroll.y);
}

export function initLibrary(root: HTMLElement): RouteController {
    const searchInput = root.querySelector<HTMLInputElement>('#search-input');
    const params = new URLSearchParams(window.location.search);
    const state: LibraryViewState = {
        root,
        phase: 'active',
        dirty: false,
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
        booksAbort: null,
        rail: createContinueReadingRail(root, isExpectedFetchCancel),
        returnPosition: createReturnPosition({
            root,
            renderedBookSelector: () => renderedBookSelector(state),
            isActive: () => state.phase === 'active',
        }),
        finishLoading: null,
        selection: null,
        jumpKey: '',
        jumps: [],
        jumpTotal: null,
    };
    const cleanup: RouteCleanup[] = [];
    const addCleanup = (fn: RouteCleanup | undefined) => {
        if (fn) cleanup.push(fn);
    };

    addCleanup(() => state.rail.destroy());

    let sortValue = normalizeSort(params.get('sort') || 'added');
    const initialOffset = initialLibraryOffset(params, sortValue, state.shelfId);

    // Rebuild the retained view: same offset, and a limit covering everything
    // that had been loaded, so a coarse change does not shrink the sequence.
    const rebuildRetained = () => {
        const limit = Math.max(PAGE_SIZE, Math.ceil(state.books.length / PAGE_SIZE) * PAGE_SIZE);
        return loadBooks(state, searchInput?.value || '', sortValue, state.pageOffset, limit);
    };

    const reload = (offset = 0) => {
        if (state.phase !== 'active') return;
        if (state.shelfId && searchInput?.value.trim()) {
            state.shelfId = '';
            const url = new URL(window.location.href);
            url.searchParams.delete('shelf');
            url.searchParams.set('q', searchInput.value.trim());
            replaceLocationURL(url);
        }
        updateLibraryBrowseURL(sortValue, offset);
        return loadBooks(state, searchInput?.value || '', sortValue, offset);
    };

    const loadMoreBtn = root.querySelector('#load-more-btn');
    const handleLoadMoreClick = () => loadMore(state);
    loadMoreBtn?.addEventListener('click', handleLoadMoreClick);
    addCleanup(() => loadMoreBtn?.removeEventListener('click', handleLoadMoreClick));

    // Held so suspend() can unschedule a pending search: a debounce that fired
    // from a detached library would rewrite the book page's URL.
    let cancelPendingSearch: (() => void) | null = null;
    let sortSelect: ManagedSelect | null = null;

    if (searchInput) {
        const handleSearchInput = debounce((_e: Event) => {
            reload();
        }, 200);
        cancelPendingSearch = () => handleSearchInput.cancel();
        searchInput.addEventListener('input', handleSearchInput);
        addCleanup(() => {
            searchInput.removeEventListener('input', handleSearchInput);
            handleSearchInput.cancel();
        });

        const q = params.get('q');
        if (q) {
            searchInput.value = q;
        }
        addCleanup(setupLibrarySearchShortcuts(state, searchInput));
        addCleanup(setupSaveSearchButton(root, searchInput));
    }

    const sortControl = root.querySelector('#sort-control');
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
        sortSelect = select;
        addCleanup(() => select.destroy());
    }

    const libraryGrid = root.querySelector<HTMLElement>('#library-grid');
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
        if (state.phase === 'destroyed') return;
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
                    if (state.phase === 'destroyed') return;
                    applyAdminStorageStatus(state, status);
                })
                .catch(() => {
                    if (state.phase === 'destroyed') return;
                    applyAdminStorageStatus(state, null);
                });
        }
        if (!state.loadingBooks && state.books.length === 0) {
            renderBooks(state, state.books);
        }
    });

    // Rebuild from the server: now if this view is on screen, on resume if not.
    const rebuild = () => {
        state.jumpKey = '';
        if (state.phase === 'suspended') state.dirty = true;
        else void reload(state.pageOffset);
    };

    // The same policy in both phases: a change to books this view already shows
    // is patched in place and never reorders or re-filters the sequence, and
    // anything coarser is rebuilt. Returning to a retained library must not jump.
    const handleCatalogChanged = (event: Event) => {
        const change = (event as CustomEvent<CatalogChange | undefined>).detail;
        // Continue reading is derived from reading state, which any of these can
        // move; its cache is not addressed by book id, so it is simply dropped.
        state.rail.invalidate();
        // A list request that started before this change would land afterwards
        // and undo it — reviving a removed book, or restoring an old card. A
        // patch cannot be trusted on top of that, so invalidate and rebuild.
        if (state.loadingBooks || state.loadingMore) {
            cancelInFlightLoads(state);
            rebuild();
            return;
        }
        if (change?.kind === 'books-updated') {
            for (const book of change.books) replaceRenderedBook(state, book);
            syncContinueReading(state);
            return;
        }
        if (change?.kind === 'books-removed') {
            removeRenderedBooks(state, change.ids);
            syncContinueReading(state);
            return;
        }
        rebuild();
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

    // Deferred while suspended: the rail's capacity depends on a viewport this
    // instance is not showing in. resume() recomputes it.
    const handleResize = () => {
        if (state.phase === 'active') renderBookJumpRail(state);
    };
    window.addEventListener('resize', handleResize);
    addCleanup(() => window.removeEventListener('resize', handleResize));
    addCleanup(() => document.body.classList.remove('has-library-jump-rail'));

    void fetchUserSettings()
        .then((settings) => {
            if (state.phase === 'destroyed') return;
            applyUserSettings(state, settings, false);
        })
        .catch(() => {
            /* main bootstrap keeps the default theme/settings behavior */
        });

    const gridBtn = root.querySelector('#view-grid-btn');
    const tableBtn = root.querySelector('#view-table-btn');

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
    // Mount stays synchronous so the router holds this route's cleanup before
    // anything is awaited; awaiting here would leave the global subscriptions
    // above live on whatever page the reader moved to next. The saved scroll is
    // re-applied once the grid actually has content to scroll.
    void reload(initialOffset)?.then(() => {
        // Only while this instance is the page on screen: a first load that
        // lands after the reader has already opened a book would otherwise
        // scroll the book page to the list's saved position.
        if (state.phase === 'active') restoreSavedScroll();
    });
    return {
        // Everything this instance owns outside its own root is handed back to
        // the page that is about to replace it.
        suspend(): void {
            if (state.phase !== 'active') return;
            state.phase = 'suspended';
            state.returnPosition.capture();
            cancelPendingSearch?.();
            sortSelect?.close();
            state.selection?.setActive(false);
            document.body.classList.remove('has-library-jump-rail');
            // One policy for unfinished list work: it does not survive the
            // return, and is recorded as a rebuild instead. A resumed library
            // must not quietly replace the sequence the reader left with a
            // result they never saw.
            if (state.loadingBooks || state.loadingMore) {
                cancelInFlightLoads(state);
                state.dirty = true;
            }
        },
        resume(pixelFallback: ScrollPosition | null): void {
            if (state.phase !== 'suspended') return;
            state.phase = 'active';
            state.selection?.setActive(true);
            renderBookJumpRail(state);
            // The position is restored against the DOM that is already here,
            // before any rebuild: waiting for the network would present the top
            // of the list first and jump afterwards.
            state.returnPosition.restore(pixelFallback);
            if (state.dirty) {
                state.dirty = false;
                state.jumpKey = '';
                // Re-anchor once the authoritative result has replaced the DOM.
                void rebuildRetained().then(() => {
                    if (state.phase === 'active') state.returnPosition.settle(pixelFallback);
                });
            }
            // A search typed in the debounce window before the book opened was
            // unscheduled rather than dropped; the reader's intent applies now.
            else if (searchInput && searchInput.value.trim() !== state.query.trim()) {
                void reload();
            }
        },
        destroy(): void {
            state.phase = 'destroyed';
            state.returnPosition.stop();
            state.loadingBooks = false;
            state.loadingMore = false;
            state.booksAbort?.abort();
            state.booksAbort = null;
            for (let i = cleanup.length - 1; i >= 0; i--) cleanup[i]();
        },
    };
}

function renderedBookSelector(state: LibraryViewState): string {
    return state.view === 'table' ? '.table-row' : '.book-card';
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
    if (resetRail) state.rail.invalidate();
    syncContinueReading(state);
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
    replaceLocationURL(url);
}

function setupLibrarySearchShortcuts(
    state: LibraryViewState,
    searchInput: HTMLInputElement,
): RouteCleanup {
    const handleDocumentKeydown = (event: KeyboardEvent) => {
        if (state.phase !== 'active') return;
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
    replaceLocationURL(url);
    searchInput.value = '';
    searchInput.dispatchEvent(new Event('input', { bubbles: true }));
}

function setupSaveSearchButton(
    root: HTMLElement,
    searchInput: HTMLInputElement,
): RouteCleanup | undefined {
    const btn = root.querySelector<HTMLButtonElement>('#save-search-btn');
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
            navigateApp(`/?shelf=${encodeURIComponent(shelf.id)}`);
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
    // A rebuild of a view that had accumulated several pages asks for all of
    // them in one request, so a coarse change does not silently shrink the
    // sequence the reader was browsing back to one page.
    limit = PAGE_SIZE,
) {
    const token = ++state.loadToken;
    // A new query/sort/shelf replaces the loaded set; selection must not linger.
    state.selection?.clearSelection();
    state.booksAbort?.abort();
    const abort = new AbortController();
    state.booksAbort = abort;
    const finishGlobalLoading = beginGlobalLoading();
    // Held so suspend() can release it: a request that is still running for a
    // library the reader has left must not keep the book page marked busy.
    state.finishLoading?.();
    state.finishLoading = finishGlobalLoading;
    state.loadingBooks = true;
    state.loadingMore = false;
    setLibraryResultsLoading(state, true);
    updateLoadMore(state);
    state.query = query;
    state.sort = sort;
    state.pageOffset = offset;
    syncBookJumps(state);
    syncContinueReading(state);
    try {
        const books = await fetchBooks(query, sort, limit, offset, state.shelfId, abort.signal);
        if (state.phase !== 'active' || token !== state.loadToken) return;
        state.books = books;
        state.hasMore = hasMoreBooks(state, state.books.length, limit);
        renderBooks(state, state.books);
        renderBookJumpRail(state);
        updateLoadMore(state);
    } catch (e: unknown) {
        // An aborted fetch is the expected outcome of cancelling a stale
        // request (newer query/sort superseded it). A full-page navigation can
        // also reject in-flight fetches as a TypeError in Chromium.
        if (token === state.loadToken && !isExpectedFetchCancel(e)) {
            console.error('Failed to fetch books:', e);
        }
    } finally {
        finishGlobalLoading();
        if (state.finishLoading === finishGlobalLoading) state.finishLoading = null;
        if (state.phase === 'active' && token === state.loadToken) {
            state.loadingBooks = false;
            setLibraryResultsLoading(state, false);
            updateLoadMore(state);
        }
    }
}

// Discard whatever list work is in flight, leaving no stuck loading control.
// The result of the discarded request can no longer be committed: its token is
// spent, and the request itself is aborted.
function cancelInFlightLoads(state: LibraryViewState): void {
    state.loadToken += 1;
    state.booksAbort?.abort();
    state.booksAbort = null;
    state.loadingBooks = false;
    state.loadingMore = false;
    state.finishLoading?.();
    state.finishLoading = null;
    setLibraryResultsLoading(state, false);
    updateLoadMore(state);
}

function syncContinueReading(state: LibraryViewState): void {
    state.rail.sync(
        shouldShowContinueReading(state) && state.userSettings?.hide_continue_reading === false,
    );
}

function setLibraryResultsLoading(state: LibraryViewState, loading: boolean): void {
    const container = state.root.querySelector('#library-grid');
    if (!container) return;
    container.classList.toggle('library-results-loading', loading);
    container.setAttribute('aria-busy', loading ? 'true' : 'false');
}

function shouldShowContinueReading(state: LibraryViewState): boolean {
    return state.shelfId === '' && state.query.trim() === '';
}

async function loadMore(state: LibraryViewState) {
    if (state.phase !== 'active' || state.loadingBooks || state.loadingMore || !state.hasMore)
        return;
    const token = state.loadToken;
    state.loadingMore = true;
    updateLoadMore(state);
    try {
        const offset = state.pageOffset + state.books.length;
        const books = await fetchBooks(
            state.query,
            state.sort,
            PAGE_SIZE,
            offset,
            state.shelfId,
            state.booksAbort?.signal,
        );
        if (state.phase !== 'active' || token !== state.loadToken) return;
        const newBooks = books;
        state.books = state.books.concat(newBooks);
        appendBooks(state, newBooks);
        state.hasMore = hasMoreBooks(state, newBooks.length);
    } catch (e: unknown) {
        if (token === state.loadToken && !isExpectedFetchCancel(e)) {
            console.error('Failed to load more books:', e);
            showToast(errorMessage(e, 'Failed to load more books'), { type: 'error' });
        }
    } finally {
        if (state.phase === 'active' && token === state.loadToken) {
            state.loadingMore = false;
            updateLoadMore(state);
        }
    }
}

function hasMoreBooks(state: LibraryViewState, received: number, limit = PAGE_SIZE): boolean {
    if (received < limit) return false;
    if (state.jumpTotal == null) return true;
    return state.pageOffset + state.books.length < state.jumpTotal;
}

function isExpectedFetchCancel(e: unknown): boolean {
    const name =
        typeof e === 'object' && e !== null && 'name' in e ? (e as { name?: unknown }).name : '';
    return name === 'AbortError' || navigatingAway;
}

function updateLoadMore(state: LibraryViewState) {
    const container = state.root.querySelector<HTMLElement>('#load-more-container');
    const btn = state.root.querySelector<HTMLButtonElement>('#load-more-btn');
    if (!container || !btn) return;
    container.hidden = !state.hasMore || state.books.length === 0;
    btn.disabled = state.loadingBooks || state.loadingMore;
    btn.textContent = state.loadingMore ? 'Loading…' : 'Load more';
}

function renderBooks(state: LibraryViewState, books: BookSummary[]) {
    const container = state.root.querySelector<HTMLElement>('#library-grid');
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

    // The one request allowed to land while this view is off screen: it writes
    // only inside its own root, its key is its generation, and it holds no
    // global loading or body UI. Finishing it means the rail is ready on return
    // instead of being requested again.
    void fetchBookJumps(key)
        .then((result) => {
            if (state.phase === 'destroyed' || state.jumpKey !== key) return;
            state.jumps = result.items;
            state.jumpTotal = result.total;
            state.hasMore = hasMoreBooks(state, state.books.length);
            renderBookJumpRail(state);
            updateLoadMore(state);
        })
        .catch((error) => {
            if (state.phase === 'destroyed' || state.jumpKey !== key) return;
            console.error('Failed to fetch book jumps:', error);
            state.jumps = [];
            state.jumpTotal = null;
            renderBookJumpRail(state);
        });
}

function renderBookJumpRail(state: LibraryViewState): void {
    const rail = state.root.querySelector<HTMLElement>('#library-jump-rail');
    if (!rail) return;
    const jumps = sampleBookJumps(state.jumps, jumpRailCapacity());
    const visible = state.jumpKey !== '' && jumps.length > 1;
    rail.hidden = !visible;
    // The body class belongs to whichever route is on screen: a jumps request
    // that lands while this library is detached must not re-pad the book page.
    if (state.phase === 'active') {
        document.body.classList.toggle('has-library-jump-rail', visible);
    }
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
        const row = state.root.querySelector<HTMLTableRowElement>(
            `.table-row[data-id=${CSS.escape(updated.id)}]`,
        );
        row?.replaceWith(createBookRow(state, updated));
        state.selection?.syncAfterRender();
        return;
    }

    const card = state.root.querySelector<HTMLElement>(
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
    const container = state.root.querySelector<HTMLElement>('#library-grid');
    const selector = renderedBookSelector(state);
    for (const id of ids) {
        container?.querySelector<HTMLElement>(`${selector}[data-id=${CSS.escape(id)}]`)?.remove();
    }
    state.selection?.syncAfterRender();
    if (state.books.length === 0) renderBooks(state, state.books);
}

// appendBooks adds only the newly-loaded books to the existing DOM, so a
// "Load more" never re-renders or reflows the books already on screen.
function appendBooks(state: LibraryViewState, newBooks: BookSummary[]) {
    const container = state.root.querySelector<HTMLElement>('#library-grid');
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
            const input = state.root.querySelector<HTMLInputElement>('#search-input');
            if (!input) return;
            const url = new URL(window.location.href);
            url.searchParams.delete('q');
            replaceLocationURL(url);
            input.value = '';
            input.dispatchEvent(new Event('input'));
            input.focus();
        });
    } else if (state.shelfId) {
        title.textContent = 'Shelf is empty';
        body.textContent = 'No books are on this shelf.';
        action.textContent = 'Library';
        action.addEventListener('click', () => {
            navigateApp('/');
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
function applyAuthorFilter(state: LibraryViewState, name: string): void {
    const input = state.root.querySelector<HTMLInputElement>('#search-input');
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
        el.addEventListener('click', () => applyAuthorFilter(state, author));
        el.addEventListener('keydown', (e) => {
            if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault();
                applyAuthorFilter(state, author);
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
        openEditModal(b, currentLibraryContext(state), currentLibrarySequence(state, b.id));
    });

    return tr;
}
