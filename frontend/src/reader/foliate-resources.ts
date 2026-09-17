import type { FoliateSection } from './foliate-engine';

// Keep section resources until both views release them. Serialize loads because
// Foliate does not merge concurrent requests for shared stylesheets, fonts or images.
export function shareSectionResources(sections: FoliateSection[]): void {
    let loading: Promise<unknown> = Promise.resolve();
    for (const section of sections) {
        const load = section.load.bind(section);
        const unload = section.unload?.bind(section);
        let readers = 0;
        let source: Promise<string> | undefined;
        section.load = () => {
            readers++;
            if (!source) {
                source = loading.then(load);
                loading = source.catch(() => {});
            }
            return source.catch((error) => {
                section.unload?.();
                throw error;
            });
        };
        section.unload = () => {
            if (readers === 0 || --readers > 0) return;
            source = undefined;
            unload?.();
        };
    }
}

// Reuse the hidden iframe's document to avoid Chromium cursor changes on navigation.
export function createPaginationDocumentLoader(): (
    frame: HTMLIFrameElement,
    source: string,
    signal?: AbortSignal,
) => Promise<void> {
    let trackedDocument: Document | null = null;
    const listenerCleanups: Array<() => void> = [];

    return async (frame, source, signal) => {
        const response = await fetch(source, { signal });
        if (!response.ok) throw new Error('Section did not load');
        const type = response.headers.get('Content-Type')?.split(';')[0];
        const parsed = new DOMParser().parseFromString(
            await response.text(),
            type === 'application/xhtml+xml' ? 'application/xhtml+xml' : 'text/html',
        );
        if (!parsed.body) throw new Error('Section has no document');

        for (const remove of listenerCleanups) remove();
        listenerCleanups.length = 0;
        Object.assign(frame.style, { width: '100%', height: '100%' });
        Object.assign(frame.parentElement!.style, { width: '100%', height: '100%', padding: '0' });

        let doc = frame.contentDocument;
        if (doc?.contentType !== parsed.contentType || doc?.compatMode !== parsed.compatMode) {
            // Navigate when the document mode changes: HTML and XHTML differ in
            // CSS selector matching, and quirks mode also affects page counts.
            const ready = waitForEvent(frame, ['load'], signal);
            frame.src = source;
            await ready;
            doc = frame.contentDocument;
        } else {
            // Parsed styles may have queued errors from their inactive document.
            // Copy the tree so those events cannot settle a new resource load.
            // Adopt other nodes because WebKit cannot import a doctype.
            const nodes: Node[] = [];
            const ready: Array<Promise<void>> = [];
            for (const child of Array.from(parsed.childNodes)) {
                const node =
                    child.nodeType === Node.ELEMENT_NODE
                        ? doc.importNode(child, true)
                        : doc.adoptNode(child);
                nodes.push(node);
                if (node.nodeType !== Node.ELEMENT_NODE) continue;
                for (const element of (node as Element).querySelectorAll(
                    'link[href], style, img, image[*|href]',
                ))
                    ready.push(waitForResource(element, signal));
            }
            doc.replaceChildren();
            for (const node of nodes) doc.appendChild(node);
            await Promise.all(ready);
        }
        if (!doc) throw new Error('Section has no document');
        if (doc !== trackedDocument) {
            // Replacing the tree leaves Foliate's document listeners attached.
            // Remove them before the next section installs its handlers.
            const document = doc;
            const add = document.addEventListener.bind(document);
            document.addEventListener = (
                type: string,
                listener: EventListenerOrEventListenerObject,
                options?: boolean | AddEventListenerOptions,
            ) => {
                listenerCleanups.push(() => document.removeEventListener(type, listener, options));
                add(type, listener, options);
            };
            trackedDocument = doc;
        }
    };
}

// fonts.ready can crash WebKit during style changes on iOS 16:
// https://github.com/readest/readest/pull/5654
export async function waitForFonts(doc: Document, signal?: AbortSignal): Promise<void> {
    // Start requests from newly applied styles before inspecting their status.
    doc.documentElement.getBoundingClientRect();
    const fonts = doc.fonts;
    while (fonts.status === 'loading' && doc.defaultView) {
        // WebKit can update the status without delivering loadingdone.
        await waitForEvent(fonts, ['loadingdone'], signal, 25);
    }
}

export async function waitForImages(doc: Document, signal?: AbortSignal): Promise<void> {
    await Promise.all(
        Array.from(doc.querySelectorAll('img'), (element) => waitForResource(element, signal)),
    );
}

function waitForResource(element: Element, signal?: AbortSignal): Promise<void> {
    if (element.localName === 'img') {
        const image = element as HTMLImageElement;
        // Lazy images must load before we measure a section's full page count.
        image.loading = 'eager';
        if (image.complete) return Promise.resolve();
    }
    if (element.localName === 'link') {
        const link = element as HTMLLinkElement;
        const stylesheet = link.rel.toLowerCase().split(/\s+/).includes('stylesheet');
        const css = !link.type || link.type.split(';')[0].trim().toLowerCase() === 'text/css';
        if (!stylesheet || !css || link.disabled) return Promise.resolve();
    }
    if (element.localName === 'style') {
        const type = element.getAttribute('type')?.trim().toLowerCase();
        if ((type && type !== 'text/css') || !element.textContent?.trim()) return Promise.resolve();
    }
    // Treat load failures as settled; cancellation leaves the count unknown.
    return waitForEvent(element, ['load', 'error'], signal);
}

function waitForEvent(
    target: EventTarget,
    events: string[],
    signal?: AbortSignal,
    pollAfterMS?: number,
): Promise<void> {
    return new Promise((resolve, reject) => {
        const cleanup = () => {
            clearTimeout(pollTimer);
            for (const event of events) target.removeEventListener(event, done);
            signal?.removeEventListener('abort', abort);
        };
        const done = () => {
            cleanup();
            resolve();
        };
        const abort = () => {
            cleanup();
            reject(new DOMException('Measurement cancelled', 'AbortError'));
        };
        const pollTimer = pollAfterMS === undefined ? undefined : setTimeout(done, pollAfterMS);
        if (signal?.aborted) {
            abort();
            return;
        }
        for (const event of events) target.addEventListener(event, done);
        signal?.addEventListener('abort', abort, { once: true });
    });
}
