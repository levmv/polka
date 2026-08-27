import { emptyTrash, fetchCurrentUser, fetchTrash, purgeBook, restoreBook } from '../api';
import { coverUrl } from '../cover';
import { escapeHtml } from '../dom';
import { errorMessage } from '../errors';
import { confirmModal } from '../modal';
import { showToast } from '../toast';
import type { TrashedBook } from '../types';

export async function initTrash(): Promise<void> {
    const container = document.getElementById('trash-content');
    if (!container) return;

    // Role gates the irreversible "Delete permanently" action; the server
    // enforces it too, so a non-admin simply never sees the button.
    let isAdmin = false;
    try {
        const me = await fetchCurrentUser();
        isAdmin = me.role === 'admin';
    } catch {
        /* fall back to non-admin view */
    }

    try {
        const books = await fetchTrash();
        renderTrash(container, books, isAdmin);
    } catch (_e) {
        container.innerHTML = `<p class="error">Failed to load trash</p>`;
    }
}

function renderTrash(container: HTMLElement, books: TrashedBook[], isAdmin: boolean): void {
    if (books.length === 0) {
        container.innerHTML = `<p class="trash-empty">Trash is empty.</p>`;
        return;
    }

    const frag = document.createDocumentFragment();
    // The bulk "Empty trash" action is admin-only, mirroring the per-card purge.
    if (isAdmin) {
        frag.appendChild(createTrashToolbar());
    }
    const grid = document.createElement('div');
    grid.className = 'trash-grid';
    for (const b of books) {
        grid.appendChild(createTrashCard(b, isAdmin));
    }
    frag.appendChild(grid);
    container.replaceChildren(frag);
}

function createTrashToolbar(): HTMLElement {
    const bar = document.createElement('div');
    bar.className = 'trash-toolbar';
    bar.innerHTML = `<button class="btn-purge btn-empty-trash" type="button">Empty trash</button>`;

    bar.querySelector<HTMLButtonElement>('.btn-empty-trash')?.addEventListener(
        'click',
        async () => {
            // Count what's actually on screen so the confirm copy is accurate even if
            // cards were restored/purged individually since the view first rendered.
            const n = document.querySelectorAll('.trash-card').length;
            if (n === 0) return;
            const ok = await confirmModal({
                title: 'Empty trash?',
                body: `Permanently delete all ${n} book${n === 1 ? '' : 's'} in the trash and their files. This cannot be undone.`,
                confirmLabel: 'Empty trash',
                danger: true,
            });
            if (!ok) return;
            try {
                await emptyTrash();
                const container = document.getElementById('trash-content');
                if (container) container.innerHTML = `<p class="trash-empty">Trash is empty.</p>`;
            } catch (e) {
                console.error('Failed to empty trash:', e);
                showToast(errorMessage(e, 'Failed to empty trash'), { type: 'error' });
            }
        },
    );
    return bar;
}

function createTrashCard(b: TrashedBook, isAdmin: boolean): HTMLElement {
    const el = document.createElement('div');
    el.className = 'trash-card';

    const meta = trashedMeta(b);
    el.innerHTML = `
        <div class="trash-card-cover">
            <img src="${coverUrl(b.id, b.cover_version, 'thumb')}" loading="lazy" draggable="false" class="book-cover-image" alt="">
        </div>
        <div class="book-info">
            <h3 class="book-title">${escapeHtml(b.title)}</h3>
            <p class="book-authors">${escapeHtml(b.authors_display)}</p>
            <p class="trash-card-meta">${escapeHtml(meta)}</p>
            <div class="trash-card-actions">
                <button class="btn-restore" type="button">Restore</button>
                ${isAdmin ? `<button class="btn-purge" type="button">Delete permanently</button>` : ''}
            </div>
        </div>
    `;

    el.querySelector<HTMLButtonElement>('.btn-restore')?.addEventListener(
        'click',
        async (event) => {
            const btn = event.currentTarget as HTMLButtonElement;
            btn.disabled = true;
            try {
                await restoreBook(b.id);
                el.remove();
                reflectEmptyState();
            } catch (e) {
                console.error('Failed to restore book:', e);
                showToast(errorMessage(e, 'Failed to restore book'), { type: 'error' });
                btn.disabled = false;
            }
        },
    );

    el.querySelector<HTMLButtonElement>('.btn-purge')?.addEventListener('click', async () => {
        const ok = await confirmModal({
            title: 'Delete permanently?',
            body: `“${b.title}” and its files will be permanently deleted. This cannot be undone.`,
            confirmLabel: 'Delete permanently',
            danger: true,
        });
        if (!ok) return;
        try {
            await purgeBook(b.id);
            el.remove();
            reflectEmptyState();
        } catch (e) {
            console.error('Failed to permanently delete book:', e);
            showToast(errorMessage(e, 'Failed to permanently delete book'), { type: 'error' });
        }
    });

    return el;
}

function trashedMeta(b: TrashedBook): string {
    const when = formatTimestampHuman(b.deleted_at);
    const parts = [];
    if (when) parts.push(`Trashed ${when}`);
    if (b.deleted_by) parts.push(`by ${b.deleted_by}`);
    return parts.join(' ');
}

// When the last card is removed, swap in the empty-state message.
function reflectEmptyState(): void {
    const container = document.getElementById('trash-content');
    const grid = container?.querySelector('.trash-grid');
    if (container && grid && grid.children.length === 0) {
        container.innerHTML = `<p class="trash-empty">Trash is empty.</p>`;
    }
}

function formatTimestampHuman(timestamp: number): string {
    const d = new Date(timestamp * 1000);
    if (Number.isNaN(d.getTime())) return '';
    return d.toLocaleDateString(undefined, { day: 'numeric', month: 'short', year: 'numeric' });
}
