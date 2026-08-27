// A single transient notification for command results that can't be shown at the
// action site — e.g. "Removed rebecca" after a row disappears. A new toast
// replaces the current one, matching Kontur's toast guidance. Neutral results
// stay 3s; ones with an action stay 7s; errors stay 10s and, like action
// toasts, carry a close button. Form validation belongs inline, not here.
export type ToastType = 'success' | 'error';

export type ToastOptions = {
    type?: ToastType;
    action?: { label: string; onClick: () => void };
    duration?: number;
};

let host: HTMLElement | null = null;
let current: { el: HTMLElement; timer: number } | null = null;

function ensureHost(): HTMLElement {
    if (host && document.body.contains(host)) return host;
    host = document.createElement('div');
    host.className = 'toast-host';
    host.setAttribute('aria-live', 'polite');
    document.body.appendChild(host);
    return host;
}

export function showToast(message: string, opts: ToastOptions = {}): void {
    const parent = ensureHost();
    dismissCurrent();

    const type = opts.type ?? 'success';
    const hasAction = Boolean(opts.action);
    const closeable = hasAction || type === 'error';
    const duration = opts.duration ?? (type === 'error' ? 10000 : hasAction ? 7000 : 3000);

    const toast = document.createElement('div');
    toast.className = `toast toast-${type}`;
    toast.setAttribute('role', type === 'error' ? 'alert' : 'status');

    const text = document.createElement('span');
    text.className = 'toast-text';
    text.textContent = message;
    toast.appendChild(text);

    if (opts.action) {
        const { label, onClick } = opts.action;
        const action = document.createElement('button');
        action.type = 'button';
        action.className = 'toast-action';
        action.textContent = label;
        action.addEventListener('click', () => {
            dismissCurrent();
            onClick();
        });
        toast.appendChild(action);
    }

    if (closeable) {
        const close = document.createElement('button');
        close.type = 'button';
        close.className = 'toast-close';
        close.setAttribute('aria-label', 'Dismiss');
        close.innerHTML = '&times;';
        close.addEventListener('click', () => dismissCurrent());
        toast.appendChild(close);
    }

    parent.appendChild(toast);
    requestAnimationFrame(() => toast.classList.add('toast-visible'));

    const timer = window.setTimeout(dismissCurrent, duration);
    current = { el: toast, timer };
}

function dismissCurrent(): void {
    if (!current) return;
    const { el, timer } = current;
    current = null;
    window.clearTimeout(timer);
    el.classList.remove('toast-visible');
    el.classList.add('toast-leaving');
    window.setTimeout(() => el.remove(), 180);
}
