import { listURLForContext, readBookListContextFromLocation } from '../book-list-context';

export function bootReader(init: () => void): void {
    document.addEventListener('DOMContentLoaded', () => {
        document.body.classList.add('reader-shell');
        document.querySelector<HTMLElement>('.app-main')?.classList.add('app-main--reader');
        applyCloseTarget();
        init();
    });
}

// A reader opened from a list closes back to that list, by the same ?from=
// contract the book page's Back control reads. Without one, the close control
// keeps the book page the server rendered into it.
function applyCloseTarget(): void {
    const context = readBookListContextFromLocation();
    if (!context) return;
    const close = document.querySelector<HTMLAnchorElement>('.reader-close');
    if (close) close.href = listURLForContext(context);
}
