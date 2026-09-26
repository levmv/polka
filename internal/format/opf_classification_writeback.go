package format

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"encoding/xml"
	"maps"
	"slices"
	"strings"
)

const calibreMetadataURI = "https://calibre-ebook.com"

// OPFTagMetadata writes the Calibre extra_tags column for a generated EPUB 3.
// The package element must declare the calibre prefix.
func OPFTagMetadata(tags []string) string {
	return renderCalibreMetadata(map[string]jsontext.Value{"#extra_tags": calibreExtraTags(tags)}, true)[0]
}

func calibreExtraTags(tags []string) jsontext.Value {
	column, _ := json.Marshal(map[string]any{
		"name": "Extra tags", "label": "extra_tags", "datatype": "text",
		"is_multiple": "|", "is_custom": true, "display": map[string]any{},
		"#value#": nonNilTagList(tags),
	}, json.Deterministic(true))
	return column
}

func renderCalibreMetadata(columns map[string]jsontext.Value, epub3 bool) []string {
	if epub3 {
		value, _ := json.Marshal(columns, json.Deterministic(true))
		return []string{`<meta property="calibre:user_metadata">` + opfEscapeText(string(value)) + `</meta>`}
	}
	var out []string
	for _, key := range slices.Sorted(maps.Keys(columns)) {
		out = append(out, `<meta name="calibre:user_metadata:`+opfEscapeAttr(key)+`" content="`+opfEscapeAttr(string(columns[key]))+`"/>`)
	}
	return out
}

// Consolidate the two Calibre encodings so readers that prefer one container
// still see unrelated custom columns. Genres move to dc:subject; extra_tags
// holds the complete tag list, including an explicit clear.
func rewriteOPFCalibreMetadata(inner []byte, tags []string, epub3 bool) ([]byte, []string, error) {
	children, err := opfMetadataChildren(inner, true)
	if err != nil {
		return nil, nil, err
	}
	columns := make(map[string]jsontext.Value)
	removed := make([]bool, len(children))
	for i, child := range children {
		if child.local != "meta" {
			continue
		}
		rawName, value := strings.TrimSpace(child.attrs["name"]), child.attrs["content"]
		if rawName == "" {
			var record opfMeta
			if xml.Unmarshal(child.raw, &record) != nil {
				continue
			}
			rawName, value = strings.TrimSpace(record.Property), record.Text
		}
		name := opfMetaName(rawName)
		switch {
		case name == "calibre:user_metadata":
			var existing map[string]jsontext.Value
			if json.Unmarshal([]byte(value), &existing) == nil && existing != nil {
				maps.Copy(columns, existing)
				removed[i] = true
			}
		case strings.HasPrefix(name, "calibre:user_metadata:#"):
			key := rawName[strings.IndexByte(rawName, '#'):]
			if strings.EqualFold(key, "#genre") || strings.EqualFold(key, "#extra_tags") {
				removed[i] = true
				continue
			}
			var column map[string]jsontext.Value
			if json.Unmarshal([]byte(value), &column) == nil && column != nil {
				columns[key] = jsontext.Value(value)
				removed[i] = true
			}
		}
	}
	delete(columns, "#genre")
	columns["#extra_tags"] = calibreExtraTags(tags)
	return removeOPFMetadataChildren(inner, children, removed), renderCalibreMetadata(columns, epub3), nil
}

func opfCalibrePackageTag(tag []byte, epub3 bool) ([]byte, bool) {
	if !epub3 {
		return tag, false
	}
	prefix := opfAttrs(string(tag))["prefix"]
	fields := strings.Fields(prefix)
	for i := 0; i+1 < len(fields); i += 2 {
		if fields[i] == "calibre:" {
			// A conflicting binding keeps its meaning; named OPF 2 records
			// are also valid in EPUB 3 and do not use vocabulary prefixes.
			return tag, fields[i+1] == calibreMetadataURI
		}
	}
	return opfSetTagAttr(tag, "prefix", strings.TrimSpace(prefix+" calibre: "+calibreMetadataURI)), true
}
