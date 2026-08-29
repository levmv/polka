// The polka-owned contents of a history entry, and the pure decisions taken
// from them. Entry identity lives here because the router, the navigation
// layer, and in-page Back controls all read it, and because these rules are
// worth testing without a document.
import type { Retention, ScrollPosition } from './router';

export interface PolkaHistoryState {
    // Identity of this entry, independent of its URL. The same library URL can
    // occur in several entries with different live state, so retention can only
    // be matched by identity.
    polkaEntryID?: string;
    polkaScroll?: ScrollPosition;
    // URL this entry was pushed from. An in-page "Back" control can then return
    // through history — bringing the previous page back as it was left —
    // instead of pushing a fresh entry that lands at the top.
    polkaFrom?: string;
    // On a book entry: the entry ID of the library it was opened from, and so
    // the key of the retained instance that Back should resume.
    polkaRetainedLibraryID?: string;
    // An overlay opened over the page this entry shares a URL with. Back
    // dismisses the overlay and leaves the route alone; Forward reopens it.
    polkaOverlay?: OverlayEntry;
    // The page entry immediately underneath an overlay. Pathnames are not
    // enough here: several distinct library surfaces legitimately share `/`.
    polkaOverlayOriginID?: string;
    [key: string]: unknown;
}

// What an overlay needs to be reopened when Forward returns to its entry. The
// kind selects the reopener; the target is whatever that reopener needs to
// identify what was being shown, such as a book id.
export interface OverlayEntry {
    kind: string;
    target?: string;
}

export function readOverlayEntry(state: unknown): OverlayEntry | null {
    const overlay = readState(state)?.polkaOverlay;
    if (!overlay || typeof overlay !== 'object') return null;
    const { kind, target } = overlay as OverlayEntry;
    if (typeof kind !== 'string' || !kind) return null;
    const entry: OverlayEntry = { kind };
    if (typeof target === 'string' && target) entry.target = target;
    return entry;
}

export function isLibraryPath(pathname: string): boolean {
    return pathname === '/' || pathname === '/index.html';
}

export function isBookPath(pathname: string): boolean {
    return pathname.startsWith('/book/');
}

let entryCounter = 0;

// crypto.randomUUID needs a secure context, and polka is routinely served over
// plain HTTP on a home network. Uniqueness within one document is all an entry
// ID has to provide.
export function newEntryID(): string {
    entryCounter += 1;
    return `${Date.now().toString(36)}-${entryCounter}-${Math.random().toString(36).slice(2, 8)}`;
}

function readState(state: unknown): PolkaHistoryState | null {
    return state && typeof state === 'object' ? (state as PolkaHistoryState) : null;
}

function readString(state: unknown, key: keyof PolkaHistoryState): string | null {
    const value = readState(state)?.[key];
    return typeof value === 'string' && value ? value : null;
}

export function readEntryID(state: unknown): string | null {
    return readString(state, 'polkaEntryID');
}

export function readRetainedLibraryID(state: unknown): string | null {
    return readString(state, 'polkaRetainedLibraryID');
}

export function readOverlayOriginID(state: unknown): string | null {
    return readString(state, 'polkaOverlayOriginID');
}

// The URL of the entry one step back, when this entry was created by in-app
// navigation. Null means history has nothing known to return to, so a Back
// control must fall back to its own href.
export function readPredecessorURL(state: unknown): string | null {
    return readString(state, 'polkaFrom');
}

export function readScrollPosition(state: unknown): ScrollPosition | null {
    const scroll = readState(state)?.polkaScroll;
    if (!scroll || typeof scroll.x !== 'number' || typeof scroll.y !== 'number') return null;
    return scroll;
}

export function historyStateWithScroll(state: unknown, scroll: ScrollPosition): PolkaHistoryState {
    return { ...(readState(state) ?? {}), polkaScroll: scroll };
}

// What opening a link should do with the retained slot. The library is retained
// when a book is opened from it; moving between books keeps whichever library
// is already held; anything else leaves the relationship.
export function retentionForPush(args: {
    fromPathname: string;
    toPathname: string;
    fromID: string;
    retainedKey: string | null;
}): { retention: Retention; retainedLibraryID?: string } {
    if (isLibraryPath(args.fromPathname) && isBookPath(args.toPathname)) {
        return { retention: { mode: 'retain', key: args.fromID }, retainedLibraryID: args.fromID };
    }
    if (isBookPath(args.fromPathname) && isBookPath(args.toPathname) && args.retainedKey) {
        return {
            retention: { mode: 'keep', key: args.retainedKey },
            retainedLibraryID: args.retainedKey,
        };
    }
    return { retention: { mode: 'release' } };
}

// Back/Forward can land on the retained library itself, on a book that still
// belongs to it, or outside the relationship entirely. Only identity decides:
// the same URL can appear in several entries.
export function retentionForPop(args: {
    targetPathname: string;
    targetID: string;
    targetRetainedLibraryID: string | null;
    fromPathname: string;
    fromID: string;
    retainedKey: string | null;
    scroll: ScrollPosition | null;
}): Retention {
    const { retainedKey, targetRetainedLibraryID: targetLibrary } = args;
    if (retainedKey && args.targetID === retainedKey) {
        return { mode: 'resume', key: retainedKey, scroll: args.scroll };
    }
    if (isBookPath(args.targetPathname) && targetLibrary) {
        if (retainedKey && targetLibrary === retainedKey) {
            return { mode: 'keep', key: retainedKey };
        }
        // Forward out of the library into the book it was opened from: the same
        // instance is retained again rather than rebuilt.
        if (!retainedKey && targetLibrary === args.fromID && isLibraryPath(args.fromPathname)) {
            return { mode: 'retain', key: args.fromID };
        }
    }
    return { mode: 'release' };
}
