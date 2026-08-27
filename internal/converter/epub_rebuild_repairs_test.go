package converter

import (
	"bytes"
	"context"
	"encoding/xml"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/format"
)

func TestRemoveEmptyTours(t *testing.T) {
	const packageStart = `<package xmlns="http://www.idpf.org/2007/opf" version="2.0">`
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "one empty container",
			body: "<metadata/><tours>\n  </tours><guide/>",
			want: "<metadata/><guide/>",
		},
		{
			name: "attributed container",
			body: `<metadata/><tours class="producer"></tours><guide/>`,
			want: `<metadata/><tours class="producer"></tours><guide/>`,
		},
		{
			name: "nonempty container",
			body: `<metadata/><tours><tour title="Contents"/></tours><guide/>`,
			want: `<metadata/><tours><tour title="Contents"/></tours><guide/>`,
		},
		{
			name: "repeated empty containers",
			body: `<metadata/><tours/><tours/><guide/>`,
			want: `<metadata/><tours/><tours/><guide/>`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(packageStart + tt.body + `</package>`)
			want := packageStart + tt.want + `</package>`
			if got := string(removeEmptyTours(raw)); got != want {
				t.Fatalf("removeEmptyTours() = %q; want %q", got, want)
			}
		})
	}
}

func TestEscapeNCXNavLabelAmpersand(t *testing.T) {
	ncx := func(body string) []byte {
		return []byte(`<?xml version="1.0" encoding="UTF-8"?>` +
			`<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/">` +
			body +
			`</ncx>`)
	}
	tests := []struct {
		name        string
		raw         []byte
		wantChanged bool
		wantText    string
	}{
		{
			name:        "simple navigation label",
			raw:         ncx(`<navMap><navPoint><navLabel><text>Truth & Consequence</text></navLabel><content src="chapter.xhtml"/></navPoint></navMap>`),
			wantChanged: true,
			wantText:    "Truth &amp; Consequence",
		},
		{
			name:     "valid entity",
			raw:      ncx(`<navMap><navPoint><navLabel><text>Truth &amp; Consequence</text></navLabel><content src="chapter.xhtml"/></navPoint></navMap>`),
			wantText: "Truth &amp; Consequence",
		},
		{
			name:     "ampersand in target attribute",
			raw:      ncx(`<navMap><navPoint><navLabel><text>Truth</text></navLabel><content src="truth & consequence.xhtml"/></navPoint></navMap>`),
			wantText: `src="truth & consequence.xhtml"`,
		},
		{
			name:     "multiple navigation candidates",
			raw:      ncx(`<navMap><navPoint><navLabel><text>Truth & Consequence</text></navLabel><content src="chapter.xhtml"/></navPoint><navPoint><navLabel><text>Fact & Fiction</text></navLabel><content src="other.xhtml"/></navPoint></navMap>`),
			wantText: "Truth & Consequence",
		},
		{
			name:     "other syntax error remains",
			raw:      ncx(`<navMap><navPoint><navLabel><text>Truth & Consequence</text></navLabel></navMap>`),
			wantText: "Truth & Consequence",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := escapeNCXNavLabelAmpersand(tt.raw)
			if changed != tt.wantChanged {
				t.Fatalf("escapeNCXNavLabelAmpersand() changed = %v; want %v", changed, tt.wantChanged)
			}
			if !strings.Contains(string(got), tt.wantText) {
				t.Fatalf("escapeNCXNavLabelAmpersand() = %q; want text %q", got, tt.wantText)
			}
			if !tt.wantChanged && !bytes.Equal(got, tt.raw) {
				t.Fatal("ambiguous NCX repair changed source bytes")
			}
		})
	}
}

func TestNormalizeVendorImageGuideAmbiguity(t *testing.T) {
	const (
		packageStart = `<package xmlns="http://www.idpf.org/2007/opf" version="2.0">`
		guide        = `<guide><reference type="other.ms-coverimage-standard" href="cover.png"/></guide>`
	)
	tests := []struct {
		name        string
		metadata    string
		manifest    string
		wantChanged bool
		wantGuide   bool
	}{
		{
			name:        "matching standard metadata replaces vendor hint",
			metadata:    `<metadata><meta name="cover" content="cover-a"/></metadata>`,
			manifest:    `<manifest><item id="cover-a" href="cover.png" media-type="image/png"/></manifest>`,
			wantChanged: true,
		},
		{
			name:      "conflicting standard metadata preserves vendor hint",
			metadata:  `<metadata><meta name="cover" content="cover-a"/><meta name="cover" content="cover-b"/></metadata>`,
			manifest:  `<manifest><item id="cover-a" href="cover.png" media-type="image/png"/></manifest>`,
			wantGuide: true,
		},
		{
			name:      "duplicate manifest target preserves vendor hint",
			metadata:  `<metadata/>`,
			manifest:  `<manifest><item id="cover-a" href="cover.png" media-type="image/png"/><item id="cover-b" href="cover.png" media-type="image/png"/></manifest>`,
			wantGuide: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(packageStart + tt.metadata + tt.manifest + guide + `</package>`)
			got := normalizeVendorImageGuide("package.opf", raw)
			if changed := !bytes.Equal(got, raw); changed != tt.wantChanged {
				t.Fatalf("repair changed = %v; want %v\n%s", changed, tt.wantChanged, got)
			}
			if kept := bytes.Contains(got, []byte("other.ms-coverimage-standard")); kept != tt.wantGuide {
				t.Fatalf("vendor guide kept = %v; want %v\n%s", kept, tt.wantGuide, got)
			}
			if repeated := normalizeVendorImageGuide("package.opf", got); !bytes.Equal(repeated, got) {
				t.Fatal("second vendor-guide repair changed OPF bytes")
			}
		})
	}
}

func TestRebuildEPUB2HTMLDoctypeEdit(t *testing.T) {
	exact := []byte(`<?xml version="1.0"?><!DOCTYPE html><html xmlns="http://www.w3.org/1999/xhtml"><body><p>Text.</p></body></html>`)
	edit, ok := rebuildEPUB2HTMLDoctypeEdit(exact)
	if !ok {
		t.Fatal("exact EPUB2 HTML doctype was not recognized")
	}
	got, changed := applyRebuildXMLEdits(exact, []rebuildXMLEdit{edit})
	want := strings.Replace(string(exact), "<!DOCTYPE html>", "<!"+rebuildXHTML11Doctype+">", 1)
	if !changed || string(got) != want {
		t.Fatalf("EPUB2 HTML doctype repair = %q; want %q", got, want)
	}

	for name, raw := range map[string][]byte{
		"unknown declaration": []byte(`<!DOCTYPE html SYSTEM "custom.dtd"><html xmlns="http://www.w3.org/1999/xhtml"/>`),
		"non-XHTML root":      []byte(`<!DOCTYPE html><html/>`),
		"multiple roots":      []byte(`<!DOCTYPE html><html xmlns="http://www.w3.org/1999/xhtml"/><html xmlns="http://www.w3.org/1999/xhtml"/>`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := rebuildEPUB2HTMLDoctypeEdit(raw); ok {
				t.Fatal("ambiguous EPUB2 doctype shape was accepted")
			}
		})
	}
}

func TestConvertEPUBToEPUBRepairsProducerNavigationCase(t *testing.T) {
	opf := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" unique-identifier="bookid" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:identifier id="bookid">urn:example:book</dc:identifier>
    <dc:title>Synthetic navigation case</dc:title>
  </metadata>
  <manifest>
    <item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/>
    <item id="cover" href="cover.xhtml" media-type="application/xhtml+xml"/>
    <item id="style" href="style.css" media-type="text/css"/>
    <item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/>
  </manifest>
  <spine toc="ncx"><itemref idref="chapter"/></spine>
  <tours>
  </tours>
  <guide><reference type="text" title="Start" href="chapter.xhtml"/></guide>
</package>`)
	ncx := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1">
  <head><meta name="dtb:uid" content="urn:example:book"/></head>
  <docTitle><text>Synthetic navigation case</text></docTitle>
  <navMap>
    <navPoint id="part-two" playOrder="1">
      <navLabel><text>Part II: Truth & Consequence</text></navLabel>
      <content src="chapter.xhtml"/>
    </navPoint>
  </navMap>
</ncx>`)
	chapter := []byte(`<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.1//EN" "http://www.w3.org/TR/xhtml11/DTD/xhtml11.dtd"><html xmlns="http://www.w3.org/1999/xhtml"><body><p>Preserved text.</p></body></html>`)
	cover := []byte(`<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE html><html xmlns="http://www.w3.org/1999/xhtml"><head><title>Cover</title></head><body><svg xmlns="http://www.w3.org/2000/svg"><image href="cover.jpg"/></svg></body></html>`)
	style := []byte("body { margin: 1em; }\n")
	src := testZip(t, map[string][]byte{
		"META-INF/container.xml": []byte(`<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OPS/package.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`),
		"OPS/package.opf":        opf,
		"OPS/chapter.xhtml":      chapter,
		"OPS/cover.xhtml":        cover,
		"OPS/style.css":          style,
		"OPS/toc.ncx":            ncx,
	})

	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("rebuild EPUB with empty tours and malformed NCX label: %v", err)
	}
	gotOPF := zipEntry(t, out.Bytes(), "OPS/package.opf")
	if strings.Contains(gotOPF, "<tours") || !strings.Contains(gotOPF, `<guide><reference type="text" title="Start" href="chapter.xhtml"/></guide>`) {
		t.Fatalf("rebuilt OPF did not remove only empty tours:\n%s", gotOPF)
	}

	gotNCX := zipEntryBytes(t, out.Bytes(), "OPS/toc.ncx")
	var decoded struct {
		NavMap struct {
			Points []struct {
				Label struct {
					Text string `xml:"text"`
				} `xml:"navLabel"`
			} `xml:"navPoint"`
		} `xml:"navMap"`
	}
	if err := xml.Unmarshal(gotNCX, &decoded); err != nil {
		t.Fatalf("rebuilt NCX is not strict XML: %v\n%s", err, gotNCX)
	}
	if len(decoded.NavMap.Points) != 1 || decoded.NavMap.Points[0].Label.Text != "Part II: Truth & Consequence" {
		t.Fatalf("rebuilt NCX navigation = %+v; want repaired original label", decoded.NavMap.Points)
	}
	if !strings.Contains(string(gotNCX), "Part II: Truth &amp; Consequence") {
		t.Fatalf("rebuilt NCX did not escape only the bare ampersand:\n%s", gotNCX)
	}
	wantCover := strings.Replace(string(cover), "<!DOCTYPE html>", "<!"+rebuildXHTML11Doctype+">", 1)
	if got := zipEntry(t, out.Bytes(), "OPS/cover.xhtml"); got != wantCover {
		t.Fatalf("rebuilt EPUB2 cover changed more than the exact HTML doctype:\n%s", got)
	}
	for name, want := range map[string][]byte{
		"OPS/chapter.xhtml": chapter,
		"OPS/style.css":     style,
	} {
		if got := zipEntryBytes(t, out.Bytes(), name); !bytes.Equal(got, want) {
			t.Fatalf("rebuilt EPUB changed unrelated entry %s", name)
		}
	}

	var repeated bytes.Buffer
	if err := ConvertContext(context.Background(), &repeated, bytes.NewReader(out.Bytes()), format.FormatEPUB, int64(out.Len()), TargetEPUB); err != nil {
		t.Fatalf("repeat producer-navigation rebuild: %v", err)
	}
	if !bytes.Equal(out.Bytes(), repeated.Bytes()) {
		t.Fatal("second producer-navigation rebuild changed archive bytes")
	}
}

func TestConvertEPUBToEPUBPreservesAmbiguousNCXNavLabelFailure(t *testing.T) {
	opf := []byte(`<package xmlns="http://www.idpf.org/2007/opf" version="2.0"><manifest><item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/><item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/></manifest><spine toc="ncx"><itemref idref="chapter"/></spine></package>`)
	ncx := []byte(`<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/"><navMap><navPoint><navLabel><text>Truth & Consequence</text></navLabel><content src="truth & consequence.xhtml"/></navPoint></navMap></ncx>`)
	src := testZip(t, map[string][]byte{
		"META-INF/container.xml": []byte(`<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OPS/package.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`),
		"OPS/package.opf":        opf,
		"OPS/chapter.xhtml":      []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><body><p>Text.</p></body></html>`),
		"OPS/toc.ncx":            ncx,
	})

	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("rebuild EPUB with ambiguous malformed NCX: %v", err)
	}
	if got := zipEntryBytes(t, out.Bytes(), "OPS/toc.ncx"); !bytes.Equal(got, ncx) {
		t.Fatalf("ambiguous NCX changed:\n%s", got)
	}
}
