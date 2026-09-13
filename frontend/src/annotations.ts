import type { Annotation, AnnotationColor } from './types';

export const ANNOTATION_COLORS: Record<AnnotationColor, string> = {
    yellow: '#f2d46b',
    green: '#91c9a0',
    blue: '#91bce4',
    pink: '#e5a4bb',
    purple: '#bea5e2',
};

export type AnnotationChanges = { note?: string; color?: AnnotationColor };

export interface AnnotationConflict {
    kind: 'conflict';
    current: Annotation;
}

// The controls own the draft text; this owns its saved base and conflict policy.
export class AnnotationDraft {
    base: Annotation;
    conflict: AnnotationConflict | null = null;

    constructor(base: Annotation) {
        this.base = base;
    }

    changes(note: string, color: AnnotationColor, overwrite = false): AnnotationChanges | null {
        // Closing or moving away must not overwrite a known text conflict.
        if (this.conflict && !overwrite) return null;
        return annotationChanges(this.base, note, color);
    }

    rebase(conflict: AnnotationConflict, changes: AnnotationChanges) {
        this.conflict = conflict;
        this.base = conflict.current;
        // Preserve edited fields and adopt fresh values for untouched fields.
        // Subsequent saves compare against this same base and revision.
        return {
            note: changes.note ?? this.base.note ?? '',
            color: changes.color ?? this.base.color,
        };
    }
}

export function annotationChanges(
    base: Annotation,
    note: string,
    color: AnnotationColor,
): AnnotationChanges {
    return {
        ...(note !== (base.note ?? '') ? { note } : {}),
        ...(color !== base.color ? { color } : {}),
    };
}

export function annotationNoteConflicts(
    base: Annotation,
    current: Annotation,
    changes: AnnotationChanges,
): boolean {
    // Text needs an explicit choice only when both edits differ. Color is a
    // cosmetic preference: keep the local choice if edited, otherwise the saved one.
    return (
        changes.note !== undefined &&
        (current.note ?? '') !== (base.note ?? '') &&
        (current.note ?? '') !== changes.note
    );
}

export type AnnotationSort = 'position' | 'created' | 'updated';

// Compare the numeric path to the start of a CFI range. Assertions identify
// surrounding text/elements; their text (including digits and escaped commas)
// does not affect reading order. Keep this independent of the reader engine.
function cfiStart(cfi: string): number[] | null {
    if (!cfi.startsWith('epubcfi(') || !cfi.endsWith(')')) return null;
    const path = cfi.slice(8, -1).replace(/\[(?:\^.|[^\]^])*\]/g, '');
    const [parent, start = ''] = path.split(',');
    const location = parent + start;
    if (!/^\/\d+(?:(?:!\/|\/)\d+)*(?::\d+)?$/.test(location)) return null;
    const values = location.match(/\d+/g)?.map(Number) ?? [];
    if (!location.includes(':')) values.push(0);
    return values;
}

export function compareAnnotationPosition(a: Annotation, b: Annotation): number {
    if (a.asset_id !== b.asset_id) return a.asset_id - b.asset_id;
    if (a.locator.page || b.locator.page) {
        const page = (a.locator.page ?? 0) - (b.locator.page ?? 0);
        if (page) return page;
        const left = a.locator.rects?.[0];
        const right = b.locator.rects?.[0];
        if (left && right) {
            const position = right.y + right.height - left.y - left.height || left.x - right.x;
            if (position) return position;
        }
        return a.id - b.id;
    }
    const left = cfiStart(a.locator.cfi ?? '');
    const right = cfiStart(b.locator.cfi ?? '');
    if (left && right) {
        for (let i = 0; i < Math.max(left.length, right.length); i++) {
            const difference = (left[i] ?? 0) - (right[i] ?? 0);
            if (difference) return difference;
        }
    } else if (left || right) {
        return left ? -1 : 1;
    } else if (a.locator.cfi !== b.locator.cfi) {
        return (a.locator.cfi ?? '') < (b.locator.cfi ?? '') ? -1 : 1;
    }
    return a.id - b.id;
}

export function sortAnnotations(rows: Annotation[], order: AnnotationSort): Annotation[] {
    return [...rows].sort((a, b) => {
        if (order === 'created') return b.created_at - a.created_at || b.id - a.id;
        if (order === 'updated') return b.updated_at - a.updated_at || b.id - a.id;
        return compareAnnotationPosition(a, b);
    });
}

export function annotationMatches(annotation: Annotation, query: string): boolean {
    const text = `${annotation.quote}\n${annotation.note ?? ''}`.toLocaleLowerCase();
    return query
        .trim()
        .toLocaleLowerCase()
        .split(/\s+/)
        .every((term) => text.includes(term));
}
