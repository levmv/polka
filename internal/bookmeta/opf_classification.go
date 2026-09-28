package bookmeta

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
)

// A recognized Calibre #genre column makes subjects a tag list. Otherwise
// subjects are genres, and extra_tags carries tags. Explicit empty lists
// distinguish cleared values from missing metadata when applying a sidecar.
func opfClassification(metadata OPFMetadata) (genres, tags []string) {
	subjects := NormalizeTags(metadata.Subject)
	var calibreGenres, extraTags []string
	var hasCalibreGenres, hasExtraTags bool
	for _, m := range metadata.Meta {
		name, value := OPFMetaName(m.Name), m.Content
		if name == "" {
			name, value = OPFMetaName(m.Property), m.Text
		}
		switch name {
		case "calibre:user_metadata:#genre", "calibre:user_metadata:#extra_tags":
			if values, ok := calibreColumnValues(jsontext.Value(value)); ok {
				if name == "calibre:user_metadata:#genre" {
					calibreGenres, hasCalibreGenres = values, true
				} else {
					extraTags = append(extraTags, values...)
					hasExtraTags = true
				}
			}
		case "calibre:user_metadata":
			var columns map[string]jsontext.Value
			if json.Unmarshal([]byte(value), &columns) == nil {
				if values, ok := calibreColumnValues(columns["#genre"]); ok {
					calibreGenres, hasCalibreGenres = values, true
				}
				if values, ok := calibreColumnValues(columns["#extra_tags"]); ok {
					extraTags = append(extraTags, values...)
					hasExtraTags = true
				}
			}
		}
	}
	if hasCalibreGenres {
		return nonNilTagList(calibreGenres), nonNilTagList(NormalizeTags(append(subjects, extraTags...)))
	}
	if hasExtraTags {
		return nonNilTagList(subjects), nonNilTagList(NormalizeTags(extraTags))
	}
	return subjects, nil
}

func calibreColumnValues(raw jsontext.Value) ([]string, bool) {
	var column map[string]jsontext.Value
	if json.Unmarshal(raw, &column) != nil {
		return nil, false
	}
	value, ok := column["#value#"]
	if !ok {
		return nil, false
	}
	return opfStringList(string(value))
}

func opfStringList(raw string) ([]string, bool) {
	var values []string
	if json.Unmarshal([]byte(raw), &values) == nil {
		return NormalizeTags(values), true
	}
	var value string
	if json.Unmarshal([]byte(raw), &value) == nil {
		return ParseTagList(value), true
	}
	return nil, false
}

func nonNilTagList(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
