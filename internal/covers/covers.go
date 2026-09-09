// Package covers owns cover image post-processing and filesystem conventions.
//
// SQLite stores only cover presence/version on the book. The original cover and
// derived display/thumb images are addressed by book_id, so this package keeps
// path construction in one place and stays independent of DB/HTTP code.
//
// Cover processing preserves the source ratio; bad crops or invented borders
// are worse than an uneven grid of otherwise faithful covers.
package covers

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"os"
	"path"
	"strconv"
	"strings"

	"golang.org/x/image/draw"

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
// root): cover originals are per-book catalog artifacts and the derived cache is
// hot and disposable, so both live next to the database rather than out on a
// possibly-remote books disk. Durable originals sit flat under covers/; the
// rebuildable display/thumb cache lives under a top-level cache/ that is safe to
// delete at any time.
func OriginalPath(bookID int64) string {
	return path.Join("covers", strconv.FormatInt(bookID, 10))
}

// TempLabel lets repair identify an original awaiting final placement.
// Import stages by source hash until its book ID has committed.
func TempLabel(bookID int64) string {
	return "book-" + strconv.FormatInt(bookID, 10) + "-cover"
}

func ParseTempLabel(label string) (int64, bool) {
	label, ok := strings.CutPrefix(label, "book-")
	if !ok {
		return 0, false
	}
	raw, ok := strings.CutSuffix(label, "-cover")
	if !ok {
		return 0, false
	}
	bookID, err := strconv.ParseInt(raw, 10, 64)
	return bookID, err == nil && bookID > 0
}

// ImportTempLabel ties a staged cover to the imported source before IDs exist.
func ImportTempLabel(sourceSHA256 []byte) string {
	return hex.EncodeToString(sourceSHA256) + "-cover"
}

func ParseImportTempLabel(label string) ([]byte, bool) {
	encoded, ok := strings.CutSuffix(label, "-cover")
	if !ok {
		return nil, false
	}
	sum, err := hex.DecodeString(encoded)
	return sum, err == nil && len(sum) == sha256.Size
}

// CachePath deliberately does not include cover_version. cover_version is only
// a browser cache-busting token in URLs; server-side derived files are replaced
// by deleting this stable cache path when a new original cover is uploaded.
func CachePath(bookID int64, variant Variant) string {
	return path.Join("cache", "covers", CacheVersion, string(variant), strconv.FormatInt(bookID, 10)+".jpg")
}

// RemoveDerived deletes the rebuildable display/thumb cache for a book, so the
// next read regenerates them from the (newly replaced) original. Best-effort: a
// leftover stale variant is harmless because reads regenerate any cache older
// than the original's mtime.
func RemoveDerived(root storage.Root, bookID int64) {
	for _, variant := range []Variant{VariantDisplay, VariantThumb} {
		cachePath, err := root.Resolve(CachePath(bookID, variant))
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

	maxW, maxH, err := variantBounds(variant, opts)
	if err != nil {
		return Encoded{}, err
	}

	resized := resizeToFit(img, maxW, maxH)
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

func resizeToFit(src image.Image, maxW, maxH int) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > maxW || h > maxH {
		scale := math.Min(float64(maxW)/float64(w), float64(maxH)/float64(h))
		w = max(1, int(math.Round(float64(w)*scale)))
		h = max(1, int(math.Round(float64(h)*scale)))
	}

	// Composite at the output size: flattening the original first would allocate
	// another full-resolution image just to produce a small JPEG.
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	return dst
}
