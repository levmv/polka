import { uploadBook } from './api';
import { bookURL } from './book-list-context';
import { notifyCatalogChanged } from './catalog-events';
import { errorMessage } from './errors';
import { navigateApp } from './router';
import { showToast } from './toast';
import type { BookImportResult, CurrentUser } from './types';

export interface UploadFailure {
    name: string;
    message: string;
}

let uploadingBooks = false;

export function initSidebarUpload(currentUserPromise: Promise<CurrentUser>): void {
    const input = document.getElementById('book-upload-input') as HTMLInputElement | null;
    const btn = document.getElementById('book-upload-btn') as HTMLButtonElement | null;
    const uploadWrap = document.getElementById('sidebar-upload');
    if (!input || !btn || !uploadWrap) return;

    currentUserPromise
        .then((me) => {
            if (me.role !== 'admin' && me.role !== 'member') {
                uploadWrap.hidden = true;
                return;
            }
            uploadWrap.hidden = false;
            wireSidebarUpload(input, btn);
        })
        .catch(() => {
            uploadWrap.hidden = true;
        });
}

function wireSidebarUpload(input: HTMLInputElement, btn: HTMLButtonElement): void {
    let dragDepth = 0;
    let internalDrag = false;

    const clearDropState = () => {
        dragDepth = 0;
        document.querySelector('.app-main')?.classList.remove('library-drop-active');
    };

    btn.addEventListener('click', () => input.click());
    input.addEventListener('change', () => {
        const files = Array.from(input.files || []);
        input.value = '';
        if (files.length > 0) {
            void importBookFiles(files, acceptedUploadExtensions(input));
        }
    });

    document.addEventListener('dragstart', () => {
        internalDrag = true;
        clearDropState();
    });
    document.addEventListener('dragend', () => {
        internalDrag = false;
        clearDropState();
    });
    document.addEventListener('dragenter', (event) => {
        if (!isExternalFileDrag(event, internalDrag)) return;
        event.preventDefault();
        dragDepth++;
        document.querySelector('.app-main')?.classList.add('library-drop-active');
    });
    document.addEventListener('dragover', (event) => {
        if (!isExternalFileDrag(event, internalDrag)) return;
        event.preventDefault();
    });
    document.addEventListener('dragleave', (event) => {
        if (!isExternalFileDrag(event, internalDrag)) return;
        dragDepth = Math.max(0, dragDepth - 1);
        if (dragDepth === 0) {
            document.querySelector('.app-main')?.classList.remove('library-drop-active');
        }
    });
    document.addEventListener('drop', (event) => {
        if (internalDrag) {
            event.preventDefault();
            internalDrag = false;
            clearDropState();
            return;
        }
        if (!isExternalFileDrag(event, false)) return;
        event.preventDefault();
        clearDropState();
        const files = Array.from(event.dataTransfer?.files || []);
        if (files.length > 0) {
            void importBookFiles(files, acceptedUploadExtensions(input));
        }
    });
}

async function importBookFiles(files: File[], acceptedExtensions: string[]): Promise<void> {
    if (uploadingBooks) return;

    const accepted = files.filter((file) => isAcceptedBookUpload(file, acceptedExtensions));
    const rejected = files
        .filter((file) => !isAcceptedBookUpload(file, acceptedExtensions))
        .map((file) => ({ name: file.name, message: 'Unsupported book format.' }));

    if (accepted.length === 0) {
        reportUploadResult([], [], rejected);
        return;
    }

    uploadingBooks = true;
    updateUploadButton();

    const imported: BookImportResult[] = [];
    const duplicates: BookImportResult[] = [];
    const failures: UploadFailure[] = [...rejected];
    try {
        for (const file of accepted) {
            try {
                const result = await uploadBook(file);
                if (result.status === 'duplicate') {
                    duplicates.push(result);
                } else {
                    imported.push(result);
                }
            } catch (error: unknown) {
                failures.push({ name: file.name, message: errorMessage(error) });
            }
        }
        reportUploadResult(imported, duplicates, failures);
        if (imported.length > 0 || duplicates.length > 0) {
            // An import adds books nobody can place in a sequence the reader is
            // part-way through, so it reports coarse through the one channel
            // rather than keeping a private one of its own.
            notifyCatalogChanged();
        }
    } finally {
        uploadingBooks = false;
        updateUploadButton();
    }
}

function reportUploadResult(
    imported: BookImportResult[],
    duplicates: BookImportResult[],
    failures: UploadFailure[],
): void {
    showUploadToast(imported, duplicates, failures);
}

function showUploadToast(
    imported: BookImportResult[],
    duplicates: BookImportResult[],
    failures: UploadFailure[],
): void {
    const total = imported.length + duplicates.length + failures.length;
    if (total === 1 && failures.length === 0) {
        const result = imported[0] || duplicates[0];
        const label =
            result.status === 'duplicate'
                ? 'Already in library'
                : result.status === 'restored'
                  ? 'Restored'
                  : 'Imported';
        showToast(`${label}: ${result.book.title}`, {
            action: {
                label: 'Open',
                onClick: () => {
                    navigateApp(bookURL(result.book.id));
                },
            },
        });
        return;
    }

    if (total === 1 && failures.length === 1) {
        showToast(`${failures[0].name}: ${failures[0].message}`, { type: 'error' });
        return;
    }

    const parts = [];
    const restoredCount = imported.filter((result) => result.status === 'restored').length;
    const importedCount = imported.length - restoredCount;
    if (importedCount > 0) parts.push(`Imported ${importedCount}`);
    if (restoredCount > 0) parts.push(`Restored ${restoredCount}`);
    if (duplicates.length > 0) parts.push(`Duplicates ${duplicates.length}`);
    if (failures.length > 0) parts.push(`Failed ${failures.length}`);
    showToast(parts.join(', ') || 'No books imported', {
        type: failures.length > 0 ? 'error' : 'success',
    });
}

function acceptedUploadExtensions(input: HTMLInputElement): string[] {
    return input.accept
        .split(',')
        .map((token) => token.trim().toLowerCase())
        .filter((token) => token.startsWith('.'))
        .sort((a, b) => b.length - a.length);
}

function isAcceptedBookUpload(file: File, acceptedExtensions: string[]): boolean {
    const name = file.name.toLowerCase();
    return acceptedExtensions.some((ext) => name.endsWith(ext));
}

function isExternalFileDrag(event: DragEvent, internalDrag: boolean): boolean {
    if (internalDrag) return false;

    const transfer = event.dataTransfer;
    if (!transfer) return false;

    const items = Array.from(transfer.items || []);
    if (items.length > 0) {
        return items.some((item) => item.kind === 'file');
    }

    return Array.from(transfer.types || []).includes('Files');
}

function updateUploadButton(): void {
    const btn = document.getElementById('book-upload-btn') as HTMLButtonElement | null;
    if (!btn) return;
    btn.disabled = uploadingBooks;
}
