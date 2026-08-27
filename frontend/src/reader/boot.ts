export function bootReader(init: () => void): void {
    document.addEventListener('DOMContentLoaded', () => {
        document.body.classList.add('reader-shell');
        document.querySelector<HTMLElement>('.app-main')?.classList.add('app-main--reader');
        init();
    });
}
