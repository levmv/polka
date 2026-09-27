import { fetchCurrentUser } from './api';
import { closeAllModals } from './modal';
import { initNavigation, navigateApp } from './navigation';
import { initRouter, type Route } from './router';
import { loadPersonalSettings } from './settings/state';
import { initSidebarAccount } from './sidebar-account';
import { initSidebarCuration } from './sidebar-curation';
import { initSidebarShelves, syncSidebarShelfActive } from './sidebar-shelves';
import { initSidebarUpload } from './sidebar-upload';
import { applyCachedTheme } from './theme';
import { initAuthors, renderAuthorsPage } from './views/authors-view';
import { initBookDetail, renderBookPage } from './views/book-view';
import { initCleanup, renderCleanupPage } from './views/cleanup-view';
import { initLibrary, renderLibraryPage } from './views/library-view';
import { initSeries, renderSeriesPage } from './views/series-view';
import { initTags, renderTagsPage, tagsPageTitle } from './views/tags-view';
import { initTrash, renderTrashPage } from './views/trash-view';

applyCachedTheme();

const routes: Route<unknown>[] = [
    {
        navId: 'nav-library',
        mainClass: 'main--strip',
        title: 'polka',
        match: (path) => (path === '/' || path === '/index.html' ? true : null),
        render: renderLibraryPage,
        mount: (_match, root, context) => initLibrary(root, context),
    },
    {
        mainClass: 'main--strip',
        match: (path) => {
            if (!path.startsWith('/book/')) return null;
            const pathParts = path.split('/');
            return pathParts[pathParts.length - 1] || null;
        },
        render: renderBookPage,
        mount: (bookId, root, context) => initBookDetail(Number(bookId), root, context),
    },
    {
        navId: 'nav-library',
        mainClass: 'main--strip',
        title: 'Cleanup - polka',
        match: (path) => (path === '/cleanup' ? true : null),
        render: renderCleanupPage,
        mount: (_match, root, context) => initCleanup(root, context.signal),
    },
    {
        navId: 'nav-series',
        mainClass: 'main--strip',
        title: 'Series - polka',
        match: (path) => (path === '/series' ? true : null),
        render: renderSeriesPage,
        mount: (_match, root, context) => initSeries(root, context),
    },
    {
        navId: 'nav-tags',
        mainClass: 'main--strip',
        title: tagsPageTitle,
        match: (path) => (path === '/tags' ? true : null),
        render: renderTagsPage,
        mount: (_match, root, context) => initTags(root, context),
    },
    {
        navId: 'nav-authors',
        mainClass: 'main--strip',
        title: 'Authors - polka',
        match: (path) => (path === '/authors' ? true : null),
        render: renderAuthorsPage,
        mount: (_match, root, context) => initAuthors(root, context),
    },
    {
        navId: 'nav-library',
        mainClass: 'main--strip',
        title: 'Trash - polka',
        match: (path) => (path === '/trash' ? true : null),
        render: renderTrashPage,
        mount: (_match, root, context) => initTrash(root, context.signal),
    },
];

document.addEventListener('DOMContentLoaded', () => {
    const toggle = document.getElementById('sidebar-toggle');
    const sidebar = document.getElementById('sidebar');
    const overlay = document.getElementById('sidebar-overlay');

    const setSidebarOpen = (open: boolean) => {
        sidebar?.classList.toggle('open', open);
        overlay?.classList.toggle('open', open);
        document.querySelector('.layout')?.classList.toggle('sidebar-open', open);
    };

    if (toggle && sidebar && overlay) {
        toggle.addEventListener('click', () => setSidebarOpen(!sidebar.classList.contains('open')));
        overlay.addEventListener('click', () => setSidebarOpen(false));
    }

    initSidebarAccount(() => setSidebarOpen(false));
    const currentUserPromise = fetchCurrentUser();
    initSidebarUpload(currentUserPromise);
    initSidebarShelves();
    loadPersonalSettings().catch(() => {
        /* keep cached/system theme */
    });

    const router = initRouter(routes);
    initNavigation(
        router,
        () => {
            setSidebarOpen(false);
            closeAllModals();
        },
        syncSidebarShelfActive,
    );
    initSidebarCuration(currentUserPromise, navigateApp);
});
