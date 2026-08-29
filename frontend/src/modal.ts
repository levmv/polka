import { escapeHtml } from './dom';
import {
    newEntryID,
    type OverlayEntry,
    type PolkaHistoryState,
    readEntryID,
    readOverlayEntry,
} from './history-state';
import { icon } from './icons';

export type ModalCloseReason = 'api' | 'backdrop' | 'close-button' | 'escape' | 'history';

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
    // Add a same-URL history entry so Back dismisses through beforeClose
    // without navigating the underlying route.
    history?: OverlayEntry;
};

type ModalState = {
    controller: ManagedModal;
    options: ModalOptions;
    returnFocus: HTMLElement | null;
    history: OwnedOverlayEntry | null;
};

type OwnedOverlayEntry = {
    entryID: string;
    overlay: OverlayEntry;
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
            state.history = options.history ? enterOverlayEntry(options.history) : null;
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
            if (guardedDismissal === state) guardedDismissal = null;
            if (confirmedDismissal === state) confirmedDismissal = null;
            state.history = null;
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
    state.history = null;

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
    finishDismissal(state, reason);
}

// A deliberate dismissal consumes a current overlay entry through Back, then
// popstate closes the modal. A Back that already consumed the entry closes it
// directly.
function finishDismissal(state: ModalState, reason: ModalCloseReason): void {
    if (state.history && currentOverlayIs(state.history)) {
        confirmedDismissal = state;
        window.history.back();
        return;
    }
    state.controller.close(reason);
}

// The modal whose entry a Back is currently consuming, so a guard that has
// already been answered is not asked again when that Back arrives.
let confirmedDismissal: ModalState | null = null;

function currentOverlayIs(entry: OwnedOverlayEntry): boolean {
    return (
        readEntryID(window.history.state) === entry.entryID &&
        readOverlayEntry(window.history.state)?.kind === entry.overlay.kind
    );
}

// The entry stands for the page with this modal over it, so it carries what
// that page's own entry carried — its position, the page it was pushed from,
// the library held for it — under an identity of its own.
function enterOverlayEntry(overlay: OverlayEntry): OwnedOverlayEntry {
    const currentOverlay = readOverlayEntry(window.history.state);
    const currentID = readEntryID(window.history.state);
    if (
        currentOverlay &&
        currentID &&
        currentOverlay.kind === overlay.kind &&
        currentOverlay.target === overlay.target
    ) {
        return {
            entryID: currentID,
            overlay: currentOverlay,
        };
    }
    const base = window.history.state;
    const entryID = newEntryID();
    const entry: PolkaHistoryState = {
        ...(base && typeof base === 'object' ? (base as PolkaHistoryState) : {}),
        polkaEntryID: entryID,
        polkaOverlay: overlay,
        polkaOverlayOriginID: readEntryID(base) ?? undefined,
    };
    window.history.pushState(entry, '');
    return { entryID, overlay };
}

type PendingOverlayRestore = {
    entry: OwnedOverlayEntry;
    promise: Promise<void>;
    resolve: () => void;
};

let pendingOverlayRestore: PendingOverlayRestore | null = null;

// Back already left the overlay entry, so return to the exact entry the browser
// kept in Forward instead of manufacturing a replacement with pushState.
function restoreOverlayEntry(entry: OwnedOverlayEntry): Promise<void> {
    if (currentOverlayIs(entry)) return Promise.resolve();
    if (pendingOverlayRestore?.entry.entryID === entry.entryID) {
        return pendingOverlayRestore.promise;
    }
    let resolve!: () => void;
    const promise = new Promise<void>((done) => {
        resolve = done;
    });
    pendingOverlayRestore = { entry, promise, resolve };
    window.history.forward();
    return promise;
}

function continueOverlayRestore(): boolean {
    const pending = pendingOverlayRestore;
    if (!pending) return false;
    if (!currentOverlayIs(pending.entry)) {
        window.history.forward();
        return true;
    }
    pendingOverlayRestore = null;
    pending.resolve();
    return true;
}

function openHistoryModal(): ModalState | null {
    for (let i = modalStack.length - 1; i >= 0; i--) {
        if (modalStack[i].history) return modalStack[i];
    }
    return null;
}

// Back landed off an overlay entry. Returns true when the modal layer owns this
// move and the router must stay out of it: dismissing a modal is not a
// navigation, and must not resume, detach, or destroy a route.
export function dismissOverlayOnPopstate(): boolean {
    if (continueOverlayRestore()) return true;
    const owner = openHistoryModal();
    if (!owner?.history) return false;
    if (confirmedDismissal === owner) {
        confirmedDismissal = null;
        guardedDismissal = null;
        owner.controller.close('history');
        return true;
    }
    const entryWasConsumed = !currentOverlayIs(owner.history);

    // Keep the editor entry in place while dismissing a child above it.
    const top = modalStack[modalStack.length - 1];
    if (top !== owner) {
        if (entryWasConsumed) {
            void restoreOverlayEntry(owner.history).then(() => attemptClose(top, 'history'));
        } else {
            void attemptClose(top, 'history');
        }
        return true;
    }
    if (guardedDismissal === owner) {
        if (entryWasConsumed) void restoreOverlayEntry(owner.history);
        return true;
    }

    const decision = owner.options.beforeClose?.('history') ?? true;
    if (typeof decision === 'boolean') {
        if (decision) {
            if (entryWasConsumed) owner.controller.close('history');
            else finishDismissal(owner, 'history');
        } else if (entryWasConsumed) {
            void restoreOverlayEntry(owner.history);
        }
        return true;
    }

    const restored = entryWasConsumed ? restoreOverlayEntry(owner.history) : Promise.resolve();
    guardedDismissal = owner;
    void Promise.all([decision, restored]).then(([proceed]) => {
        if (guardedDismissal !== owner) return;
        guardedDismissal = null;
        if (!proceed || !owner.controller.isOpen()) return;
        finishDismissal(owner, 'history');
    });
    return true;
}

let guardedDismissal: ModalState | null = null;

// The overlay is still the same layer, but it is showing something else now —
// the editor moved to the next book. Forward must come back to what it became.
export function updateOverlayEntry(overlay: OverlayEntry): void {
    const owner = openHistoryModal();
    if (!owner?.history || !currentOverlayIs(owner.history)) return;
    const base = window.history.state as PolkaHistoryState | null;
    const state = { ...(base ?? {}), polkaOverlay: overlay };
    window.history.replaceState(state, '');
    owner.history = {
        ...owner.history,
        overlay,
    };
}

const overlayReopeners = new Map<string, (overlay: OverlayEntry) => void>();

// How an overlay comes back when Forward returns to its entry. Without this the
// entry would be a dead step that changes nothing.
export function registerOverlayReopen(kind: string, reopen: (overlay: OverlayEntry) => void): void {
    overlayReopeners.set(kind, reopen);
}

export function reopenOverlay(overlay: OverlayEntry): boolean {
    const owner = openHistoryModal();
    if (owner?.history && currentOverlayIs(owner.history)) return true;
    const reopen = overlayReopeners.get(overlay.kind);
    if (!reopen) return false;
    reopen(overlay);
    return true;
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
