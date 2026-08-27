package converter

import (
	"archive/zip"
	"bytes"
	"cmp"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/levmv/polka/internal/format"
)

const (
	rebuildOPFNamespace               = "http://www.idpf.org/2007/opf"
	rebuildDCNamespace                = "http://purl.org/dc/elements/1.1/"
	rebuildNCXNamespace               = "http://www.daisy.org/z3986/2005/ncx/"
	rebuildEPUBNamespace              = "http://www.idpf.org/2007/ops"
	rebuildSVGNamespace               = "http://www.w3.org/2000/svg"
	rebuildAdobePageTemplateMediaType = "application/vnd.adobe-page-template+xml"
	rebuildLegacyPageMapMediaType     = "application/oebps-page-map+xml"
	rebuildXHTML11Doctype             = `DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.1//EN" "http://www.w3.org/TR/xhtml11/DTD/xhtml11.dtd"`
	rebuildXHTML10StrictDoctype       = `DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.0 Strict//EN" "http://www.w3.org/TR/xhtml1/DTD/xhtml1-strict.dtd"`
)

type rebuildContentRepairPlan struct {
	xml11MetaValues           bool
	adeptResourceMetadata     bool
	missingStylesheets        map[string]bool
	missingPageTemplates      map[string]bool
	epub3                     bool
	normalizeEPUB2HTMLDoctype bool
	normalizeNavStrictDoctype bool
}

type rebuildXMLEdit struct {
	start int
	end   int
	value []byte
}

type rebuildXMLNode struct {
	Name        xml.Name
	Parent      xml.Name
	Attrs       []xml.Attr
	Start       int
	StartTagEnd int
	EndTagStart int
	End         int
	Text        []byte
	Simple      bool
}

// walkRebuildXML exposes exact byte spans from a strict XML token stream. The
// repair catalogue can then edit one attribute or element without serializing
// unrelated package/content markup.
func walkRebuildXML(raw []byte, visit func(rebuildXMLNode)) bool {
	var stack []rebuildXMLNode
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	for {
		start := int(decoder.InputOffset())
		token, err := decoder.Token()
		if err == io.EOF {
			return len(stack) == 0
		}
		if err != nil {
			return false
		}
		switch token := token.(type) {
		case xml.StartElement:
			parent := xml.Name{}
			if len(stack) > 0 {
				parent = stack[len(stack)-1].Name
				stack[len(stack)-1].Simple = false
			}
			stack = append(stack, rebuildXMLNode{
				Name:        token.Name,
				Parent:      parent,
				Attrs:       append([]xml.Attr(nil), token.Attr...),
				Start:       start,
				StartTagEnd: int(decoder.InputOffset()),
				Simple:      true,
			})
		case xml.EndElement:
			if len(stack) == 0 {
				return false
			}
			current := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			current.EndTagStart = start
			current.End = int(decoder.InputOffset())
			visit(current)
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].Text = append(stack[len(stack)-1].Text, token...)
			}
		case xml.Comment, xml.Directive, xml.ProcInst:
			if len(stack) > 0 {
				stack[len(stack)-1].Simple = false
			}
		}
	}
}

func rebuildXMLAttr(attrs []xml.Attr, name string) string {
	for _, attr := range attrs {
		if attr.Name.Space == "" && strings.EqualFold(attr.Name.Local, name) {
			return attr.Value
		}
	}
	return ""
}

func rebuildXMLHasAttr(attrs []xml.Attr, name string) bool {
	for _, attr := range attrs {
		if attr.Name.Space == "" && strings.EqualFold(attr.Name.Local, name) {
			return true
		}
	}
	return false
}

func rebuildXMLAttrNameSpan(raw []byte, node rebuildXMLNode, name string) (int, int, bool) {
	if node.Start < 0 || node.StartTagEnd > len(raw) || node.Start >= node.StartTagEnd {
		return 0, 0, false
	}
	for _, attr := range kepubXMLAttrsWithLocations(string(raw[node.Start:node.StartTagEnd])) {
		if strings.EqualFold(attr.Name, name) {
			return node.Start + attr.NameStart, node.Start + attr.NameEnd, true
		}
	}
	return 0, 0, false
}

func rebuildXMLAttrValueSpan(raw []byte, node rebuildXMLNode, name string) (int, int, bool) {
	if node.Start < 0 || node.StartTagEnd > len(raw) || node.Start >= node.StartTagEnd {
		return 0, 0, false
	}
	for _, attr := range kepubXMLAttrsWithLocations(string(raw[node.Start:node.StartTagEnd])) {
		if strings.EqualFold(attr.Name, name) {
			return node.Start + attr.ValueStart, node.Start + attr.ValueEnd, true
		}
	}
	return 0, 0, false
}

func rebuildXMLAttrSpan(raw []byte, node rebuildXMLNode, name string) (int, int, bool) {
	if node.Start < 0 || node.StartTagEnd > len(raw) || node.Start >= node.StartTagEnd {
		return 0, 0, false
	}
	tag := raw[node.Start:node.StartTagEnd]
	for _, match := range kepubXMLAttrRe.FindAllSubmatchIndex(tag, -1) {
		if !strings.EqualFold(string(tag[match[2]:match[3]]), name) {
			continue
		}
		start := match[0]
		for start > 0 && rebuildXMLSpace(tag[start-1]) {
			start--
		}
		return node.Start + start, node.Start + match[1], true
	}
	return 0, 0, false
}

func rebuildXMLSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

func applyRebuildXMLEdits(raw []byte, edits []rebuildXMLEdit) ([]byte, bool) {
	if len(edits) == 0 {
		return raw, false
	}
	slices.SortFunc(edits, func(a, b rebuildXMLEdit) int {
		if start := cmp.Compare(a.start, b.start); start != 0 {
			return start
		}
		return cmp.Compare(a.end, b.end)
	})
	previousEnd := 0
	for _, edit := range edits {
		if edit.start < previousEnd || edit.start < 0 || edit.end < edit.start || edit.end > len(raw) {
			return raw, false
		}
		previousEnd = edit.end
	}
	out := bytes.Clone(raw)
	for _, edit := range slices.Backward(edits) {
		next := make([]byte, 0, len(out)+len(edit.value)-(edit.end-edit.start))
		next = append(next, out[:edit.start]...)
		next = append(next, edit.value...)
		next = append(next, out[edit.end:]...)
		out = next
	}
	return out, true
}

// removeMissingPresentationReferences drops only declarations whose
// payload is already absent and whose media type cannot carry book content.
func removeMissingPresentationReferences(zr *zip.Reader, opfPath string, raw []byte) ([]byte, map[string]bool, error) {
	var doc rebuildOPFDoc
	if err := format.DecodeOPFXML(raw, &doc); err != nil {
		return nil, nil, fmt.Errorf("parse EPUB OPF %s for missing resources: %w", opfPath, err)
	}
	spineIDs := make(map[string]bool, len(doc.Spine.Items))
	for _, item := range doc.Spine.Items {
		spineIDs[strings.TrimSpace(item.IDRef)] = true
	}
	idCounts := make(map[string]int, len(doc.Manifest.Items))
	for _, item := range doc.Manifest.Items {
		idCounts[strings.TrimSpace(item.ID)]++
	}

	removeIDs := make(map[string]bool)
	stylesheetPaths := make(map[string]string)
	for _, item := range doc.Manifest.Items {
		id := strings.TrimSpace(item.ID)
		if id == "" || idCounts[id] != 1 || spineIDs[id] || !rebuildPresentationResourceMediaType(item.MediaType) {
			continue
		}
		itemPath := cleanKEPUBHref(opfPath, item.Href)
		if itemPath == "" {
			continue
		}
		file, err := kepubZipFile(zr, itemPath)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve EPUB presentation resource %s: %w", itemPath, err)
		}
		if file != nil {
			continue
		}
		removeIDs[id] = true
		if strings.EqualFold(strings.TrimSpace(item.MediaType), "text/css") {
			stylesheetPaths[id] = itemPath
		}
	}
	if len(removeIDs) == 0 {
		return raw, nil, nil
	}

	var edits []rebuildXMLEdit
	removedStylesheets := make(map[string]bool)
	if !walkRebuildXML(raw, func(node rebuildXMLNode) {
		if !node.Simple || len(bytes.TrimSpace(node.Text)) != 0 || node.Name.Space != doc.XMLName.Space || node.Name.Local != "item" || node.Parent.Space != doc.XMLName.Space || node.Parent.Local != "manifest" {
			return
		}
		id := strings.TrimSpace(rebuildXMLAttr(node.Attrs, "id"))
		if removeIDs[id] {
			edits = append(edits, rebuildXMLEdit{start: node.Start, end: node.End})
			if stylesheetPath := stylesheetPaths[id]; stylesheetPath != "" {
				removedStylesheets[stylesheetPath] = true
			}
		}
	}) {
		return raw, nil, nil
	}
	out, changed := applyRebuildXMLEdits(raw, edits)
	if !changed {
		return raw, nil, nil
	}
	return out, removedStylesheets, nil
}

func rebuildPresentationResourceMediaType(mediaType string) bool {
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	return mediaType == "text/css" ||
		strings.HasPrefix(mediaType, "font/") ||
		strings.HasPrefix(mediaType, "application/x-font-") ||
		mediaType == "application/font-sfnt" ||
		mediaType == "application/font-woff" ||
		mediaType == "application/vnd.ms-opentype"
}

// removeLegacyPageMapPointer removes only the obsolete EPUB2 spine
// pointer when the EPUB3 package also has a non-empty page-list navigation
// document. The legacy page-map resource remains untouched in the archive.
func removeLegacyPageMapPointer(ctx context.Context, zr *zip.Reader, opfPath string, raw []byte) ([]byte, error) {
	var doc rebuildOPFDoc
	if err := format.DecodeOPFXML(raw, &doc); err != nil {
		return raw, nil
	}
	if !rebuildOPFVersionAtLeast3(doc.Version) {
		return raw, nil
	}
	pageMapID := strings.TrimSpace(doc.Spine.PageMap)
	if pageMapID == "" {
		return raw, nil
	}

	pageMapPath := ""
	pageMapMatches := 0
	navPath := ""
	navMatches := 0
	for _, item := range doc.Manifest.Items {
		if strings.TrimSpace(item.ID) == pageMapID {
			pageMapMatches++
			if strings.EqualFold(strings.TrimSpace(item.MediaType), rebuildLegacyPageMapMediaType) {
				pageMapPath = cleanKEPUBHref(opfPath, item.Href)
			}
		}
		if containsKEPUBToken(item.Properties, "nav") && strings.EqualFold(strings.TrimSpace(item.MediaType), "application/xhtml+xml") {
			navMatches++
			navPath = cleanKEPUBHref(opfPath, item.Href)
		}
	}
	if pageMapMatches != 1 || pageMapPath == "" || navMatches != 1 || navPath == "" {
		return raw, nil
	}
	pageMapFile, err := kepubZipFile(zr, pageMapPath)
	if err != nil {
		return nil, fmt.Errorf("resolve EPUB legacy page-map %s: %w", pageMapPath, err)
	}
	navFile, err := kepubZipFile(zr, navPath)
	if err != nil {
		return nil, fmt.Errorf("resolve EPUB page-list navigation %s: %w", navPath, err)
	}
	if pageMapFile == nil || navFile == nil {
		return raw, nil
	}
	navRaw, err := kepubReadZipFile(ctx, navFile, maxConverterDecodedInputBytes)
	if err != nil {
		return nil, fmt.Errorf("read EPUB page-list navigation %s: %w", navPath, err)
	}
	if !rebuildXHTMLHasPageList(navRaw) {
		return raw, nil
	}

	var edits []rebuildXMLEdit
	if !walkRebuildXML(raw, func(node rebuildXMLNode) {
		if node.Name.Space != doc.XMLName.Space || node.Name.Local != "spine" || node.Parent != doc.XMLName || strings.TrimSpace(rebuildXMLAttr(node.Attrs, "page-map")) != pageMapID {
			return
		}
		if start, end, ok := rebuildXMLAttrSpan(raw, node, "page-map"); ok {
			edits = append(edits, rebuildXMLEdit{start: start, end: end})
		}
	}) || len(edits) != 1 {
		return raw, nil
	}
	out, _ := applyRebuildXMLEdits(raw, edits)
	return out, nil
}

func rebuildOPFVersionAtLeast3(version string) bool {
	major, _, _ := strings.Cut(strings.TrimSpace(version), ".")
	value, err := strconv.Atoi(major)
	return err == nil && value >= 3
}

func classifyRebuildContent(opfPath string, raw []byte) (map[string]bool, string, bool) {
	var doc rebuildOPFDoc
	if err := format.DecodeOPFXML(raw, &doc); err != nil {
		return nil, "", false
	}
	major, _, _ := strings.Cut(strings.TrimSpace(doc.Version), ".")
	if major == "2" {
		return nil, "", true
	}
	if !rebuildOPFVersionAtLeast3(doc.Version) {
		return nil, "", false
	}
	documents := make(map[string]bool)
	navPath := ""
	navMatches := 0
	for _, item := range doc.Manifest.Items {
		if !strings.EqualFold(strings.TrimSpace(item.MediaType), "application/xhtml+xml") {
			continue
		}
		candidate := cleanKEPUBHref(opfPath, item.Href)
		if candidate == "" {
			continue
		}
		documents[candidate] = true
		if !containsKEPUBToken(item.Properties, "nav") {
			continue
		}
		navPath = candidate
		navMatches++
	}
	if navMatches != 1 {
		navPath = ""
	}
	return documents, navPath, false
}

// addMissingSVGProperties declares only strictly parsed XHTML
// documents that actually contain an SVG namespace element. It changes the
// manifest start tag in place and leaves the content document untouched.
func addMissingSVGProperties(opfPath string, raw []byte, inlineSVGDocuments map[string]bool) []byte {
	if len(inlineSVGDocuments) == 0 {
		return raw
	}
	var doc rebuildOPFDoc
	if err := format.DecodeOPFXML(raw, &doc); err != nil || !rebuildOPFVersionAtLeast3(doc.Version) {
		return raw
	}

	var edits []rebuildXMLEdit
	if !walkRebuildXML(raw, func(node rebuildXMLNode) {
		if node.Name.Space != doc.XMLName.Space || node.Name.Local != "item" || node.Parent.Space != doc.XMLName.Space || node.Parent.Local != "manifest" {
			return
		}
		if !strings.EqualFold(strings.TrimSpace(rebuildXMLAttr(node.Attrs, "media-type")), "application/xhtml+xml") {
			return
		}
		itemPath := cleanKEPUBHref(opfPath, rebuildXMLAttr(node.Attrs, "href"))
		if itemPath == "" || !inlineSVGDocuments[itemPath] {
			return
		}
		startTag := string(raw[node.Start:node.StartTagEnd])
		next := kepubEnsureOPFProperty(startTag, "svg")
		if next != startTag {
			edits = append(edits, rebuildXMLEdit{start: node.Start, end: node.StartTagEnd, value: []byte(next)})
		}
	}) {
		return raw
	}
	out, _ := applyRebuildXMLEdits(raw, edits)
	return out
}

func rebuildXHTMLHasPageList(raw []byte) bool {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	pageListDepth := 0
	foundLink := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return foundLink && pageListDepth == 0
		}
		if err != nil {
			return false
		}
		switch token := token.(type) {
		case xml.StartElement:
			if pageListDepth > 0 {
				pageListDepth++
				foundLink = foundLink || token.Name.Space == kepubXHTMLNamespace && token.Name.Local == "a" && strings.TrimSpace(rebuildXMLAttr(token.Attr, "href")) != ""
				continue
			}
			if token.Name.Space != kepubXHTMLNamespace || token.Name.Local != "nav" {
				continue
			}
			for _, attr := range token.Attr {
				if attr.Name.Space == rebuildEPUBNamespace && attr.Name.Local == "type" && containsKEPUBToken(attr.Value, "page-list") {
					pageListDepth = 1
					break
				}
			}
		case xml.EndElement:
			if pageListDepth > 0 {
				pageListDepth--
			}
		}
	}
}

// normalizeVendorImageGuide converts one Microsoft cover hint to standard
// EPUB2 metadata and removes only vendor guide entries that point to images.
func normalizeVendorImageGuide(opfPath string, raw []byte) []byte {
	var doc rebuildOPFDoc
	if err := format.DecodeOPFXML(raw, &doc); err != nil {
		return raw
	}
	imageIDs := make(map[string]string)
	duplicatePaths := make(map[string]bool)
	for _, item := range doc.Manifest.Items {
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(item.MediaType)), "image/") {
			continue
		}
		itemPath := cleanKEPUBHref(opfPath, item.Href)
		id := strings.TrimSpace(item.ID)
		if itemPath == "" || id == "" {
			continue
		}
		if _, exists := imageIDs[itemPath]; exists {
			duplicatePaths[itemPath] = true
			delete(imageIDs, itemPath)
			continue
		}
		if !duplicatePaths[itemPath] {
			imageIDs[itemPath] = id
		}
	}

	coverTargets := make(map[string]bool)
	for _, ref := range doc.Guide.References {
		if rebuildVendorGuideKind(ref.Type) == "cover" {
			coverTargets[imageIDs[cleanKEPUBHref(opfPath, ref.Href)]] = true
		}
	}
	delete(coverTargets, "")
	coverID := ""
	if len(coverTargets) == 1 {
		for id := range coverTargets {
			coverID = id
		}
	}
	existingCoverID := ""
	coverConflict := false
	for _, meta := range doc.Metadata.Meta {
		if !strings.EqualFold(strings.TrimSpace(meta.Name), "cover") || strings.TrimSpace(meta.Content) == "" {
			continue
		}
		value := strings.TrimSpace(meta.Content)
		coverConflict = coverConflict || existingCoverID != "" && existingCoverID != value
		existingCoverID = value
	}
	canRepairCover := coverID != "" && !coverConflict && (existingCoverID == "" || existingCoverID == coverID)

	type guideRef struct {
		kind     string
		targetID string
		start    int
		end      int
	}
	metadataEnd := -1
	var refs []guideRef
	if !walkRebuildXML(raw, func(node rebuildXMLNode) {
		if node.Name.Space != doc.XMLName.Space {
			return
		}
		if node.Name.Local == "metadata" && node.Parent == doc.XMLName {
			metadataEnd = node.EndTagStart
			return
		}
		if !node.Simple || len(bytes.TrimSpace(node.Text)) != 0 || node.Name.Local != "reference" || node.Parent.Space != doc.XMLName.Space || node.Parent.Local != "guide" {
			return
		}
		kind := rebuildVendorGuideKind(rebuildXMLAttr(node.Attrs, "type"))
		targetID := imageIDs[cleanKEPUBHref(opfPath, rebuildXMLAttr(node.Attrs, "href"))]
		if kind != "" && targetID != "" {
			refs = append(refs, guideRef{kind: kind, targetID: targetID, start: node.Start, end: node.End})
		}
	}) {
		return raw
	}
	coverRefLocated := false
	for _, ref := range refs {
		coverRefLocated = coverRefLocated || ref.kind == "cover" && ref.targetID == coverID
	}
	canAddCover := canRepairCover && existingCoverID == "" && metadataEnd >= 0 && coverRefLocated
	willHaveCoverMeta := canRepairCover && (existingCoverID == coverID || canAddCover)
	var edits []rebuildXMLEdit
	for _, ref := range refs {
		if ref.kind == "thumbnail" || ref.kind == "cover" && willHaveCoverMeta && ref.targetID == coverID {
			edits = append(edits, rebuildXMLEdit{start: ref.start, end: ref.end})
		}
	}
	if canAddCover {
		var escaped bytes.Buffer
		if err := xml.EscapeText(&escaped, []byte(coverID)); err != nil {
			return raw
		}
		edits = append(edits, rebuildXMLEdit{
			start: metadataEnd,
			end:   metadataEnd,
			value: []byte(`<meta name="cover" content="` + escaped.String() + `"/>`),
		})
	}
	out, _ := applyRebuildXMLEdits(raw, edits)
	return out
}

func rebuildVendorGuideKind(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "other.ms-coverimage", "other.ms-coverimage-standard":
		return "cover"
	case "other.ms-thumbimage", "other.ms-thumbimage-standard":
		return "thumbnail"
	default:
		return ""
	}
}

// removeInvalidDateSentinels removes one exact producer sentinel while
// preserving valid dates and unknown date shapes byte-for-byte.
func removeInvalidDateSentinels(raw []byte) []byte {
	var edits []rebuildXMLEdit
	if !walkRebuildXML(raw, func(node rebuildXMLNode) {
		if node.Simple && node.Name.Space == rebuildDCNamespace && node.Name.Local == "date" && node.Parent.Space == rebuildOPFNamespace && node.Parent.Local == "metadata" && strings.EqualFold(strings.TrimSpace(string(node.Text)), "none") {
			edits = append(edits, rebuildXMLEdit{start: node.Start, end: node.End})
		}
	}) {
		return raw
	}
	out, _ := applyRebuildXMLEdits(raw, edits)
	return out
}

// removeEmptyTours removes one empty EPUB2 tours container. A non-empty,
// attributed, nested, or repeated tours shape is left byte-for-byte unchanged.
func removeEmptyTours(raw []byte) []byte {
	var edits []rebuildXMLEdit
	packageMatches := 0
	if !walkRebuildXML(raw, func(node rebuildXMLNode) {
		if node.Name.Space == rebuildOPFNamespace &&
			node.Name.Local == "package" &&
			node.Parent == (xml.Name{}) {
			major, _, _ := strings.Cut(strings.TrimSpace(rebuildXMLAttr(node.Attrs, "version")), ".")
			if major == "2" {
				packageMatches++
			}
		}
		if node.Simple &&
			node.Name.Space == rebuildOPFNamespace &&
			node.Name.Local == "tours" &&
			node.Parent.Space == rebuildOPFNamespace &&
			node.Parent.Local == "package" &&
			len(node.Attrs) == 0 &&
			len(bytes.TrimSpace(node.Text)) == 0 {
			edits = append(edits, rebuildXMLEdit{start: node.Start, end: node.End})
		}
	}) || packageMatches != 1 || len(edits) != 1 {
		return raw
	}
	out, _ := applyRebuildXMLEdits(raw, edits)
	return out
}

func rebuildXHTMLRepairs(ctx context.Context, zr *zip.Reader, pkg rebuildPackage, plan rebuildContentRepairPlan) (map[*zip.File][]byte, map[string]bool, error) {
	paths, err := kepubContentDocuments(pkg.opfPath, pkg.opfBytes)
	if err != nil {
		return nil, nil, err
	}
	epub3Documents, navDocument, epub2Package := classifyRebuildContent(pkg.opfPath, pkg.opfBytes)
	repairs := make(map[*zip.File][]byte)
	inlineSVGDocuments := make(map[string]bool)
	pageTemplatePresent := make(map[string]bool)
	for _, contentPath := range paths {
		file, err := kepubZipFile(zr, contentPath)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve EPUB content document %s: %w", contentPath, err)
		}
		if file == nil {
			continue
		}
		raw, err := kepubReadZipFile(ctx, file, maxConverterDecodedInputBytes)
		if err != nil {
			return nil, nil, fmt.Errorf("read EPUB content document %s: %w", contentPath, err)
		}
		var missingPageTemplates map[string]bool
		for target := range rebuildAdobePageTemplateLinkTargets(raw, contentPath) {
			present, checked := pageTemplatePresent[target]
			if !checked {
				targetFile, err := kepubZipFile(zr, target)
				if err != nil {
					return nil, nil, fmt.Errorf("resolve EPUB Adobe page template %s: %w", target, err)
				}
				present = targetFile != nil
				pageTemplatePresent[target] = present
			}
			if !present {
				if missingPageTemplates == nil {
					missingPageTemplates = make(map[string]bool)
				}
				missingPageTemplates[target] = true
			}
		}
		contentPlan := plan
		contentPlan.epub3 = epub3Documents[contentPath]
		// Avoid a second XML token pass over every spine document. The exact
		// parsed-node check below still owns the repair boundary; this is only a
		// cheap candidate filter for the known producer marker.
		contentPlan.adeptResourceMetadata = bytes.Contains(raw, []byte("Adept.resource"))
		contentPlan.missingPageTemplates = missingPageTemplates
		contentPlan.normalizeEPUB2HTMLDoctype = epub2Package && bytes.Contains(raw, []byte("<!DOCTYPE html>"))
		contentPlan.normalizeNavStrictDoctype = navDocument != "" && contentPath == navDocument
		if !contentPlan.epub3 && !contentPlan.xml11MetaValues && !contentPlan.adeptResourceMetadata && !contentPlan.normalizeEPUB2HTMLDoctype && len(contentPlan.missingStylesheets) == 0 && len(contentPlan.missingPageTemplates) == 0 {
			continue
		}
		repaired, changed, usesInlineSVG := applyXHTMLRepairs(raw, contentPath, contentPlan)
		if usesInlineSVG {
			inlineSVGDocuments[contentPath] = true
		}
		if changed {
			repairs[file] = repaired
		}
	}
	return repairs, inlineSVGDocuments, nil
}

func applyXHTMLRepairs(raw []byte, contentPath string, plan rebuildContentRepairPlan) ([]byte, bool, bool) {
	label := rebuildXMLDeclaredEncoding(raw)
	if !utf8.Valid(raw) || label != "" && !rebuildEncodingIsUTF8(label) {
		return raw, false, false
	}
	var edits []rebuildXMLEdit
	if plan.normalizeEPUB2HTMLDoctype {
		if edit, ok := rebuildEPUB2HTMLDoctypeEdit(raw); ok {
			edits = append(edits, edit)
		}
	}
	if plan.normalizeNavStrictDoctype {
		if edit, ok := rebuildEPUB3NavStrictDoctypeEdit(raw); ok {
			edits = append(edits, edit)
		}
	}
	usesInlineSVG := false
	xhtmlRoot := false
	if !walkRebuildXML(raw, func(node rebuildXMLNode) {
		if node.Name.Space == kepubXHTMLNamespace && node.Name.Local == "html" && node.Parent == (xml.Name{}) {
			xhtmlRoot = true
		}
		if node.Name.Space == rebuildSVGNamespace && node.Name.Local == "svg" {
			usesInlineSVG = true
		}
		if node.Name.Space != kepubXHTMLNamespace {
			return
		}
		if plan.epub3 && rebuildRedundantEPUB3BodyRole(node) {
			if start, end, ok := rebuildXMLAttrSpan(raw, node, "role"); ok {
				edits = append(edits, rebuildXMLEdit{start: start, end: end})
			}
		}
		inHead := node.Parent.Space == kepubXHTMLNamespace && node.Parent.Local == "head"
		metaName := strings.TrimSpace(rebuildXMLAttr(node.Attrs, "name"))
		repairMetaValue := (plan.xml11MetaValues && metaName != "") || (plan.adeptResourceMetadata && metaName == "Adept.resource")
		if inHead && node.Name.Local == "meta" && repairMetaValue && !rebuildXMLHasAttr(node.Attrs, "content") {
			if start, end, ok := rebuildXMLAttrNameSpan(raw, node, "value"); ok {
				edits = append(edits, rebuildXMLEdit{start: start, end: end, value: []byte("content")})
			}
		}
		missingStylesheet := node.Simple && len(bytes.TrimSpace(node.Text)) == 0 && inHead && node.Name.Local == "link" && rebuildStylesheetLinkTargets(node.Attrs, contentPath, plan.missingStylesheets)
		missingPageTemplate := plan.missingPageTemplates[rebuildAdobePageTemplateLinkTarget(node, contentPath)]
		if missingStylesheet || missingPageTemplate {
			edits = append(edits, rebuildXMLEdit{start: node.Start, end: node.End})
		}
	}) {
		return raw, false, false
	}
	out, changed := applyRebuildXMLEdits(raw, edits)
	return out, changed, xhtmlRoot && usesInlineSVG
}

// rebuildEPUB2HTMLDoctypeEdit replaces only the exact HTML doctype on a strict
// XHTML document in an EPUB2 package. The XHTML 1.1 declaration changes no
// content but restores the package's declared content model.
func rebuildEPUB2HTMLDoctypeEdit(raw []byte) (rebuildXMLEdit, bool) {
	return rebuildExactXHTMLDoctypeEdit(raw, "DOCTYPE html", "<!"+rebuildXHTML11Doctype+">")
}

// rebuildEPUB3NavStrictDoctypeEdit replaces only the exact obsolete XHTML 1.0
// Strict declaration observed on an otherwise coherent EPUB3 navigation
// document. Unknown declarations and internal subsets pass through unchanged.
func rebuildEPUB3NavStrictDoctypeEdit(raw []byte) (rebuildXMLEdit, bool) {
	return rebuildExactXHTMLDoctypeEdit(raw, rebuildXHTML10StrictDoctype, "<!DOCTYPE html>")
}

func rebuildExactXHTMLDoctypeEdit(raw []byte, from, to string) (rebuildXMLEdit, bool) {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	var candidate rebuildXMLEdit
	foundDoctype := false
	foundRoot := false
	depth := 0
	for {
		start := int(decoder.InputOffset())
		token, err := decoder.Token()
		if err == io.EOF {
			return candidate, foundDoctype && foundRoot && depth == 0
		}
		if err != nil {
			return rebuildXMLEdit{}, false
		}
		switch token := token.(type) {
		case xml.Directive:
			directive := strings.TrimSpace(string(token))
			if !strings.HasPrefix(strings.ToUpper(directive), "DOCTYPE") {
				continue
			}
			if foundDoctype || foundRoot || directive != from {
				return rebuildXMLEdit{}, false
			}
			foundDoctype = true
			candidate = rebuildXMLEdit{
				start: start,
				end:   int(decoder.InputOffset()),
				value: []byte(to),
			}
		case xml.StartElement:
			if depth == 0 {
				if foundRoot {
					return rebuildXMLEdit{}, false
				}
				foundRoot = true
				if token.Name.Space != kepubXHTMLNamespace || token.Name.Local != "html" {
					return rebuildXMLEdit{}, false
				}
			}
			depth++
		case xml.EndElement:
			depth--
		}
	}
}

// rebuildRedundantEPUB3BodyRole recognizes the three invalid body-role pairs
// in the corpus only when the equivalent EPUB structural semantic survives.
func rebuildRedundantEPUB3BodyRole(node rebuildXMLNode) bool {
	if node.Name.Space != kepubXHTMLNamespace || node.Name.Local != "body" || node.Parent.Space != kepubXHTMLNamespace || node.Parent.Local != "html" {
		return false
	}
	role := strings.TrimSpace(rebuildXMLAttr(node.Attrs, "role"))
	semantic := ""
	switch role {
	case "doc-cover":
		semantic = "cover"
	case "doc-dedication":
		semantic = "dedication"
	case "doc-glossary":
		semantic = "glossary"
	default:
		return false
	}
	for _, attr := range node.Attrs {
		if attr.Name.Space == rebuildEPUBNamespace && attr.Name.Local == "type" && containsKEPUBToken(attr.Value, semantic) {
			return true
		}
	}
	return false
}

func rebuildStylesheetLinkTargets(attrs []xml.Attr, contentPath string, missingStylesheets map[string]bool) bool {
	return rebuildLinkIsStylesheet(attrs) && missingStylesheets[cleanKEPUBHref(contentPath, rebuildXMLAttr(attrs, "href"))]
}

func rebuildAdobePageTemplateLinkTargets(raw []byte, contentPath string) map[string]bool {
	label := rebuildXMLDeclaredEncoding(raw)
	if !utf8.Valid(raw) || label != "" && !rebuildEncodingIsUTF8(label) {
		return nil
	}
	var targets map[string]bool
	if !walkRebuildXML(raw, func(node rebuildXMLNode) {
		if target := rebuildAdobePageTemplateLinkTarget(node, contentPath); target != "" {
			if targets == nil {
				targets = make(map[string]bool)
			}
			targets[target] = true
		}
	}) {
		return nil
	}
	return targets
}

func rebuildAdobePageTemplateLinkTarget(node rebuildXMLNode, contentPath string) string {
	if !node.Simple || len(bytes.TrimSpace(node.Text)) != 0 || node.Name.Space != kepubXHTMLNamespace || node.Name.Local != "link" || node.Parent.Space != kepubXHTMLNamespace || node.Parent.Local != "head" {
		return ""
	}
	if !rebuildLinkIsStylesheet(node.Attrs) || !strings.EqualFold(strings.TrimSpace(rebuildXMLAttr(node.Attrs, "type")), rebuildAdobePageTemplateMediaType) {
		return ""
	}
	return cleanKEPUBHref(contentPath, rebuildXMLAttr(node.Attrs, "href"))
}

func rebuildLinkIsStylesheet(attrs []xml.Attr) bool {
	stylesheet := false
	for token := range strings.FieldsSeq(rebuildXMLAttr(attrs, "rel")) {
		stylesheet = stylesheet || strings.EqualFold(token, "stylesheet")
	}
	return stylesheet
}

// rebuildNCXRepairs requires one selected NCX before applying the narrow NCX
// repair catalogue. UID synchronization additionally requires one package
// unique identifier.
func rebuildNCXRepairs(ctx context.Context, zr *zip.Reader, pkg rebuildPackage) (*zip.File, []byte, error) {
	var doc rebuildOPFDoc
	if err := format.DecodeOPFXML(pkg.opfBytes, &doc); err != nil {
		return nil, nil, fmt.Errorf("parse EPUB OPF %s for NCX repair: %w", pkg.opfPath, err)
	}
	identifierID := strings.TrimSpace(doc.UniqueIdentifier)
	identifier := ""
	identifierMatches := 0
	for _, candidate := range doc.Metadata.Identifiers {
		if strings.TrimSpace(candidate.ID) == identifierID && strings.TrimSpace(candidate.Value) != "" {
			identifier = strings.TrimSpace(candidate.Value)
			identifierMatches++
		}
	}
	if identifierID == "" || identifierMatches != 1 {
		identifier = ""
	}

	tocID := strings.TrimSpace(doc.Spine.TOC)
	ncxPath := ""
	ncxMatches := 0
	for _, item := range doc.Manifest.Items {
		if strings.TrimSpace(item.ID) == tocID && strings.EqualFold(strings.TrimSpace(item.MediaType), "application/x-dtbncx+xml") {
			ncxPath = cleanKEPUBHref(pkg.opfPath, item.Href)
			if ncxPath != "" {
				ncxMatches++
			}
		}
	}
	if tocID == "" || ncxMatches != 1 {
		return nil, nil, nil
	}
	ncxFile, err := kepubZipFile(zr, ncxPath)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve EPUB NCX %s: %w", ncxPath, err)
	}
	if ncxFile == nil {
		return nil, nil, nil
	}
	raw, err := kepubReadZipFile(ctx, ncxFile, maxConverterDecodedInputBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("read EPUB NCX %s: %w", ncxPath, err)
	}
	repaired, changed := escapeNCXNavLabelAmpersand(raw)
	if identifier != "" {
		var uidChanged bool
		repaired, uidChanged = syncNCXUID(repaired, identifier)
		changed = changed || uidChanged
	}
	if !changed {
		return nil, nil, nil
	}
	return ncxFile, repaired, nil
}

// escapeNCXNavLabelAmpersand escapes only one bare ampersand followed by
// XML whitespace in a simple NCX navLabel/text node. XML references cannot
// contain whitespace after '&', so the edit cannot reinterpret a valid entity.
func escapeNCXNavLabelAmpersand(raw []byte) ([]byte, bool) {
	label := rebuildXMLDeclaredEncoding(raw)
	if !utf8.Valid(raw) || label != "" && !rebuildEncodingIsUTF8(label) {
		return raw, false
	}
	if walkRebuildXML(raw, func(rebuildXMLNode) {}) {
		return raw, false
	}

	candidate := -1
	for i := 0; i+1 < len(raw); i++ {
		if raw[i] != '&' || !rebuildXMLSpace(raw[i+1]) {
			continue
		}
		if candidate >= 0 {
			return raw, false
		}
		candidate = i
	}
	if candidate < 0 {
		return raw, false
	}

	repaired, changed := applyRebuildXMLEdits(raw, []rebuildXMLEdit{{
		start: candidate,
		end:   candidate + 1,
		value: []byte("&amp;"),
	}})
	if !changed {
		return raw, false
	}

	rootCount := 0
	validRoot := false
	textMatches := 0
	if !walkRebuildXML(repaired, func(node rebuildXMLNode) {
		if node.Parent == (xml.Name{}) {
			rootCount++
			validRoot = node.Name.Space == rebuildNCXNamespace && node.Name.Local == "ncx"
		}
		if node.Simple &&
			node.Name.Space == rebuildNCXNamespace &&
			node.Name.Local == "text" &&
			node.Parent.Space == rebuildNCXNamespace &&
			node.Parent.Local == "navLabel" &&
			candidate >= node.StartTagEnd &&
			candidate < node.EndTagStart {
			textMatches++
		}
	}) || rootCount != 1 || !validRoot || textMatches != 1 {
		return raw, false
	}
	return repaired, true
}

func syncNCXUID(raw []byte, identifier string) ([]byte, bool) {
	label := rebuildXMLDeclaredEncoding(raw)
	if !utf8.Valid(raw) || label != "" && !rebuildEncodingIsUTF8(label) {
		return raw, false
	}
	identifier = strings.TrimSpace(identifier)
	var edits []rebuildXMLEdit
	matches := 0
	if !walkRebuildXML(raw, func(node rebuildXMLNode) {
		if node.Name.Space != rebuildNCXNamespace || node.Name.Local != "meta" || node.Parent.Space != rebuildNCXNamespace || node.Parent.Local != "head" || !strings.EqualFold(strings.TrimSpace(rebuildXMLAttr(node.Attrs, "name")), "dtb:uid") {
			return
		}
		matches++
		if strings.TrimSpace(rebuildXMLAttr(node.Attrs, "content")) == identifier {
			return
		}
		start, end, ok := rebuildXMLAttrValueSpan(raw, node, "content")
		if !ok {
			return
		}
		var escaped bytes.Buffer
		if xml.EscapeText(&escaped, []byte(identifier)) == nil {
			edits = append(edits, rebuildXMLEdit{start: start, end: end, value: escaped.Bytes()})
		}
	}) || matches != 1 || len(edits) != 1 {
		return raw, false
	}
	return applyRebuildXMLEdits(raw, edits)
}
