package format

import (
	"bytes"
	"testing"

	"github.com/levmv/polka/internal/testfixture"
)

func TestCB7ExposesPagesMetadataAndCover(t *testing.T) {
	data := testfixture.CB7()
	pages, err := ReadyPageCount(bytes.NewReader(data), int64(len(data)), FormatCB7)
	// The second PNG has a .bin extension; only reading/conversion discovers it.
	if err != nil || pages != 1 {
		t.Fatalf("page count = %d, %v; want 1 image-named entry", pages, err)
	}

	meta, cover, ext, err := ExtractCB7MetadataAndCover(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("ExtractCB7MetadataAndCover: %v", err)
	}
	if meta.Title != "CB7 Fixture" || len(meta.Authors) != 1 || meta.Authors[0].Name != "Fixture Author" {
		t.Fatalf("metadata = %#v; want ComicInfo title and writer", meta)
	}
	if meta.PageCount != pages {
		t.Fatalf("metadata page count = %d; want %d", meta.PageCount, pages)
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
