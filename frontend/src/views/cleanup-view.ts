import { dismissCleanupDuplicates, fetchCleanup, mergeCleanupDuplicates } from '../api';
import { bookURL } from '../book-list-context';
import { coverUrl } from '../cover';
import { escapeHtml } from '../dom';
import { errorMessage } from '../errors';
import { confirmModal } from '../modal';
import { showToast } from '../toast';
import type { Asset, BookSummary, Cleanup, DuplicateGroup } from '../types';

export async function initCleanup() {
    const container = document.getElementById('cleanup-content');
    if (!container) return;
    await loadCleanup(container);
}

async function loadCleanup(container: HTMLElement): Promise<void> {
    try {
        const cleanup = await fetchCleanup();
        renderCleanup(container, cleanup);
    } catch (err) {
        console.error('Failed to load cleanup items:', err);
        container.innerHTML = `<p class="error">Failed to load cleanup items</p>`;
    }
}

function cleanupSearchURL(query: string): string {
    const params = new URLSearchParams({ q: query });
    return `/?${params.toString()}`;
}

function renderCleanup(container: HTMLElement, cleanup: Cleanup): void {
    container.innerHTML = '';
    let totalCount = 0;

    const categories: {
        key: keyof Omit<Cleanup, 'possible_duplicates'>;
        title: string;
        query: string;
    }[] = [
        { key: 'missing_cover', title: 'Missing cover', query: 'no:cover' },
        { key: 'unknown_author', title: 'Unknown author', query: 'no:author' },
        { key: 'no_tags', title: 'No tags', query: 'no:tags' },
        { key: 'no_description', title: 'No description', query: 'no:description' },
    ];

    const tiles = document.createElement('div');
    tiles.className = 'cleanup-tiles';
    for (const cat of categories) {
        const data = cleanup[cat.key];
        totalCount += data.count;
        const tile = document.createElement('a');
        tile.className = 'cleanup-tile';
        tile.href = cleanupSearchURL(cat.query);
        tile.innerHTML = `<span class="cleanup-tile-label">${escapeHtml(cat.title)}</span><span class="cleanup-tile-count">${data.count.toLocaleString()}</span>`;
        tiles.appendChild(tile);
    }
    container.appendChild(tiles);

    if (cleanup.possible_duplicates && cleanup.possible_duplicates.count > 0) {
        totalCount += cleanup.possible_duplicates.count;

        const section = document.createElement('div');
        section.className = 'cleanup-section';

        const h2 = document.createElement('h2');
        h2.innerHTML = `Possible duplicates <span class="muted-count">(${cleanup.possible_duplicates.count})</span>`;
        section.appendChild(h2);

        const containerDiv = document.createElement('div');
        containerDiv.className = 'duplicate-groups-container';

        cleanup.possible_duplicates.groups.forEach((group, index) => {
            containerDiv.appendChild(createDuplicateGroup(container, group, index));
        });

        if (cleanup.possible_duplicates.groups.length === 0) {
            const note = document.createElement('p');
            note.className = 'cleanup-note';
            note.textContent =
                'More duplicate groups are available after the current page is refreshed.';
            containerDiv.appendChild(note);
        }

        section.appendChild(containerDiv);
        container.appendChild(section);
    }

    if (totalCount === 0) {
        container.innerHTML = `<p class="all-good">Everything looks good. No cleanup items at the moment.</p>`;
    }
}

function createDuplicateGroup(
    pageContainer: HTMLElement,
    group: DuplicateGroup,
    index: number,
): HTMLElement {
    const groupDiv = document.createElement('div');
    groupDiv.className = 'duplicate-group';

    const header = document.createElement('div');
    header.className = 'duplicate-group-header';

    const caption = document.createElement('div');
    caption.className = 'duplicate-group-caption';
    caption.textContent = `${duplicateReasonLabel(group.reason)} · ${group.key}`;
    header.appendChild(caption);

    const actions = document.createElement('div');
    actions.className = 'duplicate-group-actions';
    const dismiss = document.createElement('button');
    dismiss.type = 'button';
    dismiss.className = 'cleanup-action cleanup-action-secondary';
    dismiss.textContent = 'Dismiss';
    const merge = document.createElement('button');
    merge.type = 'button';
    merge.className = 'cleanup-action cleanup-action-primary';
    merge.textContent = 'Merge';
    actions.append(dismiss, merge);
    header.appendChild(actions);
    groupDiv.appendChild(header);

    const list = document.createElement('div');
    list.className = 'cleanup-list';
    const inputName = `cleanup-duplicate-${index}`;
    group.books.forEach((book, bookIndex) => {
        list.appendChild(
            createDuplicateBookRow(book, inputName, bookIndex === group.books.length - 1),
        );
    });
    groupDiv.appendChild(list);

    merge.addEventListener('click', async () => {
        const defaultSurvivor = group.books[group.books.length - 1];
        const selected = selectedDuplicateSurvivor(groupDiv) || defaultSurvivor?.id || '';
        if (!selected) return;
        const survivor = group.books.find((book) => book.id === selected) || defaultSurvivor;
        if (!survivor) return;
        const ok = await confirmModal({
            title: 'Merge duplicates?',
            body: `Keep "${survivor.title}" and move ${group.books.length - 1} duplicate ${group.books.length === 2 ? 'book' : 'books'} to Trash.`,
            confirmLabel: 'Merge',
        });
        if (!ok) return;
        setDuplicateGroupBusy(groupDiv, true);
        try {
            const result = await mergeCleanupDuplicates(
                selected,
                group.books.map((book) => book.id),
            );
            const message =
                result.relayout_warnings > 0
                    ? 'Merged duplicates; file relayout needs attention'
                    : 'Merged duplicates';
            showToast(message, result.relayout_warnings > 0 ? { type: 'error' } : undefined);
            await loadCleanup(pageContainer);
        } catch (err) {
            console.error('Failed to merge duplicates:', err);
            showToast(errorMessage(err, 'Failed to merge duplicates'), { type: 'error' });
            setDuplicateGroupBusy(groupDiv, false);
        }
    });

    dismiss.addEventListener('click', async () => {
        setDuplicateGroupBusy(groupDiv, true);
        try {
            await dismissCleanupDuplicates(group.books.map((book) => book.id));
            showToast('Dismissed duplicate group');
            await loadCleanup(pageContainer);
        } catch (err) {
            console.error('Failed to dismiss duplicate group:', err);
            showToast(errorMessage(err, 'Failed to dismiss duplicate group'), { type: 'error' });
            setDuplicateGroupBusy(groupDiv, false);
        }
    });

    return groupDiv;
}

function createDuplicateBookRow(
    book: BookSummary,
    inputName: string,
    checked: boolean,
): HTMLElement {
    const row = document.createElement('div');
    row.className = 'cleanup-row cleanup-duplicate-row';

    const choice = document.createElement('label');
    choice.className = 'cleanup-survivor-choice';
    const input = document.createElement('input');
    input.type = 'radio';
    input.name = inputName;
    input.value = book.id;
    input.checked = checked;
    input.setAttribute('aria-label', `Keep ${book.title}`);
    choice.appendChild(input);
    const choiceText = document.createElement('span');
    choiceText.textContent = 'Keep';
    choice.appendChild(choiceText);
    row.appendChild(choice);

    const img = document.createElement('img');
    img.className = 'cleanup-row-cover';
    img.src = coverUrl(book.id, book.cover_version, 'thumb');
    img.loading = 'lazy';
    img.alt = '';
    row.appendChild(img);

    const text = document.createElement('span');
    text.className = 'cleanup-row-text';
    const title = document.createElement('a');
    title.className = 'cleanup-row-title';
    title.href = bookURL(book.id);
    title.textContent = book.title;
    const author = document.createElement('span');
    author.className = 'cleanup-row-author';
    author.textContent = book.authors_display;
    text.append(title, author, createAssetChips(book.assets));
    row.appendChild(text);

    return row;
}

function createAssetChips(assets: Asset[]): HTMLElement {
    const wrap = document.createElement('span');
    wrap.className = 'cleanup-row-formats';
    if (!assets || assets.length === 0) {
        const chip = document.createElement('span');
        chip.className = 'cleanup-format-chip';
        chip.textContent = 'No file';
        wrap.appendChild(chip);
        return wrap;
    }
    for (const asset of assets) {
        const chip = document.createElement('span');
        chip.className = asset.is_primary
            ? 'cleanup-format-chip cleanup-format-chip-primary'
            : 'cleanup-format-chip';
        chip.textContent = assetLabel(asset);
        wrap.appendChild(chip);
    }
    return wrap;
}

function assetLabel(asset: Asset): string {
    const ext = asset.extension.replace(/^\./, '').toUpperCase();
    return ext || 'FILE';
}

function selectedDuplicateSurvivor(group: HTMLElement): string | null {
    const selected = group.querySelector<HTMLInputElement>('input[type="radio"]:checked');
    return selected?.value || null;
}

function setDuplicateGroupBusy(group: HTMLElement, busy: boolean): void {
    group.classList.toggle('is-busy', busy);
    for (const el of group.querySelectorAll<HTMLInputElement | HTMLButtonElement>(
        'input, button',
    )) {
        el.disabled = busy;
    }
}

function duplicateReasonLabel(reason: string): string {
    if (reason === 'title_author') return 'Same title and author';
    return 'Possible duplicate';
}
