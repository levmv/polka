import { Overlayer } from 'foliate-js/overlayer.js';
import { ANNOTATION_COLORS } from '../annotations';
import type { Annotation } from '../types';
import type { AnnotationAnchor, AnnotationSurface } from './annotation-surface';
import type { FoliateAnnotation, FoliateLoadDetail, FoliateViewElement } from './foliate-engine';

export function foliateAnnotationSurface(view: FoliateViewElement): AnnotationSurface {
    const annotations = new Map<number, Annotation>();
    const sections = new Map<number, number>();
    const ranges = new Map<number, Range>();
    const anchor = (id: number): AnnotationAnchor | undefined => {
        const range = ranges.get(id);
        const doc = range?.startContainer.ownerDocument;
        return range && doc
            ? { doc, getBoundingClientRect: () => range.getBoundingClientRect() }
            : undefined;
    };
    const add = async (annotation: Annotation): Promise<void> => {
        if (!annotation.locator.cfi) return;
        annotations.set(annotation.id, annotation);
        const result = await view.addAnnotation?.(foliateAnnotation(annotation));
        if (typeof result?.index === 'number') sections.set(annotation.id, result.index);
    };
    return {
        connect(handlers): void {
            view.addEventListener('draw-annotation', (event) => {
                const detail = (
                    event as CustomEvent<{
                        draw?: (func: unknown, options?: unknown) => void;
                        annotation?: FoliateAnnotation;
                        range?: Range;
                    }>
                ).detail;
                if (!detail?.draw || detail.annotation?.kind !== 'highlight' || !detail.range)
                    return;
                const id = Number(detail.annotation.id);
                ranges.set(id, detail.range.cloneRange());
                detail.draw(Overlayer.highlight, {
                    color: ANNOTATION_COLORS[annotations.get(id)?.color ?? 'yellow'],
                    padding: 1,
                });
            });
            view.addEventListener('show-annotation', (event) => {
                const detail = (event as CustomEvent<{ value?: string; range?: Range }>).detail;
                if (!detail?.range || !detail.value) return;
                const annotation = [...annotations.values()].find(
                    (row) => row.locator.cfi === detail.value,
                );
                if (!annotation) return;
                ranges.set(annotation.id, detail.range.cloneRange());
                const target = anchor(annotation.id);
                if (target) handlers.show(annotation.id, target);
            });
            view.addEventListener('create-overlay', (event) => {
                const index = (event as CustomEvent<{ index?: number }>).detail?.index;
                if (index === undefined) return;
                for (const annotation of annotations.values()) {
                    if (sections.get(annotation.id) === index) {
                        void add(annotation).catch((error) =>
                            console.error('Failed to draw annotation:', error),
                        );
                    }
                }
            });
            view.addEventListener('relocate', handlers.leave);
            const documents = new WeakSet<Document>();
            const wireDocument = (doc: Document): void => {
                if (documents.has(doc)) return;
                documents.add(doc);
                doc.addEventListener('pointerdown', handlers.leave, true);
            };
            view.addEventListener('load', (event) => {
                if (!view.isFixedLayout) ranges.clear();
                wireDocument((event as CustomEvent<FoliateLoadDetail>).detail.doc);
            });
            for (const content of view.renderer?.getContents?.() ?? []) {
                if (content.doc) wireDocument(content.doc);
            }
        },
        add,
        async remove(annotation): Promise<void> {
            annotations.delete(annotation.id);
            sections.delete(annotation.id);
            ranges.delete(annotation.id);
            await view.deleteAnnotation?.(foliateAnnotation(annotation));
        },
        async show(annotation): Promise<void> {
            if (view.showAnnotation) await view.showAnnotation(foliateAnnotation(annotation));
            else if (annotation.locator.cfi) await view.goTo(annotation.locator.cfi);
        },
        anchor,
        atRange(doc, range): number | undefined {
            for (const [id, saved] of ranges) {
                if (saved.startContainer.ownerDocument !== doc) continue;
                if (rangesIntersect(range, saved)) return id;
            }
            return undefined;
        },
    };
}

function foliateAnnotation(annotation: Annotation): FoliateAnnotation {
    return { id: annotation.id, kind: 'highlight', value: annotation.locator.cfi! };
}

function rangesIntersect(a: Range, b: Range): boolean {
    const doc = a.startContainer.ownerDocument;
    if (!doc || b.startContainer.ownerDocument !== doc) return false;

    const aStart = collapsedRange(doc, a.startContainer, a.startOffset);
    const aEnd = collapsedRange(doc, a.endContainer, a.endOffset);
    const bStart = collapsedRange(doc, b.startContainer, b.startOffset);
    const bEnd = collapsedRange(doc, b.endContainer, b.endOffset);
    return (
        aStart.compareBoundaryPoints(Range.START_TO_START, bEnd) < 0 &&
        aEnd.compareBoundaryPoints(Range.START_TO_START, bStart) > 0
    );
}

function collapsedRange(doc: Document, node: Node, offset: number): Range {
    const range = doc.createRange();
    range.setStart(node, offset);
    range.collapse(true);
    return range;
}
