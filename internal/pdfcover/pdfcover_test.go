package pdfcover

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/webassembly"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
)

func TestWASMFallbackRendersWithoutHostFilesystem(t *testing.T) {
	blocker := &oneShotRenderBlocker{}
	r := newRenderer(rendererConfig{
		backend:       BackendInfo{Backend: BackendPDFiumWASM},
		renderTimeout: 15 * time.Second,
		poolFactory: func(config webassembly.Config) (pdfium.Pool, error) {
			config.Context = experimental.WithFunctionListenerFactory(context.Background(), blocker)
			return webassembly.InitWithWASM(config)
		},
	})
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})

	pdf := testBlankPDF()
	rendered, err := r.RenderFirstPageJPEG(context.Background(), bytes.NewReader(pdf), int64(len(pdf)), 72)
	if err != nil {
		t.Fatalf("RenderFirstPageJPEG: %v", err)
	}
	if len(rendered) == 0 {
		t.Fatalf("RenderFirstPageJPEG returned no bytes")
	}

	// Block one real PDFium render inside wazero. The deadline must cancel that
	// call, invalidate its worker, and leave the pool able to create a clean
	// instance for the next document.
	blocker.arm()
	r.renderTimeout = 100 * time.Millisecond
	if _, err := r.RenderFirstPageJPEG(context.Background(), bytes.NewReader(pdf), int64(len(pdf)), 72); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("blocked RenderFirstPageJPEG error = %v, want timeout", err)
	}
	if !blocker.didBlock() {
		t.Fatalf("controlled PDFium render blocker was not reached")
	}

	r.renderTimeout = 15 * time.Second
	if _, err := r.RenderFirstPageJPEG(context.Background(), bytes.NewReader(pdf), int64(len(pdf)), 72); err != nil {
		t.Fatalf("RenderFirstPageJPEG after worker reset: %v", err)
	}

	// Parent cancellation uses the same worker-reset owner as the private
	// render deadline and must likewise leave the pool reusable.
	blocker.arm()
	ctx, cancel := context.WithCancel(context.Background())
	renderDone := make(chan error, 1)
	go func() {
		_, err := r.RenderFirstPageJPEG(ctx, bytes.NewReader(pdf), int64(len(pdf)), 72)
		renderDone <- err
	}()
	blocker.waitUntilBlocked(t)
	cancel()
	select {
	case err := <-renderDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled RenderFirstPageJPEG error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled RenderFirstPageJPEG did not stop")
	}
	if _, err := r.RenderFirstPageJPEG(context.Background(), bytes.NewReader(pdf), int64(len(pdf)), 72); err != nil {
		t.Fatalf("RenderFirstPageJPEG after cancellation reset: %v", err)
	}
}

func TestEmbeddedPDFiumWASMMatchesManifest(t *testing.T) {
	type manifest struct {
		Output struct {
			Bytes  int    `json:"bytes"`
			SHA256 string `json:"sha256"`
		} `json:"output"`
	}

	manifestData, err := os.ReadFile("pdfium-wasm.json")
	if err != nil {
		t.Fatalf("read PDFium Wasm manifest: %v", err)
	}
	var expected manifest
	if err := json.Unmarshal(manifestData, &expected); err != nil {
		t.Fatalf("decode PDFium Wasm manifest: %v", err)
	}
	if len(pdfiumCoverWASM) != expected.Output.Bytes {
		t.Fatalf("embedded PDFium Wasm is %d bytes, manifest says %d", len(pdfiumCoverWASM), expected.Output.Bytes)
	}
	sum := sha256.Sum256(pdfiumCoverWASM)
	if got := fmt.Sprintf("%x", sum); got != expected.Output.SHA256 {
		t.Fatalf("embedded PDFium Wasm SHA-256 = %s, manifest says %s", got, expected.Output.SHA256)
	}
}

type oneShotRenderBlocker struct {
	mu      sync.Mutex
	armed   bool
	blocked bool
	entered chan struct{}
}

func (b *oneShotRenderBlocker) arm() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.armed = true
	b.blocked = false
	b.entered = make(chan struct{})
}

func (b *oneShotRenderBlocker) didBlock() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.blocked
}

func (b *oneShotRenderBlocker) waitUntilBlocked(t *testing.T) {
	t.Helper()
	b.mu.Lock()
	entered := b.entered
	b.mu.Unlock()
	if entered == nil {
		t.Fatal("render blocker was not armed")
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("PDFium render blocker was not reached")
	}
}

func (b *oneShotRenderBlocker) NewFunctionListener(def api.FunctionDefinition) experimental.FunctionListener {
	if !slices.Contains(def.ExportNames(), "FPDF_RenderPageBitmap") {
		return nil
	}
	return experimental.FunctionListenerFunc(b.beforeRender)
}

func (b *oneShotRenderBlocker) beforeRender(ctx context.Context, _ api.Module, _ api.FunctionDefinition, _ []uint64, _ experimental.StackIterator) {
	b.mu.Lock()
	if !b.armed {
		b.mu.Unlock()
		return
	}
	b.armed = false
	b.blocked = true
	close(b.entered)
	b.mu.Unlock()
	<-ctx.Done()
}

func TestRendererUsesProbedPoppler(t *testing.T) {
	jpegBytes := testJPEG(t)
	var renderArgs []string
	command := func(_ context.Context, _ string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
		if slices.Equal(args, []string{"-v"}) {
			_, _ = io.WriteString(stderr, "pdftoppm version 24.01.0\n")
			return nil
		}
		renderArgs = slices.Clone(args)
		gotInput, err := io.ReadAll(stdin)
		if err != nil {
			t.Fatalf("read fake stdin: %v", err)
		}
		if string(gotInput) != "pdf" {
			t.Fatalf("fake stdin = %q, want pdf", gotInput)
		}
		_, _ = stdout.Write(jpegBytes)
		return nil
	}
	backend := detectBackend(command, func(string) (string, error) {
		return filepath.Join("tools", "pdftoppm"), nil
	})
	r := newRenderer(rendererConfig{
		backend:       backend,
		command:       command,
		renderTimeout: time.Second,
	})

	info := r.BackendInfo()
	if info.Backend != BackendPoppler || info.Version != "pdftoppm version 24.01.0" || !filepath.IsAbs(info.Executable) {
		t.Fatalf("BackendInfo = %+v, want probed Poppler with absolute path", info)
	}
	got, err := r.RenderFirstPageJPEG(context.Background(), bytes.NewReader([]byte("pdf")), 3, 96)
	if err != nil {
		t.Fatalf("RenderFirstPageJPEG: %v", err)
	}
	if !bytes.Equal(got, jpegBytes) {
		t.Fatalf("rendered bytes differ from fake Poppler output")
	}
	wantArgs := []string{"-f", "1", "-l", "1", "-singlefile", "-r", "96", "-jpeg", "-jpegopt", "quality=90", "-"}
	if !slices.Equal(renderArgs, wantArgs) {
		t.Fatalf("pdftoppm args = %q, want %q", renderArgs, wantArgs)
	}
}

func TestRendererFallsBackToWASMWhenPopplerProbeFails(t *testing.T) {
	probeErr := errors.New("broken executable")
	var captured webassembly.Config
	command := func(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error {
		return probeErr
	}
	backend := detectBackend(command, func(string) (string, error) {
		return "pdftoppm", nil
	})
	r := newRenderer(rendererConfig{
		backend: backend,
		command: command,
		poolFactory: func(config webassembly.Config) (pdfium.Pool, error) {
			captured = config
			return nil, errors.New("stop after config capture")
		},
	})

	if got := r.BackendInfo().Backend; got != BackendPDFiumWASM {
		t.Fatalf("backend = %q, want %q", got, BackendPDFiumWASM)
	}
	if err := r.ensure(); err == nil {
		t.Fatalf("ensure unexpectedly succeeded")
	}
	// go-pdfium mounts the host root only when FSConfig is nil. Passing an
	// explicitly empty config is therefore the security boundary.
	if captured.FSConfig == nil {
		t.Fatalf("WASM FSConfig is nil; go-pdfium would mount the host root")
	}
	if captured.RuntimeConfig == nil {
		t.Fatalf("WASM RuntimeConfig is nil")
	}
}

func TestPopplerDocumentFailureDoesNotRetryWithWASM(t *testing.T) {
	renderErr := errors.New("damaged PDF")
	poolCalls := 0
	command := func(_ context.Context, _ string, args []string, _ io.Reader, _, stderr io.Writer) error {
		if slices.Equal(args, []string{"-v"}) {
			_, _ = io.WriteString(stderr, "pdftoppm version 24.01.0\n")
			return nil
		}
		return renderErr
	}
	backend := detectBackend(command, func(string) (string, error) {
		return "pdftoppm", nil
	})
	r := newRenderer(rendererConfig{
		backend: backend,
		command: command,
		poolFactory: func(webassembly.Config) (pdfium.Pool, error) {
			poolCalls++
			return nil, errors.New("unexpected WASM initialization")
		},
	})

	_, err := r.RenderFirstPageJPEG(context.Background(), bytes.NewReader([]byte("pdf")), 3, 0)
	if !errors.Is(err, renderErr) {
		t.Fatalf("RenderFirstPageJPEG error = %v, want wrapped render error", err)
	}
	if poolCalls != 0 {
		t.Fatalf("WASM pool initialized %d times after Poppler document failure", poolCalls)
	}
}

func TestPopplerRenderTimeout(t *testing.T) {
	command := func(ctx context.Context, _ string, args []string, _ io.Reader, _, stderr io.Writer) error {
		if slices.Equal(args, []string{"-v"}) {
			_, _ = io.WriteString(stderr, "pdftoppm version 24.01.0\n")
			return nil
		}
		<-ctx.Done()
		return ctx.Err()
	}
	backend := detectBackend(command, func(string) (string, error) {
		return "pdftoppm", nil
	})
	r := newRenderer(rendererConfig{
		backend:       backend,
		command:       command,
		renderTimeout: 10 * time.Millisecond,
	})

	_, err := r.RenderFirstPageJPEG(context.Background(), bytes.NewReader([]byte("pdf")), 3, 0)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("RenderFirstPageJPEG error = %v, want timeout", err)
	}
}

func TestPopplerRenderObservesParentCancellation(t *testing.T) {
	renderStarted := make(chan struct{})
	command := func(ctx context.Context, _ string, args []string, _ io.Reader, _, stderr io.Writer) error {
		if slices.Equal(args, []string{"-v"}) {
			_, _ = io.WriteString(stderr, "pdftoppm version 24.01.0\n")
			return nil
		}
		close(renderStarted)
		<-ctx.Done()
		return ctx.Err()
	}
	backend := detectBackend(command, func(string) (string, error) {
		return "pdftoppm", nil
	})
	r := newRenderer(rendererConfig{
		backend:       backend,
		command:       command,
		renderTimeout: time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-renderStarted
		cancel()
	}()

	_, err := r.RenderFirstPageJPEG(ctx, bytes.NewReader([]byte("pdf")), 3, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RenderFirstPageJPEG error = %v, want context.Canceled", err)
	}
}

func TestLimitedBufferBoundsProviderOutput(t *testing.T) {
	var dst limitedBuffer
	dst.limit = 4
	n, err := dst.Write([]byte("abcdef"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 6 || dst.String() != "abcd" || !dst.exceeded {
		t.Fatalf("limited buffer = n %d, bytes %q, exceeded %v", n, dst.String(), dst.exceeded)
	}
}

func testJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 2, 3))
	img.Set(0, 0, color.NRGBA{R: 0x80, G: 0x40, B: 0x20, A: 0xff})
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode test JPEG: %v", err)
	}
	return encoded.Bytes()
}

func testBlankPDF() []byte {
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 72 72] /Resources << >> /Contents 4 0 R >>",
		"<< /Length 0 >>\nstream\n\nendstream",
	}
	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for i, object := range objects {
		offsets[i+1] = pdf.Len()
		fmt.Fprintf(&pdf, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := pdf.Len()
	fmt.Fprintf(&pdf, "xref\n0 %d\n", len(offsets))
	pdf.WriteString("0000000000 65535 f \n")
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&pdf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return pdf.Bytes()
}
