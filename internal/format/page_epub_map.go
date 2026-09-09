package format

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"slices"
	"strings"

	"golang.org/x/net/html"
)

const maxPageMapLocations = 65536

type epubPageMapEntry struct {
	file            *zip.File
	fragment, label string
}

type epubPageMap map[epubPageMapEntry]struct{}

type epubPageMaps struct {
	candidates []epubPageMap
	// Only requested anchors are tracked: 0 missing, 1 found, 2 ambiguous.
	// The empty fragment marks a document visited in the reading spine.
	anchors map[*zip.File]map[string]uint8
}

func readEPUBPageMaps(ctx context.Context, files *zipEntryIndex, opf epubOPFRead) epubPageMaps {
	maps := epubPageMaps{anchors: make(map[*zip.File]map[string]uint8)}
	attempts := 0
	for _, kind := range []string{"nav", "ncx", "map"} {
		for _, item := range opf.doc.Manifest.Items {
			match := kind == "nav" && hasOPFProperty(item.Properties, "nav") ||
				kind == "ncx" && item.ID == opf.doc.Spine.TOC ||
				kind == "map" && item.ID == opf.doc.Spine.PageMap
			if !match {
				continue
			}
			attempts++
			if attempts > 8 || ctx.Err() != nil {
				return maps
			}
			file, _ := pageZIPEntry(files, opf.path, item.Href)
			if file == nil {
				continue
			}
			raw, err := readZipFileLimited(file, maxOPFDocumentBytes)
			if err != nil {
				continue
			}
			pages := readEPUBPageMap(ctx, files, file.Name, raw, kind)
			if len(pages) == 0 {
				continue
			}
			maps.candidates = append(maps.candidates, pages)
			for page := range pages {
				if maps.anchors[page.file] == nil {
					maps.anchors[page.file] = make(map[string]uint8)
				}
				maps.anchors[page.file][page.fragment] = 0
			}
		}
	}
	return maps
}

func readEPUBPageMap(ctx context.Context, files *zipEntryIndex, base string, raw []byte, kind string) epubPageMap {
	pages := make(epubPageMap)
	add := func(label, href string) bool {
		file, fragment := pageZIPEntry(files, base, href)
		if file == nil {
			return false
		}
		// Count represented pages, not the highest numeric label. Distinct
		// labels may legitimately share an anchor; discard only repeated pairs.
		label = strings.Join(strings.Fields(label), " ")
		pages[epubPageMapEntry{file: file, fragment: fragment, label: label}] = struct{}{}
		return len(pages) <= maxPageMapLocations
	}
	if kind == "nav" {
		decoded, err := DecodeHTMLToUTF8(raw)
		if err != nil {
			return nil
		}
		z := html.NewTokenizer(bytes.NewReader(decoded))
		z.SetMaxBuf(maxOPFDocumentBytes)
		navDepth := 0
		var label strings.Builder
		href, inLink := "", false
		flush := func() bool {
			if !inLink {
				return true
			}
			inLink = false
			return add(label.String(), href)
		}
		for ctx.Err() == nil {
			tokenKind := z.Next()
			switch tokenKind {
			case html.ErrorToken:
				return nil
			case html.StartTagToken, html.SelfClosingTagToken:
				if tokenKind == html.SelfClosingTagToken {
					z.NextIsNotRawText()
				}
				t := z.Token()
				tag := htmlLocalName(t.Data)
				if tag == "nav" {
					if navDepth > 0 {
						navDepth++
					} else {
						for _, a := range t.Attr {
							if a.Key == "epub:type" && hasOPFProperty(a.Val, "page-list") || a.Key == "role" && hasOPFProperty(a.Val, "doc-pagelist") {
								navDepth = 1
							}
						}
					}
				}
				if navDepth > 0 && tag == "a" {
					if !flush() {
						return nil
					}
					label.Reset()
					href, inLink = "", true
					for _, a := range t.Attr {
						if a.Key == "href" {
							href = a.Val
						}
					}
					if tokenKind == html.SelfClosingTagToken && !flush() {
						return nil
					}
				}
			case html.TextToken:
				if inLink {
					label.Write(z.Text())
				}
			case html.EndTagToken:
				tag := htmlLocalName(z.Token().Data)
				if tag == "a" && !flush() {
					return nil
				}
				if tag == "nav" && navDepth > 0 {
					navDepth--
					if navDepth == 0 {
						if !flush() {
							return nil
						}
						return pages
					}
				}
			}
		}
		return nil
	}
	root, target := "pageList", "pageTarget"
	if kind == "map" {
		root, target = "page-map", "page"
	}
	dec := xml.NewDecoder(bytes.NewReader(raw))
	inPages := false
	for ctx.Err() == nil {
		token, err := dec.Token()
		if err != nil {
			return nil
		}
		switch t := token.(type) {
		case xml.StartElement:
			if t.Name.Local == root {
				inPages = true
			}
			if inPages && t.Name.Local == target {
				var page struct {
					Name    string `xml:"name,attr"`
					Href    string `xml:"href,attr"`
					Value   string `xml:"value,attr"`
					Label   string `xml:"navLabel>text"`
					Content struct {
						Src string `xml:"src,attr"`
					} `xml:"content"`
				}
				if err := dec.DecodeElement(&page, &t); err != nil {
					return nil
				}
				label, href := page.Name, page.Href
				if kind == "ncx" {
					label, href = page.Label, page.Content.Src
					if strings.TrimSpace(label) == "" {
						label = page.Value
					}
				}
				if !add(label, href) {
					return nil
				}
			}
		case xml.EndElement:
			if inPages && t.Name.Local == root {
				return pages
			}
		}
	}
	return nil
}

// anchorObserver checks requested anchors during content estimation, avoiding
// a separate pass. A repeated spine document needs no second anchor check.
func (m epubPageMaps) anchorObserver(file *zip.File) func(string, map[string]string) {
	anchors := m.anchors[file]
	if anchors == nil || anchors[""] != 0 {
		return nil
	}
	anchors[""] = 1
	return func(tag string, attrs map[string]string) {
		ids := []string{attrs["id"], attrs["xml:id"]}
		if tag == "a" {
			ids = append(ids, attrs["name"])
		}
		for i, id := range ids {
			if id == "" || slices.Contains(ids[:i], id) {
				continue
			}
			if count, wanted := anchors[id]; wanted {
				anchors[id] = min(count+1, 2)
			}
		}
	}
}

func (m epubPageMaps) chooseCount(estimate int) int {
	for _, candidate := range m.candidates {
		pages := len(candidate)
		// Maps can be incomplete or describe another edition. The factor-of-two
		// window checks plausibility; an accepted count remains an estimate.
		if pages*2 < estimate || pages > estimate*2 {
			continue
		}
		valid := true
		for page := range candidate {
			if m.anchors[page.file][page.fragment] != 1 {
				valid = false
				break
			}
		}
		if valid {
			return pages
		}
	}
	return estimate
}
