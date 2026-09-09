import { searchCoverImages } from '../api';
import { escapeHtml } from '../dom';
import { errorMessage } from '../errors';
import { icon } from '../icons';
import { type ManagedModal, openModal } from '../modal';
import { showToast } from '../toast';
import type { CoverSearchResult } from '../types';

export function openCoverSearchDialog(opts: {
    bookID: number;
    title: string;
    author: string;
    isBusy: () => boolean;
    onChoose: (result: CoverSearchResult) => void;
    onClose: () => void;
}): ManagedModal {
    let results: CoverSearchResult[] = [];
    let request: AbortController | null = null;
    const { modal, root } = openModal({
        title: 'Find cover online',
        body: renderCoverSearchForm(opts.title, opts.author),
        modalClass: 'cover-search-modal',
        bodyClass: 'cover-search-body',
        onClose: () => {
            request?.abort();
            opts.onClose();
        },
    });
    const form = root.querySelector('.cover-search-form') as HTMLFormElement;
    const titleInput = form.elements.namedItem('title') as HTMLInputElement;
    const authorInput = form.elements.namedItem('author') as HTMLInputElement;
    const submit = form.querySelector('button[type="submit"]') as HTMLButtonElement;
    const status = root.querySelector('.cover-search-status') as HTMLElement;
    const grid = root.querySelector('.cover-search-grid') as HTMLElement;

    async function search() {
        if (request || opts.isBusy()) return;
        const title = titleInput.value.trim();
        const author = authorInput.value.trim();
        if (!title) {
            showToast('Title is required to search for a cover.', { type: 'error' });
            titleInput.focus();
            return;
        }

        const abort = new AbortController();
        request = abort;
        results = [];
        grid.replaceChildren();
        status.textContent = 'Searching...';
        submit.disabled = true;
        submit.setAttribute('aria-busy', 'true');
        submit.innerHTML = '<span class="local-spinner" aria-hidden="true"></span> Search';
        try {
            const found = await searchCoverImages(opts.bookID, title, author, abort.signal);
            if (abort.signal.aborted) return;
            results = found;
            grid.innerHTML = results.map(renderCoverSearchResult).join('');
            status.textContent = results.length === 0 ? 'No covers found.' : '';
        } catch (err) {
            if (!abort.signal.aborted) {
                status.textContent = '';
                showToast(`Cover search failed: ${errorMessage(err)}`, { type: 'error' });
            }
        } finally {
            if (!abort.signal.aborted) {
                request = null;
                submit.disabled = false;
                submit.removeAttribute('aria-busy');
                submit.innerHTML = `${icon('search', 16)} Search`;
            }
        }
    }

    form.addEventListener('submit', (event) => {
        event.preventDefault();
        void search();
    });
    grid.addEventListener('click', (event) => {
        if (request || opts.isBusy() || !(event.target instanceof Element)) return;
        const button = event.target.closest<HTMLButtonElement>('[data-cover-search-token]');
        const result = results.find((item) => item.token === button?.dataset.coverSearchToken);
        if (!result) return;
        opts.onChoose(result);
        modal.close();
    });

    modal.open(titleInput);
    if (titleInput.value.trim()) void search();
    return modal;
}

function renderCoverSearchForm(title: string, author: string): string {
    return `
        <div class="cover-search">
            <form class="cover-search-form">
                <div class="cover-search-fields">
                    <label class="cover-search-field">
                        <span class="form-label">Title</span>
                        <input type="text" name="title" class="form-input" value="${escapeHtml(title)}" autocomplete="off">
                    </label>
                    <label class="cover-search-field">
                        <span class="form-label">Author</span>
                        <input type="text" name="author" class="form-input" value="${escapeHtml(author)}" autocomplete="off">
                    </label>
                </div>
                <button type="submit" class="cover-search-submit">
                    ${icon('search', 16)} Search
                </button>
            </form>
            <div class="cover-search-status" role="status" aria-live="polite"></div>
            <div class="cover-search-grid"></div>
        </div>
    `;
}

function renderCoverSearchResult(result: CoverSearchResult, index: number): string {
    const source = result.source || 'Web';
    const resolution =
        result.width > 0 && result.height > 0 ? `${result.width} x ${result.height}` : '';
    const label = `Use cover ${index + 1} from ${source}`;
    return `
        <button type="button" class="cover-search-result" data-cover-search-token="${escapeHtml(result.token)}" aria-label="${escapeHtml(label)}">
            <img src="${escapeHtml(result.preview_url)}" alt="" class="cover-search-image">
            <span class="cover-search-result-meta">
                <span class="cover-search-source">${escapeHtml(source)}</span>
                ${resolution ? `<span class="cover-search-size">${escapeHtml(resolution)}</span>` : ''}
            </span>
        </button>
    `;
}
