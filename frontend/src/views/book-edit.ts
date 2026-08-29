import { fetchAuthorInfo, fetchBook, renameAuthor, setAuthorSortName, updateBook } from '../api';
import { authorSort, formatAuthorsForEdit, parseAuthorList } from '../authors';
import { type BookListContext, readBookListContextFromLocation } from '../book-list-context';
import { notifyCatalogChanged } from '../catalog-events';
import {
    attachAuthorAutocomplete,
    attachSeriesAutocomplete,
    attachTagAutocomplete,
} from '../components/book-metadata-autocomplete';
import { attachFlexibleDatePicker, datePickerButton } from '../components/flexible-date-picker';
import { createRichEditor } from '../components/rich-editor';
import { attachTextListAutocomplete } from '../components/text-list-autocomplete';
import { coverImgHtml } from '../cover';
import { escapeHtml } from '../dom';
import { errorMessage } from '../errors';
import { isBookPath, type OverlayEntry } from '../history-state';
import { icon } from '../icons';
import { formatIdentifiers, parseIdentifiers, validISBN } from '../identifiers';
import { beginGlobalLoading } from '../loading-indicator';
import { confirmModal, openModal, registerOverlayReopen, updateOverlayEntry } from '../modal';
import { titleSort } from '../titles';
import { showToast } from '../toast';
import type {
    Author,
    Book,
    BookSequenceItem,
    BookSequenceWindow,
    BookSummary,
    BookUpdate,
} from '../types';
import { activeBookDetailHost, type BookDetailHost } from './book-detail-host';
import {
    type CoverDraftController,
    createCoverDraftController,
    renderStoredEditCover,
} from './book-edit-cover';
import {
    dirtyEditFields,
    type EditFieldName,
    editFieldValuePreview,
    type FetchedFieldSource,
    normalizeFormBeforeSave,
    readEditForm,
    sameEditFieldValue,
    setEditFieldValue,
    stateKey,
    submitPayload,
    syncFieldDecorations,
    validateTitle,
} from './book-edit-field-state';
import {
    type BookEditSequenceController,
    createBookEditSequenceController,
} from './book-edit-sequence';
import { type MetadataDraftApply, openMetadataCandidatesModal } from './book-metadata-fetch';

// Render the date-field hint. A date is "recognized" when it is already in the
// normalized YYYY[-MM[-DD]] form, OR when the server produced a human form that
// differs from the raw value (e.g. a full ISO string the server parsed). Only a
// genuinely unparseable value (date_human echoes the raw input) gets the warning.
// Recognized dates stay silent (the field speaks for itself); only a problem
// surfaces a quiet flag — matching the identifier-validation tone.
function renderDateHint(el: HTMLElement | null, b: Book) {
    if (!el) return;
    if (!b.date) {
        el.style.display = 'none';
        return;
    }
    const isNormalized = /^\d{4}(?:-\d{2}(?:-\d{2})?)?$/.test(b.date);
    const recognized = isNormalized || (!!b.date_human && b.date_human !== b.date);
    if (recognized) {
        el.style.display = 'none';
        return;
    }
    el.innerHTML = `<span class="field-flag field-flag-bad" title="Date not recognized">${icon('close', 14)} Unrecognized</span>`;
    el.style.display = 'flex';
}

function syncEditFormFromBook(form: HTMLFormElement, b: Book, uiID: string = b.id) {
    const f = form as any;
    f.title.value = b.title || '';
    f.sort_title.value = b.sort_title || b.title || '';
    f.authors.value = formatAuthorsForEdit(b.authors_list);
    f.series.value = b.series || '';
    f.series_index.value = b.series_index == null ? '' : String(b.series_index);
    f.tags.value = b.tags || '';
    f.language.value = b.language || '';
    f.publisher.value = b.publisher || '';
    f.date.value = b.date_human || b.date || '';
    f.identifiers.value = b.identifiers || '';
    f.description.value = b.description_source || '';

    const editor = form.querySelector<HTMLElement>('.rich-editor-content');
    if (editor) {
        if (b.description_html) {
            editor.innerHTML = b.description_html;
        } else {
            editor.textContent = b.description_source || '';
        }
    }

    renderStoredEditCover(b, uiID);
}

const BOOK_EDIT_OVERLAY = 'book-edit';

// Forward returns to an editor entry. Its URL is the page underneath, so it
// already contains the origin and the list context needed to reopen it.
registerOverlayReopen(BOOK_EDIT_OVERLAY, (overlay) => {
    if (!overlay.target) return;
    const fromBook = isBookPath(window.location.pathname);
    const listContext = readBookListContextFromLocation();
    const host = fromBook ? activeBookDetailHost() : null;
    void openEditModal({ id: overlay.target }, listContext, null, host);
});

export async function openEditModal(
    summary: Pick<BookSummary, 'id'>,
    listContext?: BookListContext | null,
    initialSequence?: BookSequenceWindow | null,
    // The page that opened this dialog, when there is one behind it. Opened from
    // the library there is no book page to keep in step, so there is no host.
    host?: BookDetailHost | null,
) {
    let cancelled = false;
    const overlay: OverlayEntry = { kind: BOOK_EDIT_OVERLAY, target: summary.id };
    const { modal, root } = openModal({
        title: 'Edit book',
        body: `
            <div class="edit-modal-loading-state local-loading-state" role="status" aria-live="polite">
                <span class="local-spinner" aria-hidden="true"></span>
                <div class="edit-modal-loading-title">Loading book...</div>
            </div>
        `,
        backdropClass: 'modal-wide',
        modalClass: 'edit-modal edit-modal-loading',
        history: overlay,
        onClose: () => {
            cancelled = true;
        },
    });
    modal.open();

    const finishGlobalLoading = beginGlobalLoading();
    try {
        const b = await fetchBook(summary.id);
        if (cancelled) return;
        modal.close();
        openLoadedEditModal(b, overlay, listContext, initialSequence, host);
    } catch (e) {
        console.error('Failed to load book for editing:', e);
        if (cancelled) return;
        const body = root.querySelector<HTMLElement>('.modal-body');
        if (body) {
            body.innerHTML = `
                <div class="edit-modal-loading-state edit-modal-loading-error" role="alert">
                    <div class="edit-modal-loading-title">Could not load this book.</div>
                    <div class="edit-modal-loading-text">Close this window and try again.</div>
                </div>
            `;
        }
    } finally {
        finishGlobalLoading();
    }
}

function openLoadedEditModal(
    b: Book,
    initialOverlay: OverlayEntry,
    listContext?: BookListContext | null,
    initialSequence?: BookSequenceWindow | null,
    host?: BookDetailHost | null,
) {
    // The table/list passes a lean Book projection — the list endpoint omits the
    // edition-level fields (identifiers, language, publisher). Always load the
    // authoritative full record by id so the form is complete and current no
    // matter where edit was opened from, instead of trusting the caller's shape.
    host?.showBook(b, listContext);
    // Element ids stay pinned to the opened modal; Save & Next rebinds b below.
    const uiID = b.id;
    let coverDraft: CoverDraftController | null = null;
    let overlay = initialOverlay;

    const { modal, root } = openModal({
        header: renderEditHeader(uiID),
        body: renderEditForm(b, uiID),
        backdropClass: 'modal-wide',
        modalClass: 'edit-modal',
        closeButton: false,
        ariaLabel: 'Edit book',
        history: overlay,
        onKeydown: (event) => {
            if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 's') {
                event.preventDefault();
                form.requestSubmit();
                return true;
            }
            return false;
        },
        // Guard every dismissal path — close button, Escape, backdrop click:
        // block while busy, and confirm before discarding unsaved edits.
        beforeClose: () => {
            if (saving || switching) return false;
            if (!isDirty()) return true;
            return confirmModal({
                title: 'Discard changes?',
                body: 'The edited metadata has not been saved.',
                confirmLabel: 'Discard',
                cancelLabel: 'Keep editing',
            });
        },
        onClose: (reason) => {
            closed = true;
            coverDraft?.destroy();
            coverDraft = null;
            datePickerPopover?.destroy();
            // Navigation closes modals programmatically; do not rewrite its destination.
            if (host && reason !== 'api') {
                host.rerender();
                setTimeout(() => document.getElementById('btn-edit-book')?.focus(), 0);
            }
        },
    });
    if (listContext) {
        root.dataset.bookListContext = JSON.stringify(listContext);
    }
    const form = document.getElementById(`edit-book-form-${uiID}`) as HTMLFormElement;
    let datePickerPopover: ReturnType<typeof attachFlexibleDatePicker> | null = null;
    let closed = false;

    const saveBtn = document.getElementById(`btn-edit-save-${uiID}`) as HTMLButtonElement;
    const fetchMetadataBtn = document.getElementById(
        `btn-edit-fetch-metadata-${uiID}`,
    ) as HTMLButtonElement | null;
    let savedState = readEditForm(form);
    const fetchedFieldSources = new Map<EditFieldName, FetchedFieldSource>();
    let saving = false;
    let switching = false;
    let savedFlashTimer: number | undefined;
    let sequenceController: BookEditSequenceController | null = null;
    coverDraft = createCoverDraftController({
        uiID,
        book: () => b,
        draft: () => readEditForm(form),
        isBusy: () => saving || switching,
        isClosed: () => closed,
        onChange: () => updateDirtyState(),
    });
    const titleInput = form.querySelector<HTMLInputElement>('input[name="title"]');
    const sortTitleInput = form.querySelector<HTMLInputElement>('input[name="sort_title"]');
    const authorsInput = form.querySelector<HTMLInputElement>('input[name="authors"]');
    const titleSortControls = wireTitleSortEditor({
        uiID,
        titleInput,
        sortTitleInput,
        language: () => b.language,
    });

    const authorSortReveal = document.getElementById(
        `author-sort-reveal-${uiID}`,
    ) as HTMLButtonElement | null;
    const authorSortEditor = document.getElementById(
        `author-sort-editor-${uiID}`,
    ) as HTMLElement | null;
    const authorSortNote = document.getElementById(
        `author-sort-note-${uiID}`,
    ) as HTMLButtonElement | null;
    const authorSortInput = document.getElementById(
        `author-sort-input-${uiID}`,
    ) as HTMLInputElement | null;
    const authorSortAutoBtn = document.getElementById(
        `author-sort-auto-${uiID}`,
    ) as HTMLButtonElement | null;
    const authorSortUseNameBtn = document.getElementById(
        `author-sort-use-name-${uiID}`,
    ) as HTMLButtonElement | null;
    const authorSortRevertBtn = document.getElementById(
        `author-sort-revert-${uiID}`,
    ) as HTMLButtonElement | null;
    const authorSortHint = document.getElementById(`author-sort-hint-${uiID}`);
    const authorSortError = document.getElementById(`author-sort-error-${uiID}`);
    let authorSortState = authorSortStateFromBook(b);

    const currentAuthorNames = () => parseAuthorList(authorsInput?.value || '');
    const currentPrimaryAuthorName = () => currentAuthorNames()[0] || '';
    const currentAuthorSortBase = () =>
        authorSortState.baseName === currentPrimaryAuthorName()
            ? authorSortState.baseSortName
            : currentPrimaryAuthorName();
    const currentAuthorSortChange = () => {
        const name = currentPrimaryAuthorName();
        const sortName = authorSortInput?.value.trim() || name;
        if (!name) return null;
        if (sameEditFieldValue(sortName, currentAuthorSortBase())) return null;
        return { name, sortName };
    };
    const authorSortDirty = () => currentAuthorSortChange() !== null;
    // A genuine override: the current sort deviates from what Auto ("Last, First")
    // would derive from the name. The standard derived form is not an override, so
    // it stays silent — only deliberate, non-standard sorts surface a footnote.
    const authorSortIsCustom = () => {
        const name = currentPrimaryAuthorName();
        if (!name) return false;
        const sortName = authorSortInput?.value.trim() || name;
        return !sameEditFieldValue(sortName, authorSort(name));
    };
    // Author sort is a global property of the author. The editor stays collapsed;
    // a faint "Sort" reveal shows on hover/focus, and a "sorts as …" footnote
    // surfaces only a genuine override (see authorSortIsCustom). The exact scope
    // count is advisory: fetch it lazily, but never let it rewrite the input.
    const syncAuthorSortDisclosure = () => {
        if (!authorSortReveal || !authorSortEditor || !authorSortNote || !authorSortInput) {
            return;
        }
        const names = currentAuthorNames();
        const name = names[0] || '';
        const dirty = authorSortDirty();
        const open = authorSortState.forcedOpen;
        const custom = authorSortIsCustom();
        authorSortReveal.hidden = !name || open || custom;
        authorSortReveal.setAttribute('aria-expanded', String(open));
        authorSortEditor.hidden = !open;
        if (authorSortRevertBtn) {
            authorSortRevertBtn.hidden = !dirty;
            authorSortRevertBtn.title = `Revert to ${editFieldValuePreview(currentAuthorSortBase())}`;
        }
        if (authorSortHint) {
            authorSortHint.textContent = authorSortHintText(
                name,
                names.length,
                authorSortState.bookCount,
                dirty,
            );
        }
        if (authorSortError) {
            authorSortError.textContent = authorSortState.error;
            authorSortError.style.display = authorSortState.error ? 'block' : 'none';
        }
        if (!open && name && custom) {
            authorSortNote.hidden = false;
            authorSortNote.textContent = `sorts as “${authorSortInput.value.trim() || name}”`;
        } else {
            authorSortNote.hidden = true;
        }
    };
    // Seed the sort for a name with no stored author row from the Auto heuristic,
    // not the verbatim name, so a freshly typed author defaults to "Last, First"
    // and is not mistaken for a custom override.
    const authorSortSeedForName = (book: Book, name: string) => {
        const primary = book.authors_list?.[0];
        return primary?.name === name ? primary.sort_name || name : authorSort(name);
    };
    const resetAuthorSortForName = (
        name: string,
        forcedOpen = false,
        sortName = authorSortSeedForName(b, name),
    ) => {
        authorSortState = {
            baseName: name,
            baseSortName: sortName,
            bookCount: null,
            bookCountName: '',
            error: '',
            forcedOpen,
        };
        if (authorSortInput) authorSortInput.value = sortName;
        syncAuthorSortDisclosure();
    };
    const loadAuthorSortBookCount = async (name: string) => {
        if (!name || authorSortState.bookCountName === name) return;
        authorSortState = {
            ...authorSortState,
            bookCount: null,
            bookCountName: name,
        };
        syncAuthorSortDisclosure();
        try {
            const info = await fetchAuthorInfo(name);
            if (closed || !sameEditFieldValue(currentPrimaryAuthorName(), name)) return;
            authorSortState = {
                ...authorSortState,
                bookCount: info?.book_count ?? null,
                bookCountName: name,
            };
        } catch {
            if (closed || !sameEditFieldValue(currentPrimaryAuthorName(), name)) return;
            authorSortState = {
                ...authorSortState,
                bookCount: null,
                bookCountName: name,
            };
        }
        syncAuthorSortDisclosure();
    };
    const resetAuthorSortFromBook = (book: Book) => {
        authorSortState = authorSortStateFromBook(book);
        if (authorSortInput) authorSortInput.value = authorSortState.baseSortName;
        syncAuthorSortDisclosure();
    };
    const openAuthorSortEditor = () => {
        authorSortState = { ...authorSortState, forcedOpen: true, error: '' };
        syncAuthorSortDisclosure();
        void loadAuthorSortBookCount(currentPrimaryAuthorName());
        authorSortInput?.focus();
        authorSortInput?.select();
    };
    const applyPickedPrimaryAuthor = (author: Author) => {
        if (!sameEditFieldValue(currentPrimaryAuthorName(), author.name)) return;
        resetAuthorSortForName(
            author.name,
            authorSortState.forcedOpen,
            author.sort_name || author.name,
        );
        if (authorSortState.forcedOpen) void loadAuthorSortBookCount(author.name);
        updateDirtyState();
    };
    authorSortReveal?.addEventListener('click', openAuthorSortEditor);
    authorSortNote?.addEventListener('click', openAuthorSortEditor);
    authorSortEditor?.addEventListener('focusout', (event) => {
        if (authorSortEditor.contains(event.relatedTarget as Node | null)) return;
        authorSortState = { ...authorSortState, forcedOpen: false };
        syncAuthorSortDisclosure();
    });
    authorSortInput?.addEventListener('input', () => {
        authorSortState = {
            ...authorSortState,
            error: '',
        };
        updateDirtyState();
    });
    authorSortAutoBtn?.addEventListener('click', () => {
        const name = currentPrimaryAuthorName();
        if (!name || !authorSortInput) return;
        authorSortInput.value = authorSort(name);
        authorSortState = {
            ...authorSortState,
            error: '',
        };
        authorSortInput.focus();
        updateDirtyState();
    });
    authorSortUseNameBtn?.addEventListener('click', () => {
        const name = currentPrimaryAuthorName();
        if (!name || !authorSortInput) return;
        authorSortInput.value = name;
        authorSortState = {
            ...authorSortState,
            error: '',
        };
        authorSortInput.focus();
        updateDirtyState();
    });
    authorSortRevertBtn?.addEventListener('click', () => {
        if (!authorSortInput) return;
        authorSortInput.value = currentAuthorSortBase();
        authorSortState = {
            ...authorSortState,
            forcedOpen: false,
            error: '',
        };
        updateDirtyState();
    });
    authorsInput?.addEventListener('input', () => {
        const name = currentPrimaryAuthorName();
        if (name !== authorSortState.baseName) {
            const wasOpen = authorSortState.forcedOpen;
            resetAuthorSortForName(name, wasOpen);
            return;
        }
        syncAuthorSortDisclosure();
    });

    const formIsDirty = () => stateKey(readEditForm(form)) !== stateKey(savedState);
    const isDirty = () => formIsDirty() || !!coverDraft?.hasPending() || authorSortDirty();
    const dirtyFieldCount = () =>
        dirtyEditFields(readEditForm(form), savedState).length +
        (coverDraft?.hasPending() ? 1 : 0) +
        (authorSortDirty() ? 1 : 0);
    const updateDirtyState = () => {
        const dirty = isDirty();
        const titleValid = validateTitle(form, uiID);
        const generatingCover = coverDraft?.isGenerating() ?? false;
        saveBtn.disabled = saving || switching || generatingCover || !dirty || !titleValid;
        sequenceController?.update(dirty, saving || switching);
        if (fetchMetadataBtn) {
            fetchMetadataBtn.disabled = saving || switching || generatingCover;
            fetchMetadataBtn.title = dirty
                ? 'Fetched metadata will skip fields already edited in this draft'
                : '';
        }
        syncFieldDecorations(form, savedState, fetchedFieldSources, revertField);
        titleSortControls.sync();
        syncAuthorSortDisclosure();
        coverDraft?.syncControls(saving || switching);
        if (switching) {
            setSaveIndicator(uiID, '', '');
        } else if (saving) {
            setSaveIndicator(uiID, 'Saving...', 'saving');
        } else if (dirty) {
            const count = dirtyFieldCount();
            setSaveIndicator(
                uiID,
                `${count} unsaved ${count === 1 ? 'change' : 'changes'}`,
                'dirty',
            );
        } else if (!savedFlashTimer) {
            setSaveIndicator(uiID, '', '');
        }
    };

    const applyMetadataDraft = (apply: MetadataDraftApply) => {
        for (const field of apply.fields) {
            setEditFieldValue(form, field, apply.patch[field] ?? null);
            fetchedFieldSources.set(field, {
                providerName: apply.providerName,
                value: readEditForm(form)[field],
            });
        }
        if (apply.coverUrl) {
            coverDraft?.setFetched(apply.coverUrl, apply.providerName);
        }
        titleSortControls.resetFollow();
        updateIdentifiersValidation();
        renderDateHint(document.getElementById(`date-validation-${uiID}`), b);
        updateDirtyState();
    };

    function revertField(field: EditFieldName) {
        setEditFieldValue(form, field, savedState[field]);
        fetchedFieldSources.delete(field);
        if (field === 'sort_title') titleSortControls.close();
        if (field === 'title' || field === 'sort_title') titleSortControls.resetFollow();
        updateIdentifiersValidation();
        renderDateHint(document.getElementById(`date-validation-${uiID}`), b);
        updateDirtyState();
    }

    const setFormSwitching = (active: boolean) => {
        switching = active;
        const modalEl = root.querySelector<HTMLElement>('.edit-modal');
        const loadingOverlay = document.getElementById(`edit-form-loading-overlay-${uiID}`);
        if (modalEl) {
            if (active) {
                const height = Math.ceil(modalEl.getBoundingClientRect().height);
                modalEl.style.minHeight = `${height}px`;
                modalEl.classList.add('edit-modal-switching');
                modalEl.setAttribute('aria-busy', 'true');
                if (loadingOverlay) loadingOverlay.hidden = false;
            } else {
                modalEl.removeAttribute('aria-busy');
                if (loadingOverlay) loadingOverlay.hidden = true;
                window.requestAnimationFrame(() => {
                    window.requestAnimationFrame(() => {
                        modalEl.style.minHeight = '';
                        modalEl.classList.remove('edit-modal-switching');
                    });
                });
            }
        }
        for (const control of form.querySelectorAll<
            HTMLInputElement | HTMLTextAreaElement | HTMLButtonElement
        >('input, textarea, button')) {
            control.disabled = active;
        }
        updateDirtyState();
    };

    const switchToBook = async (target: BookSequenceItem) => {
        datePickerPopover?.close();
        if (savedFlashTimer) {
            window.clearTimeout(savedFlashTimer);
            savedFlashTimer = undefined;
        }
        const previousSequence = sequenceController?.snapshot() ?? null;
        const targetIndex = sequenceController?.targetIndex(target.id) ?? -1;
        const direction = sequenceController?.directionForIndex(targetIndex) ?? 'next';
        setFormSwitching(true);
        const finishGlobalLoading = beginGlobalLoading();
        try {
            const nextBook = await fetchBook(target.id);
            if (closed) return;
            b = nextBook;
            host?.showBook(b, listContext);
            overlay = { ...overlay, target: b.id };
            updateOverlayEntry(overlay);
            syncEditFormFromBook(form, b, uiID);
            savedState = readEditForm(form);
            fetchedFieldSources.clear();
            coverDraft?.resetToStored();
            titleSortControls.close();
            titleSortControls.resetFollow();
            resetAuthorSortFromBook(b);
            updateIdentifiersValidation();
            renderDateHint(document.getElementById(`date-validation-${uiID}`), b);
            sequenceController?.setCurrentIndex(targetIndex);
            sequenceController?.update(isDirty(), saving || switching);
            sequenceController?.maybeRefreshExhausted(direction);
            titleInput?.focus({ preventScroll: true });
        } catch (err) {
            console.error('Failed to switch edit book:', err);
            sequenceController?.restore(previousSequence);
            showToast(`Load failed: ${errorMessage(err)}`, { type: 'error' });
        } finally {
            finishGlobalLoading();
            if (!closed) {
                setFormSwitching(false);
            }
        }
    };

    const commitCurrent = async (flash: boolean): Promise<Book | null> => {
        if (saving || switching) return null;
        if (!isDirty()) return b;
        const metadataDirty = formIsDirty();
        const authorSortChange = currentAuthorSortChange();
        let saved: Book | null = null;
        const authorChange = { changed: false, previous: '', next: '' };
        if (metadataDirty) {
            await saveEditForm(
                b,
                form,
                savedState,
                uiID,
                {
                    beforeSave: () => {
                        saving = true;
                        updateDirtyState();
                    },
                    afterSave: (ok, updated, previousState) => {
                        saving = false;
                        if (ok && updated) {
                            Object.assign(b, updated);
                            syncEditFormFromBook(form, b, uiID);
                            coverDraft?.renderPending();
                            savedState = readEditForm(form);
                            fetchedFieldSources.clear();
                            titleSortControls.close();
                            titleSortControls.resetFollow();
                            if (!authorSortChange) resetAuthorSortFromBook(b);
                            const prevAuthors = previousState.authors || '';
                            const nextAuthors = savedState.authors || '';
                            if (prevAuthors !== nextAuthors) {
                                authorChange.changed = true;
                                authorChange.previous = prevAuthors;
                                authorChange.next = nextAuthors;
                            }
                            if (savedFlashTimer) window.clearTimeout(savedFlashTimer);
                            savedFlashTimer = undefined;
                            if (flash && !coverDraft?.hasPending()) {
                                savedFlashTimer = flashSaved(uiID, () => {
                                    savedFlashTimer = undefined;
                                    updateDirtyState();
                                });
                            }
                            saved = updated;
                            notifyCatalogChanged({ kind: 'books-updated', books: [updated] });
                        }
                        updateIdentifiersValidation();
                        renderDateHint(document.getElementById(`date-validation-${uiID}`), b);
                        updateDirtyState();
                    },
                },
                host,
            );
            if (!saved) return null;
        }
        if (authorSortChange) {
            authorSortState = { ...authorSortState, error: '' };
            saving = true;
            updateDirtyState();
            try {
                await setAuthorSortName(authorSortChange.name, authorSortChange.sortName);
                const updated = await fetchBook(b.id);
                if (closed) return null;
                host?.applySaved(updated);
                Object.assign(b, updated);
                syncEditFormFromBook(form, b, uiID);
                coverDraft?.renderPending();
                savedState = readEditForm(form);
                resetAuthorSortFromBook(b);
                saved = updated;
                notifyCatalogChanged({ kind: 'books-updated', books: [updated] });
                if (savedFlashTimer) window.clearTimeout(savedFlashTimer);
                savedFlashTimer = undefined;
                if (flash && !coverDraft?.hasPending()) {
                    savedFlashTimer = flashSaved(uiID, () => {
                        savedFlashTimer = undefined;
                        updateDirtyState();
                    });
                }
            } catch (err) {
                authorSortState = {
                    ...authorSortState,
                    error: `Author sort save failed: ${errorMessage(err)}`,
                };
                return null;
            } finally {
                saving = false;
                updateDirtyState();
            }
        }
        if (coverDraft?.hasPending()) {
            saving = true;
            updateDirtyState();
            try {
                const updated = await coverDraft.savePending(b.id);
                host?.applySaved(updated);
                Object.assign(b, updated);
                syncEditFormFromBook(form, b, uiID);
                savedState = readEditForm(form);
                fetchedFieldSources.clear();
                titleSortControls.close();
                titleSortControls.resetFollow();
                resetAuthorSortFromBook(b);
                saved = updated;
                notifyCatalogChanged({ kind: 'books-updated', books: [updated] });
                if (savedFlashTimer) window.clearTimeout(savedFlashTimer);
                savedFlashTimer = undefined;
                if (flash) {
                    savedFlashTimer = flashSaved(uiID, () => {
                        savedFlashTimer = undefined;
                        updateDirtyState();
                    });
                }
            } catch {
                return null;
            } finally {
                saving = false;
                updateIdentifiersValidation();
                renderDateHint(document.getElementById(`date-validation-${uiID}`), b);
                updateDirtyState();
            }
        }
        if (saved && authorChange.changed) {
            await maybeOfferAuthorConvergence(b, authorChange.previous, authorChange.next, uiID);
        }
        return saved;
    };

    const openSequenceItem = async (target: BookSequenceItem | null | undefined) => {
        if (!target || saving || switching) return;
        const resolvedTarget = target;
        if (isDirty()) {
            const saved = await commitCurrent(false);
            if (!saved) return;
        }
        void switchToBook(resolvedTarget);
    };

    form.addEventListener('submit', (e) => {
        e.preventDefault();
        if (saving || switching || !isDirty()) return;
        void commitCurrent(true);
    });

    fetchMetadataBtn?.addEventListener('click', () => {
        if (switching) return;
        openMetadataCandidatesModal(
            b,
            readEditForm(form),
            savedState,
            coverDraft?.pendingURL() ?? null,
            applyMetadataDraft,
        );
    });

    const identifiersInput = document.getElementById(
        `identifiers-input-${uiID}`,
    ) as HTMLInputElement;
    const identifiersValidation = document.getElementById(
        `identifiers-validation-${uiID}`,
    ) as HTMLElement;

    // Quiet validation: a valid ISBN gets only a small check (no shouty green
    // label), while an invalid one gets a flagged mark plus a short word so the
    // problem is legible. The exact value lives in the title tooltip, not inline.
    const updateIdentifiersValidation = () => {
        const ids = parseIdentifiers(identifiersInput.value);
        const marks: string[] = [];
        for (const id of ids) {
            if (id.type !== 'isbn') continue;
            if (validISBN(id.value)) {
                marks.push(
                    `<span class="field-flag field-flag-ok" title="Valid ISBN ${escapeHtml(id.value)}">${icon('check', 14)}</span>`,
                );
            } else {
                marks.push(
                    `<span class="field-flag field-flag-bad" title="Invalid ISBN ${escapeHtml(id.value)}">${icon('close', 14)} Invalid ISBN</span>`,
                );
            }
        }
        identifiersValidation.innerHTML = marks.join('');
        identifiersValidation.style.display = marks.length > 0 ? 'flex' : 'none';
    };

    if (identifiersInput) {
        attachIdentifierAutocomplete(identifiersInput);
        identifiersInput.addEventListener('input', updateIdentifiersValidation);
        updateIdentifiersValidation();
    }

    const hiddenDescInput = form.querySelector(
        'textarea[name="description"]',
    ) as HTMLTextAreaElement;
    hiddenDescInput.value = b.description_source || '';
    const editorWrapper = document.getElementById(`editor-wrapper-${uiID}`);
    if (editorWrapper) {
        const editorComponent = createRichEditor(
            b.description_html || null,
            b.description_source || null,
            (html) => {
                hiddenDescInput.value = html;
                hiddenDescInput.dispatchEvent(new Event('input'));
            },
            () => {
                hiddenDescInput.dispatchEvent(new Event('blur'));
            },
        );
        editorWrapper.appendChild(editorComponent);
    }

    const dateInput = document.getElementById(`date-input-${uiID}`) as HTMLInputElement;
    const dateValidation = document.getElementById(`date-validation-${uiID}`) as HTMLElement;
    const datePickerTrigger = document.getElementById(
        `date-picker-${uiID}`,
    ) as HTMLButtonElement | null;
    if (dateInput) {
        renderDateHint(dateValidation, b);
    }
    if (dateInput && datePickerTrigger) {
        datePickerPopover = attachFlexibleDatePicker(dateInput, datePickerTrigger, {
            value: () => dateInput.value,
            onCommit: () => {
                updateDirtyState();
            },
        });
    }

    if (authorsInput) {
        attachAuthorAutocomplete(authorsInput, {
            onPick: applyPickedPrimaryAuthor,
        });
    }
    const tagsInput = form.querySelector('input[name="tags"]') as HTMLInputElement | null;
    if (tagsInput) {
        attachTagAutocomplete(tagsInput);
    }
    const seriesInput = form.querySelector('input[name="series"]') as HTMLInputElement | null;
    if (seriesInput) {
        attachSeriesAutocomplete(seriesInput);
    }

    const inputs = form.querySelectorAll<HTMLInputElement | HTMLTextAreaElement>(
        'input[name], textarea[name]',
    );
    inputs.forEach((input) => {
        input.addEventListener('input', updateDirtyState);
        input.addEventListener('change', updateDirtyState);
        input.addEventListener('blur', (e) => {
            const target = e.target as HTMLInputElement | HTMLTextAreaElement;
            const name = target.name;

            if (name === 'identifiers') {
                target.value = formatIdentifiers(parseIdentifiers(target.value));
                updateIdentifiersValidation();
            }
            updateDirtyState();
        });
    });

    savedState = readEditForm(form);
    resetAuthorSortFromBook(b);
    sequenceController = createBookEditSequenceController({
        uiID,
        initialSequence,
        listContext,
        currentBookID: () => b.id,
        isClosed: () => closed,
        isDirty,
        isBusy: () => saving || switching,
        onOpen: openSequenceItem,
    });
    updateDirtyState();
    sequenceController.start();
    modal.open();
}

function renderEditHeader(uiID: string): string {
    return `
            <div class="modal-header edit-modal-header">
                <div class="edit-header-titlebar">
                    <h2>Edit book</h2>
                    <div class="edit-sequence-actions" id="edit-sequence-actions-${uiID}" hidden>
                        <button type="button" id="btn-edit-previous-${uiID}" class="edit-nav-btn edit-nav-icon-btn" disabled aria-label="Previous book" title="Previous book">${icon('arrow_back', 18)}</button>
                        <button type="button" id="btn-edit-next-${uiID}" class="edit-nav-btn edit-nav-icon-btn" disabled aria-label="Next book" title="Next book">${icon('arrow_back', 18, 'edit-nav-next-icon')}</button>
                    </div>
                </div>
                <div class="edit-header-actions">
                    <div id="save-indicator-${uiID}" class="save-indicator" aria-live="polite"></div>
                    <button type="button" id="btn-edit-fetch-metadata-${uiID}" class="metadata-fetch-action">Fetch meta</button>
                    <button type="submit" form="edit-book-form-${uiID}" id="btn-edit-save-${uiID}" class="edit-save-btn" disabled>Save</button>
                    <button class="modal-close edit-header-close" type="button" data-modal-close aria-label="Close">${icon('close', 24)}</button>
                </div>
            </div>
    `;
}

function renderEditForm(b: Book, uiID: string): string {
    const editCoverHtml = coverImgHtml(
        b.id,
        b.cover_version,
        `edit-cover-image-${uiID}`,
        'detail-cover-image edit-cover-image-small',
    );

    return `
                <form id="edit-book-form-${uiID}" class="edit-form">
                    <div class="edit-form-layout">
                        <div class="edit-form-main">
                            <div class="sort-host">
                                <div class="form-group" data-edit-field="title">
                                    <label class="form-label">Title</label>
                                    <div class="sort-input-wrap">
                                        <input type="text" name="title" value="${escapeHtml(b.title)}" required class="form-input">
                                        <button type="button" id="title-sort-reveal-${uiID}" class="sort-reveal" aria-expanded="false" aria-controls="title-sort-editor-${uiID}">Sort</button>
                                    </div>
                                    <div id="title-error-${uiID}" class="input-error">Title cannot be empty.</div>
                                </div>
                                <div class="sort-field">
                                    <div id="title-sort-editor-${uiID}" class="sort-editor" hidden>
                                        <div class="sort-editor-head">
                                            <label class="form-label" for="title-sort-input-${uiID}">Title sort</label>
                                            <button type="button" id="title-sort-auto-${uiID}" class="sort-action">Auto</button>
                                            <button type="button" id="title-sort-same-${uiID}" class="sort-action">Use title</button>
                                        </div>
                                        <input type="text" id="title-sort-input-${uiID}" name="sort_title" value="${escapeHtml(b.sort_title || b.title)}" class="form-input">
                                    </div>
                                    <button type="button" id="title-sort-note-${uiID}" class="sort-note" hidden></button>
                                </div>
                            </div>
                            <div class="sort-host">
                                <div class="form-group" data-edit-field="authors">
                                    <label class="form-label">Authors<span class="field-hint"> — semicolon separated</span></label>
                                    <div class="sort-input-wrap">
                                        <input type="text" name="authors" value="${escapeHtml(formatAuthorsForEdit(b.authors_list))}" required class="form-input" title="Use ; between authors. Calibre-style & also works; use && for a literal &.">
                                        <button type="button" id="author-sort-reveal-${uiID}" class="sort-reveal" aria-expanded="false" aria-controls="author-sort-editor-${uiID}">Sort</button>
                                    </div>
                                </div>
                                <div class="sort-field">
                                    <div id="author-sort-editor-${uiID}" class="sort-editor" hidden>
                                        <div class="sort-editor-head">
                                            <label class="form-label" for="author-sort-input-${uiID}">Author sort</label>
                                            <button type="button" id="author-sort-auto-${uiID}" class="sort-action">Auto</button>
                                            <button type="button" id="author-sort-use-name-${uiID}" class="sort-action">Use name</button>
                                            <button type="button" id="author-sort-revert-${uiID}" class="sort-action" hidden>Revert</button>
                                        </div>
                                        <input type="text" id="author-sort-input-${uiID}" class="form-input">
                                        <div id="author-sort-hint-${uiID}" class="author-sort-hint"></div>
                                        <div id="author-sort-error-${uiID}" class="author-sort-error"></div>
                                    </div>
                                    <button type="button" id="author-sort-note-${uiID}" class="sort-note" hidden></button>
                                </div>
                            </div>
                            <div class="form-row">
                                <div class="form-col-flex" data-edit-field="series">
                                    <label class="form-label">Series</label>
                                    <input type="text" name="series" value="${escapeHtml(b.series || '')}" class="form-input">
                                </div>
                                <div class="form-col-fixed" data-edit-field="series_index">
                                    <label class="form-label">Index</label>
                                    <input type="number" step="0.1" name="series_index" value="${b.series_index || ''}" class="form-input">
                                </div>
                            </div>
                            <div class="form-group" data-edit-field="tags">
                                <label class="form-label">Tags</label>
                                <input type="text" name="tags" value="${escapeHtml(b.tags || '')}" class="form-input">
                            </div>
                            <div class="form-row">
                                <div class="form-col-narrow" data-edit-field="language">
                                    <label class="form-label">Language</label>
                                    <input type="text" name="language" value="${escapeHtml(b.language || '')}" class="form-input">
                                </div>
                                <div class="form-col-flex" data-edit-field="publisher">
                                    <label class="form-label">Publisher</label>
                                    <input type="text" name="publisher" value="${escapeHtml(b.publisher || '')}" class="form-input">
                                </div>
                                <div class="form-col-narrow form-col-date" data-edit-field="date">
                                    <label class="form-label">Published</label>
                                    <div class="date-input-row">
                                        <input type="text" name="date" id="date-input-${uiID}" value="${escapeHtml(b.date_human || b.date || '')}" class="form-input">
                                        ${datePickerButton(`date-picker-${uiID}`)}
                                    </div>
                                    <div id="date-validation-${uiID}" class="field-validation"></div>
                                </div>
                            </div>
                            <div class="form-group" data-edit-field="identifiers">
                                <label class="form-label">Identifiers</label>
                                <input type="text" name="identifiers" id="identifiers-input-${uiID}" value="${escapeHtml(b.identifiers || '')}" class="form-input">
                                <div id="identifiers-validation-${uiID}" class="field-validation"></div>
                            </div>
                        </div>

                        <div class="edit-form-side">
                            <div class="edit-cover-row">
                                <div id="edit-cover-container-${uiID}" class="edit-cover-container">
                                    ${editCoverHtml}
                                </div>
                                <div class="edit-cover-actions">
                                    <input type="file" id="edit-cover-upload-${uiID}" style="display:none" accept="image/jpeg,image/png,image/gif,image/webp">
                                    <button type="button" id="btn-edit-cover-chooser-${uiID}" class="edit-cover-choose">${icon('edit', 16)} Change cover</button>
                                    <button type="button" id="btn-edit-cover-search-${uiID}" class="edit-cover-choose">${icon('search', 16)} Find cover online</button>
                                    <button type="button" id="btn-edit-revert-cover-${uiID}" class="edit-cover-revert" hidden>Revert cover</button>
                                </div>
                            </div>
                            <div class="form-group edit-description-group" id="editor-wrapper-${uiID}" data-edit-field="description">
                                <label class="form-label">Description</label>
                                <textarea name="description" style="display:none"></textarea>
                            </div>
                        </div>
                    </div>
                    <div class="edit-form-loading-overlay" id="edit-form-loading-overlay-${uiID}" role="status" aria-live="polite" hidden>
                        <span class="local-spinner" aria-hidden="true"></span>
                        <span>Loading book...</span>
                    </div>
                </form>
    `;
}

type TitleSortControls = {
    sync: () => void;
    resetFollow: () => void;
    close: () => void;
};

function wireTitleSortEditor(opts: {
    uiID: string;
    titleInput: HTMLInputElement | null;
    sortTitleInput: HTMLInputElement | null;
    language: () => string | null | undefined;
}): TitleSortControls {
    const reveal = document.getElementById(
        `title-sort-reveal-${opts.uiID}`,
    ) as HTMLButtonElement | null;
    const editor = document.getElementById(`title-sort-editor-${opts.uiID}`) as HTMLElement | null;
    const note = document.getElementById(
        `title-sort-note-${opts.uiID}`,
    ) as HTMLButtonElement | null;
    const autoBtn = document.getElementById(
        `title-sort-auto-${opts.uiID}`,
    ) as HTMLButtonElement | null;
    const sameBtn = document.getElementById(
        `title-sort-same-${opts.uiID}`,
    ) as HTMLButtonElement | null;

    let forcedOpen = false;
    let followsTitle = sameEditFieldValue(opts.sortTitleInput?.value, opts.titleInput?.value);
    const currentSortText = () => opts.sortTitleInput?.value.trim() || '';
    const currentTitleText = () => opts.titleInput?.value.trim() || '';

    const sync = () => {
        if (!reveal || !editor || !note || !opts.sortTitleInput) return;
        const differs = !sameEditFieldValue(currentSortText(), currentTitleText());
        editor.hidden = !forcedOpen;
        reveal.hidden = forcedOpen || differs;
        reveal.setAttribute('aria-expanded', String(forcedOpen));
        if (!forcedOpen && differs) {
            note.hidden = false;
            note.textContent = `sorts as “${currentSortText() || 'empty'}”`;
        } else {
            note.hidden = true;
        }
    };
    const resetFollow = () => {
        followsTitle = sameEditFieldValue(opts.sortTitleInput?.value, opts.titleInput?.value);
    };
    const open = () => {
        forcedOpen = true;
        sync();
        opts.sortTitleInput?.focus();
        opts.sortTitleInput?.select();
    };
    const close = () => {
        forcedOpen = false;
        sync();
    };

    opts.titleInput?.addEventListener('input', () => {
        if (!followsTitle || !opts.sortTitleInput || !opts.titleInput) return;
        opts.sortTitleInput.value = opts.titleInput.value;
    });
    opts.sortTitleInput?.addEventListener('input', () => {
        resetFollow();
    });
    reveal?.addEventListener('click', open);
    note?.addEventListener('click', open);
    editor?.addEventListener('focusout', (event) => {
        if (editor.contains(event.relatedTarget as Node | null)) return;
        close();
    });
    autoBtn?.addEventListener('click', () => {
        if (!opts.sortTitleInput || !opts.titleInput) return;
        opts.sortTitleInput.value = titleSort(opts.titleInput.value, opts.language());
        opts.sortTitleInput.dispatchEvent(new Event('input', { bubbles: true }));
        opts.sortTitleInput.focus();
    });
    sameBtn?.addEventListener('click', () => {
        if (!opts.sortTitleInput || !opts.titleInput) return;
        opts.sortTitleInput.value = opts.titleInput.value;
        opts.sortTitleInput.dispatchEvent(new Event('input', { bubbles: true }));
        opts.sortTitleInput.focus();
    });

    return { sync, resetFollow, close };
}

function attachIdentifierAutocomplete(input: HTMLInputElement) {
    const types = [
        ['isbn:', 'ISBN-10 or ISBN-13'],
        ['doi:', 'Digital object identifier'],
        ['url:', 'Web link'],
        ['openlibrary:', 'Open Library'],
        ['google:', 'Google Books'],
        ['goodreads:', 'Goodreads'],
        ['uuid:', 'External UUID'],
    ];
    attachTextListAutocomplete(input, {
        className: 'identifier-list-input',
        minQueryLength: 0,
        load: async (query) => {
            const q = query.trim().toLowerCase();
            if (q.includes(':')) return [];
            return types
                .filter(([value]) => value.includes(q))
                .map(([value, meta]) => ({ value, label: value, meta }));
        },
    });
}

type SaveCallbacks = {
    beforeSave: () => void;
    afterSave: (ok: boolean, updated: Book | null, previousState: BookUpdate) => void;
};

type AuthorSortState = {
    baseName: string;
    baseSortName: string;
    bookCount: number | null;
    bookCountName: string;
    error: string;
    forcedOpen: boolean;
};

function authorSortStateFromBook(book: Book): AuthorSortState {
    const author = book.authors_list[0];
    const name = author?.name || '';
    const sortName = author?.sort_name || name;
    return {
        baseName: name,
        baseSortName: sortName,
        bookCount: null,
        bookCountName: '',
        error: '',
        forcedOpen: false,
    };
}

function authorSortHintText(
    name: string,
    authorCount: number,
    bookCount: number | null,
    dirty: boolean,
): string {
    if (!name) return '';

    const parts: string[] = [];
    if (bookCount != null && bookCount > 1) {
        parts.push(`${dirty ? 'Will update' : 'Editing updates'} "${name}" in ${bookCount} books.`);
    }
    if (authorCount > 1) {
        parts.push('Only the first author controls book sorting.');
    }
    return parts.join(' ');
}

// saveEditForm persists the current draft in one explicit write. Keeping the
// previous saved state lets the caller run post-save flows such as the
// single-author convergence prompt only after the write has landed.
async function saveEditForm(
    b: Book,
    form: HTMLFormElement,
    previousState: BookUpdate,
    uiID: string,
    callbacks: SaveCallbacks,
    host?: BookDetailHost | null,
): Promise<void> {
    normalizeFormBeforeSave(form);
    if (!validateTitle(form, uiID)) return;
    const payload = submitPayload(readEditForm(form), previousState);
    callbacks.beforeSave();

    try {
        const updatedBook = await updateBook(b.id, payload);
        host?.applySaved(updatedBook);
        callbacks.afterSave(true, updatedBook, previousState);
    } catch (err) {
        showToast(`Save failed: ${errorMessage(err)}`, { type: 'error' });
        callbacks.afterSave(false, null, previousState);
    }
}

function setSaveIndicator(uiID: string, text: string, kind: string) {
    const ind = document.getElementById(`save-indicator-${uiID}`);
    if (!ind) return;
    ind.textContent = text;
    ind.className = `save-indicator${kind ? ` save-indicator-${kind}` : ''}`;
}

function flashSaved(uiID: string, onDone?: () => void): number {
    setSaveIndicator(uiID, 'Saved', 'saved');
    return window.setTimeout(() => {
        const ind = document.getElementById(`save-indicator-${uiID}`);
        if (ind?.classList.contains('save-indicator-saved')) {
            setSaveIndicator(uiID, '', '');
        }
        onDone?.();
    }, 2000);
}

function splitAuthors(s: string): string[] {
    return parseAuthorList(s);
}

// maybeOfferAuthorConvergence runs after a per-book author edit saved. When the
// edit was an unambiguous single rename (one name left the list, one arrived) and
// the old name is still credited on other books, it offers to rename those too —
// the opinionated "found N other books by «old», rename them to «new»?" path,
// applied through the shared global-rename endpoint. Anything ambiguous (a
// reorder, an add/remove, multiple changes) is left as a plain per-book edit.
async function maybeOfferAuthorConvergence(
    b: Book,
    prevAuthors: string,
    nextAuthors: string,
    uiID: string = b.id,
) {
    const oldTokens = splitAuthors(prevAuthors);
    const newTokens = splitAuthors(nextAuthors);
    const removed = oldTokens.filter(
        (a) => !newTokens.some((n) => n.toLowerCase() === a.toLowerCase()),
    );
    const added = newTokens.filter(
        (a) => !oldTokens.some((o) => o.toLowerCase() === a.toLowerCase()),
    );
    if (removed.length !== 1 || added.length !== 1) return;
    const oldName = removed[0];
    const newName = added[0];

    let info: Awaited<ReturnType<typeof fetchAuthorInfo>>;
    try {
        info = await fetchAuthorInfo(oldName);
    } catch {
        return; // a missing count just means no prompt — never block the edit
    }
    // This book already moved to the new name, so a remaining count is "other
    // books". Nothing left crediting the old name → nothing to converge.
    if (!info || info.book_count < 1) return;

    const n = info.book_count;
    const ok = await confirmModal({
        title: 'Apply to other books?',
        body: `“${oldName}” is still credited on ${n} other book${n === 1 ? '' : 's'}. Rename ${
            n === 1 ? 'it' : 'them'
        } to “${newName}” too?`,
        confirmLabel: 'Rename all',
        cancelLabel: 'Keep separate',
    });
    if (!ok) return;

    try {
        await renameAuthor(oldName, newName);
        notifyCatalogChanged();
        flashSaved(uiID);
    } catch (err) {
        showToast(`Rename failed: ${errorMessage(err)}`, { type: 'error' });
    }
}
