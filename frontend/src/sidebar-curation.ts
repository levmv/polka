import { createMenu } from './menu';
import type { CurrentUser } from './types';

// Curator-only sidebar navigation is one policy even though Authors is a direct
// link while Cleanup and Trash live in the Library action menu.
export function initSidebarCuration(
    currentUserPromise: Promise<CurrentUser>,
    navigate: (href: string) => void,
): void {
    const authorsItem = document.getElementById('nav-authors')?.closest('li');
    const libraryActions = document.getElementById('nav-library-actions');

    currentUserPromise
        .then((currentUser) => {
            const canCurate = currentUser.role === 'admin' || currentUser.role === 'member';
            if (authorsItem instanceof HTMLElement) authorsItem.hidden = !canCurate;
            if (!(libraryActions instanceof HTMLButtonElement) || !canCurate) return;

            libraryActions.hidden = false;
            createMenu(libraryActions, [
                { label: 'Cleanup', action: () => navigate('/cleanup') },
                { label: 'Trash', action: () => navigate('/trash') },
            ]);
        })
        .catch(() => {
            if (authorsItem instanceof HTMLElement) authorsItem.hidden = true;
            if (libraryActions instanceof HTMLButtonElement) libraryActions.hidden = true;
        });
}
