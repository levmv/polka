// Package covers owns cover image post-processing and filesystem conventions.
//
// SQLite stores only cover presence/version on the work. The original cover and
// derived display/thumb images are addressed by work_id, so this package keeps
// path construction in one place and stays independent of DB/HTTP code.
//
// Cover processing preserves the source ratio; bad crops or invented borders
// are worse than an uneven grid of otherwise faithful covers.
package covers

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"math"
	"os"
	"path"

	"github.com/levmv/polka/internal/imagecodec"
	"github.com/levmv/polka/internal/storage"
)

const ContentTypeJPEG = "image/jpeg"

type Variant string

const (
	VariantDisplay Variant = "display"
	VariantThumb   Variant = "thumb"

	CacheVersion = "v2"

	MaxCoverPixels = 80_000_000
)

type Options struct {
	DisplayMaxWidth  int
	DisplayMaxHeight int
	ThumbMaxWidth    int
	ThumbMaxHeight   int
	JPEGQuality      int
}

type Info struct {
	Width  int
	Height int
	Ratio  float64
}

type Encoded struct {
	Bytes       []byte
	Width       int
	Height      int
	ContentType string
}

func DefaultOptions() Options {
	return Options{
		DisplayMaxWidth:  900,
		DisplayMaxHeight: 1350,
		ThumbMaxWidth:    360,
		ThumbMaxHeight:   540,
		JPEGQuality:      88,
	}
}

// OriginalPath and CachePath are relative to the app data dir (not the books
// root): cover originals are per-work catalog artifacts and the derived cache is
// hot and disposable, so both live next to the database rather than out on a
// possibly-remote books disk. Durable originals sit flat under covers/; the
// rebuildable display/thumb cache lives under a top-level cache/ that is safe to
// delete at any time.
func OriginalPath(workID string) string {
	return path.Join("covers", workID)
}

// CachePath deliberately does not include cover_version. cover_version is only
// a browser cache-busting token in URLs; server-side derived files are replaced
// by deleting this stable cache path when a new original cover is uploaded.
func CachePath(workID string, variant Variant) string {
	return path.Join("cache", "covers", CacheVersion, string(variant), workID+".jpg")
}

// RemoveDerived deletes the rebuildable display/thumb cache for a work, so the
// next read regenerates them from the (newly replaced) original. Best-effort: a
// leftover stale variant is harmless because reads regenerate any cache older
// than the original's mtime.
func RemoveDerived(root storage.Root, workID string) {
	for _, variant := range []Variant{VariantDisplay, VariantThumb} {
		cachePath, err := root.Resolve(CachePath(workID, variant))
		if err != nil {
			continue
		}
		_ = os.Remove(cachePath)
	}
}

func Inspect(src []byte) (Info, error) {
	cfg, _, err := imagecodec.DecodeConfig(bytes.NewReader(src))
	if err != nil {
		return Info{}, fmt.Errorf("decode image config: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return Info{}, errors.New("invalid image dimensions")
	}
	pixels := uint64(cfg.Width) * uint64(cfg.Height)
	if pixels > MaxCoverPixels {
		return Info{}, fmt.Errorf("image dimensions %dx%d exceed %d pixels", cfg.Width, cfg.Height, MaxCoverPixels)
	}
	return Info{Width: cfg.Width, Height: cfg.Height, Ratio: float64(cfg.Width) / float64(cfg.Height)}, nil
}

// Validate verifies both the bounded image dimensions and the complete encoded
// image. DecodeConfig alone can accept a PNG whose header is intact but whose
// pixel data or checksum is corrupt, leaving a stored cover that fails every
// time the web layer tries to materialize a display variant.
func Validate(src []byte) (Info, error) {
	info, err := Inspect(src)
	if err != nil {
		return Info{}, err
	}
	if _, _, err := imagecodec.Decode(bytes.NewReader(src)); err != nil {
		return Info{}, fmt.Errorf("decode image: %w", err)
	}
	return info, nil
}

func Process(src []byte, variant Variant, opts Options) (Encoded, error) {
	opts = normalizeOptions(opts)

	img, _, err := imagecodec.Decode(bytes.NewReader(src))
	if err != nil {
		return Encoded{}, fmt.Errorf("decode image: %w", err)
	}

	flat := flattenToRGBA(img)

	maxW, maxH, err := variantBounds(variant, opts)
	if err != nil {
		return Encoded{}, err
	}

	resized := resizeToFit(flat, maxW, maxH)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, resized, &jpeg.Options{Quality: opts.JPEGQuality}); err != nil {
		return Encoded{}, fmt.Errorf("encode jpeg: %w", err)
	}

	b := resized.Bounds()
	return Encoded{
		Bytes:       out.Bytes(),
		Width:       b.Dx(),
		Height:      b.Dy(),
		ContentType: ContentTypeJPEG,
	}, nil
}

func normalizeOptions(opts Options) Options {
	def := DefaultOptions()
	if opts.DisplayMaxWidth <= 0 {
		opts.DisplayMaxWidth = def.DisplayMaxWidth
	}
	if opts.DisplayMaxHeight <= 0 {
		opts.DisplayMaxHeight = def.DisplayMaxHeight
	}
	if opts.ThumbMaxWidth <= 0 {
		opts.ThumbMaxWidth = def.ThumbMaxWidth
	}
	if opts.ThumbMaxHeight <= 0 {
		opts.ThumbMaxHeight = def.ThumbMaxHeight
	}
	if opts.JPEGQuality <= 0 {
		opts.JPEGQuality = def.JPEGQuality
	}
	return opts
}

func variantBounds(variant Variant, opts Options) (int, int, error) {
	switch variant {
	case VariantDisplay:
		return opts.DisplayMaxWidth, opts.DisplayMaxHeight, nil
	case VariantThumb:
		return opts.ThumbMaxWidth, opts.ThumbMaxHeight, nil
	default:
		return 0, 0, fmt.Errorf("unknown cover variant %q", variant)
	}
}

// flattenToRGBA composites onto white, so the remaining cover pipeline can use
// concrete opaque RGBA fast paths for resizing and JPEG encoding.
func flattenToRGBA(src image.Image) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Over)
	return dst
}

func resizeToFit(src *image.RGBA, maxW, maxH int) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxW && h <= maxH {
		return src
	}

	scale := math.Min(float64(maxW)/float64(w), float64(maxH)/float64(h))
	newW := max(1, int(math.Round(float64(w)*scale)))
	newH := max(1, int(math.Round(float64(h)*scale)))

	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	for y := range newH {
		srcY := (float64(y)+0.5)/scale - 0.5
		y0 := clampInt(int(math.Floor(srcY)), 0, h-1)
		y1 := clampInt(y0+1, 0, h-1)
		fy := srcY - math.Floor(srcY)
		for x := range newW {
			srcX := (float64(x)+0.5)/scale - 0.5
			x0 := clampInt(int(math.Floor(srcX)), 0, w-1)
			x1 := clampInt(x0+1, 0, w-1)
			fx := srcX - math.Floor(srcX)
			dst.SetRGBA(x, y, bilinear(src, b.Min.X+x0, b.Min.Y+y0, b.Min.X+x1, b.Min.Y+y1, fx, fy))
		}
	}
	return dst
}

func bilinear(src *image.RGBA, x0, y0, x1, y1 int, fx, fy float64) color.RGBA {
	c00 := src.RGBAAt(x0, y0)
	c10 := src.RGBAAt(x1, y0)
	c01 := src.RGBAAt(x0, y1)
	c11 := src.RGBAAt(x1, y1)

	return color.RGBA{
		R: interp(c00.R, c10.R, c01.R, c11.R, fx, fy),
		G: interp(c00.G, c10.G, c01.G, c11.G, fx, fy),
		B: interp(c00.B, c10.B, c01.B, c11.B, fx, fy),
		A: interp(c00.A, c10.A, c01.A, c11.A, fx, fy),
	}
}

func interp(c00, c10, c01, c11 uint8, fx, fy float64) uint8 {
	top := float64(c00)*(1-fx) + float64(c10)*fx
	bottom := float64(c01)*(1-fx) + float64(c11)*fx
	return uint8(math.Round(top*(1-fy) + bottom*fy))
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
