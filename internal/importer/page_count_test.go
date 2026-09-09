package importer

import (
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/storage"
	"github.com/levmv/polka/internal/testfixture"
)

func TestImportPageCountsBelongToAssets(t *testing.T) {
	for _, sidecarCover := range []bool{false, true} {
		name := "extract cover"
		if sidecarCover {
			name = "sidecar cover"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			database, err := db.InitPath(filepath.Join(dir, "library.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			root := storage.NewRoot(filepath.Join(dir, "books"))
			if err := storage.EnsureLayout(root); err != nil {
				t.Fatal(err)
			}
			sources := t.TempDir()
			if err := os.WriteFile(filepath.Join(sources, "metadata.opf"), []byte(`<metadata><title>Curated</title><creator>Author</creator><meta name="calibre:user_metadata:#pages" content='{"#value#":999}'/></metadata>`), 0o600); err != nil {
				t.Fatal(err)
			}
			cover := testCBZPNG(t, color.White)
			if sidecarCover {
				if err := os.WriteFile(filepath.Join(sources, "cover.png"), cover, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ep := filepath.Join(sources, "book.epub")
			writeEPUB(t, ep, []byte(`<package><metadata><title>Example</title><meta property="schema:numberOfPages">37</meta></metadata></package>`))
			fixed := filepath.Join(sources, "fixed.epub")
			writeEPUBWithBinaryFiles(t, fixed, []byte(`<package><metadata><title>Fixed</title><meta property="rendition:layout">pre-paginated</meta><meta property="schema:numberOfPages">37</meta></metadata><manifest><item id="p1" href="p1.xhtml"/><item id="p2" href="p2.xhtml"/></manifest><spine><itemref idref="p1"/><itemref idref="p2"/></spine></package>`), map[string][]byte{
				"OEBPS/p1.xhtml": []byte(`<html><body>First page</body></html>`),
				"OEBPS/p2.xhtml": []byte(`<html><body>Second page</body></html>`),
			})
			docx := filepath.Join(sources, "book.docx")
			writeTestDOCXWithParts(t, docx, `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Example</w:t></w:r></w:p></w:body></w:document>`, "", map[string][]byte{"docProps/app.xml": []byte(`<Properties><Pages>12</Pages></Properties>`)})
			cbz := filepath.Join(sources, "book.cbz")
			writeCBZ(t, cbz, map[string][]byte{"page.png": cover})
			cb7 := filepath.Join(sources, "book.cb7")
			if err := os.WriteFile(cb7, testfixture.CB7(), 0o600); err != nil {
				t.Fatal(err)
			}
			cbr := filepath.Join(sources, "book.cbr")
			if err := os.WriteFile(cbr, testfixture.CBR5(), 0o600); err != nil {
				t.Fatal(err)
			}
			inputs := []Source{{Path: ep}, {Path: fixed}, {Path: docx}, {Path: cbz}, {Path: cb7}, {Path: cbr}}
			// Exercise each EPUB as both the primary and an additional file.
			if sidecarCover {
				inputs[0], inputs[1] = inputs[1], inputs[0]
			}
			group, err := ImportGroup(t.Context(), database, root, inputs, nil, Options{})
			if err != nil {
				t.Fatal(err)
			}
			assets, err := db.AssetsByBookIDs(database.Read(t.Context()), []int64{group.BookID})
			if err != nil {
				t.Fatal(err)
			}
			if len(assets) != len(inputs) {
				t.Fatalf("imported %d assets; want %d", len(assets), len(inputs))
			}
			wants := map[string]int{"book.epub": 999, "fixed.epub": 2, "book.docx": 12, "book.cbz": 1, "book.cb7": 1, "book.cbr": 2}
			for _, asset := range assets {
				want := wants[asset.OriginalFilename]
				if asset.PageCount != want {
					t.Fatalf("%s count = %d; want %d", asset.OriginalFilename, asset.PageCount, want)
				}
			}
		})
	}
}

func TestImportFB2CountWithCuratedSidecar(t *testing.T) {
	for _, tc := range []struct {
		name        string
		embedded    string
		declaration string
		want        int
	}{
		{name: "eager estimate"},
		{name: "embedded declaration", embedded: `<custom-info info-type="schema:numberOfPages">37</custom-info>`, want: 37},
		{name: "sidecar declaration", embedded: `<custom-info info-type="schema:numberOfPages">37</custom-info>`, declaration: `<meta property="schema:numberOfPages">123</meta>`, want: 123},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			name := filepath.Join(dir, "book.fb2")
			raw := []byte(`<FictionBook><description><title-info><book-title>Embedded</book-title></title-info>` + tc.embedded + `</description><body>` + strings.Repeat(`<p>A short paragraph.</p>`, 300) + `</body></FictionBook>`)
			if err := os.WriteFile(name, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "metadata.opf"), []byte(`<metadata><title>Curated</title><creator>Author</creator>`+tc.declaration+`</metadata>`), 0o600); err != nil {
				t.Fatal(err)
			}
			plan, err := Resolve(t.Context(), Source{Path: name, SidecarDir: dir}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Title != "Curated" || plan.PageCount == 0 {
				t.Fatalf("curated metadata or count lost: %+v", plan)
			}
			if tc.want > 0 && plan.PageCount != tc.want {
				t.Fatalf("page count = %d; want declaration %d", plan.PageCount, tc.want)
			}
		})
	}
}
