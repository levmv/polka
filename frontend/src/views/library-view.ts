import {
    fetchAdminStorageStatus,
    fetchBookJumps,
    fetchBooks,
    fetchCurrentUser,
    fetchUserSettings,
} from '../api';
import {
    type BookListContext,
    bookURL,
    libraryBookListContext,
    parseShelfID,
} from '../book-list-context';
import { CATALOG_CHANGED, type CatalogChange, type CatalogField } from '../catalog-events';
import { createBookCard } from '../components/book-card';
import { createSelect, type ManagedSelect } from '../components/select';
import { coverUrl } from '../cover';
import { debounce, escapeHtml } from '../dom';
import { errorMessage } from '../errors';
import { readScrollPosition } from '../history-state';
import { icon } from '../icons';
import { beginGlobalLoading } from '../loading-indicator';
import {
    navigateApp,
    type RouteCleanup,
    type RouteController,
    replaceLocationURL,
    type ScrollPosition,
} from '../router';
import { queryTerm, seriesLibraryURL } from '../search-query';
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
import { openEditModal } from './book-edit';
import { type ContinueReadingRail, createContinueReadingRail } from './continue-reading';
import { createLibrarySelection, type LibrarySelection } from './library-selection';
import { createReturnPosition, type ReturnPosition } from './return-position';

const PAGE_SIZE = 50;
// Must not exceed maxBooksLimit in internal/web/api_books.go: a shorter
// response is interpreted as the end of the list.
const REFRESH_PAGE_SIZE = 1000;
const BROWSE_SORT_OPTIONS = [
    { value: 'added', label: 'Recently added' },
    { value: 'title', label: 'Title' },
    { value: 'author', label: 'Author' },
    { value: 'year', label: 'Year' },
    { value: 'series', label: 'Series order' },
];
const SEARCH_SORT_OPTIONS = [{ value: 'relevance', label: 'Relevance' }, ...BROWSE_SORT_OPTIONS];

type LibraryViewMode = 'grid' | 'table';

interface LibraryQuery {
    query: string;
    sort: string;
    shelfId: number;
    offset: number;
}

interface LibraryReplacement {
    query: LibraryQuery;
    count: number;
    refresh: boolean;
}

interface LibraryViewState {
    // Router-owned route root. Every lookup this view makes is scoped to it.
    root: HTMLElement;
    // 'suspended' means the root is detached: state and DOM are intact, but this
    // instance owns nothing on the visible page and must not touch its URL.
    phase: 'active' | 'suspended' | 'destroyed';
    // Putting the reader back where they were; see return-position.ts.
    returnPosition: ReturnPosition;
    books: BookSummary[];
    view: LibraryViewMode;
    current: LibraryQuery;
    // Retained until success, including cancellation and failed reads. This is
    // independent of both the displayed result and the search input's draft.
    // Paging and jumps wait for it; a new search or sort can replace it.
    replacement: LibraryReplacement | null;
    dependencies: string[] | null;
    // Raw rows advance the offset; only a short raw page ends paging. Rendered
    // cards are deduplicated and cached totals may be stale. Concurrent edits
    // can still cause skips in this live offset sequence.
    nextOffset: number;
    hasMore: boolean;
    loadingMore: boolean;
    loadFailure: { message: string; retry(): void } | null;
    loadMoreObserver: IntersectionObserver | null;
    userSettings: UserSettings | null;
    canCurateCatalog: boolean;
    canManageStorage: boolean;
    canWriteback: boolean;
    loadingBooks: boolean;
    // Owned by this instance: a stale load is cancelled here, never by whatever
    // view happens to request books next.
    booksAbort: AbortController | null;
    rail: ContinueReadingRail;
    // Completion for the page-level busy indicator, released on suspend.
    finishLoading: (() => void) | null;
    selection: LibrarySelection | null;
    jumpKey: string;
    jumpDirty: boolean;
    jumpToken: number;
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

export function initLibrary(root: HTMLElement): RouteController {
    // Capture before loading: the empty page can clamp to the top and save that
    // position into history before the books arrive.
    const initialScroll = readScrollPosition(window.history.state);
    const searchInput = root.querySelector<HTMLInputElement>('#search-input');
    const params = new URLSearchParams(window.location.search);
    const initialQuery = params.get('q') || '';
    let shelfId = parseShelfID(params.get('shelf'));
    if (searchInput) searchInput.value = initialQuery;
    const state: LibraryViewState = {
        root,
        phase: 'active',
        books: [],
        view: readLibraryViewMode(),
        current: { query: '', sort: '', shelfId, offset: 0 },
        replacement: null,
        dependencies: null,
        nextOffset: 0,
        hasMore: false,
        loadingMore: false,
        loadFailure: null,
        loadMoreObserver: null,
        userSettings: null,
        canCurateCatalog: false,
        canManageStorage: false,
        canWriteback: false,
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
        jumpDirty: false,
        jumpToken: 0,
        jumps: [],
        jumpTotal: null,
    };
    const cleanup: RouteCleanup[] = [];
    const addCleanup = (fn: RouteCleanup | undefined) => {
        if (fn) cleanup.push(fn);
    };

    addCleanup(() => state.rail.destroy());

    let searching = initialQuery.trim() !== '';
    const requestedSort = params.get('sort') || '';
    let sortValue = normalizeSort(requestedSort, searching);
    let sortOverridden = requestedSort !== '' && requestedSort === sortValue;
    const initialOffset = initialLibraryOffset(params, sortValue, state.current.shelfId);

    const reload = (offset = 0) => {
        if (state.phase !== 'active') return;
        const query = searchInput?.value.trim() || '';
        if (query) shelfId = 0;
        const next = { query, sort: sortValue, shelfId, offset };
        updateLibraryBrowseURL({ ...next, sort: sortOverridden ? sortValue : '' });
        return loadBooks(state, {
            query: next,
            count: PAGE_SIZE,
            refresh: false,
        });
    };

    const loadMoreBtn = root.querySelector('#load-more-btn');
    const handleLoadMoreClick = () =>
        state.loadFailure ? state.loadFailure.retry() : loadMore(state);
    loadMoreBtn?.addEventListener('click', handleLoadMoreClick);
    addCleanup(() => loadMoreBtn?.removeEventListener('click', handleLoadMoreClick));
    if (typeof IntersectionObserver !== 'undefined') {
        state.loadMoreObserver = new IntersectionObserver(
            (entries) => {
                if (!state.loadFailure && entries.some((entry) => entry.isIntersecting)) {
                    void loadMore(state);
                }
            },
            { rootMargin: '0px 0px 600px 0px' },
        );
        addCleanup(() => state.loadMoreObserver?.disconnect());
    }

    // Held so suspend() can unschedule a pending search: a debounce that fired
    // from a detached library would rewrite the book page's URL.
    let cancelPendingSearch: (() => void) | null = null;
    let sortSelect: ManagedSelect | null = null;
    const sortControl = root.querySelector('#sort-control');

    const renderSortSelect = () => {
        sortSelect?.destroy();
        sortSelect = null;
        if (!sortControl) return;
        const select = createSelect({
            ariaLabel: 'Sort books',
            value: sortValue,
            options: sortOptions(searching),
            onChange: (value) => {
                sortValue = value;
                sortOverridden = true;
                reload();
            },
        });
        sortControl.replaceChildren(select.el);
        sortSelect = select;
    };

    const syncSearchSort = () => {
        const nextSearching = searchInput?.value.trim() !== '';
        if (nextSearching === searching) return;
        searching = nextSearching;
        if (searching && !sortOverridden) {
            sortValue = 'relevance';
        } else if (!searching && sortValue === 'relevance') {
            sortValue = 'added';
            sortOverridden = false;
        }
        renderSortSelect();
    };

    if (searchInput) {
        const handleSearchInput = debounce((_e: Event) => {
            syncSearchSort();
            reload();
        }, 250);
        cancelPendingSearch = () => handleSearchInput.cancel();
        searchInput.addEventListener('input', handleSearchInput);
        addCleanup(() => {
            searchInput.removeEventListener('input', handleSearchInput);
            handleSearchInput.cancel();
        });
        addCleanup(setupLibrarySearchShortcuts(state, searchInput));
        addCleanup(setupSaveSearchButton(root, searchInput));
    }

    renderSortSelect();
    addCleanup(() => sortSelect?.destroy());

    const libraryGrid = root.querySelector<HTMLElement>('#library-grid');
    if (libraryGrid) {
        state.selection = createLibrarySelection({
            container: libraryGrid,
            getBooks: () => state.books,
            canWriteback: () => state.canWriteback,
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

    const handleCatalogChanged = (event: Event) => {
        const change = (event as CustomEvent<CatalogChange>).detail ?? { kind: 'coarse' };
        state.rail.invalidate();
        if (
            change.kind === 'reading-state' ||
            (change.kind === 'books-updated' &&
                change.books.length === 0 &&
                change.fields?.length === 0)
        ) {
            syncContinueReading(state);
            return;
        }
        if (
            change.kind === 'shelf-membership' &&
            change.shelfId !== state.current.shelfId &&
            change.shelfId !== state.replacement?.query.shelfId &&
            state.dependencies !== null &&
            !state.dependencies.includes('shelves')
        ) {
            syncContinueReading(state);
            return;
        }

        const wasAppending = state.loadingMore;
        if (state.loadingBooks || wasAppending) cancelInFlightLoads(state);
        let fields: CatalogField[] | undefined;
        if (change.kind === 'books-updated') {
            fields = change.fields;
            replaceRenderedBooks(state, change.books);
        } else if (change.kind === 'books-removed') {
            fields = [];
            removeRenderedBooks(state, change.ids);
        } else if (change.kind === 'author-sort') {
            // Author sort can also change managed filenames indexed by search.
            fields = ['author_sort', 'assets'];
            // Author sort is shared. Keep all retained summaries current even
            // when this list's ordering does not depend on that author.
            for (const book of state.books) {
                book.authors_list = book.authors_list.map((author) =>
                    author.name === change.name
                        ? { ...author, sort_name: change.sortName }
                        : author,
                );
            }
        }
        const needsRefresh =
            fields === undefined ||
            (fields.length > 0 &&
                (state.dependencies === null ||
                    fields.some((field) => state.dependencies?.includes(field))));
        // Jumps are derived from the same sequence, including deletions.
        if (needsRefresh || change.kind === 'books-removed') invalidateBookJumps(state);
        state.selection?.syncAfterRender();
        syncContinueReading(state);
        if (state.replacement || needsRefresh) {
            refreshBooks(state);
        } else if (state.phase === 'active') {
            syncBookJumps(state);
            updateLoadMore(state);
            if (wasAppending || (state.books.length === 0 && state.hasMore)) void loadMore(state);
        }
    };
    document.addEventListener(CATALOG_CHANGED, handleCatalogChanged);
    addCleanup(() => document.removeEventListener(CATALOG_CHANGED, handleCatalogChanged));

    const handleUserSettings = (event: Event) => {
        const settings = (event as CustomEvent<UserSettings>).detail;
        if (!settings || typeof settings.show_continue_reading !== 'boolean') return;
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
    void reload(initialOffset)?.then((loaded) => {
        // Only while this instance is the page on screen: a first load that
        // lands after the reader has already opened a book would otherwise
        // scroll the book page to the list's saved position.
        if (loaded && state.phase === 'active' && initialScroll) {
            window.scrollTo(initialScroll.x, initialScroll.y);
        }
    });
    return {
        // Everything this instance owns outside its own root is handed back to
        // the page that is about to replace it.
        suspend(): void {
            if (state.phase !== 'active') return;
            state.phase = 'suspended';
            state.loadMoreObserver?.disconnect();
            state.returnPosition.capture();
            cancelPendingSearch?.();
            sortSelect?.close();
            state.selection?.setActive(false);
            document.body.classList.remove('has-library-jump-rail');
            // A cancelled replacement needs rebuilding on return. A cancelled
            // append leaves the loaded sequence intact; just resume pagination.
            if (state.loadingBooks || state.loadingMore) {
                cancelInFlightLoads(state);
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
            // Apply a draft search before replaying a pending replacement.
            const requestedQuery = state.replacement?.query.query ?? state.current.query;
            if (searchInput && searchInput.value.trim() !== requestedQuery.trim()) {
                syncSearchSort();
                void reload();
            } else if (state.replacement) {
                refreshBooks(state);
            }
            syncContinueReading(state);
            updateLoadMore(state);
            if (state.books.length === 0 && state.hasMore) void loadMore(state);
        },
        destroy(): void {
            state.phase = 'destroyed';
            state.returnPosition.stop();
            cancelInFlightLoads(state);
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

function sortOptions(searching: boolean) {
    return searching ? SEARCH_SORT_OPTIONS : BROWSE_SORT_OPTIONS;
}

function normalizeSort(value: string, searching: boolean): string {
    if (sortOptions(searching).some((option) => option.value === value)) return value;
    return searching ? 'relevance' : 'added';
}

function initialLibraryOffset(params: URLSearchParams, sort: string, shelfId: number): number {
    if (params.get('q')?.trim() || shelfId !== 0 || (sort !== 'title' && sort !== 'author'))
        return 0;
    const value = Number(params.get('offset'));
    return Number.isSafeInteger(value) && value >= 0 ? value : 0;
}

function updateLibraryBrowseURL({ query, sort, shelfId, offset }: LibraryQuery): void {
    const url = new URL(window.location.href);
    if (query) {
        url.searchParams.set('q', query);
    } else {
        url.searchParams.delete('q');
    }
    if (shelfId) {
        url.searchParams.set('shelf', String(shelfId));
    } else {
        url.searchParams.delete('shelf');
    }
    if (!sort) {
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
            searchInput.value = '';
            searchInput.dispatchEvent(new Event('input', { bubbles: true }));
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
            navigateApp(`/?shelf=${shelf.id}`);
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

function refreshBooks(state: LibraryViewState): void {
    state.replacement ??= {
        query: state.current,
        count: Math.max(PAGE_SIZE, state.nextOffset - state.current.offset),
        refresh: true,
    };
    if (state.phase === 'active') void loadBooks(state, state.replacement);
}

async function loadBooks(state: LibraryViewState, request: LibraryReplacement): Promise<boolean> {
    state.booksAbort?.abort();
    const abort = new AbortController();
    state.booksAbort = abort;
    state.replacement = request;
    const finishGlobalLoading = beginGlobalLoading();
    state.finishLoading?.();
    state.finishLoading = finishGlobalLoading;
    state.loadingBooks = true;
    state.loadingMore = false;
    state.loadFailure = null;
    setLibraryResultsLoading(state, true);
    updateLoadMore(state);
    renderBookJumpRail(state);
    const { query, sort, shelfId, offset } = request.query;
    try {
        const books: BookSummary[] = [];
        const seen = new Set<number>();
        let nextOffset = offset;
        let hasMore = true;
        let dependencies: string[] | null = null;
        // Bound each response and commit the complete browsed range together.
        do {
            const limit = request.refresh
                ? Math.min(
                      REFRESH_PAGE_SIZE,
                      Math.max(PAGE_SIZE, request.count - (nextOffset - offset)),
                  )
                : PAGE_SIZE;
            const page = await fetchBooks(query, sort, limit, nextOffset, shelfId, abort.signal);
            if (state.phase !== 'active' || state.booksAbort !== abort) return false;
            nextOffset += page.books.length;
            hasMore = page.books.length === limit;
            dependencies = page.dependencies;
            for (const book of page.books) {
                if (seen.has(book.id)) continue;
                seen.add(book.id);
                books.push(book);
            }
        } while (hasMore && nextOffset - offset < request.count);

        // Anchor at commit time so scrolling during the request takes precedence.
        if (request.refresh) state.returnPosition.capture();
        else state.selection?.clearSelection();
        const previous = state.books;
        state.current = request.query;
        state.books = books;
        state.nextOffset = nextOffset;
        state.hasMore = hasMore;
        state.dependencies = dependencies;
        state.replacement = null;
        renderBooks(state, books, request.refresh ? previous : []);
        if (request.refresh) {
            state.returnPosition.restore(null);
            state.returnPosition.settle(null);
        }
        syncBookJumps(state);
        syncContinueReading(state);
        return true;
    } catch (e: unknown) {
        if (state.booksAbort === abort && !isExpectedFetchCancel(e)) {
            console.error('Failed to fetch books:', e);
            state.loadFailure = {
                message: 'Could not refresh books.',
                retry: () => {
                    void loadBooks(state, request);
                },
            };
        }
    } finally {
        finishGlobalLoading();
        if (state.finishLoading === finishGlobalLoading) state.finishLoading = null;
        if (state.phase === 'active' && state.booksAbort === abort) {
            state.loadingBooks = false;
            state.booksAbort = null;
            setLibraryResultsLoading(state, false);
            updateLoadMore(state);
        }
    }
    return false;
}

// Keep pending replacements for retry, but make every in-flight response obsolete.
function cancelInFlightLoads(state: LibraryViewState): void {
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
    if (state.phase !== 'active') return;
    state.rail.sync(
        shouldShowContinueReading(state) && state.userSettings?.show_continue_reading === true,
    );
}

function setLibraryResultsLoading(state: LibraryViewState, loading: boolean): void {
    const container = state.root.querySelector('#library-grid');
    if (!container) return;
    container.classList.toggle('library-results-loading', loading);
    container.setAttribute('aria-busy', loading ? 'true' : 'false');
}

function shouldShowContinueReading(state: LibraryViewState): boolean {
    return state.current.shelfId === 0 && state.current.query.trim() === '';
}

async function loadMore(state: LibraryViewState) {
    if (
        state.phase !== 'active' ||
        state.loadingBooks ||
        state.loadingMore ||
        state.replacement ||
        !state.hasMore
    )
        return;
    const abort = new AbortController();
    state.booksAbort = abort;
    state.loadingMore = true;
    state.loadFailure = null;
    updateLoadMore(state);
    try {
        const page = await fetchBooks(
            state.current.query,
            state.current.sort,
            PAGE_SIZE,
            state.nextOffset,
            state.current.shelfId,
            abort.signal,
        );
        if (state.phase !== 'active' || state.booksAbort !== abort) return;
        state.nextOffset += page.books.length;
        state.hasMore = page.books.length === PAGE_SIZE;
        state.dependencies = page.dependencies;
        const seen = new Set(state.books.map((book) => book.id));
        const added = page.books.filter((book) => {
            if (seen.has(book.id)) return false;
            seen.add(book.id);
            return true;
        });
        const wasEmpty = state.books.length === 0;
        state.books.push(...added);
        if (wasEmpty) renderBooks(state, state.books);
        else appendBooks(state, added);
    } catch (e: unknown) {
        if (state.booksAbort === abort && !isExpectedFetchCancel(e)) {
            console.error('Failed to load more books:', e);
            state.loadFailure = {
                message: 'Could not load more books.',
                retry: () => {
                    void loadMore(state);
                },
            };
        }
    } finally {
        if (state.phase === 'active' && state.booksAbort === abort) {
            state.loadingMore = false;
            state.booksAbort = null;
            updateLoadMore(state);
        }
    }
}

function isExpectedFetchCancel(e: unknown): boolean {
    const name =
        typeof e === 'object' && e !== null && 'name' in e ? (e as { name?: unknown }).name : '';
    return name === 'AbortError' || navigatingAway;
}

function updateLoadMore(state: LibraryViewState) {
    const container = state.root.querySelector<HTMLElement>('#load-more-container');
    const btn = state.root.querySelector<HTMLButtonElement>('#load-more-btn');
    const status = state.root.querySelector<HTMLElement>('#load-more-status');
    if (!container || !btn || !status) return;
    state.loadMoreObserver?.disconnect();
    container.hidden = !state.loadFailure && !state.hasMore;
    btn.disabled = state.loadingBooks || state.loadingMore;
    btn.hidden = state.loadingMore || (state.loadMoreObserver !== null && !state.loadFailure);
    btn.textContent = state.loadFailure ? 'Try again' : 'Load more';
    status.hidden = !state.loadingMore && !state.loadFailure;
    if (state.loadFailure) status.textContent = state.loadFailure.message;
    else
        status.innerHTML =
            '<span class="local-loading-state"><span class="local-spinner" aria-hidden="true"></span>Loading more books…</span>';
    // Re-arm after each page: a short page or a tall viewport can leave the
    // sentinel in view without another intersection crossing. Failures wait
    // for an explicit retry instead of repeatedly hitting an unavailable API.
    if (
        state.phase === 'active' &&
        !container.hidden &&
        !state.loadingBooks &&
        !state.loadingMore &&
        !state.loadFailure
    ) {
        state.loadMoreObserver?.observe(container);
    }
}

function renderBooks(state: LibraryViewState, books: BookSummary[], previous: BookSummary[] = []) {
    const container = state.root.querySelector<HTMLElement>('#library-grid');
    if (!container) return;

    const old = new Map(previous.map((book) => [book.id, book]));
    const retained = new Map<number, HTMLElement>();
    const updated = new Map(books.map((book) => [book.id, book]));
    for (const element of container.querySelectorAll<HTMLElement>(renderedBookSelector(state))) {
        const id = Number(element.dataset.id);
        // A mismatch only rebuilds a card; position is restored by book ID.
        if (old.has(id) && JSON.stringify(old.get(id)) === JSON.stringify(updated.get(id)))
            retained.set(id, element);
    }
    if (state.view === 'grid') {
        container.className = 'library-grid';
        renderGrid(state, container, books, retained);
    } else {
        container.className = 'library-table-container';
        renderTable(state, container, books, retained);
    }
    // container.className was just reset; re-apply selection styling if armed.
    state.selection?.syncAfterRender();
}

function currentLibraryContext(state: LibraryViewState): BookListContext {
    return libraryBookListContext(
        state.current.query,
        state.current.sort,
        state.current.shelfId,
        state.current.offset,
    );
}

function currentLibrarySequence(
    state: LibraryViewState,
    bookID: number,
): BookSequenceWindow | null {
    // A jumped page does not contain the preceding slice, so let the edit
    // controller fetch its bounded server-side window instead of temporarily
    // presenting the first visible book as the first book in the library.
    if (state.current.offset > 0) return null;
    const currentIndex = state.books.findIndex((book) => book.id === bookID);
    if (currentIndex < 0) return null;
    return {
        items: state.books.map((book) => ({ id: book.id, title: book.title })),
        current_index: currentIndex,
        total:
            state.jumpTotal ??
            (state.hasMore ? undefined : state.current.offset + state.books.length),
    };
}

function invalidateBookJumps(state: LibraryViewState): void {
    state.jumpToken++;
    state.jumpDirty = true;
    state.jumpTotal = null;
}

function syncBookJumps(state: LibraryViewState): void {
    const key =
        state.current.shelfId === 0 &&
        state.current.query.trim() === '' &&
        (state.current.sort === 'title' || state.current.sort === 'author')
            ? state.current.sort
            : '';
    if (key === state.jumpKey && !state.jumpDirty) {
        renderBookJumpRail(state);
        return;
    }

    // Keep the current rail during a same-query refresh: hiding it changes
    // the grid's width and moves cards throughout a long retained list.
    if (key !== state.jumpKey) {
        state.jumps = [];
        state.jumpTotal = null;
    }
    state.jumpKey = key;
    state.jumpDirty = false;
    const token = ++state.jumpToken;
    renderBookJumpRail(state);
    if (!key) return;

    // This request may land while this view is off screen: it writes
    // only inside its own root, checks its generation, and holds no
    // global loading or body UI. Finishing it means the rail is ready on return
    // instead of being requested again.
    void fetchBookJumps(key)
        .then((result) => {
            if (state.phase === 'destroyed' || token !== state.jumpToken) return;
            state.jumps = result.items;
            state.jumpTotal = result.total;
            renderBookJumpRail(state);
            updateLoadMore(state);
        })
        .catch((error) => {
            if (state.phase === 'destroyed' || token !== state.jumpToken) return;
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
        if (jump.offset <= state.current.offset) activeOffset = jump.offset;
    }
    const kind = state.current.sort === 'author' ? 'authors' : 'titles';
    for (const jump of jumps) {
        const button = document.createElement('button');
        button.type = 'button';
        button.className = 'library-jump-button';
        button.disabled = state.replacement !== null;
        button.textContent = jump.label;
        button.classList.toggle('active', jump.offset === activeOffset);
        button.setAttribute('aria-label', `Jump to ${kind} starting with ${jump.label}`);
        button.setAttribute('aria-current', jump.offset === activeOffset ? 'true' : 'false');
        button.addEventListener('click', () => {
            if (jump.offset === state.current.offset || state.replacement) return;
            const query = { ...state.current, offset: jump.offset };
            updateLibraryBrowseURL(query);
            window.scrollTo({ top: 0, behavior: 'auto' });
            void loadBooks(state, {
                query,
                count: PAGE_SIZE,
                refresh: false,
            });
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

// Keep untouched rows/cards in place. Callers restore selection after the batch.
function replaceRenderedBooks(state: LibraryViewState, updates: BookSummary[]): void {
    if (updates.length === 0) return;
    const byID = new Map(updates.map((book) => [book.id, book]));
    state.books = state.books.map((book) => byID.get(book.id) ?? book);

    for (const element of state.root.querySelectorAll<HTMLElement>(renderedBookSelector(state))) {
        const updated = byID.get(Number(element.dataset.id));
        if (!updated) continue;
        element.replaceWith(
            state.view === 'table'
                ? createBookRow(state, updated)
                : createBookCard(updated, currentLibraryContext(state)),
        );
    }
}

function removeRenderedBooks(state: LibraryViewState, ids: number[]): void {
    if (ids.length === 0) return;
    const drop = new Set(ids);
    const count = state.books.length;
    state.books = state.books.filter((book) => !drop.has(book.id));
    state.nextOffset -= count - state.books.length;
    const container = state.root.querySelector<HTMLElement>('#library-grid');
    const selector = renderedBookSelector(state);
    for (const id of ids) {
        container?.querySelector<HTMLElement>(`${selector}[data-id="${id}"]`)?.remove();
    }
    if (state.books.length === 0 && !state.hasMore) renderBooks(state, state.books);
}

// Append without replacing existing cards, preserving focus and selection.
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
        return;
    }
    state.selection?.syncAfterRender();
}

function renderGrid(
    state: LibraryViewState,
    container: HTMLElement,
    books: BookSummary[],
    retained: Map<number, HTMLElement>,
) {
    const fragment = document.createDocumentFragment();

    if (books.length === 0) {
        container.replaceChildren(createLibraryEmptyState(state));
        return;
    }

    books.forEach((b) => {
        fragment.appendChild(retained.get(b.id) ?? createBookCard(b, currentLibraryContext(state)));
    });
    container.replaceChildren(fragment);
}

function renderTable(
    state: LibraryViewState,
    container: HTMLElement,
    books: BookSummary[],
    retained: Map<number, HTMLElement>,
) {
    container.replaceChildren();

    if (books.length === 0) {
        container.replaceChildren(createLibraryEmptyState(state));
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
        tbody.appendChild(retained.get(b.id) ?? createBookRow(state, b));
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

    const query = state.current.query.trim();
    if (query) {
        title.textContent = 'No matches';
        body.textContent = `No books match “${query}”.`;
    } else if (state.current.shelfId !== 0) {
        title.textContent = 'Shelf is empty';
        body.textContent = 'No books are on this shelf.';
        action.textContent = 'Library';
        action.addEventListener('click', () => {
            navigateApp('/');
        });
        actions.append(action);
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
        }
    }

    el.append(title, body);
    if (actions.childElementCount) el.append(actions);
    return el;
}

// How many tags a table row shows before collapsing the rest behind a "+N".
const TABLE_TAG_LIMIT = 3;

// Add a table filter to the current search without repeating it.
function applyTableFilter(state: LibraryViewState, token: string): void {
    const input = state.root.querySelector<HTMLInputElement>('#search-input');
    if (!input) return;
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
                `<span class="table-author-link" role="button" tabindex="0" data-filter="${escapeHtml(queryTerm('author', n))}">${escapeHtml(n)}</span>`,
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
    const sep = ' <span class="table-tag-sep">·</span> ';
    const tag = (t: string) =>
        `<span class="table-tag-text" role="button" tabindex="0" data-filter="${escapeHtml(queryTerm('tag', t))}">${escapeHtml(t)}</span>`;

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
    tr.dataset.id = String(b.id);

    const href = escapeHtml(bookURL(b.id, currentLibraryContext(state)));
    const coverHtml = `<a href="${href}" class="table-cover-link"><img loading="lazy" src="${coverUrl(b.id, b.cover_version, 'thumb')}" class="table-cover-image" alt=""></a>`;

    let seriesHtml = '';
    if (b.series) {
        seriesHtml = `<a class="table-series-link" href="${escapeHtml(seriesLibraryURL(b.series))}">${escapeHtml(b.series)}</a>`;
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
    for (const el of tr.querySelectorAll<HTMLElement>('[data-filter]')) {
        const token = el.dataset.filter!;
        el.addEventListener('click', () => applyTableFilter(state, token));
        el.addEventListener('keydown', (e) => {
            if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault();
                applyTableFilter(state, token);
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
