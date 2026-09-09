# Tailored PDFium WebAssembly module

Polka embeds `generated/pdfium-cover.wasm` for PDF cover fallback and page
counting when the bounded structural reader in `internal/format` cannot resolve
the count.
WASM cover rendering also returns the count from the same opened document.
The file is a deterministic post-link reduction of the module distributed by
go-pdfium, not an independent PDFium source build.

[`pdfium-wasm.json`](pdfium-wasm.json) is the source of truth for the upstream
version and hashes, retained imports and exports, Binaryen version, and
expected output.

## Build and verify

`make build` and `make test` prepare the module automatically before compiling
Polka. It is generated locally and ignored by Git. A valid existing module is
reused after checking its hash, imports, exports, and dependency pins; a missing
or corrupt module is regenerated. GitHub Actions also caches the module by its
manifest and runs the same verification before reuse.

The upstream module comes from the Go module cache. `npm ci` installs the pinned
`binaryen` build dependency, whose self-contained `wasm-opt` script runs through
Node.js with `BINARYEN_CORES=1`. No additional system tools are required.
Preparation runs before compilation and tests to keep their memory use separate.
Downloading dependencies requires network access on the first build.

To prepare just the module, or verify it without downloading or regenerating:

```sh
make pdfium-wasm
make pdfium-wasm-verify
```

To force derivation even when the existing module is valid:

```sh
go run ./internal/pdfcover/wasmtool derive \
  -input "$(go env GOMODCACHE)/github.com/klippa-app/go-pdfium@VERSION/webassembly/pdfium.wasm" \
  -wasm-opt node_modules/binaryen/bin/wasm-opt
```

The tool verifies its inputs and the resulting artifact against the manifest.
Reproducibility starts from go-pdfium's published Wasm module because its full
PDFium source and Emscripten invocation are not published beside that file.

## Capability boundary

The retained exports are limited to opening a seekable PDF, reading its page
count, rendering a known page, and releasing the associated resources. PDFium's
internally reachable font, image, color, transparency, and page-content handling
remains available.

Adding another server-side PDF capability requires an explicit allowlist and
manifest update; it must not silently restore the full module.

## Updating

When updating go-pdfium or PDFium:

1. update the manifest and dependency pins (`go.mod`, `package.json`, and their
   lock files as needed), then regenerate twice, requiring identical output;
2. compare the full and tailored renderers on tracked fixtures and the private
   corpus;
3. exercise both wazero modes and the timeout/replacement-worker path;
4. review the exported surface, resource impact, and third-party notices.

Licenses and upstream notices are collected in
[`ThirdPartyNotices.txt`](../../ThirdPartyNotices.txt). Binaryen is used only as
a build tool and is not distributed with Polka.
