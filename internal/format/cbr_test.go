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
			pages, err := ListCBRPages(r, r.Size())
			if err != nil {
				t.Fatalf("ListCBRPages: %v", err)
			}
			if len(pages) != 2 {
				t.Fatalf("len(pages) = %d; want 2: %#v", len(pages), pages)
			}
			if pages[0].Index != 0 || pages[0].Name != "testfile.jpg" || pages[0].Extension != "jpg" || pages[0].Width != 2 || pages[0].Height != 2 {
				t.Fatalf("pages[0] = %#v; want first 2x2 JPEG", pages[0])
			}
			if pages[1].Index != 1 || pages[1].Name != "testfile.png" || pages[1].Extension != "png" || pages[1].Width != 2 || pages[1].Height != 2 {
				t.Fatalf("pages[1] = %#v; want second 2x2 PNG", pages[1])
			}

			meta, cover, ext, err := ExtractCBRMetadataAndCover(bytes.NewReader(tt.data), int64(len(tt.data)))
			if err != nil {
				t.Fatalf("ExtractCBRMetadataAndCover: %v", err)
			}
			if meta == nil || meta.Title != "" {
				t.Fatalf("metadata = %#v; want empty metadata without ComicInfo.xml", meta)
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
