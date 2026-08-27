import { escapeHtml } from '../dom';

export type TextListSuggestion = {
    value: string;
    label?: string;
    meta?: string;
    search?: string;
    data?: unknown;
};

export type TextListAutocompleteOptions = {
    load: (query: string) => Promise<TextListSuggestion[]>;
    delay?: number;
    minQueryLength?: number;
    className?: string;
    emptyText?: string;
    replacement?: (suggestion: TextListSuggestion) => string;
    isDelimiter?: (value: string, index: number) => boolean;
    onPick?: (suggestion: TextListSuggestion) => void;
};

export type TextListAutocompleteController = {
    close: () => void;
};

let textListID = 0;

// A real text input stays authoritative. The dropdown only replaces the list
// segment that currently holds the caret, preserving whole-line copy/paste and
// normal text editing.
export function attachTextListAutocomplete(
    input: HTMLInputElement,
    options: TextListAutocompleteOptions,
): TextListAutocompleteController {
    const root = document.createElement('div');
    root.className = ['text-list-ac-wrap', options.className || ''].filter(Boolean).join(' ');
    input.parentNode?.insertBefore(root, input);
    root.appendChild(input);

    const listID = `text-list-ac-${++textListID}`;
    const list = document.createElement('div');
    list.id = listID;
    list.className = 'text-list-ac-list';
    list.setAttribute('role', 'listbox');
    list.hidden = true;
    root.appendChild(list);

    input.setAttribute('autocomplete', 'off');
    input.setAttribute('role', 'combobox');
    input.setAttribute('aria-autocomplete', 'list');
    input.setAttribute('aria-controls', listID);
    input.setAttribute('aria-expanded', 'false');
    input.setAttribute('aria-haspopup', 'listbox');

    const delay = options.delay ?? 150;
    const minQueryLength = options.minQueryLength ?? 1;
    let timer = 0;
    let blurTimer = 0;
    let requestID = 0;
    let items: TextListSuggestion[] = [];
    let highlighted = -1;
    const scrollParent = input.closest<HTMLElement>('.modal-body');
    const isDelimiter =
        options.isDelimiter ||
        ((value: string, index: number) => {
            return value[index] === ',';
        });

    const activeSegment = () => {
        const value = input.value;
        const caret = input.selectionStart ?? value.length;
        let start = 0;
        for (let i = Math.max(0, caret - 1); i >= 0; i--) {
            if (isDelimiter(value, i)) {
                start = i + 1;
                break;
            }
        }
        let end = value.length;
        for (let i = caret; i < value.length; i++) {
            if (isDelimiter(value, i)) {
                end = i;
                break;
            }
        }
        const raw = value.slice(start, end);
        return { start, end, token: raw.trim() };
    };

    const close = () => {
        window.clearTimeout(timer);
        window.clearTimeout(blurTimer);
        requestID += 1;
        list.hidden = true;
        list.innerHTML = '';
        input.setAttribute('aria-expanded', 'false');
        input.removeAttribute('aria-activedescendant');
        items = [];
        highlighted = -1;
    };

    const syncActive = () => {
        const buttons = Array.from(list.querySelectorAll<HTMLButtonElement>('.text-list-ac-item'));
        buttons.forEach((button, index) => {
            const active = index === highlighted;
            button.classList.toggle('active', active);
            button.setAttribute('aria-selected', active ? 'true' : 'false');
            if (active) {
                input.setAttribute('aria-activedescendant', button.id);
                button.scrollIntoView({ block: 'nearest' });
            }
        });
        if (highlighted < 0) input.removeAttribute('aria-activedescendant');
    };

    const syncPosition = () => {
        const rect = input.getBoundingClientRect();
        const top = rect.bottom + 2;
        list.style.left = `${rect.left}px`;
        list.style.top = `${top}px`;
        list.style.width = `${rect.width}px`;
        list.style.maxHeight = `${Math.max(96, Math.min(220, window.innerHeight - top - 8))}px`;
    };

    const render = () => {
        if (items.length === 0) {
            close();
            return;
        }
        syncPosition();
        list.innerHTML = items
            .map((item, index) => {
                const label = item.label || item.value;
                const meta = item.meta
                    ? `<span class="text-list-ac-meta">${escapeHtml(item.meta)}</span>`
                    : '';
                return `
                    <button id="${listID}-item-${index}" class="text-list-ac-item" type="button" role="option" aria-selected="${index === highlighted ? 'true' : 'false'}" data-index="${index}">
                        <span class="text-list-ac-primary">${escapeHtml(label)}</span>${meta}
                    </button>
                `;
            })
            .join('');
        list.hidden = false;
        input.setAttribute('aria-expanded', 'true');
        syncActive();
    };

    const pick = (item: TextListSuggestion) => {
        const segment = activeSegment();
        const replacement = options.replacement?.(item) ?? item.value;
        const prefix = input.value.slice(0, segment.start);
        const suffix = input.value.slice(segment.end);
        const lead = segment.start === 0 ? '' : ' ';
        const nextSegment = `${lead}${replacement}`;
        input.value = `${prefix}${nextSegment}${suffix}`;
        const caret = prefix.length + nextSegment.length;
        input.setSelectionRange(caret, caret);
        input.focus();
        close();
        input.dispatchEvent(new Event('input', { bubbles: true }));
        options.onPick?.(item);
    };

    const load = async (initialHighlight = 0) => {
        const { token } = activeSegment();
        if (token.length < minQueryLength) {
            close();
            return;
        }
        const currentRequestID = ++requestID;
        try {
            const suggestions = await options.load(token);
            if (requestID !== currentRequestID) return;
            const normalizedToken = token.toLocaleLowerCase();
            items = suggestions
                .filter((item) => item.value.trim() !== '')
                .filter((item) => item.value.toLocaleLowerCase() !== normalizedToken);
            if (items.length === 0) {
                highlighted = -1;
            } else if (initialHighlight < 0) {
                highlighted = items.length - 1;
            } else {
                highlighted = Math.min(initialHighlight, items.length - 1);
            }
            render();
        } catch {
            if (requestID === currentRequestID) close();
        }
    };

    const scheduleLoad = () => {
        window.clearTimeout(blurTimer);
        if (document.activeElement !== input) {
            close();
            return;
        }
        window.clearTimeout(timer);
        timer = window.setTimeout(() => {
            void load();
        }, delay);
    };

    input.addEventListener('input', scheduleLoad);
    input.addEventListener('focus', () => {
        window.clearTimeout(blurTimer);
    });
    input.addEventListener('click', () => {
        if (!list.hidden) syncActive();
    });
    input.addEventListener('keydown', (event) => {
        if (event.key === 'Escape' && !list.hidden) {
            event.preventDefault();
            event.stopPropagation();
            close();
            return;
        }
        if (event.key === 'Tab' && !list.hidden) {
            close();
            return;
        }
        if (event.key === 'ArrowDown') {
            if (list.hidden) {
                event.preventDefault();
                void load(0);
                return;
            }
            if (items.length === 0) return;
            event.preventDefault();
            highlighted = (highlighted + 1) % items.length;
            syncActive();
            return;
        }
        if (event.key === 'ArrowUp' && list.hidden) {
            event.preventDefault();
            void load(-1);
            return;
        }
        if (event.key === 'ArrowUp' && items.length > 0) {
            event.preventDefault();
            highlighted = (highlighted - 1 + items.length) % items.length;
            syncActive();
            return;
        }
        if (event.key === 'Enter' && !list.hidden && highlighted >= 0) {
            event.preventDefault();
            pick(items[highlighted]);
        }
    });

    list.addEventListener('mousedown', (event) => {
        const button =
            event.target instanceof Element
                ? event.target.closest<HTMLButtonElement>('.text-list-ac-item')
                : null;
        if (!button) return;
        event.preventDefault();
        const index = Number(button.dataset.index);
        const item = items[index];
        if (item) pick(item);
    });
    list.addEventListener('mousemove', (event) => {
        const button =
            event.target instanceof Element
                ? event.target.closest<HTMLButtonElement>('.text-list-ac-item')
                : null;
        if (!button) return;
        highlighted = Number(button.dataset.index);
        syncActive();
    });
    input.addEventListener('blur', () => {
        blurTimer = window.setTimeout(() => {
            if (document.activeElement !== input) close();
        }, 150);
    });
    scrollParent?.addEventListener('scroll', close, { passive: true });

    return { close };
}
