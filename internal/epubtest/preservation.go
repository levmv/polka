package epubtest

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/net/html/charset"
)

type contentMark struct {
	value      string
	start, end int
}

type documentContent struct {
	images, links, anchors, preformatted []contentMark
}

// CompareContent checks relationships that normalized text and resource hashes
// cannot establish. The documents must retain their resource paths, as in EPUB
// rebuilding and EPUB-to-KEPUB conversion. New anchors and wrapper elements are
// allowed; original anchors, link ranges, image occurrences and preformatted
// text must survive. This does not compare CSS or rendered appearance.
func CompareContent(source, output []byte) error {
	before, err := readDocumentContent(source, nil)
	if err != nil {
		return fmt.Errorf("parse source content: %w", err)
	}
	ids := make(map[string]bool, len(before.anchors))
	for _, anchor := range before.anchors {
		ids[anchor.value] = true
	}
	after, err := readDocumentContent(output, ids)
	if err != nil {
		return fmt.Errorf("parse output content: %w", err)
	}
	for _, field := range []struct {
		name          string
		before, after []contentMark
	}{
		{"image occurrences", before.images, after.images},
		{"links", before.links, after.links},
		{"anchors", before.anchors, after.anchors},
		{"preformatted text", before.preformatted, after.preformatted},
	} {
		if !slices.Equal(field.before, field.after) {
			for i := 0; i < min(len(field.before), len(field.after)); i++ {
				if field.before[i] != field.after[i] {
					return fmt.Errorf("%s differ at item %d", field.name, i+1)
				}
			}
			return fmt.Errorf("%s count differs: source=%d output=%d", field.name, len(field.before), len(field.after))
		}
	}
	return nil
}

func readDocumentContent(raw []byte, wantedIDs map[string]bool) (documentContent, error) {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	decoder.CharsetReader = charset.NewReaderLabel
	decoder.Entity = xml.HTMLEntity
	var result documentContent
	type frame struct {
		ignored     bool
		link, pre   int
		anchorStart int
		anchorEnd   int
	}
	var stack []frame
	var preText strings.Builder
	position := 0
	preDepth := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return result, nil
		}
		if err != nil {
			return documentContent{}, err
		}
		switch token := token.(type) {
		case xml.StartElement:
			f := frame{link: -1, pre: -1, anchorStart: len(result.anchors)}
			f.ignored = len(stack) > 0 && stack[len(stack)-1].ignored
			switch token.Name.Local {
			case "head", "title", "script", "style":
				f.ignored = true
			}
			if !f.ignored {
				for _, attr := range token.Attr {
					if (attr.Name.Local == "id" || token.Name.Local == "a" && attr.Name.Local == "name") &&
						(wantedIDs == nil || wantedIDs[attr.Value]) {
						result.anchors = append(result.anchors, contentMark{value: attr.Value, start: position})
					}
				}
				switch token.Name.Local {
				case "img", "image":
					ref := "src"
					if token.Name.Local == "image" {
						ref = "href"
					}
					result.images = append(result.images, contentMark{value: token.Name.Local + ":" + contentAttr(token, ref), start: position})
					position++
				case "a", "area", "content":
					ref := "href"
					if token.Name.Local == "content" {
						ref = "src" // NCX navigation.
					}
					if target := contentAttr(token, ref); target != "" {
						f.link = len(result.links)
						result.links = append(result.links, contentMark{value: target, start: position})
					}
				case "pre":
					if preDepth == 0 {
						f.pre = len(result.preformatted)
						result.preformatted = append(result.preformatted, contentMark{start: position})
						preText.Reset()
					}
					preDepth++
				case "br":
					if preDepth > 0 {
						preText.WriteByte('\n')
					}
				}
			}
			f.anchorEnd = len(result.anchors)
			stack = append(stack, f)
		case xml.EndElement:
			f := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if !f.ignored && token.Name.Local == "pre" {
				preDepth--
			}
			if f.pre >= 0 {
				result.preformatted[f.pre].value = preText.String()
				result.preformatted[f.pre].end = position
			}
			if f.link >= 0 {
				result.links[f.link].end = position
			}
			for i := f.anchorStart; i < f.anchorEnd; i++ {
				result.anchors[i].end = position
			}
		case xml.CharData:
			if len(stack) == 0 || stack[len(stack)-1].ignored {
				continue
			}
			if preDepth > 0 {
				preText.Write(token)
			}
			// Ignore layout whitespace when locating content. Text equality is
			// checked separately; image occurrences also occupy one position.
			for _, r := range string(token) {
				if !unicode.IsSpace(r) {
					position++
				}
			}
		}
	}
}

func contentAttr(element xml.StartElement, name string) string {
	for _, attr := range element.Attr {
		if attr.Name.Local == name {
			return strings.TrimSpace(attr.Value)
		}
	}
	return ""
}
