package format

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
)

type pageMapTestLink struct {
	label, href string
}

func pageMapTestEPUB(t *testing.T, bodies []string, kind string, links []pageMapTestLink, metadata string) []byte {
	t.Helper()
	entries := map[string][]byte{
		"mimetype":               []byte(epubMimetype),
		"META-INF/container.xml": []byte(`<container><rootfiles><rootfile full-path="book.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`),
	}
	var manifest, spine, targets strings.Builder
	for i, body := range bodies {
		fmt.Fprintf(&manifest, `<item id="p%d" href="part%d.xhtml" media-type="application/xhtml+xml"/>`, i, i)
		fmt.Fprintf(&spine, `<itemref idref="p%d"/>`, i)
		entries[fmt.Sprintf("part%d.xhtml", i)] = []byte(`<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><head><title/></head><body>` + body + `</body></html>`)
	}
	name, mediaType, properties, spineAttrs := "pages.xhtml", "application/xhtml+xml", ` properties="nav"`, ""
	start, end := `<html><body><nav role="doc-pagelist"><ol>`, `</ol></nav></body></html>`
	switch kind {
	case "ncx":
		name, mediaType, properties, spineAttrs = "pages.ncx", "application/x-dtbncx+xml", "", ` toc="pages"`
		start, end = `<ncx><pageList>`, `</pageList></ncx>`
	case "map":
		name, mediaType, properties, spineAttrs = "pages.xml", "application/oebps-page-map+xml", "", ` page-map="pages"`
		start, end = `<page-map>`, `</page-map>`
	}
	for i, link := range links {
		label, href := opfEscapeText(link.label), opfEscapeText(link.href)
		switch kind {
		case "nav":
			fmt.Fprintf(&targets, `<li><a href="%s"><span>%s</span></a></li>`, href, label)
		case "ncx":
			fmt.Fprintf(&targets, `<pageTarget value="%d"><navLabel><text>%s</text></navLabel><content src="%s"/></pageTarget>`, i+1, label, href)
		case "map":
			fmt.Fprintf(&targets, `<page name="%s" href="%s"/>`, label, href)
		}
	}
	entries[name] = []byte(start + targets.String() + end)
	fmt.Fprintf(&manifest, `<item id="pages" href="%s" media-type="%s"%s/>`, name, mediaType, properties)
	entries["book.opf"] = []byte(`<package version="3.0"><metadata><title>Example</title>` + metadata + `</metadata><manifest>` + manifest.String() + `</manifest><spine` + spineAttrs + `>` + spine.String() + `</spine></package>`)
	return writeTestZip(t, entries)
}

func TestEPUBPageMapsValidateContent(t *testing.T) {
	short := []string{`<p id="p1">First</p><p id="p2">Second</p>`}
	links := []pageMapTestLink{{"iv", "part0.xhtml#p1"}, {"169", "part0.xhtml#p2"}, {"169", "./part0.xhtml#p%32"}}
	for _, tt := range []struct {
		kind, name string
		bodies     []string
		links      []pageMapTestLink
		pages      int // Zero means the map must be ignored.
	}{
		{"nav", "nav labels and duplicate links", short, links, 2},
		{"ncx", "NCX labels and duplicate links", short, links, 2},
		{"map", "page-map labels and duplicate links", short, links, 2},
		{"nav", "different labels at same anchor", short, []pageMapTestLink{{"169", "part0.xhtml#p1"}, {"170", "part0.xhtml#p1"}}, 2},
		{"nav", "same label at different anchors", short, []pageMapTestLink{{"1", "part0.xhtml#p1"}, {"1", "part0.xhtml#p2"}}, 2},
		{"nav", "missing document", short, []pageMapTestLink{{"1", "part0.xhtml#p1"}, {"2", "missing.xhtml#p2"}}, 0},
		{"nav", "target outside spine", short, []pageMapTestLink{{"1", "part0.xhtml#p1"}, {"2", "book.opf"}}, 0},
		{"nav", "missing anchor", short, []pageMapTestLink{{"1", "part0.xhtml#p1"}, {"2", "part0.xhtml#missing"}}, 0},
		{"nav", "ambiguous anchor", []string{`<p id="p1">First</p><p id="p1">Second</p>`}, []pageMapTestLink{{"1", "part0.xhtml#p1"}, {"2", "part0.xhtml"}}, 0},
		{"nav", "anchor aliases on one element", []string{`<a id="p1" name="p1"/><p>Text</p>`}, []pageMapTestLink{{"1", "part0.xhtml#p1"}, {"2", "part0.xhtml"}}, 2},
		{"nav", "hidden page markers", []string{`<span id="p1" role="doc-pagebreak" hidden/><p>First</p><span id="p2" epub:type="pagebreak" aria-hidden="true"/><p>Second</p>`}, []pageMapTestLink{{"1", "part0.xhtml#p1"}, {"2", "part0.xhtml#p2"}}, 2},
		{"nav", "legacy and escaped anchors", []string{`<a name="p1"/><p>First</p><p xml:id="p2">Second</p>`}, []pageMapTestLink{{"1", "part0.xhtml#p%31"}, {"2", "part0.xhtml#p2"}}, 2},
		{"nav", "fragmentless pages", []string{`<p>First</p>`, `<p>Second</p>`}, []pageMapTestLink{{"1", "part0.xhtml"}, {"2", "part1.xhtml"}}, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			baseline := pageTestEPUB(t, tt.bodies, "", "")
			estimate, err := CountPages(t.Context(), bytes.NewReader(baseline), int64(len(baseline)), FormatEPUB)
			if err != nil || estimate <= 0 {
				t.Fatalf("baseline = %d, %v", estimate, err)
			}
			want := tt.pages
			if want == 0 {
				want = estimate
			}
			raw := pageMapTestEPUB(t, tt.bodies, tt.kind, tt.links, "")
			got, err := CountPages(t.Context(), bytes.NewReader(raw), int64(len(raw)), FormatEPUB)
			if err != nil || got != want {
				t.Fatalf("count = %d, %v; want %d (content estimate %d)", got, err, want, estimate)
			}
		})
	}
}

func TestEPUBPageMapComparedWithContentEstimate(t *testing.T) {
	for _, tt := range []struct {
		name               string
		estimate, mapPages int
		want               int
	}{
		{"empty content", 0, 1, 0},
		{"single link into long book", 100, 1, 100},
		{"plausible map for illustrated book", 3, 4, 4},
		{"inflated map", 3, 20, 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Each illustration fills one reference page. A text-only comparison
			// would incorrectly reject every nonempty map in this table.
			body := `<span id="p1"/>` + strings.Repeat(`<img width="480" height="720"/>`, tt.estimate)
			var links []pageMapTestLink
			for i := 0; i < tt.mapPages; i++ {
				links = append(links, pageMapTestLink{fmt.Sprint(i + 1), "part0.xhtml#p1"})
			}
			raw := pageMapTestEPUB(t, []string{body}, "nav", links, "")
			got, err := CountPages(t.Context(), bytes.NewReader(raw), int64(len(raw)), FormatEPUB)
			if err != nil || got != tt.want {
				t.Fatalf("estimate %d, map %d: count = %d, %v; want %d", tt.estimate, tt.mapPages, got, err, tt.want)
			}
		})
	}
}

func TestEPUBPageMapFallsBackToAnotherMap(t *testing.T) {
	bodies := []string{`<p id="p1">First</p>` + strings.Repeat(`<p>Text</p>`, 100)} // Three reference pages.
	links := []pageMapTestLink{{"1", "part0.xhtml#p1"}, {"2", "part0.xhtml#p1"}, {"3", "part0.xhtml#p1"}, {"4", "part0.xhtml#p1"}}
	for _, tt := range []struct {
		name  string
		first []pageMapTestLink
		want  int
	}{
		{"invalid anchor", []pageMapTestLink{{"1", "part0.xhtml#missing"}, {"2", "part0.xhtml#p1"}}, 4},
		{"prefer valid NAV", links[:2], 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			nav := pageMapTestEPUB(t, bodies, "nav", tt.first, "")
			ncx := pageMapTestEPUB(t, bodies, "ncx", links[:4], "")
			entries := make(map[string][]byte)
			for _, file := range testZipReader(t, nav).File {
				entries[file.Name] = testZipEntryBytes(t, nav, file.Name)
			}
			entries["pages.ncx"] = testZipEntryBytes(t, ncx, "pages.ncx")
			opf := string(entries["book.opf"])
			opf = strings.Replace(opf, "</manifest>", `<item id="ncx" href="pages.ncx" media-type="application/x-dtbncx+xml"/></manifest>`, 1)
			opf = strings.Replace(opf, "<spine>", `<spine toc="ncx">`, 1)
			entries["book.opf"] = []byte(opf)
			raw := writeTestZip(t, entries)
			got, err := CountPages(t.Context(), bytes.NewReader(raw), int64(len(raw)), FormatEPUB)
			if err != nil || got != tt.want {
				t.Fatalf("count = %d, %v; want %d", got, err, tt.want)
			}
		})
	}
}

func TestEPUBPageMapParsingIsBounded(t *testing.T) {
	raw := pageMapTestEPUB(t, []string{`<p id="p1">Text</p>`}, "nav", []pageMapTestLink{{"1", "part0.xhtml#p1"}}, "")
	zr := testZipReader(t, raw)
	opf, ok, err := readEPUBOPF(zr)
	if err != nil || !ok {
		t.Fatalf("OPF: %v, %v", ok, err)
	}
	files := &zipEntryIndex{files: zr.File}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if maps := readEPUBPageMaps(ctx, files, opf); len(maps.candidates) != 0 {
		t.Fatal("cancelled parsing produced a map")
	}
	zipEntryByName(zr, "pages.xhtml").UncompressedSize64 = maxOPFDocumentBytes + 1
	if maps := readEPUBPageMaps(t.Context(), files, opf); len(maps.candidates) != 0 {
		t.Fatal("oversized document produced a map")
	}
}
