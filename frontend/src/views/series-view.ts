import { fetchSeriesPage } from '../api';
import { coverUrl } from '../cover';
import { escapeHtml } from '../dom';
import type { RouteCleanup } from '../router';
import { seriesLibraryURL } from '../search-query';
import type { SeriesSummary } from '../types';

export async function initSeries(root: HTMLElement): Promise<RouteCleanup | undefined> {
    const grid = root.querySelector<HTMLElement>('#series-grid');
    const loadMoreWrap = root.querySelector<HTMLElement>('#series-load-more');
    const loadMoreButton = root.querySelector('#series-load-more-btn');
    if (!grid || !loadMoreWrap || !(loadMoreButton instanceof HTMLButtonElement)) return;

    let nextCursor = '';
    let destroyed = false;

    const appendPage = (series: SeriesSummary[]) => {
        const fragment = document.createDocumentFragment();
        for (const item of series) {
            fragment.appendChild(createSeriesCard(item));
        }
        grid.appendChild(fragment);
    };

    const loadMore = async () => {
        if (!nextCursor || loadMoreButton.disabled) return;
        loadMoreButton.disabled = true;
        loadMoreButton.textContent = 'Loading...';
        try {
            const page = await fetchSeriesPage(nextCursor);
            if (destroyed) return;
            appendPage(page.items);
            nextCursor = page.next_cursor || '';
            loadMoreWrap.hidden = !nextCursor;
        } catch (error) {
            if (destroyed) return;
            console.error('Failed to load more series:', error);
        } finally {
            if (!destroyed) {
                loadMoreButton.disabled = false;
                loadMoreButton.textContent = 'Show more';
            }
        }
    };
    loadMoreButton.addEventListener('click', () => void loadMore());

    try {
        const page = await fetchSeriesPage();
        if (destroyed) return;
        if (page.items.length === 0) {
            grid.innerHTML = `<p class="series-grid-message">No series yet.</p>`;
        } else {
            appendPage(page.items);
        }
        nextCursor = page.next_cursor || '';
        loadMoreWrap.hidden = !nextCursor;
    } catch (e) {
        if (destroyed) return;
        console.error('Failed to load series:', e);
        grid.innerHTML = `<p class="series-grid-message error">Failed to load series</p>`;
    }

    return () => {
        destroyed = true;
    };
}

function createSeriesCard(series: SeriesSummary): HTMLElement {
    const card = document.createElement('a');
    card.className = 'series-card';
    card.href = seriesLibraryURL(series.name);

    const badge = countBadge(series);
    const progressHtml =
        series.finished_count > 0
            ? `<span class="series-card-progress"><span class="series-card-progress-fill" style="width: ${Math.round((series.finished_count / series.book_count) * 100)}%"></span></span>`
            : '';

    const authorHtml = series.author
        ? `<span class="series-card-author">${escapeHtml(series.author)}</span>`
        : '';

    card.innerHTML = `
        <span class="series-card-cover-slot">
            <img src="${coverUrl(series.cover_work_id, series.cover_version, 'thumb')}" loading="lazy" draggable="false" class="series-card-cover-image" alt="">
            ${progressHtml}
            <span class="series-card-count" title="${escapeHtml(badge.title)}" aria-hidden="true">${escapeHtml(badge.text)}</span>
        </span>
        <span class="series-card-name">${escapeHtml(series.name)}</span>
        ${authorHtml}
        <span class="sr-only">${escapeHtml(badge.title)}</span>
    `;
    return card;
}

// The badge stays glanceable: just the size of the series until some of it has
// been read, then read-of-total. The full phrasing is the tooltip and the
// screen-reader text.
function countBadge(series: SeriesSummary): { text: string; title: string } {
    const books = series.book_count === 1 ? '1 book' : `${series.book_count} books`;
    if (series.finished_count === 0) {
        return { text: String(series.book_count), title: books };
    }
    return {
        text: `${series.finished_count}/${series.book_count}`,
        title: `${books}, ${series.finished_count} read`,
    };
}
