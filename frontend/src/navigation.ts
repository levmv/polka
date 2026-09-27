import { isPlainClick } from './dom';
import { errorMessage } from './errors';
import {
    newEntryID,
    type OverlayEntry,
    type PolkaHistoryState,
    readEntryID,
    readOverlayEntry,
    readOverlayOriginID,
    readPredecessorURL,
    readScrollPosition,
    retentionForPop,
    retentionForPush,
} from './history-state';
import { beginGlobalLoading } from './loading-indicator';
import { adoptReaderOrigin, rememberReaderOrigin } from './reader-history';
import type { Retention, Router } from './router';
import { showToast } from './toast';

// Bound to a mounted page. A suspended or superseded page cannot change the
// visible URL. Overlays share their origin's page state, but have their own entry.
export interface PageHistory {
    readonly state: unknown;
    replace(url: string | URL): void;
    push(url: string | URL): void;
    save(): void;
}

interface PageEntry {
    state: PolkaHistoryState;
    url: URL;
    ready: boolean;
}

export interface OverlayController {
    canClose(): boolean | Promise<boolean>;
    close(reason: 'history' | 'api'): void;
}

export interface OverlayHandle {
    update(descriptor: OverlayEntry): void;
    dismiss(): Promise<void>;
    release(): void;
}

// Load first; navigation alone decides whether the returned opener is still
// current. Consumers never open UI from an unguarded asynchronous callback.
type OverlayReopener = (
    entry: OverlayEntry,
    signal: AbortSignal,
) => (() => void) | null | Promise<(() => void) | null>;

interface OpenOverlay {
    id: string;
    index: number;
    controller: OverlayController;
}

let router: Router;
let page: PageEntry;
let closeTransientUI: () => void;
let locationChanged: () => void;
let overlay: OpenOverlay | null = null;
let confirmedClose: OpenOverlay | null = null;
let guardSequence = 0;
let restoreEntry: { id: string; resolve(): void } | null = null;
let scrollFrame = 0;
let scrollTimer = 0;
let pendingOverlay: AbortController | null = null;
let overlaySequence = 0;
const reopeners = new Map<string, { prepare: OverlayReopener; urlParam?: string }>();

const locationURL = () => new URL(window.location.href);
const relativeURL = (url: URL) => `${url.pathname}${url.search}${url.hash}`;
const entryIndex = (state: PolkaHistoryState) => state.polkaIndex ?? 0;
const currentState = (): PolkaHistoryState => window.history.state ?? {};

function ensureEntry(): PolkaHistoryState {
    const state = currentState();
    if (!state.polkaEntryID) state.polkaEntryID = newEntryID();
    state.polkaIndex ??= 0;
    window.history.replaceState(state, '');
    return state;
}

function overlayFromURL(url: URL): OverlayEntry | null {
    for (const [kind, { urlParam }] of reopeners) {
        if (urlParam && url.searchParams.has(urlParam))
            return { kind, target: url.searchParams.get(urlParam) || undefined };
    }
    return null;
}

function entryURL(url: URL, descriptor?: OverlayEntry | null): URL {
    const result = new URL(url);
    for (const { urlParam } of reopeners.values()) {
        if (urlParam) result.searchParams.delete(urlParam);
    }
    const param = descriptor && reopeners.get(descriptor.kind)?.urlParam;
    if (param) result.searchParams.set(param, String(descriptor?.target ?? ''));
    return result;
}

function pageEntry(state: PolkaHistoryState, url: URL): PageEntry {
    const { polkaOverlay, polkaOverlayOriginID, ...base } = state;
    if (polkaOverlayOriginID) {
        base.polkaEntryID = polkaOverlayOriginID;
        base.polkaIndex = entryIndex(state) - 1;
    }
    return { state: base, url: entryURL(url), ready: false };
}

function pageIsCurrent(): boolean {
    const state = currentState();
    return (
        state.polkaEntryID === page.state.polkaEntryID ||
        state.polkaOverlayOriginID === page.state.polkaEntryID
    );
}

function persistPage(): void {
    if (!pageIsCurrent()) return;
    const state = currentState();
    window.history.replaceState(
        state.polkaOverlay
            ? {
                  ...page.state,
                  polkaEntryID: state.polkaEntryID,
                  polkaIndex: state.polkaIndex,
                  polkaOverlay: state.polkaOverlay,
                  polkaOverlayOriginID: state.polkaOverlayOriginID,
              }
            : page.state,
        '',
        relativeURL(entryURL(page.url, state.polkaOverlay)),
    );
}

function capturePage(): void {
    if (!page.ready || !pageIsCurrent()) return;
    page.state.polkaScroll = { x: window.scrollX, y: window.scrollY };
    page.state.polkaView = router.snapshot();
    persistPage();
}

function prepareToLeave(): void {
    cancelOverlayReopen();
    router.prepareToLeave();
    capturePage();
}

function pageHistory(): PageHistory {
    let id = page.state.polkaEntryID;
    const state = page.state.polkaView;
    const active = () => id === page.state.polkaEntryID && pageIsCurrent();
    return {
        state,
        replace(value) {
            if (!active()) return;
            const url = entryURL(new URL(value, window.location.href));
            if (relativeURL(url) === relativeURL(page.url)) return;
            page.url = url;
            persistPage();
            locationChanged();
        },
        push(value) {
            if (!active()) return;
            const url = new URL(value, window.location.href);
            if (relativeURL(url) === relativeURL(page.url)) return;
            cancelOverlayReopen();
            capturePage();
            page = {
                url,
                ready: true,
                state: newPageState(),
            };
            id = page.state.polkaEntryID;
            window.history.pushState(page.state, '', relativeURL(url));
            locationChanged();
        },
        save() {
            if (active()) capturePage();
        },
    };
}

function newPageState(): PolkaHistoryState {
    return {
        polkaEntryID: newEntryID(),
        polkaIndex: entryIndex(currentState()) + 1,
        polkaFrom: relativeURL(locationURL()),
        polkaScroll: { x: 0, y: 0 },
    };
}

export function navigateApp(href: string): void {
    if (!router) {
        window.location.assign(href);
        return;
    }
    const url = new URL(href, window.location.href);
    if (relativeURL(url) === relativeURL(locationURL()) && url.origin === location.origin) return;
    const descriptor = url.origin === location.origin ? overlayFromURL(url) : null;
    if (descriptor && relativeURL(entryURL(url)) === relativeURL(page.url)) {
        void reopenOverlay(descriptor);
        return;
    }
    prepareToLeave();
    if (url.origin !== location.origin || !router.canMount(url.pathname)) {
        rememberReaderOrigin(url);
        window.location.assign(url);
        return;
    }
    const { retention, retainedLibraryID } = retentionForPush({
        fromPathname: page.url.pathname,
        toPathname: url.pathname,
        fromID: page.state.polkaEntryID!,
        retainedKey: router.retainedKey(),
    });
    const state = newPageState();
    if (retainedLibraryID) state.polkaRetainedLibraryID = retainedLibraryID;
    window.history.pushState(state, '', relativeURL(url));
    void mountPage(state, url, retention);
}

async function mountPage(
    state: PolkaHistoryState,
    url: URL,
    retention: Retention = { mode: 'release' },
    clientNavigation = true,
): Promise<void> {
    const target = pageEntry(state, url);
    const sequence = overlaySequence;
    page = target;
    closeTransientUI();
    window.cancelAnimationFrame(scrollFrame);
    window.clearTimeout(scrollTimer);
    const finish = beginGlobalLoading();
    try {
        const mounted = await router.mount(url.pathname, {
            retention,
            clientNavigation,
            history: pageHistory(),
        });
        if (page !== target) return;
        if (!mounted) {
            window.location.replace(url);
            return;
        }
        locationChanged();
        const restore = () => {
            if (page !== target) return;
            if (retention.mode !== 'resume' && !url.hash) {
                const scroll = readScrollPosition(target.state);
                if (scroll) window.scrollTo(scroll.x, scroll.y);
            }
            target.ready = true;
            capturePage();
        };
        if (retention.mode === 'resume') restore();
        else
            scrollFrame = window.requestAnimationFrame(() => {
                scrollFrame = window.requestAnimationFrame(restore);
            });
        const descriptor = readOverlayEntry(state) ?? overlayFromURL(url);
        if (descriptor && sequence === overlaySequence) void reopenOverlay(descriptor);
    } finally {
        finish();
    }
}

async function returnToOverlay(owner: OpenOverlay): Promise<void> {
    if (readEntryID(currentState()) === owner.id) return;
    await new Promise<void>((resolve) => {
        restoreEntry = { id: owner.id, resolve };
        window.history.go(owner.index - entryIndex(currentState()));
    });
}

async function onPopState(): Promise<void> {
    cancelOverlayReopen();
    const state = ensureEntry();
    if (restoreEntry) {
        if (state.polkaEntryID === restoreEntry.id) {
            const pending = restoreEntry;
            restoreEntry = null;
            pending.resolve();
        }
        return;
    }
    const url = locationURL();
    const owner = overlay;
    if (owner && state.polkaEntryID !== owner.id) {
        const sequence = ++guardSequence;
        const decision = confirmedClose === owner ? true : owner.controller.canClose();
        confirmedClose = null;
        if (decision !== true) {
            await returnToOverlay(owner);
            const proceed = await decision;
            if (sequence !== guardSequence || overlay !== owner || !proceed) return;
            confirmedClose = owner;
            window.history.go(entryIndex(state) - owner.index);
            return;
        }
        owner.controller.close(state.polkaEntryID === page.state.polkaEntryID ? 'history' : 'api');
    }

    const targetPageID = readOverlayOriginID(state) ?? state.polkaEntryID;
    if (targetPageID === page.state.polkaEntryID) {
        // Save & Next may have changed the page while its overlay was current.
        // Only the actual origin receives that update when the overlay closes.
        persistPage();
        const descriptor = readOverlayEntry(state);
        if (descriptor) void reopenOverlay(descriptor);
        return;
    }
    if (!router.canMount(url.pathname)) return;
    const retention = retentionForPop({
        targetPathname: url.pathname,
        targetID: targetPageID!,
        targetRetainedLibraryID: state.polkaRetainedLibraryID ?? null,
        fromPathname: page.url.pathname,
        fromID: page.state.polkaEntryID!,
        retainedKey: router.retainedKey(),
        scroll: readScrollPosition(state),
    });
    await mountPage(state, url, retention);
}

export function enterOverlay(
    descriptor: OverlayEntry,
    controller: OverlayController,
): OverlayHandle {
    prepareToLeave();
    const state = currentState();
    const url = relativeURL(entryURL(page.url, descriptor));
    if (state.polkaOverlay) {
        window.history.replaceState({ ...state, polkaOverlay: descriptor }, '', url);
    } else {
        window.history.pushState(
            {
                ...page.state,
                polkaEntryID: newEntryID(),
                polkaIndex: entryIndex(state) + 1,
                polkaOverlay: descriptor,
                polkaOverlayOriginID: page.state.polkaEntryID,
            },
            '',
            url,
        );
    }
    const owner: OpenOverlay = {
        id: currentState().polkaEntryID!,
        index: entryIndex(currentState()),
        controller,
    };
    overlay = owner;
    let dismissed: Promise<void> | null = null;
    let resolveDismissal: (() => void) | undefined;
    return {
        update(descriptor) {
            if (overlay !== owner || readEntryID(currentState()) !== owner.id) return;
            window.history.replaceState(
                { ...currentState(), polkaOverlay: descriptor },
                '',
                relativeURL(entryURL(page.url, descriptor)),
            );
        },
        dismiss() {
            if (overlay !== owner) return Promise.resolve();
            if (dismissed) return dismissed;
            dismissed = new Promise((resolve) => {
                resolveDismissal = resolve;
            });
            confirmedClose = owner;
            window.history.back();
            return dismissed;
        },
        release() {
            if (overlay === owner) overlay = null;
            resolveDismissal?.();
        },
    };
}

// Any newly opened modal supersedes a pending reopen, even when it has no
// history step of its own (for example, a confirmation).
export function cancelOverlayReopen(): void {
    overlaySequence++;
    pendingOverlay?.abort();
    pendingOverlay = null;
}

async function reopenOverlay(descriptor: OverlayEntry): Promise<void> {
    cancelOverlayReopen();
    const reopener = reopeners.get(descriptor.kind);
    if (!reopener) return;
    const request = new AbortController();
    pendingOverlay = request;
    const id = readEntryID(currentState());
    const current = () =>
        !request.signal.aborted &&
        readEntryID(currentState()) === id &&
        reopeners.get(descriptor.kind) === reopener;
    const finish = beginGlobalLoading();
    request.signal.addEventListener('abort', finish, { once: true });
    try {
        const open = await reopener.prepare(descriptor, request.signal);
        if (!current()) return;
        pendingOverlay = null;
        if (!open) throw new Error('This item is no longer available.');
        open();
    } catch (error) {
        if (!current()) return;
        showToast(errorMessage(error, 'Could not reopen this dialog.'), { type: 'error' });
        if (!overlay && readOverlayOriginID(currentState())) window.history.back();
    } finally {
        if (pendingOverlay === request) pendingOverlay = null;
        request.signal.removeEventListener('abort', finish);
        finish();
    }
}

export function registerOverlayReopen(
    kind: string,
    prepare: OverlayReopener,
    options: { urlParam?: string } = {},
): () => void {
    reopeners.set(kind, { prepare, ...options });
    return () => {
        if (reopeners.get(kind)?.prepare === prepare) reopeners.delete(kind);
    };
}

export function initNavigation(
    appRouter: Router,
    closeUI: () => void,
    onLocationChange: () => void,
): void {
    router = appRouter;
    closeTransientUI = closeUI;
    locationChanged = onLocationChange;
    adoptReaderOrigin();
    const state = ensureEntry();
    page = pageEntry(state, locationURL());
    if (router.canMount(location.pathname)) {
        window.history.scrollRestoration = 'manual';
        void mountPage(state, locationURL(), { mode: 'release' }, false);
    }
    document.addEventListener('click', (event) => {
        if (event.defaultPrevented || !isPlainClick(event)) return;
        const link =
            event.target instanceof Element
                ? event.target.closest<HTMLAnchorElement>('a[href]')
                : null;
        if (!link || (link.target && link.target !== '_self') || link.hasAttribute('download'))
            return;
        const url = new URL(link.href);
        if (url.origin !== location.origin) return;
        if (link.hasAttribute('data-back') && readPredecessorURL(currentState())) {
            event.preventDefault();
            window.history.back();
        } else if (router.canMount(location.pathname) && router.canMount(url.pathname)) {
            event.preventDefault();
            navigateApp(link.href);
        } else if (url.pathname.startsWith('/read/')) {
            prepareToLeave();
            rememberReaderOrigin(url);
        }
    });
    window.addEventListener('popstate', () => void onPopState());
    window.addEventListener(
        'scroll',
        () => {
            window.clearTimeout(scrollTimer);
            scrollTimer = window.setTimeout(capturePage, 100);
        },
        { passive: true },
    );
    window.addEventListener('pagehide', capturePage);
}
