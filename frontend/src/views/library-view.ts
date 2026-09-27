import { fetchAdminStorageStatus, fetchBookJumps, fetchBooks, fetchCurrentUser } from '../api';
import {
    type BookListContext,
    bookURL,
    libraryBookListContext,
    parseShelfID,
} from '../book-list-context';
import { CATALOG_CHANGED, type CatalogChange, type CatalogField } from '../catalog-events';
import { createBookCard } from '../components/book-card';
import { type CollectionView, createCollectionControls } from '../components/collection-controls';
import { trackSearch } from '../components/search-history';
import { coverUrl } from '../cover';
import { escapeHtml } from '../dom';
import { errorMessage } from '../errors';
import { icon } from '../icons';
import { beginGlobalLoading } from '../loading-indicator';
import { navigateApp, type PageHistory } from '../navigation';
import type { RouteCleanup, RouteController, RouteMountContext, ScrollPosition } from '../router';
import { queryTerm, seriesLibraryURL } from '../search-query';
import { openSettingsModal } from '../settings';
import { loadPersonalSettings, type PersonalSettings, writebackSetting } from '../settings/state';
import { openCreateShelfDialog } from '../shelf-dialog';
import { tagDisplayLabels } from '../tags';
import { showToast } from '../toast';
import type { BookJump, BookSequenceWindow, BookSummary } from '../types';
import { openEditModal } from './book-edit';
import { type ContinueReadingRail, createContinueReadingRail } from './continue-reading';
import { createLibrarySelection, type LibrarySelection } from './library-selection';
import { createReturnPosition, type ReturnPosition } from './return-position';

const PAGE_SIZE = 50;
// Must not exceed maxBooksLimit in internal/web/api_books.go: a shorter
// response is interpreted as the end of the list.
const MAX_PAGE_SIZE = 1000;
const BROWSE_SORT_OPTIONS = [
    { value: 'added', label: 'Recently added' },
    { value: 'title', label: 'Title' },
    { value: 'author', label: 'Author' },
    { value: 'year', label: 'Year' },
    { value: 'series', label: 'Series order' },
];
const SEARCH_SORT_OPTIONS = [{ value: 'relevance', label: 'Relevance' }, ...BROWSE_SORT_OPTIONS];

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

interface LibrarySnapshot {
    view: CollectionView;
    count: number;
    selected: number[];
}

interface LibraryViewState {
    history: PageHistory;
    // Router-owned route root. Every lookup this view makes is scoped to it.
    root: HTMLElement;
    // 'suspended' means the root is detached: state and DOM are intact, but this
    // instance owns nothing on the visible page and must not touch its URL.
    phase: 'active' | 'suspended' | 'destroyed';
    // Putting the reader back where they were; see return-position.ts.
    returnPosition: ReturnPosition;
    books: BookSummary[];
    view: CollectionView;
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
    userSettings: PersonalSettings | null;
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

export function renderLibraryPage(): string {
    return `
        <section id="continue-reading" class="continue-reading" aria-labelledby="continue-reading-title" hidden>
            <div class="section-heading-row">
                <h2 id="continue-reading-title">Continue reading</h2>
                <button id="continue-reading-dismiss" class="continue-reading-dismiss" type="button" aria-label="Hide Continue reading" title="Hide Continue reading">
                    ${icon('close', 16)}
                </button>
            </div>
            <div id="continue-reading-list" class="continue-reading-list"></div>
        </section>
        <div class="search-container">
            <div class="search-row">
                <div class="search-field">
                    <input type="search" id="search-input" placeholder="Search library..." autocomplete="off" aria-label="Search library" aria-keyshortcuts="/" title="Search library (/)">
                    <button id="save-search-btn" class="search-icon-btn" type="button" aria-label="Save search as shelf" title="Save search as shelf" hidden>
                        ${icon('bookmark', 20)}
                    </button>
                </div>
                <div class="collection-controls"></div>
            </div>
        </div>
        <div id="library-grid" class="library-grid"></div>
        <div id="load-more-container" class="load-more-container" hidden>
            <div id="load-more-status" role="status" aria-live="polite" hidden></div>
            <button id="load-more-btn" class="load-more-btn">Load more</button>
        </div>
        <nav id="library-jump-rail" class="library-jump-rail" aria-label="Jump through books" hidden></nav>
    `;
}

export function initLibrary(root: HTMLElement, context: RouteMountContext): RouteController {
    const saved = context.history.state as LibrarySnapshot | undefined;
    const searchInput = root.querySelector<HTMLInputElement>('#search-input');
    const params = new URLSearchParams(window.location.search);
    const initialQuery = params.get('q') || '';
    let shelfId = parseShelfID(params.get('shelf'));
    if (searchInput) searchInput.value = initialQuery;
    const state: LibraryViewState = {
        history: context.history,
        root,
        phase: 'active',
        books: [],
        view: saved?.view ?? readLibraryViewMode(),
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

    const reload = (offset = 0, newEntry = false, count = PAGE_SIZE) => {
        if (state.phase !== 'active') return;
        const query = searchInput?.value.trim() || '';
        if (query) shelfId = 0;
        const next = { query, sort: sortValue, shelfId, offset };
        const url = libraryBrowseURL({ ...next, sort: sortOverridden ? sortValue : '' });
        if (newEntry) context.history.push(url);
        else context.history.replace(url);
        return loadBooks(state, {
            query: next,
            count,
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

    const controls = createCollectionControls({
        view: state.view,
        sort: sortValue,
        sortOptions: sortOptions(searching),
        sortLabel: 'Sort books',
        onViewChange: (view) => {
            searchHistory?.finish();
            state.view = view;
            localStorage.setItem('polka-view-mode', view);
            renderBooks(state, state.books);
        },
        onSortChange: (value) => {
            searchHistory?.finish();
            sortValue = value;
            sortOverridden = true;
            reload();
        },
    });
    root.querySelector('.collection-controls')!.replaceWith(controls.el);
    addCleanup(() => controls.destroy());

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
        controls.setSort(sortValue, sortOptions(searching));
    };

    const searchHistory = searchInput
        ? trackSearch(searchInput, (newEntry) => {
              if (state.phase !== 'active') return;
              syncSearchSort();
              void reload(0, newEntry);
          })
        : null;
    if (searchInput) {
        addCleanup(() => searchHistory?.destroy());
        addCleanup(setupLibrarySearchShortcuts(state, searchInput));
        addCleanup(setupSaveSearchButton(root, searchInput));
    }

    const libraryGrid = root.querySelector<HTMLElement>('#library-grid');
    if (libraryGrid) {
        state.selection = createLibrarySelection({
            container: libraryGrid,
            getBooks: () => state.books,
            canWriteback: () => state.canWriteback,
            onChange: () => context.history.save(),
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
                    const mode = writebackSetting(status.writeback.mode);
                    const update = () => {
                        state.canWriteback = mode.value === 'manual';
                        state.selection?.refreshActions();
                    };
                    update();
                    addCleanup(mode.subscribe(update));
                })
                .catch(() => {
                    if (state.phase === 'destroyed') return;
                    state.canWriteback = false;
                    state.selection?.refreshActions();
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

    // Deferred while suspended: the rail's capacity depends on a viewport this
    // instance is not showing in. resume() recomputes it.
    const handleResize = () => {
        if (state.phase === 'active') renderBookJumpRail(state);
    };
    window.addEventListener('resize', handleResize);
    addCleanup(() => window.removeEventListener('resize', handleResize));
    addCleanup(() => document.body.classList.remove('has-library-jump-rail'));

    void loadPersonalSettings()
        .then((settings) => {
            if (state.phase === 'destroyed') return;
            state.userSettings = settings;
            addCleanup(
                settings.show_continue_reading.subscribe(() => {
                    state.rail.invalidate();
                    syncContinueReading(state);
                }),
            );
            syncContinueReading(state);
        })
        .catch(() => {
            /* main bootstrap keeps the default theme/settings behavior */
        });

    const ready = reload(initialOffset, false, saved?.count ?? PAGE_SIZE)?.then((loaded) => {
        if (loaded && state.phase === 'active' && saved) {
            state.selection?.restore(saved.selected);
        }
    });
    return {
        ready,
        beforeLeave: () => searchHistory?.finish(),
        snapshot: (): LibrarySnapshot => ({
            view: state.view,
            count: state.replacement
                ? PAGE_SIZE
                : Math.max(PAGE_SIZE, state.nextOffset - state.current.offset),
            selected: state.selection?.selectedIDs() ?? [],
        }),
        // Everything this instance owns outside its own root is handed back to
        // the page that is about to replace it.
        suspend(): void {
            if (state.phase !== 'active') return;
            state.phase = 'suspended';
            state.loadMoreObserver?.disconnect();
            state.returnPosition.capture();
            controls.close();
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

function readLibraryViewMode(): CollectionView {
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

function libraryBrowseURL({ query, sort, shelfId, offset }: LibraryQuery): string {
    const params = new URLSearchParams();
    if (query) params.set('q', query);
    if (shelfId) params.set('shelf', String(shelfId));
    if (sort) params.set('sort', sort);
    if (offset > 0) params.set('offset', String(offset));
    const search = params.toString();
    return search ? `/?${search}` : '/';
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
                navigateOnCreate: true,
            });
            if (!shelf) {
                btn.disabled = false;
                return;
            }
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
            const limit = Math.min(
                MAX_PAGE_SIZE,
                Math.max(PAGE_SIZE, request.count - (nextOffset - offset)),
            );
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
        shouldShowContinueReading(state) &&
            state.userSettings?.show_continue_reading.value === true,
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
            '<span class="loading-state"><span class="spinner" aria-hidden="true"></span>Loading more books…</span>';
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
            state.history.replace(libraryBrowseURL(query));
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
                <th class="col-genres">Genres</th>
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

const TABLE_GENRE_LIMIT = 3;

function tableFilterURL(state: LibraryViewState, token: string): string {
    const current = state.current.query;
    const query = !current ? token : current.includes(token) ? current : `${current} ${token}`;
    return libraryBrowseURL({
        query,
        sort: new URLSearchParams(window.location.search).get('sort') || '',
        shelfId: 0,
        offset: 0,
    });
}

function authorCellHtml(state: LibraryViewState, b: BookSummary): string {
    const names = b.authors_list.map((author) => author.name).filter(Boolean);
    if (names.length === 0) return '';
    return names
        .map(
            (n) =>
                `<a class="table-author-link" href="${escapeHtml(tableFilterURL(state, queryTerm('author', n)))}">${escapeHtml(n)}</a>`,
        )
        .join(' &amp; ');
}

function genresCellHtml(state: LibraryViewState, b: BookSummary): string {
    const genres = b.genres
        ? b.genres
              .split(',')
              .map((name) => name.trim())
              .filter(Boolean)
        : [];
    if (genres.length === 0) return '';

    const labels = tagDisplayLabels(genres);
    // Hidden values include their separator so expansion keeps the list readable.
    const sep = ' <span class="table-genre-sep">·</span> ';
    let html = '<span class="table-genres-text">';
    for (const [index, name] of genres.entries()) {
        const genre = `<a class="table-genre-text" href="${escapeHtml(tableFilterURL(state, queryTerm('genre', name)))}" title="${escapeHtml(name)}">${escapeHtml(labels[index])}</a>`;
        if (index >= TABLE_GENRE_LIMIT) {
            html += `<span class="table-genre-hidden" hidden>${sep}${genre}</span>`;
        } else {
            html += (index > 0 ? sep : '') + genre;
        }
    }
    html += '</span>';
    const hiddenCount = genres.length - TABLE_GENRE_LIMIT;
    if (hiddenCount > 0) {
        html += ` <button type="button" class="table-genre-more" aria-label="Show ${hiddenCount} more ${hiddenCount === 1 ? 'genre' : 'genres'}">+${hiddenCount}</button>`;
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
        <td class="col-author">${authorCellHtml(state, b)}</td>
        <td class="col-series">${seriesHtml}</td>
        <td class="col-year">${escapeHtml(b.year || '')}</td>
        <td class="col-genres">${genresCellHtml(state, b)}</td>
        <td class="col-format">${formats}</td>
        <td class="col-actions">
            <button class="btn-quick-edit" title="Quick Edit" aria-label="Quick Edit">
                ${icon('edit', 18)}
            </button>
        </td>
    `;
    const moreBtn = tr.querySelector('.table-genre-more');
    moreBtn?.addEventListener('click', () => {
        for (const el of tr.querySelectorAll<HTMLElement>('.table-genre-hidden')) {
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
