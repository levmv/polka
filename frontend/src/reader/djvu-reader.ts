import {
    DjvuDecoder,
    mapPoint,
    type PageText,
    type RandomAccessSource,
    zoneText,
} from '../../../internal/format/djvu/vendor/decoder.mjs';
import { PagedReader, type PagedReaderOptions, pagedReaderElements } from './paged-reader';
import { createReaderTOCPanel } from './toc-panel';

export async function initDjvuReader(
    page: HTMLElement,
    assetId: number,
    options: PagedReaderOptions = {},
): Promise<void> {
    const elements = pagedReaderElements(page);
    const url = page.dataset.readerUrl;
    if (!elements || !url) return;
    await new DjvuReader(page, assetId, url, elements, options).open();
}

class DjvuReader extends PagedReader {
    protected readonly label = 'DjVu';
    private decoder: DjvuDecoder | null = null;

    protected async load(): Promise<number> {
        this.decoder = await DjvuDecoder.create('/static/djvutang.wasm', {
            workerUrl: '/static/djvu.worker.js',
        });
        const source = await djvuSource(this.readURL);
        const document = await this.decoder.open(source);
        if (document.indirect) {
            throw new Error(
                'This DjVu needs external page files. Use a bundled DjVu to read it here.',
            );
        }
        if (!document.pages.length) throw new Error('This DjVu has no pages.');
        return document.pages.length;
    }

    protected async setup(): Promise<void> {
        // A missing or damaged outline must not prevent reading the pages.
        void this.loadOutline().catch((error) =>
            console.warn('Could not load DjVu contents:', error),
        );
    }

    protected async renderPage(generation: number): Promise<void> {
        const decoder = this.decoder!;
        const number = this.pageNumber - 1;
        const geometry = await decoder.geometry(number);
        if (generation !== this.renderGeneration) return;
        const size = this.pageSize(geometry.width, geometry.height);
        // Leave room in the decoder's 64 MiB budget for scan layers; raster copies
        // also occupy JS and canvas memory.
        const outputScale = Math.min(
            size.outputScale,
            Math.sqrt(4_000_000 / (size.width * size.height)),
        );
        const raster = await decoder.render(number, {
            size: {
                width: Math.max(1, Math.floor(size.width * outputScale)),
                height: Math.max(1, Math.floor(size.height * outputScale)),
            },
        });
        if (generation !== this.renderGeneration) return;

        const canvas = this.elements.canvas;
        canvas.width = raster.width;
        canvas.height = raster.height;
        canvas.style.width = `${size.width}px`;
        canvas.style.height = `${size.height}px`;
        this.elements.page.style.width = `${size.width}px`;
        this.elements.page.style.height = `${size.height}px`;
        this.elements.page.hidden = false;
        const context = canvas.getContext('2d', { alpha: false });
        if (!context) throw new Error('Could not allocate a DjVu canvas.');
        context.putImageData(
            new ImageData(new Uint8ClampedArray(raster.rgba), raster.width, raster.height),
            0,
            0,
        );

        const text = await decoder.text(number).catch((error) => {
            if (generation === this.renderGeneration)
                console.warn('Could not load DjVu text:', error);
            return null;
        });
        if (!text || generation !== this.renderGeneration) return;
        this.renderText(text, geometry, size.width / geometry.width, size.height / geometry.height);
    }

    private renderText(
        text: PageText,
        geometry: Awaited<ReturnType<DjvuDecoder['geometry']>>,
        scaleX: number,
        scaleY: number,
    ): void {
        const layer = this.elements.textLayer;
        const measuring = document.createElement('canvas').getContext('2d')!;
        // Prefer word boxes for selections. Line-only OCR is also common.
        const type = text.zones.some((zone) => zone.type === 'word') ? 'word' : 'line';
        for (const zone of text.zones) {
            if (zone.type !== type) continue;
            const value = zoneText(text, zone);
            if (!value.trim()) continue;
            if (zone.width <= 0 || zone.height <= 0) continue;
            const origin = mapPoint(geometry.matrix, zone);
            const [a, b, c, d] = geometry.matrix;
            const span = document.createElement('span');
            span.textContent = `${value} `;
            span.style.fontSize = `${zone.height}px`;
            measuring.font = `${zone.height}px sans-serif`;
            const measured = measuring.measureText(span.textContent).width;
            const stretch = measured > 0 ? zone.width / measured : 1;
            span.style.transform = `matrix(${a * scaleX * stretch}, ${b * scaleY * stretch}, ${c * scaleX}, ${d * scaleY}, ${origin.x * scaleX}, ${origin.y * scaleY})`;
            layer.append(span);
        }
    }

    private async loadOutline(): Promise<void> {
        const decoder = this.decoder!;
        const outline = await decoder.outline();
        if (!outline?.entries.length) return;
        const controls = createReaderTOCPanel(this.root);
        if (!controls) return;
        const depths: number[] = [];
        for (const entry of outline.entries.slice(0, 1_000)) {
            const depth = entry.parent === null ? 0 : Math.min((depths[entry.parent] ?? 0) + 1, 16);
            depths.push(depth);
            const row = document.createElement('li');
            row.className = 'reader-toc-row';
            const button = document.createElement('button');
            button.type = 'button';
            button.className = 'reader-toc-item';
            button.textContent = entry.title;
            button.style.paddingInlineStart = `${0.65 + depth}rem`;
            button.addEventListener('click', () => {
                void decoder
                    .resolveLink(entry.href, this.pageNumber - 1)
                    .then(async (link) => {
                        if (link.kind !== 'page' || link.page === null) return;
                        await this.navigateTo(link.page + 1);
                        controls.close();
                    })
                    .catch((error) => console.warn('Could not follow DjVu contents:', error));
            });
            row.append(button);
            controls.list.append(row);
        }
    }

    protected releasePage(): void {
        // Geometry and OCR requests can outlive a turn; generation checks keep
        // their results off the next page. Rendering itself is cancellable.
        void this.decoder?.cancelRender().catch(() => undefined);
        this.elements.textLayer.replaceChildren();
        this.elements.canvas.width = 0;
        this.elements.canvas.height = 0;
    }

    protected destroy(): void {
        this.decoder?.destroy();
    }
}

async function djvuSource(url: string): Promise<RandomAccessSource> {
    const head = await fetch(url, { method: 'HEAD', credentials: 'same-origin' });
    if (!head.ok) throw new Error('Could not open this DjVu. Reopen the book to try again.');
    const size = Number(head.headers.get('Content-Length'));
    if (!Number.isSafeInteger(size) || size < 1 || size > 0xffffffff) {
        throw new Error('This DjVu is too large or has an invalid file size.');
    }
    // Read ahead to avoid an HTTP request for every small chunk header.
    // Return slices of cached data: the adapter transfers buffers to its worker.
    let cache: { offset: number; bytes: ArrayBuffer } | undefined;
    return {
        size,
        async read(offset, length, { signal }): Promise<ArrayBuffer> {
            if (
                cache &&
                offset >= cache.offset &&
                offset + length <= cache.offset + cache.bytes.byteLength
            ) {
                return cache.bytes.slice(offset - cache.offset, offset - cache.offset + length);
            }
            const end = Math.min(size, offset + Math.max(length, 64 * 1024)) - 1;
            const response = await fetch(url, {
                headers: { Range: `bytes=${offset}-${end}` },
                credentials: 'same-origin',
                signal,
            });
            if (
                response.status !== 206 ||
                response.headers.get('Content-Range') !== `bytes ${offset}-${end}/${size}`
            ) {
                void response.body?.cancel();
                throw new Error('Could not read this DjVu range.');
            }
            const bytes = await response.arrayBuffer();
            if (bytes.byteLength !== end - offset + 1) throw new Error('Incomplete DjVu range.');
            if (bytes.byteLength <= 64 * 1024) {
                cache = { offset, bytes };
                return bytes.slice(0, length);
            }
            return bytes.byteLength === length ? bytes : bytes.slice(0, length);
        },
    };
}
