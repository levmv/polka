// Small History-API router for authenticated app pages.
//
// Each route matches by pathname only; returning null means "not this route".
// The router creates and owns one root element per mounted route, renders the
// route skeleton into it, and hands it to mount(). Views own everything wired
// beneath that root and return either a cleanup function or a controller.
// Owning the root — rather than replacing #app-content wholesale — is what lets
// a single route later be detached and kept alive across a navigation.
// Cleanup always runs before the next route is mounted, so views can remove
// global listeners and floating UI safely.
// Returning the controller synchronously — starting the data load without
// awaiting it — is the direction, and is what lets a later navigation cancel a
// route's work rather than race its render. The library and book routes do
// this. For the async form the remaining routes still use, mountSeq destroys a
// result that resolves after a newer navigation; it cannot reach inside a
// running mount, so those views still need local staleness guards.
export type RouteCleanup = () => void;

export interface ScrollPosition {
    x: number;
    y: number;
}

// What a mounted route hands back to the router. destroy() is final and must be
// safe to call once; the router never calls it twice.
export interface RouteController {
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

export type RouteMountResult = undefined | RouteCleanup | RouteController;

export interface RouteMountContext {
    // False for the mount that happens as the document loads. The browser
    // already announces a page it loaded itself and starts focus at the top; a
    // client navigation replaces the page silently, and that is the only case
    // where a view has to move focus deliberately.
    clientNavigation: boolean;
}

export interface Route<TMatch> {
    // Sidebar nav item id to mark active for this route, if any.
    navId?: string;
    // Temporary class applied to .app-main while this route is active.
    mainClass?: string;
    // Static or match-derived document title.
    title?: string | ((match: TMatch) => string);
    // Return route-specific data for a matching pathname, or null to skip.
    match: (pathname: string) => TMatch | null;
    // Optional HTML skeleton rendered into the route root before mount().
    render?: (match: TMatch) => string;
    // Wire the page inside its own root and return cleanup for listeners,
    // popovers, and state. Every DOM lookup belongs inside root.
    mount: (
        match: TMatch,
        root: HTMLElement,
        context: RouteMountContext,
    ) => RouteMountResult | Promise<RouteMountResult>;
}

export interface MountOptions {
    retention?: Retention;
    // Set by the navigation layer. The router's own first mount leaves it off.
    clientNavigation?: boolean;
}

export interface Router {
    mount(pathname: string, opts?: MountOptions): Promise<boolean>;
    canMount(pathname: string): boolean;
    // The key the retained slot currently holds, so the navigation layer can
    // stay in step with what actually happened (a retain is a no-op when the
    // outgoing route never finished mounting).
    retainedKey(): string | null;
    destroy(): void;
}

interface MatchedRoute<TMatch> {
    route: Route<TMatch>;
    match: TMatch;
}

const CONTENT_HOST_ID = 'app-content';
// A plain static block. It must never take position, transform, filter,
// contain, or display: contents: .library-jump-rail is position: fixed and
// measured against the viewport, and any of those would make this root its
// containing block.
const ROUTE_ROOT_CLASS = 'route-root';

export function initRouter(routes: Route<unknown>[]): Router {
    let activeRoot: HTMLElement | null = null;
    let activeController: RouteController | null = null;
    let retained: { key: string; root: HTMLElement; controller: RouteController } | null = null;
    let destroyed = false;
    let mountSeq = 0;
    let activeNavId: string | undefined;
    let activeMainClass: string | undefined;

    // Clearing the field before destroy() runs keeps a re-entrant callback from
    // rediscovering a half-destroyed instance.
    const releaseActive = (): void => {
        const controller = activeController;
        const root = activeRoot;
        activeController = null;
        activeRoot = null;
        controller?.destroy();
        root?.remove();
    };

    const releaseRetained = (): void => {
        const slot = retained;
        retained = null;
        slot?.controller.destroy();
        slot?.root.remove();
    };

    // Detaching is what takes the route out of layout, focus order, pointer
    // input, and the accessibility tree. It destroys nothing, so suspend() and
    // a later destroy() stay explicit rather than implied by moving a node.
    const retainActive = (key: string): void => {
        const root = activeRoot;
        const controller = activeController;
        if (!root || !controller) {
            releaseActive();
            return;
        }
        releaseRetained();
        activeRoot = null;
        activeController = null;
        controller.suspend?.();
        root.remove();
        retained = { key, root, controller };
    };

    const applyChrome = (matched: MatchedRoute<unknown> | null): void => {
        setActiveNav(activeNavId, matched?.route.navId);
        activeNavId = matched?.route.navId;
        setMainClass(activeMainClass, matched?.route.mainClass);
        activeMainClass = matched?.route.mainClass;
        if (matched) setDocumentTitle(matched.route.title, matched.match);
    };

    const router: Router = {
        canMount(pathname: string): boolean {
            return matchRoute(routes, pathname) !== null;
        },
        retainedKey(): string | null {
            return retained?.key ?? null;
        },
        async mount(pathname: string, opts: MountOptions = {}): Promise<boolean> {
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

            try {
                const result = await matched.route.mount(matched.match, root, {
                    clientNavigation: opts.clientNavigation ?? false,
                });
                const controller = normalizeController(result);
                if (destroyed || seq !== mountSeq) {
                    controller?.destroy();
                    return false;
                }
                activeController = controller;
                return true;
            } catch (error: unknown) {
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

    void router.mount(window.location.pathname);
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
    const main = document.querySelector<HTMLElement>('.app-main');
    if (!main) return;
    if (previous && previous !== next) main.classList.remove(previous);
    if (next) main.classList.add(next);
}

function setDocumentTitle<TMatch>(title: Route<TMatch>['title'], match: TMatch): void {
    if (!title) return;
    document.title = typeof title === 'function' ? title(match) : title;
}

function normalizeController(result: RouteMountResult): RouteController | null {
    if (!result) return null;
    if (typeof result === 'function') return { destroy: result };
    return result;
}

// The app-level navigate, published so a view deep in the tree can make an
// ordinary in-app navigation. A full page load would discard the retained route
// this design exists to preserve, so the only places that should still assign
// window.location are those leaving the app entirely.
let appNavigate: ((href: string) => void) | null = null;

export function setAppNavigate(navigate: (href: string) => void): void {
    appNavigate = navigate;
}

export function navigateApp(href: string): void {
    if (appNavigate) appNavigate(href);
    else window.location.href = href;
}

// Rewriting the current entry's URL must not discard its history state: that
// state carries the saved scroll position and the pathname the entry was pushed
// from, both of which back-navigation depends on. Tidying a URL (dropping a
// spent `q`, recording the browse offset, following the editor to the next
// book) is not a new entry, so it merges rather than passing null.
export function replaceLocationURL(url: string | URL): void {
    window.history.replaceState(window.history.state, '', url);
}
