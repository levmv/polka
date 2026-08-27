package covers

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/testfixture"
)

func TestAVIFCoverValidationAndProcessing(t *testing.T) {
	src := testfixture.AVIF()
	info, err := Validate(src)
	if err != nil || info.Width != 2 || info.Height != 2 {
		t.Fatalf("Validate(AVIF) = %#v, %v; want 2x2", info, err)
	}
	processed, err := Process(src, VariantThumb, DefaultOptions())
	if err != nil {
		t.Fatalf("Process(AVIF): %v", err)
	}
	if processed.ContentType != ContentTypeJPEG || processed.Width != 2 || processed.Height != 2 {
		t.Fatalf("processed AVIF = %#v; want 2x2 JPEG", processed)
	}
}

func TestCoverPathsAreDataDirRelative(t *testing.T) {
	// Covers live in the app data dir, not the books root: durable originals sit
	// flat under covers/, and the rebuildable cache under a top-level cache/.
	// These paths are load-bearing — a silent drift would orphan every cover.
	if got := OriginalPath("w_abc"); got != "covers/w_abc" {
		t.Fatalf("OriginalPath = %q; want covers/w_abc", got)
	}
	if got := CachePath("w_abc", VariantDisplay); got != "cache/covers/v2/display/w_abc.jpg" {
		t.Fatalf("CachePath display = %q; want cache/covers/v2/display/w_abc.jpg", got)
	}
	if got := CachePath("w_abc", VariantThumb); got != "cache/covers/v2/thumb/w_abc.jpg" {
		t.Fatalf("CachePath thumb = %q; want cache/covers/v2/thumb/w_abc.jpg", got)
	}
}

var tinyGIF = []byte{
	0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x01, 0x00,
	0x01, 0x00, 0x80, 0x00, 0x00, 0x00, 0x00, 0x00,
	0xff, 0xff, 0xff, 0x21, 0xf9, 0x04, 0x01, 0x00,
	0x00, 0x00, 0x00, 0x2c, 0x00, 0x00, 0x00, 0x00,
	0x01, 0x00, 0x01, 0x00, 0x00, 0x02, 0x02, 0x44,
	0x01, 0x00, 0x3b,
}

var tinyWebP = testfixture.WebP()

func encodeJPEG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func solidImage(w, h int, c color.Color) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: c}, image.Point{}, draw.Src)
	return img
}

func TestInspect(t *testing.T) {
	src := encodeJPEG(t, solidImage(120, 180, color.White))
	info, err := Inspect(src)
	if err != nil {
		t.Fatal(err)
	}
	if info.Width != 120 || info.Height != 180 {
		t.Fatalf("expected 120x180, got %dx%d", info.Width, info.Height)
	}
	if info.Ratio < 0.66 || info.Ratio > 0.67 {
		t.Fatalf("unexpected ratio %f", info.Ratio)
	}
}

func TestInspectRejectsHugeImageConfig(t *testing.T) {
	src := gifConfig(10000, 10000)
	_, err := Inspect(src)
	if err == nil {
		t.Fatal("Inspect returned nil error; want pixel limit error")
	}
	if !strings.Contains(err.Error(), "exceed") {
		t.Fatalf("Inspect error = %v; want pixel limit error", err)
	}
}

func TestValidateRejectsCorruptImageDataAfterValidConfig(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	src := encoded.Bytes()
	idat := bytes.Index(src, []byte("IDAT"))
	if idat < 4 {
		t.Fatal("encoded PNG has no IDAT chunk")
	}
	chunkLen := int(binary.BigEndian.Uint32(src[idat-4 : idat]))
	crc := idat + 4 + chunkLen
	if crc+4 > len(src) {
		t.Fatal("encoded PNG has truncated IDAT checksum")
	}
	src[crc] ^= 0xff

	if _, err := Inspect(src); err != nil {
		t.Fatalf("Inspect rejected intact PNG config: %v", err)
	}
	if _, err := Validate(src); err == nil || !strings.Contains(err.Error(), "decode image") {
		t.Fatalf("Validate error = %v; want complete image decode failure", err)
	}
}

func TestProcessGIF(t *testing.T) {
	info, err := Inspect(tinyGIF)
	if err != nil {
		t.Fatalf("Inspect GIF: %v", err)
	}
	if info.Width != 1 || info.Height != 1 {
		t.Fatalf("GIF dimensions = %dx%d; want 1x1", info.Width, info.Height)
	}

	out, err := Process(tinyGIF, VariantThumb, DefaultOptions())
	if err != nil {
		t.Fatalf("Process GIF: %v", err)
	}
	if out.ContentType != ContentTypeJPEG {
		t.Fatalf("ContentType = %q; want %q", out.ContentType, ContentTypeJPEG)
	}
}

func gifConfig(width, height uint16) []byte {
	return []byte{
		'G', 'I', 'F', '8', '9', 'a',
		byte(width), byte(width >> 8), byte(height), byte(height >> 8),
		0x80, 0x00, 0x00,
		0x00, 0x00, 0x00,
		0xff, 0xff, 0xff,
		0x3b,
	}
}

func TestProcessWebP(t *testing.T) {
	info, err := Inspect(tinyWebP)
	if err != nil {
		t.Fatalf("Inspect WebP: %v", err)
	}
	if info.Width <= 0 || info.Height <= 0 {
		t.Fatalf("WebP dimensions = %dx%d; want positive size", info.Width, info.Height)
	}

	out, err := Process(tinyWebP, VariantThumb, DefaultOptions())
	if err != nil {
		t.Fatalf("Process WebP: %v", err)
	}
	if out.ContentType != ContentTypeJPEG {
		t.Fatalf("ContentType = %q; want %q", out.ContentType, ContentTypeJPEG)
	}
}

func TestProcessPreservesSourceRatioWhenResizing(t *testing.T) {
	src := encodeJPEG(t, solidImage(96, 150, color.NRGBA{R: 40, G: 80, B: 120, A: 255}))
	opts := DefaultOptions()
	opts.DisplayMaxWidth = 48
	opts.DisplayMaxHeight = 1000

	out, err := Process(src, VariantDisplay, opts)
	if err != nil {
		t.Fatal(err)
	}
	if out.Width != 48 || out.Height != 75 {
		t.Fatalf("processed dimensions = %dx%d; want ratio-preserving 48x75", out.Width, out.Height)
	}
}
