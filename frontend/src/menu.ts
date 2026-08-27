import { clamp } from './dom';

export type MenuItem = {
    label: string;
    action: () => void;
    disabled?: boolean;
};

export type ManagedMenu = {
    open(): void;
    close(): void;
    isOpen(): boolean;
    destroy(): void;
};

type MenuOptions = {
    onOpen?: () => void;
    onClose?: () => void;
};

let openMenu: ManagedMenu | null = null;
let openMenuRoot: HTMLElement | null = null;
let openMenuTrigger: HTMLElement | null = null;
let documentListenersAttached = false;

export function createMenu(
    trigger: HTMLElement,
    items: MenuItem[],
    options: MenuOptions = {},
): ManagedMenu {
    const root = document.createElement('div');
    root.className = 'floating-menu';
    root.setAttribute('role', 'menu');
    root.hidden = true;

    const buttons = items.map((item) => {
        const button = document.createElement('button');
        button.type = 'button';
        button.className = 'menu-item';
        button.setAttribute('role', 'menuitem');
        button.tabIndex = -1;
        button.textContent = item.label;
        button.disabled = Boolean(item.disabled);
        button.addEventListener('click', () => {
            if (button.disabled) return;
            controller.close();
            item.action();
        });
        root.appendChild(button);
        return button;
    });

    document.body.appendChild(root);
    trigger.setAttribute('aria-haspopup', 'menu');
    trigger.setAttribute('aria-expanded', 'false');

    const controller: ManagedMenu = {
        open(): void {
            if (controller.isOpen()) return;
            openMenu?.close();
            openMenu = controller;
            openMenuRoot = root;
            openMenuTrigger = trigger;
            root.hidden = false;
            trigger.setAttribute('aria-expanded', 'true');
            options.onOpen?.();
            positionMenu(trigger, root);
            ensureDocumentListeners();
            window.addEventListener('resize', reposition);
            window.addEventListener('scroll', reposition, true);
            window.setTimeout(() => {
                firstEnabledButton(buttons)?.focus({ preventScroll: true });
            }, 0);
        },
        close(): void {
            if (!controller.isOpen()) return;
            root.hidden = true;
            trigger.setAttribute('aria-expanded', 'false');
            window.removeEventListener('resize', reposition);
            window.removeEventListener('scroll', reposition, true);
            if (openMenu === controller) {
                openMenu = null;
                openMenuRoot = null;
                openMenuTrigger = null;
                detachDocumentListeners();
            }
            options.onClose?.();
            if (document.contains(trigger)) {
                trigger.focus({ preventScroll: true });
            }
        },
        isOpen(): boolean {
            return openMenu === controller && !root.hidden;
        },
        destroy(): void {
            controller.close();
            trigger.removeEventListener('click', handleTriggerClick);
            trigger.removeEventListener('keydown', handleTriggerKeydown);
            root.removeEventListener('keydown', handleRootKeydown);
            root.remove();
            trigger.removeAttribute('aria-haspopup');
            trigger.removeAttribute('aria-expanded');
        },
    };

    function reposition(): void {
        if (controller.isOpen()) {
            positionMenu(trigger, root);
        }
    }

    function handleTriggerClick(event: MouseEvent): void {
        event.preventDefault();
        if (controller.isOpen()) {
            controller.close();
        } else {
            controller.open();
        }
    }

    function handleTriggerKeydown(event: KeyboardEvent): void {
        if (event.key === 'Enter' || event.key === ' ' || event.key === 'ArrowDown') {
            event.preventDefault();
            controller.open();
        }
    }

    function handleRootKeydown(event: KeyboardEvent): void {
        handleMenuKeydown(event, buttons, controller);
    }

    trigger.addEventListener('click', handleTriggerClick);
    trigger.addEventListener('keydown', handleTriggerKeydown);
    root.addEventListener('keydown', handleRootKeydown);

    return controller;
}

function ensureDocumentListeners(): void {
    if (documentListenersAttached) return;
    documentListenersAttached = true;
    document.addEventListener('pointerdown', handleDocumentPointerDown);
    document.addEventListener('keydown', handleDocumentKeydown);
}

function detachDocumentListeners(): void {
    if (!documentListenersAttached) return;
    documentListenersAttached = false;
    document.removeEventListener('pointerdown', handleDocumentPointerDown);
    document.removeEventListener('keydown', handleDocumentKeydown);
}

function handleDocumentPointerDown(event: PointerEvent): void {
    if (!openMenu) return;
    const target = event.target;
    if (!(target instanceof Node)) return;
    if (openMenuRoot?.contains(target) || openMenuTrigger?.contains(target)) return;
    openMenu.close();
}

function handleDocumentKeydown(event: KeyboardEvent): void {
    if (event.key !== 'Escape') return;
    if (!openMenu) return;
    event.preventDefault();
    openMenu.close();
}

function handleMenuKeydown(
    event: KeyboardEvent,
    buttons: HTMLButtonElement[],
    controller: ManagedMenu,
): void {
    const enabled = buttons.filter((button) => !button.disabled);
    if (enabled.length === 0) return;

    const currentIndex = enabled.indexOf(document.activeElement as HTMLButtonElement);
    let nextIndex = currentIndex;

    switch (event.key) {
        case 'ArrowDown':
            event.preventDefault();
            nextIndex = currentIndex >= 0 ? (currentIndex + 1) % enabled.length : 0;
            enabled[nextIndex].focus();
            break;
        case 'ArrowUp':
            event.preventDefault();
            nextIndex =
                currentIndex >= 0
                    ? (currentIndex - 1 + enabled.length) % enabled.length
                    : enabled.length - 1;
            enabled[nextIndex].focus();
            break;
        case 'Home':
            event.preventDefault();
            enabled[0].focus();
            break;
        case 'End':
            event.preventDefault();
            enabled[enabled.length - 1].focus();
            break;
        case 'Enter':
        case ' ':
            event.preventDefault();
            if (document.activeElement instanceof HTMLButtonElement) {
                document.activeElement.click();
            }
            break;
        case 'Tab':
            controller.close();
            break;
    }
}

function firstEnabledButton(buttons: HTMLButtonElement[]): HTMLButtonElement | null {
    return buttons.find((button) => !button.disabled) || null;
}

function positionMenu(trigger: HTMLElement, root: HTMLElement): void {
    const margin = 8;
    const triggerRect = trigger.getBoundingClientRect();

    root.style.left = '0px';
    root.style.top = '0px';
    root.style.minWidth = `${Math.max(160, Math.round(triggerRect.width))}px`;

    const menuRect = root.getBoundingClientRect();
    const belowTop = triggerRect.bottom + 6;
    const aboveTop = triggerRect.top - menuRect.height - 6;
    const fitsBelow = belowTop + menuRect.height + margin <= window.innerHeight;

    const top = fitsBelow ? belowTop : Math.max(margin, aboveTop);
    const left = clamp(
        triggerRect.right - menuRect.width,
        margin,
        window.innerWidth - menuRect.width - margin,
    );

    root.style.left = `${Math.round(left)}px`;
    root.style.top = `${Math.round(top)}px`;
}
