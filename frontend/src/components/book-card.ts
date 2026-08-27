import { type BookListContext, bookURL } from '../book-list-context';
import { coverUrl } from '../cover';
import { escapeHtml } from '../dom';
import type { BookSummary } from '../types';

export function createBookCard(b: BookSummary, context?: BookListContext | null): HTMLElement {
    const el = document.createElement('div');
    el.className = 'book-card';
    el.dataset.id = b.id;

    const href = escapeHtml(bookURL(b.id, context));

    let seriesHtml = '';
    if (b.series) {
        seriesHtml = `<p class="book-card-series">${escapeHtml(b.series)} ${b.series_index ? `#${b.series_index}` : ''}</p>`;
    }

    // Selection checkbox overlay. Inert (hidden) unless the library grid is in a
    // curate context — the library-selection controller owns its behaviour.
    const selectHtml = `<button type="button" class="card-select" role="checkbox" aria-checked="false" aria-label="Select book" tabindex="-1"></button>`;

    el.innerHTML = `
        ${selectHtml}
        <a href="${href}" class="book-cover-slot book-title-link" aria-label="${escapeHtml(b.title)}">
            <img src="${coverUrl(b.id, b.cover_version, 'thumb')}" loading="lazy" draggable="false" class="book-cover-image" alt="">
            <div class="book-info">
                <h3 class="book-title">${escapeHtml(b.title)}</h3>
                <p class="book-authors">${escapeHtml(b.authors_display)}</p>
                ${seriesHtml}
            </div>
        </a>
    `;
    return el;
}
