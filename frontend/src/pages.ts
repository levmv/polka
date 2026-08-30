import { icon } from './icons';

export function renderLibraryPage(): string {
    return `
        <section id="continue-reading" class="continue-reading" aria-labelledby="continue-reading-title" hidden>
            <div class="section-heading-row">
                <h2 id="continue-reading-title">Continue reading</h2>
                <button id="continue-reading-dismiss" class="continue-reading-dismiss" type="button" aria-label="Hide Continue reading" title="Hide Continue reading">
                    ${icon('close', 16)}
                </button>
            </div>
            <div id="continue-reading-list" class="continue-reading-list"></div>
        </section>
        <div class="search-container">
            <div class="search-row">
                <div class="search-field">
                    <input type="text" id="search-input" placeholder="Search library..." autocomplete="off" aria-label="Search library" aria-keyshortcuts="/" title="Search library (/)">
                    <button id="save-search-btn" class="search-icon-btn" type="button" aria-label="Save search as shelf" title="Save search as shelf" hidden>
                        ${icon('bookmark', 20)}
                    </button>
                </div>
                <div class="library-view-controls">
                    <div class="view-toggle">
                        <button id="view-grid-btn" class="view-btn active" aria-label="Grid view">
                            ${icon('grid_view', 18)}
                        </button>
                        <button id="view-table-btn" class="view-btn" aria-label="Table view">
                            ${icon('table_rows', 18)}
                        </button>
                    </div>
                    <div id="sort-control" class="sort-control"></div>
                </div>
            </div>
        </div>
        <div id="library-grid" class="library-grid"></div>
        <div id="load-more-container" class="load-more-container" hidden>
            <button id="load-more-btn" class="load-more-btn">Load more</button>
        </div>
        <nav id="library-jump-rail" class="library-jump-rail" aria-label="Jump through books" hidden></nav>
    `;
}

export function renderBookPage(): string {
    return `
        <div class="back-link">
            <a href="/" class="page-close" title="Back to Library" aria-label="Back to Library" data-app-back>${icon('close', 24)}</a>
        </div>
        <div id="book-detail-container" class="detail-layout book-detail-loading" aria-busy="true">
            <div class="book-detail-loading-card local-loading-state" role="status" aria-live="polite">
                <span class="local-spinner" aria-hidden="true"></span>
                <div class="book-detail-loading-title">Loading book...</div>
            </div>
        </div>
    `;
}

export function renderCleanupPage(): string {
    return `
        <div class="page-container cleanup-container">
            <div class="page-heading-row">
                <h1>Cleanup</h1>
            </div>
            <div id="cleanup-content"></div>
        </div>
    `;
}

export function renderSeriesPage(): string {
    return `
        <div class="page-container series-container">
            <div class="page-heading-row">
                <h1>Series</h1>
            </div>
            <div id="series-grid" class="series-grid"></div>
            <div id="series-load-more" class="load-more-container" hidden>
                <button id="series-load-more-btn" type="button" class="load-more-btn" aria-label="Show more series">Show more</button>
            </div>
        </div>
    `;
}

export function renderAuthorsPage(): string {
    return `
        <div class="page-container authors-container">
            <div class="page-heading-row">
                <h1>Authors</h1>
            </div>
            <p class="authors-intro">Rename or merge authors, or override how a name sorts. Changes relocate the affected books' files automatically.</p>
            <div id="authors-status" class="authors-status" hidden></div>
            <div id="authors-content"></div>
        </div>
    `;
}

export function renderTrashPage(): string {
    return `
        <div class="page-container trash-container">
            <div class="page-heading-row">
                <h1>Trash</h1>
            </div>
            <p class="trash-intro">Removed books are kept here and stay out of the library and search. Restore one to bring it back. Permanent deletion is admin-only.</p>
            <div id="trash-content"></div>
        </div>
    `;
}
