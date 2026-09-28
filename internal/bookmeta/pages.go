package bookmeta

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strconv"
	"strings"
)

var calibrePageColumns = [3]string{"#pages", "#pagecount", "#page_count"}

func PositivePageCount(value string) int {
	n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32)
	if err != nil || n <= 0 {
		return 0
	}
	return int(n)
}

func IsCalibrePageColumn(key string) bool {
	for _, alias := range calibrePageColumns {
		if key == alias {
			return true
		}
	}
	return false
}

func calibreColumnPages(raw string) int {
	var column struct {
		Value jsontext.Value `json:"#value#"`
	}
	if json.Unmarshal([]byte(raw), &column) != nil {
		return 0
	}
	value := string(column.Value)
	if strings.HasPrefix(value, `"`) {
		if json.Unmarshal(column.Value, &value) != nil {
			return 0
		}
	}
	return PositivePageCount(value)
}

// Prefer schema:numberOfPages, then Calibre columns, then BookOrbit.
// These declarations remain estimates, including after our own writeback.
func OPFDeclaredPageCount(metas []OPFMeta) int {
	values := make(map[string]int)
	for _, meta := range metas {
		if strings.TrimSpace(meta.Refines) != "" {
			continue
		}
		key, value := OPFMetaName(meta.Name), meta.Content
		if meta.Property != "" {
			key, value = OPFMetaName(meta.Property), meta.Text
		}
		var count int
		switch {
		case key == "schema:numberofpages", key == "bookorbit:page_count":
			count = PositivePageCount(value)
		case strings.HasPrefix(key, "calibre:user_metadata:"):
			key = strings.TrimPrefix(key, "calibre:user_metadata:")
			if IsCalibrePageColumn(key) {
				count = calibreColumnPages(value)
			}
		case key == "calibre:user_metadata":
			var columns map[string]jsontext.Value
			if json.Unmarshal([]byte(value), &columns) == nil {
				for _, alias := range calibrePageColumns {
					if n := calibreColumnPages(string(columns[alias])); n > 0 && values[alias] == 0 {
						values[alias] = n
					}
				}
			}
		}
		if count > 0 && values[key] == 0 {
			values[key] = count
		}
	}
	if values["schema:numberofpages"] > 0 {
		return values["schema:numberofpages"]
	}
	for _, key := range calibrePageColumns {
		if values[key] > 0 {
			return values[key]
		}
	}
	return values["bookorbit:page_count"]
}

func IsCanonicalPageCountKey(key string) bool {
	return OPFMetaName(key) == "schema:numberofpages"
}

func PageCountKeyPriority(key string) int {
	key = OPFMetaName(key)
	if key == "schema:numberofpages" {
		return 1
	}
	for i, column := range calibrePageColumns {
		if key == "calibre:user_metadata:"+column {
			return i + 2
		}
	}
	if key == "bookorbit:page_count" {
		return len(calibrePageColumns) + 2
	}
	return 0
}
