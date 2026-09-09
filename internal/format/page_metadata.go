package format

import (
	"archive/zip"
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
)

var calibrePageColumns = []string{"#pages", "#pagecount", "#page_count"}

func positivePageCount(value string) int {
	n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32)
	if err != nil || n <= 0 {
		return 0
	}
	return int(n)
}

func isCalibrePageColumn(key string) bool {
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
	return positivePageCount(value)
}

// Prefer schema:numberOfPages, then Calibre columns, then BookOrbit.
// These declarations remain estimates, including after our own writeback.
func opfDeclaredPageCount(metas []opfMeta) int {
	values := make(map[string]int)
	for _, meta := range metas {
		if strings.TrimSpace(meta.Refines) != "" {
			continue
		}
		key, value := opfMetaName(meta.Name), meta.Content
		if meta.Property != "" {
			key, value = opfMetaName(meta.Property), meta.Text
		}
		var count int
		switch {
		case key == "schema:numberofpages", key == "bookorbit:page_count":
			count = positivePageCount(value)
		case strings.HasPrefix(key, "calibre:user_metadata:"):
			key = strings.TrimPrefix(key, "calibre:user_metadata:")
			if isCalibrePageColumn(key) {
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

func isCanonicalPageCountKey(key string) bool {
	return opfMetaName(key) == "schema:numberofpages"
}

func pageCountKeyPriority(key string) int {
	key = opfMetaName(key)
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

// NormalizeEPUBPageCountMetadata consolidates counts during EPUB repair or
// KEPUB conversion, which retain the book's reading content. Other source
// formats must not transfer their counts through this path.
func NormalizeEPUBPageCountMetadata(zr *zip.Reader, opfPath string, raw []byte) ([]byte, error) {
	metadataTag, err := opfFirstTag(raw, opfMetadataTagRe)
	if err != nil {
		return raw, nil
	}
	var doc opfDoc
	if err := DecodeOPFXML(raw, &doc); err != nil {
		return nil, err
	}
	pages := opfDeclaredPageCount(doc.Metadata.Meta)
	if fixed := epubFixedPageCount(&zipEntryIndex{files: zr.File}, epubOPFRead{path: opfPath, doc: doc}); fixed > 0 {
		pages = fixed
	}
	epub3, err := opfDocumentVersionAtLeast3(raw)
	if err != nil {
		return nil, err
	}
	prefix := opfTagNamePrefix(metadataTag.raw)
	canonical := fmt.Sprintf(`<%smeta name="schema:numberOfPages" content="%d"/>`, prefix, pages)
	if epub3 {
		canonical = fmt.Sprintf(`<%smeta property="schema:numberOfPages">%d</%smeta>`, prefix, pages, prefix)
	}
	if opfParseTag(metadataTag.raw).selfClosing {
		if pages > 0 {
			return opfAppendMetadataChild(raw, canonical)
		}
		return raw, nil
	}
	endStart, _, err := opfFindMetadataEnd(raw, metadataTag.end)
	if err != nil {
		return nil, err
	}
	children, err := opfMetadataChildren(raw[metadataTag.end:endStart], true)
	if err != nil {
		return nil, err
	}
	written := false
	refinements := make(map[string][]int)
	removedIDs := make(map[string]bool)
	var removed []string
	removeID := func(id string) {
		if id != "" && !removedIDs[id] {
			removedIDs[id] = true
			removed = append(removed, id)
		}
	}
	for i := range children {
		child := &children[i]
		if target := strings.TrimPrefix(strings.TrimSpace(child.attrs["refines"]), "#"); target != "" {
			refinements[target] = append(refinements[target], i)
			continue
		}
		if child.local != "meta" {
			continue
		}
		if pageCountKeyPriority(child.attrs["name"]) > 0 || pageCountKeyPriority(child.attrs["property"]) > 0 {
			removeID(child.attrs["id"])
			child.raw = nil
			if pages > 0 && !written {
				child.raw = []byte(canonical)
				written = true
			}
		} else {
			child.raw = withoutCalibrePageColumns(*child)
			if len(child.raw) == 0 {
				removeID(child.attrs["id"])
			}
		}
	}
	// Refinements can refer to other refinements. Follow only removed records'
	// dependents, preserving unrelated IDs and avoiding dangling references.
	for i := 0; i < len(removed); i++ {
		for _, index := range refinements[removed[i]] {
			child := &children[index]
			if child.raw != nil {
				child.raw = nil
				removeID(child.attrs["id"])
			}
		}
	}
	// Patch child spans so comments, namespaces and unrelated metadata keep
	// their original bytes. Replacing the first scalar count is also idempotent.
	out := make([]byte, 0, len(raw))
	pos := 0
	for _, child := range children {
		start, end := metadataTag.end+child.start, metadataTag.end+child.end
		out = append(out, raw[pos:start]...)
		out = append(out, child.raw...)
		pos = end
	}
	out = append(out, raw[pos:]...)
	if pages > 0 && !written {
		return opfAppendMetadataChild(out, canonical)
	}
	return out, nil
}

// Preserve unrelated Calibre columns even when page-count aliases share the
// same JSON object. An unreadable object remains foreign metadata.
func withoutCalibrePageColumns(child opfMetadataChild) []byte {
	key, raw := opfMetaName(child.attrs["property"]), ""
	if key == "" {
		key, raw = opfMetaName(child.attrs["name"]), child.attrs["content"]
	}
	if key != "calibre:user_metadata" || child.attrs["refines"] != "" {
		return child.raw
	}
	if child.attrs["property"] != "" {
		var meta opfMeta
		if xml.Unmarshal(child.raw, &meta) != nil {
			return child.raw
		}
		raw = meta.Text
	}
	var columns map[string]jsontext.Value
	if json.Unmarshal([]byte(raw), &columns) != nil {
		return child.raw
	}
	changed := false
	for _, alias := range calibrePageColumns {
		if _, exists := columns[alias]; exists {
			changed = true
			delete(columns, alias)
		}
	}
	if !changed {
		return child.raw
	}
	if len(columns) == 0 {
		return nil
	}
	data, err := json.Marshal(columns, json.Deterministic(true))
	if err != nil {
		return child.raw
	}
	if child.attrs["property"] == "" {
		return opfSetTagAttr(child.raw, "content", string(data))
	}
	open, close := opfTagEnd(child.raw, 0), bytes.LastIndex(child.raw, []byte("</"))
	if open < 0 || close < open {
		return child.raw
	}
	out := append([]byte(nil), child.raw[:open]...)
	out = append(out, opfEscapeText(string(data))...)
	return append(out, child.raw[close:]...)
}
