package bookmeta

import (
	"bytes"
	"encoding/xml"
	"slices"
	"strings"
)

const (
	xmpDCNamespace  = "http://purl.org/dc/elements/1.1/"
	xmpRDFNamespace = "http://www.w3.org/1999/02/22-rdf-syntax-ns#"
)

func ParseXMP(packet []byte) *Metadata {
	if len(packet) == 0 {
		return nil
	}

	decoder := xml.NewDecoder(bytes.NewReader(packet))
	var prop string
	propDepth := 0
	liDepth := 0
	var direct strings.Builder
	var li strings.Builder
	var titles []string
	var creators []string

	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}

		switch t := token.(type) {
		case xml.StartElement:
			if propDepth > 0 {
				propDepth++
				if liDepth > 0 {
					liDepth++
				} else if t.Name.Local == "li" && (t.Name.Space == xmpRDFNamespace || t.Name.Space == "") {
					liDepth = 1
					li.Reset()
				}
				continue
			}
			if t.Name.Space != xmpDCNamespace {
				continue
			}
			switch t.Name.Local {
			case "title", "creator":
				prop = t.Name.Local
				propDepth = 1
				direct.Reset()
			}
		case xml.CharData:
			if propDepth == 0 {
				continue
			}
			if liDepth > 0 {
				li.Write([]byte(t))
			} else {
				direct.Write([]byte(t))
			}
		case xml.EndElement:
			if propDepth == 0 {
				continue
			}
			if liDepth > 0 {
				liDepth--
				if liDepth == 0 {
					switch prop {
					case "title":
						titles = appendXMPText(titles, li.String())
					case "creator":
						creators = appendXMPText(creators, li.String())
					}
					li.Reset()
				}
			}
			propDepth--
			if propDepth == 0 {
				switch prop {
				case "title":
					titles = appendXMPText(titles, direct.String())
				case "creator":
					creators = appendXMPText(creators, direct.String())
				}
				prop = ""
			}
		}
	}

	meta := &Metadata{}
	if len(titles) > 0 {
		meta.Title = titles[0]
	}
	for _, creator := range creators {
		meta.Authors = append(meta.Authors, AuthorMeta{Name: creator})
	}
	if meta.Title == "" && len(meta.Authors) == 0 {
		return nil
	}
	return meta
}

func appendXMPText(values []string, text string) []string {
	text = DecodeLegacyHexWrappedText(strings.TrimSpace(text))
	if text == "" {
		return values
	}
	if slices.Contains(values, text) {
		return values
	}
	return append(values, text)
}
