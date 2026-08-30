import 'foliate-js/view.js';
import { EPUB } from 'foliate-js/epub.js';
import { Overlayer } from 'foliate-js/overlayer.js';
import {
    BlobReader,
    BlobWriter,
    configure as configureZIP,
    TextWriter,
    type ZipEntry,
    ZipReader,
} from 'foliate-js/vendor/zip.js';

import { clampNumber } from '../dom';
import type { ReaderDisplayStyle, ReaderPreferences } from '../types';

export const DEFAULT_READER_DISPLAY_STYLE: ReaderDisplayStyle = 'paper';

export function normalizeReaderDisplayStyle(style: unknown): ReaderDisplayStyle {
    return style === 'original' || style === 'custom' ? style : DEFAULT_READER_DISPLAY_STYLE;
}

export type FoliateTarget = string | number | { fraction: number };

export interface FoliateTOCItem {
    label?: string;
    href?: string;
    subitems?: FoliateTOCItem[];
}

export interface FoliateBook {
    toc?: FoliateTOCItem[];
    sections?: Array<{ id?: string }>;
}

interface FoliateComicSection {
    id: string;
    load: () => Promise<string>;
    unload: () => void;
    size: number;
}

interface FoliateComicBook extends FoliateBook {
    metadata: { title: string };
    getCover: () => Promise<Blob>;
    sections: FoliateComicSection[];
    rendition: { layout: 'pre-paginated' };
    resolveHref: (href: string) => { index: number };
    splitTOCHref: (href: string) => [string, null];
    getTOCFragment: (doc: Document) => Element;
    destroy: () => void;
}

export interface FoliateSearchExcerpt {
    pre?: string;
    match?: string;
    post?: string;
}

export interface FoliateSearchResult {
    cfi: string;
    excerpt: FoliateSearchExcerpt;
}

export type FoliateSearchYield =
    | 'done'
    | { progress: number }
    | { label?: string; subitems: FoliateSearchResult[] }
    | FoliateSearchResult;

export interface FoliateAnnotation {
    value: string;
    color?: string;
    [key: string]: unknown;
}

export interface FoliateRendererElement extends HTMLElement {
    setStyles?: (styles: string | [string, string]) => void;
    getContents?: () => Array<{ doc?: Document; index?: number }>;
}

export interface FoliateViewElement extends HTMLElement {
    book?: FoliateBook;
    renderer?: FoliateRendererElement;
    isFixedLayout?: boolean;
    open: (book: string | File | FoliateBook) => Promise<void>;
    init: (options: {
        lastLocation?: FoliateTarget | null;
        showTextStart?: boolean;
    }) => Promise<void>;
    goLeft?: () => Promise<void>;
    goRight?: () => Promise<void>;
    goTo: (target: FoliateTarget) => Promise<unknown>;
    prev: () => Promise<void>;
    next: () => Promise<void>;
    getCFI?: (index: number, range: Range) => string;
    search?: (options: {
        query: string;
        matchCase?: boolean;
        matchDiacritics?: boolean;
        matchWholeWords?: boolean;
    }) => AsyncGenerator<FoliateSearchYield>;
    clearSearch?: () => void;
    addAnnotation?: (
        annotation: FoliateAnnotation,
        remove?: boolean,
    ) => Promise<{ index?: number; label?: string } | undefined>;
    deleteAnnotation?: (annotation: FoliateAnnotation) => Promise<unknown>;
    showAnnotation?: (annotation: FoliateAnnotation) => Promise<unknown>;
}

export interface FoliateRelocateDetail {
    cfi?: string;
    fraction?: number;
    location?: {
        current?: number;
        next?: number;
        total?: number;
    };
    tocItem?: {
        label?: string;
        href?: string;
    };
}

export interface FoliateLoadDetail {
    doc: Document;
    index?: number;
}

export interface ReaderDisplayPalette {
    background: string;
    text: string;
}

// foliate-js 1.0.1 exposes no search-marker renderer and uses Overlayer.outline
// only for transient search annotations. Keep this adapter beside the pinned
// engine integration instead of patching the installed dependency.
Overlayer.outline = (rects: unknown[]): SVGElement => {
    const marker = Overlayer.highlight(rects, {
        color: rootCSSVariable('--reader-search-highlight-fill', 'rgba(255, 196, 0, 0.38)'),
    });
    marker.dataset.polkaSearchHighlight = 'true';
    marker.style.opacity = '1';
    marker.setAttribute(
        'stroke',
        rootCSSVariable('--reader-search-highlight-outline', 'rgba(180, 119, 0, 0.24)'),
    );
    marker.setAttribute('stroke-width', '1');
    for (const rect of marker.querySelectorAll('rect')) rect.setAttribute('rx', '2');
    return marker;
};

export function createFoliateView(stage: HTMLElement): FoliateViewElement {
    const view = document.createElement('foliate-view') as FoliateViewElement;
    view.className = 'reader-epub-view';
    view.setAttribute('autohide-cursor', '');
    stage.append(view);
    return view;
}

export async function openFoliateBookFile(view: FoliateViewElement, file: File): Promise<void> {
    const name = file.name.toLowerCase();
    const book = name.endsWith('.epub')
        ? ((await openFoliateEPUB(file)) as FoliateBook)
        : name.endsWith('.cbz')
          ? await openFoliateComic(file)
          : file;
    await view.open(book);
    tuneRenderer(view);
}

async function openFoliateEPUB(file: File): Promise<object> {
    const signature = new Uint8Array(await file.slice(0, 4).arrayBuffer());
    if (
        signature[0] !== 0x50 ||
        signature[1] !== 0x4b ||
        signature[2] !== 0x03 ||
        signature[3] !== 0x04
    ) {
        throw new Error('EPUB does not start with a canonical ZIP local header');
    }
    configureZIP({ useWebWorkers: false });
    const reader = new ZipReader(new BlobReader(file));
    const entries = await reader.getEntries();
    const map = foliateZIPEntryMap(entries);
    const loadText = (name: string): Promise<string> | null => {
        const entry = map.get(name);
        return entry ? (entry.getData(new TextWriter()) as Promise<string>) : null;
    };
    const loadBlob = (name: string, type?: string): Promise<Blob> | null => {
        const entry = map.get(name);
        return entry ? (entry.getData(new BlobWriter(type)) as Promise<Blob>) : null;
    };
    const getSize = (name: string): number => map.get(name)?.uncompressedSize ?? 0;
    return await new EPUB({ loadText, loadBlob, getSize }).init();
}

const COMIC_IMAGE_EXTENSIONS = ['.jpg', '.jpeg', '.png', '.gif', '.webp', '.avif'];
const MAX_COMIC_SNIFF_ENTRY_BYTES = 32 * 1024 * 1024;
const MAX_COMIC_SNIFF_TOTAL_BYTES = 64 * 1024 * 1024;

async function openFoliateComic(file: File): Promise<FoliateComicBook> {
    configureZIP({ useWebWorkers: false });
    const reader = new ZipReader(new BlobReader(file));
    const cachedBlobs = new Map<ZipEntry, Blob>();
    const entries: ZipEntry[] = [];
    let sniffedBytes = 0;
    for (const entry of await reader.getEntries()) {
        if (
            entry.filename.endsWith('/') ||
            isIgnoredComicPath(entry.filename) ||
            entry.filename.toLowerCase().endsWith('comicinfo.xml')
        ) {
            continue;
        }
        if (COMIC_IMAGE_EXTENSIONS.some((ext) => entry.filename.toLowerCase().endsWith(ext))) {
            entries.push(entry);
            continue;
        }
        const size = entry.uncompressedSize ?? MAX_COMIC_SNIFF_ENTRY_BYTES + 1;
        if (
            size > MAX_COMIC_SNIFF_ENTRY_BYTES ||
            size > MAX_COMIC_SNIFF_TOTAL_BYTES - sniffedBytes
        ) {
            continue;
        }
        sniffedBytes += size;
        const blob = await comicEntryBlob(entry);
        if (await isSupportedComicImage(blob)) {
            cachedBlobs.set(entry, blob);
            entries.push(entry);
        }
    }
    entries.sort((a, b) => compareNaturalComicPaths(a.filename, b.filename));
    if (entries.length === 0) throw new Error('No supported image files in comic archive');

    const cachedPages = new Map<ZipEntry, string>();
    const objectURLs = new Map<ZipEntry, [string, string]>();
    const loadPage = async (entry: ZipEntry): Promise<string> => {
        const cached = cachedPages.get(entry);
        if (cached) return cached;
        const image = URL.createObjectURL(cachedBlobs.get(entry) ?? (await comicEntryBlob(entry)));
        const page = URL.createObjectURL(
            new Blob([`<body style="margin:0"><img src="${image}">`], {
                type: 'text/html',
            }),
        );
        cachedPages.set(entry, page);
        objectURLs.set(entry, [image, page]);
        return page;
    };
    const unloadPage = (entry: ZipEntry): void => {
        for (const url of objectURLs.get(entry) ?? []) URL.revokeObjectURL(url);
        objectURLs.delete(entry);
        cachedPages.delete(entry);
    };
    const sections = entries.map((entry) => ({
        id: entry.filename,
        load: () => loadPage(entry),
        unload: () => unloadPage(entry),
        size: entry.uncompressedSize ?? 0,
    }));

    return {
        metadata: { title: file.name },
        getCover: async () => cachedBlobs.get(entries[0]) ?? (await comicEntryBlob(entries[0])),
        sections,
        toc: entries.map((entry) => ({ label: entry.filename, href: entry.filename })),
        rendition: { layout: 'pre-paginated' },
        resolveHref: (href: string) => ({
            index: sections.findIndex((section) => section.id === href),
        }),
        splitTOCHref: (href: string): [string, null] => [href, null],
        getTOCFragment: (doc: Document) => doc.documentElement,
        destroy: () => {
            for (const entry of entries) unloadPage(entry);
        },
    };
}

async function comicEntryBlob(entry: ZipEntry): Promise<Blob> {
    const blob = await entry.getData?.(new BlobWriter());
    if (!(blob instanceof Blob)) throw new Error(`Could not read comic page ${entry.filename}`);
    return blob;
}

async function isSupportedComicImage(blob: Blob): Promise<boolean> {
    const bytes = new Uint8Array(await blob.slice(0, 512).arrayBuffer());
    const startsWith = (...signature: number[]): boolean =>
        signature.every((byte, index) => bytes[index] === byte);
    return (
        startsWith(0xff, 0xd8, 0xff) ||
        startsWith(0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a) ||
        startsWith(0x47, 0x49, 0x46, 0x38, 0x37, 0x61) ||
        startsWith(0x47, 0x49, 0x46, 0x38, 0x39, 0x61) ||
        (startsWith(0x52, 0x49, 0x46, 0x46) &&
            bytes[8] === 0x57 &&
            bytes[9] === 0x45 &&
            bytes[10] === 0x42 &&
            bytes[11] === 0x50) ||
        isAVIFComicImage(bytes)
    );
}

function isAVIFComicImage(bytes: Uint8Array): boolean {
    if (bytes.length < 16 || ascii(bytes, 4) !== 'ftyp') return false;
    const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
    let boxSize = view.getUint32(0);
    let brandOffset = 8;
    if (boxSize === 1) {
        if (bytes.length < 24) return false;
        const high = view.getUint32(8);
        const low = view.getUint32(12);
        if (high !== 0) return false;
        boxSize = low;
        brandOffset = 16;
    }
    boxSize = Math.min(boxSize || bytes.length, bytes.length);
    if (boxSize < brandOffset + 8) return false;
    if (isAVIFBrand(ascii(bytes, brandOffset))) return true;
    for (let offset = brandOffset + 8; offset + 4 <= boxSize; offset += 4) {
        if (isAVIFBrand(ascii(bytes, offset))) return true;
    }
    return false;
}

function ascii(bytes: Uint8Array, offset: number): string {
    return String.fromCharCode(...bytes.subarray(offset, offset + 4));
}

function isAVIFBrand(brand: string): boolean {
    return brand === 'avif' || brand === 'avis';
}

function isIgnoredComicPath(name: string): boolean {
    const normalized = name.replace(/\\/g, '/');
    const parts = normalized.split('/');
    if (parts.some((part) => part.toLowerCase() === '__macosx' || part.startsWith('.')))
        return true;
    return parts[parts.length - 1]?.toLowerCase() === 'thumbs.db';
}

function compareNaturalComicPaths(left: string, right: string): number {
    const a =
        left
            .replace(/\\/g, '/')
            .toLowerCase()
            .match(/\d+|\D+/g) ?? [];
    const b =
        right
            .replace(/\\/g, '/')
            .toLowerCase()
            .match(/\d+|\D+/g) ?? [];
    for (let index = 0; index < Math.min(a.length, b.length); index++) {
        const aChunk = a[index];
        const bChunk = b[index];
        if (aChunk === bChunk) continue;
        if (/^\d/.test(aChunk) && /^\d/.test(bChunk)) {
            const aNumber = aChunk.replace(/^0+/, '') || '0';
            const bNumber = bChunk.replace(/^0+/, '') || '0';
            if (aNumber.length !== bNumber.length) return aNumber.length - bNumber.length;
            if (aNumber !== bNumber) return aNumber < bNumber ? -1 : 1;
            return aChunk.length - bChunk.length;
        }
        return aChunk < bChunk ? -1 : 1;
    }
    return a.length - b.length;
}

function foliateZIPEntryMap(entries: ZipEntry[]): Map<string, ZipEntry> {
    const exact = new Map(entries.map((entry) => [entry.filename, entry]));
    const aliases = new Map<string, ZipEntry>();
    const ambiguous = new Set<string>();
    const utf8 = new TextDecoder('utf-8', { fatal: true });

    for (const entry of entries) {
        if (entry.filenameUTF8 || !entry.rawFilename?.some((byte) => byte >= 0x80)) continue;
        let alias: string;
        try {
            alias = utf8.decode(entry.rawFilename);
        } catch {
            continue;
        }
        if (alias === entry.filename || exact.has(alias) || ambiguous.has(alias)) continue;
        const previous = aliases.get(alias);
        if (previous && previous !== entry) {
            aliases.delete(alias);
            ambiguous.add(alias);
            continue;
        }
        aliases.set(alias, entry);
    }
    for (const [alias, entry] of aliases) exact.set(alias, entry);
    return exact;
}

export function suppressTransientFoliateRenderErrors(): void {
    // foliate-js 1.0.1 starts observing the paginator before a newly created
    // section iframe necessarily has a body. A queued ResizeObserver callback
    // can therefore render against that short-lived empty document. The iframe
    // load callback still performs the real render, so suppress only these two
    // exact upstream exceptions and keep reporting every other reader error.
    const handler = (event: ErrorEvent) => {
        if (!String(event.filename || '').includes('/static/reader.js')) return;
        const message = event.message || '';
        const missingStyleTarget =
            message.includes("Cannot destructure property 'style'") && message.includes('null');
        const missingComputedStyleTarget =
            message.includes('getComputedStyle') && message.includes('Element');
        if (!missingStyleTarget && !missingComputedStyleTarget) return;
        const stack = String(event.error?.stack || '');
        if (stack && !stack.includes('ResizeObserver')) return;
        event.preventDefault();
        event.stopImmediatePropagation();
    };
    window.addEventListener('error', handler, true);
}

// foliate-js detects FB2/MOBI/etc. by filename, but the asset URL is served by
// opaque id with no extension. Fetch the bytes and wrap them in a File whose
// name carries the real format so detection is reliable. EPUB is recognized by
// its zip signature regardless, so a correct name there is simply harmless.
export async function fetchFoliateBookFile(url: string, format: string): Promise<File> {
    const res = await fetch(url);
    if (!res.ok) throw new Error(`Failed to fetch book file: ${res.status}`);
    const blob = await res.blob();
    const filenameFormat = format === 'kepub' ? 'kepub.epub' : format;
    return new File([blob], `book.${filenameFormat}`, { type: blob.type });
}

function tuneRenderer(view: FoliateViewElement): void {
    const renderer = view.renderer;
    if (!renderer) return;

    renderer.setAttribute('max-inline-size', '760px');
    renderer.setAttribute('max-column-count', '1');
    renderer.setAttribute('margin', `${readerMargin()}px`);
    renderer.setAttribute('gap', '7%');
}

export function applyFoliateDisplay(view: FoliateViewElement, prefs: ReaderPreferences): void {
    const renderer = view.renderer;
    if (!renderer) return;

    const style = normalizeReaderDisplayStyle(prefs.display_style);
    const palette = readerDisplayPalette(style);
    const columnWidth =
        style === 'custom'
            ? clampNumber(prefs.custom_column_width, 560, 920, 760)
            : style === 'original'
              ? 820
              : 760;

    renderer.style.setProperty('--theme-bg-color', palette.background);
    renderer.style.background = palette.background;
    renderer.setAttribute('max-inline-size', `${columnWidth}px`);
    renderer.setAttribute('max-column-count', '1');
    renderer.setAttribute('margin', `${readerMargin()}px`);
    renderer.setAttribute('gap', '7%');
    renderer.setStyles?.(readerContentCSS(style, prefs, palette));
}

function readerMargin(): number {
    // Foliate uses this as the paginated head/footer row height as well as the
    // page margin. Keep it close to our overlay chrome instead of letting tall
    // desktop viewports grow empty 64px bands above and below the book.
    return Math.round(clampNumber(window.innerHeight * 0.055, 28, 48, 40));
}

export function readerDisplayPalette(style: ReaderDisplayStyle): ReaderDisplayPalette {
    if (style === 'paper') {
        return { background: '#fbf7ef', text: '#241f1a' };
    }
    return {
        background: rootCSSVariable('--bg-color', '#fcfcfc'),
        text: rootCSSVariable('--text-main', '#202020'),
    };
}

function readerContentCSS(
    style: ReaderDisplayStyle,
    prefs: ReaderPreferences,
    palette: ReaderDisplayPalette,
): string {
    const rootFontSize = (1.05 + clampNumber(prefs.font_scale, -4, 6, 0) * 0.06).toFixed(2);
    if (style === 'original') {
        return `
html {
    --theme-bg-color: ${palette.background};
    background: ${palette.background} !important;
}

body {
    font-size: ${rootFontSize}rem !important;
    letter-spacing: 0 !important;
    box-sizing: border-box !important;
    padding-bottom: 4rem !important;
}
`;
    }

    const lineHeight =
        style === 'custom' ? clampNumber(prefs.custom_line_height, 1.2, 2.2, 1.72) : 1.72;

    return `
html {
    --theme-bg-color: ${palette.background};
    background: ${palette.background} !important;
    color: ${palette.text} !important;
    font-size: ${rootFontSize}rem !important;
    line-height: ${lineHeight.toFixed(2)} !important;
}

body {
    background: transparent !important;
    color: ${palette.text} !important;
    font-family: Charter, "Iowan Old Style", Georgia, serif !important;
    font-size: 1rem !important;
    line-height: inherit !important;
    letter-spacing: 0 !important;
    box-sizing: border-box !important;
    padding-bottom: 4rem !important;
    hanging-punctuation: allow-end last;
    orphans: 2;
    widows: 2;
}

p,
li,
div,
pre,
dd,
dt,
blockquote,
td,
th {
    font-size: 1rem !important;
    line-height: inherit !important;
}

p {
    margin: 0 0 1em !important;
}

h1,
h2,
h3,
h4,
h5,
h6 {
    line-height: 1.22 !important;
    margin: 0 0 1.1em !important;
}

h1 {
    font-size: 1.75rem !important;
}

h2 {
    font-size: 1.5rem !important;
}

h3 {
    font-size: 1.25rem !important;
}

h4,
h5,
h6 {
    font-size: 1rem !important;
}

p,
li,
blockquote,
dd {
    -webkit-hyphens: manual;
    hyphens: manual;
}

pre {
    white-space: pre-wrap !important;
    tab-size: 2;
}

small {
    font-size: smaller !important;
}

sub,
sup {
    font-size: 67.5% !important;
    line-height: 1 !important;
}
`;
}

// Books often ship full justification, which reads poorly in a variable-width
// reader column. Keep this as a computed-style pass instead of a blanket CSS
// override so intentional center/right alignment in headings and title pages
// survives opinionated themes.
const JUSTIFY_BLOCK_SELECTOR = 'p, div, li, blockquote, dd, dt, td, th, h1, h2, h3, h4, h5, h6';
const JUSTIFY_RELAXED_ATTR = 'data-polka-relaxed-justify';
const JUSTIFY_DOCUMENT_STATE_ATTR = 'data-polka-justify-state';
const JUSTIFY_ORIGINAL_ALIGN_ATTR = 'data-polka-original-text-align';

export function setCurrentFoliateDocumentJustification(
    view: FoliateViewElement,
    relaxed: boolean,
): void {
    const contents = view.renderer?.getContents?.() || [];
    for (const content of contents) {
        if (content.doc) setFoliateDocumentJustification(content.doc, relaxed);
    }
}

export function setFoliateDocumentJustification(doc: Document, relaxed: boolean): void {
    if (!relaxed) {
        if (doc.documentElement?.getAttribute(JUSTIFY_DOCUMENT_STATE_ATTR) === 'original') return;
        restoreFoliateDocumentJustification(doc);
        doc.documentElement?.setAttribute(JUSTIFY_DOCUMENT_STATE_ATTR, 'original');
        return;
    }
    if (doc.documentElement?.getAttribute(JUSTIFY_DOCUMENT_STATE_ATTR) === 'relaxed') return;
    const win = doc.defaultView;
    if (!win) return;
    const blocks = doc.querySelectorAll<HTMLElement>(JUSTIFY_BLOCK_SELECTOR);
    const justified: HTMLElement[] = [];
    for (let i = 0; i < blocks.length; i++) {
        if (win.getComputedStyle(blocks[i]).textAlign === 'justify') justified.push(blocks[i]);
    }
    for (let i = 0; i < justified.length; i++) {
        if (!justified[i].hasAttribute(JUSTIFY_RELAXED_ATTR)) {
            justified[i].setAttribute(JUSTIFY_ORIGINAL_ALIGN_ATTR, justified[i].style.textAlign);
            justified[i].setAttribute(JUSTIFY_RELAXED_ATTR, 'true');
        }
        justified[i].style.textAlign = 'start';
    }
    doc.documentElement?.setAttribute(JUSTIFY_DOCUMENT_STATE_ATTR, 'relaxed');
}

const COVER_SECTION_RE = /(?:^|[/_\-.])cover(?:page|image)?(?:[/_\-.]|$)/i;
const COVER_MARKER_RE = /(?:^|[\s_-])cover(?:[\s_-]|page|image|$)/i;

export function fitFoliateCoverDocument(
    doc: Document,
    sectionID = '',
    sectionIndex?: number,
): void {
    const root = doc.documentElement;
    const body = doc.body;
    if (!root || !body || !isFoliateCoverDocument(root, body, sectionID, sectionIndex)) return;

    const svg = body.querySelector<SVGSVGElement>('svg[viewBox]');
    if (!svg?.querySelector('image')) return;

    preserveSVGImageAspectRatio(svg);
    for (const image of svg.querySelectorAll<SVGImageElement>('image')) {
        preserveSVGImageAspectRatio(image);
    }
}

function isFoliateCoverDocument(
    root: HTMLElement,
    body: HTMLElement,
    sectionID: string,
    sectionIndex?: number,
): boolean {
    if (COVER_SECTION_RE.test(sectionID)) return true;
    for (const element of [root, body, body.firstElementChild]) {
        if (!element) continue;
        const marker = [
            element.getAttribute('id'),
            element.getAttribute('class'),
            element.getAttribute('epub:type'),
            element.getAttribute('role'),
        ]
            .filter(Boolean)
            .join(' ');
        if (COVER_MARKER_RE.test(marker)) return true;
    }
    return sectionIndex === 0 && isSoleDocumentGraphic(body);
}

function isSoleDocumentGraphic(body: HTMLElement): boolean {
    const first = body.firstElementChild;
    if (!first || first.nextElementSibling) return false;
    let element: Element = first;
    const wrappers = new Set(['a', 'div', 'figure', 'p', 'section']);
    while (wrappers.has(element.localName)) {
        const child: Element | null = element.firstElementChild;
        if (!child || child.nextElementSibling) return false;
        element = child;
    }
    return element.localName === 'svg' || element.localName === 'img';
}

function preserveSVGImageAspectRatio(element: SVGSVGElement | SVGImageElement): void {
    const value = element.getAttribute('preserveAspectRatio')?.trim().toLowerCase() || '';
    if (value.split(/\s+/).includes('none')) {
        element.setAttribute('preserveAspectRatio', 'xMidYMid meet');
    }
}

function restoreFoliateDocumentJustification(doc: Document): void {
    const relaxed = doc.querySelectorAll<HTMLElement>(`[${JUSTIFY_RELAXED_ATTR}]`);
    for (let i = 0; i < relaxed.length; i++) {
        const original = relaxed[i].getAttribute(JUSTIFY_ORIGINAL_ALIGN_ATTR);
        if (original) relaxed[i].style.textAlign = original;
        else relaxed[i].style.removeProperty('text-align');
        relaxed[i].removeAttribute(JUSTIFY_ORIGINAL_ALIGN_ATTR);
        relaxed[i].removeAttribute(JUSTIFY_RELAXED_ATTR);
    }
}

function rootCSSVariable(name: string, fallback: string): string {
    const value = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
    return value || fallback;
}

export function wireCurrentFoliateDocuments(
    view: FoliateViewElement,
    handler: (doc: Document) => void,
): void {
    const contents = view.renderer?.getContents?.() || [];
    for (const content of contents) {
        if (content.doc) handler(content.doc);
    }
}

export async function waitForRendererContents(view: FoliateViewElement): Promise<void> {
    // Foliate can resolve init before the iframe document is visible through
    // getContents(). Reader-ready should mean our click/touch handlers are wired.
    for (let i = 0; i < 60; i++) {
        if ((view.renderer?.getContents?.() || []).some((content) => content.doc)) return;
        await new Promise<void>((resolve) => window.requestAnimationFrame(() => resolve()));
    }
}
