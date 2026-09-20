# PDFium WebAssembly module

Polka embeds `generated/pdfium-cover.wasm`, a reduced version of go-pdfium's
published module, for PDF cover fallback and page counting.
[`pdfium-wasm.json`](pdfium-wasm.json) pins the versions, hashes, imports, and
exports. JPEG encoding uses Go to avoid an extra pixel copy inside WASM.

## Build

`make build` and `make test` prepare the module automatically and verify it
before reuse. Preparation uses Node.js and downloads the pinned go-pdfium
module and Binaryen `wasm-opt` tool as needed. The first build requires network
access; generated files are cached locally and ignored by Git.

To prepare just the module:

```sh
make pdfium-wasm
```

To force regeneration:

```sh
go run ./internal/format/pdf/wasmtool prepare -force
```

## Updating

1. Update `go.mod`, `go.sum`, and the manifest pins; regenerate twice and require
   identical output.
2. Compare the full and reduced renderers on public fixtures and, when
   available, the private corpus.
3. Check both wazero compiler and interpreter modes, including cancellation,
   timeouts, and worker recovery.
4. Review exports, resource use, and
   [third-party notices](../../../ThirdPartyNotices.txt).
