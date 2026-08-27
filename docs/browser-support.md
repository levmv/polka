# Browser support baseline

Polka should stay conservative on browser requirements. The web UI is part of
the product, but it should not depend on very new platform APIs unless there is a
clear fallback or the feature is explicitly progressive enhancement.

## Target

- Core browse, catalog, administration, and download baseline: Safari / iOS
  Safari 14 and contemporary Chromium / Firefox / Edge from roughly 2020.
- The in-browser PDF reader's exact Safari floor is unresolved because the
  pinned PDF.js runtime uses APIs newer than Safari 14. Browse and download
  remain available when that reader capability is unavailable.
- Older browsers may keep working on a best-effort basis, especially for simple
  browse/download flows, but they are not the contract.
- iPad Safari remains the practical constraint for interaction size, layout
  steadiness, downloads, and reader behavior.
- Frontend output should stay at the current conservative build level
  (`es2018`) unless a concrete feature justifies changing it.

This baseline is intentionally about capability age, not only market share:
prefer platform features that were already broadly available by 2019-2020.

## Allowed default primitives

These are acceptable for core UI code:

- standard DOM APIs, event delegation, `classList`, `dataset`;
- `fetch`, `AbortController`, `URL`, `URLSearchParams`;
- `history.pushState` / `popstate` for router navigation;
- `CustomEvent` for small cross-module UI events;
- CSS flex/grid, custom properties, media queries, and `prefers-color-scheme`.

## Use with care

Do not make these mandatory without an explicit compatibility check and fallback:

- Navigation API;
- View Transitions API;
- Popover API;
- native `dialog` / `inert`;
- very new CSS selectors or layout features, including parent selectors such as
  `:has()` for core behavior;
- APIs that require recent Safari releases beyond the baseline.

The stylesheet should not depend on CSS `:has()` for core UI behavior. Use
explicit state classes from TypeScript/templates instead; `:has()` is acceptable
only as optional progressive enhancement.

## Testing posture

Chromium browser tests cover the main desktop suite and an iPad-sized responsive
pass. The iPad responsive checks also run under Playwright WebKit, which catches
Safari-engine layout/runtime differences that Chromium cannot. This is still not
the same as real iOS Safari on device, so keep device checks for release-critical
download/reader flows when available.
