# Tailored PDFium WebAssembly module

Polka embeds `pdfium-cover.wasm` for its PDF cover fallback. The file is a
deterministic post-link reduction of the module distributed by go-pdfium, not
an independent PDFium source build.

[`pdfium-wasm.json`](pdfium-wasm.json) is the source of truth for the upstream
version and hashes, retained imports and exports, Binaryen version, and
expected output.

## Verify and regenerate

The normal build verifies the checked-in artifact without downloading or
regenerating it:

```sh
make pdfium-wasm-verify
```

To regenerate it with the pinned Binaryen version:

```sh
go run ./internal/pdfcover/wasmtool derive \
  -input "$(go env GOMODCACHE)/github.com/klippa-app/go-pdfium@VERSION/webassembly/pdfium.wasm" \
  -wasm-opt /path/to/wasm-opt
```

The tool verifies its inputs and the resulting artifact against the manifest.
Reproducibility starts from go-pdfium's published Wasm module because its full
PDFium source and Emscripten invocation are not published beside that file.

## Capability boundary

The retained exports are limited to opening a seekable PDF, rendering a known
page, and releasing the associated resources. PDFium's internally reachable
font, image, color, transparency, and page-content handling remains available.

Adding another server-side PDF capability requires an explicit allowlist and
manifest update; it must not silently restore the full module.

## Updating

When updating go-pdfium or PDFium:

1. update the manifest and regenerate the artifact twice, requiring identical
   output;
2. compare the full and tailored renderers on tracked fixtures and the private
   corpus;
3. exercise both wazero modes and the timeout/replacement-worker path;
4. review the exported surface, resource impact, and third-party notices.

Licenses and upstream notices are collected in
[`ThirdPartyNotices.txt`](../../ThirdPartyNotices.txt). Binaryen is used only as
a maintenance tool and is not distributed with Polka.
