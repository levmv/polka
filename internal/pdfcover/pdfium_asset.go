package pdfcover

import _ "embed"

// pdfiumCoverWASM is a post-link reduction of go-pdfium's PDFium 7961 module.
// Its manifest, derivation tool, capability boundary, and notices are tracked
// alongside the artifact; do not replace it with an unreviewed Wasm binary.
//
//go:embed pdfium-cover.wasm
var pdfiumCoverWASM []byte
