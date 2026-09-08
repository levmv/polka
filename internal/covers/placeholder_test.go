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

func TestGeneratedCoverSeedAndStyle(t *testing.T) {
	baseline, err := Generated("Dune", "Frank Herbert", VariantThumb, DefaultOptions(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if baseline.Width != 360 || baseline.Height != 540 {
		t.Fatalf("generated thumb size = %dx%d; want 360x540", baseline.Width, baseline.Height)
	}
	if _, _, err := image.DecodeConfig(bytes.NewReader(baseline.Bytes)); err != nil {
		t.Fatalf("decode generated cover: %v", err)
	}
	for _, tt := range []struct {
		name      string
		seed      int
		style     string
		wantEqual bool
	}{
		{"same inputs", 1, GeneratedStyleClassic, true},
		{"different seed", 2, GeneratedStyleClassic, false},
		{"different style", 1, GeneratedStyleLabel, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GeneratedStyled("Dune", "Frank Herbert", VariantThumb, DefaultOptions(), tt.seed, tt.style)
			if err != nil {
				t.Fatal(err)
			}
			if equal := bytes.Equal(baseline.Bytes, got.Bytes); equal != tt.wantEqual {
				t.Errorf("same bytes = %v; want %v", equal, tt.wantEqual)
			}
		})
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
