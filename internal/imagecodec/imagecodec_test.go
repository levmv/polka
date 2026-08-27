package imagecodec

import (
	"bytes"
	"testing"

	"github.com/levmv/polka/internal/testfixture"
)

var tinyPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
	0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41,
	0x54, 0x08, 0xd7, 0x63, 0xf8, 0xff, 0xff, 0x3f,
	0x00, 0x05, 0xfe, 0x02, 0xfe, 0xdc, 0xcc, 0x59,
	0xe7, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e,
	0x44, 0xae, 0x42, 0x60, 0x82,
}

func TestStandardDecoderRegistration(t *testing.T) {
	cfg, formatName, err := DecodeConfig(bytes.NewReader(tinyPNG))
	if err != nil || formatName != "png" || cfg.Width != 1 || cfg.Height != 1 {
		t.Fatalf("DecodeConfig(PNG) = %#v/%q, %v; want 1x1 PNG", cfg, formatName, err)
	}
}

func TestAVIFDetectionAndDecoding(t *testing.T) {
	for _, tt := range []struct {
		name string
		data []byte
	}{
		{name: "major brand", data: testfixture.AVIF()},
		{name: "compatible brand", data: avifWithCompatibleBrand(t, testfixture.AVIF())},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if !IsAVIF(tt.data) {
				t.Fatal("IsAVIF = false; want true")
			}
			cfg, formatName, err := DecodeConfig(bytes.NewReader(tt.data))
			if err != nil || formatName != "avif" || cfg.Width != 2 || cfg.Height != 2 {
				t.Fatalf("DecodeConfig = %#v/%q, %v; want 2x2 AVIF", cfg, formatName, err)
			}
			img, formatName, err := Decode(bytes.NewReader(tt.data))
			if err != nil || formatName != "avif" || img.Bounds().Dx() != 2 || img.Bounds().Dy() != 2 {
				t.Fatalf("Decode = %#v/%q, %v; want 2x2 AVIF", img, formatName, err)
			}
		})
	}
}

func avifWithCompatibleBrand(t *testing.T, src []byte) []byte {
	t.Helper()
	if len(src) < 20 || string(src[8:12]) != "avif" || string(src[16:20]) != "avif" {
		t.Fatal("fixture does not have the expected AVIF brands")
	}
	out := append([]byte(nil), src...)
	copy(out[8:12], "mif1")
	return out
}
