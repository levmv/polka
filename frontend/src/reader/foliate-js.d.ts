declare module 'foliate-js/view.js';

declare module 'foliate-js/epub.js' {
    export class EPUB {
        constructor(loader: {
            loadText(name: string): Promise<string> | null;
            loadBlob(name: string, type?: string): Promise<Blob> | null;
            getSize(name: string): number;
        });
        init(): Promise<object>;
    }
}

declare module 'foliate-js/vendor/zip.js' {
    export interface ZipEntry {
        filename: string;
        filenameUTF8?: boolean;
        rawFilename?: Uint8Array;
        uncompressedSize?: number;
        getData(writer: unknown): Promise<Blob | string>;
    }

    export class BlobReader {
        constructor(blob: Blob);
    }

    export class BlobWriter {
        constructor(type?: string);
    }

    export class TextWriter {
        constructor(encoding?: string);
    }

    export class ZipReader {
        constructor(reader: BlobReader);
        getEntries(): Promise<ZipEntry[]>;
    }

    export function configure(options: { useWebWorkers: boolean }): void;
}

declare module 'foliate-js/overlayer.js' {
    export const Overlayer: {
        highlight(rects: unknown[], options?: Record<string, unknown>): SVGElement;
        outline(rects: unknown[], options?: Record<string, unknown>): SVGElement;
    };
}
