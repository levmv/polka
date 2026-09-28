package mobi

import (
	"bytes"
	"image"
	"image/color"
	"testing"

	"golang.org/x/image/bmp"

	"github.com/levmv/polka/internal/testfixture"
)

func TestExtractPDBMetadata(t *testing.T) {
	data := testfixture.PalmDOC("Lem Stanislaw - Solaris")
	r := bytes.NewReader(data)
	meta, err := ExtractPalmDOCMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractPalmDOCMetadata: %v", err)
	}
	if meta.Title != "Lem Stanislaw - Solaris" {
		t.Fatalf("Title = %q; want Palm database name", meta.Title)
	}
}

func TestExtractPDBMetadataUsesEmbeddedOEBBlock(t *testing.T) {
	text := []byte(`<HTML><HEAD><metadata><dc-metadata xmlns:dc="http://purl.org/metadata/dublin_core">
<dc:Title>Embedded Palm Title</dc:Title><dc:Creator>Ada Writer</dc:Creator>
<dc:Language>en-us</dc:Language><dc:Publisher>Palm House</dc:Publisher>
</dc-metadata></metadata></HEAD><BODY><p>Book text.</p></BODY></HTML>`)
	data := testfixture.PalmDOCWithText("Database Fallback", mobiCompressionPalmDOC, [][]byte{text}, uint32(len(text)))
	r := bytes.NewReader(data)

	meta, err := ExtractPalmDOCMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractPalmDOCMetadata: %v", err)
	}
	if meta.Title != "Embedded Palm Title" || len(meta.Authors) != 1 || meta.Authors[0].Name != "Ada Writer" {
		t.Fatalf("embedded title/authors = %q / %+v", meta.Title, meta.Authors)
	}
	if meta.Language != "en-US" || meta.Publisher != "Palm House" {
		t.Fatalf("embedded language/publisher = %q / %q", meta.Language, meta.Publisher)
	}
}

func testPalmDOCBMP(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	img.Set(0, 0, color.NRGBA{R: 0xff, A: 0xff})
	img.Set(1, 0, color.NRGBA{B: 0xff, A: 0xff})
	var out bytes.Buffer
	if err := bmp.Encode(&out, img); err != nil {
		t.Fatalf("encode PalmDOC BMP: %v", err)
	}
	return out.Bytes()
}
