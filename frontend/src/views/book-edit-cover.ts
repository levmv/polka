import {
    applyCoverSearchResult as apiApplyCoverSearchResult,
    applyCoverURL as apiApplyCoverURL,
    uploadCover as apiUploadCover,
    generateCoverPreview,
    searchCoverImages,
} from '../api';
import { parseAuthorList } from '../authors';
import { coverImgHtml } from '../cover';
import { escapeHtml } from '../dom';
import { errorMessage } from '../errors';
import { icon } from '../icons';
import { openModal } from '../modal';
import { showToast } from '../toast';
import type { Book, BookUpdate, CoverSearchResult } from '../types';

export type PendingCoverDraft =
    | {
          kind: 'url';
          url: string;
          providerName: string;
          source: 'fetched';
      }
    | {
          kind: 'file';
          file: File;
          previewUrl: string;
          source: 'upload' | 'generated';
          generatedStyleID?: string;
      }
    | {
          kind: 'search';
          token: string;
          previewUrl: string;
          sourceName: string;
          source: 'search';
      };

type GeneratedCoverStyle = {
    id: string;
};

type GeneratedCoverVariantPreview = GeneratedCoverStyle & {
    blob: Blob;
    previewUrl: string;
    seed: number;
};

const GENERATED_COVER_STYLES: GeneratedCoverStyle[] = [
    { id: 'classic' },
    { id: 'bands' },
    { id: 'label' },
    { id: 'quiet' },
];

export type CoverDraftController = {
    hasPending: () => boolean;
    isGenerating: () => boolean;
    pendingURL: () => string | null;
    setFetched: (url: string, providerName: string) => void;
    renderPending: () => void;
    resetToStored: () => void;
    syncControls: (disabled: boolean) => void;
    savePending: (workID: string) => Promise<Book>;
    destroy: () => void;
};

export function createCoverDraftController(opts: {
    uiID: string;
    book: () => Book;
    draft: () => BookUpdate;
    isBusy: () => boolean;
    isClosed: () => boolean;
    onChange: () => void;
}): CoverDraftController {
    let pendingCover: PendingCoverDraft | null = null;
    let coverChooser: ReturnType<typeof openModal> | null = null;
    let coverSearch: ReturnType<typeof openModal> | null = null;
    let generatingCover = false;
    let generatedCoverSeed = 0;
    let generatedVariants: GeneratedCoverVariantPreview[] = [];
    let showSavedReference = false;
    let coverSearchResults: CoverSearchResult[] = [];
    let coverSearchLoading = false;
    let coverSearchSearched = false;
    let coverSearchTitle = '';
    let coverSearchAuthor = '';
    let coverSearchAbort: AbortController | null = null;

    const coverChooserBtn = document.getElementById(
        `btn-edit-cover-chooser-${opts.uiID}`,
    ) as HTMLButtonElement | null;
    const coverSearchBtn = document.getElementById(
        `btn-edit-cover-search-${opts.uiID}`,
    ) as HTMLButtonElement | null;
    const coverClickTarget = document.getElementById(
        `edit-cover-container-${opts.uiID}`,
    ) as HTMLElement | null;
    const revertCoverBtn = document.getElementById(
        `btn-edit-revert-cover-${opts.uiID}`,
    ) as HTMLButtonElement | null;
    const uploadInput = document.getElementById(
        `edit-cover-upload-${opts.uiID}`,
    ) as HTMLInputElement | null;

    const currentBook = () => opts.book();
    const notifyChange = () => opts.onChange();

    const clearPendingCover = () => {
        releasePendingCoverDraft(pendingCover);
        pendingCover = null;
    };

    const releaseGeneratedVariantPreviews = (variants: GeneratedCoverVariantPreview[]) => {
        for (const variant of variants) {
            URL.revokeObjectURL(variant.previewUrl);
        }
    };

    const clearGeneratedVariants = () => {
        releaseGeneratedVariantPreviews(generatedVariants);
        generatedVariants = [];
    };

    const refreshCoverChooser = () => {
        const chooserRoot = coverChooser?.root;
        if (!chooserRoot || !coverChooser?.modal.isOpen()) return;
        const body = chooserRoot.querySelector<HTMLElement>('.modal-body');
        if (!body) return;
        body.innerHTML = renderCoverChooser(
            currentBook(),
            opts.uiID,
            pendingCover,
            generatedVariants,
            showSavedReference,
        );
        wireCoverChooserActions();
    };

    const syncCoverChooserSurface = () => {
        const chooserRoot = coverChooser?.root;
        if (!chooserRoot || !coverChooser?.modal.isOpen()) return;
        const book = currentBook();
        const label = chooserRoot.querySelector<HTMLElement>(
            '.cover-picker-primary .cover-picker-preview-label',
        );
        if (label) label.textContent = pendingCover ? 'Pending' : 'Current';

        const frame = chooserRoot.querySelector<HTMLElement>('.cover-picker-primary-frame');
        if (frame) {
            frame.classList.toggle('is-fetched', pendingCover?.source === 'fetched');
            frame.classList.toggle('is-dirty', !!pendingCover && pendingCover.source !== 'fetched');
            if (pendingCover) {
                frame.title = pendingCoverTitle(pendingCover);
            } else {
                frame.removeAttribute('title');
            }
            frame.innerHTML = renderCoverPickerPrimaryImage(book, opts.uiID, pendingCover);
        }

        const savedSelected = !pendingCover;
        const savedBtn = chooserRoot.querySelector<HTMLButtonElement>(
            `#cover-picker-saved-${opts.uiID}`,
        );
        savedBtn?.classList.toggle('is-selected', savedSelected);
        savedBtn?.setAttribute('aria-pressed', savedSelected ? 'true' : 'false');

        const selectedStyle =
            pendingCover?.kind === 'file' && pendingCover.source === 'generated'
                ? pendingCover.generatedStyleID
                : '';
        chooserRoot.querySelectorAll<HTMLButtonElement>('[data-cover-style]').forEach((btn) => {
            const selected = btn.dataset.coverStyle === selectedStyle;
            btn.classList.toggle('is-selected', selected);
            btn.setAttribute('aria-pressed', selected ? 'true' : 'false');
        });
    };

    const setPendingCover = (
        cover: PendingCoverDraft,
        optsSet: { refreshChooser?: boolean } = {},
    ) => {
        clearPendingCover();
        pendingCover = cover;
        showSavedReference = true;
        renderPendingEditCover(opts.uiID, pendingCover);
        if (optsSet.refreshChooser === false) {
            syncCoverChooserSurface();
        } else {
            refreshCoverChooser();
        }
    };

    const resetToStored = () => {
        clearPendingCover();
        showSavedReference = generatedVariants.length > 0;
        renderStoredEditCover(currentBook(), opts.uiID);
        syncCoverChooserSurface();
    };

    const revertCover = () => {
        resetToStored();
        notifyChange();
    };

    const setPendingGeneratedVariant = (
        variant: GeneratedCoverVariantPreview,
        optsSet: { refreshChooser?: boolean } = {},
    ) => {
        const book = currentBook();
        setPendingCover(
            {
                kind: 'file',
                file: new File(
                    [variant.blob],
                    `${book.id}-generated-cover-${variant.id}-${variant.seed}.jpg`,
                    {
                        type: variant.blob.type || 'image/jpeg',
                    },
                ),
                previewUrl: URL.createObjectURL(variant.blob),
                source: 'generated',
                generatedStyleID: variant.id,
            },
            optsSet,
        );
        notifyChange();
    };

    const generateCoverVariants = async (trigger: HTMLButtonElement | null = null) => {
        if (opts.isBusy() || generatingCover) return;
        const draft = opts.draft();
        const title = draft.title.trim();
        if (!title) {
            showToast('Title is required to generate a cover.', { type: 'error' });
            return;
        }

        generatingCover = true;
        if (trigger) {
            trigger.disabled = true;
            trigger.setAttribute('aria-busy', 'true');
        }
        notifyChange();
        const createdVariants: GeneratedCoverVariantPreview[] = [];
        try {
            const author = parseAuthorList(draft.authors || '')[0] || '';
            const seed = ++generatedCoverSeed;
            const book = currentBook();
            clearGeneratedVariants();
            refreshCoverChooser();
            const variants = await Promise.all(
                GENERATED_COVER_STYLES.map(async (style) => {
                    const blob = await generateCoverPreview(book.id, {
                        title,
                        author,
                        seed,
                        style: style.id,
                    });
                    const variant = {
                        ...style,
                        blob,
                        previewUrl: URL.createObjectURL(blob),
                        seed,
                    };
                    createdVariants.push(variant);
                    return variant;
                }),
            );
            if (opts.isClosed()) {
                releaseGeneratedVariantPreviews(variants);
                return;
            }
            generatedVariants = variants;
            createdVariants.length = 0;
            if (variants[0]) setPendingGeneratedVariant(variants[0], { refreshChooser: false });
        } catch (err) {
            releaseGeneratedVariantPreviews(createdVariants);
            showToast(`Cover generation failed: ${errorMessage(err)}`, { type: 'error' });
        } finally {
            generatingCover = false;
            if (trigger) {
                trigger.disabled = false;
                trigger.removeAttribute('aria-busy');
            }
            refreshCoverChooser();
            notifyChange();
        }
    };

    const openCoverChooser = () => {
        if (opts.isBusy() || generatingCover) return;
        if (coverChooser?.modal.isOpen()) {
            refreshCoverChooser();
            return;
        }
        coverChooser = openModal({
            title: 'Cover',
            body: renderCoverChooser(
                currentBook(),
                opts.uiID,
                pendingCover,
                generatedVariants,
                showSavedReference,
            ),
            modalClass: 'cover-picker-modal',
            bodyClass: 'cover-picker-body',
            onClose: () => {
                coverChooser = null;
                if (!pendingCover && generatedVariants.length === 0) showSavedReference = false;
            },
        });
        wireCoverChooserActions();

        const focusTarget =
            coverChooser.root.querySelector<HTMLButtonElement>(
                `#cover-picker-upload-${opts.uiID}`,
            ) ||
            coverChooser.root.querySelector<HTMLButtonElement>(
                `#btn-edit-generate-cover-${opts.uiID}`,
            ) ||
            undefined;
        coverChooser.modal.open(focusTarget);
    };

    function wireCoverChooserActions() {
        const chooserRoot = coverChooser?.root;
        if (!chooserRoot) return;
        const uploadBtn = chooserRoot.querySelector<HTMLButtonElement>(
            `#cover-picker-upload-${opts.uiID}`,
        );
        const generateBtn = chooserRoot.querySelector<HTMLButtonElement>(
            `#btn-edit-generate-cover-${opts.uiID}`,
        );
        const savedBtn = chooserRoot.querySelector<HTMLButtonElement>(
            `#cover-picker-saved-${opts.uiID}`,
        );

        uploadBtn?.addEventListener('click', () => {
            uploadInput?.click();
        });
        generateBtn?.addEventListener('click', () => {
            void generateCoverVariants(generateBtn);
        });
        savedBtn?.addEventListener('click', () => revertCover());
        chooserRoot.querySelectorAll<HTMLButtonElement>('[data-cover-style]').forEach((btn) => {
            btn.addEventListener('click', () => {
                const styleID = btn.dataset.coverStyle || '';
                const variant = generatedVariants.find((item) => item.id === styleID);
                if (!variant || generatingCover || opts.isBusy()) return;
                setPendingGeneratedVariant(variant, { refreshChooser: false });
            });
        });
    }

    const refreshCoverSearch = () => {
        const searchRoot = coverSearch?.root;
        if (!searchRoot || !coverSearch?.modal.isOpen()) return;
        const body = searchRoot.querySelector<HTMLElement>('.modal-body');
        if (!body) return;
        body.innerHTML = renderCoverSearchModalBody({
            uiID: opts.uiID,
            title: coverSearchTitle,
            author: coverSearchAuthor,
            results: coverSearchResults,
            loading: coverSearchLoading,
            searched: coverSearchSearched,
        });
        wireCoverSearchActions();
    };

    const openCoverSearch = () => {
        if (opts.isBusy() || generatingCover) return;
        if (coverSearch?.modal.isOpen()) {
            refreshCoverSearch();
            return;
        }
        const draft = opts.draft();
        coverSearchTitle = draft.title.trim();
        coverSearchAuthor = parseAuthorList(draft.authors || '')[0] || '';
        coverSearchResults = [];
        const autoSearch = coverSearchTitle !== '';
        coverSearchLoading = autoSearch;
        coverSearchSearched = autoSearch;
        coverSearch = openModal({
            title: 'Find cover online',
            body: renderCoverSearchModalBody({
                uiID: opts.uiID,
                title: coverSearchTitle,
                author: coverSearchAuthor,
                results: coverSearchResults,
                loading: coverSearchLoading,
                searched: coverSearchSearched,
            }),
            modalClass: 'cover-search-modal',
            bodyClass: 'cover-search-body',
            onClose: () => {
                coverSearchAbort?.abort();
                coverSearchAbort = null;
                coverSearch = null;
                coverSearchLoading = false;
            },
        });
        wireCoverSearchActions();
        const focusTarget =
            coverSearch.root.querySelector<HTMLInputElement>(`#cover-search-title-${opts.uiID}`) ||
            undefined;
        coverSearch.modal.open(focusTarget);
        if (autoSearch) {
            const abort = new AbortController();
            coverSearchAbort = abort;
            void loadCoverSearchResults(coverSearchTitle, coverSearchAuthor, abort);
        }
    };

    async function loadCoverSearchResults(title: string, author: string, abort: AbortController) {
        try {
            const results = await searchCoverImages(currentBook().id, title, author, abort.signal);
            if (abort.signal.aborted || opts.isClosed()) return;
            coverSearchResults = results;
        } catch (err) {
            if (!abort.signal.aborted) {
                coverSearchSearched = false;
                showToast(`Cover search failed: ${errorMessage(err)}`, { type: 'error' });
            }
        } finally {
            if (coverSearchAbort === abort) {
                coverSearchAbort = null;
            }
            if (!abort.signal.aborted) {
                coverSearchLoading = false;
                refreshCoverSearch();
            }
        }
    }

    function performCoverSearch() {
        if (coverSearchLoading || opts.isBusy()) return;
        const searchRoot = coverSearch?.root;
        if (!searchRoot) return;
        const titleInput = searchRoot.querySelector<HTMLInputElement>(
            `#cover-search-title-${opts.uiID}`,
        );
        const authorInput = searchRoot.querySelector<HTMLInputElement>(
            `#cover-search-author-${opts.uiID}`,
        );
        coverSearchTitle = titleInput?.value.trim() || '';
        coverSearchAuthor = authorInput?.value.trim() || '';
        if (!coverSearchTitle) {
            showToast('Title is required to search for a cover.', { type: 'error' });
            titleInput?.focus();
            return;
        }

        coverSearchAbort?.abort();
        const abort = new AbortController();
        coverSearchAbort = abort;
        coverSearchLoading = true;
        coverSearchSearched = true;
        coverSearchResults = [];
        refreshCoverSearch();
        void loadCoverSearchResults(coverSearchTitle, coverSearchAuthor, abort);
    }

    function wireCoverSearchActions() {
        const searchRoot = coverSearch?.root;
        if (!searchRoot) return;
        const form = searchRoot.querySelector<HTMLFormElement>(`#cover-search-form-${opts.uiID}`);
        form?.addEventListener('submit', (event) => {
            event.preventDefault();
            performCoverSearch();
        });
        searchRoot
            .querySelectorAll<HTMLButtonElement>('[data-cover-search-token]')
            .forEach((btn) => {
                btn.addEventListener('click', () => {
                    if (opts.isBusy() || coverSearchLoading) return;
                    const token = btn.dataset.coverSearchToken || '';
                    const result = coverSearchResults.find((item) => item.token === token);
                    if (!result) return;
                    setPendingCover({
                        kind: 'search',
                        token: result.token,
                        previewUrl: result.preview_url,
                        sourceName: result.source || 'web',
                        source: 'search',
                    });
                    notifyChange();
                    coverSearch?.modal.close();
                });
            });
    }

    uploadInput?.addEventListener('change', (e: Event) => {
        const input = e.target as HTMLInputElement;
        const file = input.files?.[0] || null;
        if (!file) return;
        if (file.size > 10 * 1024 * 1024) {
            input.value = '';
            showToast('Cover image is too large.', { type: 'error' });
            return;
        }
        setPendingCover({
            kind: 'file',
            file,
            previewUrl: URL.createObjectURL(file),
            source: 'upload',
        });
        input.value = '';
        notifyChange();
    });

    coverChooserBtn?.addEventListener('click', () => {
        openCoverChooser();
    });
    coverClickTarget?.addEventListener('click', () => {
        openCoverChooser();
    });
    coverSearchBtn?.addEventListener('click', () => {
        openCoverSearch();
    });
    revertCoverBtn?.addEventListener('click', () => {
        revertCover();
    });

    return {
        hasPending: () => !!pendingCover,
        isGenerating: () => generatingCover,
        pendingURL: () => (pendingCover ? pendingCoverImageURL(pendingCover) : null),
        setFetched: (url, providerName) => {
            setPendingCover({
                kind: 'url',
                url,
                providerName,
                source: 'fetched',
            });
        },
        renderPending: () => {
            if (pendingCover) renderPendingEditCover(opts.uiID, pendingCover);
        },
        resetToStored,
        syncControls: (disabled) => {
            const coverBusy = disabled || generatingCover;
            if (coverChooserBtn) coverChooserBtn.disabled = coverBusy;
            if (coverSearchBtn) coverSearchBtn.disabled = coverBusy;
            syncCoverDraftControls(opts.uiID, pendingCover, revertCoverBtn);
        },
        savePending: async (workID) => {
            const cover = pendingCover;
            if (!cover) throw new Error('no pending cover');
            try {
                const updated =
                    cover.kind === 'url'
                        ? await apiApplyCoverURL(workID, cover.url)
                        : cover.kind === 'search'
                          ? await apiApplyCoverSearchResult(workID, cover.token)
                          : await apiUploadCover(workID, cover.file);
                releasePendingCoverDraft(cover);
                pendingCover = null;
                showSavedReference = generatedVariants.length > 0;
                return updated;
            } catch (err) {
                showToast(`Cover save failed: ${errorMessage(err)}`, { type: 'error' });
                renderPendingEditCover(opts.uiID, cover);
                throw err;
            }
        },
        destroy: () => {
            coverChooser?.modal.close();
            coverChooser = null;
            coverSearch?.modal.close();
            coverSearch = null;
            coverSearchAbort?.abort();
            coverSearchAbort = null;
            clearPendingCover();
            clearGeneratedVariants();
            showSavedReference = false;
        },
    };
}

export function renderStoredEditCover(b: Book, uiID: string = b.id): void {
    const coverContainer = document.getElementById(`edit-cover-container-${uiID}`);
    if (!coverContainer) return;
    coverContainer.classList.remove('is-fetched');
    coverContainer.classList.remove('is-dirty');
    coverContainer.removeAttribute('title');
    coverContainer.innerHTML = coverImgHtml(
        b.id,
        b.cover_version,
        `edit-cover-image-${uiID}`,
        'detail-cover-image edit-cover-image-small',
    );
}

function renderCoverChooser(
    b: Book,
    uiID: string,
    pendingCover: PendingCoverDraft | null,
    generatedVariants: GeneratedCoverVariantPreview[],
    showSavedReference: boolean,
): string {
    const savedReferenceHtml = coverImgHtml(
        b.id,
        b.cover_version,
        `cover-picker-saved-image-${uiID}`,
        'cover-picker-reference-image',
    );
    const primaryCoverHtml = renderCoverPickerPrimaryImage(b, uiID, pendingCover);
    const railHtml = renderCoverPickerRail(
        uiID,
        pendingCover,
        generatedVariants,
        savedReferenceHtml,
        showSavedReference,
    );
    const hasRail = railHtml !== '';

    return `
        <div class="cover-picker">
            <div class="cover-picker-layout ${hasRail ? 'has-rail' : ''}">
                ${railHtml}
                <div class="cover-picker-primary">
                    <div class="cover-picker-preview-label">${pendingCover ? 'Pending' : 'Current'}</div>
                    <div class="cover-picker-primary-frame ${pendingCover ? (pendingCover.source === 'fetched' ? 'is-fetched' : 'is-dirty') : ''}"${pendingCover ? ` title="${escapeHtml(pendingCoverTitle(pendingCover))}"` : ''}>
                        ${primaryCoverHtml}
                    </div>
                    <div class="cover-picker-actions">
                        <button type="button" id="cover-picker-upload-${uiID}" class="cover-picker-action">${icon('upload', 18)} Upload</button>
                        <button type="button" id="btn-edit-generate-cover-${uiID}" class="cover-picker-action">${icon('add', 18)} Generate</button>
                    </div>
                </div>
            </div>
        </div>
    `;
}

function renderCoverPickerPrimaryImage(
    b: Book,
    uiID: string,
    pendingCover: PendingCoverDraft | null,
): string {
    if (pendingCover) {
        return `<img src="${escapeHtml(pendingCoverImageURL(pendingCover))}" class="cover-picker-primary-image edit-cover-image-pending" alt="">`;
    }
    return coverImgHtml(
        b.id,
        b.cover_version,
        `cover-picker-current-image-${uiID}`,
        'cover-picker-primary-image',
    );
}

function renderCoverPickerRail(
    uiID: string,
    pendingCover: PendingCoverDraft | null,
    generatedVariants: GeneratedCoverVariantPreview[],
    savedReferenceHtml: string,
    showSavedReference: boolean,
): string {
    const shouldShowSaved = showSavedReference || !!pendingCover || generatedVariants.length > 0;
    if (!shouldShowSaved) return '';
    const savedSelected = !pendingCover;
    const savedHtml = `
            <div class="cover-picker-reference">
                <button type="button" id="cover-picker-saved-${uiID}" class="cover-picker-reference-button ${savedSelected ? 'is-selected' : ''}" aria-label="Use saved cover" aria-pressed="${savedSelected ? 'true' : 'false'}">
                    ${savedReferenceHtml}
                </button>
            </div>
          `;
    return `
        <div class="cover-picker-rail">
            ${savedHtml}
            ${renderGeneratedVariantGrid(pendingCover, generatedVariants)}
        </div>
    `;
}

function renderGeneratedVariantGrid(
    pendingCover: PendingCoverDraft | null,
    generatedVariants: GeneratedCoverVariantPreview[],
): string {
    if (generatedVariants.length === 0) return '';
    const selectedStyle =
        pendingCover?.kind === 'file' && pendingCover.source === 'generated'
            ? pendingCover.generatedStyleID
            : '';
    return `
        <div class="cover-picker-generated">
            <div class="cover-picker-variant-grid">
                ${generatedVariants
                    .map((variant, index) => {
                        const selected = variant.id === selectedStyle;
                        return `
                            <button type="button" class="cover-picker-variant ${selected ? 'is-selected' : ''}" data-cover-style="${escapeHtml(variant.id)}" aria-label="Generated cover variant ${index + 1}" aria-pressed="${selected ? 'true' : 'false'}">
                                <img src="${escapeHtml(variant.previewUrl)}" alt="" class="cover-picker-variant-image">
                            </button>
                        `;
                    })
                    .join('')}
            </div>
        </div>
    `;
}

function renderCoverSearchModalBody(state: {
    uiID: string;
    title: string;
    author: string;
    results: CoverSearchResult[];
    loading: boolean;
    searched: boolean;
}): string {
    return `
        <div class="cover-search">
            <form id="cover-search-form-${state.uiID}" class="cover-search-form">
                <div class="cover-search-fields">
                    <label class="cover-search-field">
                        <span class="form-label">Title</span>
                        <input type="text" id="cover-search-title-${state.uiID}" class="form-input" value="${escapeHtml(state.title)}" autocomplete="off">
                    </label>
                    <label class="cover-search-field">
                        <span class="form-label">Author</span>
                        <input type="text" id="cover-search-author-${state.uiID}" class="form-input" value="${escapeHtml(state.author)}" autocomplete="off">
                    </label>
                </div>
                <button type="submit" class="cover-search-submit" ${state.loading ? 'disabled aria-busy="true"' : ''}>
                    ${state.loading ? '<span class="local-spinner" aria-hidden="true"></span>' : icon('search', 16)}
                    Search
                </button>
            </form>
            <div class="cover-search-status" role="status" aria-live="polite">
                ${state.loading ? 'Searching...' : state.searched && state.results.length === 0 ? 'No covers found.' : ''}
            </div>
            <div class="cover-search-grid">
                ${state.results.map(renderCoverSearchResult).join('')}
            </div>
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

function renderPendingEditCover(uiID: string, cover: PendingCoverDraft): void {
    const coverContainer = document.getElementById(`edit-cover-container-${uiID}`);
    if (!coverContainer) return;
    coverContainer.classList.toggle('is-fetched', cover.source === 'fetched');
    coverContainer.classList.toggle('is-dirty', cover.source !== 'fetched');
    coverContainer.title = pendingCoverTitle(cover);
    coverContainer.innerHTML = `<img src="${escapeHtml(pendingCoverImageURL(cover))}" id="edit-cover-image-${uiID}" class="detail-cover-image edit-cover-image-small edit-cover-image-pending" alt="">`;
}

function syncCoverDraftControls(
    uiID: string,
    cover: PendingCoverDraft | null,
    revertCoverBtn: HTMLButtonElement | null,
): void {
    const coverContainer = document.getElementById(`edit-cover-container-${uiID}`);
    coverContainer?.classList.toggle('is-fetched', cover?.source === 'fetched');
    coverContainer?.classList.toggle('is-dirty', !!cover && cover.source !== 'fetched');
    if (!revertCoverBtn) return;
    revertCoverBtn.hidden = !cover;
    revertCoverBtn.title = cover ? 'Revert to stored cover' : '';
}

function pendingCoverImageURL(cover: PendingCoverDraft): string {
    return cover.kind === 'url' ? cover.url : cover.previewUrl;
}

function pendingCoverTitle(cover: PendingCoverDraft): string {
    if (cover.kind === 'file' && cover.source === 'generated') {
        return 'Pending generated cover';
    }
    if (cover.kind === 'file') return `Pending upload: ${cover.file.name}`;
    if (cover.kind === 'search') return `Pending web cover from ${cover.sourceName}`;
    return `Pending cover from ${cover.providerName}`;
}

function releasePendingCoverDraft(cover: PendingCoverDraft | null): void {
    if (cover?.kind === 'file') {
        URL.revokeObjectURL(cover.previewUrl);
    }
}
