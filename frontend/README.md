# Frontend

The frontend is vanilla TypeScript bundled into `internal/web/static` by esbuild and embedded into the Go binary. Use the existing small helpers and primitives; do not introduce a framework or utility dependency for ordinary UI work.

## Tooling

- Source lives under `frontend/src/`; the root `package.json` owns esbuild, `tsc`, Playwright, and Biome.
- `make frontend` bundles the app and reader assets. `make test` formats sources and runs the full repository gate.
- Run `make browser-test` for UI changes and inspect the generated screenshots. For a one-off capture against a running server, use `npm run screenshot -- <path-or-url> <output.png>`.
- Browser support follows [../docs/browser-support.md](../docs/browser-support.md): conservative `es2018` output, iPad Safari as a practical constraint, and no mandatory very-new platform APIs without fallback.

## Code Map

- `src/main.ts` is the authenticated app composition root: route table, sidebar setup, bootstrap settings, and History-click wiring.
- `src/router.ts` owns route matching and mount lifecycle. Routes match by pathname. The router creates and owns one root element per mounted route and passes it to `mount(match, root, context)`, which may return a cleanup function or a controller (`destroy`, and optionally `suspend`/`resume`). Cleanup runs before the next route is mounted.
- `src/history-state.ts` owns what polka keeps in a history entry — its identity, saved scroll, and the page it was pushed from — and the pure retention decisions taken from it. `src/main.ts` applies them; the router only executes.
- `src/catalog-events.ts` is the one channel a successful mutation reports through. Its payload has exactly three shapes: books updated, books removed, or coarse.
- `src/pages.ts` contains routed page skeleton HTML. Page behavior belongs in `src/views/`.
- `src/views/` owns page-level behavior: library, book detail/edit, series, authors, cleanup, trash, and related workflows.
- `src/components/` owns reusable form/list widgets: book cards, autocomplete controls, the flexible date picker, rich editor, select, and toggle.
- `src/modal.ts`, `src/menu.ts`, `src/popover.ts`, and `src/toast.ts` are shared UI primitives used by views and components.
- `src/loading-indicator.ts` and the `.local-loading-state` CSS class cover global and local loading states. Use page/panel-local empty or error states when content cannot load.
- `src/settings.ts` owns the settings modal shell and tab composition;
  `src/settings/` keeps each panel's state and workflows with that panel, while
  `src/settings/ui.ts` holds only DOM/lifecycle primitives shared inside settings.
- `src/api.ts` is the API client boundary; `src/types.ts` mirrors the JSON DTOs served by Go.
- `src/reader/` is the standalone reader bundle entry and runtime. Keep it separate from routed app views unless deliberately changing that boundary.
- `src/styles/style.css` is the single bundled stylesheet. Prefer explicit state classes from TypeScript over fragile selector tricks.

## Routed Pages

To add an authenticated app page, add its skeleton in `src/pages.ts`, implement behavior in `src/views/`, register one route in `src/main.ts`, and add API helpers/types if needed. Keep login, setup, reader, download, and API routes outside the SPA router unless there is a concrete reason to fold them in.

Every DOM lookup a view makes belongs inside its own root. The root is what lets a route be detached and kept alive across a navigation, and a `document.getElementById` reaching past it finds the wrong page as soon as two instances exist. Look outside the root only for things that genuinely live there — the sidebar, modals, and other floating layers.

Route cleanup must remove global listeners, timers, popovers, menus, and other floating UI created by the view. The router rejects stale async mount results, but it does not cancel side effects inside a still-running mount; async views that mutate DOM after awaited fetches still need local cancellation or staleness guards.

Prefer acquiring the controller synchronously: start the data load without awaiting it and return the controller at once, as the library and book routes do. Then a later navigation can cancel the work instead of racing its render, and global listeners registered during mount are held from the first moment — anything awaited before returning leaves them live on the next page. Content readiness is view state, not route ownership: the page shows its own loading shell and marks itself busy for its own data.

`main.ts` restores the saved scroll position once `mount()` resolves, which is too early for a view whose content is still loading — `scrollTo` against a document that is one empty viewport tall clamps to the top. Such a view re-applies the position itself once its content is in the DOM, as the library does after its first page of books.

One route at a time may be retained rather than destroyed: opening a book from the library detaches the list and brings the same instance back on the way out, with its accumulated pages, position, focus, and selection intact. A retained route is `suspend`ed while detached — it owns nothing on the visible page, touches no body class or URL, and does not let unfinished work land — and `resume`s when it is put back. `src/views/return-position.ts` is what puts the reader back where they were. Only the library is eligible; `src/history-state.ts` holds that policy.

View-local state should live inside the mount closure or an object created by that mount — including the data a page has loaded, its request generations, and its render cleanup. Two instances of a route exist for a moment during a navigation, and a module-level "current page" is how one of them starts writing over the other. Module-level state is only for deliberate cross-mount persistence, such as localStorage-backed preferences or process-wide pagehide signals.

## API and Data Shapes

The list/detail split is intentional: `BookSummary` is the lean list/cleanup projection and `Book` extends it with fields loaded only by the single-book endpoint. If a flow needs detail-only fields such as identifiers, language, or publisher, fetch the full book by id instead of trusting a list row.

Keep authenticated API calls in `src/api.ts`, whose internal `apiFetch` / `fetchJSON` helpers centralize 401 handling and error shaping. Views should call the exported typed endpoint functions. A request that a view can outlive takes that view's `AbortSignal`, so one surface cannot cancel another's work.

Never expose or cache `storage_path` in frontend state. File access goes through server routes that resolve by `asset_id`.

Escape external text interpolated into HTML templates with `escapeHtml()`, or assign it with `textContent`. Insert raw HTML only when it comes from an explicitly trusted local renderer or a server-sanitized `*_html` field. Book details expose `description_source` only as the lossless editor baseline/text fallback; display surfaces consume `description_html` and never render the source field.

## UI Conventions

- Keep interfaces calm, dense enough for repeated use, and free of normal-flow warning banners.
- Update the smallest practical DOM region; avoid full-tree rerenders for incremental list, card, and form updates.
- Keep layout dimensions stable for book cards, tables, toolbars, menus, and buttons so loading states and hover states do not shift the page.
- In the library grid, give every cover the same column width, preserve its natural proportions, and align each row along the covers' bottom edge. Title, author, and series are a non-layout-shifting overlay shown on hover or keyboard focus; the table remains the always-visible metadata view.
- Series have no page of their own: series links always lead to the library filtered by series in volume order (`seriesLibraryURL` in `src/search-query.ts`). The Series grid is deliberately not the library grid — its tiles crop to a shared box.
- Reuse the sizing, corner, color, and surface tokens in `:root`. Keep responsive overrides after the base rules they override; `src/styles/style.css` is ordered by feature rather than as one final media-query block.
- At drawer widths, routed pages share the top strip with the fixed sidebar toggle. Reuse the `.app-main--strip` and `.page-heading-row` patterns instead of adding page-specific top spacing.
- Lazy-load covers, debounce search, and cancel stale requests with `AbortController` where user input can outrun the network.
- Loading feedback: keep existing content visible while new data loads. Use the global loading bar for broad page/request progress, and small local busy states (e.g. edit-modal switching, detail loads) where stale data would otherwise look final. Avoid fake skeleton cards/rows unless a specific screen is clearly better with them.
- Build cover URLs with `coverUrl()` so variants and cache busting stay consistent. The server returns either the stored cover or the generated placeholder.
- Keep validation and recoverable workflow messages next to the relevant control or content. Use toast when a command result has no stable action site, and a local page or panel state when the requested content itself cannot load.
- Use existing icons from `src/icons.ts` and existing primitives before adding new UI helpers.
- Choose the right primitive: `src/menu.ts` for action menus, `src/popover.ts` for rich caller-rendered panels, `src/components/select.ts` for single-value choices, and `src/components/toggle.ts` for on/off settings. Native checkboxes remain appropriate for list selection and membership. Catalog-backed author, tag, and series combobox adapters live in `src/components/book-metadata-autocomplete.ts` — keep them off the menu API.
- Use `openModal` / `confirmModal` for explicit user-invoked dialogs. Do not use blocking modals as routine browse/read warnings.
- Preserve iPad/Safari ergonomics: large enough tap targets, predictable downloads, and no reliance on APIs excluded by the browser support baseline.

## Settings

Settings live in one tabbed modal outside the main nav; keep the number of sections small and do not pre-build empty tabs. Broad app/UI preferences belong to `user_settings`, while reader-engine display preferences belong to `user_reader_preferences`. Roles determine which tabs and controls are shown, but the server must enforce every role boundary — UI gating is never authorization.

## Reader

The Foliate bundle starts at `src/reader/index.ts`; the fixed-layout PDF.js bundle starts at `src/reader/pdf-index.ts`. They may share small reader UI and state modules, but must not import each other's engine, so non-PDF books do not load PDF.js. Engine-specific search stays in `src/reader/search.ts` and `src/reader/pdf-search.ts`, with shared panel policy in `src/reader/search-panel.ts`. Keep reader code under `src/reader/`, and do not couple it to routed app views; PDF cover rendering remains a server-side catalog capability, not a reader backend.
