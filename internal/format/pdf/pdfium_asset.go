package pdf

import _ "embed"

// pdfiumCoverWASM is a post-link reduction of go-pdfium's PDFium 8044 module.
// Its manifest, derivation tool, capability boundary, and notices are tracked
// alongside the build tool; make pdfium-wasm prepares and verifies the artifact.
//
//go:embed generated/pdfium-cover.wasm
var pdfiumCoverWASM []byte

// PDFium version pinned by pdfium-wasm.json.
const pdfiumVersion = "8044"
