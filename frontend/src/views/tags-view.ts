import {
    deleteTag,
    fetchCurrentUser,
    fetchTagPage,
    renameTag,
    type TagKind,
    type TagSummary,
} from '../api';
import { attachTagAutocomplete } from '../components/book-metadata-autocomplete';
import { escapeHtml } from '../dom';
import { errorMessage } from '../errors';
import { icon } from '../icons';
import { confirmModal, type ManagedModal, openModal } from '../modal';
import { queryTerm } from '../search-query';
import { showToast } from '../toast';

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
        <nav class="page-heading-row dictionary-heading" aria-label="Dictionary">
            ${headings}
        </nav>
        <input type="search" class="form-input tags-search" aria-label="Search ${kind}s" placeholder="Find a ${kind}…">
        <p class="tags-error error" role="alert" hidden></p>
        <p class="tags-empty" hidden></p>
        <table class="tags-table" hidden>
            <thead><tr><th scope="col">Name</th><th scope="col">Books</th><th scope="col"><span class="sr-only">Actions</span></th></tr></thead>
            <tbody></tbody>
        </table>
        <div class="load-more-container" hidden><button type="button" class="load-more-btn" aria-label="Show more ${kind}s">Show more</button></div>
    </div>`;
}

export function initTags(root: HTMLElement, signal: AbortSignal): () => void {
    const kind = selectedKind();
    const label = kind === 'genre' ? 'Genre' : 'Tag';
    const search = root.querySelector<HTMLInputElement>('.tags-search')!;
    const table = root.querySelector<HTMLTableElement>('.tags-table')!;
    const body = table.querySelector('tbody')!;
    const empty = root.querySelector<HTMLElement>('.tags-empty')!;
    const error = root.querySelector<HTMLElement>('.tags-error')!;
    const more = root.querySelector<HTMLElement>('.load-more-container')!;
    const moreButton = more.querySelector<HTMLButtonElement>('button')!;
    let canEdit = false;
    let cursor = '';
    let request: AbortController | null = null;
    let searchTimer: ReturnType<typeof setTimeout> | undefined;
    let editor: ManagedModal | null = null;

    const load = async (append = false) => {
        request?.abort();
        const current = new AbortController();
        request = current;
        moreButton.disabled = true;
        error.hidden = true;
        try {
            const page = await fetchTagPage(
                kind,
                search.value.trim(),
                append ? cursor : '',
                current.signal,
            );
            if (signal.aborted || current.signal.aborted) return;
            if (!append) body.replaceChildren();
            for (const tag of page.items) body.appendChild(makeRow(tag));
            cursor = page.next_cursor || '';
            more.hidden = !cursor;
            table.hidden = body.children.length === 0;
            empty.hidden = !table.hidden;
            empty.textContent = search.value.trim()
                ? `No matching ${kind}s.`
                : `No ${kind}s yet.${canEdit ? ` Add ${kind}s when editing a book.` : ''}`;
        } catch (err) {
            if (signal.aborted || current.signal.aborted) return;
            error.textContent = errorMessage(err, `Failed to load ${kind}s`);
            error.hidden = false;
        } finally {
            if (request === current) moreButton.disabled = false;
        }
    };

    const openRename = (tag: TagSummary) => {
        editor?.close();
        const { modal, root: dialog } = openModal({
            title: `Rename ${kind}`,
            modalClass: 'tag-rename-modal',
            body: `<form id="tag-rename-form">
                <label for="tag-name">Name</label>
                <input id="tag-name" class="form-input" value="${escapeHtml(tag.name)}" required>
                <p class="tag-rename-hint">Applies to every book using this ${kind}. Using an existing name merges the entries.</p>
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
                void load();
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
            body: `Remove “${tag.name}” from every book using it?`,
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
            void load();
        } catch (err) {
            if (signal.aborted) return;
            showToast(errorMessage(err, 'Delete failed'), { type: 'error' });
        }
    };

    const makeRow = (tag: TagSummary): HTMLTableRowElement => {
        const row = document.createElement('tr');
        const href = `/?${new URLSearchParams({ q: queryTerm(kind, tag.name) })}`;
        row.innerHTML = `<td><a href="${escapeHtml(href)}" class="tag-name-link" data-nav>${escapeHtml(tag.name)}</a></td><td class="tag-count">${tag.book_count}</td><td class="tag-actions"></td>`;
        if (canEdit) {
            row.lastElementChild!.innerHTML = `<div class="tag-action-buttons">
                <button type="button" class="action-btn tag-edit" aria-label="Edit ${escapeHtml(tag.name)}" title="Edit ${kind}">${icon('edit', 18)}<span class="tag-edit-label">Edit</span></button>
                <button type="button" class="action-btn action-btn-icon tag-delete" aria-label="Delete ${escapeHtml(tag.name)}" title="Delete ${kind}">${icon('delete', 18)}</button>
            </div>`;
            row.querySelector('.tag-edit')!.addEventListener('click', () => openRename(tag));
            row.querySelector('.tag-delete')!.addEventListener('click', () => {
                void remove(tag);
            });
        }
        return row;
    };

    search.addEventListener('input', () => {
        request?.abort();
        clearTimeout(searchTimer);
        more.hidden = true;
        searchTimer = setTimeout(() => {
            void load();
        }, 200);
    });
    moreButton.addEventListener('click', () => {
        void load(true);
    });
    void fetchCurrentUser()
        .then((user) => {
            if (signal.aborted) return;
            canEdit = user.role !== 'reader';
            void load();
        })
        .catch((err) => {
            if (signal.aborted) return;
            error.textContent = errorMessage(err, `Failed to load ${kind}s`);
            error.hidden = false;
        });

    return () => {
        request?.abort();
        clearTimeout(searchTimer);
        editor?.close();
    };
}
