import type { PageHistory } from './navigation';

// Routes match by pathname and own a root element plus its controller. One
// instance can be detached and retained; others are destroyed before mounting
// the next page. Navigation owns the history decisions behind those lifetimes.
// Return a controller synchronously so cleanup is available while data loads.
// Data loads through ready; the route's signal is aborted before destroy().
export type RouteCleanup = () => void;

export interface ScrollPosition {
    x: number;
    y: number;
}

// What a mounted route hands back to the router. destroy() is final and must be
// safe to call once; the router never calls it twice.
export interface RouteController {
    ready?: Promise<void>;
    snapshot?(): unknown;
    beforeLeave?(): void;
    // Called while the root is still in the document, before it is detached.
    // The instance closes its floating UI, stops acting on global events, and
    // captures whatever it needs to restore its own position later — geometry
    // is unreadable once the root is out of the document.
    suspend?(): void;
    // Called after the root is back in the document, so layout exists again.
    // pixelFallback is the position history saved for this entry; the instance
    // may prefer its own anchor and use this only when that anchor is gone.
    resume?(pixelFallback: ScrollPosition | null): void;
    destroy: RouteCleanup;
}

// What the navigation layer wants done with the retained slot. Which routes are
// eligible is navigation policy; the router only executes it.
export type Retention =
    // Detach the current route into the slot instead of destroying it.
    | { mode: 'retain'; key: string }
    // Put the slot's instance back on screen instead of mounting a new one.
    | { mode: 'resume'; key: string; scroll: ScrollPosition | null }
    // Leave the slot alone — this navigation stays inside the relationship.
    | { mode: 'keep'; key: string }
    // Default: the relationship is over and anything retained is destroyed.
    | { mode: 'release' };

export interface RouteMountContext {
    history: PageHistory;
    // False for the mount that happens as the document loads. The browser
    // already announces a page it loaded itself and starts focus at the top; a
    // client navigation replaces the page silently, and that is the only case
    // where a view has to move focus deliberately.
    clientNavigation: boolean;
    // Aborted as soon as this route is destroyed. Views
    // pass it to their reads so an abandoned page cannot occupy a browser
    // connection or keep the global loading indicator alive.
    signal: AbortSignal;
}

export interface Route<TMatch> {
    // Sidebar nav item id to mark active for this route, if any.
    navId?: string;
    // Temporary class applied to .main while this route is active.
    mainClass?: string;
    // Static or match-derived document title.
    title?: string | ((match: TMatch) => string);
    // Return route-specific data for a matching pathname, or null to skip.
    match: (pathname: string) => TMatch | null;
    // Optional HTML skeleton rendered into the route root before mount().
    render?: (match: TMatch) => string;
    // Wire the page inside its own root and return cleanup for listeners,
    // popovers, and state. Every DOM lookup belongs inside root.
    mount: (match: TMatch, root: HTMLElement, context: RouteMountContext) => RouteController;
}

export interface MountOptions {
    history: PageHistory;
    retention?: Retention;
    // False for the initial document load.
    clientNavigation?: boolean;
}

export interface Router {
    snapshot(): unknown;
    prepareToLeave(): void;
    mount(pathname: string, opts: MountOptions): Promise<boolean>;
    canMount(pathname: string): boolean;
    // The key the retained slot currently holds, so the navigation layer can
    // stay in step with what actually happened.
    retainedKey(): string | null;
    destroy(): void;
}

interface MatchedRoute<TMatch> {
    route: Route<TMatch>;
    match: TMatch;
}

const CONTENT_HOST_ID = 'content';
// A plain static block. It must never take position, transform, filter,
// contain, or display: contents: .library-jump-rail is position: fixed and
// measured against the viewport, and any of those would make this root its
// containing block.
const ROUTE_ROOT_CLASS = 'route-root';

export function initRouter(routes: Route<unknown>[]): Router {
    let activeRoot: HTMLElement | null = null;
    let activeController: RouteController | null = null;
    let activeAbort: AbortController | null = null;
    let retained: {
        key: string;
        root: HTMLElement;
        controller: RouteController;
        abort: AbortController;
    } | null = null;
    let destroyed = false;
    let mountSeq = 0;
    let activeNavId: string | undefined;
    let activeMainClass: string | undefined;

    // Clearing the field before destroy() runs keeps a re-entrant callback from
    // rediscovering a half-destroyed instance.
    const releaseActive = (): void => {
        const controller = activeController;
        const root = activeRoot;
        const abort = activeAbort;
        activeController = null;
        activeRoot = null;
        activeAbort = null;
        abort?.abort();
        controller?.destroy();
        root?.remove();
    };

    const releaseRetained = (): void => {
        const slot = retained;
        retained = null;
        slot?.abort.abort();
        slot?.controller.destroy();
        slot?.root.remove();
    };

    // Detaching is what takes the route out of layout, focus order, pointer
    // input, and the accessibility tree. It destroys nothing, so suspend() and
    // a later destroy() stay explicit rather than implied by moving a node.
    const retainActive = (key: string): void => {
        const root = activeRoot;
        const controller = activeController;
        const abort = activeAbort;
        if (!root || !controller || !abort) {
            releaseActive();
            return;
        }
        releaseRetained();
        activeRoot = null;
        activeController = null;
        activeAbort = null;
        controller.suspend?.();
        root.remove();
        retained = { key, root, controller, abort };
    };

    const applyChrome = (matched: MatchedRoute<unknown> | null): void => {
        setActiveNav(activeNavId, matched?.route.navId);
        activeNavId = matched?.route.navId;
        setMainClass(activeMainClass, matched?.route.mainClass);
        activeMainClass = matched?.route.mainClass;
        if (matched) setDocumentTitle(matched.route.title, matched.match);
    };

    const router: Router = {
        snapshot: () => activeController?.snapshot?.(),
        prepareToLeave: () => activeController?.beforeLeave?.(),
        canMount(pathname: string): boolean {
            return matchRoute(routes, pathname) !== null;
        },
        retainedKey(): string | null {
            return retained?.key ?? null;
        },
        async mount(pathname: string, opts: MountOptions): Promise<boolean> {
            const seq = ++mountSeq;
            const retention: Retention = opts.retention ?? { mode: 'release' };
            const host = document.getElementById(CONTENT_HOST_ID);

            // Resuming is not a mount: no skeleton is rendered and mount() is
            // never called, so the instance keeps every bit of state it had.
            if (retention.mode === 'resume' && retained?.key === retention.key && host) {
                const slot = retained;
                retained = null;
                releaseActive();
                applyChrome(matchRoute(routes, pathname));
                host.replaceChildren(slot.root);
                activeRoot = slot.root;
                activeController = slot.controller;
                activeAbort = slot.abort;
                slot.controller.resume?.(retention.scroll);
                return true;
            }

            if (retention.mode === 'retain') {
                retainActive(retention.key);
            } else {
                if (retention.mode !== 'keep' || retained?.key !== retention.key) releaseRetained();
                releaseActive();
            }

            const matched = matchRoute(routes, pathname);
            applyChrome(matched);
            if (!matched) return false;
            if (!host) return false;
            const root = document.createElement('div');
            root.className = ROUTE_ROOT_CLASS;
            if (matched.route.render) root.innerHTML = matched.route.render(matched.match);
            host.replaceChildren(root);
            activeRoot = root;
            const abort = new AbortController();
            activeAbort = abort;

            try {
                const controller = matched.route.mount(matched.match, root, {
                    clientNavigation: opts.clientNavigation ?? false,
                    signal: abort.signal,
                    history: opts.history,
                });
                activeController = controller;
                await controller.ready;
                return !destroyed && seq === mountSeq;
            } catch (error: unknown) {
                if (abort.signal.aborted || destroyed || seq !== mountSeq) return false;
                console.error('Failed to mount page:', error);
                return false;
            }
        },
        destroy(): void {
            mountSeq++;
            destroyed = true;
            releaseActive();
            releaseRetained();
        },
    };

    return router;
}

function matchRoute(routes: Route<unknown>[], pathname: string): MatchedRoute<unknown> | null {
    for (const route of routes) {
        const match = route.match(pathname);
        if (match !== null) return { route, match };
    }
    return null;
}

function setActiveNav(previous: string | undefined, next: string | undefined): void {
    if (previous && previous !== next)
        document.getElementById(previous)?.classList.remove('active');
    if (next) document.getElementById(next)?.classList.add('active');
}

function setMainClass(previous: string | undefined, next: string | undefined): void {
    const main = document.querySelector<HTMLElement>('.main');
    if (!main) return;
    if (previous && previous !== next) main.classList.remove(previous);
    if (next) main.classList.add(next);
}

function setDocumentTitle<TMatch>(title: Route<TMatch>['title'], match: TMatch): void {
    if (!title) return;
    document.title = typeof title === 'function' ? title(match) : title;
}
