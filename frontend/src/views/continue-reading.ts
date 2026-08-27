import { fetchContinueReading, saveUserSettings } from '../api';
import { coverUrl } from '../cover';
import type { ContinueReadingItem } from '../types';

const CONTINUE_READING_LIMIT = 8;

let items: ContinueReadingItem[] = [];
let loaded = false;
let loading = false;
let visible = false;

export function initContinueReadingRail(): (() => void) | undefined {
    const section = document.getElementById('continue-reading') as HTMLElement | null;
    const button = document.getElementById('continue-reading-dismiss') as HTMLButtonElement | null;
    if (!section || !button) return;

    const dismiss = async () => {
        button.disabled = true;
        try {
            const settings = await saveUserSettings({ hide_continue_reading: true });
            window.dispatchEvent(new CustomEvent('polka:user-settings', { detail: settings }));
            visible = false;
            loaded = true;
            items = [];
            section.hidden = true;
        } catch (e) {
            console.error('Failed to hide Continue reading:', e);
            button.disabled = false;
        }
    };
    button.addEventListener('click', dismiss);
    return () => button.removeEventListener('click', dismiss);
}

export function syncContinueReadingRail(
    shouldShow: boolean,
    isExpectedFetchCancel: (e: unknown) => boolean,
): void {
    visible = shouldShow;

    const section = document.getElementById('continue-reading') as HTMLElement | null;
    if (!section) return;

    if (!visible) {
        section.hidden = true;
        return;
    }

    if (loaded) {
        render();
        return;
    }
    if (loading) return;

    loading = true;
    fetchContinueReading(CONTINUE_READING_LIMIT)
        .then((nextItems) => {
            items = nextItems;
            loaded = true;
            render();
        })
        .catch((e) => {
            if (!isExpectedFetchCancel(e)) console.error('Failed to fetch continue reading:', e);
            section.hidden = true;
        })
        .finally(() => {
            loading = false;
        });
}

export function resetContinueReadingRail(): void {
    loaded = false;
    items = [];
}

function render(): void {
    const section = document.getElementById('continue-reading') as HTMLElement | null;
    const list = document.getElementById('continue-reading-list');
    if (!section || !list) return;

    if (!visible || items.length === 0) {
        section.hidden = true;
        list.replaceChildren();
        return;
    }

    const fragment = document.createDocumentFragment();
    for (const item of items) {
        fragment.appendChild(createCard(item));
    }
    list.replaceChildren(fragment);
    section.hidden = false;
}

function createCard(item: ContinueReadingItem): HTMLElement {
    const link = document.createElement('a');
    link.className = 'continue-reading-card';
    link.href = `/read/asset/${encodeURIComponent(item.asset_id)}`;
    link.setAttribute('aria-label', `Continue reading ${item.title}`);

    const coverSlot = document.createElement('span');
    coverSlot.className = 'continue-reading-cover';

    const img = document.createElement('img');
    img.src = coverUrl(item.id, item.cover_version, 'thumb');
    img.loading = 'lazy';
    img.draggable = false;
    img.alt = '';
    coverSlot.appendChild(img);

    const info = document.createElement('span');
    info.className = 'continue-reading-info';

    const title = document.createElement('span');
    title.className = 'continue-reading-book-title';
    title.textContent = item.title;

    const authors = document.createElement('span');
    authors.className = 'continue-reading-authors';
    authors.textContent = item.authors_display;

    const progress = clampProgress(item.progress);
    const progressPercent = Math.round(progress * 100);
    const meta = document.createElement('span');
    meta.className = 'continue-reading-meta';
    meta.textContent = progressLabel(progressPercent);

    const bar = document.createElement('span');
    bar.className = 'continue-reading-progress';
    const fill = document.createElement('span');
    fill.className = 'continue-reading-progress-fill';
    fill.style.width = progressPercent > 0 ? `${Math.max(3, progressPercent)}%` : '0%';
    bar.appendChild(fill);

    info.append(title, authors, meta, bar);
    link.append(coverSlot, info);
    return link;
}

function clampProgress(progress: number): number {
    if (!Number.isFinite(progress)) return 0;
    return Math.max(0, Math.min(1, progress));
}

function progressLabel(progressPercent: number): string {
    if (progressPercent >= 100) return 'Finished';
    if (progressPercent > 0) return `${progressPercent}% read`;
    return 'Started';
}
