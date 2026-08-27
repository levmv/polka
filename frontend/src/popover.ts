import { clamp } from './dom';

// Floating panel primitive: a popover anchored to a trigger with arbitrary
// content (search box, checklist, small form). Sibling to menu.ts — same
// floating/dismiss mechanics, but the body is caller-rendered rather than a
// roving-tabindex menu.

export type ManagedPopover = {
    open(): void;
    close(): void;
    isOpen(): boolean;
    // Re-anchor to the trigger; call after the panel's content (and thus size)
    // changes while open, e.g. once async data has loaded.
    reposition(): void;
    destroy(): void;
};

// render populates the (emptied) panel each time the popover opens, so callers
// can fetch fresh data per open. Return value is ignored.
type RenderFn = (panel: HTMLElement, popover: ManagedPopover) => void;

let openPopover: ManagedPopover | null = null;
let openPopoverRoot: HTMLElement | null = null;
let openPopoverTrigger: HTMLElement | null = null;
let documentListenersAttached = false;

export function createPopover(trigger: HTMLElement, render: RenderFn): ManagedPopover {
    const root = document.createElement('div');
    root.className = 'floating-panel';
    root.setAttribute('role', 'dialog');
    root.hidden = true;

    document.body.appendChild(root);
    trigger.setAttribute('aria-haspopup', 'dialog');
    trigger.setAttribute('aria-expanded', 'false');

    const controller: ManagedPopover = {
        open(): void {
            if (controller.isOpen()) return;
            openPopover?.close();
            openPopover = controller;
            openPopoverRoot = root;
            openPopoverTrigger = trigger;
            root.replaceChildren();
            render(root, controller);
            root.hidden = false;
            trigger.setAttribute('aria-expanded', 'true');
            position(trigger, root);
            ensureDocumentListeners();
            window.addEventListener('resize', reposition);
            window.addEventListener('scroll', reposition, true);
            window.setTimeout(() => focusFirst(root), 0);
        },
        close(): void {
            if (!controller.isOpen()) return;
            root.hidden = true;
            trigger.setAttribute('aria-expanded', 'false');
            window.removeEventListener('resize', reposition);
            window.removeEventListener('scroll', reposition, true);
            if (openPopover === controller) {
                openPopover = null;
                openPopoverRoot = null;
                openPopoverTrigger = null;
                detachDocumentListeners();
            }
            if (document.contains(trigger)) trigger.focus({ preventScroll: true });
        },
        isOpen(): boolean {
            return openPopover === controller && !root.hidden;
        },
        reposition(): void {
            reposition();
        },
        destroy(): void {
            controller.close();
            trigger.removeEventListener('click', handleTriggerClick);
            root.remove();
            trigger.removeAttribute('aria-haspopup');
            trigger.removeAttribute('aria-expanded');
        },
    };

    function reposition(): void {
        if (controller.isOpen()) position(trigger, root);
    }

    function handleTriggerClick(event: MouseEvent): void {
        event.preventDefault();
        if (controller.isOpen()) {
            controller.close();
        } else {
            controller.open();
        }
    }

    trigger.addEventListener('click', handleTriggerClick);
    return controller;
}

function focusFirst(panel: HTMLElement): void {
    const focusable = panel.querySelector<HTMLElement>(
        'input:not([disabled]), button:not([disabled]), [tabindex]:not([tabindex="-1"])',
    );
    focusable?.focus({ preventScroll: true });
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
    if (!openPopover) return;
    const target = event.target;
    if (!(target instanceof Node)) return;
    if (openPopoverRoot?.contains(target) || openPopoverTrigger?.contains(target)) return;
    openPopover.close();
}

function handleDocumentKeydown(event: KeyboardEvent): void {
    if (event.key !== 'Escape' || !openPopover) return;
    event.preventDefault();
    openPopover.close();
}

function position(trigger: HTMLElement, root: HTMLElement): void {
    const margin = 8;
    const triggerRect = trigger.getBoundingClientRect();

    root.style.left = '0px';
    root.style.top = '0px';

    const panelRect = root.getBoundingClientRect();
    const belowTop = triggerRect.bottom + 6;
    const aboveTop = triggerRect.top - panelRect.height - 6;
    const fitsBelow = belowTop + panelRect.height + margin <= window.innerHeight;

    const top = fitsBelow ? belowTop : Math.max(margin, aboveTop);
    const left = clamp(
        triggerRect.right - panelRect.width,
        margin,
        window.innerWidth - panelRect.width - margin,
    );

    root.style.left = `${Math.round(left)}px`;
    root.style.top = `${Math.round(top)}px`;
}
