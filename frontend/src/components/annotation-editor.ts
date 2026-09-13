import {
    ANNOTATION_COLORS,
    AnnotationDraft,
    annotationChanges,
    annotationNoteConflicts,
} from '../annotations';
import { deleteAnnotation, saveAnnotationChanges } from '../api';
import { textEl } from '../dom';
import { errorMessage } from '../errors';
import { iconElement } from '../icons';
import type { Annotation } from '../types';
import { createAnnotationColorPicker } from './annotation-color-picker';

interface AnnotationEditorOptions {
    layout?: 'inline' | 'popover';
    signal?: AbortSignal;
    onChange(annotation: Annotation, previous: Annotation): void;
    onDelete(annotation: Annotation): void;
    onClose(): void;
    onRead?: (annotation: Annotation) => void;
    onLayout?: () => void;
}

export function createAnnotationEditor(initial: Annotation, options: AnnotationEditorOptions) {
    const compact = options.layout === 'popover';
    let draft = new AnnotationDraft(initial);
    let pending: Promise<boolean> | null = null;
    let disposed = false;
    const inactive = () => disposed || options.signal?.aborted;
    const el = document.createElement('form');
    el.className = 'annotation-editor';
    el.classList.toggle('annotation-editor-compact', compact);
    const fields = document.createElement('fieldset');
    fields.className = 'annotation-editor-fields';
    const colors = createAnnotationColorPicker(`annotation-color-${initial.id}`);
    const input = document.createElement('textarea');
    input.className = 'annotation-note-input';
    input.rows = 4;
    input.maxLength = 4000;
    input.placeholder = 'Add a note';
    input.setAttribute('aria-label', 'Note');
    const feedback = textEl('p', 'annotation-feedback', '');
    feedback.setAttribute('role', 'status');
    const saved = textEl('div', 'annotation-saved-note', '');
    const savedNote = textEl('blockquote', '', '');
    saved.append(textEl('span', '', 'Saved note'), savedNote);
    const actions = textEl('div', 'annotation-editor-actions', '');
    const submit = button('Save', 'detail-action detail-action-primary');
    submit.type = 'submit';
    const done = compact ? button('', 'annotation-editor-done') : null;
    if (done) {
        done.type = 'submit';
        done.title = 'Done';
        done.setAttribute('aria-label', 'Done');
        done.append(iconElement('check'));
        const header = textEl('div', 'annotation-editor-header', '');
        header.append(textEl('div', 'annotation-editor-quote', initial.quote), done);
        fields.append(header, input, colors.el);
    } else fields.append(colors.el, input);
    const cancel = button('Cancel', 'detail-action');
    const remove = button('Delete', 'annotation-delete');
    remove.hidden = compact;
    actions.append(submit, cancel);
    if (options.onRead) {
        const read = button('Open in reader', 'detail-action');
        read.prepend(iconElement('menu_book', 16));
        read.addEventListener('click', async () => {
            if (await save()) options.onRead?.(draft.base);
        });
        actions.append(read);
    }
    actions.append(remove);
    fields.append(feedback, saved, actions);
    el.append(fields);

    function render(): void {
        const current = draft.conflict?.current;
        saved.hidden = !current;
        savedNote.textContent = current?.note || 'No saved note.';
        if (current)
            saved.style.setProperty('--annotation-color', ANNOTATION_COLORS[current.color]);
        submit.textContent = current ? 'Overwrite' : 'Save';
        submit.setAttribute('aria-label', submit.textContent);
        submit.hidden = compact && !current;
        cancel.hidden = compact && !current;
        if (done) done.hidden = Boolean(current);
        actions.hidden = compact && !current && remove.hidden;
        options.onLayout?.();
    }

    function reset(): void {
        draft = new AnnotationDraft(draft.base);
        input.value = draft.base.note ?? '';
        colors.setValue(draft.base.color);
        feedback.textContent = '';
        render();
    }

    function accept(annotation: Annotation): void {
        const previous = draft.base;
        draft = new AnnotationDraft(annotation);
        options.onChange(annotation, previous);
    }

    function save(overwrite = false): Promise<boolean> {
        if (inactive()) return Promise.resolve(false);
        if (pending) return pending;
        const changes = draft.changes(input.value, colors.getValue(), overwrite);
        if (!changes) return Promise.resolve(false);
        if (!Object.keys(changes).length) return Promise.resolve(true);
        fields.disabled = true;
        feedback.textContent = 'Saving…';
        pending = saveAnnotationChanges(draft.base, changes, options.signal)
            .then((result) => {
                if (inactive()) return false;
                if (result.kind === 'conflict') {
                    const previous = draft.base;
                    const values = draft.rebase(result, changes);
                    input.value = values.note;
                    colors.setValue(values.color);
                    options.onChange(result.current, previous);
                    feedback.textContent = 'This note changed elsewhere.';
                    return false;
                }
                accept(result.annotation);
                reset();
                return true;
            })
            .catch((error) => {
                if (!inactive()) feedback.textContent = errorMessage(error, 'Could not save note.');
                return false;
            })
            .finally(() => {
                pending = null;
                fields.disabled = false;
                if (!inactive()) render();
            });
        return pending;
    }

    function requestDelete(): Promise<boolean> | null {
        if (inactive() || pending) return null;
        if (!window.confirm('Delete this highlight and its note?')) return null;
        remove.hidden = false;
        return removeAnnotation();
    }

    function removeAnnotation(): Promise<boolean> {
        if (inactive()) return Promise.resolve(false);
        if (pending) return pending;
        fields.disabled = true;
        feedback.textContent = 'Deleting…';
        pending = deleteAnnotation(draft.base, options.signal)
            .then((result) => {
                if (inactive()) return false;
                if (result.kind === 'conflict') {
                    const previous = draft.base;
                    const changes = annotationChanges(previous, input.value, colors.getValue());
                    const values = draft.rebase(result, changes);
                    if (!annotationNoteConflicts(previous, result.current, changes))
                        draft.conflict = null;
                    input.value = values.note;
                    colors.setValue(values.color);
                    options.onChange(result.current, previous);
                    feedback.textContent = 'This note changed elsewhere.';
                    return false;
                }
                options.onDelete(draft.base);
                return true;
            })
            .catch((error) => {
                if (!inactive())
                    feedback.textContent = errorMessage(error, 'Could not delete highlight.');
                return false;
            })
            .finally(() => {
                pending = null;
                fields.disabled = false;
                if (!inactive()) render();
            });
        return pending;
    }

    el.addEventListener('submit', async (event) => {
        event.preventDefault();
        if (!fields.disabled && (await save(true))) options.onClose();
    });
    input.addEventListener('keydown', (event) => {
        if (event.key === 'Enter' && (event.ctrlKey || event.metaKey)) {
            event.preventDefault();
            (done && !done.hidden ? done : submit).click();
        }
    });
    input.addEventListener('input', () => {
        if (!draft.conflict) feedback.textContent = '';
    });
    cancel.addEventListener('click', () => {
        reset();
        options.onClose();
    });
    remove.addEventListener('click', requestDelete);
    reset();

    return {
        el,
        input,
        save,
        reset,
        requestDelete,
        destroy(): void {
            disposed = true;
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
