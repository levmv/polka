import { formatAuthorList, parseAuthorList } from '../authors';
import { formatIdentifiers, parseIdentifiers } from '../identifiers';
import type { BookPatch, BookUpdate } from '../types';

export type EditFieldName = keyof BookUpdate;

export type FetchedFieldSource = {
    providerName: string;
    value: BookUpdate[EditFieldName];
};

const editFieldNames: EditFieldName[] = [
    'title',
    'sort_title',
    'authors',
    'series',
    'series_index',
    'tags',
    'language',
    'publisher',
    'date',
    'identifiers',
    'description',
];

export function readEditForm(form: HTMLFormElement): BookUpdate {
    const f = form as any;
    return {
        title: f.title.value.trim(),
        sort_title: f.sort_title.value ? f.sort_title.value.trim() : null,
        authors: f.authors.value.trim() ? f.authors.value.trim() : null,
        series: f.series.value ? f.series.value : null,
        series_index: f.series_index.value ? parseFloat(f.series_index.value) : null,
        tags: f.tags.value ? f.tags.value : null,
        description: f.description.value ? f.description.value : null,
        language: f.language.value ? f.language.value : null,
        publisher: f.publisher.value ? f.publisher.value : null,
        date: f.date.value ? f.date.value : null,
        identifiers: f.identifiers.value ? f.identifiers.value : null,
    };
}

export function stateKey(state: BookUpdate): string {
    return JSON.stringify(state);
}

export function validateTitle(form: HTMLFormElement, bookId: string): boolean {
    const titleInput = form.querySelector<HTMLInputElement>('input[name="title"]');
    const valid = !!titleInput?.value.trim();
    const error = document.getElementById(`title-error-${bookId}`);
    if (error) error.style.display = valid ? 'none' : 'block';
    return valid;
}

export function dirtyEditFields(current: BookUpdate, saved: BookUpdate): EditFieldName[] {
    return editFieldNames.filter((field) => isEditFieldDirty(field, current, saved));
}

function isEditFieldDirty(field: EditFieldName, current: BookUpdate, saved: BookUpdate): boolean {
    if (field === 'sort_title' && titleSortFollowsTitleImplicitly(current, saved)) {
        return false;
    }
    return !sameEditFieldValue(current[field], saved[field]);
}

function titleSortFollowsTitleImplicitly(current: BookUpdate, saved: BookUpdate): boolean {
    return (
        !sameEditFieldValue(current.sort_title, saved.sort_title) &&
        sameEditFieldValue(current.sort_title, current.title) &&
        sameEditFieldValue(saved.sort_title, saved.title)
    );
}

export function submitPayload(current: BookUpdate, saved: BookUpdate): BookPatch {
    const payload: BookPatch = {};
    for (const field of dirtyEditFields(current, saved)) {
        Object.assign(payload, { [field]: current[field] });
    }
    // Moving a custom sort title back to the title means "automatic". Encode
    // that intent as null so the server can remove the manual override rather
    // than persist an indistinguishable explicit string.
    if (
        'sort_title' in payload &&
        sameEditFieldValue(current.sort_title, current.title) &&
        !sameEditFieldValue(saved.sort_title, saved.title)
    ) {
        payload.sort_title = null;
    }
    return payload;
}

export function sameEditFieldValue(a: unknown, b: unknown): boolean {
    if (a == null && b == null) return true;
    return String(a ?? '').trim() === String(b ?? '').trim();
}

export function setEditFieldValue(
    form: HTMLFormElement,
    field: EditFieldName,
    value: unknown,
): void {
    const text = value == null ? '' : String(value);
    const input = form.querySelector<HTMLInputElement | HTMLTextAreaElement>(`[name="${field}"]`);
    if (input) {
        input.value = text;
        input.dispatchEvent(new Event('input', { bubbles: true }));
    }
    if (field === 'description') {
        const editor = form.querySelector<HTMLElement>('.rich-editor-content');
        if (editor) {
            editor.textContent = text;
        }
    }
}

export function syncFieldDecorations(
    form: HTMLFormElement,
    saved: BookUpdate,
    fetchedSources: Map<EditFieldName, FetchedFieldSource>,
    onRevert: (field: EditFieldName) => void,
): void {
    const current = readEditForm(form);
    for (const field of editFieldNames) {
        const el = form.querySelector<HTMLElement>(`[data-edit-field="${field}"]`);
        if (!el) continue;
        el.classList.add('edit-field');
        const dirty = isEditFieldDirty(field, current, saved);
        const source = fetchedSources.get(field);
        const fetched = dirty && !!source && sameEditFieldValue(current[field], source.value);
        if (source && !fetched) fetchedSources.delete(field);

        el.classList.toggle('is-dirty', dirty);
        el.classList.toggle('is-fetched', fetched);
        syncFieldStateControls(el, field, dirty, saved[field], onRevert);
    }
}

function syncFieldStateControls(
    el: HTMLElement,
    field: EditFieldName,
    dirty: boolean,
    revertValue: BookUpdate[EditFieldName],
    onRevert: (field: EditFieldName) => void,
): void {
    let state = el.querySelector<HTMLElement>(':scope > .edit-field-state');
    if (!state) {
        state = document.createElement('div');
        state.className = 'edit-field-state';
        const revert = document.createElement('button');
        revert.type = 'button';
        revert.className = 'edit-field-revert';
        revert.textContent = 'Revert';
        revert.addEventListener('click', (event) => {
            event.preventDefault();
            onRevert(field);
        });
        state.append(revert);
        el.appendChild(state);
    }
    state.hidden = !dirty;

    const revert = state.querySelector<HTMLButtonElement>('.edit-field-revert');
    if (revert) {
        revert.hidden = !dirty;
        const preview = editFieldValuePreview(revertValue);
        revert.title = `Revert to ${preview}`;
        revert.setAttribute('aria-label', `Revert ${field.replace('_', ' ')} to ${preview}`);
    }
}

export function editFieldValuePreview(value: unknown): string {
    const text = String(value ?? '')
        .replace(/\s+/g, ' ')
        .trim();
    if (!text) return 'empty value';
    return text.length > 160 ? `${text.slice(0, 157)}...` : text;
}

export function normalizeFormBeforeSave(form: HTMLFormElement): void {
    const authorsInput = form.querySelector<HTMLInputElement>('input[name="authors"]');
    if (authorsInput) {
        authorsInput.value = formatAuthorList(parseAuthorList(authorsInput.value));
    }
    const identifiersInput = form.querySelector<HTMLInputElement>('input[name="identifiers"]');
    if (identifiersInput) {
        identifiersInput.value = formatIdentifiers(parseIdentifiers(identifiersInput.value));
    }
}
