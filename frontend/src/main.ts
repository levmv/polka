import { initSidebarAccount } from './account-menu';
import { fetchCurrentUser, fetchUserSettings } from './api';
import {
    historyStateWithScroll,
    newEntryID,
    type PolkaHistoryState,
    readEntryID,
    readOverlayEntry,
    readOverlayOriginID,
    readPredecessorURL,
    readRetainedLibraryID,
    readScrollPosition,
    retentionForPop,
    retentionForPush,
} from './history-state';
import { beginGlobalLoading } from './loading-indicator';
import { closeAllModals, dismissOverlayOnPopstate, reopenOverlay } from './modal';
import {
    renderAuthorsPage,
    renderBookPage,
    renderCleanupPage,
    renderLibraryPage,
    renderSeriesPage,
    renderTrashPage,
} from './pages';
import {
    initRouter,
    type Retention,
    type Route,
    type Router,
    type ScrollPosition,
    setAppNavigate,
} from './router';
import { initSidebarCuration } from './sidebar-curation';
import { initSidebarShelves, syncSidebarShelfActive } from './sidebar-shelves';
import { initSidebarUpload } from './sidebar-upload';
import { applyCachedTheme, applyTheme } from './theme';
import { initAuthors } from './views/authors-view';
import { initBookDetail } from './views/book-view';
import { initCleanup } from './views/cleanup-view';
import { initLibrary } from './views/library-view';
import { initSeries } from './views/series-view';
import { initTrash } from './views/trash-view';

applyCachedTheme();

const routes: Route<unknown>[] = [
    {
        navId: 'nav-library',
        mainClass: 'app-main--strip',
        title: 'polka',
        match: (path) => (path === '/' || path === '/index.html' ? true : null),
        render: () => renderLibraryPage(),
        mount: (_match, root) => initLibrary(root),
    },
    {
        mainClass: 'app-main--strip',
        match: (path) => {
            if (!path.startsWith('/book/')) return null;
            const pathParts = path.split('/');
            return pathParts[pathParts.length - 1] || null;
        },
        render: () => renderBookPage(),
        mount: (workId, root, context) => initBookDetail(String(workId), root, context),
    },
    {
        navId: 'nav-library',
        mainClass: 'app-main--strip',
        title: 'Cleanup - polka',
        match: (path) => (path === '/cleanup' ? true : null),
        render: () => renderCleanupPage(),
        mount: async (_match, root) => {
            await initCleanup(root);
            return undefined;
        },
    },
    {
        navId: 'nav-series',
        mainClass: 'app-main--strip',
        title: 'Series - polka',
        match: (path) => (path === '/series' ? true : null),
        render: () => renderSeriesPage(),
        mount: (_match, root) => initSeries(root),
    },
    {
        navId: 'nav-authors',
        mainClass: 'app-main--strip',
        title: 'Authors - polka',
        match: (path) => (path === '/authors' ? true : null),
        render: () => renderAuthorsPage(),
        mount: async (_match, root) => {
            return await initAuthors(root);
        },
    },
    {
        navId: 'nav-library',
        mainClass: 'app-main--strip',
        title: 'Trash - polka',
        match: (path) => (path === '/trash' ? true : null),
        render: () => renderTrashPage(),
        mount: async (_match, root) => {
            await initTrash(root);
            return undefined;
        },
    },
];

document.addEventListener('DOMContentLoaded', () => {
    const toggle = document.getElementById('sidebar-toggle');
    const sidebar = document.getElementById('app-sidebar');
    const overlay = document.getElementById('sidebar-overlay');

    const setSidebarOpen = (open: boolean) => {
        sidebar?.classList.toggle('open', open);
        overlay?.classList.toggle('open', open);
        document.querySelector('.app-layout')?.classList.toggle('sidebar-open', open);
    };

    if (toggle && sidebar && overlay) {
        toggle.addEventListener('click', () => setSidebarOpen(!sidebar.classList.contains('open')));
        overlay.addEventListener('click', () => setSidebarOpen(false));
    }

    initSidebarAccount();
    const currentUserPromise = fetchCurrentUser();
    initSidebarUpload(currentUserPromise);
    initSidebarShelves();
    fetchUserSettings()
        .then((settings) => {
            applyTheme(settings.theme);
            window.dispatchEvent(new CustomEvent('polka:user-settings', { detail: settings }));
        })
        .catch(() => {
            /* keep cached/system theme */
        });

    const router = initRouter(routes);
    const navigate = initNavigation(router, () => setSidebarOpen(false));
    setAppNavigate(navigate);
    initSidebarCuration(currentUserPromise, navigate);
});

function initNavigation(router: Router, closeSidebar: () => void): (href: string) => void {
    setupScrollRestoration();
    // The route that is mounted, which is deliberately not "whatever the URL
    // says": Back and Forward change the URL before popstate runs, so this is
    // the only remaining record of the page being left. It may also lag the
    // URL, which the edit dialog rewrites when it moves to the next book.
    let activeRoutePathname = window.location.pathname;
    let currentEntryID = ensureEntryID();

    const navigate = (href: string) => {
        if (!canNavigateWithRouter(href, router)) {
            closeSidebar();
            return;
        }
        const url = new URL(href, window.location.href);
        const { retention, retainedLibraryID } = retentionForPush({
            // Nothing has moved yet, so the URL is the page being left, and it
            // is the more current of the two.
            fromPathname: window.location.pathname,
            toPathname: url.pathname,
            fromID: currentEntryID,
            retainedKey: router.retainedKey(),
        });

        activeRoutePathname = url.pathname;
        currentEntryID = newEntryID();
        void navigateWithRouter(url, router, closeSidebar, {
            pushHistory: true,
            scroll: { x: 0, y: 0 },
            entryID: currentEntryID,
            retainedLibraryID,
            retention,
        });
    };

    document.addEventListener('click', (event) => {
        const link = appNavigationLink(event.target, router);
        if (!link) return;
        if (!canUseAppNavigation(event, link)) return;

        // A "Back" control means one history entry back whenever this entry has
        // a known in-app predecessor, so the previous page comes back as it was
        // left rather than being re-entered at the top. The href stays real for
        // a middle click, "open in new tab", and arriving by direct link.
        if (link.hasAttribute('data-app-back') && readPredecessorURL(window.history.state)) {
            event.preventDefault();
            window.history.back();
            return;
        }
        if (!canNavigateWithRouter(link.href, router)) return;

        event.preventDefault();
        navigate(link.href);
    });

    window.addEventListener('popstate', (event) => {
        const url = new URL(window.location.href);
        if (!router.canMount(url.pathname)) return;
        // Leaving an open overlay entry dismisses only its modal; the origin
        // route stays mounted.
        if (dismissOverlayOnPopstate()) {
            currentEntryID = readEntryID(window.history.state) ?? currentEntryID;
            return;
        }
        // Forward can reopen directly only over the exact mounted origin. If
        // the app navigated away, fall through to remount the route first.
        const overlay = readOverlayEntry(event.state);
        const overlayOriginID = readOverlayOriginID(event.state);
        if (
            overlay &&
            overlayOriginID === currentEntryID &&
            url.pathname === activeRoutePathname &&
            reopenOverlay(overlay)
        ) {
            currentEntryID = readEntryID(event.state) ?? currentEntryID;
            return;
        }
        const scroll = readScrollPosition(event.state);
        if (url.pathname === activeRoutePathname && handlesQueryPopstateLocally(url.pathname)) {
            closeSidebar();
            restoreScrollPosition(scroll);
            return;
        }
        const targetID = readEntryID(event.state) ?? ensureEntryID();
        const retention = retentionForPop({
            targetPathname: url.pathname,
            targetID,
            targetRetainedLibraryID: readRetainedLibraryID(event.state),
            fromPathname: activeRoutePathname,
            fromID: currentEntryID,
            retainedKey: router.retainedKey(),
            scroll,
        });
        activeRoutePathname = url.pathname;
        currentEntryID = targetID;
        void navigateWithRouter(url, router, closeSidebar, {
            pushHistory: false,
            scroll,
            retention,
        }).then((mounted) => {
            if (mounted && overlay) reopenOverlay(overlay);
        });
    });

    // history.state survives a reload. Reconnect an overlay recorded on the
    // entry the document loaded instead of showing a bare page with a dead
    // editor step still sitting underneath the browser's Back button.
    const initialOverlay = readOverlayEntry(window.history.state);
    if (initialOverlay) reopenOverlay(initialOverlay);

    return navigate;
}

// Entries that predate this (a reload, a link from outside) get an ID on first
// sight, so identity is available even for the entry the app started on.
function ensureEntryID(): string {
    const existing = readEntryID(window.history.state);
    if (existing) return existing;
    const id = newEntryID();
    const state = window.history.state;
    const base = state && typeof state === 'object' ? { ...(state as PolkaHistoryState) } : {};
    window.history.replaceState({ ...base, polkaEntryID: id }, '');
    return id;
}

function canNavigateWithRouter(href: string, router: Router): boolean {
    const url = new URL(href, window.location.href);
    if (url.origin !== window.location.origin) return false;
    if (url.pathname === window.location.pathname && url.search === window.location.search) {
        return false;
    }
    if (!router.canMount(window.location.pathname)) return false;
    return router.canMount(url.pathname);
}

function appNavigationLink(target: EventTarget | null, router: Router): HTMLAnchorElement | null {
    if (!(target instanceof Element)) return null;
    const link = target.closest<HTMLAnchorElement>('a[href]');
    if (!link) return null;
    const url = new URL(link.href, window.location.href);
    if (url.origin !== window.location.origin) return null;
    if (link.hasAttribute('data-app-nav')) return link;
    if (router.canMount(url.pathname)) return link;
    return null;
}

function canUseAppNavigation(event: MouseEvent, link: HTMLAnchorElement): boolean {
    if (event.defaultPrevented || event.button !== 0) return false;
    if (event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return false;
    return !link.target || link.target === '_self';
}

interface NavigationOptions {
    pushHistory: boolean;
    scroll: ScrollPosition | null;
    entryID?: string;
    retainedLibraryID?: string;
    retention?: Retention;
}

async function navigateWithRouter(
    url: URL,
    router: Router,
    closeSidebar: () => void,
    opts: NavigationOptions,
): Promise<boolean> {
    if (opts.pushHistory) {
        const from = `${window.location.pathname}${window.location.search}`;
        saveScrollPosition();
        const state: PolkaHistoryState = {
            ...historyStateWithScroll(null, opts.scroll || { x: 0, y: 0 }),
            polkaEntryID: opts.entryID ?? newEntryID(),
            polkaFrom: from,
        };
        if (opts.retainedLibraryID) state.polkaRetainedLibraryID = opts.retainedLibraryID;
        window.history.pushState(state, '', `${url.pathname}${url.search}${url.hash}`);
    }
    closeSidebar();
    closeAllModals();
    // Any restore still waiting for a frame belongs to the navigation this one
    // supersedes. Left alone it lands after the new page is in place — which is
    // how a Back that has already restored its position gets pulled back to the
    // book page's top.
    cancelScrollRestore();
    const finishGlobalLoading = beginGlobalLoading();
    try {
        const mounted = await router.mount(url.pathname, {
            retention: opts.retention,
            clientNavigation: true,
        });
        if (!mounted) {
            if (
                window.location.pathname === url.pathname &&
                window.location.search === url.search
            ) {
                window.location.href = url.href;
            }
            return false;
        }
        syncSidebarShelfActive();
        // A resumed route restores its own position from its own anchor; the
        // generic pixel restore would fight it a frame later.
        if (!url.hash && opts.retention?.mode !== 'resume') restoreScrollPosition(opts.scroll);
        return true;
    } finally {
        finishGlobalLoading();
    }
}

function setupScrollRestoration(): void {
    if ('scrollRestoration' in window.history) {
        window.history.scrollRestoration = 'manual';
    }
    saveScrollPosition();

    let pendingSave = 0;
    window.addEventListener(
        'scroll',
        () => {
            if (pendingSave) window.clearTimeout(pendingSave);
            pendingSave = window.setTimeout(() => {
                pendingSave = 0;
                saveScrollPosition();
            }, 100);
        },
        { passive: true },
    );
}

function saveScrollPosition(): void {
    window.history.replaceState(
        historyStateWithScroll(window.history.state, { x: window.scrollX, y: window.scrollY }),
        '',
    );
}

// Two frames, so the restore runs after the mounted route's first layout and
// paint. The wait is what makes it cancellable work: see navigateWithRouter.
let pendingScrollFrame = 0;

function cancelScrollRestore(): void {
    if (!pendingScrollFrame) return;
    window.cancelAnimationFrame(pendingScrollFrame);
    pendingScrollFrame = 0;
}

function restoreScrollPosition(scroll: ScrollPosition | null): void {
    cancelScrollRestore();
    if (!scroll) return;
    pendingScrollFrame = window.requestAnimationFrame(() => {
        pendingScrollFrame = window.requestAnimationFrame(() => {
            pendingScrollFrame = 0;
            window.scrollTo(scroll.x, scroll.y);
        });
    });
}

function handlesQueryPopstateLocally(pathname: string): boolean {
    return pathname === '/series';
}
