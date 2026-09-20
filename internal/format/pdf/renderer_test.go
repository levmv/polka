package pdf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"os/exec"
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
	// Compilation under -race is slow; the forced deadline below stays short.
	r := newRenderer(rendererConfig{
		backend:          BackendInfo{Backend: BackendPDFiumWASM},
		operationTimeout: time.Minute,
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
	rendered, pages, err := r.RenderCoverJPEG(context.Background(), bytes.NewReader(pdf), int64(len(pdf)), 72)
	if err != nil {
		t.Fatalf("RenderCoverJPEG: %v", err)
	}
	if len(rendered) == 0 || pages != 1 {
		t.Fatalf("RenderCoverJPEG returned %d bytes and %d pages; want a cover and one page", len(rendered), pages)
	}
	// The one-inch page at 1 DPI produces a single pixel even with a healthy
	// renderer. Reject it as a cover while retaining the known page count.
	rendered, pages, err = r.RenderCoverJPEG(t.Context(), bytes.NewReader(pdf), int64(len(pdf)), 1)
	if err == nil || !strings.Contains(err.Error(), "1x1") || rendered != nil || pages != 1 {
		t.Fatalf("single-pixel render = %d bytes, %d pages, %v; want no cover, one page and dimension error", len(rendered), pages, err)
	}

	// Block one real PDFium render inside wazero. The deadline must cancel that
	// call, invalidate its worker, and leave the pool able to create a clean
	// instance for the next document.
	blocker.arm()
	r.operationTimeout = 100 * time.Millisecond
	if _, _, err := r.RenderCoverJPEG(context.Background(), bytes.NewReader(pdf), int64(len(pdf)), 72); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("blocked RenderCoverJPEG error = %v, want timeout", err)
	}
	if !blocker.didBlock() {
		t.Fatalf("controlled PDFium render blocker was not reached")
	}

	r.operationTimeout = time.Minute
	if _, _, err := r.RenderCoverJPEG(context.Background(), bytes.NewReader(pdf), int64(len(pdf)), 72); err != nil {
		t.Fatalf("RenderCoverJPEG after worker reset: %v", err)
	}

	// Parent cancellation uses the same worker-reset owner as the private
	// render deadline and must likewise leave the pool reusable.
	blocker.arm()
	ctx, cancel := context.WithCancel(context.Background())
	renderDone := make(chan error, 1)
	go func() {
		_, _, err := r.RenderCoverJPEG(ctx, bytes.NewReader(pdf), int64(len(pdf)), 72)
		renderDone <- err
	}()
	blocker.waitUntilBlocked(t)
	cancel()
	select {
	case err := <-renderDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled RenderCoverJPEG error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled RenderCoverJPEG did not stop")
	}
	if _, _, err := r.RenderCoverJPEG(context.Background(), bytes.NewReader(pdf), int64(len(pdf)), 72); err != nil {
		t.Fatalf("RenderCoverJPEG after cancellation reset: %v", err)
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
	jpegBytes := testJPEG(t, 2, 3)
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
		backend:          backend,
		command:          command,
		operationTimeout: time.Second,
	})

	info := r.BackendInfo()
	if info.Backend != BackendPoppler || info.Version != "pdftoppm version 24.01.0" || !filepath.IsAbs(info.Executable) {
		t.Fatalf("BackendInfo = %+v, want probed Poppler with absolute path", info)
	}
	got, _, err := r.RenderCoverJPEG(context.Background(), bytes.NewReader([]byte("pdf")), 3, 96)
	if err != nil {
		t.Fatalf("RenderCoverJPEG: %v", err)
	}
	if !bytes.Equal(got, jpegBytes) {
		t.Fatalf("rendered bytes differ from fake Poppler output")
	}
	wantArgs := []string{"-f", "1", "-l", "1", "-singlefile", "-r", "96", "-jpeg", "-jpegopt", "quality=90", "-"}
	if !slices.Equal(renderArgs, wantArgs) {
		t.Fatalf("pdftoppm args = %q, want %q", renderArgs, wantArgs)
	}
}

func TestPopplerRenderCoverDimensions(t *testing.T) {
	for _, tc := range []struct {
		width, height int
		wantError     bool
	}{
		{1, 1, true},
		{1, 2, false},
		{2, 1, false},
	} {
		t.Run(fmt.Sprintf("%dx%d", tc.width, tc.height), func(t *testing.T) {
			jpegBytes := testJPEG(t, tc.width, tc.height)
			r := newRenderer(rendererConfig{
				backend: BackendInfo{Backend: BackendPoppler},
				command: func(_ context.Context, _ string, _ []string, _ io.Reader, stdout, _ io.Writer) error {
					_, err := stdout.Write(jpegBytes)
					return err // A successful exit does not guarantee a usable cover.
				},
				poolFactory: func(webassembly.Config) (pdfium.Pool, error) {
					t.Error("unexpected WASM initialization after Poppler render")
					return nil, errors.New("unexpected WASM initialization")
				},
			})
			got, _, err := r.RenderCoverJPEG(t.Context(), bytes.NewReader([]byte("pdf")), 3, 0)
			if tc.wantError {
				if err == nil || !strings.Contains(err.Error(), "poppler returned a degenerate 1x1 PDF cover") || got != nil {
					t.Fatalf("render = %d bytes, %v; want no cover and dimension error", len(got), err)
				}
			} else if err != nil || !bytes.Equal(got, jpegBytes) {
				t.Fatalf("render = %d bytes, %v; want the rendered cover", len(got), err)
			}
		})
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
	if err := r.ensureWASM(); err == nil {
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

	_, _, err := r.RenderCoverJPEG(context.Background(), bytes.NewReader([]byte("pdf")), 3, 0)
	if !errors.Is(err, renderErr) {
		t.Fatalf("RenderCoverJPEG error = %v, want wrapped render error", err)
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
		backend:          backend,
		command:          command,
		operationTimeout: 10 * time.Millisecond,
	})

	_, _, err := r.RenderCoverJPEG(context.Background(), bytes.NewReader([]byte("pdf")), 3, 0)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("RenderCoverJPEG error = %v, want timeout", err)
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
		backend:          backend,
		command:          command,
		operationTimeout: time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-renderStarted
		cancel()
	}()

	_, _, err := r.RenderCoverJPEG(ctx, bytes.NewReader([]byte("pdf")), 3, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RenderCoverJPEG error = %v, want context.Canceled", err)
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

func testJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.NRGBA{R: 0x80, G: 0x40, B: 0x20, A: 0xff})
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode test JPEG: %v", err)
	}
	return encoded.Bytes()
}

func testBlankPDF() []byte {
	return testBlankPDFPages(1)
}

func TestWASMPageCount(t *testing.T) {
	r := newRenderer(rendererConfig{backend: BackendInfo{Backend: BackendPDFiumWASM}})
	t.Cleanup(func() { _ = r.Close() })
	for _, want := range []int{1, 3} {
		pdf := testBlankPDFPages(want)
		got, err := r.CountPages(t.Context(), bytes.NewReader(pdf), int64(len(pdf)))
		if err != nil || got != want {
			t.Fatalf("count = %d, %v; want %d", got, err, want)
		}
	}
}

func TestCoverSkipsBlankPages(t *testing.T) {
	for _, backend := range []Backend{BackendPDFiumWASM, BackendPoppler} {
		t.Run(string(backend), func(t *testing.T) {
			info := BackendInfo{Backend: backend}
			if backend == BackendPoppler {
				var err error
				info.Executable, err = exec.LookPath("pdftoppm")
				if err != nil {
					t.Skip("pdftoppm is not installed")
				}
			}
			r := newRenderer(rendererConfig{backend: info})
			t.Cleanup(func() { _ = r.Close() })
			const marked = "0 0 0 rg 18 18 36 36 re f\n"
			for _, tc := range []struct {
				name    string
				pages   []string
				content bool
			}{
				{"first page", []string{marked, ""}, true},
				{"two blank pages", []string{"", "", marked}, true},
				{"all blank", []string{""}, false},
				{"bounded lookahead", []string{"", "", "", marked}, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					data := testPDFPages(tc.pages...)
					cover, _, err := r.RenderCoverJPEG(t.Context(), bytes.NewReader(data), int64(len(data)), 72)
					if err != nil {
						t.Fatal(err)
					}
					img, err := jpeg.Decode(bytes.NewReader(cover))
					if err != nil {
						t.Fatal(err)
					}
					red, _, _, _ := img.At(36, 36).RGBA()
					if (red < 8192) != tc.content {
						t.Fatalf("center pixel = %d; want content %v", red, tc.content)
					}
				})
			}
		})
	}
}

func testBlankPDFPages(pages int) []byte {
	return testPDFPages(make([]string, pages)...)
}

func testPDFPages(contents ...string) []byte {
	pages := len(contents)
	var kids strings.Builder
	for i := range pages {
		fmt.Fprintf(&kids, "%d 0 R ", i+3)
	}
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids.String(), pages),
	}
	for i := range pages {
		objects = append(objects, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 72 72] /Resources << >> /Contents %d 0 R >>", pages+3+i))
	}
	for _, content := range contents {
		objects = append(objects, fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content))
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
