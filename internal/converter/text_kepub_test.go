package converter

import (
	"bytes"
	"testing"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/format"
)

func TestConvertTextAndWebToKEPUB(t *testing.T) {
	const body = "Привет, café."
	markdown := []byte("# Chapter\n\n" + body + " **Bold** and [back](#chapter).\n\n    keep  spaces\n")
	web := `<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Web notes</title></head><body>
<h1 id="chapter">Chapter</h1>
<p>` + body + ` <strong>Bold</strong> and <a href="#chapter">back</a>.</p>
<pre>keep  spaces
  and indentation</pre></body></html>`
	const webText = "Chapter Привет, café. Bold and back. keep spaces and indentation"
	for _, tt := range []struct {
		kind     format.Format
		src      []byte
		wantText string
	}{
		{kind: format.FormatTXT, src: []byte(body + "\n\nSecond paragraph."), wantText: body + " Second paragraph."},
		{kind: format.FormatMarkdown, src: markdown, wantText: "Chapter " + body + " Bold and back. keep spaces"},
		{kind: format.FormatTXTZ, src: testZip(t, map[string][]byte{"book.md": markdown}), wantText: "Chapter " + body + " Bold and back. keep spaces"},
		{kind: format.FormatHTML, src: []byte(web), wantText: webText},
		{
			kind:     format.FormatXHTML,
			src:      []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><head><title>XML notes</title></head><body><p><![CDATA[<literal> & Привет, café.]]></p></body></html>`),
			wantText: "<literal> & " + body,
		},
		{kind: format.FormatHTMLZ, src: testZip(t, map[string][]byte{"index.html": []byte(web)}), wantText: webText},
	} {
		t.Run(format.FormatLabel(tt.kind), func(t *testing.T) {
			opts := ConversionOptions{Metadata: &bookmeta.Metadata{
				Title: "Library notes", Authors: []bookmeta.AuthorMeta{{Name: "Note Author"}}, Language: "ru",
			}}
			var kepub bytes.Buffer
			if err := ConvertContextWithOptions(t.Context(), &kepub, bytes.NewReader(tt.src), tt.kind, int64(len(tt.src)), TargetKEPUB, opts); err != nil {
				t.Fatalf("convert to KEPUB: %v", err)
			}
			content := zipEntryBytes(t, kepub.Bytes(), "OEBPS/text.xhtml")
			if got := testKEPUBReadingText(t, content); got != tt.wantText {
				t.Fatalf("reading text = %q; want %q", got, tt.wantText)
			}
			if len(testKEPUBSpanSnapshots(t, content)) == 0 {
				t.Fatal("KEPUB has no Kobo position spans")
			}
			meta, err := format.ExtractEPUBMetadata(bytes.NewReader(kepub.Bytes()), int64(kepub.Len()))
			if err != nil {
				t.Fatalf("read KEPUB metadata: %v", err)
			}
			if meta.Title != "Library notes" || len(meta.Authors) != 1 || meta.Authors[0].Name != "Note Author" || meta.Language != "ru" {
				t.Fatalf("KEPUB metadata = %+v; want catalog title, author and language", meta)
			}
			if problems, err := checkEPUBInternalLinks(kepub.Bytes()); err != nil || len(problems) != 0 {
				t.Fatalf("KEPUB internal links: %v, %v", problems, err)
			}
		})
	}
}
