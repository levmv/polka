package format

import (
	"bytes"
	"encoding/xml"
	"image"
	"strings"
)

// The XML decoder reuses this value for every body, including notes.
// Accumulate layout and image uses without retaining the body text.
type fb2PageBody struct {
	Extent     pageExtent
	ImageUses  map[string]int
	Incomplete bool
}

func (b *fb2PageBody) UnmarshalXML(dec *xml.Decoder, start xml.StartElement) error {
	if b.ImageUses == nil {
		b.ImageUses = make(map[string]int)
	}
	b.Extent.Break()
	depth := 1
	for depth > 0 {
		token, err := dec.Token()
		if err != nil {
			return err
		}
		switch t := token.(type) {
		case xml.StartElement:
			depth++
			if depth > 256 {
				b.Incomplete = true
				if err := dec.Skip(); err != nil {
					return err
				}
				depth--
				continue
			}
			switch t.Name.Local {
			case "p", "v", "subtitle", "title", "empty-line", "text-author", "tr":
				b.Extent.Break()
			case "td", "th":
				b.Extent.Text(" ")
			case "image":
				for _, attr := range t.Attr {
					if attr.Name.Local == "href" {
						id := fb2BinaryID(attr.Value)
						if id == "" {
							continue
						}
						if b.ImageUses[id] == 0 && len(b.ImageUses) >= 8192 {
							b.Incomplete = true
							continue
						}
						b.ImageUses[id]++
					}
				}
			}
		case xml.EndElement:
			depth--
			switch t.Name.Local {
			case "p", "v", "subtitle", "title", "empty-line", "text-author", "tr":
				b.Extent.Break()
			}
		case xml.CharData:
			b.Extent.Text(string(t))
		}
	}
	return nil
}

func fb2PageCount(doc *fb2Doc) int {
	declared, priority := 0, 0
	for _, info := range doc.Description.CustomInfo {
		if p := pageCountKeyPriority(info.Type); p > 0 && (priority == 0 || p < priority) {
			if count := positivePageCount(info.Text); count > 0 {
				declared, priority = count, p
			}
		}
	}
	if declared > 0 {
		return declared
	}
	if doc.Body.Incomplete {
		return 0
	}
	extent := doc.Body.Extent
	extent.Break()
	requested := doc.Body.ImageUses
	images := make(map[string]image.Config)
	for _, binary := range doc.Binaries {
		if requested[binary.ID] == 0 {
			continue
		}
		// Only the header is needed. Keep the same base64 tolerance as cover
		// extraction, with a bounded prefix and complete encoding quanta.
		prefix := strings.Join(strings.Fields(binary.Data[:min(len(binary.Data), 128<<10)]), "")
		prefix = prefix[:min(len(prefix)/4*4, 64<<10)]
		raw, err := DecodeLenientBase64(prefix)
		if err != nil {
			continue
		}
		config, _, err := image.DecodeConfig(bytes.NewReader(raw))
		if err == nil {
			images[binary.ID] = config
		}
	}
	// Image binaries follow the bodies. Accumulate their area after decoding,
	// without buffering the body or rounding each inline formula to a full line.
	inlineCells := 0
	for id, occurrences := range requested {
		config := images[id]
		var illustration pageExtent
		illustration.Image(config.Width, config.Height)
		inlineCells += occurrences * illustration.wordColumns
		extent.lines += occurrences * illustration.lines
	}
	extent.lines += (inlineCells + pageColumns - 1) / pageColumns
	return extent.Pages()
}
