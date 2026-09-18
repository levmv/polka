package position

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/converter"
	"github.com/levmv/polka/internal/format"
)

func TestKEPUBPositions(t *testing.T) {
	src := positionEPUB(t, `<p id="a[1]">A😀<![CDATA[B]]>C. Next <em>word</em>.</p><table><tr><td>Repeat.</td></tr></table><p>Repeat.</p>`)
	var out bytes.Buffer
	if err := converter.ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), converter.TargetKEPUB); err != nil {
		t.Fatal(err)
	}
	spans := kepubSpans(t, out.Bytes())
	const prefix = "epubcfi(/6/4[main]!/4"
	for _, tt := range []struct {
		name, point, start, text string
		occurrence               int
	}{
		{"UTF-16 and CDATA", "/2[a^[1^]]/1:9)", "/2[a^[1^]]/1:7)", "Next ", 0},
		{"inline", "/2/2/1:2)", "/2[a^[1^]]/2/1:0)", "word", 0},
		{"table", "/4/2/2/1:4)", "/4/2/2/1:0)", "Repeat.", 0},
		{"repeated passage", "/6/1:4)", "/6/1:0)", "Repeat.", 1},
		{"range start", "/2,/1:3,/1:5)", "/2[a^[1^]]/1:0)", "A😀BC. ", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Compare to the spans in a complete delivered KEPUB, not a second
			// copy of the chapter-only mapping algorithm.
			want := KEPUB{Path: "OPS/chapter.xhtml"}
			occurrence := 0
			for _, span := range spans {
				if span.Text == tt.text {
					if occurrence == tt.occurrence {
						want.Fragment = span.ID
						break
					}
					occurrence++
				}
			}
			if want.Fragment == "" {
				t.Fatalf("missing span %q in %+v", tt.text, spans)
			}
			got, err := CFIToKEPUB(context.Background(), bytes.NewReader(src), int64(len(src)), prefix+tt.point)
			if err != nil || got != want {
				t.Fatalf("CFIToKEPUB = %+v, %v; want %+v", got, err, want)
			}
			cfi, err := KEPUBToCFI(context.Background(), bytes.NewReader(src), int64(len(src)), got)
			if err != nil || cfi != prefix+tt.start {
				t.Fatalf("KEPUBToCFI = %q, %v; want %q", cfi, err, prefix+tt.start)
			}
		})
	}

	for _, cfi := range []string{prefix + "/2[stale]/1:0)", prefix + "/2/1:999)"} {
		if _, err := CFIToKEPUB(context.Background(), bytes.NewReader(src), int64(len(src)), cfi); err == nil {
			t.Errorf("accepted a stale location: %s", cfi)
		}
	}
}

func TestKEPUBPositionsDeclineInexactMapping(t *testing.T) {
	for _, tt := range []struct {
		name, body string
	}{
		{"reordered table text", "<table><tr><td>A.</td></tr>Moved text.</table><p>B.</p>"},
		{"image anchor", `<p><img src="cover.png" alt="cover"/></p>`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			src := positionEPUB(t, tt.body)
			_, err := KEPUBToCFI(context.Background(), bytes.NewReader(src), int64(len(src)), KEPUB{Path: "OPS/chapter.xhtml", Fragment: "kobo.1.1"})
			if err == nil {
				t.Fatal("accepted a position without a matching source text location")
			}
		})
	}
}

func positionEPUB(t *testing.T, body string, extraSpine ...string) []byte {
	t.Helper()
	entries := map[string][]byte{
		"mimetype": []byte("application/epub+zip"),
		"META-INF/container.xml": []byte(`<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles>
<rootfile full-path="OPS/book.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`),
		"OPS/book.opf": []byte(`<package xmlns="http://www.idpf.org/2007/opf" version="3.0"><metadata/><manifest>
<item id="cover" href="cover.xhtml" media-type="application/xhtml+xml"/>
<item id="text" href="chapter.xhtml" media-type="application/xhtml+xml"/>
</manifest><spine><itemref idref="cover" linear="no"/><itemref id="main" idref="text"/>` + strings.Join(extraSpine, "") + `</spine></package>`),
		"OPS/cover.xhtml":   []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Cover</title></head><body><p>Cover.</p></body></html>`),
		"OPS/chapter.xhtml": []byte("\uFEFF" + `<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Chapter</title></head><body>` + body + "</body>\n</html>\n"),
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for name, raw := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

type kepubSpan struct {
	ID   string `xml:"id,attr"`
	Text string `xml:",chardata"`
}

func kepubSpans(t *testing.T, data []byte) []kepubSpan {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	chapter, err := zr.Open("OPS/chapter.xhtml")
	if err != nil {
		t.Fatal(err)
	}
	defer chapter.Close()
	decoder := xml.NewDecoder(chapter)
	var spans []kepubSpan
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return spans
		}
		if err != nil {
			t.Fatal(err)
		}
		if start, ok := token.(xml.StartElement); ok && start.Name.Local == "span" {
			for _, attr := range start.Attr {
				if attr.Name.Local == "class" && attr.Value == "koboSpan" {
					var span kepubSpan
					if err := decoder.DecodeElement(&span, &start); err != nil {
						t.Fatal(err)
					}
					spans = append(spans, span)
					break
				}
			}
		}
	}
}
