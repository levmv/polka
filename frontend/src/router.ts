// Small History-API router for authenticated app pages.
//
// Each route matches by pathname only; returning null means "not this route".
// mount(match) owns page wiring and may return cleanup either as a function or
// as { destroy }. Cleanup always runs before the next route render replaces
// #app-content, so views can remove global listeners and floating UI safely.
// mountSeq rejects async mount results that resolve after a newer navigation:
// the stale cleanup is run immediately and the old route never becomes active.
// It does not cancel side effects inside a still-running mount; view code that
// mutates DOM after awaited fetches still needs local staleness guards.
export type RouteCleanup = () => void;
export type RouteMountResult = undefined | RouteCleanup | { destroy: RouteCleanup };

export interface Route<TMatch> {
    // Sidebar nav item id to mark active for this route, if any.
    navId?: string;
    // Temporary class applied to .app-main while this route is active.
    mainClass?: string;
    // Static or match-derived document title.
    title?: string | ((match: TMatch) => string);
    // Return route-specific data for a matching pathname, or null to skip.
    match: (pathname: string) => TMatch | null;
    // Optional HTML skeleton rendered before mount().
    render?: (match: TMatch) => string;
    // Wire the page and return cleanup for listeners, popovers, and state.
    mount: (match: TMatch) => RouteMountResult | Promise<RouteMountResult>;
}

export interface Router {
    mount(pathname: string, opts?: MountOptions): Promise<boolean>;
    canMount(pathname: string): boolean;
    destroy(): void;
}

type MountOptions = {
    render?: boolean;
};

interface MatchedRoute<TMatch> {
    route: Route<TMatch>;
    match: TMatch;
}

export function initRouter(routes: Route<unknown>[]): Router {
    let cleanup: RouteCleanup | null = null;
    let destroyed = false;
    let mountSeq = 0;
    let activeNavId: string | undefined;
    let activeMainClass: string | undefined;

    const router: Router = {
        canMount(pathname: string): boolean {
            return matchRoute(routes, pathname) !== null;
        },
        async mount(pathname: string, opts: MountOptions = {}): Promise<boolean> {
            const seq = ++mountSeq;
            cleanup?.();
            cleanup = null;
            const matched = matchRoute(routes, pathname);
            setActiveNav(activeNavId, matched?.route.navId);
            activeNavId = matched?.route.navId;
            setMainClass(activeMainClass, matched?.route.mainClass);
            activeMainClass = matched?.route.mainClass;
            if (!matched) return false;
            setDocumentTitle(matched.route.title, matched.match);

            if (opts.render && matched.route.render) {
                const target = document.getElementById('app-content');
                if (target) target.innerHTML = matched.route.render(matched.match);
            }

            try {
                const result = await matched.route.mount(matched.match);
                const nextCleanup = normalizeCleanup(result);
                if (destroyed || seq !== mountSeq) {
                    nextCleanup?.();
                    return false;
                }
                cleanup = nextCleanup;
                return true;
            } catch (error: unknown) {
                console.error('Failed to mount page:', error);
                return false;
            }
        },
        destroy(): void {
            mountSeq++;
            destroyed = true;
            cleanup?.();
            cleanup = null;
        },
    };

    void router.mount(window.location.pathname, { render: true });
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

function normalizeCleanup(result: RouteMountResult): RouteCleanup | null {
    if (!result) return null;
    if (typeof result === 'function') return result;
    return result.destroy;
}

// Rewriting the current entry's URL must not discard its history state: that
// state carries the saved scroll position and the pathname the entry was pushed
// from, both of which back-navigation depends on. Tidying a URL (dropping a
// spent `q`, recording the browse offset, following the editor to the next
// book) is not a new entry, so it merges rather than passing null.
export function replaceLocationURL(url: string | URL): void {
    window.history.replaceState(window.history.state, '', url);
}
