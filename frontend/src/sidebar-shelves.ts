import { deleteShelf, fetchCurrentUser, fetchShelves } from './api';
import { readBookListContextFromLocation } from './book-list-context';
import { errorMessage } from './errors';
import { icon } from './icons';
import { createMenu, type ManagedMenu, type MenuItem } from './menu';
import { openCreateShelfDialog, openEditShelfDialog } from './shelf-dialog';
import { showToast } from './toast';
import type { CurrentUser, Shelf } from './types';

const SHELVES_CHANGED = 'polka:shelves-changed';

export function notifyShelvesChanged(): void {
    document.dispatchEvent(new CustomEvent(SHELVES_CHANGED));
}

// Floating menus are appended to <body> by createMenu, so they outlive a
// list.innerHTML reset. Track them and destroy before each re-render to avoid
// leaking detached menu nodes and their document listeners.
let activeMenus: ManagedMenu[] = [];

// A full-page navigation can reject in-flight fetches; that is not a real load
// failure. Track the page going away so the shelves loader stays quiet in that
// case (the same stance the library view takes toward a cancelled stale
// request), while still surfacing a genuine load error when the page isn't
// navigating.
let navigatingAway = false;
if (typeof window !== 'undefined') {
    window.addEventListener('pagehide', () => {
        navigatingAway = true;
    });
}

export function initSidebarShelves(): void {
    const list = document.getElementById('shelf-nav');
    const newBtn = document.getElementById('new-shelf-btn') as HTMLButtonElement | null;
    if (!list) return;

    let currentUser: CurrentUser | null = null;
    let loadGeneration = 0;
    const load = async () => {
        const generation = ++loadGeneration;
        try {
            const [shelves, me] = await Promise.all([fetchShelves(), fetchCurrentUser()]);
            if (generation !== loadGeneration) return;
            currentUser = me;
            renderShelves(list, shelves, me, load);
        } catch (e) {
            if (generation === loadGeneration && !navigatingAway) {
                console.error('Failed to load shelves:', e);
                showToast(errorMessage(e, 'Failed to load shelves'), { type: 'error' });
            }
        }
    };

    newBtn?.addEventListener('click', async () => {
        try {
            const me = currentUser || (await fetchCurrentUser());
            const shelf = await openCreateShelfDialog({ currentUser: me, kind: 'manual' });
            if (!shelf) return;
            notifyShelvesChanged();
        } catch (e) {
            console.error('Failed to create shelf:', e);
            showToast(errorMessage(e, 'Create shelf failed'), { type: 'error' });
        }
    });
    document.addEventListener(SHELVES_CHANGED, load);
    load();
}

function currentShelfID(): string {
    return new URLSearchParams(window.location.search).get('shelf') || '';
}

export function syncSidebarShelfActive(): void {
    const path = window.location.pathname;
    const isLibrary =
        path === '/' || path === '/index.html' || path === '/cleanup' || path === '/trash';
    const currentShelf = activeShelfID(path);
    let activeShelf = false;

    document.querySelectorAll<HTMLAnchorElement>('#shelf-nav .shelf-nav-item').forEach((item) => {
        const url = new URL(item.href, window.location.href);
        const shelfID = url.searchParams.get('shelf') || '';
        const active = currentShelf !== '' && shelfID === currentShelf;
        item.classList.toggle('active', active);
        activeShelf = activeShelf || active;
    });

    document.getElementById('nav-library')?.classList.toggle('active', isLibrary && !activeShelf);
}

function activeShelfID(path: string): string {
    if (path === '/' || path === '/index.html') return currentShelfID();
    if (!path.startsWith('/book/')) return '';

    const context = readBookListContextFromLocation();
    return context?.source === 'library' ? context.shelf || '' : '';
}

function renderShelves(
    list: HTMLElement,
    shelves: Shelf[],
    currentUser: CurrentUser,
    reload: () => Promise<void>,
): void {
    for (const menu of activeMenus) menu.destroy();
    activeMenus = [];

    list.innerHTML = '';
    for (const shelf of shelves) {
        list.appendChild(renderShelfRow(shelf, currentUser, reload));
    }
    syncSidebarShelfActive();
}

function renderShelfRow(
    shelf: Shelf,
    currentUser: CurrentUser,
    reload: () => Promise<void>,
): HTMLElement {
    const li = document.createElement('li');
    li.className = 'shelf-nav-row';

    const a = document.createElement('a');
    a.href = `/?shelf=${encodeURIComponent(shelf.id)}`;
    a.className = 'nav-item shelf-nav-item';

    const marker = document.createElement('span');
    marker.className = 'shelf-kind-marker';
    marker.dataset.kind = shelf.kind;
    marker.setAttribute('aria-hidden', 'true');

    const label = document.createElement('span');
    label.className = 'shelf-nav-label';
    label.textContent = shelf.name;
    a.append(marker, label);

    li.appendChild(a);

    if (!canMutateShelf(currentUser, shelf)) {
        return li;
    }

    const actions = document.createElement('button');
    actions.type = 'button';
    actions.className = 'sidebar-icon-btn shelf-actions-btn';
    actions.setAttribute('aria-label', `Actions for ${shelf.name}`);
    actions.innerHTML = icon('more_vert', 18);
    li.appendChild(actions);

    const items: MenuItem[] = [{ label: 'Edit', action: () => void editShelf(shelf) }];
    if (canDeleteShelf(currentUser, shelf)) {
        items.push({ label: 'Delete', action: () => showDeleteConfirm(li, shelf, reload) });
    }

    const menu = createMenu(actions, items, {
        onOpen: () => li.classList.add('shelf-nav-row--menu-open'),
        onClose: () => li.classList.remove('shelf-nav-row--menu-open'),
    });
    activeMenus.push(menu);

    return li;

    async function editShelf(target: Shelf): Promise<void> {
        try {
            const updated = await openEditShelfDialog({ currentUser, shelf: target });
            if (!updated) return;
            notifyShelvesChanged();
        } catch (e) {
            console.error('Failed to update shelf:', e);
            showToast(errorMessage(e, 'Shelf update failed'), { type: 'error' });
        }
    }
}

function canMutateShelf(currentUser: CurrentUser, shelf: Shelf): boolean {
    if (currentUser.role === 'admin' || currentUser.role === 'member') return true;
    return shelf.visibility === 'personal' && shelf.owner_id === currentUser.id;
}

function canDeleteShelf(currentUser: CurrentUser, shelf: Shelf): boolean {
    if (shelf.owner_id !== currentUser.id) return false;
    if (shelf.visibility === 'shared') {
        return currentUser.role === 'admin' || currentUser.role === 'member';
    }
    return true;
}

function showDeleteConfirm(li: HTMLElement, shelf: Shelf, reload: () => Promise<void>): void {
    const bar = document.createElement('div');
    bar.className = 'shelf-delete-confirm';
    bar.innerHTML = `
        <span class="shelf-delete-text">Delete shelf?</span>
        <button class="shelf-delete-yes" type="button">Delete</button>
        <button class="shelf-delete-no" type="button">Cancel</button>
    `;
    li.replaceChildren(bar);

    const yes = bar.querySelector('.shelf-delete-yes') as HTMLButtonElement;
    const no = bar.querySelector('.shelf-delete-no') as HTMLButtonElement;

    no.addEventListener('click', () => void reload());
    yes.addEventListener('click', async () => {
        yes.disabled = true;
        no.disabled = true;
        try {
            await deleteShelf(shelf.id);
            notifyShelvesChanged();
            // If we're currently viewing the deleted shelf, fall back to the
            // full library; otherwise just refresh the sidebar list.
            if (currentShelfID() === shelf.id) {
                window.location.href = '/';
                return;
            }
            await reload();
        } catch (e) {
            console.error('Failed to delete shelf:', e);
            showToast(errorMessage(e, 'Delete shelf failed'), { type: 'error' });
            void reload();
        }
    });

    no.focus();
}
