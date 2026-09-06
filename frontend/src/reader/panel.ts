import { type IconName, iconElement } from '../icons';

export interface ReaderPanelElements {
    backdrop: HTMLButtonElement;
    panel: HTMLElement;
    toggle: HTMLButtonElement;
    closeButton: HTMLButtonElement;
}

// Callers place the toggle and own opening, closing and cleanup.
export function createReaderPanel(
    page: HTMLElement,
    options: {
        name: string;
        title: string;
        label?: string;
        closeLabel: string;
        icon?: IconName;
        toggle?: HTMLButtonElement; // Display supplies its server-rendered toggle.
    },
): ReaderPanelElements {
    const prefix = `reader-${options.name}`;
    const label = options.label ?? options.title;
    const toggle = options.toggle ?? document.createElement('button');
    toggle.classList.add('reader-panel-toggle', `${prefix}-toggle`);
    toggle.type = 'button';
    toggle.title = label;
    toggle.setAttribute(`data-${prefix}-toggle`, 'true');
    toggle.setAttribute('aria-label', label);
    toggle.setAttribute('aria-controls', `${prefix}-panel`);
    toggle.setAttribute('aria-expanded', 'false');
    if (options.icon) toggle.append(iconElement(options.icon));

    const backdrop = document.createElement('button');
    backdrop.className = 'reader-panel-backdrop';
    backdrop.type = 'button';
    backdrop.hidden = true;
    backdrop.tabIndex = -1;
    backdrop.setAttribute('aria-label', options.closeLabel);

    const panel = document.createElement('aside');
    panel.id = `${prefix}-panel`;
    panel.className = `reader-panel ${prefix}-panel`;
    panel.hidden = true;
    panel.setAttribute('aria-label', label);

    const header = document.createElement('div');
    header.className = 'reader-panel-header';
    const title = document.createElement('h2');
    title.className = 'reader-panel-title';
    title.textContent = options.title;
    const closeButton = document.createElement('button');
    closeButton.className = 'reader-panel-close';
    closeButton.type = 'button';
    closeButton.title = options.closeLabel;
    closeButton.setAttribute('aria-label', options.closeLabel);
    closeButton.append(iconElement('close'));
    header.append(title, closeButton);
    panel.append(header);
    page.append(backdrop, panel);
    return { backdrop, panel, toggle, closeButton };
}
