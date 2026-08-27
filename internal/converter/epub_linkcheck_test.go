package converter

import (
	"archive/zip"
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/format"
)

func TestCheckEPUBInternalLinksAcceptsCleanKindleOutput(t *testing.T) {
	doc := &format.KindleDocument{
		Metadata: &format.Metadata{
			Title:    "Kindle Export",
			Language: "en",
			Authors:  []bookmeta.AuthorMeta{{Name: "Jane Doe"}},
		},
		Flows: []format.KindleTextFlow{{
			ID:        "flow-0001",
			Href:      "text/flow-0001.html",
			MediaType: "text/html",
			Data: []byte(`<html><body><p><a filepos=0000000012>Go</a></p>` +
				`<p id="x"><img recindex="00001"><img src="kindle:embed:0001?mime=image/png" alt="e">` +
				`<img src="kindle:flow:0003?mime=image/svg+xml" alt="s"></p></body></html>`),
		}},
		Resources: []format.KindleResource{{
			ID: "res-00001", Href: "images/00001.png", MediaType: "image/png",
			Data: converterTinyPNG, EmbedIndex: 1, Cover: true,
		}, {
			ID: "svg-0003", Href: "images/flow-0003.svg", MediaType: "image/svg+xml",
			Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"></svg>`), FlowIndex: 3,
		}, {
			ID: "style-0001", Href: "styles/flow-0001.css", MediaType: "text/css",
			Data: []byte(".figure { background: url(\"kindle:flow:0003?mime=image/svg+xml\"); }\n"),
		}},
		Navigation: []format.KindleNavItem{{Label: "Chapter", Href: "text/flow-0001.html#filepos12"}},
	}

	var out bytes.Buffer
	if err := convertKindleDocumentToEPUB(context.Background(), &out, doc, ConversionOptions{}); err != nil {
		t.Fatalf("convertKindleDocumentToEPUB: %v", err)
	}
	problems, err := checkEPUBInternalLinks(out.Bytes())
	if err != nil {
		t.Fatalf("checkEPUBInternalLinks: %v", err)
	}
	if len(problems) != 0 {
		t.Fatalf("clean Kindle EPUB has %d link problems:\n%s", len(problems), joinProblems(problems))
	}
}

func TestCheckEPUBInternalLinksDetectsBrokenReferences(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(map[string]string)
		want    string
		wantLoc string
	}{
		{
			name:   "clean base has no problems",
			mutate: func(map[string]string) {},
		},
		{
			name: "manifest item target missing",
			mutate: func(files map[string]string) {
				files["OEBPS/content.opf"] = strings.Replace(files["OEBPS/content.opf"],
					`</manifest>`,
					`  <item id="extra" href="missing.bin" media-type="application/octet-stream"/>`+"\n  </manifest>", 1)
			},
			want:    "manifest item target is not packaged",
			wantLoc: "OEBPS/content.opf",
		},
		{
			name: "image src not packaged",
			mutate: func(files map[string]string) {
				files["OEBPS/text.xhtml"] = strings.Replace(files["OEBPS/text.xhtml"],
					`</body>`, `<p><img src="images/missing.png" alt=""/></p></body>`, 1)
			},
			want:    "target is not packaged",
			wantLoc: "OEBPS/text.xhtml",
		},
		{
			name: "same-document fragment missing",
			mutate: func(files map[string]string) {
				files["OEBPS/text.xhtml"] = strings.Replace(files["OEBPS/text.xhtml"],
					`</body>`, `<p><a href="#nope">x</a></p></body>`, 1)
			},
			want:    "fragment #nope has no matching id",
			wantLoc: "OEBPS/text.xhtml",
		},
		{
			name: "cross-document fragment missing",
			mutate: func(files map[string]string) {
				files["OEBPS/nav.xhtml"] = strings.Replace(files["OEBPS/nav.xhtml"],
					`</ol>`, `<li><a href="text.xhtml#nope">y</a></li></ol>`, 1)
			},
			want:    "fragment #nope has no matching id in OEBPS/text.xhtml",
			wantLoc: "OEBPS/nav.xhtml",
		},
		{
			name: "leftover kindle scheme reference",
			mutate: func(files map[string]string) {
				files["OEBPS/text.xhtml"] = strings.Replace(files["OEBPS/text.xhtml"],
					`</body>`, `<p><img src="kindle:embed:0001?mime=image/png" alt=""/></p></body>`, 1)
			},
			want:    "reference does not resolve to a packaged item",
			wantLoc: "OEBPS/text.xhtml",
		},
		{
			name: "stylesheet url target missing",
			mutate: func(files map[string]string) {
				files["OEBPS/styles/main.css"] += "\n.y { background: url(../images/missing.png); }\n"
			},
			want:    "url() target is not packaged",
			wantLoc: "OEBPS/styles/main.css",
		},
		{
			name: "cover metadata references unknown id",
			mutate: func(files map[string]string) {
				files["OEBPS/content.opf"] = strings.Replace(files["OEBPS/content.opf"],
					`content="cover"`, `content="nope"`, 1)
			},
			want:    "cover metadata references an unknown manifest id",
			wantLoc: "OEBPS/content.opf",
		},
		{
			name: "spine references unknown id",
			mutate: func(files map[string]string) {
				files["OEBPS/content.opf"] = strings.Replace(files["OEBPS/content.opf"],
					`</spine>`, `<itemref idref="ghost"/></spine>`, 1)
			},
			want:    "spine references an unknown manifest id",
			wantLoc: "OEBPS/content.opf",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := baseLinkCheckEPUBFiles()
			tt.mutate(files)
			epub := zipStringEntries(t, files)
			problems, err := checkEPUBInternalLinks(epub)
			if err != nil {
				t.Fatalf("checkEPUBInternalLinks: %v", err)
			}
			if tt.want == "" {
				if len(problems) != 0 {
					t.Fatalf("expected no problems, got %d:\n%s", len(problems), joinProblems(problems))
				}
				return
			}
			if len(problems) != 1 {
				t.Fatalf("expected exactly one problem, got %d:\n%s", len(problems), joinProblems(problems))
			}
			if !strings.Contains(problems[0].Reason, tt.want) {
				t.Fatalf("problem reason = %q; want to contain %q", problems[0].Reason, tt.want)
			}
			if problems[0].Location != tt.wantLoc {
				t.Fatalf("problem location = %q; want %q", problems[0].Location, tt.wantLoc)
			}
		})
	}
}

func baseLinkCheckEPUBFiles() map[string]string {
	return map[string]string{
		"mimetype": "application/epub+zip",
		"META-INF/container.xml": `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`,
		"OEBPS/content.opf": `<?xml version="1.0"?>
<package version="3.0" unique-identifier="pub-id" xmlns="http://www.idpf.org/2007/opf">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:identifier id="pub-id">urn:uuid:x</dc:identifier>
    <dc:title>Title</dc:title>
    <dc:language>en</dc:language>
    <meta name="cover" content="cover"/>
  </metadata>
  <manifest>
    <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
    <item id="text" href="text.xhtml" media-type="application/xhtml+xml"/>
    <item id="cover" href="images/cover.png" media-type="image/png" properties="cover-image"/>
    <item id="style" href="styles/main.css" media-type="text/css"/>
  </manifest>
  <spine><itemref idref="text"/></spine>
</package>`,
		"OEBPS/nav.xhtml": `<?xml version="1.0"?>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Nav</title></head>
<body><nav><ol><li><a href="text.xhtml#chap1">Chapter</a></li></ol></nav></body></html>`,
		"OEBPS/text.xhtml": `<?xml version="1.0"?>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Text</title><link rel="stylesheet" href="styles/main.css"/></head>
<body><h1 id="chap1">Chapter</h1><p><a href="#chap1">top</a><img src="images/cover.png" alt=""/></p></body></html>`,
		"OEBPS/styles/main.css":  "body { color: #000; }\n.x { background: url(../images/cover.png); }\n",
		"OEBPS/images/cover.png": string(converterTinyPNG),
	}
}

func zipStringEntries(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("write zip entry %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

func joinProblems(problems []epubLinkProblem) string {
	var b strings.Builder
	for _, p := range problems {
		b.WriteString("  ")
		b.WriteString(p.String())
		b.WriteByte('\n')
	}
	return b.String()
}
