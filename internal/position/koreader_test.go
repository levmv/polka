package position

import (
	"bytes"
	"testing"
)

func TestKOReaderPositions(t *testing.T) {
	src := positionEPUB(t, `<p id="a[1]">A😀<!-- note --><![CDATA[B]]>C <em>word</em> tail.</p><table><tr><td>Repeat.</td></tr></table><p><a id="empty"/>Repeat.</p><p>A&nbsp;B&mdash;C<![CDATA[&nbsp;]]>D</p><p>Before<blockquote><p>Inside.</p></blockquote>After</p><p>Later.</p><div><a id="chapter"/><img src="cover.png"/><br/>Text.</div>`)
	const cfiPrefix = "epubcfi(/6/4[main]!/4"
	const xpPrefix = "/body[1]/DocFragment[2]/body[1]"
	for _, tt := range []struct{ name, cfi, xpointer string }{
		{"Unicode", "/2[a^[1^]]/1:1)", "/p[1]/text()[1].1"},
		{"CDATA after comment", "/2[a^[1^]]/1:3)", "/p[1]/text()[2].0"},
		{"after CDATA", "/2[a^[1^]]/1:5)", "/p[1]/text()[3].1"},
		{"inline", "/2[a^[1^]]/2/1:2)", "/p[1]/em[1]/text()[1].2"},
		{"after inline", "/2[a^[1^]]/3:2)", "/p[1]/text()[4].2"},
		{"table", "/4/2/2/1:3)", "/table[1]/tr[1]/td[1]/text()[1].3"},
		{"repeated passage after empty anchor", "/6/3:3)", "/p[2]/text()[1].3"},
		{"named entities", "/8/1:4)", "/p[3]/text()[1].4"},
		{"literal entity in CDATA", "/8/1:6)", "/p[3]/text()[2].1"},
		{"after entity and CDATA", "/8/1:11)", "/p[3]/text()[3].0"},
		{"nested block", "/10/2/2/1:2)", "/p[4]/blockquote[1]/p[1]/text()[1].2"},
		{"after nested block", "/12/1:2)", "/p[5]/text()[1].2"},
		{"empty chapter anchor", "/14/2[chapter])", "/div[1]/a[1]"},
		{"image", "/14/4)", "/div[1]/img[1]"},
		{"line break", "/14/6)", "/div[1]/br[1]"},
		{"container start", "/14)", "/div[1]"},
		{"chapter start", ")", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfi, xp := cfiPrefix+tt.cfi, xpPrefix+tt.xpointer
			got, err := CFIToKOReader(t.Context(), bytes.NewReader(src), int64(len(src)), cfi)
			if err != nil || got != xp {
				t.Fatalf("CFIToKOReader = %q, %v; want %q", got, err, xp)
			}
			got, err = KOReaderToCFI(t.Context(), bytes.NewReader(src), int64(len(src)), xp)
			if err != nil || got != cfi {
				t.Fatalf("KOReaderToCFI = %q, %v; want %q", got, err, cfi)
			}
		})
	}
	// KOReader can omit [1] for an only child.
	got, err := KOReaderToCFI(t.Context(), bytes.NewReader(src), int64(len(src)), "/body/DocFragment[2]/body/p[1]/em/text().2")
	if err != nil || got != cfiPrefix+"/2[a^[1^]]/2/1:2)" {
		t.Fatalf("implicit first indices = %q, %v", got, err)
	}
	// Page starts on images can use an element offset of zero.
	got, err = KOReaderToCFI(t.Context(), bytes.NewReader(src), int64(len(src)), "/body/DocFragment[2]/body/div/img.0")
	if err != nil || got != cfiPrefix+"/14/4)" {
		t.Fatalf("image page start = %q, %v", got, err)
	}
}

func TestKOReaderRepeatedSpineResource(t *testing.T) {
	src := positionEPUB(t, "<p>Repeat.</p>", `<itemref id="again" idref="text"/>`)
	for _, tt := range []struct{ cfi, xpointer string }{
		{"epubcfi(/6/4[main]!/4/2/1:3)", "/body[1]/DocFragment[2]/body[1]/p[1]/text()[1].3"},
		{"epubcfi(/6/6[again]!/4/2/1:3)", "/body[1]/DocFragment[3]/body[1]/p[1]/text()[1].3"},
	} {
		cfi, err := KOReaderToCFI(t.Context(), bytes.NewReader(src), int64(len(src)), tt.xpointer)
		if err != nil || cfi != tt.cfi {
			t.Fatalf("repeated resource import = %q, %v; want %q", cfi, err, tt.cfi)
		}
		xp, err := CFIToKOReader(t.Context(), bytes.NewReader(src), int64(len(src)), tt.cfi)
		if err != nil || xp != tt.xpointer {
			t.Fatalf("repeated resource export = %q, %v; want %q", xp, err, tt.xpointer)
		}
	}
}

func TestKOReaderParagraphPositions(t *testing.T) {
	for _, tt := range []struct{ name, body, cfi, xpointer, paragraph string }{
		{"whitespace setting", "<p>A  B</p>", "/2/1:3)", "/p/text().2", "/p[1]"},
		{"discarded formatting node", "<p> <em>A</em> B</p>", "/2/3:1)", "/p/text().1", "/p[1]"},
		{"inside inline", "<p>Start <em>A  B</em></p>", "/2/2/1:3)", "/p/em/text().2", "/p[1]"},
		{"pre DOM version", "<pre>\nAB</pre>", "/2/1:2)", "/pre/text().1", "/pre[1]"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			src := positionEPUB(t, tt.body)
			ctx := t.Context()
			xp, exportErr := CFIToKOReader(ctx, bytes.NewReader(src), int64(len(src)), "epubcfi(/6/4[main]!/4"+tt.cfi)
			cfi, importErr := KOReaderToCFI(ctx, bytes.NewReader(src), int64(len(src)), "/body/DocFragment[2]/body"+tt.xpointer)
			const wantCFI = "epubcfi(/6/4[main]!/4/2)"
			wantXP := "/body[1]/DocFragment[2]/body[1]" + tt.paragraph
			if importErr != nil || exportErr != nil || cfi != wantCFI || xp != wantXP {
				t.Fatalf("paragraph = %q, %v; %q, %v; want %q, %q", cfi, importErr, xp, exportErr, wantCFI, wantXP)
			}
			if got, err := KOReaderToCFI(ctx, bytes.NewReader(src), int64(len(src)), xp); err != nil || got != cfi {
				t.Fatalf("paragraph import moved again: %q, %v", got, err)
			}
			if got, err := CFIToKOReader(ctx, bytes.NewReader(src), int64(len(src)), cfi); err != nil || got != xp {
				t.Fatalf("paragraph export moved again: %q, %v", got, err)
			}
		})
	}
}
