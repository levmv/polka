import { iconElement } from '../icons';
import { focusReaderSurface } from './controls';

export interface ReaderTOCPanel {
    panel: HTMLElement;
    nav: HTMLElement;
    list: HTMLOListElement;
    close(): void;
    destroy(): void;
}

export function createReaderTOCPanel(page: HTMLElement): ReaderTOCPanel | null {
    const actions = page.querySelector<HTMLElement>('.reader-actions');
    if (!actions) return null;

    const panelID = 'reader-toc-panel';
    const toggle = document.createElement('button');
    toggle.className = 'reader-toc-toggle';
    toggle.type = 'button';
    toggle.dataset.readerTocToggle = 'true';
    toggle.title = 'Contents';
    toggle.setAttribute('aria-label', 'Contents');
    toggle.setAttribute('aria-controls', panelID);
    toggle.setAttribute('aria-expanded', 'false');
    toggle.append(iconElement('menu'));

    const backdrop = document.createElement('button');
    backdrop.className = 'reader-toc-backdrop';
    backdrop.type = 'button';
    backdrop.hidden = true;
    backdrop.tabIndex = -1;
    backdrop.setAttribute('aria-label', 'Close contents');

    const panel = document.createElement('aside');
    panel.id = panelID;
    panel.className = 'reader-toc-panel';
    panel.hidden = true;
    panel.setAttribute('aria-label', 'Contents');

    const header = document.createElement('div');
    header.className = 'reader-toc-header';
    const title = document.createElement('h2');
    title.className = 'reader-toc-title';
    title.textContent = 'Contents';
    const closeButton = document.createElement('button');
    closeButton.className = 'reader-toc-close';
    closeButton.type = 'button';
    closeButton.title = 'Close contents';
    closeButton.setAttribute('aria-label', 'Close contents');
    closeButton.append(iconElement('close'));
    header.append(title, closeButton);

    const nav = document.createElement('nav');
    nav.className = 'reader-toc-nav';
    nav.setAttribute('aria-label', 'Contents');
    const list = document.createElement('ol');
    list.className = 'reader-toc-list';
    nav.append(list);
    panel.append(header, nav);

    const open = (): void => {
        page.classList.add('reader-toc-open');
        panel.hidden = false;
        backdrop.hidden = false;
        toggle.setAttribute('aria-expanded', 'true');
        panel.querySelector<HTMLElement>('.reader-toc-item, .reader-toc-close')?.focus();
    };
    const close = (): void => {
        page.classList.remove('reader-toc-open');
        panel.hidden = true;
        backdrop.hidden = true;
        toggle.setAttribute('aria-expanded', 'false');
        focusReaderSurface(page);
    };
    const handleKeydown = (event: KeyboardEvent): void => {
        if (event.key !== 'Escape' || panel.hidden) return;
        event.preventDefault();
        close();
    };

    toggle.addEventListener('click', () => (panel.hidden ? open() : close()));
    closeButton.addEventListener('click', close);
    backdrop.addEventListener('click', close);
    document.addEventListener('keydown', handleKeydown);
    actions.prepend(toggle);
    page.append(backdrop, panel);

    return {
        panel,
        nav,
        list,
        close,
        destroy(): void {
            document.removeEventListener('keydown', handleKeydown);
            page.classList.remove('reader-toc-open');
            toggle.remove();
            backdrop.remove();
            panel.remove();
        },
    };
}
