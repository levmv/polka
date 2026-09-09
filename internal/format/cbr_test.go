package format

import (
	"bytes"
	"image"
	"testing"

	"github.com/levmv/polka/internal/testfixture"
)

func TestCBR3AndCBR5ExposePagesAndCover(t *testing.T) {
	for _, tt := range []struct {
		name string
		data []byte
	}{
		{name: "RAR3", data: testfixture.CBR3()},
		{name: "RAR5", data: testfixture.CBR5()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := bytes.NewReader(tt.data)
			pages, err := ReadyPageCount(r, r.Size(), FormatCBR)
			if err != nil || pages != 2 {
				t.Fatalf("page count = %d, %v; want 2", pages, err)
			}

			meta, cover, ext, err := ExtractCBRMetadataAndCover(bytes.NewReader(tt.data), int64(len(tt.data)))
			if err != nil {
				t.Fatalf("ExtractCBRMetadataAndCover: %v", err)
			}
			if meta == nil || meta.Title != "" || meta.PageCount != pages {
				t.Fatalf("metadata = %#v; want only the page count without ComicInfo.xml", meta)
			}
			if ext != "jpg" || len(cover) == 0 {
				t.Fatalf("cover ext=%q bytes=%d; want JPEG cover", ext, len(cover))
			}
			cfg, formatName, err := image.DecodeConfig(bytes.NewReader(cover))
			if err != nil || formatName != "jpeg" || cfg.Width != 2 || cfg.Height != 2 {
				t.Fatalf("cover config=%#v format=%q err=%v; want 2x2 JPEG", cfg, formatName, err)
			}
		})
	}
}

func TestCBRRejectsSignatureWithoutArchive(t *testing.T) {
	data := []byte{'R', 'a', 'r', '!', 0x1a, 0x07, 0x00}
	r := bytes.NewReader(data)
	if got := DetectFormat("broken.cbr", r, r.Size()); got != FormatUnknown {
		t.Fatalf("DetectFormat = %v; want FormatUnknown", got)
	}
}
