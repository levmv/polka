import type { BookListContext } from '../book-list-context';
import type { Book } from '../types';

// The narrow bridge an editor opened over a book page uses to keep that page
// synchronized. It lives outside book-view/book-edit so Forward can reconnect
// a fresh editor without creating a runtime import cycle between the views.
export interface BookDetailHost {
    applySaved(b: Book): void;
    showBook(b: Book, listContext?: BookListContext | null): void;
    rerender(): void;
}

let activeHost: BookDetailHost | null = null;

export function registerActiveBookDetailHost(host: BookDetailHost): () => void {
    activeHost = host;
    return () => {
        if (activeHost === host) activeHost = null;
    };
}

export function activeBookDetailHost(): BookDetailHost | null {
    return activeHost;
}
