// Package pdfcover renders the first page of a PDF to a raster image. It uses a
// usable pdftoppm found on PATH when the Renderer is created, otherwise PDFium
// compiled to WebAssembly and run via wazero. The fallback is pure Go, so Polka
// retains a zero-configuration, no-CGO renderer and its single static binary.
//
// PDFium also supplies page counts when internal/format cannot resolve them.
// Cover rendering runs during import or explicit maintenance; serving an
// existing cover never starts a renderer.
package pdfcover

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/responses"
	"github.com/klippa-app/go-pdfium/webassembly"
	"github.com/tetratelabs/wazero"

	"github.com/levmv/polka/internal/covers"
)

// DefaultDPI renders a 6×9in trade page at ~900×1350 — matching the covers
// display variant, so a derived thumbnail downsamples without upscaling.
const DefaultDPI = 150

const (
	defaultOperationTimeout = 30 * time.Second
	workerStopTimeout       = 5 * time.Second

	// Match the cover-upload limit before decoding the provider's output.
	maxRenderedCoverBytes = 10 << 20

	// Limit an expensive document to 128 MiB of WASM linear memory;
	// the module's declared maximum is 2 GiB.
	pdfiumMemoryLimitPages = 128 << 4 // 16 WebAssembly memory pages per MiB.

	// go-pdfium's WebAssembly file callback currently receives a uint32 offset.
	// Larger books still import and remain downloadable; WASM operations are skipped.
	maxSeekablePDFBytes int64 = 1<<32 - 1
)

type Backend string

const (
	BackendPDFiumWASM Backend = "pdfium-wasm"
	BackendPoppler    Backend = "poppler"
)

type BackendInfo struct {
	Backend    Backend `json:"backend"`
	Version    string  `json:"version"`
	Executable string  `json:"executable,omitempty"`
	WASMSHA256 string  `json:"wasm_sha256,omitempty"`
}

type rendererConfig struct {
	backend          BackendInfo
	command          externalCommand
	poolFactory      func(webassembly.Config) (pdfium.Pool, error)
	operationTimeout time.Duration
}

// Renderer retains one cover backend; document failures do not switch engines.
// CountPages always uses PDFium, including when covers use Poppler. The WASM
// module compiles on the first PDFium operation. Share a Renderer across
// sequential operations and close it after the batch.
type Renderer struct {
	backend          BackendInfo
	command          externalCommand
	poolFactory      func(webassembly.Config) (pdfium.Pool, error)
	operationTimeout time.Duration

	once    sync.Once
	pool    pdfium.Pool
	initErr error
}

var (
	defaultBackendOnce  sync.Once
	defaultBackendInfo  BackendInfo
	errOperationTimeout = errors.New("PDF operation timeout")
)

func NewRenderer() *Renderer {
	defaultBackendOnce.Do(func() {
		defaultBackendInfo = detectBackend(externalCommandContext, nil)
	})
	return newRenderer(rendererConfig{backend: defaultBackendInfo})
}

func detectBackend(command externalCommand, lookPath func(string) (string, error)) BackendInfo {
	info, err := detectPoppler(command, lookPath)
	if err != nil {
		return BackendInfo{
			Backend: BackendPDFiumWASM, Version: pdfiumVersion,
			WASMSHA256: fmt.Sprintf("%x", sha256.Sum256(pdfiumCoverWASM)),
		}
	}
	return info
}

func newRenderer(config rendererConfig) *Renderer {
	if config.command == nil {
		config.command = externalCommandContext
	}
	if config.poolFactory == nil {
		config.poolFactory = webassembly.InitWithWASM
	}
	if config.operationTimeout <= 0 {
		config.operationTimeout = defaultOperationTimeout
	}

	return &Renderer{
		backend:          config.backend,
		command:          config.command,
		poolFactory:      config.poolFactory,
		operationTimeout: config.operationTimeout,
	}
}

func (r *Renderer) BackendInfo() BackendInfo { return r.backend }

func (r *Renderer) ensureWASM() error {
	r.once.Do(func() {
		pool, err := r.poolFactory(webassembly.Config{
			MinIdle:       1,
			MaxIdle:       1,
			MaxTotal:      1,
			WASM:          pdfiumCoverWASM,
			FSConfig:      wazero.NewFSConfig(),
			RuntimeConfig: wazero.NewRuntimeConfig().WithMemoryLimitPages(pdfiumMemoryLimitPages).WithCloseOnContextDone(true),
		})
		if err != nil {
			r.initErr = fmt.Errorf("init pdfium: %w", err)
			return
		}
		r.pool = pool
	})
	return r.initErr
}

// RenderFirstPageJPEG renders page 1 of a seekable PDF to JPEG bytes at the
// given DPI (DefaultDPI when dpi <= 0). The WASM provider also returns the page
// count from the same document, even if rendering subsequently fails. Poppler
// returns zero for the count. Both providers stream/seek over the source.
func (r *Renderer) RenderFirstPageJPEG(ctx context.Context, pdf io.ReadSeeker, size int64, dpi int) ([]byte, int, error) {
	if pdf == nil || size <= 0 {
		return nil, 0, errors.New("empty pdf")
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if dpi <= 0 {
		dpi = DefaultDPI
	}
	renderCtx, cancel := context.WithTimeoutCause(ctx, r.operationTimeout, errOperationTimeout)
	defer cancel()
	if _, err := pdf.Seek(0, io.SeekStart); err != nil {
		return nil, 0, fmt.Errorf("seek pdf: %w", err)
	}

	var (
		result pdfResult
		err    error
	)
	switch r.backend.Backend {
	case BackendPoppler:
		result.jpeg, err = r.renderPoppler(renderCtx, pdf, size, dpi)
	case BackendPDFiumWASM:
		if size > maxSeekablePDFBytes {
			return nil, 0, fmt.Errorf("pdf is too large for the seekable WASM renderer (%d bytes)", size)
		}
		result, err = r.renderWASM(renderCtx, pdf, size, dpi)
	default:
		return nil, 0, fmt.Errorf("unknown PDF cover backend %q", r.backend.Backend)
	}
	if err != nil {
		return nil, result.pages, err
	}
	if len(result.jpeg) > maxRenderedCoverBytes {
		return nil, result.pages, fmt.Errorf("rendered PDF cover exceeds %d bytes", maxRenderedCoverBytes)
	}
	if _, err := covers.Inspect(result.jpeg); err != nil {
		return nil, result.pages, fmt.Errorf("%s returned invalid JPEG: %w", r.backend.Backend, err)
	}
	return result.jpeg, result.pages, nil
}

func (r *Renderer) renderWASM(ctx context.Context, pdf io.ReadSeeker, size int64, dpi int) (pdfResult, error) {
	return r.withWASMInstance(ctx, "render PDF page 1", func(instance pdfOperations) (pdfResult, error) {
		return renderWASMInstance(instance, pdf, size, dpi)
	})
}

// CountPages is the fallback for counts unavailable to internal/format.
// It uses PDFium even when covers use Poppler.
func (r *Renderer) CountPages(ctx context.Context, pdf io.ReadSeeker, size int64) (int, error) {
	if pdf == nil || size <= 0 || size > maxSeekablePDFBytes {
		return 0, errors.New("PDF size is outside the seekable parser limit")
	}
	ctx, cancel := context.WithTimeoutCause(ctx, r.operationTimeout, errOperationTimeout)
	defer cancel()
	result, err := r.withWASMInstance(ctx, "count PDF pages", func(instance pdfOperations) (pdfResult, error) {
		doc, err := instance.OpenDocument(&requests.OpenDocument{FileReader: pdf, FileReaderSize: size})
		if err != nil {
			return pdfResult{}, err
		}
		count, err := instance.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
		if err != nil {
			return pdfResult{}, err
		}
		return pdfResult{pages: count.PageCount}, nil
	})
	if err != nil {
		return 0, err
	}
	return result.pages, nil
}

type pdfResult struct {
	jpeg  []byte
	pages int
}

type pdfOperations interface {
	OpenDocument(*requests.OpenDocument) (*responses.OpenDocument, error)
	FPDF_GetPageCount(*requests.FPDF_GetPageCount) (*responses.FPDF_GetPageCount, error)
	RenderToFile(*requests.RenderToFile) (*responses.RenderToFile, error)
}

func (r *Renderer) withWASMInstance(ctx context.Context, action string, run func(pdfOperations) (pdfResult, error)) (pdfResult, error) {
	if err := ctx.Err(); err != nil {
		return pdfResult{}, err
	}
	if err := r.ensureWASM(); err != nil {
		return pdfResult{}, err
	}

	instance, err := r.pool.GetInstanceWithContext(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return pdfResult{}, operationContextError(ctx, action, r.operationTimeout)
		}
		return pdfResult{}, fmt.Errorf("get pdfium instance: %w", err)
	}
	// go-pdfium's pool wrapper races on its closed flag when Kill interrupts a
	// call. Capture the WASM implementation before starting; only this owner
	// touches the wrapper, while operations use the worker that Kill cancels.
	operations, ok := instance.GetImplementation().(pdfOperations)
	if !ok {
		_ = instance.Close()
		return pdfResult{}, errors.New("unexpected PDFium WASM implementation")
	}

	type callResult struct {
		output pdfResult
		err    error
	}
	done := make(chan callResult, 1)
	go func() {
		var call callResult
		defer func() {
			// Keep the pool wrapper's panic-to-error boundary for malformed PDFs.
			if recovered := recover(); recovered != nil {
				call.err = fmt.Errorf("%s: %v", action, recovered)
			}
			done <- call
		}()
		call.output, call.err = run(operations)
	}()

	select {
	case call := <-done:
		// go-pdfium closes every document via FPDF_CloseDocument here. With
		// ReuseWorkers left false, Close also discards the worker's linear memory;
		// the pool retains the compiled module for the next operation.
		closeErr := instance.Close()
		if call.err != nil {
			return call.output, call.err
		}
		if closeErr != nil {
			return call.output, fmt.Errorf("close pdfium instance: %w", closeErr)
		}
		return call.output, nil
	case <-ctx.Done():
		// Interrupt WASM, then wait for the call to release the source reader.
		contextErr := operationContextError(ctx, action, r.operationTimeout)
		killDone := make(chan error, 1)
		go func() {
			killDone <- instance.Kill()
		}()

		select {
		case killErr := <-killDone:
			select {
			case <-done:
			case <-time.After(workerStopTimeout):
				return pdfResult{}, fmt.Errorf("%w; WASM call did not stop", contextErr)
			}
			if killErr != nil {
				return pdfResult{}, fmt.Errorf("%w; reset worker: %w", contextErr, killErr)
			}
			return pdfResult{}, contextErr
		case <-time.After(workerStopTimeout):
			return pdfResult{}, fmt.Errorf("%w; could not reset WASM worker", contextErr)
		}
	}
}

func operationContextError(ctx context.Context, action string, timeout time.Duration) error {
	if errors.Is(context.Cause(ctx), errOperationTimeout) {
		return fmt.Errorf("%s timed out after %s: %w", action, timeout, context.DeadlineExceeded)
	}
	return fmt.Errorf("%s: %w", action, ctx.Err())
}

func renderWASMInstance(instance pdfOperations, pdf io.ReadSeeker, size int64, dpi int) (pdfResult, error) {
	doc, err := instance.OpenDocument(&requests.OpenDocument{
		FileReader:     pdf,
		FileReaderSize: size,
	})
	if err != nil {
		return pdfResult{}, fmt.Errorf("open pdf: %w", err)
	}
	result := pdfResult{}
	if pages, err := instance.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document}); err == nil {
		result.pages = pages.PageCount
	}

	res, err := instance.RenderToFile(&requests.RenderToFile{
		RenderPageInDPI: &requests.RenderPageInDPI{
			DPI:  dpi,
			Page: requests.Page{ByIndex: &requests.PageByIndex{Document: doc.Document, Index: 0}},
		},
		OutputFormat:  requests.RenderToFileOutputFormatJPG,
		OutputTarget:  requests.RenderToFileOutputTargetBytes,
		OutputQuality: 90,
	})
	if err != nil {
		return result, fmt.Errorf("render page 1: %w", err)
	}
	if res.ImageBytes == nil {
		return result, errors.New("pdfium returned no image bytes")
	}
	result.jpeg = *res.ImageBytes
	return result, nil
}

// Close releases the pdfium pool. Safe to call when the pool was never
// initialized (no PDF operation has run).
func (r *Renderer) Close() error {
	if r.pool != nil {
		return r.pool.Close()
	}
	return nil
}
