package format

import (
	"bytes"
	"testing"

	"github.com/levmv/polka/internal/testfixture"
)

func TestCB7ExposesPagesMetadataAndCover(t *testing.T) {
	data := testfixture.CB7()
	pages, err := ListCB7Pages(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("ListCB7Pages: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("len(pages) = %d; want 2: %#v", len(pages), pages)
	}
	if pages[0].Index != 0 || pages[0].Name != "1.png" || pages[0].Extension != "png" || pages[0].Width != 1 || pages[0].Height != 1 {
		t.Fatalf("pages[0] = %#v; want first 1x1 PNG", pages[0])
	}
	if pages[1].Index != 1 || pages[1].Name != "2.bin" || pages[1].Extension != "png" || pages[1].Width != 1 || pages[1].Height != 1 {
		t.Fatalf("pages[1] = %#v; want content-sniffed 1x1 PNG", pages[1])
	}

	meta, cover, ext, err := ExtractCB7MetadataAndCover(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("ExtractCB7MetadataAndCover: %v", err)
	}
	if meta.Title != "CB7 Fixture" || len(meta.Authors) != 1 || meta.Authors[0].Name != "Fixture Author" {
		t.Fatalf("metadata = %#v; want ComicInfo title and writer", meta)
	}
	if ext != "png" || len(cover) == 0 {
		t.Fatalf("cover ext=%q bytes=%d; want PNG cover", ext, len(cover))
	}
}

func TestCB7RejectsSignatureWithoutArchive(t *testing.T) {
	data := []byte{'7', 'z', 0xbc, 0xaf, 0x27, 0x1c}
	r := bytes.NewReader(data)
	if got := DetectFormat("broken.cb7", r, r.Size()); got != FormatUnknown {
		t.Fatalf("DetectFormat = %v; want FormatUnknown", got)
	}
}
