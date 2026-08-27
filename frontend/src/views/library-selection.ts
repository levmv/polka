// Library bulk selection uses direct checkboxes and a bottom floating action
// panel; only curators can select.
import { bulkTrashBooks, bulkWritebackBooks } from '../api';
import { errorMessage } from '../errors';
import { icon } from '../icons';
import { confirmModal } from '../modal';
import { showToast } from '../toast';
import type { BookSummary, BulkEditResult } from '../types';
import {
    type BulkShelfOutcome,
    openBulkAuthorsDialog,
    openBulkSeriesDialog,
    openBulkShelvesDialog,
    openBulkTagsDialog,
} from './bulk-edit-dialogs';

export interface LibrarySelection {
    // Turn selection on/off for the current user (catalog curators only). When
    // disabled the checkboxes stay hidden and any selection is dropped.
    setEnabled(on: boolean): void;
    // Drop the current selection (also how the bar's ✕ exits). Kept when the
    // underlying list changes so stale ids never linger.
    clearSelection(): void;
    // Re-apply selected styling/checkbox state after the grid/table re-rendered
    // or a row was swapped, and prune ids no longer loaded.
    syncAfterRender(): void;
    // Rebuilds the floating action panel when an external capability changes.
    refreshActions(): void;
    destroy(): void;
}

export interface SelectionOptions {
    container: HTMLElement;
    getBooks(): BookSummary[];
    canWriteback(): boolean;
    // Patch the rendered rows (and view state) for the returned summaries.
    onApplied(updated: BookSummary[]): void;
    // Drop the given works from the rendered list and view state (bulk trash).
    onRemoved(ids: string[]): void;
}

export function createLibrarySelection(opts: SelectionOptions): LibrarySelection {
    const selected = new Set<string>();
    let enabled = false;
    let bar: HTMLElement | null = null;
    let countEl: HTMLElement | null = null;
    let actionButtons: HTMLButtonElement[] = [];

    const selectedBooks = (): BookSummary[] => opts.getBooks().filter((b) => selected.has(b.id));

    const handleApplied = (result: BulkEditResult) => {
        opts.onApplied(result.books);
        updateUI();
        let message: string;
        if (result.changed === 0) {
            message = 'No changes';
        } else {
            message = result.changed === 1 ? 'Updated 1 book' : `Updated ${result.changed} books`;
        }
        if (result.relayout_warnings > 0) {
            const w = result.relayout_warnings;
            message += ` · ${w} file move ${w === 1 ? 'warning' : 'warnings'}`;
        }
        showToast(message);
    };

    // Shelving does not change the books themselves (per-user membership), so it
    // just reports the delta; the selection stays put for a follow-up action.
    const handleShelved = ({ changed, op, shelfName }: BulkShelfOutcome) => {
        let message: string;
        if (changed === 0) {
            message = op === 'add' ? `Already on “${shelfName}”` : `Not on “${shelfName}”`;
        } else {
            const noun = changed === 1 ? 'book' : 'books';
            message =
                op === 'add'
                    ? `Added ${changed} ${noun} to “${shelfName}”`
                    : `Removed ${changed} ${noun} from “${shelfName}”`;
        }
        showToast(message);
    };

    // Soft-delete the selection: confirm, move to Trash (reversible, files kept),
    // then drop the trashed rows from the view.
    const runTrash = async (books: BookSummary[]): Promise<void> => {
        const n = books.length;
        const ok = await confirmModal({
            title: n === 1 ? 'Remove from library?' : `Remove ${n} books from library?`,
            body:
                n === 1
                    ? `“${books[0].title}” moves to Trash. You can restore it later; the files are kept until it is permanently deleted.`
                    : `${n} books move to Trash. You can restore them later; the files are kept until they are permanently deleted.`,
            confirmLabel: 'Remove',
            danger: true,
        });
        if (!ok) return;
        try {
            const result = await bulkTrashBooks(books.map((b) => b.id));
            for (const id of result.ids) selected.delete(id);
            opts.onRemoved(result.ids);
            updateUI();
            const t = result.trashed;
            showToast(t === 1 ? 'Moved 1 book to Trash' : `Moved ${t} books to Trash`);
        } catch (e) {
            showToast(errorMessage(e, 'Failed to move books to Trash'), { type: 'error' });
        }
    };

    const runWriteback = async (books: BookSummary[]): Promise<void> => {
        try {
            const result = await bulkWritebackBooks(books.map((b) => b.id));
            const n = result.queued;
            showToast(
                n === 0
                    ? 'No writable files selected'
                    : `Queued metadata write for ${n} ${n === 1 ? 'file' : 'files'}`,
            );
        } catch (e) {
            showToast(errorMessage(e, 'Failed to queue metadata write'), { type: 'error' });
        }
    };

    const buildBar = () => {
        const el = document.createElement('div');
        el.className = 'bulk-bar';
        el.setAttribute('role', 'region');
        el.setAttribute('aria-label', 'Bulk actions');
        el.innerHTML = `
            <div class="bulk-bar-actions">
                <button type="button" class="bulk-bar-action" data-action="authors" aria-label="Author" title="Author">${icon('person', 18)}<span>Author</span></button>
                <button type="button" class="bulk-bar-action" data-action="tags" aria-label="Tags" title="Tags">${icon('sell', 18)}<span>Tags</span></button>
                <button type="button" class="bulk-bar-action" data-action="series" aria-label="Series" title="Series">${icon('menu_book', 18)}<span>Series</span></button>
                <button type="button" class="bulk-bar-action" data-action="shelves" aria-label="Shelves" title="Shelves">${icon('bookmark', 18)}<span>Shelves</span></button>
                ${
                    opts.canWriteback()
                        ? `<button type="button" class="bulk-bar-action" data-action="writeback" aria-label="Write metadata" title="Write metadata to files">${icon('check', 18)}<span>Write metadata</span></button>`
                        : ''
                }
                <span class="bulk-bar-divider" aria-hidden="true"></span>
                <button type="button" class="bulk-bar-action bulk-bar-action--danger bulk-bar-action--icon" data-action="delete" aria-label="Delete" title="Delete">${icon('delete', 18)}</button>
            </div>
            <div class="bulk-bar-end">
                <span class="bulk-bar-count" data-count></span>
                <button type="button" class="bulk-bar-exit" aria-label="Clear selection">${icon('close', 18)}</button>
            </div>`;

        el.querySelector('.bulk-bar-exit')?.addEventListener('click', () => clearSelection());
        actionButtons = Array.from(el.querySelectorAll<HTMLButtonElement>('.bulk-bar-action'));
        for (const btn of actionButtons) {
            btn.addEventListener('click', () => {
                const books = selectedBooks();
                if (books.length === 0) return;
                if (btn.dataset.action === 'authors') openBulkAuthorsDialog(books, handleApplied);
                else if (btn.dataset.action === 'tags') openBulkTagsDialog(books, handleApplied);
                else if (btn.dataset.action === 'series')
                    openBulkSeriesDialog(books, handleApplied);
                else if (btn.dataset.action === 'shelves')
                    openBulkShelvesDialog(books, handleShelved);
                else if (btn.dataset.action === 'writeback') void runWriteback(books);
                else if (btn.dataset.action === 'delete') void runTrash(books);
            });
        }
        countEl = el.querySelector('[data-count]');
        return el;
    };

    const removeBar = () => {
        bar?.remove();
        bar = null;
        countEl = null;
        actionButtons = [];
    };

    // Reflect the selection set onto the DOM: card/row styling, checkbox state,
    // the table select-all tristate, the body class that reveals every grid
    // checkbox once a selection exists, and the floating bar.
    function updateUI(): void {
        const showSelection = enabled && selected.size > 0;
        document.body.classList.toggle('has-selection', showSelection);

        for (const card of opts.container.querySelectorAll<HTMLElement>('.book-card')) {
            const on = selected.has(card.dataset.id || '');
            card.classList.toggle('selected', on);
            const cb = card.querySelector('.card-select');
            cb?.classList.toggle('checked', on);
            cb?.setAttribute('aria-checked', on ? 'true' : 'false');
        }
        for (const row of opts.container.querySelectorAll<HTMLElement>('.table-row')) {
            const on = selected.has(row.dataset.id || '');
            row.classList.toggle('selected', on);
            const cb = row.querySelector<HTMLInputElement>('.table-select-row');
            if (cb) cb.checked = on;
        }
        const all = opts.container.querySelector<HTMLInputElement>('.table-select-all');
        if (all) {
            const ids = opts.getBooks().map((b) => b.id);
            const count = ids.filter((id) => selected.has(id)).length;
            all.checked = count > 0 && count === ids.length;
            all.indeterminate = count > 0 && count < ids.length;
        }

        if (showSelection) {
            if (!bar) {
                bar = buildBar();
                document.body.appendChild(bar);
            }
            // The count keeps the word in a span so a very narrow bar can drop it
            // (to just the number) and still fit on one row.
            if (countEl)
                countEl.innerHTML = `${selected.size}&nbsp;<span class="bulk-bar-count-word">selected</span>`;
        } else {
            removeBar();
        }
    }

    function setSelected(id: string, on: boolean): void {
        if (on) selected.add(id);
        else selected.delete(id);
        updateUI();
    }

    function clearSelection(): void {
        if (selected.size > 0) selected.clear();
        updateUI();
    }

    function syncAfterRender(): void {
        const loaded = new Set(opts.getBooks().map((b) => b.id));
        for (const id of Array.from(selected)) {
            if (!loaded.has(id)) selected.delete(id);
        }
        updateUI();
    }

    // Fast selection, capture-phase so a toggle beats the cover/title link:
    // the checkbox always toggles, and once a selection exists the whole cover or
    // row toggles too — so you can rattle through covers without aiming. With
    // nothing selected yet, a cover click still opens the book.
    const onClick = (event: Event) => {
        if (!enabled) return;
        const target = event.target instanceof Element ? event.target : null;
        if (!target) return;

        const card = target.closest<HTMLElement>('.book-card');
        if (card?.dataset.id) {
            const onCheckbox = Boolean(target.closest('.card-select'));
            if (!onCheckbox && selected.size === 0) return; // let the cover navigate
            event.preventDefault();
            event.stopPropagation();
            setSelected(card.dataset.id, !selected.has(card.dataset.id));
            return;
        }

        const row = target.closest<HTMLElement>('.table-row');
        if (row?.dataset.id) {
            // The checkbox cell toggles through its label/change; skip it here so
            // it isn't toggled twice. Otherwise the whole row toggles once
            // selecting, and stays inert (links/quick-edit work) before that.
            if (target.closest('.col-select')) return;
            if (selected.size === 0) return;
            event.preventDefault();
            event.stopPropagation();
            setSelected(row.dataset.id, !selected.has(row.dataset.id));
        }
    };
    opts.container.addEventListener('click', onClick, true);

    // Table checkbox cell (its label toggles the input natively) and select-all.
    const onChange = (event: Event) => {
        if (!enabled) return;
        const target = event.target;
        if (!(target instanceof HTMLInputElement)) return;
        if (target.classList.contains('table-select-row')) {
            const row = target.closest<HTMLElement>('.table-row');
            if (row?.dataset.id) setSelected(row.dataset.id, target.checked);
        } else if (target.classList.contains('table-select-all')) {
            const on = target.checked;
            for (const id of opts.getBooks().map((b) => b.id)) {
                if (on) selected.add(id);
                else selected.delete(id);
            }
            updateUI();
        }
    };
    opts.container.addEventListener('change', onChange);

    const onKeydown = (event: KeyboardEvent) => {
        // defaultPrevented covers a control that already handled Escape — notably
        // the search box, whose own handler clears the query first.
        if (event.key !== 'Escape' || !enabled || selected.size === 0 || event.defaultPrevented) {
            return;
        }
        // Also yield to an open modal or menu; their own Escape handling wins.
        if (
            document.querySelector(
                '.modal-backdrop[aria-hidden="false"], .floating-menu:not([hidden]), .floating-panel:not([hidden])',
            )
        ) {
            return;
        }
        event.preventDefault();
        clearSelection();
    };
    document.addEventListener('keydown', onKeydown);

    return {
        setEnabled: (on) => {
            enabled = on;
            document.body.classList.toggle('can-curate', on);
            if (!on) selected.clear();
            updateUI();
        },
        clearSelection,
        syncAfterRender,
        refreshActions: () => {
            if (bar) removeBar();
            updateUI();
        },
        destroy: () => {
            opts.container.removeEventListener('click', onClick, true);
            opts.container.removeEventListener('change', onChange);
            document.removeEventListener('keydown', onKeydown);
            document.body.classList.remove('has-selection', 'can-curate');
            removeBar();
        },
    };
}
