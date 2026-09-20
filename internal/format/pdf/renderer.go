// Package pdf reads PDF metadata and page counts and renders covers. It uses a
// usable pdftoppm found on PATH when a cover is first rendered, otherwise PDFium
// compiled to WebAssembly and run via wazero. The fallback is pure Go, so Polka
// retains a zero-configuration, no-CGO renderer and its single static binary.
//
// PDFium also supplies page counts the native parser cannot resolve.
// Cover rendering runs during import or explicit maintenance; serving an
// existing cover never starts a renderer.
package pdf

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"sync"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/responses"
	"github.com/klippa-app/go-pdfium/webassembly"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"

	"github.com/levmv/polka/internal/covers"
)

// DefaultCoverSize bounds the longest edge in pixels, matching the display
// cover's maximum height regardless of the PDF page's physical dimensions.
const DefaultCoverSize = 1350

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

// Renderer selects a cover backend on its first render and retains it;
// document failures do not switch engines. Construction performs no IO.
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

var defaultBackend = sync.OnceValue(func() BackendInfo {
	return detectBackend(externalCommandContext, nil)
})

var errOperationTimeout = errors.New("PDF operation timeout")

func NewRenderer() *Renderer {
	return newRenderer(rendererConfig{})
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

// BackendInfo reports the selected cover backend, or zero before the first render.
// Reading metadata and counting pages do not select a cover backend.
func (r *Renderer) BackendInfo() BackendInfo { return r.backend }

func (r *Renderer) ensureWASM() error {
	r.once.Do(func() {
		pool, err := r.poolFactory(webassembly.Config{
			MinIdle:  1,
			MaxIdle:  1,
			MaxTotal: 1,
			WASM:     pdfiumCoverWASM,
			FSConfig: wazero.NewFSConfig(),
			// PDFium's setjmp/longjmp uses WebAssembly exception handling.
			RuntimeConfig: wazero.NewRuntimeConfig().
				WithCoreFeatures(api.CoreFeaturesV2 | experimental.CoreFeaturesExceptionHandling).
				WithMemoryLimitPages(pdfiumMemoryLimitPages).WithCloseOnContextDone(true),
		})
		if err != nil {
			r.initErr = fmt.Errorf("init pdfium: %w", err)
			return
		}
		r.pool = pool
	})
	return r.initErr
}

// RenderCoverJPEG renders the first nonblank opening page to JPEG bytes at the
// given maximum edge in pixels (DefaultCoverSize when maxSize <= 0), preserving
// aspect ratio. The WASM provider also returns the page count from the same
// document, even if rendering subsequently fails. Poppler returns zero for the
// count. Both providers stream/seek over the source.
// Lookahead is limited to covers.CoverPageLimit; the first page is the fallback.
func (r *Renderer) RenderCoverJPEG(ctx context.Context, pdf io.ReadSeeker, size int64, maxSize int) ([]byte, int, error) {
	if pdf == nil || size <= 0 {
		return nil, 0, errors.New("empty pdf")
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if maxSize <= 0 {
		maxSize = DefaultCoverSize
	}
	renderCtx, cancel := context.WithTimeoutCause(ctx, r.operationTimeout, errOperationTimeout)
	defer cancel()
	if _, err := pdf.Seek(0, io.SeekStart); err != nil {
		return nil, 0, fmt.Errorf("seek pdf: %w", err)
	}
	if r.backend.Backend == "" {
		r.backend = defaultBackend()
	}

	var (
		result pdfResult
		err    error
	)
	switch r.backend.Backend {
	case BackendPoppler:
		result.jpeg, err = r.renderCoverPages(renderCtx, covers.CoverPageLimit, func(page int) ([]byte, error) {
			if _, err := pdf.Seek(0, io.SeekStart); err != nil {
				return nil, err
			}
			return r.renderPoppler(renderCtx, pdf, size, maxSize, page+1)
		})
	case BackendPDFiumWASM:
		if size > maxSeekablePDFBytes {
			return nil, 0, fmt.Errorf("pdf is too large for the seekable WASM renderer (%d bytes)", size)
		}
		result, err = r.renderWASM(renderCtx, pdf, size, maxSize)
	default:
		return nil, 0, fmt.Errorf("unknown PDF cover backend %q", r.backend.Backend)
	}
	if err != nil {
		return nil, result.pages, err
	}
	return result.jpeg, result.pages, nil
}

func (r *Renderer) coverIsBlank(data []byte) (bool, error) {
	if len(data) > maxRenderedCoverBytes {
		return false, fmt.Errorf("rendered PDF cover exceeds %d bytes", maxRenderedCoverBytes)
	}
	info, err := covers.Inspect(data)
	if err != nil {
		return false, fmt.Errorf("%s returned invalid JPEG: %w", r.backend.Backend, err)
	}
	// Poppler can report success for a damaged PDF while returning one pixel.
	if info.Width == 1 && info.Height == 1 {
		return false, fmt.Errorf("%s returned a degenerate 1x1 PDF cover", r.backend.Backend)
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return false, fmt.Errorf("%s returned invalid JPEG: %w", r.backend.Backend, err)
	}
	return covers.IsBlankPage(img), nil
}

func (r *Renderer) renderCoverPages(ctx context.Context, pages int, render func(int) ([]byte, error)) ([]byte, error) {
	var first []byte
	for page := range min(pages, covers.CoverPageLimit) {
		data, err := render(page)
		blank := false
		if err == nil {
			blank, err = r.coverIsBlank(data)
		}
		if ctx.Err() != nil {
			return nil, operationContextError(ctx, "render PDF cover", r.operationTimeout)
		}
		if err != nil {
			// A failed optional lookahead must not discard a usable first page.
			if first != nil {
				return first, nil
			}
			return nil, err
		}
		if !blank {
			return data, nil
		}
		if page == 0 {
			first = data
		}
	}
	return first, nil
}

func (r *Renderer) renderWASM(ctx context.Context, pdf io.ReadSeeker, size int64, maxSize int) (pdfResult, error) {
	return r.withWASMInstance(ctx, "render PDF cover", func(instance pdfOperations) (pdfResult, error) {
		return r.renderWASMInstance(ctx, instance, pdf, size, maxSize)
	})
}

// CountPages is the fallback for counts unavailable to the native parser.
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

func (r *Renderer) renderWASMInstance(ctx context.Context, instance pdfOperations, pdf io.ReadSeeker, size int64, maxSize int) (pdfResult, error) {
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

	result.jpeg, err = r.renderCoverPages(ctx, max(result.pages, 1), func(page int) ([]byte, error) {
		res, err := instance.RenderToFile(&requests.RenderToFile{
			RenderPageInPixels: &requests.RenderPageInPixels{
				Width:  maxSize,
				Height: maxSize,
				Page:   requests.Page{ByIndex: &requests.PageByIndex{Document: doc.Document, Index: page}},
			},
			OutputFormat:  requests.RenderToFileOutputFormatJPG,
			OutputTarget:  requests.RenderToFileOutputTargetBytes,
			OutputQuality: 90,
		})
		if err != nil {
			return nil, fmt.Errorf("render page %d: %w", page+1, err)
		}
		if res.ImageBytes == nil {
			return nil, errors.New("pdfium returned no image bytes")
		}
		return *res.ImageBytes, nil
	})
	return result, err
}

// Close releases the pdfium pool. Safe to call when the pool was never
// initialized (no PDF operation has run).
func (r *Renderer) Close() error {
	if r.pool != nil {
		return r.pool.Close()
	}
	return nil
}
