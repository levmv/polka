import {
    deleteTag,
    fetchCurrentUser,
    fetchTag,
    fetchTagPage,
    renameTag,
    type TagKind,
    type TagSort,
    type TagSummary,
} from '../api';
import { attachTagAutocomplete } from '../components/book-metadata-autocomplete';
import { type CollectionView, createCollectionControls } from '../components/collection-controls';
import { trackSearch } from '../components/search-history';
import { escapeHtml, isPlainClick } from '../dom';
import { errorMessage } from '../errors';
import { icon } from '../icons';
import { confirmModal, type ManagedModal, openModal } from '../modal';
import { registerOverlayReopen } from '../navigation';
import type { RouteController, RouteMountContext } from '../router';
import { queryTerm } from '../search-query';
import { tagParts } from '../tags';
import { showToast } from '../toast';

type TagLocation = Pick<TagSummary, 'name' | 'label'>;
type TagsState = {
    view: CollectionView;
    gridPath: TagLocation[];
    expanded: number[];
    counts: Record<string, number>;
};

type Branch = {
    parent: string;
    completion: Promise<void>;
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

export function initTags(root: HTMLElement, context: RouteMountContext): RouteController {
    const { signal, history } = context;
    const params = new URLSearchParams(window.location.search);
    const kind = selectedKind();
    const label = kind === 'genre' ? 'Genre' : 'Tag';
    const search = root.querySelector<HTMLInputElement>('.tags-search')!;
    const content = root.querySelector<HTMLElement>('.tags-browser')!;
    const breadcrumbs = root.querySelector<HTMLElement>('.tags-path')!;
    const state = history.state as TagsState | undefined;
    let view: CollectionView =
        state?.view ??
        (params.has('branch')
            ? 'grid'
            : localStorage.getItem('polka-tags-view') === 'table'
              ? 'table'
              : 'grid');
    let sort: TagSort = params.get('sort') === 'name' ? 'name' : 'books';
    let query = params.get('q') || '';
    const parts = params.get('branch') ? tagParts(params.get('branch')!) : [];
    let gridPath = parts.length
        ? parts.map((label, index) => ({ label, name: parts.slice(0, index + 1).join('.') }))
        : (state?.gridPath ?? []);
    let savedCounts = state?.counts ?? {};
    const expanded = new Set(state?.expanded);
    const branches = new Map<string, Branch>();
    let canEdit = false;
    let ready = false;
    let editor: ManagedModal | null = null;

    // Both views share pages. Collapsing a branch or switching views keeps its
    // pending request; changing the search/sort or leaving the page cancels it.
    function getBranch(parent: string): Branch {
        let branch = branches.get(parent);
        if (!branch) {
            branch = {
                parent,
                completion: Promise.resolve(),
                items: [],
                cursor: '',
                loaded: false,
                request: null,
                error: '',
            };
            branches.set(parent, branch);
            branch.completion = loadBranch(branch);
        }
        return branch;
    }

    async function loadBranch(branch: Branch): Promise<void> {
        if (branch.request || (branch.loaded && !branch.cursor)) return;
        const request = new AbortController();
        branch.request = request;
        branch.error = '';
        try {
            do {
                const page = await fetchTagPage(
                    kind,
                    {
                        query,
                        sort,
                        parentName: branch.parent,
                        cursor: branch.cursor,
                    },
                    request.signal,
                );
                if (signal.aborted || request.signal.aborted) return;
                branch.items.push(...page.items);
                branch.cursor = page.next_cursor || '';
                branch.loaded = true;
            } while (branch.cursor && branch.items.length < (savedCounts[branch.parent] ?? 0));
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
        savedCounts = {};
        if (resetNavigation) {
            gridPath = [];
            expanded.clear();
        }
        renderNavigation();
        renderResults();
    }

    function locationFor(nextQuery = query, path = gridPath): string {
        const params = new URLSearchParams({ kind });
        if (nextQuery) params.set('q', nextQuery);
        if (view === 'grid' && path.length) params.set('branch', path[path.length - 1].name);
        if (sort === 'name') params.set('sort', sort);
        return `/tags?${params}`;
    }

    function snapshot(): TagsState {
        const counts: Record<string, number> = {};
        for (const [key, branch] of branches) counts[key] = branch.items.length;
        return { view, gridPath, expanded: [...expanded], counts };
    }

    function followBranch(event: MouseEvent, path: TagLocation[]): void {
        if (!isPlainClick(event)) return;
        if (search.value.trim() !== query) return;
        event.preventDefault();
        searchHistory.finish();
        history.push(locationFor(query, path));
        gridPath = path;
        renderNavigation();
        renderResults();
        if (breadcrumbs.hidden) search.focus();
        else breadcrumbs.querySelector<HTMLElement>('[aria-current]')?.focus();
    }

    function renderNavigation(): void {
        breadcrumbs.replaceChildren();
        breadcrumbs.hidden = view !== 'grid' || !!query || !gridPath.length;
        if (breadcrumbs.hidden) return;
        const crumb = (text: string, length: number) => {
            const link = document.createElement('a');
            link.href = locationFor(query, gridPath.slice(0, length));
            link.textContent = text;
            if (length === gridPath.length) link.setAttribute('aria-current', 'page');
            const path = gridPath.slice(0, length);
            link.addEventListener('click', (event) => followBranch(event, path));
            return link;
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
                : branch.parent
                  ? `No ${kind}s here.`
                  : `No ${kind}s yet.${canEdit ? ` Add ${kind}s when editing a book.` : ''}`;
            status.innerHTML = `<p class="tags-empty">${message}</p>`;
        }
        if (branch.error || branch.cursor) {
            const more = document.createElement('button');
            more.type = 'button';
            more.id = `tag-more-${branch.parent}`;
            more.className = 'load-more-btn';
            more.textContent = branch.error ? 'Retry' : 'Show more';
            more.disabled = !!branch.request;
            more.addEventListener('click', () => {
                branch.completion = loadBranch(branch);
                renderResults();
            });
            status.append(more);
        }
        return status.childNodes.length ? status : null;
    }

    function renderGrid(): DocumentFragment {
        const fragment = document.createDocumentFragment();
        const parent = query ? '' : (gridPath[gridPath.length - 1]?.name ?? '');
        const branch = getBranch(parent);
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
        const appendBranch = (parent: string, depth: number) => {
            const branch = getBranch(parent);
            for (const tag of branch.items) {
                body.append(makeRow(tag, depth));
                if (!query && tag.has_children && expanded.has(tag.id))
                    appendBranch(tag.name, depth + 1);
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
        appendBranch('', 0);
        return table;
    }

    function renderResults(): void {
        if (signal.aborted) return;
        history.replace(locationFor());
        history.save();
        if (!ready) return;
        const active = document.activeElement;
        const focusID = active && content.contains(active) ? active.id : '';
        content.replaceChildren(view === 'grid' ? renderGrid() : renderTable());
        if (focusID)
            content
                .querySelector<HTMLElement>(`#${CSS.escape(focusID)}`)
                ?.focus({ preventScroll: true });
    }

    const openRename = (tag: Pick<TagSummary, 'id' | 'name'>) => {
        editor?.close();
        const { modal, root: dialog } = openModal({
            title: `Rename ${kind}`,
            history: { kind: 'tag-rename', target: tag.id },
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
        dialog.querySelector('.tag-cancel')!.addEventListener('click', () => void modal.dismiss());
        dialog.querySelector('form')!.addEventListener('submit', async (event) => {
            event.preventDefault();
            if (save.disabled) return;
            const name = input.value.trim();
            if (name === tag.name) {
                void modal.dismiss();
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
                modal.updateHistory({ kind: 'tag-rename', target: result.tag.id });
                await modal.dismiss();
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

    const releaseRename = registerOverlayReopen('tag-rename', async (entry, signal) => {
        if (!canEdit || !entry.target) return null;
        const tag = await fetchTag(Number(entry.target), signal);
        return tag && tag.kind === kind ? () => openRename(tag) : null;
    });

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
        const path = [...gridPath, { name: tag.name, label: tag.label }];
        const branchURL = locationFor(query, path);
        const main = branch
            ? view === 'grid'
                ? `<a id="tag-main-${tag.id}" href="${escapeHtml(branchURL)}" class="tag-main" aria-label="Open ${escapeHtml(tag.name)}"><span>${text}</span>${icon('expand_more', 18, 'tag-chevron')}</a>`
                : `<button type="button" id="tag-main-${tag.id}" class="tag-main" aria-label="${action} ${escapeHtml(tag.name)}" aria-expanded="${expanded.has(tag.id)}"><span>${text}</span>${icon('expand_more', 18, 'tag-chevron')}</button>`
            : `<a id="tag-main-${tag.id}" href="${escapeHtml(href)}" class="tag-main tag-name-link" data-nav><span>${text}</span></a>`;
        const count = `<a id="tag-count-${tag.id}" href="${escapeHtml(href)}" class="tag-count" data-nav aria-label="${tag.book_count} ${tag.book_count === 1 ? 'book' : 'books'}: ${escapeHtml(tag.name)}" title="View books${tag.has_children ? `, including nested ${kind}s` : ''}">${tag.book_count}</a>`;
        row.innerHTML =
            view === 'grid'
                ? main + count
                : `<td>${main}</td><td class="tag-count-cell">${count}</td>`;
        if (branch && view === 'table') {
            row.querySelector('.tag-main')!.addEventListener('click', () => {
                if (expanded.has(tag.id)) expanded.delete(tag.id);
                else expanded.add(tag.id);
                renderResults();
            });
        } else if (branch) {
            row.querySelector<HTMLAnchorElement>('.tag-main')!.addEventListener('click', (event) =>
                followBranch(event, path),
            );
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
            searchHistory.finish();
            view = value;
            localStorage.setItem('polka-tags-view', view);
            renderNavigation();
            renderResults();
        },
        onSortChange: (value) => {
            searchHistory.finish();
            sort = value as TagSort;
            reload();
        },
    });
    root.querySelector('.collection-controls')!.replaceWith(controls.el);
    search.value = query;
    const searchHistory = trackSearch(search, (newEntry) => {
        const next = search.value.trim();
        if (newEntry) history.push(locationFor(next));
        query = next;
        reload();
    });
    renderNavigation();
    const loaded = fetchCurrentUser()
        .then(async (user) => {
            if (signal.aborted) return;
            canEdit = user.role !== 'reader';
            ready = true;
            renderResults();
            while (!signal.aborted) {
                const pending = [...branches.values()].filter((branch) => branch.request);
                if (!pending.length) break;
                await Promise.all(pending.map((branch) => branch.completion));
            }
        })
        .catch((err) => {
            if (signal.aborted) return;
            showToast(errorMessage(err, `Failed to load ${kind}s`), { type: 'error' });
        });

    return {
        ready: loaded,
        snapshot,
        beforeLeave: () => searchHistory.finish(),
        destroy() {
            releaseRename();
            cancelLoads();
            searchHistory.destroy();
            controls.destroy();
            editor?.close();
        },
    };
}
