// Package djvu hosts DjVuTang for metadata extraction and server covers.
package djvu

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"math"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/covers"
)

//go:embed generated/djvutang.wasm
var wasm []byte

// Bound active document memory even across separate import batches.
var renderSlot = make(chan struct{}, 1)

// Decoder lazily compiles DjVuTang and retains its code for one import batch.
// Close it after the batch; each document's memory is released after extraction.
type Decoder struct {
	mu     sync.Mutex
	closed bool
	// Compile explicitly: Instantiate closes its compiled code with the document.
	runtime  wazero.Runtime
	compiled wazero.CompiledModule
}

func NewDecoder() *Decoder { return &Decoder{} }

// RenderCover renders a single document and releases the decoder afterward.
// Use a Decoder to share compilation across a batch.
func RenderCover(ctx context.Context, source io.ReaderAt, size int64) ([]byte, error) {
	r := NewDecoder()
	defer r.Close()
	return r.RenderCover(ctx, source, size)
}

func (r *Decoder) RenderCover(ctx context.Context, source io.ReaderAt, size int64) ([]byte, error) {
	result, err := r.Extract(ctx, source, size, Options{Cover: true})
	return result.Cover, err
}

type Options struct {
	Metadata bool
	Cover    bool
}

type Result struct {
	Metadata bookmeta.Metadata
	Cover    []byte
}

// Extract shares one open document between metadata and cover work. Either
// result may be usable when the other fails; errors name the failed operation.
func (r *Decoder) Extract(ctx context.Context, source io.ReaderAt, size int64, options Options) (result Result, err error) {
	if !options.Metadata && !options.Cover {
		return result, nil
	}
	if size <= 0 || size > math.MaxUint32 {
		return result, errors.New("DjVu file size is outside the decoder's range")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	select {
	case renderSlot <- struct{}{}:
		defer func() { <-renderSlot }()
	case <-ctx.Done():
		return result, ctx.Err()
	}

	if err := ctx.Err(); err != nil {
		return result, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return result, errors.New("DjVu renderer is closed")
	}
	if r.runtime == nil {
		r.runtime = wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().
			WithMemoryLimitPages(4096).WithCloseOnContextDone(true))
	}
	if r.compiled == nil {
		var err error
		r.compiled, err = r.runtime.CompileModule(ctx, wasm)
		if err != nil {
			return result, fmt.Errorf("compile DjVu decoder: %w", err)
		}
	}
	module, err := r.runtime.InstantiateModule(ctx, r.compiled, wazero.NewModuleConfig().WithName(""))
	if err != nil {
		return result, fmt.Errorf("load DjVu decoder: %w", err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = module.Close(cleanup)
	}()
	g := guest{ctx: ctx, module: module, functions: make(map[string]api.Function)}
	if err := g.open(source, size); err != nil {
		return result, err
	}
	pages := int(g.call("page_count"))
	if options.Metadata {
		result.Metadata, err = g.metadata(source, size)
		if err != nil {
			err = fmt.Errorf("read DjVu metadata: %w", err)
		}
	}
	result.Metadata.PageCount = pages
	if options.Cover {
		var coverErr error
		result.Cover, coverErr = g.renderCover(source)
		if coverErr != nil {
			err = errors.Join(err, fmt.Errorf("render DjVu cover: %w", coverErr))
		}
	}
	return result, err
}

func (g *guest) open(source io.ReaderAt, size int64) error {
	g.check(g.call("source_start", uint64(size), 64<<20))
	for g.err == nil {
		ptr := g.call("source_range")
		g.check(g.call("last_status"))
		if ptr == 0 || g.err != nil {
			break
		}
		offset, length := g.word(ptr), g.word(ptr+4)
		buffer := g.call("source_alloc")
		g.check(g.call("last_status"))
		g.read(source, offset, length, buffer)
		g.check(g.call("source_commit"))
	}
	if g.call("document_indirect") != 0 {
		return errors.New("DjVu document needs external page files; use a bundled DjVu")
	}
	return g.error()
}

func (g guest) renderCover(source io.ReaderAt) ([]byte, error) {
	var first []byte
	options := covers.DefaultOptions()
	pages := int(g.call("page_count"))
	if pages == 0 {
		return nil, errors.New("DjVu has no pages")
	}
	for page := range min(pages, covers.CoverPageLimit) {
		img, err := g.renderPage(source, uint64(page))
		if err != nil {
			if first != nil && g.ctx.Err() == nil {
				return first, nil
			}
			return nil, err
		}
		blank := covers.IsBlankPage(img)
		if page == 0 || !blank {
			// Encode while the instance owns the pixels, avoiding an RGBA copy.
			var out bytes.Buffer
			if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: options.JPEGQuality}); err != nil {
				return nil, err
			}
			if !blank {
				return out.Bytes(), nil
			}
			first = out.Bytes()
		}
		g.call("render_cancel")
		g.check(g.call("trim_cache", 16<<20))
	}
	return first, g.error()
}

func (g *guest) renderPage(source io.ReaderAt, page uint64) (*image.RGBA, error) {
	for g.err == nil {
		missing := g.call("next_missing", page, 1)
		g.check(g.call("last_status"))
		if missing == 0 || g.err != nil {
			break
		}
		index := uint64(missing - 1)
		ptr := g.call("component_info", index)
		g.check(g.call("last_status"))
		offset, length := g.word(ptr+36), g.word(ptr+40)
		buffer := g.call("component_alloc", index, uint64(length))
		g.check(g.call("last_status"))
		g.read(source, offset, length, buffer)
		g.check(g.call("component_commit", index))
	}
	options := covers.DefaultOptions()
	g.check(g.call("render_start_sized", page, uint64(options.DisplayMaxWidth), uint64(options.DisplayMaxHeight), 0, 0, 0, 0, 0))
	for g.err == nil {
		status := g.call("render_step", 4096)
		if status != 1 {
			g.check(status)
			break
		}
	}
	w, h := g.call("result_width"), g.call("result_height")
	ptr, length := g.call("result_ptr"), g.call("result_len")
	if err := g.error(); err != nil {
		return nil, err
	}
	rgba, ok := g.module.Memory().Read(ptr, length)
	if !ok || w == 0 || h == 0 || uint64(w)*uint64(h)*4 != uint64(length) {
		return nil, errors.New("invalid DjVu raster")
	}
	return &image.RGBA{Pix: rgba, Stride: int(w) * 4, Rect: image.Rect(0, 0, int(w), int(h))}, nil
}

// Close releases compiled code and the runtime. Safe before first use or after Close.
func (r *Decoder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	if r.runtime == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := r.runtime.Close(ctx)
	r.runtime, r.compiled = nil, nil
	return err
}

// metadata and renderCover use value receivers to keep operation errors separate
// while sharing the open document.
type guest struct {
	ctx       context.Context
	module    api.Module
	functions map[string]api.Function
	err       error
}

func (g *guest) call(name string, args ...uint64) uint32 {
	if g.err != nil {
		return 0
	}
	fn := g.functions[name]
	if fn == nil {
		fn = g.module.ExportedFunction(name)
		if fn == nil {
			g.err = fmt.Errorf("DjVu decoder is missing %s", name)
			return 0
		}
		g.functions[name] = fn
	}
	result, err := fn.Call(g.ctx, args...)
	if err != nil {
		g.err = fmt.Errorf("DjVu %s: %w", name, err)
		return 0
	}
	if len(result) == 0 {
		return 0
	}
	return uint32(result[0])
}

func (g *guest) error() error {
	if err := g.ctx.Err(); err != nil {
		return err
	}
	return g.err
}

func (g *guest) check(status uint32) {
	if g.err == nil && status != 0 {
		names := [...]string{"", "Progress", "InvalidData", "Unsupported", "LimitExceeded", "Cancelled", "OutOfMemory", "InvalidArgument", "Busy", "MissingComponent"}
		if status < uint32(len(names)) {
			g.err = fmt.Errorf("DjVu: %s", names[status])
		} else {
			g.err = fmt.Errorf("DjVu: status %d", status)
		}
	}
}

func (g *guest) word(ptr uint32) uint32 {
	if g.err != nil {
		return 0
	}
	value, ok := g.module.Memory().ReadUint32Le(ptr)
	if !ok {
		g.err = errors.New("invalid DjVu memory address")
	}
	return value
}

func (g *guest) read(source io.ReaderAt, offset, length, ptr uint32) {
	if g.err != nil {
		return
	}
	if g.err = g.ctx.Err(); g.err != nil {
		return
	}
	buffer, ok := g.module.Memory().Read(ptr, length)
	if !ok {
		g.err = errors.New("invalid DjVu source buffer")
		return
	}
	n, err := source.ReadAt(buffer, int64(offset))
	if n != len(buffer) {
		g.err = io.ErrUnexpectedEOF
	} else if err != nil && err != io.EOF {
		g.err = err
	}
}
