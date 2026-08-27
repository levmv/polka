import { createShelf, updateShelf, validateSearchQuery } from './api';
import { formField } from './dom';
import { errorMessage } from './errors';
import { openModal } from './modal';
import { showToast } from './toast';
import type { CurrentUser, Shelf, ShelfKind } from './types';

type CreateShelfDialogOptions = {
    currentUser: CurrentUser;
    kind: ShelfKind;
    initialName?: string;
    initialQuery?: string;
    defaultShared?: boolean;
};

type EditShelfDialogOptions = {
    currentUser: CurrentUser;
    shelf: Shelf;
};

export function openCreateShelfDialog(opts: CreateShelfDialogOptions): Promise<Shelf | null> {
    const initialQuery = opts.initialQuery ?? opts.initialName ?? '';
    const initialName =
        opts.initialName ?? (opts.kind === 'query' ? defaultQueryShelfName(initialQuery) : '');
    return openShelfDialog({
        mode: 'create',
        currentUser: opts.currentUser,
        kind: opts.kind,
        initialName,
        initialQuery,
        defaultShared: opts.defaultShared === true,
    });
}

export function openEditShelfDialog(opts: EditShelfDialogOptions): Promise<Shelf | null> {
    return openShelfDialog({
        mode: 'edit',
        currentUser: opts.currentUser,
        shelf: opts.shelf,
        kind: opts.shelf.kind,
        initialName: opts.shelf.name,
        initialQuery: opts.shelf.query || '',
        defaultShared: opts.shelf.visibility === 'shared',
    });
}

type ShelfDialogState = {
    mode: 'create' | 'edit';
    currentUser: CurrentUser;
    kind: ShelfKind;
    initialName: string;
    initialQuery: string;
    defaultShared: boolean;
    shelf?: Shelf;
};

function openShelfDialog(opts: ShelfDialogState): Promise<Shelf | null> {
    return new Promise((resolve) => {
        const formId = `shelf-create-${Math.random().toString(36).slice(2, 8)}`;
        const canShare = opts.currentUser.role === 'admin' || opts.currentUser.role === 'member';
        const canChangeVisibility =
            canShare &&
            (opts.mode === 'create' ||
                Boolean(opts.shelf && opts.shelf.owner_id === opts.currentUser.id));

        const fields = document.createElement('div');
        fields.className = 'settings-submodal-fields';

        const name = document.createElement('input');
        name.type = 'text';
        name.className = 'settings-input';
        name.autocomplete = 'off';
        name.value = opts.initialName;
        fields.append(formField('Name', name));

        const query = document.createElement('textarea');
        let queryValid = opts.kind !== 'query';
        let queryValidationTimer: number | undefined;
        let queryValidationRun = 0;
        const queryStatus = document.createElement('div');
        queryStatus.className = 'settings-field-hint shelf-query-status';
        queryStatus.setAttribute('role', 'status');

        if (opts.kind === 'query') {
            query.className = 'settings-input shelf-query-input';
            query.autocomplete = 'off';
            query.spellcheck = false;
            query.rows = 3;
            query.value = opts.initialQuery;
            const queryField = formField('Search query', query);
            queryField.append(queryStatus);
            fields.append(queryField);
        }

        const visibility = canChangeVisibility ? visibilityRadios(opts.defaultShared) : null;
        if (visibility) {
            fields.append(visibilityField(visibility.el));
        }

        const status = document.createElement('div');
        status.className = 'settings-status';
        status.setAttribute('role', 'status');

        const form = document.createElement('form');
        form.id = formId;
        form.append(fields, status);

        const cancel = document.createElement('button');
        cancel.type = 'button';
        cancel.className = 'btn-confirm-cancel';
        cancel.textContent = 'Cancel';
        cancel.setAttribute('data-modal-close', '');

        const submit = document.createElement('button');
        submit.type = 'submit';
        submit.className = 'btn-confirm';
        submit.textContent = opts.mode === 'create' ? 'Create shelf' : 'Save';
        submit.setAttribute('form', formId);

        const syncSubmit = () => {
            submit.disabled = name.value.trim() === '' || (opts.kind === 'query' && !queryValid);
        };

        const setQueryStatus = (message: string, error: boolean) => {
            queryStatus.textContent = message;
            queryStatus.classList.toggle('error', error);
            queryStatus.hidden = message === '';
        };

        const validateCurrentQuery = async (): Promise<boolean> => {
            if (opts.kind !== 'query') return true;
            if (queryValidationTimer !== undefined) {
                window.clearTimeout(queryValidationTimer);
                queryValidationTimer = undefined;
            }
            const raw = query.value.trim();
            const run = ++queryValidationRun;
            queryValid = false;
            syncSubmit();

            if (!raw) {
                setQueryStatus('Search query is required', true);
                return false;
            }

            setQueryStatus('', false);
            try {
                const result = await validateSearchQuery(raw);
                if (settled || run !== queryValidationRun) return false;
                queryValid = result.valid;
                setQueryStatus(result.valid ? '' : result.error || 'Search query is invalid', true);
                syncSubmit();
                return result.valid;
            } catch (err) {
                if (settled || run !== queryValidationRun) return false;
                queryValid = false;
                setQueryStatus(errorMessage(err, 'Failed to validate search query'), true);
                syncSubmit();
                return false;
            }
        };

        const scheduleQueryValidation = () => {
            if (opts.kind !== 'query') return;
            queryValid = false;
            syncSubmit();
            if (queryValidationTimer !== undefined) {
                window.clearTimeout(queryValidationTimer);
            }
            queryValidationTimer = window.setTimeout(() => {
                queryValidationTimer = undefined;
                void validateCurrentQuery();
            }, 180);
        };

        let settled = false;
        const finish = (shelf: Shelf | null) => {
            if (settled) return;
            settled = true;
            if (queryValidationTimer !== undefined) window.clearTimeout(queryValidationTimer);
            resolve(shelf);
            modal.close();
        };

        const { modal } = openModal({
            title: dialogTitle(opts.mode, opts.kind),
            body: form,
            bodyClass: 'settings-submodal-body',
            modalClass: 'modal-flow settings-submodal',
            actions: [cancel, submit],
            onClose: () => {
                if (!settled) {
                    settled = true;
                    if (queryValidationTimer !== undefined) {
                        window.clearTimeout(queryValidationTimer);
                    }
                    resolve(null);
                }
            },
        });

        name.addEventListener('input', syncSubmit);
        if (opts.kind === 'query') {
            query.addEventListener('input', scheduleQueryValidation);
            void validateCurrentQuery();
        } else {
            syncSubmit();
        }

        form.addEventListener('submit', async (event) => {
            event.preventDefault();
            const shelfName = name.value.trim();
            const shelfQuery = opts.kind === 'query' ? query.value.trim() : '';
            if (!shelfName) {
                setError(status, 'Name is required');
                name.focus();
                return;
            }
            if (opts.kind === 'query' && !shelfQuery) {
                setError(status, 'Search query is required');
                query.focus();
                return;
            }
            if (opts.kind === 'query' && !(await validateCurrentQuery())) {
                query.focus();
                return;
            }

            submit.disabled = true;
            setError(status, '');
            try {
                const shelf =
                    opts.mode === 'create'
                        ? await createShelf({
                              name: shelfName,
                              kind: opts.kind,
                              query: shelfQuery,
                              shared: visibility?.shared(),
                          })
                        : await updateShelf(opts.shelf!.id, {
                              name: shelfName,
                              query: opts.kind === 'query' ? shelfQuery : undefined,
                              shared: visibility?.shared(),
                          });
                finish(shelf);
            } catch (err) {
                const fallback =
                    opts.mode === 'create' ? 'Create shelf failed' : 'Shelf update failed';
                showToast(errorMessage(err, fallback), { type: 'error' });
                syncSubmit();
            }
        });

        modal.open(name);
        name.select();
    });
}

function dialogTitle(mode: 'create' | 'edit', kind: ShelfKind): string {
    if (mode === 'create') return kind === 'query' ? 'Save search' : 'New shelf';
    return kind === 'query' ? 'Edit saved search' : 'Edit shelf';
}

function defaultQueryShelfName(query: string): string {
    const raw = query.trim();
    if (!raw) return '';

    const qualified = raw.match(/^(author|series|tag|title):(.*)$/);
    if (qualified) {
        return simpleSearchValueName(qualified[2]);
    }
    if (raw.includes(':')) {
        return '';
    }
    return simpleFreeTextName(raw);
}

function simpleSearchValueName(rawValue: string): string {
    const value = rawValue.trim();
    if (!value) return '';
    if (value.startsWith('"')) {
        return simpleQuotedName(value);
    }
    if (/[\s":]/.test(value)) return '';
    return value;
}

function simpleFreeTextName(value: string): string {
    if (value.startsWith('"')) {
        return simpleQuotedName(value);
    }
    if (value.includes('"')) return '';
    return value;
}

function simpleQuotedName(value: string): string {
    const parsed = readSimpleQuotedValue(value);
    if (parsed === null || parsed.rest.trim() !== '') return '';
    return parsed.value.trim();
}

function readSimpleQuotedValue(input: string): { value: string; rest: string } | null {
    let value = '';
    for (let i = 1; i < input.length; i++) {
        if (input[i] === '"') {
            return { value, rest: input.slice(i + 1) };
        }
        value += input[i];
    }
    return null;
}

function visibilityRadios(defaultShared: boolean): { el: HTMLElement; shared(): boolean } {
    const group = document.createElement('div');
    group.className = 'shelf-visibility-radios';

    const name = `shelf-visibility-${Math.random().toString(36).slice(2, 8)}`;
    const personal = radio(name, 'Personal', 'personal', !defaultShared);
    const shared = radio(name, 'Shared', 'shared', defaultShared);
    group.append(personal.label, shared.label);

    return {
        el: group,
        shared: () => shared.input.checked,
    };
}

function radio(
    name: string,
    labelText: string,
    value: string,
    checked: boolean,
): { label: HTMLLabelElement; input: HTMLInputElement } {
    const label = document.createElement('label');
    label.className = 'shelf-visibility-option';

    const input = document.createElement('input');
    input.type = 'radio';
    input.name = name;
    input.value = value;
    input.checked = checked;

    const text = document.createElement('span');
    text.textContent = labelText;
    label.append(input, text);
    return { label, input };
}

function visibilityField(control: HTMLElement): HTMLElement {
    const wrap = document.createElement('div');
    wrap.className = 'settings-field shelf-visibility-field';
    wrap.append(control);
    return wrap;
}

function setError(status: HTMLElement, message: string): void {
    status.textContent = message;
    status.classList.toggle('error', Boolean(message));
}
