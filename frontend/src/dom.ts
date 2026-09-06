export function escapeHtml(value: unknown): string {
    if (value == null) return '';
    return String(value)
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;')
        .replace(/'/g, '&#039;');
}

export function clamp(value: number, min: number, max: number): number {
    if (max < min) return min;
    return Math.min(Math.max(value, min), max);
}

export function clampNumber(value: unknown, min: number, max: number, fallback: number): number {
    if (typeof value !== 'number' || !Number.isFinite(value)) return fallback;
    return clamp(value, min, max);
}

export function textEl<K extends keyof HTMLElementTagNameMap>(
    tag: K,
    className: string,
    text: string,
): HTMLElementTagNameMap[K] {
    const el = document.createElement(tag);
    el.className = className;
    el.textContent = text;
    return el;
}

export function formField(labelText: string, control: HTMLElement): HTMLLabelElement {
    const field = document.createElement('label');
    field.className = 'settings-field';
    const label = document.createElement('span');
    label.textContent = labelText;
    field.append(label, control);
    return field;
}

export type Debounced<TArgs extends unknown[]> = ((...args: TArgs) => void) & {
    cancel(): void;
};

export function debounce<TArgs extends unknown[], TResult>(
    func: (...args: TArgs) => TResult,
    wait: number,
): Debounced<TArgs> {
    let timeout: ReturnType<typeof setTimeout> | undefined;
    const cancel = () => {
        if (timeout === undefined) return;
        clearTimeout(timeout);
        timeout = undefined;
    };
    const debounced = (...args: TArgs) => {
        cancel();
        timeout = setTimeout(() => {
            timeout = undefined;
            void func(...args);
        }, wait);
    };
    debounced.cancel = cancel;
    return debounced;
}

// Position a fixed panel in viewport coordinates. The caller controls its width.
export function positionFloating(
    triggerRect: DOMRectReadOnly,
    root: HTMLElement,
    options: { align?: 'left' | 'right'; gap?: number } = {},
): void {
    const margin = 8;
    const gap = options.gap ?? 6;
    root.style.left = '0px';
    root.style.top = '0px';
    const panelRect = root.getBoundingClientRect();
    const belowTop = triggerRect.bottom + gap;
    const aboveTop = triggerRect.top - panelRect.height - gap;
    const fitsBelow = belowTop + panelRect.height + margin <= window.innerHeight;
    const top = fitsBelow ? belowTop : Math.max(margin, aboveTop);
    const left = clamp(
        options.align === 'left' ? triggerRect.left : triggerRect.right - panelRect.width,
        margin,
        window.innerWidth - panelRect.width - margin,
    );
    root.style.left = `${Math.round(left)}px`;
    root.style.top = `${Math.round(top)}px`;
}
