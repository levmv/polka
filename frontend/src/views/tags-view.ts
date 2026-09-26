import {
    deleteTag,
    fetchCurrentUser,
    fetchTagPage,
    renameTag,
    type TagKind,
    type TagSort,
    type TagSummary,
} from '../api';
import { attachTagAutocomplete } from '../components/book-metadata-autocomplete';
import { type CollectionView, createCollectionControls } from '../components/collection-controls';
import { debounce, escapeHtml } from '../dom';
import { errorMessage } from '../errors';
import { icon } from '../icons';
import { confirmModal, type ManagedModal, openModal } from '../modal';
import { queryTerm } from '../search-query';
import { showToast } from '../toast';

type TagLocation = Pick<TagSummary, 'id' | 'label'>;
type TagsState = {
    kind: TagKind;
    view: CollectionView;
    sort: TagSort;
    query: string;
    gridPath: TagLocation[];
    expanded: number[];
};

type Branch = {
    parentID: number;
    items: TagSummary[];
    cursor: string;
    loaded: boolean;
    request: AbortController | null;
    error: string;
};

function selectedKind(): TagKind {
    return new URLSearchParams(window.location.search).get('kind') === 'tag' ? 'tag' : 'genre';
}

export function tagsPageTitle(): string {
    return `${selectedKind() === 'genre' ? 'Genres' : 'Tags'} - polka`;
}

export function renderTagsPage(): string {
    const kind = selectedKind();
    const headings = (['genre', 'tag'] as const)
        .map((item) => {
            const current = item === kind;
            const label = item === 'genre' ? 'Genres' : 'Tags';
            const link = `<a href="/tags?kind=${item}" data-nav ${current ? 'aria-current="page"' : ''}>${label}</a>`;
            return current ? `<h1>${link}</h1>` : link;
        })
        .join('');
    return `<div class="page-container tags-container">
        <div class="tags-toolbar">
            <nav class="page-heading-row dictionary-heading" aria-label="Dictionary">
                ${headings}
            </nav>
            <input type="search" class="form-input tags-search" aria-label="Search ${kind}s" placeholder="Find a ${kind}…">
            <div class="collection-controls"></div>
        </div>
        <nav class="tags-path" aria-label="${kind === 'genre' ? 'Genre' : 'Tag'} path" hidden></nav>
        <div class="tags-browser"></div>
    </div>`;
}

export function initTags(root: HTMLElement, signal: AbortSignal): () => void {
    const kind = selectedKind();
    const label = kind === 'genre' ? 'Genre' : 'Tag';
    const search = root.querySelector<HTMLInputElement>('.tags-search')!;
    const content = root.querySelector<HTMLElement>('.tags-browser')!;
    const breadcrumbs = root.querySelector<HTMLElement>('.tags-path')!;
    const saved = window.history.state?.polkaTags as TagsState | undefined;
    const state = saved?.kind === kind ? saved : undefined;
    let view: CollectionView =
        state?.view ?? (localStorage.getItem('polka-tags-view') === 'table' ? 'table' : 'grid');
    let sort: TagSort = state?.sort ?? 'books';
    let query = state?.query ?? '';
    let gridPath = state?.gridPath ?? [];
    const expanded = new Set(state?.expanded);
    const branches = new Map<number, Branch>();
    let canEdit = false;
    let ready = false;
    let editor: ManagedModal | null = null;

    // Both views share pages. Collapsing a branch or switching views keeps its
    // pending request; changing the search/sort or leaving the page cancels it.
    function getBranch(parentID: number): Branch {
        let branch = branches.get(parentID);
        if (!branch) {
            branch = { parentID, items: [], cursor: '', loaded: false, request: null, error: '' };
            branches.set(parentID, branch);
            void loadBranch(branch);
        }
        return branch;
    }

    async function loadBranch(branch: Branch): Promise<void> {
        if (branch.request || (branch.loaded && !branch.cursor)) return;
        const request = new AbortController();
        branch.request = request;
        branch.error = '';
        try {
            const page = await fetchTagPage(
                kind,
                {
                    query,
                    sort,
                    parentID: branch.parentID,
                    cursor: branch.cursor,
                },
                request.signal,
            );
            if (signal.aborted || request.signal.aborted) return;
            branch.items.push(...page.items);
            branch.cursor = page.next_cursor || '';
            branch.loaded = true;
        } catch (err) {
            if (signal.aborted || request.signal.aborted) return;
            branch.error = errorMessage(err, `Failed to load ${kind}s`);
        } finally {
            branch.request = null;
            if (!signal.aborted && !request.signal.aborted) renderResults();
        }
    }

    function cancelLoads(): void {
        for (const branch of branches.values()) branch.request?.abort();
    }

    function reload(resetNavigation = false): void {
        cancelLoads();
        branches.clear();
        if (resetNavigation) {
            gridPath = [];
            expanded.clear();
        }
        renderNavigation();
        renderResults();
    }

    function saveState(): void {
        const next: TagsState = { kind, view, sort, query, gridPath, expanded: [...expanded] };
        window.history.replaceState({ ...window.history.state, polkaTags: next }, '');
    }

    function renderNavigation(): void {
        breadcrumbs.replaceChildren();
        breadcrumbs.hidden = view !== 'grid' || !!query || !gridPath.length;
        if (breadcrumbs.hidden) return;
        const crumb = (text: string, length: number) => {
            const button = document.createElement('button');
            button.type = 'button';
            button.textContent = text;
            if (length === gridPath.length) button.setAttribute('aria-current', 'page');
            button.addEventListener('click', () => {
                gridPath = gridPath.slice(0, length);
                renderNavigation();
                renderResults();
                if (!breadcrumbs.hidden)
                    breadcrumbs.querySelector<HTMLButtonElement>('[aria-current]')?.focus();
                else search.focus();
            });
            return button;
        };
        breadcrumbs.append(crumb(`All ${kind}s`, 0));
        gridPath.forEach((tag, index) => {
            breadcrumbs.insertAdjacentHTML('beforeend', '<span aria-hidden="true">›</span>');
            breadcrumbs.append(crumb(tag.label, index + 1));
        });
    }

    function branchStatus(branch: Branch): HTMLElement | null {
        const status = document.createElement('div');
        status.className = 'tag-page-status';
        if (branch.error) {
            status.innerHTML = `<p class="error" role="alert">${escapeHtml(branch.error)}</p>`;
        } else if (branch.request) {
            status.innerHTML = '<span role="status">Loading…</span>';
        } else if (branch.loaded && !branch.items.length) {
            const message = query
                ? `No matching ${kind}s.`
                : branch.parentID
                  ? `No ${kind}s here.`
                  : `No ${kind}s yet.${canEdit ? ` Add ${kind}s when editing a book.` : ''}`;
            status.innerHTML = `<p class="tags-empty">${message}</p>`;
        }
        if (branch.error || branch.cursor) {
            const more = document.createElement('button');
            more.type = 'button';
            more.id = `tag-more-${branch.parentID}`;
            more.className = 'load-more-btn';
            more.textContent = branch.error ? 'Retry' : 'Show more';
            more.disabled = !!branch.request;
            more.addEventListener('click', () => {
                void loadBranch(branch);
                renderResults();
            });
            status.append(more);
        }
        return status.childNodes.length ? status : null;
    }

    function renderGrid(): DocumentFragment {
        const fragment = document.createDocumentFragment();
        const parentID = query ? 0 : (gridPath[gridPath.length - 1]?.id ?? 0);
        const branch = getBranch(parentID);
        const list = document.createElement('ul');
        list.className = 'tags-grid';
        list.setAttribute('aria-busy', String(!!branch.request));
        for (const tag of branch.items) list.append(makeRow(tag, 0));
        fragment.append(list);
        const status = branchStatus(branch);
        if (status) fragment.append(status);
        return fragment;
    }

    function renderTable(): HTMLTableElement {
        const table = document.createElement('table');
        table.className = 'tags-table';
        table.innerHTML = `<thead><tr><th scope="col">Name</th><th scope="col" class="tag-count-cell">Books</th>${canEdit ? '<th scope="col" class="tag-actions" aria-label="Actions"></th>' : ''}</tr></thead><tbody></tbody>`;
        const body = table.querySelector('tbody')!;
        const appendBranch = (parentID: number, depth: number) => {
            const branch = getBranch(parentID);
            for (const tag of branch.items) {
                body.append(makeRow(tag, depth));
                if (!query && tag.has_children && expanded.has(tag.id))
                    appendBranch(tag.id, depth + 1);
            }
            const status = branchStatus(branch);
            if (status) {
                const row = body.insertRow();
                const cell = row.insertCell();
                cell.colSpan = canEdit ? 3 : 2;
                cell.className = 'tag-status-cell';
                cell.style.setProperty('--tag-depth', String(depth));
                cell.append(status);
            }
        };
        appendBranch(0, 0);
        return table;
    }

    function renderResults(): void {
        if (signal.aborted) return;
        saveState();
        if (!ready) return;
        const active = document.activeElement;
        const focusID = active && content.contains(active) ? active.id : '';
        content.replaceChildren(view === 'grid' ? renderGrid() : renderTable());
        if (focusID)
            content
                .querySelector<HTMLElement>(`#${CSS.escape(focusID)}`)
                ?.focus({ preventScroll: true });
    }

    const openRename = (tag: TagSummary) => {
        editor?.close();
        const { modal, root: dialog } = openModal({
            title: `Rename ${kind}`,
            modalClass: 'tag-rename-modal',
            body: `<form id="tag-rename-form">
                <label for="tag-name">Name</label>
                <input id="tag-name" class="form-input" value="${escapeHtml(tag.name)}" required>
                <p class="tag-rename-hint">Renames this path and any children on all books. Use dots for nesting. Existing paths merge.</p>
                <p class="error" role="alert" hidden></p>
            </form>`,
            actions: `<button type="button" class="btn-confirm-cancel tag-cancel">Cancel</button><button type="submit" form="tag-rename-form" class="btn-confirm tag-save">Save</button>`,
            onClose: () => {
                autocomplete.close();
                editor = null;
            },
        });
        editor = modal;
        const input = dialog.querySelector<HTMLInputElement>('#tag-name')!;
        const save = dialog.querySelector<HTMLButtonElement>('.tag-save')!;
        const message = dialog.querySelector<HTMLElement>('.error')!;
        const autocomplete = attachTagAutocomplete(input, kind);
        dialog.querySelector('.tag-cancel')!.addEventListener('click', () => modal.close());
        dialog.querySelector('form')!.addEventListener('submit', async (event) => {
            event.preventDefault();
            if (save.disabled) return;
            const name = input.value.trim();
            if (name === tag.name) {
                modal.close();
                return;
            }
            if (!name || name.includes(',')) {
                message.textContent = 'Enter one name without commas';
                message.hidden = false;
                return;
            }
            save.disabled = true;
            input.disabled = true;
            message.hidden = true;
            try {
                const result = await renameTag(tag.id, name);
                modal.close();
                if (signal.aborted) return;
                showToast(
                    `${label} updated on ${result.affected} ${result.affected === 1 ? 'book' : 'books'}`,
                );
                reload(true);
            } catch (err) {
                if (signal.aborted || !modal.isOpen()) return;
                save.disabled = input.disabled = false;
                message.textContent = errorMessage(err, 'Rename failed');
                message.hidden = false;
                input.focus();
            }
        });
        modal.open(input);
    };

    const remove = async (tag: TagSummary) => {
        const confirmed = await confirmModal({
            title: `Delete ${kind}?`,
            body: `Remove “${tag.name}” and any nested ${kind}s from all books?`,
            confirmLabel: `Delete ${kind}`,
            danger: true,
        });
        if (!confirmed || signal.aborted) return;
        try {
            const result = await deleteTag(tag.id);
            if (signal.aborted) return;
            showToast(
                `${label} removed from ${result.affected} ${result.affected === 1 ? 'book' : 'books'}`,
            );
            reload(true);
        } catch (err) {
            if (signal.aborted) return;
            showToast(errorMessage(err, 'Delete failed'), { type: 'error' });
        }
    };

    const makeRow = (tag: TagSummary, depth: number): HTMLElement => {
        const row = document.createElement(view === 'grid' ? 'li' : 'tr');
        row.className = 'tag-row';
        row.style.setProperty('--tag-depth', String(depth));
        const href = `/?${new URLSearchParams({ q: queryTerm(kind, tag.name) })}`;
        const text = escapeHtml(query ? tag.name : tag.label);
        const branch = tag.has_children && !query;
        const action = view === 'grid' ? 'Open' : expanded.has(tag.id) ? 'Collapse' : 'Expand';
        const main = branch
            ? `<button type="button" id="tag-main-${tag.id}" class="tag-main" aria-label="${action} ${escapeHtml(tag.name)}" ${view === 'table' ? `aria-expanded="${expanded.has(tag.id)}"` : ''}><span>${text}</span>${icon('expand_more', 18, 'tag-chevron')}</button>`
            : `<a id="tag-main-${tag.id}" href="${escapeHtml(href)}" class="tag-main tag-name-link" data-nav><span>${text}</span></a>`;
        const count = `<a id="tag-count-${tag.id}" href="${escapeHtml(href)}" class="tag-count" data-nav aria-label="${tag.book_count} ${tag.book_count === 1 ? 'book' : 'books'}: ${escapeHtml(tag.name)}" title="View books${tag.has_children ? `, including nested ${kind}s` : ''}">${tag.book_count}</a>`;
        row.innerHTML =
            view === 'grid'
                ? main + count
                : `<td>${main}</td><td class="tag-count-cell">${count}</td>`;
        if (branch) {
            row.querySelector('.tag-main')!.addEventListener('click', () => {
                if (view === 'grid') {
                    gridPath = [...gridPath, { id: tag.id, label: tag.label }];
                    renderNavigation();
                    renderResults();
                    breadcrumbs.querySelector<HTMLButtonElement>('[aria-current]')?.focus();
                } else {
                    if (expanded.has(tag.id)) expanded.delete(tag.id);
                    else expanded.add(tag.id);
                    renderResults();
                }
            });
        }
        if (canEdit) {
            const actions = document.createElement(view === 'grid' ? 'div' : 'td');
            actions.className = 'tag-actions';
            actions.innerHTML = `<div class="tag-action-buttons">
                <button type="button" id="tag-edit-${tag.id}" class="action-btn action-btn-icon tag-edit" aria-label="Edit ${escapeHtml(tag.name)}" title="Edit ${kind}">${icon('edit', 18)}</button>
                <button type="button" id="tag-delete-${tag.id}" class="action-btn action-btn-icon tag-delete" aria-label="Delete ${escapeHtml(tag.name)}" title="Delete ${kind}">${icon('delete', 18)}</button>
            </div>`;
            row.append(actions);
            row.querySelector('.tag-edit')!.addEventListener('click', () => openRename(tag));
            row.querySelector('.tag-delete')!.addEventListener('click', () => {
                void remove(tag);
            });
        }
        return row;
    };

    const controls = createCollectionControls({
        view,
        tableLabel: 'List view',
        sort,
        sortLabel: `Sort ${kind}s`,
        sortOptions: [
            { value: 'name', label: 'Name' },
            { value: 'books', label: 'Books' },
        ],
        onViewChange: (value) => {
            view = value;
            localStorage.setItem('polka-tags-view', view);
            renderNavigation();
            renderResults();
        },
        onSortChange: (value) => {
            sort = value as TagSort;
            reload();
        },
    });
    root.querySelector('.collection-controls')!.replaceWith(controls.el);
    search.value = query;
    const handleSearchInput = debounce(() => {
        query = search.value.trim();
        reload();
    }, 200);
    search.addEventListener('input', handleSearchInput);
    renderNavigation();
    void fetchCurrentUser()
        .then((user) => {
            if (signal.aborted) return;
            canEdit = user.role !== 'reader';
            ready = true;
            renderResults();
        })
        .catch((err) => {
            if (signal.aborted) return;
            showToast(errorMessage(err, `Failed to load ${kind}s`), { type: 'error' });
        });

    return () => {
        cancelLoads();
        search.removeEventListener('input', handleSearchInput);
        handleSearchInput.cancel();
        controls.destroy();
        editor?.close();
    };
}
