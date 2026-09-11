package converter

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/format"
	"golang.org/x/net/html"
)

func TestFB2WhitespaceAtStartOfNestedInlineWrapper(t *testing.T) {
	raw := []byte(`<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0"><description><title-info><book-title>Spacing</book-title><lang>en</lang></title-info></description><body><section><p><emphasis><strong>alpha</strong></emphasis><strong>  <emphasis>beta</emphasis></strong></p><p>gamma</p></section></body></FictionBook>`)
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(raw), format.FormatFB2, int64(len(raw)), TargetEPUB); err != nil {
		t.Fatal(err)
	}
	xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
	if !strings.Contains(xhtml, `<strong> <em>beta</em></strong>`) {
		t.Fatalf("separator lost: %s", xhtml)
	}
}

func TestKindleFileposPreservesCharacterReferencesAndUTF8(t *testing.T) {
	for _, token := range []string{"&nbsp;", "&#160;", "&#xA0;", "é", "中"} {
		t.Run(token, func(t *testing.T) {
			raw := []byte("<p>alpha" + token + "beta</p>")
			var refs []int
			for i := 1; i < len(token); i++ {
				refs = append(refs, len("<p>alpha")+i)
			}
			got := insertKindleFileposAnchors(raw, refs)
			z := html.NewTokenizer(bytes.NewReader(got))
			var text strings.Builder
			for {
				kind := z.Next()
				if kind == html.ErrorToken {
					break
				}
				if kind == html.TextToken {
					text.Write(z.Text())
				}
			}
			want := "alpha" + html.UnescapeString(token) + "beta"
			if text.String() != want {
				t.Fatalf("got=%q want=%q output=%s", text.String(), want, got)
			}
			for _, ref := range refs {
				if !bytes.Contains(got, []byte(fmt.Sprintf(`id="filepos%d"`, ref))) {
					t.Fatalf("missing anchor %d", ref)
				}
			}
		})
	}
	if got := kindleAnchorTextOffset([]byte("a&nbsp;b;"), 7); got != 7 {
		t.Fatalf("offset outside entity moved to %d", got)
	}
}

func TestMarkdownFencesPreserveCodeAndWhitespace(t *testing.T) {
	for _, fence := range []string{"```", "~~~~"} {
		source := fence + "go\n  value_name := 2 * 3\n\n \t\n\n  print(\"<tag>\")\n" + fence + "\n"
		raw := []byte(strings.ReplaceAll(source, "\n", "\r\n"))
		var out bytes.Buffer
		if err := ConvertContext(t.Context(), &out, bytes.NewReader(raw), format.FormatMarkdown, int64(len(raw)), TargetEPUB); err != nil {
			t.Fatal(err)
		}
		xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
		want := "<pre><code>  value_name := 2 * 3\n\n \t\n\n  print(&#34;&lt;tag&gt;&#34;)\n</code></pre>"
		if !strings.Contains(xhtml, want) {
			t.Fatalf("fence=%q output=%s", fence, xhtml)
		}
	}
}

func TestKindleFileposInsideTagsPreservesText(t *testing.T) {
	raw := []byte(`<p title="example">alpha</p><p>beta</p>`)
	var refs []int
	for i := 0; i < len(raw); i++ {
		refs = append(refs, i)
	}
	got := insertKindleFileposAnchors(raw, refs)
	z := html.NewTokenizer(bytes.NewReader(got))
	var text strings.Builder
	for {
		kind := z.Next()
		if kind == html.ErrorToken {
			break
		}
		if kind == html.TextToken {
			text.Write(z.Text())
		}
	}
	if text.String() != "alphabeta" {
		t.Fatalf("text=%q output=%s", text.String(), got)
	}
	for _, ref := range refs {
		if !bytes.Contains(got, []byte(fmt.Sprintf(`id="filepos%d"`, ref))) {
			t.Fatalf("missing anchor %d: %s", ref, got)
		}
	}
}
