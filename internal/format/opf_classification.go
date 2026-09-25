package format

import (
	"encoding/json/jsontext"
	"encoding/json/v2"

	"github.com/levmv/polka/internal/bookmeta"
)

// OPF has subjects but no standard genre/tag distinction. A polka tags record
// marks subjects as genres, including when either list has been explicitly
// cleared. Otherwise recognize the two Calibre custom columns before falling
// back to subjects as genres. Unknown custom metadata remains untouched.
func opfClassification(metadata opfMetadata) (genres, tags []string) {
	subjects := bookmeta.NormalizeTags(metadata.Subject)
	var calibreGenres, extraTags []string
	var hasCalibreGenres, hasExtraTags bool
	for _, m := range metadata.Meta {
		name, value := opfMetaName(m.Name), m.Content
		if name == "" {
			name, value = opfMetaName(m.Property), m.Text
		}
		switch name {
		case "polka:tags":
			if ownTags, ok := opfStringList(value); ok {
				return nonNilTagList(subjects), nonNilTagList(ownTags)
			}
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
		return nonNilTagList(calibreGenres), nonNilTagList(bookmeta.NormalizeTags(append(subjects, extraTags...)))
	}
	if hasExtraTags {
		return subjects, nonNilTagList(bookmeta.NormalizeTags(extraTags))
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
		return bookmeta.NormalizeTags(values), true
	}
	var value string
	if json.Unmarshal([]byte(raw), &value) == nil {
		return bookmeta.ParseTagList(value), true
	}
	return nil, false
}

func nonNilTagList(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// OPFTagMetadata preserves tags separately from dc:subject in both generated
// and rewritten EPUBs. An empty record also prevents stale Calibre columns from
// overriding explicit clears on a later import.
func OPFTagMetadata(tags []string) string {
	value, _ := json.Marshal(nonNilTagList(tags))
	return `<meta name="polka:tags" content="` + opfEscapeAttr(string(value)) + `"/>`
}
