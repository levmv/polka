import { icon } from '../icons';

export type SelectOption = {
    value: string;
    label: string;
};

export type ManagedSelect = {
    el: HTMLButtonElement;
    getValue(): string;
    setValue(value: string): void;
    destroy(): void;
};

export type SelectOptions = {
    options: SelectOption[];
    value: string;
    onChange?: (value: string) => void;
    ariaLabel?: string;
    className?: string;
};

// Tracks the single open select so opening another (or a menu) closes it.
let openSelect: { close(): void } | null = null;

// createSelect builds a floating choice control: a trigger showing the
// current value + a down chevron, opening a floating listbox whose selected
// option carries a trailing check. Keyboard-navigable; reuses the floating-layer
// look (`.floating-menu`). Works inside a modal — Escape/Tab close only the
// listbox (handled on the listbox root, which stops them reaching the modal).
export function createSelect(opts: SelectOptions): ManagedSelect {
    let value = opts.value;

    const trigger = document.createElement('button');
    trigger.type = 'button';
    trigger.className = opts.className ? `select-trigger ${opts.className}` : 'select-trigger';
    trigger.setAttribute('aria-haspopup', 'listbox');
    trigger.setAttribute('aria-expanded', 'false');
    if (opts.ariaLabel) trigger.setAttribute('aria-label', opts.ariaLabel);

    const labelSpan = document.createElement('span');
    labelSpan.className = 'select-trigger-label';
    trigger.appendChild(labelSpan);
    trigger.insertAdjacentHTML('beforeend', icon('expand_more', 18, 'select-chevron'));

    const list = document.createElement('div');
    list.className = 'floating-menu select-menu';
    list.setAttribute('role', 'listbox');
    if (opts.ariaLabel) list.setAttribute('aria-label', opts.ariaLabel);
    list.hidden = true;

    const optionEls = opts.options.map((option) => {
        const button = document.createElement('button');
        button.type = 'button';
        button.className = 'menu-item select-option';
        button.setAttribute('role', 'option');
        button.dataset.value = option.value;
        button.tabIndex = -1;

        const text = document.createElement('span');
        text.className = 'select-option-label';
        text.textContent = option.label;
        button.appendChild(text);
        button.insertAdjacentHTML('beforeend', icon('check', 18, 'select-check'));

        button.addEventListener('click', () => commit(option.value));
        list.appendChild(button);
        return button;
    });

    document.body.appendChild(list);

    function syncLabel(): void {
        const current = opts.options.find((o) => o.value === value);
        labelSpan.textContent = current ? current.label : '';
        for (const el of optionEls) {
            const selected = el.dataset.value === value;
            el.setAttribute('aria-selected', String(selected));
        }
    }

    function isOpen(): boolean {
        return !list.hidden;
    }

    function open(): void {
        if (isOpen()) return;
        openSelect?.close();
        openSelect = { close };
        list.hidden = false;
        trigger.setAttribute('aria-expanded', 'true');
        position();
        document.addEventListener('pointerdown', onDocPointerDown, true);
        window.addEventListener('resize', position);
        window.addEventListener('scroll', position, true);
        const active = optionEls.find((el) => el.dataset.value === value) || optionEls[0];
        window.setTimeout(() => active?.focus({ preventScroll: true }), 0);
    }

    function close(): void {
        if (!isOpen()) return;
        list.hidden = true;
        trigger.setAttribute('aria-expanded', 'false');
        document.removeEventListener('pointerdown', onDocPointerDown, true);
        window.removeEventListener('resize', position);
        window.removeEventListener('scroll', position, true);
        if (openSelect?.close === close) openSelect = null;
        if (document.contains(trigger)) trigger.focus({ preventScroll: true });
    }

    function commit(next: string): void {
        const changed = next !== value;
        value = next;
        syncLabel();
        close();
        if (changed) opts.onChange?.(next);
    }

    function onDocPointerDown(event: PointerEvent): void {
        const target = event.target;
        if (!(target instanceof Node)) return;
        if (list.contains(target) || trigger.contains(target)) return;
        close();
    }

    function position(): void {
        const margin = 8;
        const rect = trigger.getBoundingClientRect();
        list.style.left = '0px';
        list.style.top = '0px';
        list.style.minWidth = `${Math.round(rect.width)}px`;

        const menuRect = list.getBoundingClientRect();
        const belowTop = rect.bottom + 4;
        const aboveTop = rect.top - menuRect.height - 4;
        const fitsBelow = belowTop + menuRect.height + margin <= window.innerHeight;
        const top = fitsBelow ? belowTop : Math.max(margin, aboveTop);
        const left = Math.min(
            Math.max(rect.left, margin),
            Math.max(margin, window.innerWidth - menuRect.width - margin),
        );
        list.style.left = `${Math.round(left)}px`;
        list.style.top = `${Math.round(top)}px`;
    }

    function moveFocus(delta: number): void {
        const idx = optionEls.indexOf(document.activeElement as HTMLButtonElement);
        const start = idx >= 0 ? idx : optionEls.findIndex((el) => el.dataset.value === value);
        const next = (start + delta + optionEls.length) % optionEls.length;
        optionEls[next]?.focus();
    }

    list.addEventListener('keydown', (event) => {
        switch (event.key) {
            case 'ArrowDown':
                event.preventDefault();
                moveFocus(1);
                break;
            case 'ArrowUp':
                event.preventDefault();
                moveFocus(-1);
                break;
            case 'Home':
                event.preventDefault();
                optionEls[0]?.focus();
                break;
            case 'End':
                event.preventDefault();
                optionEls[optionEls.length - 1]?.focus();
                break;
            case 'Enter':
            case ' ':
                event.preventDefault();
                (document.activeElement as HTMLButtonElement)?.click();
                break;
            case 'Escape':
                // Stop the modal's document-level Escape from also firing.
                event.preventDefault();
                event.stopPropagation();
                close();
                break;
            case 'Tab':
                event.stopPropagation();
                close();
                break;
        }
    });

    trigger.addEventListener('click', (event) => {
        event.preventDefault();
        isOpen() ? close() : open();
    });
    trigger.addEventListener('keydown', (event) => {
        if (event.key === 'ArrowDown' || event.key === 'Enter' || event.key === ' ') {
            event.preventDefault();
            open();
        }
    });

    syncLabel();

    return {
        el: trigger,
        getValue: () => value,
        setValue(next: string): void {
            value = next;
            syncLabel();
        },
        destroy(): void {
            close();
            list.remove();
        },
    };
}
