package format

import (
	"archive/zip"
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"encoding/xml"
	"fmt"
	"strings"

	"github.com/levmv/polka/internal/bookmeta"
)

// NormalizeEPUBPageCountMetadata consolidates counts during EPUB repair or
// KEPUB conversion, which retain the book's reading content. Other source
// formats must not transfer their counts through this path.
func NormalizeEPUBPageCountMetadata(zr *zip.Reader, opfPath string, raw []byte) ([]byte, error) {
	metadataTag, err := opfFirstTag(raw, opfMetadataTagRe)
	if err != nil {
		return raw, nil
	}
	var doc opfDoc
	if err := bookmeta.DecodeOPFXML(raw, &doc); err != nil {
		return nil, err
	}
	pages := bookmeta.OPFDeclaredPageCount(doc.Metadata.Meta)
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
		if bookmeta.PageCountKeyPriority(child.attrs["name"]) > 0 || bookmeta.PageCountKeyPriority(child.attrs["property"]) > 0 {
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
	key, raw := bookmeta.OPFMetaName(child.attrs["property"]), ""
	if key == "" {
		key, raw = bookmeta.OPFMetaName(child.attrs["name"]), child.attrs["content"]
	}
	if key != "calibre:user_metadata" || child.attrs["refines"] != "" {
		return child.raw
	}
	if child.attrs["property"] != "" {
		var meta bookmeta.OPFMeta
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
	for key := range columns {
		if bookmeta.IsCalibrePageColumn(key) {
			changed = true
			delete(columns, key)
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
