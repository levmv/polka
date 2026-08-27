import { type AuthorOpResult, fetchAuthorPage, renameAuthor, setAuthorSortName } from '../api';
import { errorMessage } from '../errors';
import { icon } from '../icons';
import { createMenu, type ManagedMenu } from '../menu';
import type { AuthorAdmin } from '../types';

interface AuthorsViewState {
    container: HTMLElement;
    statusTimeout: ReturnType<typeof setTimeout> | null;
    // Only one row in this mount may be in edit mode at a time.
    activeEditCancel: (() => void) | null;
    authorMenus: ManagedMenu[];
    destroyed: boolean;
}

export async function initAuthors() {
    const container = document.getElementById('authors-content');
    if (!container) return;

    const state: AuthorsViewState = {
        container,
        statusTimeout: null,
        activeEditCancel: null,
        authorMenus: [],
        destroyed: false,
    };

    const reload = async () => {
        try {
            const page = await fetchAuthorPage();
            if (state.destroyed) return;
            renderAuthors(state, page.items, page.next_cursor || '', reload);
        } catch (_err) {
            if (state.destroyed) return;
            container.innerHTML = `<p class="error">Failed to load authors</p>`;
        }
    };

    await reload();
    return () => cleanupAuthors(state);
}

function cleanupAuthors(state: AuthorsViewState): void {
    state.destroyed = true;
    state.activeEditCancel?.();
    state.activeEditCancel = null;
    destroyAuthorMenus(state);
    if (state.statusTimeout) {
        clearTimeout(state.statusTimeout);
        state.statusTimeout = null;
    }
}

function destroyAuthorMenus(state: AuthorsViewState): void {
    for (const menu of state.authorMenus) {
        menu.destroy();
    }
    state.authorMenus = [];
}

function showStatus(state: AuthorsViewState, msg: string) {
    if (state.destroyed) return;
    const statusEl = document.getElementById('authors-status');
    if (!statusEl) return;
    statusEl.textContent = msg;
    statusEl.removeAttribute('hidden');
    if (state.statusTimeout) clearTimeout(state.statusTimeout);
    state.statusTimeout = setTimeout(() => statusEl.setAttribute('hidden', ''), 5000);
}

function opSummary(prefix: string, res: AuthorOpResult): string {
    const books = res.affected === 1 ? 'book' : 'books';
    const files = res.moved === 1 ? 'file' : 'files';
    let msg = `${prefix} — ${res.affected} ${books} updated, ${res.moved} ${files} moved`;
    if (res.warnings > 0) {
        msg += ` (${res.warnings} ${res.warnings === 1 ? 'warning' : 'warnings'})`;
    }
    return msg;
}

function renderAuthors(
    state: AuthorsViewState,
    authors: AuthorAdmin[],
    initialNextCursor: string,
    reload: () => void,
) {
    const { container } = state;
    state.activeEditCancel?.();
    state.activeEditCancel = null;
    destroyAuthorMenus(state);
    container.innerHTML = '';

    if (authors.length === 0) {
        container.innerHTML = `<p class="all-good">No authors yet.</p>`;
        return;
    }

    const tableWrap = document.createElement('div');
    tableWrap.className = 'authors-table-wrap';

    const table = document.createElement('table');
    table.className = 'authors-table';
    table.innerHTML = `
        <thead>
            <tr>
                <th scope="col">Name</th>
                <th scope="col">Sort as</th>
                <th scope="col">Books</th>
                <th scope="col" class="authors-actions-heading"><span class="sr-only">Actions</span></th>
            </tr>
        </thead>
    `;

    const tbody = document.createElement('tbody');
    table.appendChild(tbody);

    const loadMoreWrap = document.createElement('div');
    loadMoreWrap.className = 'load-more-container authors-load-more';
    const loadMoreBtn = document.createElement('button');
    loadMoreBtn.type = 'button';
    loadMoreBtn.className = 'load-more-btn';
    loadMoreBtn.textContent = 'Show more';
    loadMoreBtn.setAttribute('aria-label', 'Show more authors');
    loadMoreWrap.appendChild(loadMoreBtn);

    const appendAuthors = (next: AuthorAdmin[]) => {
        const fragment = document.createDocumentFragment();
        for (const author of next) {
            fragment.appendChild(createAuthorRow(state, author, reload));
        }
        tbody.appendChild(fragment);
    };
    let nextCursor = initialNextCursor;
    loadMoreWrap.hidden = !nextCursor;
    loadMoreBtn.addEventListener('click', async () => {
        if (!nextCursor || loadMoreBtn.disabled) return;
        loadMoreBtn.disabled = true;
        loadMoreBtn.textContent = 'Loading...';
        try {
            const page = await fetchAuthorPage(nextCursor);
            if (state.destroyed) return;
            appendAuthors(page.items);
            nextCursor = page.next_cursor || '';
            loadMoreWrap.hidden = !nextCursor;
        } catch {
            if (state.destroyed) return;
            showStatus(state, 'Failed to load more authors');
        } finally {
            if (!state.destroyed) {
                loadMoreBtn.disabled = false;
                loadMoreBtn.textContent = 'Show more';
            }
        }
    });

    tableWrap.appendChild(table);
    container.append(tableWrap, loadMoreWrap);
    appendAuthors(authors);
}

function createAuthorRow(
    state: AuthorsViewState,
    author: AuthorAdmin,
    reload: () => void,
): HTMLTableRowElement {
    const row = document.createElement('tr');
    row.className = 'author-row';
    let rowMenu: ManagedMenu | null = null;
    let nameCell: HTMLTableCellElement | null = null;
    let sortCell: HTMLTableCellElement | null = null;
    let actionsBtn: HTMLButtonElement | null = null;

    const clearRow = () => {
        if (rowMenu) {
            rowMenu.destroy();
            state.authorMenus = state.authorMenus.filter((menu) => menu !== rowMenu);
        }
        rowMenu = null;
        actionsBtn = null;
        row.innerHTML = '';
    };

    const showNormalView = () => {
        clearRow();

        nameCell = document.createElement('td');
        nameCell.className = 'author-name-cell';
        const nameBtn = cellEditButton(
            'author-row-name author-cell-edit author-name-edit',
            author.name,
            `Edit display name for ${author.name}`,
        );
        nameBtn.addEventListener('click', () => openNameEditor());
        nameCell.appendChild(nameBtn);
        row.appendChild(nameCell);

        sortCell = document.createElement('td');
        sortCell.className = 'author-sort-cell';
        const sortBtn = cellEditButton(
            'author-row-sort author-cell-edit author-sort-edit',
            author.sort_name,
            `Edit sort name for ${author.name}`,
        );
        sortBtn.addEventListener('click', () => openSortEditor());
        sortCell.appendChild(sortBtn);
        row.appendChild(sortCell);

        const countCell = document.createElement('td');
        countCell.className = 'author-count-cell';
        const countSpan = document.createElement('span');
        countSpan.className = 'author-row-count';
        countSpan.textContent = author.book_count === 1 ? '1 book' : `${author.book_count} books`;
        countCell.appendChild(countSpan);
        row.appendChild(countCell);

        const actionsCell = document.createElement('td');
        actionsCell.className = 'author-actions-cell';
        actionsBtn = document.createElement('button');
        actionsBtn.type = 'button';
        actionsBtn.className = 'author-actions-btn';
        actionsBtn.setAttribute('aria-label', `Actions for ${author.name}`);
        actionsBtn.setAttribute('aria-haspopup', 'menu');
        actionsBtn.setAttribute('aria-expanded', 'false');
        actionsBtn.innerHTML = icon('more_vert', 18);
        actionsBtn.addEventListener('click', (event) => {
            if (rowMenu) return;
            event.preventDefault();
            ensureRowMenu().open();
        });
        actionsBtn.addEventListener('keydown', (event) => {
            if (rowMenu) return;
            if (event.key !== 'Enter' && event.key !== ' ' && event.key !== 'ArrowDown') return;
            event.preventDefault();
            ensureRowMenu().open();
        });
        actionsCell.appendChild(actionsBtn);
        row.appendChild(actionsCell);
    };

    const ensureRowMenu = (): ManagedMenu => {
        if (rowMenu) return rowMenu;
        if (!actionsBtn) throw new Error('author row action trigger missing');
        rowMenu = createMenu(actionsBtn, [
            { label: 'Rename / merge', action: () => openNameEditor() },
            { label: 'Sort as', action: () => openSortEditor() },
        ]);
        state.authorMenus.push(rowMenu);
        return rowMenu;
    };

    const openNameEditor = () => {
        if (!nameCell) return;
        openEditor(nameCell, {
            initial: author.name,
            onSave: async (value) =>
                opSummary(
                    `Renamed "${author.name}" → "${value}"`,
                    await renameAuthor(author.name, value),
                ),
        });
    };

    const openSortEditor = () => {
        if (!sortCell) return;
        openEditor(sortCell, {
            initial: author.sort_name,
            onSave: async (value) =>
                opSummary(
                    `Sort name for "${author.name}" set to "${value}"`,
                    await setAuthorSortName(author.name, value),
                ),
        });
    };

    const openEditor = (
        cell: HTMLTableCellElement,
        opts: { initial: string; onSave: (value: string) => Promise<string> },
    ) => {
        if (state.activeEditCancel) state.activeEditCancel();

        const previousContent = Array.from(cell.childNodes);
        cell.replaceChildren();
        cell.classList.add('author-cell-editing');

        const editor = document.createElement('div');
        editor.className = 'author-cell-editor';

        const input = document.createElement('input');
        input.type = 'text';
        input.className = 'author-edit-input';
        input.value = opts.initial;
        editor.appendChild(input);

        const buttons = document.createElement('div');
        buttons.className = 'author-edit-buttons';

        const saveBtn = document.createElement('button');
        saveBtn.className = 'author-save-btn';
        saveBtn.textContent = 'Save';
        buttons.appendChild(saveBtn);

        const cancelBtn = document.createElement('button');
        cancelBtn.className = 'author-cancel-btn';
        cancelBtn.textContent = 'Cancel';
        buttons.appendChild(cancelBtn);

        const errorDiv = document.createElement('div');
        errorDiv.className = 'author-row-error';
        errorDiv.hidden = true;

        editor.append(buttons, errorDiv);
        cell.appendChild(editor);

        const restoreCell = () => {
            cell.classList.remove('author-cell-editing');
            cell.replaceChildren(...previousContent);
        };

        const cancelEdit = () => {
            if (state.activeEditCancel === cancelEdit) state.activeEditCancel = null;
            restoreCell();
        };
        state.activeEditCancel = cancelEdit;

        const doSave = async () => {
            const value = input.value.trim();
            if (value === '' || value === opts.initial) {
                cancelEdit();
                return;
            }

            input.disabled = saveBtn.disabled = cancelBtn.disabled = true;
            errorDiv.hidden = true;

            try {
                const msg = await opts.onSave(value);
                if (state.destroyed) return;
                state.activeEditCancel = null;
                showStatus(state, msg);
                reload();
            } catch (err: unknown) {
                if (state.destroyed) return;
                input.disabled = saveBtn.disabled = cancelBtn.disabled = false;
                errorDiv.textContent = errorMessage(err, 'Update failed');
                errorDiv.hidden = false;
                input.focus();
            }
        };

        saveBtn.addEventListener('click', () => void doSave());
        cancelBtn.addEventListener('click', cancelEdit);
        input.addEventListener('keydown', (e) => {
            if (e.key === 'Enter') {
                e.preventDefault();
                void doSave();
            } else if (e.key === 'Escape') {
                e.preventDefault();
                cancelEdit();
            }
        });

        setTimeout(() => input.focus(), 0);
    };

    showNormalView();
    return row;
}

function cellEditButton(className: string, text: string, label: string): HTMLButtonElement {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = className;
    button.textContent = text;
    button.setAttribute('aria-label', label);
    return button;
}
