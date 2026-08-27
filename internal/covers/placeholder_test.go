package covers

import (
	"bytes"
	"image"
	"testing"
)

func TestPlaceholderDimensions(t *testing.T) {
	cases := []struct {
		variant Variant
		wantW   int
		wantH   int
	}{
		{VariantDisplay, 900, 1350},
		{VariantThumb, 360, 540},
	}
	for _, tc := range cases {
		enc, err := Placeholder("The Winter King", "Bernard Cornwell", tc.variant, DefaultOptions())
		if err != nil {
			t.Fatalf("Placeholder(%s): %v", tc.variant, err)
		}
		if enc.Width != tc.wantW || enc.Height != tc.wantH {
			t.Errorf("%s: got %dx%d, want %dx%d", tc.variant, enc.Width, enc.Height, tc.wantW, tc.wantH)
		}
		if enc.ContentType != ContentTypeJPEG {
			t.Errorf("%s: content type %q, want %q", tc.variant, enc.ContentType, ContentTypeJPEG)
		}
		cfg, _, err := image.DecodeConfig(bytes.NewReader(enc.Bytes))
		if err != nil {
			t.Fatalf("%s: decode encoded bytes: %v", tc.variant, err)
		}
		if cfg.Width != tc.wantW || cfg.Height != tc.wantH {
			t.Errorf("%s: decoded %dx%d, want %dx%d", tc.variant, cfg.Width, cfg.Height, tc.wantW, tc.wantH)
		}
	}
}

func TestPlaceholderDeterministic(t *testing.T) {
	a, err := Placeholder("Dune", "Frank Herbert", VariantThumb, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	b, err := Placeholder("Dune", "Frank Herbert", VariantThumb, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes, b.Bytes) {
		t.Error("identical inputs produced different bytes")
	}
}

func TestGeneratedCoverVariesBySeed(t *testing.T) {
	a, err := Generated("Dune", "Frank Herbert", VariantThumb, DefaultOptions(), 1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Generated("Dune", "Frank Herbert", VariantThumb, DefaultOptions(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if a.Width != 360 || a.Height != 540 {
		t.Fatalf("generated thumb size = %dx%d; want 360x540", a.Width, a.Height)
	}
	if bytes.Equal(a.Bytes, b.Bytes) {
		t.Error("different generated cover seeds produced identical bytes")
	}
	if _, _, err := image.DecodeConfig(bytes.NewReader(a.Bytes)); err != nil {
		t.Fatalf("decode generated cover: %v", err)
	}
}

func TestGeneratedCoverVariesByStyle(t *testing.T) {
	a, err := GeneratedStyled("Dune", "Frank Herbert", VariantThumb, DefaultOptions(), 4, GeneratedStyleClassic)
	if err != nil {
		t.Fatal(err)
	}
	b, err := GeneratedStyled("Dune", "Frank Herbert", VariantThumb, DefaultOptions(), 4, GeneratedStyleLabel)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a.Bytes, b.Bytes) {
		t.Error("different generated cover styles produced identical bytes")
	}
}

func TestGeneratedTitleScalePrefersShortTitles(t *testing.T) {
	short := generatedTitleScale("Dune")
	medium := generatedTitleScale("The Left Hand of Darkness")
	long := generatedTitleScale("A Very Long Generated Cover Title That Needs More Room Than One Line")
	if short <= medium || medium <= long {
		t.Fatalf("title scales not ordered by length: short=%v medium=%v long=%v", short, medium, long)
	}
	if short < 1.25 {
		t.Fatalf("short title scale = %v; want visibly larger than base", short)
	}
	if long >= 1 {
		t.Fatalf("long title scale = %v; want below base", long)
	}
}

func TestGeneratedCoverDeterministicForSeed(t *testing.T) {
	a, err := Generated("Dune", "Frank Herbert", VariantThumb, DefaultOptions(), 3)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Generated("Dune", "Frank Herbert", VariantThumb, DefaultOptions(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes, b.Bytes) {
		t.Error("identical generated-cover inputs produced different bytes")
	}
}

func TestPlaceholderFillsFrame(t *testing.T) {
	// The background must cover the whole frame and the text must mark it:
	// decode and confirm at least two distinct colours are present.
	enc, err := Placeholder("A Tale", "An Author", VariantThumb, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	img, _, err := image.Decode(bytes.NewReader(enc.Bytes))
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	first := img.At(b.Min.X, b.Min.Y)
	fr, fg, fb, _ := first.RGBA()
	distinct := false
	for y := b.Min.Y; y < b.Max.Y && !distinct; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if r != fr || g != fg || bl != fb {
				distinct = true
				break
			}
		}
	}
	if !distinct {
		t.Error("placeholder is a single flat colour — no text rendered")
	}
}

func TestPlaceholderEmptyInputs(t *testing.T) {
	// Empty title/author must not panic and must still produce a valid image.
	enc, err := Placeholder("", "", VariantThumb, DefaultOptions())
	if err != nil {
		t.Fatalf("Placeholder(empty): %v", err)
	}
	if _, _, err := image.DecodeConfig(bytes.NewReader(enc.Bytes)); err != nil {
		t.Fatalf("decode empty-input placeholder: %v", err)
	}
}

func TestPlaceholderLongTitle(t *testing.T) {
	long := "Supercalifragilisticexpialidocious " +
		"Antidisestablishmentarianism Pneumonoultramicroscopicsilicovolcanoconiosis " +
		"and a very long subtitle that keeps going well past any reasonable bound"
	enc, err := Placeholder(long, "Some Author", VariantThumb, DefaultOptions())
	if err != nil {
		t.Fatalf("Placeholder(long): %v", err)
	}
	if _, _, err := image.DecodeConfig(bytes.NewReader(enc.Bytes)); err != nil {
		t.Fatalf("decode long-title placeholder: %v", err)
	}
}
