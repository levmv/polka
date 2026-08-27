import { escapeHtml } from './dom';
import { icon } from './icons';

export type ModalCloseReason = 'api' | 'backdrop' | 'close-button' | 'escape';

export type ManagedModal = {
    root: HTMLElement;
    open(focusTarget?: HTMLElement): void;
    close(reason?: ModalCloseReason): void;
    isOpen(): boolean;
};

export type ModalOptions = {
    closeExisting?: boolean;
    onClose?: (reason: ModalCloseReason) => void;
    onKeydown?: (event: KeyboardEvent, modal: ManagedModal) => boolean | undefined;
    // beforeClose gates user-initiated dismissal (the close button, Escape, and
    // backdrop click). Return false — or a promise resolving false — to veto the
    // close, e.g. to confirm discarding unsaved edits. Programmatic close() is
    // never gated; it is a deliberate action by the caller.
    beforeClose?: (reason: ModalCloseReason) => boolean | Promise<boolean>;
};

type ModalState = {
    controller: ManagedModal;
    options: ModalOptions;
    returnFocus: HTMLElement | null;
};

const modalStack: ModalState[] = [];

export function createModal(root: HTMLElement, options: ModalOptions = {}): ManagedModal {
    if (!root.hasAttribute('tabindex')) {
        root.setAttribute('tabindex', '-1');
    }

    const state = {} as ModalState;
    const controller: ManagedModal = {
        root,
        open(focusTarget?: HTMLElement): void {
            if (controller.isOpen()) return;
            if (options.closeExisting) {
                closeAllModals();
            }
            state.returnFocus =
                document.activeElement instanceof HTMLElement ? document.activeElement : null;
            state.controller = controller;
            state.options = options;
            if (modalStack.length === 0) {
                lockPageScroll();
            }
            modalStack.push(state);
            root.setAttribute('aria-hidden', 'false');
            ensureKeydownListener();
            window.setTimeout(() => {
                const target = focusTarget || firstFocusableElement(root) || root;
                focusElement(target, false);
            }, 0);
        },
        close(reason: ModalCloseReason = 'api'): void {
            const index = modalStack.indexOf(state);
            if (index === -1) return;
            modalStack.splice(index, 1);
            root.setAttribute('aria-hidden', 'true');
            options.onClose?.(reason);
            if (modalStack.length === 0) {
                unlockPageScroll();
            }
            if (modalStack.length === 0) {
                document.removeEventListener('keydown', handleKeydown);
                restoreFocus(state.returnFocus);
            } else {
                focusTopModal();
            }
        },
        isOpen(): boolean {
            return modalStack.includes(state);
        },
    };
    state.controller = controller;
    state.options = options;
    state.returnFocus = null;

    let backdropPointerDown = false;
    root.addEventListener('pointerdown', (event) => {
        backdropPointerDown = event.target === root;
    });

    root.addEventListener('click', (event) => {
        if (event.target === root) {
            const shouldClose = backdropPointerDown;
            backdropPointerDown = false;
            if (!shouldClose) return;
            event.preventDefault();
            void attemptClose(state, 'backdrop');
            return;
        }
        backdropPointerDown = false;
        const target = event.target;
        if (target instanceof Element && closestElement(target, '[data-modal-close]', root)) {
            event.preventDefault();
            void attemptClose(state, 'close-button');
        }
    });

    return controller;
}

// attemptClose runs the optional beforeClose guard for a user-initiated
// dismissal, then closes only if the guard allows it. A no-op if the modal has
// already left the stack (e.g. a guarded second click while a confirm is open).
async function attemptClose(state: ModalState, reason: ModalCloseReason): Promise<void> {
    if (!state.controller.isOpen()) return;
    if (state.options.beforeClose) {
        const proceed = await state.options.beforeClose(reason);
        if (!proceed || !state.controller.isOpen()) return;
    }
    state.controller.close(reason);
}

export type ModalContent = string | Node | (string | Node)[];

export type ModalSpec = ModalOptions & {
    // A plain title renders the standard `.modal-header` with an <h2>. For a
    // bespoke header (extra buttons, a toolbar) pass `header` instead, which is
    // inserted verbatim and owns its own markup.
    title?: string;
    header?: ModalContent;
    body: ModalContent;
    actions?: ModalContent;
    // The canonical close button (top-right SVG ×). On by default; pass false
    // for confirm-style dialogs, or when a custom header supplies its own.
    closeButton?: boolean;
    modalClass?: string;
    backdropClass?: string;
    bodyClass?: string;
    labelledBy?: string;
    ariaLabel?: string;
};

// openModal builds the standard modal chrome — backdrop, dialog, one canonical
// close button, header/body/actions — appends it to the document, and wires
// behaviour through createModal. It does not open the modal; the caller calls
// .open() once it has queried `root` for any inner elements it needs to wire.
export function openModal(spec: ModalSpec): { modal: ManagedModal; root: HTMLElement } {
    const {
        title,
        header,
        body,
        actions,
        closeButton = true,
        modalClass,
        backdropClass,
        bodyClass,
        labelledBy,
        ariaLabel,
        ...options
    } = spec;

    const root = document.createElement('div');
    root.className = joinClasses('modal-backdrop', backdropClass);

    const modal = document.createElement('div');
    modal.className = joinClasses('modal', modalClass);
    modal.setAttribute('role', 'dialog');
    modal.setAttribute('aria-modal', 'true');

    const titleId = labelledBy || (title ? uniqueId('modal-title') : undefined);
    if (titleId) {
        modal.setAttribute('aria-labelledby', titleId);
    } else if (ariaLabel) {
        modal.setAttribute('aria-label', ariaLabel);
    }

    if (closeButton) {
        const close = document.createElement('button');
        close.type = 'button';
        close.className = 'modal-close';
        close.setAttribute('data-modal-close', '');
        close.setAttribute('aria-label', 'Close');
        close.innerHTML = icon('close', 24);
        modal.appendChild(close);
    }

    if (header !== undefined) {
        appendContent(modal, header);
    } else if (title) {
        const head = document.createElement('div');
        head.className = 'modal-header';
        const heading = document.createElement('h2');
        if (titleId) heading.id = titleId;
        heading.textContent = title;
        head.appendChild(heading);
        modal.appendChild(head);
    }

    const bodyEl = document.createElement('div');
    bodyEl.className = joinClasses('modal-body', bodyClass);
    appendContent(bodyEl, body);
    modal.appendChild(bodyEl);

    if (actions !== undefined) {
        const footer = document.createElement('div');
        footer.className = 'modal-actions';
        appendContent(footer, actions);
        modal.appendChild(footer);
    }

    root.appendChild(modal);
    document.body.appendChild(root);

    // openModal owns the root it created, so it tears it down on close — callers
    // don't repeat root.remove(). Their onClose still runs first, for cleanup.
    const userOnClose = options.onClose;
    const controller = createModal(root, {
        ...options,
        onClose: (reason) => {
            userOnClose?.(reason);
            root.remove();
        },
    });
    return { modal: controller, root };
}

function appendContent(target: HTMLElement, content: ModalContent): void {
    const items = Array.isArray(content) ? content : [content];
    for (const item of items) {
        if (typeof item === 'string') {
            const template = document.createElement('template');
            template.innerHTML = item;
            target.append(...Array.from(template.content.childNodes));
        } else {
            target.append(item);
        }
    }
}

function joinClasses(...classes: (string | undefined)[]): string {
    return classes.filter(Boolean).join(' ');
}

function uniqueId(prefix: string): string {
    return `${prefix}-${Math.random().toString(36).slice(2, 8)}`;
}

export function closeAllModals(reason: ModalCloseReason = 'api'): void {
    modalStack
        .slice()
        .reverse()
        .forEach((state) => {
            state.controller.close(reason);
        });
}

export type ConfirmOptions = {
    title: string;
    body?: string;
    confirmLabel: string;
    cancelLabel?: string;
    danger?: boolean;
};

// confirmModal shows an explicit, user-invoked yes/no dialog and resolves true
// only if the user confirms. Escape / backdrop / cancel all resolve false, so a
// dismissed dialog is a safe no-op — the right default for destructive actions.
// It deliberately omits the corner close button: the choice is the cancel/ok
// pair.
export function confirmModal(opts: ConfirmOptions): Promise<boolean> {
    return new Promise((resolve) => {
        const cancel = document.createElement('button');
        cancel.type = 'button';
        cancel.className = 'btn-confirm-cancel';
        cancel.textContent = opts.cancelLabel || 'Cancel';

        const ok = document.createElement('button');
        ok.type = 'button';
        ok.className = opts.danger ? 'btn-confirm btn-confirm-danger' : 'btn-confirm';
        ok.textContent = opts.confirmLabel;

        let settled = false;
        const finish = (value: boolean) => {
            if (settled) return;
            settled = true;
            resolve(value);
            modal.close();
        };

        const { modal } = openModal({
            title: opts.title,
            body: opts.body ? `<p>${escapeHtml(opts.body)}</p>` : '',
            actions: [cancel, ok],
            closeButton: false,
            backdropClass: 'modal-confirm-backdrop',
            modalClass: 'modal-confirm',
            onClose: () => {
                // Closed via escape/backdrop without an explicit choice → cancel.
                if (!settled) {
                    settled = true;
                    resolve(false);
                }
            },
        });

        cancel.addEventListener('click', () => finish(false));
        ok.addEventListener('click', () => finish(true));

        modal.open(ok);
    });
}

function ensureKeydownListener(): void {
    if (modalStack.length === 1) {
        document.addEventListener('keydown', handleKeydown);
    }
}

function handleKeydown(event: KeyboardEvent): void {
    const state = modalStack[modalStack.length - 1];
    let handled = false;
    if (!state) return;
    if (state.options.onKeydown) {
        handled = Boolean(state.options.onKeydown(event, state.controller));
    }
    if (handled) return;
    if (event.key === 'Escape') {
        event.preventDefault();
        void attemptClose(state, 'escape');
        return;
    }
    if (event.key === 'Tab') {
        trapFocus(event, state.controller.root);
    }
}

function trapFocus(event: KeyboardEvent, root: HTMLElement): void {
    const items = focusableElements(root);
    if (items.length === 0) {
        event.preventDefault();
        root.focus();
        return;
    }
    const first = items[0];
    const last = items[items.length - 1];
    const active = document.activeElement;
    if (event.shiftKey && active === first) {
        event.preventDefault();
        last.focus();
    } else if (!event.shiftKey && active === last) {
        event.preventDefault();
        first.focus();
    } else if (!root.contains(active)) {
        event.preventDefault();
        first.focus();
    }
}

function firstFocusableElement(root: HTMLElement): HTMLElement | null {
    return focusableElements(root)[0] || null;
}

function focusableElements(root: HTMLElement): HTMLElement[] {
    const selector = [
        'a[href]',
        "input:not([disabled]):not([type='hidden']):not([aria-hidden])",
        'select:not([disabled]):not([aria-hidden])',
        'textarea:not([disabled]):not([aria-hidden])',
        'button:not([disabled]):not([aria-hidden])',
        'iframe',
        'embed',
        '[contenteditable]',
        "[tabindex]:not([tabindex^='-'])",
    ].join(',');
    const items = Array.from(root.querySelectorAll<HTMLElement>(selector));
    return items.filter((item) => item.offsetParent !== null || item === document.activeElement);
}

function lockPageScroll(): void {
    document.body.classList.add('modal_open');
}

function unlockPageScroll(): void {
    document.body.classList.remove('modal_open');
}

function restoreFocus(target: HTMLElement | null): void {
    if (target && document.contains(target)) {
        focusElement(target, true);
    }
}

function focusTopModal(): void {
    const state = modalStack[modalStack.length - 1];
    if (!state) return;
    const target =
        state.returnFocus &&
        document.contains(state.returnFocus) &&
        state.controller.root.contains(state.returnFocus)
            ? state.returnFocus
            : state.controller.root;
    focusElement(target, true);
}

function focusElement(target: HTMLElement, preventScroll: boolean): void {
    try {
        target.focus({ preventScroll });
    } catch (_err) {
        target.focus();
    }
}

type MatchableElement = Element & {
    msMatchesSelector?: (selector: string) => boolean;
    webkitMatchesSelector?: (selector: string) => boolean;
};

function closestElement(target: Element, selector: string, boundary: HTMLElement): Element | null {
    let node: Element | null = target;
    while (node) {
        if (elementMatches(node, selector)) return node;
        if (node === boundary) return null;
        node = node.parentElement;
    }
    return null;
}

function elementMatches(element: Element, selector: string): boolean {
    const item = element as MatchableElement;
    const matches = item.matches || item.msMatchesSelector || item.webkitMatchesSelector;
    return Boolean(matches?.call(element, selector));
}
