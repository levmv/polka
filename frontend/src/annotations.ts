import type { Annotation, AnnotationColor } from './types';

export const ANNOTATION_COLORS: Record<AnnotationColor, string> = {
    yellow: '#f2d46b',
    green: '#91c9a0',
    blue: '#91bce4',
    pink: '#e5a4bb',
    purple: '#bea5e2',
};

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
    const left = cfiStart(a.cfi);
    const right = cfiStart(b.cfi);
    if (left && right) {
        for (let i = 0; i < Math.max(left.length, right.length); i++) {
            const difference = (left[i] ?? 0) - (right[i] ?? 0);
            if (difference) return difference;
        }
    } else if (left || right) {
        return left ? -1 : 1;
    } else if (a.cfi !== b.cfi) {
        return a.cfi < b.cfi ? -1 : 1;
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
