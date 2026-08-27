package format

import (
	"archive/zip"
	"bytes"
	"maps"
	"testing"
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

var tinyGIF = []byte{
	0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x01, 0x00,
	0x01, 0x00, 0x80, 0x00, 0x00, 0x00, 0x00, 0x00,
	0xff, 0xff, 0xff, 0x21, 0xf9, 0x04, 0x01, 0x00,
	0x00, 0x00, 0x00, 0x2c, 0x00, 0x00, 0x00, 0x00,
	0x01, 0x00, 0x01, 0x00, 0x00, 0x02, 0x02, 0x44,
	0x01, 0x00, 0x3b,
}

func createTestEPUB(t *testing.T, files map[string]string, binaryFiles map[string][]byte) *bytes.Reader {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(content))
	}
	for name, content := range binaryFiles {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		f.Write(content)
	}
	w.Close()
	return bytes.NewReader(buf.Bytes())
}

func TestExtractEPUBCover(t *testing.T) {
	containerXML := `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
	<rootfiles>
		<rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
	</rootfiles>
</container>`

	tests := []struct {
		name      string
		opf       string
		files     map[string]string
		binary    map[string][]byte
		wantExt   string
		wantBytes []byte
	}{
		{
			name: "meta cover",
			opf: `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
	<metadata>
		<meta name="cover" content="cover-image"/>
	</metadata>
	<manifest>
		<item id="cover-image" href="images/cover.png" media-type="image/png"/>
	</manifest>
</package>`,
			binary:    map[string][]byte{"OEBPS/images/cover.png": tinyPNG},
			wantExt:   ".png",
			wantBytes: tinyPNG,
		},
		{
			name: "meta cover href fallback",
			opf: `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
	<metadata>
		<meta name="cover" content="images/cover.png"/>
	</metadata>
	<manifest>
		<item id="img1" href="images/cover.png" media-type="image/png"/>
	</manifest>
</package>`,
			binary:    map[string][]byte{"OEBPS/images/cover.png": tinyPNG},
			wantExt:   ".png",
			wantBytes: tinyPNG,
		},
		{
			name: "epub3 cover-image property",
			opf: `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
	<metadata></metadata>
	<manifest>
		<item id="img1" href="images/mycover.png" properties="cover-image" media-type="image/png"/>
	</manifest>
</package>`,
			binary:    map[string][]byte{"OEBPS/images/mycover.png": tinyPNG},
			wantExt:   ".png",
			wantBytes: tinyPNG,
		},
		{
			name: "epub3 cover-image property multi-token",
			opf: `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
	<metadata></metadata>
	<manifest>
		<item id="img1" href="images/mycover.png" properties="foo cover-image" media-type="image/png"/>
	</manifest>
</package>`,
			binary:    map[string][]byte{"OEBPS/images/mycover.png": tinyPNG},
			wantExt:   ".png",
			wantBytes: tinyPNG,
		},
		{
			name: "manifest cover-image id",
			opf: `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
	<metadata></metadata>
	<manifest>
		<item id="cover-image" href="images/id-cover.png" media-type="image/png"/>
	</manifest>
</package>`,
			binary:    map[string][]byte{"OEBPS/images/id-cover.png": tinyPNG},
			wantExt:   ".png",
			wantBytes: tinyPNG,
		},
		{
			name: "first spine image item",
			opf: `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
	<metadata></metadata>
	<manifest>
		<item id="page-1" href="images/page-1.png" media-type="image/png"/>
	</manifest>
	<spine>
		<itemref idref="page-1"/>
	</spine>
</package>`,
			binary:    map[string][]byte{"OEBPS/images/page-1.png": tinyPNG},
			wantExt:   ".png",
			wantBytes: tinyPNG,
		},
		{
			name: "guide cover page image",
			opf: `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
	<metadata></metadata>
	<manifest>
		<item id="cover-page" href="text/cover.xhtml" media-type="application/xhtml+xml"/>
		<item id="cover-image" href="images/cover%20image.png" media-type="image/png"/>
	</manifest>
	<guide>
		<reference type="cover" href="text/cover.xhtml#cover"/>
	</guide>
</package>`,
			files: map[string]string{
				"OEBPS/text/cover.xhtml": `<?xml version="1.0"?><html xmlns="http://www.w3.org/1999/xhtml"><body><img src="../images/cover%20image.png"/></body></html>`,
			},
			binary:    map[string][]byte{"OEBPS/images/cover image.png": tinyPNG},
			wantExt:   ".png",
			wantBytes: tinyPNG,
		},
		{
			name: "microsoft guide cover type",
			opf: `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
	<metadata></metadata>
	<manifest>
		<item id="cover-image" href="images/ms-cover.png" media-type="image/png"/>
	</manifest>
	<guide>
		<reference type="other.ms-coverimage-standard" href="images/ms-cover.png"/>
	</guide>
</package>`,
			binary:    map[string][]byte{"OEBPS/images/ms-cover.png": tinyPNG},
			wantExt:   ".png",
			wantBytes: tinyPNG,
		},
		{
			name: "gif cover image",
			opf: `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
	<metadata></metadata>
	<manifest>
		<item id="cover-image" href="images/cover.gif" media-type="image/gif" properties="cover-image"/>
	</manifest>
</package>`,
			binary:    map[string][]byte{"OEBPS/images/cover.gif": tinyGIF},
			wantExt:   ".gif",
			wantBytes: tinyGIF,
		},
		{
			name: "webp cover image",
			opf: `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
	<metadata></metadata>
	<manifest>
		<item id="cover-image" href="images/cover.webp" media-type="image/webp" properties="cover-image"/>
	</manifest>
</package>`,
			binary:    map[string][]byte{"OEBPS/images/cover.webp": tinyWebP},
			wantExt:   ".webp",
			wantBytes: tinyWebP,
		},
		{
			name: "no cover",
			opf: `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
	<metadata></metadata>
	<manifest>
		<item id="text" href="text.html" media-type="application/xhtml+xml"/>
	</manifest>
</package>`,
			binary:    nil,
			wantExt:   "",
			wantBytes: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string]string{
				"META-INF/container.xml": containerXML,
				"OEBPS/content.opf":      tt.opf,
			}
			maps.Copy(files, tt.files)
			r := createTestEPUB(t, files, tt.binary)
			b, ext, err := ExtractEPUBCover(r, int64(r.Len()))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ext != tt.wantExt {
				t.Errorf("got ext %q, want %q", ext, tt.wantExt)
			}
			if !bytes.Equal(b, tt.wantBytes) {
				t.Errorf("bytes mismatch")
			}
		})
	}
}

func TestExtractEPUBCoverRejectsOversizedImageEntry(t *testing.T) {
	r := createTestEPUB(t, map[string]string{
		"META-INF/container.xml": `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
	<rootfiles>
		<rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
	</rootfiles>
</container>`,
		"OEBPS/content.opf": `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
	<metadata>
		<meta name="cover" content="cover-image"/>
	</metadata>
	<manifest>
		<item id="cover-image" href="images/cover.png" media-type="image/png"/>
	</manifest>
</package>`,
	}, map[string][]byte{
		"OEBPS/images/cover.png": bytes.Repeat([]byte{0}, maxEPUBCoverBytes+1),
	})

	if _, _, err := ExtractEPUBCover(r, int64(r.Len())); err == nil {
		t.Fatalf("ExtractEPUBCover oversized entry error = nil; want size error")
	}
}
