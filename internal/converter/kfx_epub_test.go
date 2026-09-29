package converter

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"image/png"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/format"
	"github.com/levmv/polka/internal/testfixture"
)

func TestKFXFixedPages(t *testing.T) {
	source := testfixture.KFXFixed(t)
	if pages, err := format.CountPages(t.Context(), bytes.NewReader(source), int64(len(source)), format.FormatKFX); err != nil || pages != 4 {
		t.Fatalf("page count = %d, %v; want 4", pages, err)
	}
	for _, target := range []Target{TargetEPUB, TargetKEPUB} {
		t.Run(string(target), func(t *testing.T) {
			var out bytes.Buffer
			if err := ConvertContext(t.Context(), &out, bytes.NewReader(source), format.FormatKFX, int64(len(source)), target); err != nil {
				t.Fatal(err)
			}
			opf := zipEntry(t, out.Bytes(), "OEBPS/content.opf")
			var pkg struct {
				Spine struct {
					Direction string `xml:"page-progression-direction,attr"`
					Items     []struct {
						ID     string `xml:"idref,attr"`
						Spread string `xml:"properties,attr"`
					} `xml:"itemref"`
				} `xml:"spine"`
			}
			if err := xml.Unmarshal([]byte(opf), &pkg); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(opf, `property="rendition:layout">pre-paginated`) || pkg.Spine.Direction != "rtl" || len(pkg.Spine.Items) != 4 {
				t.Fatalf("lost fixed page layout: %s", opf)
			}
			for i, spread := range []string{"rendition:page-spread-center", "page-spread-right", "page-spread-left", "rendition:page-spread-center"} {
				item := pkg.Spine.Items[i]
				if item.ID != fmt.Sprintf("page-%d", i+1) || item.Spread != spread {
					t.Fatalf("page %d moved: %+v", i, item)
				}
				width := 120
				if i == 3 {
					width = 240
				}
				body := zipEntry(t, out.Bytes(), fmt.Sprintf("OEBPS/page-%d.xhtml", i+1))
				if !strings.Contains(body, fmt.Sprintf(`content="width=%d, height=160"`, width)) || !strings.Contains(body, fmt.Sprintf(`alt="Page %d"`, i+1)) {
					t.Fatalf("page %d lost its viewport or content: %s", i, body)
				}
				img, err := png.Decode(bytes.NewReader(zipEntryBytes(t, out.Bytes(), fmt.Sprintf("OEBPS/resources/resource-%d.png", i+1))))
				if err != nil {
					t.Fatal(err)
				}
				red, _, _, _ := img.At(100, 100).RGBA()
				if img.Bounds().Dx() != width || red>>8 != uint32(80+i*40) {
					t.Fatalf("page %d image changed or moved", i)
				}
			}
			if problems, err := checkEPUBInternalLinks(out.Bytes()); err != nil || len(problems) != 0 {
				t.Fatalf("page navigation: %v %v", problems, err)
			}
		})
	}
}

func TestKFXJapaneseText(t *testing.T) {
	source := testfixture.KFXJapanese(t)
	for _, target := range []Target{TargetEPUB, TargetKEPUB} {
		t.Run(string(target), func(t *testing.T) {
			var out bytes.Buffer
			if err := ConvertContext(t.Context(), &out, bytes.NewReader(source), format.FormatKFX, int64(len(source)), target); err != nil {
				t.Fatal(err)
			}
			body := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
			for _, fragment := range []string{"layout.css", "<ruby", "<rt>", "にほん", "text-combine-upright:all", "text-orientation:sideways", "text-emphasis-style:filled sesame", "text-emphasis-color:rgba(176,0,64,1.000)", "text-emphasis-position:under left"} {
				if !strings.Contains(body, fragment) {
					t.Errorf("output lacks %q", fragment)
				}
			}
			if !strings.Contains(zipEntry(t, out.Bytes(), "OEBPS/content.opf"), `page-progression-direction="rtl"`) ||
				zipEntry(t, out.Bytes(), "OEBPS/layout.css") != "html,body{writing-mode:vertical-rl;}" {
				t.Error("lost vertical page layout")
			}
			if problems, err := checkEPUBInternalLinks(out.Bytes()); err != nil || len(problems) != 0 {
				t.Fatalf("Japanese navigation: %v %v", problems, err)
			}
		})
	}
}

func TestKFXConversionPreservesContent(t *testing.T) {
	for _, bundle := range []bool{false, true} {
		name := "book.kfx"
		if bundle {
			name = "book.kfx-zip"
		}
		t.Run(name, func(t *testing.T) {
			source := testfixture.KFX(t, bundle)
			kind := format.DetectFormat(name, bytes.NewReader(source), int64(len(source)))
			if kind != format.FormatKFX {
				t.Fatalf("detected %v", kind)
			}
			meta, err := format.ExtractMetadata(bytes.NewReader(source), int64(len(source)), kind)
			if err != nil {
				t.Fatal(err)
			}
			if meta.Title != "Polka KFX Test" || len(meta.Authors) != 1 || meta.Authors[0].Name != "Ada Lovelace" {
				t.Fatalf("metadata: %+v", meta)
			}
			var out bytes.Buffer
			if err := ConvertContext(t.Context(), &out, bytes.NewReader(source), kind, int64(len(source)), TargetEPUB); err != nil {
				t.Fatal(err)
			}
			body := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
			for _, fragment := range []string{"First chapter", "Second chapter", "😀 ", "Bold", "&amp; text.", "نص عربي محفوظ", "Note body.", "<table", "Left cell", "Right cell", `dir="rtl"`, "font-weight:bold", "First line<br/>", "\u00a0\u00a0line"} {
				if !strings.Contains(body, fragment) {
					t.Errorf("output lacks %q", fragment)
				}
			}
			if strings.Index(body, "First chapter") >= strings.Index(body, "Second chapter") {
				t.Error("reading order changed")
			}
			if strings.Count(body, "<a ") != 5 {
				t.Error("lost footnote, image or table links")
			}
			nav := zipEntry(t, out.Bytes(), "OEBPS/nav.xhtml")
			if !strings.Contains(nav, `epub:type="page-list"`) || strings.Count(nav, `href="text.xhtml#`) != 6 {
				t.Error("lost chapter or page navigation")
			}
			if problems, err := checkEPUBInternalLinks(out.Bytes()); err != nil || len(problems) != 0 {
				t.Fatalf("EPUB references: %v %v", problems, err)
			}
			if !strings.Contains(zipEntry(t, out.Bytes(), "OEBPS/content.opf"), `properties="cover-image"`) {
				t.Error("cover missing")
			}
			fonts := zipEntry(t, out.Bytes(), "OEBPS/fonts.css")
			if strings.Count(fonts, "@font-face{") != 2 || strings.Count(fonts, "resources/resource-1.ttf") != 2 ||
				len(zipEntryBytes(t, out.Bytes(), "OEBPS/resources/resource-1.ttf")) == 0 {
				t.Errorf("shared embedded font lost: %s", fonts)
			}
		})
	}
}
