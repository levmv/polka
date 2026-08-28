import { textEl } from '../dom';
import { errorMessage } from '../errors';
import { icon, iconElement } from '../icons';
import { openModal } from '../modal';
import { showToast } from '../toast';

export type AsyncLoadState = {
    loaded: boolean;
    loading: boolean;
    loadError: string;
};

let settingsRowLabelSequence = 0;

// Keep failures stable until explicit retry; rerendering immediately would
// create a request loop while the server is unreachable.
export function renderAsyncSection(
    state: AsyncLoadState,
    opts: {
        target: HTMLElement;
        load: () => Promise<void>;
        rerender: () => void;
        errorFallback: string;
        isReady?: () => boolean;
    },
): boolean {
    if (state.loading) {
        opts.target.append(loadingNote());
        return true;
    }
    if (state.loadError) {
        opts.target.append(
            retryableErrorNote(state.loadError, () => {
                state.loadError = '';
                opts.rerender();
            }),
        );
        return true;
    }

    if (!state.loaded) {
        state.loading = true;
        opts.target.append(loadingNote());
        opts.load()
            .then(() => {
                state.loaded = true;
            })
            .catch((err) => {
                state.loadError = errorMessage(err, opts.errorFallback);
            })
            .finally(() => {
                state.loading = false;
                opts.rerender();
            });
        return true;
    }

    if (opts.isReady && !opts.isReady()) return true;
    return false;
}

// settingsRow lays out one preference: a label (with optional muted description
// below) on the left and its control on the right. Rows are hairline-separated
// by CSS.
export function settingsRow(
    labelText: string,
    description: string,
    control: HTMLElement,
    opts: {
        rowClass?: string;
        labelAccessory?: HTMLElement;
        controlHidden?: boolean;
    } = {},
): HTMLElement {
    const row = document.createElement('div');
    row.className = 'settings-row';
    if (opts.rowClass) row.classList.add(opts.rowClass);

    const text = document.createElement('div');
    text.className = 'settings-row-text';
    const label = textEl('div', 'settings-row-label', labelText);
    if (opts.labelAccessory) {
        const heading = document.createElement('div');
        heading.className = 'settings-row-heading';
        heading.append(label, opts.labelAccessory);
        text.appendChild(heading);
    } else {
        text.appendChild(label);
    }
    if (description) {
        text.appendChild(textEl('div', 'settings-row-desc', description));
    }

    const controlWrap = document.createElement('div');
    controlWrap.className = 'settings-row-control';
    controlWrap.appendChild(control);
    controlWrap.hidden = Boolean(opts.controlHidden);

    if (isDirectFormControl(control) && !hasAccessibleName(control)) {
        label.id = `settings-row-label-${++settingsRowLabelSequence}`;
        control.setAttribute('aria-labelledby', label.id);
    }

    row.append(text, controlWrap);
    return row;
}

function isDirectFormControl(
    control: HTMLElement,
): control is HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement {
    return (
        control instanceof HTMLInputElement ||
        control instanceof HTMLSelectElement ||
        control instanceof HTMLTextAreaElement
    );
}

function hasAccessibleName(
    control: HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement,
): boolean {
    return Boolean(
        control.getAttribute('aria-label') ||
            control.getAttribute('aria-labelledby') ||
            control.labels?.length,
    );
}

// Disclosure is one-way: folding a row back would only undo the click that opened
// it, and the panel is rebuilt folded on the next visit anyway. Callers pass
// `revealed` so a row holding a non-default value is never hidden in the first place.
export function settingsReveal(
    label: string,
    content: HTMLElement,
    revealed: boolean,
): HTMLElement {
    const wrap = document.createElement('div');
    wrap.className = 'settings-reveal';
    if (revealed) {
        wrap.append(content);
        return wrap;
    }
    const button = buttonEl('settings-reveal-btn', label, () => {
        button.remove();
        wrap.append(content);
        // The trigger disappears, so move focus into the revealed content.
        content.querySelector<HTMLElement>('input, button, select, textarea')?.focus();
    });
    button.append(iconElement('expand_more', 16));
    wrap.append(button);
    return wrap;
}

export function createReadonlyCopyControl(
    label: string,
    value: string,
    opts: { inputClass?: string; copyLabel?: string } = {},
): HTMLElement {
    const row = document.createElement('div');
    row.className = 'settings-opds-row';

    const input = document.createElement('input');
    input.type = 'text';
    input.readOnly = true;
    input.value = value;
    input.className = ['settings-secret-input', opts.inputClass || ''].filter(Boolean).join(' ');
    input.setAttribute('aria-label', label);
    input.autocomplete = 'off';
    input.spellcheck = false;

    row.append(
        input,
        createCopyButton(
            () => input.value,
            () => input.select(),
            opts.copyLabel || `Copy ${label.toLowerCase()}`,
        ),
    );
    return row;
}

export function createReadonlyCopyField(
    label: string,
    value: string,
    opts: { inputClass?: string; copyLabel?: string } = {},
): HTMLElement {
    const field = document.createElement('div');
    field.className = 'settings-connection-field';
    field.append(
        textEl('div', 'settings-connection-label', label),
        createReadonlyCopyControl(label, value, opts),
    );
    return field;
}

export function settingsItemRow(opts: {
    name: string;
    meta: string;
    actions?: readonly HTMLElement[];
    rowClass?: string;
    actionsClass?: string;
}): HTMLElement {
    const row = document.createElement('div');
    row.className = 'settings-item-row';
    if (opts.rowClass) row.classList.add(opts.rowClass);

    const main = document.createElement('div');
    main.className = 'settings-item-main';
    main.append(
        textEl('div', 'settings-item-name', opts.name),
        textEl('div', 'settings-item-meta', opts.meta),
    );
    row.appendChild(main);

    if (opts.actions?.length) {
        const actions = document.createElement('div');
        actions.className = 'settings-item-actions';
        if (opts.actionsClass) actions.classList.add(opts.actionsClass);
        actions.append(...opts.actions);
        row.appendChild(actions);
    }
    return row;
}

// A copy-to-clipboard icon button that briefly flips to a checkmark on success
// and toasts on failure. getValue is read at click time so the latest field
// value is copied.
export function createCopyButton(
    getValue: () => string,
    onFail?: () => void,
    label = 'Copy',
): HTMLButtonElement {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'settings-icon-btn settings-copy-btn';
    button.setAttribute('aria-label', label);
    button.title = label;
    button.innerHTML = icon('content_copy', 18);

    let revert: number | undefined;
    button.addEventListener('click', async () => {
        try {
            await copyToClipboard(getValue());
            button.innerHTML = icon('check', 18);
            button.classList.add('is-copied');
            window.clearTimeout(revert);
            revert = window.setTimeout(() => {
                button.innerHTML = icon('content_copy', 18);
                button.classList.remove('is-copied');
            }, 1400);
        } catch {
            showToast('Copy failed', { type: 'error' });
            onFail?.();
        }
    });
    return button;
}

async function copyToClipboard(value: string): Promise<void> {
    if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(value);
        return;
    }

    const input = document.createElement('textarea');
    input.value = value;
    input.style.position = 'fixed';
    input.style.left = '-9999px';
    document.body.appendChild(input);
    input.focus();
    input.select();
    const copied = document.execCommand('copy');
    input.remove();
    if (!copied) throw new Error('copy failed');
}

// openFormModal stacks a small form on top of the settings modal. The submit
// button lives in the footer but drives the body <form> via its `form`
// attribute, so Enter submits too. onSubmit returns true to close, or false to
// keep the form open after surfacing an inline error.
export function openFormModal(opts: {
    title: string;
    submitLabel: string;
    fields: HTMLElement;
    focus?: HTMLElement;
    danger?: boolean;
    onSubmit: (setError: (message: string) => void) => Promise<boolean>;
}): void {
    const formId = `settings-form-${Math.random().toString(36).slice(2, 8)}`;

    const status = document.createElement('div');
    status.className = 'settings-status';
    status.setAttribute('role', 'status');

    const form = document.createElement('form');
    form.id = formId;
    form.append(opts.fields, status);

    const cancel = buttonEl('btn-confirm-cancel', 'Cancel');
    cancel.setAttribute('data-modal-close', '');

    const submit = buttonEl(
        opts.danger ? 'btn-confirm btn-confirm-danger' : 'btn-confirm',
        opts.submitLabel,
        undefined,
        'submit',
    );
    submit.setAttribute('form', formId);

    const { modal } = openModal({
        title: opts.title,
        body: form,
        bodyClass: 'settings-submodal-body',
        modalClass: 'modal-flow settings-submodal',
        actions: [cancel, submit],
    });

    const setError = (message: string) => setStatus(status, message, Boolean(message));

    form.addEventListener('submit', async (event) => {
        event.preventDefault();
        submit.disabled = true;
        setError('');
        try {
            if (await opts.onSubmit(setError)) {
                modal.close();
            }
        } finally {
            submit.disabled = false;
        }
    });

    modal.open(opts.focus);
}

// openInfoModal is a read-only stacked modal (one dismiss button), used for the
// shown-once app-password secret.
export function openInfoModal(title: string, body: HTMLElement, focus?: HTMLElement): void {
    const { modal } = openModal({
        title,
        body,
        bodyClass: 'settings-submodal-body',
        modalClass: 'modal-flow settings-submodal',
        actions: '<button class="btn-confirm" type="button" data-modal-close>Done</button>',
    });
    modal.open(focus);
}

export function loadingNote(): HTMLElement {
    return textEl('div', 'settings-note', 'Loading…');
}

export function errorNote(message: string): HTMLElement {
    return textEl('div', 'settings-note settings-note-error', message);
}

// A failed section is a dead end until the modal is reopened, so its message
// carries the way out with it.
function retryableErrorNote(message: string, onRetry: () => void): HTMLElement {
    const note = errorNote(message);
    note.append(document.createTextNode(' · '), inlineSettingsButton('Retry', onRetry));
    return note;
}

export function buttonEl(
    className: string,
    text: string,
    onClick?: (event: MouseEvent) => void | Promise<void>,
    type: 'button' | 'submit' = 'button',
): HTMLButtonElement {
    const button = document.createElement('button');
    button.type = type;
    button.className = className;
    button.textContent = text;
    if (onClick) button.addEventListener('click', onClick);
    return button;
}

export function inlineSettingsButton(
    text: string,
    onClick: (event: MouseEvent) => void | Promise<void>,
): HTMLButtonElement {
    const button = buttonEl('settings-inline-btn', text, onClick);
    return button;
}

export function fieldGroup(): HTMLDivElement {
    const group = document.createElement('div');
    group.className = 'settings-submodal-fields';
    return group;
}

export function makeInput(type: string, autocomplete: string): HTMLInputElement {
    const input = document.createElement('input');
    input.type = type;
    input.autocomplete = autocomplete as HTMLInputElement['autocomplete'];
    input.required = true;
    input.className = 'settings-input';
    return input;
}

function setStatus(target: HTMLElement, message: string, isError: boolean): void {
    target.textContent = message;
    target.classList.toggle('error', isError);
}
