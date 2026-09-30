package converter

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"strings"

	"github.com/levmv/polka/internal/css"
	"github.com/levmv/polka/internal/xmlutil"
)

func rewriteSVGReference(element, href string, resolve func(string, string) (string, error)) (string, error) {
	if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "data:") {
		return href, nil
	}
	name, fragment, hasFragment := strings.Cut(href, "#")
	ref, err := resolve(element, name)
	if err == nil && ref != "" && hasFragment {
		ref += "#" + fragment
	}
	return ref, err
}

func rewriteSVGAttribute(element, name, value string, resolve func(string, string) (string, error)) (string, error) {
	switch name {
	case "href":
		if element != "a" {
			return rewriteSVGReference(element, value, resolve)
		}
	case "style", "fill", "stroke", "filter", "clip-path", "mask", "marker", "marker-start", "marker-mid", "marker-end", "cursor", "color-profile":
		return css.Parse(value).RewriteURLs(func(href string) (string, error) {
			return rewriteSVGReference("style", href, resolve)
		})
	}
	return value, nil
}

// Rewrite resource references without reserializing the artwork. Attribute
// names, namespaces, text, comments and untouched CSS retain their source bytes.
func rewriteSVGResources(ctx context.Context, raw []byte, resolve func(element, href string) (string, error)) ([]byte, error) {
	var edits []rebuildXMLEdit
	size := int64(len(raw))
	addEdit := func(edit rebuildXMLEdit) {
		edits = append(edits, edit)
		size += int64(len(edit.value) - (edit.end - edit.start))
	}
	attributes := func(start, end int, element string, attrs []xml.Attr) error {
		var spans []xmlutil.AttributeSpan
		for i, attr := range attrs {
			if attr.Name.Space != "" && !(attr.Name.Space == "http://www.w3.org/1999/xlink" && attr.Name.Local == "href") {
				continue
			}
			value, err := rewriteSVGAttribute(element, attr.Name.Local, attr.Value, resolve)
			if err != nil {
				return err
			}
			if value != attr.Value {
				if spans == nil {
					spans = xmlutil.AttributeSpans(string(raw[start:end]))
				}
				if i >= len(spans) {
					return fmt.Errorf("cannot locate SVG attribute %s", attr.Name.Local)
				}
				addEdit(rebuildXMLEdit{start + spans[i].ValueStart, start + spans[i].ValueEnd, []byte(html.EscapeString(value))})
			}
		}
		return nil
	}
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	decoder.Entity = xml.HTMLEntity
	var style strings.Builder
	var styleText []rebuildXMLEdit
	styleSimple := true
	styleDepth, depth, tokens := 0, 0, 0
	for {
		start := int(decoder.InputOffset())
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read SVG resources: %w", err)
		}
		tokens++
		if tokens%512 == 0 {
			if err := checkContext(ctx); err != nil {
				return nil, err
			}
		}
		if tokens > 200000 || depth > 128 {
			return nil, fmt.Errorf("SVG complexity exceeds limit: %w", ErrResourceLimit)
		}
		end := int(decoder.InputOffset())
		switch t := token.(type) {
		case xml.StartElement:
			depth++
			if styleDepth > 0 {
				styleSimple = false
			}
			if t.Name.Space == "http://www.w3.org/2000/svg" || t.Name.Space == "" {
				if t.Name.Local == "style" && styleDepth == 0 {
					styleDepth = depth
					styleSimple = true
					style.Reset()
					styleText = styleText[:0]
				}
				err = attributes(start, end, t.Name.Local, t.Attr)
			}
		case xml.EndElement:
			if depth == styleDepth {
				// CDATA and XML comments can split a CSS token. Parse the full
				// text once; unchanged styles retain all their original bytes.
				if styleSimple && len(styleText) > 0 {
					var value string
					value, err = rewriteSVGAttribute("style", "style", style.String(), resolve)
					if err == nil && value != style.String() {
						first := &styleText[0]
						if bytes.HasPrefix(raw[first.start:first.end], []byte("<![CDATA[")) {
							value = "<![CDATA[" + strings.ReplaceAll(value, "]]>", "]]]]><![CDATA[>") + "]]>"
						} else {
							value = html.EscapeString(value)
						}
						// Only replace character data, keeping XML comments and
						// other markup between the original text fragments.
						first.value = []byte(value)
						for _, edit := range styleText {
							addEdit(edit)
						}
					}
				}
				styleDepth = 0
			}
			depth--
		case xml.CharData:
			if depth == styleDepth && styleDepth > 0 {
				style.Write(t)
				styleText = append(styleText, rebuildXMLEdit{start: start, end: end})
			}
		case xml.ProcInst:
			if t.Target == "xml-stylesheet" {
				var tag struct {
					Attrs []xml.Attr `xml:",any,attr"`
				}
				err = xml.Unmarshal([]byte("<style "+string(t.Inst)+"/>"), &tag)
				if err == nil {
					err = attributes(start, end, t.Target, tag.Attrs)
				}
			}
		}
		if err != nil {
			return nil, err
		}
		if size > maxConverterResourceBytes {
			return nil, ErrInputTooLarge
		}
	}
	out, changed := applyRebuildXMLEdits(raw, edits)
	if len(edits) > 0 && !changed {
		return nil, fmt.Errorf("cannot apply SVG resource edits")
	}
	return out, nil
}
