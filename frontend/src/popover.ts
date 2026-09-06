import { positionFloating } from './dom';

// A trigger-anchored panel with caller-rendered content, such as a search field
// or checklist.

export type ManagedPopover = {
    open(): void;
    close(): void;
    isOpen(): boolean;
    // Re-anchor to the trigger; call after the panel's content (and thus size)
    // changes while open, e.g. once async data has loaded.
    reposition(): void;
    destroy(): void;
};

// Content is rebuilt on each open. Async renderers handle their own completion.
type RenderFn = (panel: HTMLElement, popover: ManagedPopover) => void;

let openPopover: ManagedPopover | null = null;

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
            root.replaceChildren();
            render(root, controller);
            root.hidden = false;
            trigger.setAttribute('aria-expanded', 'true');
            positionFloating(trigger.getBoundingClientRect(), root);
            document.addEventListener('pointerdown', handleDocumentPointerDown);
            document.addEventListener('keydown', handleDocumentKeydown);
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
            openPopover = null;
            document.removeEventListener('pointerdown', handleDocumentPointerDown);
            document.removeEventListener('keydown', handleDocumentKeydown);
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
        if (controller.isOpen()) positionFloating(trigger.getBoundingClientRect(), root);
    }

    function handleTriggerClick(event: MouseEvent): void {
        event.preventDefault();
        if (controller.isOpen()) {
            controller.close();
        } else {
            controller.open();
        }
    }

    function handleDocumentPointerDown(event: PointerEvent): void {
        const target = event.target;
        if (!(target instanceof Node)) return;
        if (root.contains(target) || trigger.contains(target)) return;
        controller.close();
    }

    function handleDocumentKeydown(event: KeyboardEvent): void {
        if (event.key !== 'Escape') return;
        event.preventDefault();
        controller.close();
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
