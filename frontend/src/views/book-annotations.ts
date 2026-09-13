import {
    ANNOTATION_COLORS,
    type AnnotationSort,
    annotationMatches,
    sortAnnotations,
} from '../annotations';
import { fetchBookAnnotations } from '../api';
import { createAnnotationEditor } from '../components/annotation-editor';
import { createSelect } from '../components/select';
import { textEl } from '../dom';
import { errorMessage } from '../errors';
import { iconElement } from '../icons';
import { createMenu } from '../menu';
import type { Annotation, Book } from '../types';

const PAGE_SIZE = 40;

export function createBookAnnotations(book: Book) {
    const abort = new AbortController();
    const el = document.createElement('section');
    el.className = 'book-annotations';
    el.hidden = true;
    el.setAttribute('aria-labelledby', `book-annotations-title-${book.id}`);

    const header = textEl('div', 'book-annotations-heading', '');
    const title = textEl('h2', '', 'Highlights & notes');
    title.id = `book-annotations-title-${book.id}`;
    const count = textEl('span', 'book-annotations-count', '');
    count.setAttribute('role', 'status');
    const heading = textEl('div', 'book-annotations-title', '');
    heading.append(title, count);
    const exportButton = button('', 'detail-action detail-action-icon');
    exportButton.setAttribute('aria-label', 'Export');
    exportButton.title = 'Export highlights';
    exportButton.append(iconElement('download', 18));

    const toolbar = textEl('div', 'book-annotations-toolbar', '');
    const searchField = textEl('div', 'book-annotations-search', '');
    const search = document.createElement('input');
    search.type = 'search';
    search.placeholder = 'Search';
    search.setAttribute('aria-label', 'Search quotes and notes');
    searchField.append(iconElement('search', 18), search);
    let order: AnnotationSort = 'position';
    const sort = createSelect({
        ariaLabel: 'Sort highlights',
        value: order,
        options: [
            { value: 'position', label: 'Book order' },
            { value: 'created', label: 'Newest first' },
            { value: 'updated', label: 'Recently edited' },
        ],
        onChange(value) {
            order = value as AnnotationSort;
            refresh();
        },
    });
    toolbar.append(searchField, sort.el, exportButton);
    header.append(heading, toolbar);

    const viewport = textEl('div', 'book-annotations-scroll', '');
    viewport.tabIndex = 0;
    viewport.setAttribute('role', 'region');
    viewport.setAttribute('aria-label', 'Highlights and notes');
    const list = document.createElement('ol');
    list.className = 'book-annotations-list';
    const status = textEl('p', 'book-annotations-status', '');
    status.setAttribute('role', 'status');
    const retry = button('Try again', 'detail-action');
    retry.hidden = true;
    const more = button('Show more highlights', 'detail-action book-annotations-more');
    more.hidden = true;
    viewport.append(list, status, retry, more);
    el.append(header, viewport);

    let rows: Annotation[] = [];
    let visible: Annotation[] = [];
    let shown = 0;
    // Keep row editors across filtering/sorting, including unsaved notes.
    const items = new Map<number, HTMLLIElement>();
    const exportMenu = createMenu(exportButton, [
        { label: 'Export all as HTML', action: () => download('html') },
        { label: 'Export all as Markdown', action: () => download('markdown') },
        { label: 'Export all as Web Annotation', action: () => download('jsonld') },
    ]);
    function download(format: string): void {
        const link = document.createElement('a');
        link.href = `/api/books/${book.id}/annotations/export?format=${format}`;
        link.download = '';
        link.click();
    }

    function appendPage(): void {
        const next = Math.min(shown + PAGE_SIZE, visible.length);
        for (const annotation of visible.slice(shown, next)) {
            let item = items.get(annotation.id);
            if (!item) {
                item = createRow(annotation);
                items.set(annotation.id, item);
            }
            list.append(item);
        }
        shown = next;
        more.hidden = shown >= visible.length;
    }

    function refresh(resetScroll = true): void {
        const scrollTop = resetScroll ? 0 : viewport.scrollTop;
        const limit = resetScroll ? PAGE_SIZE : Math.max(shown, PAGE_SIZE);
        visible = sortAnnotations(
            rows.filter((row) => annotationMatches(row, search.value)),
            order,
        );
        count.textContent = search.value.trim()
            ? `${visible.length} of ${rows.length}`
            : String(rows.length);
        exportButton.disabled = rows.length === 0;
        el.hidden = rows.length === 0;
        status.textContent = visible.length ? '' : 'No matching highlights or notes.';
        status.hidden = visible.length > 0;
        list.replaceChildren();
        shown = 0;
        do appendPage();
        while (shown < Math.min(limit, visible.length));
        viewport.scrollTop = scrollTop;
    }

    function createRow(initial: Annotation): HTMLLIElement {
        let annotation = initial;
        const asset = book.assets.find((asset) => asset.id === annotation.asset_id);
        const item = document.createElement('li');
        item.className = 'book-annotation';
        item.dataset.annotationId = String(annotation.id);
        const quote = button(annotation.quote, 'book-annotation-quote');
        quote.setAttribute('aria-expanded', 'false');
        const note = textEl('p', 'book-annotation-note', annotation.note ?? '');
        const meta = textEl('p', 'book-annotation-meta', '');
        const editor = createAnnotationEditor(initial, {
            signal: abort.signal,
            onChange(saved, previous) {
                annotation = saved;
                rows = rows.map((row) => (row.id === previous.id ? saved : row));
                items.delete(previous.id);
                items.set(saved.id, item);
                item.dataset.annotationId = String(saved.id);
                display();
            },
            onDelete(deleted) {
                rows = rows.filter((row) => row.id !== deleted.id);
                items.delete(deleted.id);
                finishEdit();
            },
            onClose: finishEdit,
            onRead: asset?.can_read
                ? (saved) =>
                      window.location.assign(`/read/asset/${saved.asset_id}#annotation=${saved.id}`)
                : undefined,
        });
        editor.el.classList.add('book-annotation-editor');
        editor.el.id = `annotation-editor-${book.id}-${initial.id}`;
        editor.el.hidden = true;
        quote.setAttribute('aria-controls', editor.el.id);
        item.append(quote, note, meta, editor.el);

        function display(): void {
            item.style.setProperty('--annotation-color', ANNOTATION_COLORS[annotation.color]);
            note.textContent = annotation.note ?? '';
            note.hidden = !editor.el.hidden || !annotation.note;
            const date = new Date(Math.max(annotation.created_at, annotation.updated_at) * 1000);
            meta.textContent = date.toLocaleDateString(undefined, {
                day: 'numeric',
                month: 'short',
                year: 'numeric',
            });
            meta.title = date.toLocaleString();
            if (book.assets.length > 1 && asset)
                meta.textContent += ` · ${asset.extension.replace('.', '').toUpperCase()}`;
        }
        function finishEdit(): void {
            editor.el.hidden = true;
            quote.setAttribute('aria-expanded', 'false');
            display();
            refresh(false);
            if (list.contains(quote)) quote.focus({ preventScroll: true });
            else if (rows.length) search.focus({ preventScroll: true });
            else {
                const heading = el.parentElement?.querySelector<HTMLElement>('.detail-title');
                if (heading) {
                    heading.tabIndex = -1;
                    heading.focus({ preventScroll: true });
                }
            }
        }
        quote.addEventListener('click', () => {
            if (editor.el.hidden) {
                editor.reset();
                editor.el.hidden = false;
                quote.setAttribute('aria-expanded', 'true');
                note.hidden = true;
                editor.el.scrollIntoView({ block: 'nearest' });
            }
            editor.input.focus({ preventScroll: true });
        });
        display();
        return item;
    }

    async function load(): Promise<void> {
        retry.hidden = true;
        status.textContent = 'Loading highlights…';
        try {
            rows = await fetchBookAnnotations(book.id, abort.signal);
            if (abort.signal.aborted) return;
            toolbar.hidden = false;
            refresh();
        } catch (error) {
            if (abort.signal.aborted) return;
            el.hidden = false;
            toolbar.hidden = true;
            exportButton.disabled = true;
            status.textContent = errorMessage(error, 'Could not load highlights.');
            retry.hidden = false;
        }
    }
    search.addEventListener('input', () => refresh());
    retry.addEventListener('click', () => void load());
    more.addEventListener('click', appendPage);
    void load();
    return {
        bookId: book.id,
        el,
        destroy(): void {
            abort.abort();
            exportMenu.destroy();
            sort.destroy();
            items.clear();
            el.remove();
        },
    };
}

function button(label: string, className: string): HTMLButtonElement {
    const el = document.createElement('button');
    el.type = 'button';
    el.className = className;
    el.textContent = label;
    return el;
}
