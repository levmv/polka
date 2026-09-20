package djvu

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image/jpeg"
	"io"
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
)

func TestRenderCover(t *testing.T) {
	data, err := os.ReadFile("../../testfixture/reader.djvu")
	if err != nil {
		t.Fatal(err)
	}
	truncated := data[:len(data)/2]
	if _, err := RenderCover(t.Context(), bytes.NewReader(truncated), int64(len(truncated))); err == nil {
		t.Fatal("truncated document produced a cover")
	}
	cover, err := RenderCover(t.Context(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(cover))
	if err != nil {
		t.Fatal(err)
	}
	if got := img.Bounds().Size(); got.X != 900 || got.Y != 1200 {
		t.Fatalf("cover dimensions: %v", got)
	}
	// The synthetic cover has a black frame on a white page.
	black, _, _, _ := img.At(45, 45).RGBA()
	white, _, _, _ := img.At(10, 10).RGBA()
	if black > 8192 || white < 57344 {
		t.Fatalf("cover pixels: black=%d white=%d", black, white)
	}
}

func TestCoverSkipsBlankPages(t *testing.T) {
	data, err := os.ReadFile("../../testfixture/blank-first.djvu")
	if err != nil {
		t.Fatal(err)
	}
	cover, err := RenderCover(t.Context(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(cover))
	if err != nil {
		t.Fatal(err)
	}
	bounds := img.Bounds()
	red, _, _, _ := img.At(bounds.Dx()/2, bounds.Dy()/2).RGBA()
	if red > 8192 {
		t.Fatalf("center pixel = %d; want the third page's black rectangle", red)
	}
}

func TestDecoderBatchLifetime(t *testing.T) {
	data, err := os.ReadFile("../../testfixture/metadata.djvu")
	if err != nil {
		t.Fatal(err)
	}
	r := NewDecoder()
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})

	compilations := 0
	var cancelExtraction context.CancelFunc
	// The factory runs during compilation; its listener runs inside real WASM.
	ctx := experimental.WithFunctionListenerFactory(t.Context(), experimental.FunctionListenerFactoryFunc(func(def api.FunctionDefinition) experimental.FunctionListener {
		if slices.Contains(def.ExportNames(), "render_step") {
			compilations++
		} else if !slices.Contains(def.ExportNames(), "metadata_step") {
			return nil
		}
		return experimental.FunctionListenerFunc(func(context.Context, api.Module, api.FunctionDefinition, []uint64, experimental.StackIterator) {
			if cancelExtraction != nil {
				cancelExtraction()
				cancelExtraction = nil
			}
		})
	}))
	want, err := r.Extract(ctx, bytes.NewReader(data), int64(len(data)), Options{Metadata: true, Cover: true})
	if err != nil {
		t.Fatal(err)
	}

	// Cancelling an active document must release its slot and leave the shared
	// compiled code usable by the next document in the batch.
	for _, options := range []Options{{Cover: true}, {Metadata: true}} {
		cancelCtx, cancel := context.WithCancel(ctx)
		cancelExtraction = cancel
		_, err := r.Extract(cancelCtx, bytes.NewReader(data), int64(len(data)), options)
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel during extraction: %v", err)
		}
		if cancelExtraction != nil {
			t.Fatal("WASM operation was not reached")
		}
		got, err := r.Extract(ctx, bytes.NewReader(data), int64(len(data)), Options{Metadata: true, Cover: true})
		if err != nil {
			t.Fatalf("extract after cancellation: %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatal("extraction changed after cancellation")
		}
	}
	if compilations != 1 {
		t.Fatalf("compiled %d times in one batch, want once", compilations)
	}

	// Ending the batch must close the actual runtime, not just its documents.
	runtime := r.runtime
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.CompileModule(ctx, wasm); err == nil {
		t.Fatal("batch runtime is still open")
	}
	if _, err := r.RenderCover(ctx, bytes.NewReader(data), int64(len(data))); err == nil {
		t.Fatal("closed renderer accepted another document")
	}
}

func TestExtractIndependentResults(t *testing.T) {
	data, err := os.ReadFile("../../testfixture/metadata.djvu")
	if err != nil {
		t.Fatal(err)
	}
	brokenAnnotation := bytes.Clone(data)
	ant := bytes.Index(brokenAnnotation, []byte("ANTz"))
	clear(brokenAnnotation[ant+8 : ant+8+int(binary.BigEndian.Uint32(brokenAnnotation[ant+4:ant+8]))])
	imageStart := int64(bytes.Index(data, []byte("Sjbz")) + 8)
	annotationStart := int64(ant + 8)
	r := NewDecoder()
	defer r.Close()
	for _, tt := range []struct {
		name                   string
		data                   []byte
		failAt                 int64
		options                Options
		metadata, cover, fails bool
	}{
		{"metadata skips images", data, imageStart, Options{Metadata: true}, true, false, false},
		{"image failure preserves metadata", data, imageStart, Options{Metadata: true, Cover: true}, true, false, true},
		{"annotation failure preserves cover", brokenAnnotation, -1, Options{Metadata: true, Cover: true}, false, true, true},
		{"cover skips annotation decoding", brokenAnnotation, -1, Options{Cover: true}, false, true, false},
		{"metadata IO failure is not absence", data, annotationStart, Options{Metadata: true}, false, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			src := failingRangeReader{bytes.NewReader(tt.data), tt.failAt}
			got, err := r.Extract(t.Context(), src, int64(len(tt.data)), tt.options)
			if (err != nil) != tt.fails || (got.Metadata.Title != "") != tt.metadata || (len(got.Cover) > 0) != tt.cover || got.Metadata.PageCount != 3 {
				t.Fatalf("title=%q, cover=%d, pages=%d, error=%v", got.Metadata.Title, len(got.Cover), got.Metadata.PageCount, err)
			}
			if tt.fails && tt.failAt >= 0 && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("lost source error: %v", err)
			}
		})
	}
}

type failingRangeReader struct {
	*bytes.Reader
	failAt int64
}

func (r failingRangeReader) ReadAt(p []byte, off int64) (int, error) {
	if off <= r.failAt && r.failAt < off+int64(len(p)) {
		return 0, io.ErrUnexpectedEOF
	}
	return r.Reader.ReadAt(p, off)
}
