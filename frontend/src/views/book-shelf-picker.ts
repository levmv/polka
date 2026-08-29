import { addBookToShelf, fetchBookShelves, fetchCurrentUser, removeBookFromShelf } from '../api';
import { notifyCatalogChanged } from '../catalog-events';
import { errorMessage } from '../errors';
import { icon } from '../icons';
import type { ManagedPopover } from '../popover';
import { openCreateShelfDialog } from '../shelf-dialog';
import { notifyShelvesChanged } from '../sidebar-shelves';
import { showToast } from '../toast';
import type { BookShelfMembership } from '../types';

// Above this many shelves the picker grows a filter box; below it the list is
// short enough to just scan.
const SHELF_SEARCH_THRESHOLD = 7;

export function renderShelfPicker(
    panel: HTMLElement,
    popover: ManagedPopover,
    workId: string,
): void {
    panel.classList.add('shelf-popover');
    // Arrow-key nav reads the live DOM, so bind once even though the popover
    // re-renders this panel on every open.
    if (!panel.dataset.navBound) {
        panel.addEventListener('keydown', handleShelfNav);
        panel.dataset.navBound = '1';
    }
    panel.innerHTML = '<p class="shelf-popover-empty">Loading…</p>';

    void (async () => {
        try {
            const memberships = await fetchBookShelves(workId);
            buildShelfPicker(panel, popover, workId, memberships);
            // Focus here, not via the popover's initial focus — that fires while
            // this panel still shows "Loading…", before the controls exist.
            popover.reposition();
            focusPicker(panel);
        } catch (e) {
            console.error('Failed to load shelves:', e);
            panel.innerHTML = '<p class="shelf-popover-empty">Could not load shelves.</p>';
            popover.reposition();
        }
    })();
}

// Focus the filter box if present, else the first shelf checkbox.
function focusPicker(panel: HTMLElement): void {
    const target =
        panel.querySelector<HTMLElement>('.shelf-popover-search') ??
        panel.querySelector<HTMLElement>('.shelf-picker-row input');
    target?.focus({ preventScroll: true });
}

// Up/Down move focus through the visible controls (filter box, checkboxes,
// create button), wrapping around and skipping filtered-out rows.
function handleShelfNav(event: KeyboardEvent): void {
    if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return;
    const panel = event.currentTarget as HTMLElement;
    const items = Array.from(panel.querySelectorAll<HTMLElement>('input, button')).filter(
        (el) => el.offsetParent !== null && !(el as HTMLButtonElement).disabled,
    );
    if (items.length === 0) return;
    event.preventDefault();
    const current = items.indexOf(document.activeElement as HTMLElement);
    const delta = event.key === 'ArrowDown' ? 1 : -1;
    const next =
        current < 0
            ? delta === 1
                ? 0
                : items.length - 1
            : (current + delta + items.length) % items.length;
    items[next].focus();
}

function buildShelfPicker(
    panel: HTMLElement,
    popover: ManagedPopover,
    workId: string,
    memberships: BookShelfMembership[],
): void {
    panel.replaceChildren();

    let searchInput: HTMLInputElement | null = null;
    if (memberships.length > SHELF_SEARCH_THRESHOLD) {
        searchInput = document.createElement('input');
        searchInput.type = 'text';
        searchInput.className = 'shelf-popover-search';
        searchInput.placeholder = 'Filter shelves';
        searchInput.setAttribute('aria-label', 'Filter shelves');
        panel.appendChild(searchInput);
    }

    const list = document.createElement('div');
    list.className = 'shelf-picker-list';
    panel.appendChild(list);

    if (memberships.length === 0) {
        const empty = document.createElement('p');
        empty.className = 'shelf-popover-empty';
        empty.textContent = 'No shelves yet.';
        list.appendChild(empty);
    } else {
        for (const membership of memberships) {
            list.appendChild(shelfPickerRow(membership, workId));
        }
    }

    if (searchInput) {
        searchInput.addEventListener('input', () => {
            const q = searchInput.value.trim().toLowerCase();
            for (const row of list.querySelectorAll<HTMLElement>('.shelf-picker-row')) {
                row.hidden = !(row.dataset.name || '').includes(q);
            }
        });
    }

    panel.appendChild(buildCreateRow(popover, workId));
}

function shelfPickerRow(membership: BookShelfMembership, workId: string): HTMLElement {
    const label = document.createElement('label');
    label.className = 'shelf-picker-row';
    label.dataset.name = membership.name.toLowerCase();

    const checkbox = document.createElement('input');
    checkbox.type = 'checkbox';
    checkbox.checked = membership.in_shelf;

    const name = document.createElement('span');
    name.className = 'shelf-picker-name';
    name.textContent = membership.name;

    // Guard re-entrancy with a flag, not `disabled` — disabling the focused
    // checkbox blurs it (focus jumps to <body>), which kills the popover's
    // keyboard nav after a single toggle.
    let pending = false;
    checkbox.addEventListener('change', async () => {
        if (pending) {
            checkbox.checked = !checkbox.checked;
            return;
        }
        pending = true;
        const checked = checkbox.checked;
        try {
            if (checked) {
                await addBookToShelf(membership.id, workId);
            } else {
                await removeBookFromShelf(membership.id, workId);
            }
            // A shelf-scoped list may no longer contain this book, and only that
            // list knows which shelf it shows.
            notifyCatalogChanged();
        } catch (e) {
            console.error('Failed to update shelf membership:', e);
            checkbox.checked = !checked;
            showToast(
                errorMessage(e, checked ? 'Add to shelf failed' : 'Remove from shelf failed'),
                {
                    type: 'error',
                },
            );
        } finally {
            pending = false;
        }
    });

    label.append(checkbox, name);
    return label;
}

// The create affordance opens the normal shelf dialog. Close this popover first
// so the floating panel cannot sit above the modal.
function buildCreateRow(popover: ManagedPopover, workId: string): HTMLElement {
    const wrap = document.createElement('div');
    wrap.className = 'shelf-popover-create';

    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'shelf-popover-create-btn';
    button.innerHTML = `${icon('add', 16)}<span>New shelf</span>`;
    wrap.appendChild(button);

    button.addEventListener('click', async () => {
        button.disabled = true;
        popover.close();
        try {
            const currentUser = await fetchCurrentUser();
            const shelf = await openCreateShelfDialog({ currentUser, kind: 'manual' });
            if (!shelf) return;
            await addBookToShelf(shelf.id, workId);
            notifyShelvesChanged();
            notifyCatalogChanged();
        } catch (e) {
            console.error('Failed to create shelf:', e);
            showToast(errorMessage(e, 'Shelf update failed'), { type: 'error' });
        } finally {
            button.disabled = false;
        }
    });

    return wrap;
}
