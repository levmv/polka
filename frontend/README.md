# Frontend

Vanilla TypeScript, bundled by [build.mjs](build.mjs) into
`internal/web/static` and embedded in the Go binary. The app, Foliate reader
and PDF.js reader have separate bundles. Each engine belongs only in its own
reader bundle; keep the app and shared reader UI independent of both engines.

Repository-wide contribution rules live in [AGENTS.md](../AGENTS.md).

## Development

Run commands from the repository root:

- `make serve` builds and runs a local library.
- `make frontend` bundles assets; `make test` formats sources and runs the full
  repository gate.
- `make browser-test` runs the UI suite. Inspect its screenshots in
  `browser-test/screenshots/` after UI changes.

The root [package.json](../package.json) owns the toolchain and focused scripts.
Pure logic tests live in [test/](test/); browser tests live in
[browser-test/tests/](../browser-test/tests/). Follow the
[browser support baseline](../docs/browser-support.md), with iPad Safari as the
practical constraint.

## Code map

| Area | Start here |
| --- | --- |
| App shell and route registration | [src/main.ts](src/main.ts) |
| Route lifecycle and history policy | [src/router.ts](src/router.ts), [src/history-state.ts](src/history-state.ts) |
| Page markup and behavior | [src/pages.ts](src/pages.ts), [src/views/](src/views/) |
| Settings modal and panels | [src/settings.ts](src/settings.ts), [src/settings/](src/settings/) |
| API client, response types and catalog notifications | [src/api.ts](src/api.ts), [src/types.ts](src/types.ts), [src/catalog-events.ts](src/catalog-events.ts) |
| Form and list widgets | [src/components/](src/components/) |
| Dialogs, action menus, floating panels and notifications | [src/modal.ts](src/modal.ts), [src/menu.ts](src/menu.ts), [src/popover.ts](src/popover.ts), [src/toast.ts](src/toast.ts) |
| Reader entry points | [src/reader/index.ts](src/reader/index.ts) (Foliate), [src/reader/pdf-index.ts](src/reader/pdf-index.ts) (PDF.js) |
| Styles and icons | [src/styles/style.css](src/styles/style.css), [src/icons.ts](src/icons.ts) |

A new app page needs a skeleton in `pages.ts`, behavior under `views/`, and a
route in `main.ts`. The router covers authenticated app pages; login, setup,
readers, downloads and API endpoints stay outside it.

## State and lifetimes

The router gives each mounted view its own root. Scope DOM queries to that root
and keep transient state inside the mount. Module-level state is for deliberate
sharing or persistence across mounts. Prefer returning a cleanup function or
controller synchronously while data loads in the background.

Views, panels and dialogs own their controls, global listeners, timers and
floating UI. Release these when replacing or closing the owner. Cancel obsolete
reads and ignore late results; removing DOM alone does not stop asynchronous
work.

Opening a book retains the library view so Back restores its loaded pages,
position, focus and selection. While suspended, that view must not affect the
visible page or URL. `history-state.ts` decides when to retain or resume it;
[views/return-position.ts](src/views/return-position.ts) handles its position.

## Data boundaries

- Use the typed endpoint functions in `api.ts`, which centralize authentication
  and errors. Pass an owner's `AbortSignal` to reads that can outlive it.
- `BookSummary` is the list projection; `Book` adds detail fields. Fetch the
  single-book endpoint when a workflow needs the full record.
- Use `catalog-events.ts` to notify other views after a successful catalog
  mutation, so they can patch or refresh their results.
- Account and reader preferences share one settings record. Save only changed
  fields to avoid overwriting another surface's choices.
- Use `textContent` or `escapeHtml()` for external text. Insert HTML only from
  trusted renderers or server-sanitized fields. Display book descriptions from
  `description_html`; `description_source` belongs to editing.

## UI conventions

- Reuse the existing widgets, floating UI, icons and CSS variables in `:root`.
  Keep styles grouped by feature, with responsive overrides beside that feature.
- Update the smallest practical DOM region to preserve drafts, focus and layout.
  Keep loaded content visible during refresh; use `loading-indicator.ts` for
  global progress and local loading states for individual panels.
- Keep validation and recoverable errors beside the relevant control or content.
  Use a toast when an action has no stable place for feedback. Reserve modals
  for explicit user workflows.
