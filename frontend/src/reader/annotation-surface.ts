import type { Annotation } from '../types';

// Bounds are in the owning document's viewport. Foliate uses a DOM range;
// PDF uses the saved page rectangles transformed for the current display.
export interface AnnotationAnchor {
    doc: Document;
    getBoundingClientRect(): DOMRect;
}

export interface AnnotationSurface {
    connect(handlers: { show(id: number, anchor: AnnotationAnchor): void; leave(): void }): void;
    add(annotation: Annotation): Promise<void>;
    remove(annotation: Annotation): Promise<void>;
    show(annotation: Annotation): Promise<void>;
    anchor(id: number): AnnotationAnchor | undefined;
    atRange(doc: Document, range: Range): number | undefined;
}
