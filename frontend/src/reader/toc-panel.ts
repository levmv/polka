import { focusReaderSurface, revealChrome } from './chrome';
import { createReaderPanel } from './panel';

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

    const { panel, backdrop, toggle, closeButton } = createReaderPanel(page, {
        name: 'toc',
        title: 'Contents',
        closeLabel: 'Close contents',
        icon: 'menu',
    });

    const nav = document.createElement('nav');
    nav.className = 'reader-toc-nav';
    nav.setAttribute('aria-label', 'Contents');
    const list = document.createElement('ol');
    list.className = 'reader-toc-list';
    nav.append(list);
    panel.append(nav);

    const open = (): void => {
        page.classList.add('reader-toc-open');
        panel.hidden = false;
        backdrop.hidden = false;
        toggle.setAttribute('aria-expanded', 'true');
        closeButton.focus();
    };
    const close = (): void => {
        page.classList.remove('reader-toc-open');
        panel.hidden = true;
        backdrop.hidden = true;
        toggle.setAttribute('aria-expanded', 'false');
        revealChrome(page);
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
