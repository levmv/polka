// Package pdfcover renders the first page of a PDF to a raster image. It uses a
// usable pdftoppm found on PATH when the Renderer is created, otherwise PDFium
// compiled to WebAssembly and run via wazero. The fallback is pure Go, so Polka
// retains a zero-configuration, no-CGO renderer and its single static binary.
//
// PDFium is heavy (the tailored embedded Wasm is about 4 MiB and is compiled
// once per process), so rendering is an explicit/batch step — wired into import
// and a backfill command, never into a lazy /covers read.
package pdfcover

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
	"github.com/tetratelabs/wazero"

	"github.com/levmv/polka/internal/covers"
)

// DefaultDPI renders a 6×9in trade page at ~900×1350 — matching the covers
// display variant, so a derived thumbnail downsamples without upscaling.
const DefaultDPI = 150

const (
	defaultRenderTimeout = 30 * time.Second
	workerStopTimeout    = 5 * time.Second

	// Rendered cover bytes are immediately decoded and normalized by covers,
	// but bound the provider output before it reaches that step. This matches
	// the HTTP cover-upload limit and is ample for a 150-DPI JPEG cover.
	maxRenderedCoverBytes = 10 << 20

	// PDFium's embedded module declares a 2 GiB maximum. First-page cover
	// rendering does not need anything close to that: a 128 MiB linear-memory
	// ceiling leaves ample room for the corpus's complex scan pages while making
	// a pathological page a recoverable missing-cover warning instead of an
	// unbounded server allocation.
	pdfiumMemoryLimitPages = 128 << 4 // 64 KiB pages per MiB.

	// go-pdfium's WebAssembly file callback currently receives a uint32 offset.
	// Larger books still import and remain downloadable; only their best-effort
	// rendered cover is skipped.
	maxSeekablePDFBytes int64 = 1<<32 - 1
)

type Backend string

const (
	BackendPDFiumWASM Backend = "pdfium-wasm"
	BackendPoppler    Backend = "poppler"
)

type BackendInfo struct {
	Backend    Backend
	Version    string
	Executable string
}

type rendererConfig struct {
	backend       BackendInfo
	command       externalCommand
	poolFactory   func(webassembly.Config) (pdfium.Pool, error)
	renderTimeout time.Duration
}

// Renderer owns the selected cover provider. The choice is made once: a
// usable pdftoppm is retained by absolute path, otherwise the WASM module is
// compiled lazily. A per-document Poppler failure is returned to the importer;
// it is not retried through a second engine. A Renderer is safe for sequential
// use and should be shared by a batch caller, then closed.
type Renderer struct {
	backend       BackendInfo
	command       externalCommand
	poolFactory   func(webassembly.Config) (pdfium.Pool, error)
	renderTimeout time.Duration

	once    sync.Once
	pool    pdfium.Pool
	initErr error
}

var (
	defaultBackendOnce sync.Once
	defaultBackendInfo BackendInfo
	errRenderTimeout   = errors.New("PDF cover render timeout")
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
		return BackendInfo{Backend: BackendPDFiumWASM}
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
	if config.renderTimeout <= 0 {
		config.renderTimeout = defaultRenderTimeout
	}

	return &Renderer{
		backend:       config.backend,
		command:       config.command,
		poolFactory:   config.poolFactory,
		renderTimeout: config.renderTimeout,
	}
}

func (r *Renderer) BackendInfo() BackendInfo { return r.backend }

func (r *Renderer) ensure() error {
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
// given DPI (DefaultDPI when dpi <= 0). Both providers stream/seek over the
// source rather than copying the complete PDF into the Go heap.
func (r *Renderer) RenderFirstPageJPEG(ctx context.Context, pdf io.ReadSeeker, size int64, dpi int) ([]byte, error) {
	if pdf == nil || size <= 0 {
		return nil, errors.New("empty pdf")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if dpi <= 0 {
		dpi = DefaultDPI
	}
	renderCtx, cancel := context.WithTimeoutCause(ctx, r.renderTimeout, errRenderTimeout)
	defer cancel()
	if _, err := pdf.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek pdf: %w", err)
	}

	var (
		rendered []byte
		err      error
	)
	switch r.backend.Backend {
	case BackendPoppler:
		rendered, err = r.renderPoppler(renderCtx, pdf, size, dpi)
	case BackendPDFiumWASM:
		if size > maxSeekablePDFBytes {
			return nil, fmt.Errorf("pdf is too large for the seekable WASM renderer (%d bytes)", size)
		}
		rendered, err = r.renderWASM(renderCtx, pdf, size, dpi)
	default:
		return nil, fmt.Errorf("unknown PDF cover backend %q", r.backend.Backend)
	}
	if err != nil {
		return nil, err
	}
	if len(rendered) > maxRenderedCoverBytes {
		return nil, fmt.Errorf("rendered PDF cover exceeds %d bytes", maxRenderedCoverBytes)
	}
	if _, err := covers.Inspect(rendered); err != nil {
		return nil, fmt.Errorf("%s returned invalid JPEG: %w", r.backend.Backend, err)
	}
	return rendered, nil
}

func (r *Renderer) renderWASM(ctx context.Context, pdf io.ReadSeeker, size int64, dpi int) ([]byte, error) {
	if err := r.ensure(); err != nil {
		return nil, err
	}

	instance, err := r.pool.GetInstanceWithContext(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, renderContextError(ctx, "render PDF page 1", r.renderTimeout)
		}
		return nil, fmt.Errorf("get pdfium instance: %w", err)
	}

	type renderResult struct {
		bytes []byte
		err   error
	}
	done := make(chan renderResult, 1)
	go func() {
		rendered, renderErr := renderWASMInstance(instance, pdf, size, dpi)
		done <- renderResult{bytes: rendered, err: renderErr}
	}()

	select {
	case result := <-done:
		closeErr := instance.Close()
		if result.err != nil {
			return nil, result.err
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close pdfium instance: %w", closeErr)
		}
		return result.bytes, nil
	case <-ctx.Done():
		contextErr := renderContextError(ctx, "render PDF page 1", r.renderTimeout)
		killDone := make(chan error, 1)
		go func() {
			killDone <- instance.Kill()
		}()

		select {
		case killErr := <-killDone:
			select {
			case <-done:
			case <-time.After(workerStopTimeout):
				return nil, fmt.Errorf("%w; WASM call did not stop", contextErr)
			}
			if killErr != nil {
				return nil, fmt.Errorf("%w; reset worker: %w", contextErr, killErr)
			}
			return nil, contextErr
		case <-time.After(workerStopTimeout):
			return nil, fmt.Errorf("%w; could not reset WASM worker", contextErr)
		}
	}
}

func renderContextError(ctx context.Context, action string, timeout time.Duration) error {
	if errors.Is(context.Cause(ctx), errRenderTimeout) {
		return fmt.Errorf("%s timed out after %s: %w", action, timeout, context.DeadlineExceeded)
	}
	return fmt.Errorf("%s: %w", action, ctx.Err())
}

func renderWASMInstance(instance pdfium.Pdfium, pdf io.ReadSeeker, size int64, dpi int) ([]byte, error) {
	doc, err := instance.OpenDocument(&requests.OpenDocument{
		FileReader:     pdf,
		FileReaderSize: size,
	})
	if err != nil {
		return nil, fmt.Errorf("open pdf: %w", err)
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
		return nil, fmt.Errorf("render page 1: %w", err)
	}
	if res.ImageBytes == nil {
		return nil, errors.New("pdfium returned no image bytes")
	}
	return *res.ImageBytes, nil
}

// Close releases the pdfium pool. Safe to call when the pool was never
// initialized (no PDF was ever rendered).
func (r *Renderer) Close() error {
	if r.pool != nil {
		return r.pool.Close()
	}
	return nil
}
