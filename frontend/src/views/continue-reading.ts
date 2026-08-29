import { fetchContinueReading, saveUserSettings } from '../api';
import { coverUrl } from '../cover';
import type { ContinueReadingItem } from '../types';

const CONTINUE_READING_LIMIT = 8;

// The rail belongs to one library instance, and so does its data. A retained
// library brings its own rendered rail back with it, so a cache shared between
// mounts would buy nothing on the return path and could only serve a fresh
// mount stale items. Every lookup is scoped to the library's route root, so a
// library that is no longer on screen cannot write into the visible page.
export interface ContinueReadingRail {
    // Show or hide the rail, fetching its items the first time they are needed.
    sync(shouldShow: boolean): void;
    // The items are derived from reading state and are not addressed by book
    // id, so any catalog change simply drops them.
    invalidate(): void;
    destroy(): void;
}

export function createContinueReadingRail(
    root: HTMLElement,
    isExpectedFetchCancel: (e: unknown) => boolean,
): ContinueReadingRail {
    let items: ContinueReadingItem[] = [];
    let loaded = false;
    let loading = false;
    let visible = false;
    let destroyed = false;

    const section = root.querySelector<HTMLElement>('#continue-reading');
    const button = root.querySelector<HTMLButtonElement>('#continue-reading-dismiss');

    const dismiss = async () => {
        if (!button || !section) return;
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
    button?.addEventListener('click', dismiss);

    const render = () => {
        const list = root.querySelector<HTMLElement>('#continue-reading-list');
        if (!section || !list) return;
        if (!visible || items.length === 0) {
            section.hidden = true;
            list.replaceChildren();
            return;
        }
        const fragment = document.createDocumentFragment();
        for (const item of items) fragment.appendChild(createCard(item));
        list.replaceChildren(fragment);
        section.hidden = false;
    };

    return {
        sync(shouldShow: boolean): void {
            visible = shouldShow;
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
                    if (destroyed) return;
                    items = nextItems;
                    loaded = true;
                    render();
                })
                .catch((e) => {
                    if (destroyed) return;
                    if (!isExpectedFetchCancel(e)) {
                        console.error('Failed to fetch continue reading:', e);
                    }
                    section.hidden = true;
                })
                .finally(() => {
                    loading = false;
                });
        },
        invalidate(): void {
            loaded = false;
            items = [];
        },
        destroy(): void {
            destroyed = true;
            button?.removeEventListener('click', dismiss);
        },
    };
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
