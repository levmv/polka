import {
    fetchAdminStorageStatus,
    importServerFolder,
    previewFolderImport,
    saveAdminStorageStatus,
    scanIncomingFolder,
} from '../api';
import { notifyCatalogChanged } from '../catalog-events';
import { createToggle } from '../components/toggle';
import { textEl } from '../dom';
import { errorMessage } from '../errors';
import { showToast } from '../toast';
import type {
    AdminStorageStatus,
    BooksStorage,
    FolderImportPreview,
    StorageScanResult,
} from '../types';
import {
    type AsyncLoadState,
    buttonEl,
    createCopyButton,
    createReadonlyCopyControl,
    inlineSettingsButton,
    renderAsyncSection,
    settingsRow,
} from './ui';

type StorageState = AsyncLoadState & {
    status: AdminStorageStatus | null;
    folderImportExpanded: boolean;
    folderImportPath: string;
    folderImportPreview: FolderImportPreview | null;
    folderImportBusy: boolean;
};

export type StoragePanel = {
    render(root: HTMLElement): void;
    refresh(rerender: () => void): void;
};

export function createStoragePanel(): StoragePanel {
    const state: StorageState = {
        loaded: false,
        loading: false,
        status: null,
        folderImportExpanded: false,
        folderImportPath: '',
        folderImportPreview: null,
        folderImportBusy: false,
        loadError: '',
    };
    return {
        render: (root) => renderStoragePanel(root, state),
        refresh: (rerender) => {
            if (!state.loaded) return;
            fetchAdminStorageStatus()
                .then((status) => {
                    state.status = status;
                    rerender();
                })
                .catch(() => {
                    // Keep showing the current values; explicit actions surface errors.
                });
        },
    };
}

function settingsPathRow(
    labelText: string,
    description: string,
    control: HTMLElement,
    opts: { headingControl?: HTMLElement; controlHidden?: boolean } = {},
): HTMLElement {
    return settingsRow(labelText, description, control, {
        rowClass: 'settings-path-row',
        labelAccessory: opts.headingControl,
        controlHidden: opts.controlHidden,
    });
}

function renderStoragePanel(root: HTMLElement, state: StorageState): void {
    root.replaceChildren();

    const rerender = () => renderStoragePanel(root, state);
    root.append(textEl('h3', 'settings-section-title', 'Storage'));

    if (
        renderAsyncSection(state, {
            target: root,
            load: async () => {
                state.status = await fetchAdminStorageStatus();
            },
            rerender,
            errorFallback: 'Failed to load storage status',
            isReady: () => state.status !== null,
        })
    ) {
        return;
    }

    if (!state.status) return;

    const rows = document.createElement('div');
    rows.className = 'settings-rows';
    rows.append(
        settingsPathRow(
            'Books folder',
            'Where polka keeps your book files.',
            booksFolderControl(state.status.books),
        ),
        folderImportRow(state, rerender),
        settingsPathRow(
            'Incoming folder',
            'Polka watches this folder for new book files and imports them automatically.',
            incomingFolderControl(state, rerender),
            { headingControl: incomingFolderToggle(state, rerender) },
        ),
        settingsPathRow(
            'File layout',
            'How managed book files are organized inside the books folder.',
            fileLayoutControl(state.status.layout),
        ),
    );
    root.append(rows);
}

function folderImportRow(state: StorageState, rerender: () => void): HTMLElement {
    const toggle = inlineSettingsButton(
        state.folderImportExpanded ? 'Hide' : 'Add from folder…',
        () => {
            state.folderImportExpanded = !state.folderImportExpanded;
            rerender();
            if (state.folderImportExpanded) {
                requestAnimationFrame(() =>
                    document
                        .querySelector<HTMLInputElement>('#settings-folder-import-path')
                        ?.focus(),
                );
            }
        },
    );
    toggle.setAttribute('aria-expanded', String(state.folderImportExpanded));

    const control = state.folderImportExpanded
        ? folderImportControl(state, rerender)
        : document.createElement('div');
    control.id = 'settings-folder-import-fields';
    toggle.setAttribute('aria-controls', control.id);
    return settingsPathRow(
        'Add existing books',
        'Copy books from a folder on this server. The source folder stays unchanged.',
        control,
        {
            headingControl: toggle,
            controlHidden: !state.folderImportExpanded,
        },
    );
}

function fileLayoutControl(layout: AdminStorageStatus['layout']): HTMLElement {
    const wrap = document.createElement('div');
    wrap.className = 'settings-layout-control';
    wrap.append(
        createReadonlyCopyControl('File layout', layout.template),
        textEl(
            'div',
            'settings-note settings-storage-note',
            'Changes currently go through the CLI.',
        ),
    );
    return wrap;
}

// The books folder gets a health line under its path: is the folder reachable
// (the key signal when it lives on a NAS), what filesystem it is, and how much
// it holds against how much room is left.
function booksFolderControl(books: BooksStorage): HTMLElement {
    const wrap = document.createElement('div');
    wrap.className = 'settings-books-control';
    wrap.append(createReadonlyCopyControl('Books folder', books.path), booksHealthLine(books));
    return wrap;
}

function booksHealthLine(books: BooksStorage): HTMLElement {
    const line = textEl('div', 'settings-note settings-storage-note settings-health', '');

    const parts: HTMLElement[] = [
        textEl(
            'span',
            books.reachable ? 'settings-health-ok' : 'settings-health-warn',
            books.reachable ? 'Reachable' : 'Unreachable',
        ),
    ];
    if (books.fs_type && books.fs_type !== 'unknown') {
        parts.push(
            textEl('span', '', books.network ? `${books.fs_type} (network)` : books.fs_type),
        );
    }
    parts.push(
        textEl('span', '', `${books.book_count} ${books.book_count === 1 ? 'book' : 'books'}`),
    );
    parts.push(textEl('span', '', formatBytes(books.size_bytes)));
    if (books.free_bytes >= 0) {
        parts.push(textEl('span', '', `${formatBytes(books.free_bytes)} free`));
    }

    parts.forEach((part, index) => {
        if (index > 0) line.append(document.createTextNode(' · '));
        line.append(part);
    });
    return line;
}

function folderImportControl(state: StorageState, rerender: () => void): HTMLElement {
    const wrap = document.createElement('div');
    wrap.className = 'settings-folder-import-control';

    const pathInput = document.createElement('input');
    pathInput.id = 'settings-folder-import-path';
    pathInput.type = 'text';
    pathInput.value = state.folderImportPath;
    pathInput.placeholder = '/srv/books';
    pathInput.className = 'settings-secret-input settings-path-input';
    pathInput.setAttribute('aria-label', 'Add existing books folder');
    pathInput.spellcheck = false;

    const summary = textEl(
        'div',
        'settings-note settings-storage-note settings-folder-import-summary',
        '',
    );
    renderFolderImportPreviewSummary(summary, state.folderImportPreview);

    const previewButton = buttonEl('settings-btn', 'Preview', async () => {
        const path = pathInput.value.trim();
        if (!path) {
            showToast('Folder path is required', { type: 'error' });
            pathInput.focus();
            return;
        }
        state.folderImportBusy = true;
        state.folderImportPath = path;
        previewButton.textContent = 'Previewing…';
        updateButtons();
        try {
            const preview = await previewFolderImport(path);
            state.folderImportPreview = preview;
            renderFolderImportPreviewSummary(summary, preview);
            showToast(
                folderImportPreviewText(preview),
                preview.failed > 0 ? { type: 'error' } : undefined,
            );
        } catch (err) {
            state.folderImportPreview = null;
            renderFolderImportPreviewSummary(summary, null);
            showToast(errorMessage(err, 'Preview failed'), { type: 'error' });
        } finally {
            state.folderImportBusy = false;
            previewButton.textContent = 'Preview';
            updateButtons();
        }
    });

    const importButton = buttonEl('settings-btn settings-primary-btn', 'Import', async () => {
        const path = pathInput.value.trim();
        const preview = state.folderImportPreview;
        if (!preview || preview.path !== path) {
            showToast('Preview this folder before importing', { type: 'error' });
            pathInput.focus();
            return;
        }
        state.folderImportBusy = true;
        state.folderImportPath = path;
        importButton.textContent = 'Importing…';
        updateButtons();
        try {
            const result = await importServerFolder(path);
            state.status = result.storage;
            state.loaded = true;
            state.folderImportPreview = null;
            window.dispatchEvent(
                new CustomEvent<AdminStorageStatus>('polka:admin-storage', {
                    detail: result.storage,
                }),
            );
            if (result.imported > 0) notifyCatalogChanged();
            showToast(
                folderImportResultText(result),
                result.failed > 0 ? { type: 'error' } : undefined,
            );
            rerender();
        } catch (err) {
            showToast(errorMessage(err, 'Import failed'), { type: 'error' });
        } finally {
            state.folderImportBusy = false;
            importButton.textContent = 'Import';
            updateButtons();
        }
    });

    const updateButtons = () => {
        const path = pathInput.value.trim();
        const preview = state.folderImportPreview;
        previewButton.disabled = state.folderImportBusy || !path;
        importButton.disabled =
            state.folderImportBusy ||
            !preview ||
            preview.path !== path ||
            preview.would_import + preview.duplicates === 0;
    };

    pathInput.addEventListener('input', () => {
        state.folderImportPath = pathInput.value;
        state.folderImportPreview = null;
        renderFolderImportPreviewSummary(summary, null);
        updateButtons();
    });
    pathInput.addEventListener('keydown', (event) => {
        if (event.key === 'Enter') {
            event.preventDefault();
            previewButton.click();
        }
    });

    const pathRow = document.createElement('div');
    pathRow.className = 'settings-path-control settings-folder-import-path';
    pathRow.append(pathInput, previewButton, importButton);
    wrap.append(pathRow, summary);
    updateButtons();
    return wrap;
}

function renderFolderImportPreviewSummary(
    target: HTMLElement,
    preview: FolderImportPreview | null,
): void {
    target.replaceChildren();
    target.classList.toggle('settings-note-error', Boolean(preview && preview.failed > 0));
    if (!preview) {
        target.textContent = 'Use an absolute server path, then preview before importing.';
        return;
    }
    target.append(document.createTextNode(folderImportPreviewText(preview)));
    appendFolderImportErrors(target, preview.errors);
}

function folderImportPreviewText(preview: FolderImportPreview): string {
    if (preview.files === 0 && preview.failed === 0) {
        return 'No supported book files found';
    }
    const parts = [
        `${preview.files} supported ${preview.files === 1 ? 'file' : 'files'}`,
        `${preview.would_import} new`,
    ];
    appendDuplicateSummary(parts, preview.duplicates, preview.trashed);
    if (preview.calibre_books > 0) {
        parts.push(
            `${preview.calibre_books} Calibre ${preview.calibre_books === 1 ? 'folder' : 'folders'}`,
        );
    }
    if (preview.skipped > 0) parts.push(`${preview.skipped} skipped`);
    if (preview.failed > 0) parts.push(`${preview.failed} failed`);
    return parts.join(' · ');
}

function folderImportResultText(result: {
    imported: number;
    duplicates: number;
    trashed: number;
    restored: number;
    skipped: number;
    failed: number;
    warnings: number;
}): string {
    if (result.imported === 0 && result.duplicates === 0 && result.failed === 0) {
        return 'No new files to import';
    }
    const parts: string[] = [];
    if (result.imported) parts.push(`${result.imported} imported`);
    if (result.restored) parts.push(`${result.restored} restored from Trash`);
    appendDuplicateSummary(parts, result.duplicates, result.trashed);
    if (result.skipped) parts.push(`${result.skipped} skipped`);
    if (result.warnings) parts.push(`${result.warnings} warnings`);
    if (result.failed) parts.push(`${result.failed} failed`);
    return parts.join(' · ');
}

function appendFolderImportErrors(target: HTMLElement, errors?: string[]): void {
    if (!errors || errors.length === 0) return;
    const detail = document.createElement('div');
    detail.className = 'settings-folder-import-errors';
    detail.textContent = errors[0];
    if (errors.length > 1) {
        detail.textContent += ` (${errors.length - 1} more)`;
    }
    target.append(document.createElement('br'), detail);
}

// Decimal (SI) units, matching how disk sizes are advertised. Free space from
// the server is in bytes; a negative value means "unknown".
function formatBytes(bytes: number): string {
    if (!Number.isFinite(bytes) || bytes < 0) return 'unknown';
    if (bytes < 1000) return `${bytes} B`;
    const units = ['KB', 'MB', 'GB', 'TB', 'PB'];
    let value = bytes / 1000;
    let unit = 0;
    while (value >= 1000 && unit < units.length - 1) {
        value /= 1000;
        unit += 1;
    }
    return `${value.toFixed(value >= 100 ? 0 : 1)} ${units[unit]}`;
}

function scanSummaryText(result: StorageScanResult): string {
    if (result.imported === 0 && result.duplicates === 0 && result.failed === 0) {
        return 'No new files to import';
    }
    const parts: string[] = [];
    if (result.imported) parts.push(`${result.imported} imported`);
    if (result.restored) parts.push(`${result.restored} restored from Trash`);
    appendDuplicateSummary(parts, result.duplicates, result.trashed);
    if (result.failed) parts.push(`${result.failed} failed`);
    return parts.join(' · ');
}

function appendDuplicateSummary(parts: string[], duplicates: number, trashed: number): void {
    const liveDuplicates = Math.max(0, duplicates - trashed);
    if (liveDuplicates) parts.push(`${liveDuplicates} already in library`);
    if (trashed) {
        parts.push(`${trashed} ${trashed === 1 ? 'duplicate' : 'duplicates'} currently in Trash`);
    }
}

function incomingFolderToggle(state: StorageState, rerender: () => void): HTMLElement {
    const status = state.status;
    const enabledToggle = createToggle({
        ariaLabel: 'Enable incoming folder',
        checked: Boolean(status?.ingest.enabled),
        onChange: (enabled) => {
            const previous = Boolean(status?.ingest.enabled);
            enabledToggle.el.disabled = true;
            void saveIncomingSettings(
                state,
                { enabled },
                rerender,
                'Failed to save storage settings',
            )
                .catch(() => {
                    enabledToggle.setChecked(previous);
                })
                .finally(() => {
                    enabledToggle.el.disabled = false;
                });
        },
    });
    return enabledToggle.el;
}

function incomingFolderControl(state: StorageState, rerender: () => void): HTMLElement {
    const status = state.status;
    if (!status) return document.createElement('div');

    const wrap = document.createElement('div');
    wrap.className = 'settings-incoming-control';
    wrap.classList.toggle('is-disabled', !status.ingest.enabled);

    const pathInput = document.createElement('input');
    pathInput.type = 'text';
    pathInput.value = status.ingest.path;
    pathInput.className = 'settings-secret-input settings-path-input';
    pathInput.setAttribute('aria-label', 'Incoming folder');
    pathInput.spellcheck = false;

    const saveButton = buttonEl('settings-btn', 'Save', () => {
        void savePath();
    });
    const syncSaveButton = () => {
        saveButton.disabled = pathInput.value.trim() === status.ingest.path;
    };
    async function savePath(): Promise<void> {
        const path = pathInput.value.trim();
        if (!path) {
            showToast('Incoming folder path is required', { type: 'error' });
            pathInput.focus();
            return;
        }
        saveButton.disabled = true;
        try {
            await saveIncomingSettings(state, { path }, rerender, 'Failed to save incoming folder');
        } finally {
            syncSaveButton();
        }
    }
    pathInput.addEventListener('input', syncSaveButton);
    pathInput.addEventListener('keydown', (event) => {
        if (event.key === 'Enter') {
            event.preventDefault();
            void savePath();
        }
    });
    syncSaveButton();

    // Scan now is an explicit one-shot import — the UI twin of `polka ingest`.
    // It works even when automatic watching is off (the button below), so it
    // doubles as the manual-import path.
    const scanButton = buttonEl('settings-btn', 'Scan now', async () => {
        scanButton.disabled = true;
        scanButton.textContent = 'Scanning…';
        try {
            const result = await scanIncomingFolder();
            state.status = result.storage;
            state.loaded = true;
            showToast(scanSummaryText(result), result.failed > 0 ? { type: 'error' } : undefined);
            rerender();
        } catch (err) {
            showToast(errorMessage(err, 'Scan failed'), { type: 'error' });
            scanButton.disabled = false;
            scanButton.textContent = 'Scan now';
        }
    });

    const pathRow = document.createElement('div');
    pathRow.className = 'settings-path-control';
    pathRow.append(
        pathInput,
        createCopyButton(
            () => pathInput.value,
            () => pathInput.select(),
        ),
        saveButton,
        scanButton,
    );

    const deleteRow = incomingDeleteSourcesToggle(state, rerender);

    const note = textEl(
        'div',
        status.ingest.last_error ? 'settings-note settings-note-error' : 'settings-note',
        ingestStatusText(status),
    );
    note.classList.add('settings-storage-note');

    wrap.append(pathRow, deleteRow, note);
    return wrap;
}

function incomingDeleteSourcesToggle(state: StorageState, rerender: () => void): HTMLElement {
    const status = state.status;
    const row = document.createElement('div');
    row.className = 'settings-inline-option';
    row.classList.toggle('is-disabled', !status?.ingest.enabled);

    const toggle = createToggle({
        ariaLabel: 'Delete source files after import',
        checked: Boolean(status?.ingest.delete_sources),
        onChange: (delete_sources) => {
            const previous = Boolean(status?.ingest.delete_sources);
            toggle.el.disabled = true;
            void saveIncomingSettings(
                state,
                { delete_sources },
                rerender,
                'Failed to save storage settings',
            )
                .catch(() => {
                    toggle.setChecked(previous);
                })
                .finally(() => {
                    toggle.el.disabled = !state.status?.ingest.enabled;
                });
        },
    });
    toggle.el.disabled = !status?.ingest.enabled;

    row.append(textEl('span', 'settings-inline-option-label', 'Delete after import'), toggle.el);
    return row;
}

async function saveIncomingSettings(
    state: StorageState,
    ingest: { enabled?: boolean; delete_sources?: boolean; path?: string },
    rerender: () => void,
    fallback: string,
): Promise<void> {
    try {
        state.status = await saveAdminStorageStatus({ ingest });
        state.loaded = true;
        rerender();
    } catch (err) {
        showToast(errorMessage(err, fallback), { type: 'error' });
        throw err;
    }
}

function ingestStatusText(status: AdminStorageStatus): string {
    const ingest = status.ingest;
    if (!ingest.enabled) {
        return 'Automatic import is off — use Scan now to import manually';
    }
    if (!ingest.reachable) {
        return 'Folder not found — check the path or mount';
    }
    const parts = [
        `${ingest.pending} pending`,
        ingest.running ? 'watching for new files' : 'not watching',
    ];
    if (ingest.last_error) parts.push(`last error: ${ingest.last_error}`);
    return parts.join(' · ');
}
