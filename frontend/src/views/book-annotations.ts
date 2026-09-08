import {
    ANNOTATION_COLORS,
    type AnnotationSort,
    annotationMatches,
    sortAnnotations,
} from '../annotations';
import { deleteAnnotation, fetchBookAnnotations, updateAnnotation } from '../api';
import { createAnnotationColorPicker } from '../components/annotation-color-picker';
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
        const editor = document.createElement('form');
        editor.className = 'book-annotation-editor';
        editor.id = `annotation-editor-${book.id}-${annotation.id}`;
        editor.hidden = true;
        quote.setAttribute('aria-controls', editor.id);
        const fields = document.createElement('fieldset');
        fields.className = 'book-annotation-fields';
        const colors = createAnnotationColorPicker(`annotation-color-${book.id}-${annotation.id}`);
        const input = document.createElement('textarea');
        input.rows = 4;
        input.maxLength = 4000;
        input.placeholder = 'Add a note';
        input.setAttribute('aria-label', 'Note');
        const actions = textEl('div', 'book-annotation-actions', '');
        const save = button('Save', 'detail-action detail-action-primary');
        save.type = 'submit';
        const cancel = button('Cancel', 'detail-action');
        const read = button('Open in reader', 'detail-action');
        read.prepend(iconElement('menu_book', 16));
        read.disabled = !asset?.can_read;
        const remove = button('Delete', 'book-annotation-delete');
        const feedback = textEl('p', 'book-annotation-feedback', '');
        feedback.setAttribute('role', 'status');
        const deletion = textEl('div', 'book-annotation-deletion', '');
        deletion.hidden = true;
        const confirm = button('Delete highlight', 'detail-action book-annotation-delete');
        const keep = button('Keep highlight', 'detail-action');
        deletion.append(textEl('span', '', 'Delete this highlight and its note?'), confirm, keep);
        actions.append(save, cancel, read, remove);
        fields.append(colors.el, input, actions, deletion);
        editor.append(fields, feedback);
        item.append(quote, note, meta, editor);

        function display(): void {
            item.style.setProperty('--annotation-color', ANNOTATION_COLORS[annotation.color]);
            note.textContent = annotation.note ?? '';
            note.hidden = !editor.hidden || !annotation.note;
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
        function close(): void {
            editor.hidden = true;
            quote.setAttribute('aria-expanded', 'false');
            feedback.textContent = '';
            deletion.hidden = true;
            display();
            quote.focus({ preventScroll: true });
        }
        quote.addEventListener('click', () => {
            if (!editor.hidden) {
                input.focus();
                return;
            }
            input.value = annotation.note ?? '';
            colors.setValue(annotation.color);
            editor.hidden = false;
            quote.setAttribute('aria-expanded', 'true');
            note.hidden = true;
            input.focus({ preventScroll: true });
            editor.scrollIntoView({ block: 'nearest' });
        });
        cancel.addEventListener('click', close);
        remove.addEventListener('click', () => {
            deletion.hidden = false;
            keep.focus();
        });
        keep.addEventListener('click', () => {
            deletion.hidden = true;
            remove.focus();
        });
        confirm.addEventListener('click', async () => {
            fields.disabled = true;
            feedback.textContent = 'Deleting…';
            try {
                await deleteAnnotation(annotation.asset_id, annotation.id);
                if (abort.signal.aborted) return;
                rows = rows.filter((row) => row.id !== annotation.id);
                items.delete(annotation.id);
                refresh(false);
                if (rows.length) search.focus({ preventScroll: true });
                else {
                    const heading = el.parentElement?.querySelector<HTMLElement>('.detail-title');
                    if (heading) {
                        heading.tabIndex = -1;
                        heading.focus({ preventScroll: true });
                    }
                }
            } catch (error) {
                if (!abort.signal.aborted)
                    feedback.textContent = errorMessage(error, 'Could not delete highlight.');
            } finally {
                fields.disabled = false;
            }
        });
        async function persist(): Promise<boolean> {
            if (abort.signal.aborted || fields.disabled) return false;
            const changes: { note?: string; color?: Annotation['color'] } = {};
            if (input.value !== (annotation.note ?? '')) changes.note = input.value;
            if (colors.getValue() !== annotation.color) changes.color = colors.getValue();
            if (!Object.keys(changes).length) return true;
            fields.disabled = true;
            feedback.textContent = 'Saving…';
            try {
                annotation = await updateAnnotation(annotation.asset_id, annotation.id, changes);
                if (abort.signal.aborted) return false;
                rows = rows.map((row) => (row.id === annotation.id ? annotation : row));
                display();
                feedback.textContent = '';
                return true;
            } catch (error) {
                if (!abort.signal.aborted)
                    feedback.textContent = errorMessage(error, 'Could not save highlight.');
                return false;
            } finally {
                fields.disabled = false;
            }
        }
        editor.addEventListener('submit', async (event) => {
            event.preventDefault();
            if (fields.disabled) return;
            if (await persist()) {
                close();
                refresh(false);
                if (list.contains(quote)) quote.focus({ preventScroll: true });
                else search.focus({ preventScroll: true });
            }
        });
        input.addEventListener('keydown', (event) => {
            if (event.key === 'Enter' && (event.ctrlKey || event.metaKey)) {
                event.preventDefault();
                save.click();
            }
        });
        read.addEventListener('click', async () => {
            if (await persist())
                window.location.assign(
                    `/read/asset/${annotation.asset_id}#annotation=${annotation.id}`,
                );
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
