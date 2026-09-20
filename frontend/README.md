# Frontend

Vanilla TypeScript, bundled by [build.mjs](build.mjs) into
`internal/web/static/generated` and embedded in the Go binary. Static assets
from [static/](static/) are copied there during the build. The app, Foliate
reader, PDF.js reader and DjVu reader have separate bundles. Keep each engine
in its own reader bundle and shared UI independent of the engines.

Foliate source patches live in [foliate-build.mjs](foliate-build.mjs); review
them when upgrading Foliate.

## Development

Run commands from the repository root. `make frontend` rebuilds assets; the root
[package.json](../package.json) has focused checks. Follow the
[browser support baseline](../docs/browser-support.md), with iPad Safari as the
practical constraint.

The build downloads and caches DjVuTang's WASM on first use. Its browser adapter
and types are vendored. See [DjVuTang updates](../internal/format/djvu/README.md)
when changing the dependency.

Pure logic tests live in [test/](test/); browser tests live in
[browser-test/tests/](../browser-test/tests/). For a focused browser run:

```sh
make browser-test PWARGS='tests/pdf-reader.spec.ts --project=ipad-webkit'
```

Each browser test gets its own library copy and server, managed by
[fixtures.ts](../browser-test/tests/fixtures.ts). Use
`test.use({ account: 'reader' })` for a reader account or
`test.use({ seed: 'pagination' })` for pagination scenarios.

## Code map

| Area | Start here |
| --- | --- |
| HTML layout, login/setup pages and reader shell | [internal/web/templates/](../internal/web/templates/) |
| App shell and route registration | [src/main.ts](src/main.ts) |
| Route lifecycle and history policy | [src/router.ts](src/router.ts), [src/history-state.ts](src/history-state.ts) |
| Page markup and behavior | [src/views/](src/views/) |
| Settings modal and panels | [src/settings.ts](src/settings.ts), [src/settings/](src/settings/) |
| API client, response types and catalog notifications | [src/api.ts](src/api.ts), [src/types.ts](src/types.ts), [src/catalog-events.ts](src/catalog-events.ts) |
| Form and list widgets | [src/components/](src/components/) |
| Dialogs, action menus, floating panels and notifications | [src/modal.ts](src/modal.ts), [src/menu.ts](src/menu.ts), [src/popover.ts](src/popover.ts), [src/toast.ts](src/toast.ts) |
| Reader entry points | [src/reader/index.ts](src/reader/index.ts) (Foliate), [src/reader/pdf-index.ts](src/reader/pdf-index.ts) (PDF.js), [src/reader/djvu-index.ts](src/reader/djvu-index.ts) (DjVuTang) |
| PDF/DjVu page navigation and zoom | [src/reader/paged-reader.ts](src/reader/paged-reader.ts); each reader owns its raster and text layer |
| PDF fallback fonts | [fonts/](fonts/README.md) |
| Reader chrome, lifecycle and position | [src/reader/chrome.ts](src/reader/chrome.ts), [src/reader/lifecycle.ts](src/reader/lifecycle.ts), [src/reader/position-sync.ts](src/reader/position-sync.ts), [src/reader/position-saver.ts](src/reader/position-saver.ts) |
| Screen pagination | [src/reader/foliate-pagination.ts](src/reader/foliate-pagination.ts), [src/reader/pagination-cache.ts](src/reader/pagination-cache.ts), [src/reader/foliate-resources.ts](src/reader/foliate-resources.ts) |
| Highlights and notes | [src/components/annotation-editor.ts](src/components/annotation-editor.ts) (shared editor), [src/reader/annotations.ts](src/reader/annotations.ts) (reader UI), [src/reader/annotation-surface.ts](src/reader/annotation-surface.ts) (engine interface) |
| Styles and icons | [src/styles/](src/styles/), [src/icons.ts](src/icons.ts) |

A new app page keeps its skeleton and mount function under `views/`, with a
route in `main.ts`. The router covers authenticated app pages; readers and the
server-rendered login and setup pages stay outside it.

## State and lifetimes

Scope DOM queries and transient state to the mounted view's root. Module-level
state is for deliberate sharing or persistence across mounts. Prefer returning
cleanup or a controller synchronously while data loads in the background.

Views, panels and dialogs own their listeners, timers, subscriptions and
floating UI. Release them on unmount or close; cancel obsolete reads and ignore
late results.

Opening book details retains the catalog instance so Back restores loaded
pages, scroll, focus and selection. A suspended view must not affect the visible
page or URL. `history-state.ts` owns this policy;
[views/return-position.ts](src/views/return-position.ts) handles position.

## Data boundaries

- Use typed endpoints in `api.ts` for authentication and error handling. Pass
  the owner's `AbortSignal` to reads that can outlive it.
- `BookSummary` is the list projection; fetch a full `Book` when a workflow needs
  detail fields.
- Account and reader settings share one record: save only changed fields to
  avoid overwriting another surface's choices.
- Shared autosave values in `settings/state.ts` survive closing Settings;
  controls subscribe on mount and unsubscribe on unmount.
- Use `textContent` or `escapeHtml()` for external text. Render descriptions
  from the server-sanitized `description_html`; use `description_source` for
  editing. Other HTML must come from a trusted renderer.

## Catalog refresh

Publish successful mutations through `catalog-events.ts`, including those that
finish after their editor closes. The [library view](src/views/library-view.ts)
patches edits unrelated to the active filter or sort; other edits refresh the
loaded range while preserving scroll and selection. Query dependencies come
from [BookListDependencies](../internal/db/book_list_dependencies.go).

## UI conventions

- Reuse existing widgets, floating UI, icons and CSS variables in `:root`.
  Group styles by feature, with responsive overrides beside that feature.
- Update the smallest practical DOM region to preserve drafts, focus and layout.
  Keep content visible during refresh; use `loading-indicator.ts` for global
  progress and local loading states for individual panels.
- Keep validation and recoverable errors beside the relevant control or content.
  Use a toast when no stable place for feedback exists, and modals for explicit
  user workflows.
