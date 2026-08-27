import { initSidebarAccount } from './account-menu';
import { fetchCurrentUser, fetchUserSettings } from './api';
import { beginGlobalLoading } from './loading-indicator';
import { closeAllModals } from './modal';
import {
    renderAuthorsPage,
    renderBookPage,
    renderCleanupPage,
    renderLibraryPage,
    renderSeriesPage,
    renderTrashPage,
} from './pages';
import { initRouter, type Route, type Router } from './router';
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
        mount: () => initLibrary(),
    },
    {
        mainClass: 'app-main--strip',
        match: (path) => {
            if (!path.startsWith('/book/')) return null;
            const pathParts = path.split('/');
            return pathParts[pathParts.length - 1] || null;
        },
        render: () => renderBookPage(),
        mount: async (workId) => {
            return await initBookDetail(String(workId));
        },
    },
    {
        navId: 'nav-library',
        mainClass: 'app-main--strip',
        title: 'Cleanup - polka',
        match: (path) => (path === '/cleanup' ? true : null),
        render: () => renderCleanupPage(),
        mount: async () => {
            await initCleanup();
            return undefined;
        },
    },
    {
        navId: 'nav-series',
        mainClass: 'app-main--strip',
        title: 'Series - polka',
        match: (path) => (path === '/series' ? true : null),
        render: () => renderSeriesPage(),
        mount: () => initSeries(),
    },
    {
        navId: 'nav-authors',
        mainClass: 'app-main--strip',
        title: 'Authors - polka',
        match: (path) => (path === '/authors' ? true : null),
        render: () => renderAuthorsPage(),
        mount: async () => {
            return await initAuthors();
        },
    },
    {
        navId: 'nav-library',
        mainClass: 'app-main--strip',
        title: 'Trash - polka',
        match: (path) => (path === '/trash' ? true : null),
        render: () => renderTrashPage(),
        mount: async () => {
            await initTrash();
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
    initSidebarCuration(currentUserPromise, navigate);
});

interface ScrollPosition {
    x: number;
    y: number;
}

interface PolkaHistoryState {
    polkaScroll?: ScrollPosition;
    [key: string]: unknown;
}

function initNavigation(router: Router, closeSidebar: () => void): (href: string) => void {
    setupScrollRestoration();
    let activeRoutePathname = window.location.pathname;

    const navigate = (href: string) => {
        if (!canNavigateWithRouter(href, router)) {
            closeSidebar();
            return;
        }
        const url = new URL(href, window.location.href);
        activeRoutePathname = url.pathname;
        void navigateWithRouter(url, router, closeSidebar, {
            pushHistory: true,
            scroll: { x: 0, y: 0 },
        });
    };

    document.addEventListener('click', (event) => {
        const link = appNavigationLink(event.target, router);
        if (!link) return;
        if (!canUseAppNavigation(event, link)) return;
        if (!canNavigateWithRouter(link.href, router)) return;

        event.preventDefault();
        navigate(link.href);
    });

    window.addEventListener('popstate', (event) => {
        const url = new URL(window.location.href);
        if (!router.canMount(url.pathname)) return;
        const scroll = readScrollPosition(event.state);
        if (url.pathname === activeRoutePathname && handlesQueryPopstateLocally(url.pathname)) {
            closeSidebar();
            restoreScrollPosition(scroll);
            return;
        }
        activeRoutePathname = url.pathname;
        void navigateWithRouter(url, router, closeSidebar, { pushHistory: false, scroll });
    });

    return navigate;
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

async function navigateWithRouter(
    url: URL,
    router: Router,
    closeSidebar: () => void,
    opts: { pushHistory: boolean; scroll: ScrollPosition | null },
): Promise<boolean> {
    if (opts.pushHistory) {
        saveScrollPosition();
        window.history.pushState(
            historyStateWithScroll(null, opts.scroll || { x: 0, y: 0 }),
            '',
            `${url.pathname}${url.search}${url.hash}`,
        );
    }
    closeSidebar();
    closeAllModals();
    const finishGlobalLoading = beginGlobalLoading();
    try {
        const mounted = await router.mount(url.pathname, { render: true });
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
        if (!url.hash) restoreScrollPosition(opts.scroll);
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

function readScrollPosition(state: unknown): ScrollPosition | null {
    if (!state || typeof state !== 'object') return null;
    const scroll = (state as PolkaHistoryState).polkaScroll;
    if (!scroll || typeof scroll.x !== 'number' || typeof scroll.y !== 'number') return null;
    return scroll;
}

function historyStateWithScroll(state: unknown, scroll: ScrollPosition): PolkaHistoryState {
    const base =
        state && typeof state === 'object' ? { ...(state as Record<string, unknown>) } : {};
    return { ...base, polkaScroll: scroll };
}

function restoreScrollPosition(scroll: ScrollPosition | null): void {
    if (!scroll) return;
    window.requestAnimationFrame(() => {
        window.requestAnimationFrame(() => window.scrollTo(scroll.x, scroll.y));
    });
}

function handlesQueryPopstateLocally(pathname: string): boolean {
    return pathname === '/series';
}
