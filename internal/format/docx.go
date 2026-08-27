package format

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"image"
	"io"
	"path"
	"strings"

	"github.com/levmv/polka/internal/bookmeta"
)

const (
	maxDOCXMetadataBytes     = 2 << 20
	maxDOCXDocumentScanBytes = 4 << 20
	maxDOCXCoverBytes        = 32 << 20
)

const (
	docxOfficeDocumentRelationship       = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument"
	docxStrictOfficeDocumentRelationship = "http://purl.oclc.org/ooxml/officeDocument/relationships/officeDocument"
	docxCorePropertiesRelationship       = "http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties"
	docxAppPropertiesRelationship        = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/extended-properties"
	docxStrictAppPropertiesRelationship  = "http://purl.oclc.org/ooxml/officeDocument/relationships/extended-properties"
	docxStylesRelationship               = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles"
	docxStrictStylesRelationship         = "http://purl.oclc.org/ooxml/officeDocument/relationships/styles"
	docxImageRelationship                = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/image"
	docxStrictImageRelationship          = "http://purl.oclc.org/ooxml/officeDocument/relationships/image"
	docxMarkupCompatibilityNamespace     = "http://schemas.openxmlformats.org/markup-compatibility/2006"
)

type docxRelationships struct {
	Relationships []docxRelationship `xml:"Relationship"`
}

type docxRelationship struct {
	ID         string `xml:"Id,attr"`
	Type       string `xml:"Type,attr"`
	Target     string `xml:"Target,attr"`
	TargetMode string `xml:"TargetMode,attr"`
}

type docxCoreProperties struct {
	Title       string   `xml:"title"`
	Creators    []string `xml:"creator"`
	Subject     string   `xml:"subject"`
	Description string   `xml:"description"`
	Keywords    string   `xml:"keywords"`
	Language    string   `xml:"language"`
	Created     string   `xml:"created"`
	Modified    string   `xml:"modified"`
}

type docxAppProperties struct {
	Company string `xml:"Company"`
}

func isDOCX(r io.ReaderAt, size int64) bool {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return false
	}
	return docxMainDocumentName(zr) != ""
}

// ExtractDOCXMetadata reads Office Open XML package metadata from DOCX/DOCM.
func ExtractDOCXMetadata(r io.ReaderAt, size int64) (*Metadata, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, err
	}
	meta := &Metadata{}
	rootRels, err := docxPackageRelationships(zr)
	if err != nil {
		return nil, err
	}
	corePropsName := docxRelatedPartName(zr, "", rootRels, docxIsCorePropertiesRelationship)
	if corePropsName == "" {
		corePropsName = "docProps/core.xml"
	}
	if coreProps := zipEntryByName(zr, corePropsName); coreProps != nil {
		raw, err := readZipFileLimited(coreProps, maxDOCXMetadataBytes)
		if err != nil {
			return nil, err
		}
		docxMergeCoreProperties(meta, raw)
	}
	appPropsName := docxRelatedPartName(zr, "", rootRels, docxIsAppPropertiesRelationship)
	if appPropsName == "" {
		appPropsName = "docProps/app.xml"
	}
	if appProps := zipEntryByName(zr, appPropsName); appProps != nil {
		raw, err := readZipFileLimited(appProps, maxDOCXMetadataBytes)
		if err != nil {
			return nil, err
		}
		docxMergeAppProperties(meta, raw)
	}
	if meta.Language == "" {
		stylesName := ""
		if documentName := docxMainDocumentNameFromRelationships(zr, rootRels); documentName != "" {
			documentRels, err := docxPartRelationships(zr, documentName)
			if err != nil {
				return nil, err
			}
			stylesName = docxRelatedPartName(zr, documentName, documentRels, docxIsStylesRelationship)
		}
		if stylesName == "" {
			stylesName = "word/styles.xml"
		}
		if styles := zipEntryByName(zr, stylesName); styles != nil {
			raw, err := readZipFileLimited(styles, maxDOCXMetadataBytes)
			if err != nil {
				return nil, err
			}
			meta.Language = docxDefaultLanguage(raw)
		}
	}
	return meta, nil
}

// ExtractDOCXCover returns the first embedded document image that looks like a
// book cover. This intentionally stays heuristic and bounded; full DOCX layout
// conversion belongs to the export/reader track.
func ExtractDOCXCover(r io.ReaderAt, size int64) ([]byte, string, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, "", err
	}
	documentName := docxMainDocumentName(zr)
	if documentName == "" {
		return nil, "", nil
	}
	rels, err := docxRelationshipMap(zr, documentName)
	if err != nil {
		return nil, "", err
	}
	if len(rels) == 0 {
		return nil, "", nil
	}
	document := zipEntryByName(zr, documentName)
	if document == nil {
		return nil, "", nil
	}
	ids, err := docxDocumentImageIDs(document)
	if err != nil {
		return nil, "", err
	}
	for _, id := range ids {
		target := rels[id]
		if target == "" {
			continue
		}
		entry := zipEntryByName(zr, target)
		if entry == nil {
			continue
		}
		raw, err := readZipFileLimited(entry, maxDOCXCoverBytes)
		if err != nil {
			return nil, "", err
		}
		ext, width, height, ok := docxCoverImageInfo(raw)
		if !ok || !docxLooksLikeCover(width, height) {
			continue
		}
		return raw, ext, nil
	}
	return nil, "", nil
}

func docxMainDocumentName(zr *zip.Reader) string {
	if zipEntryByName(zr, "[Content_Types].xml") == nil || zipEntryByName(zr, "_rels/.rels") == nil {
		return ""
	}
	rels, err := docxPackageRelationships(zr)
	if err != nil {
		return ""
	}
	return docxMainDocumentNameFromRelationships(zr, rels)
}

func docxMainDocumentNameFromRelationships(zr *zip.Reader, rels []docxRelationship) string {
	for _, rel := range rels {
		if !docxIsOfficeDocumentRelationship(rel.Type) || strings.EqualFold(rel.TargetMode, "External") {
			continue
		}
		target := cleanDOCXTarget(rel.Target)
		if docxIsMainDocumentTarget(target) && zipEntryByName(zr, target) != nil {
			return target
		}
	}
	return ""
}

func docxPackageRelationships(zr *zip.Reader) ([]docxRelationship, error) {
	return docxReadRelationships(zipEntryByName(zr, "_rels/.rels"))
}

func docxPartRelationships(zr *zip.Reader, sourceName string) ([]docxRelationship, error) {
	relsName := path.Join(path.Dir(sourceName), "_rels", path.Base(sourceName)+".rels")
	return docxReadRelationships(zipEntryByName(zr, relsName))
}

func docxReadRelationships(relsFile *zip.File) ([]docxRelationship, error) {
	if relsFile == nil {
		return nil, nil
	}
	raw, err := readZipFileLimited(relsFile, maxDOCXMetadataBytes)
	if err != nil {
		return nil, err
	}
	var parsed docxRelationships
	if err := xml.NewDecoder(bytes.NewReader(raw)).Decode(&parsed); err != nil {
		return nil, nil
	}
	return parsed.Relationships, nil
}

func docxRelatedPartName(zr *zip.Reader, sourceName string, rels []docxRelationship, matches func(string) bool) string {
	for _, rel := range rels {
		if !matches(rel.Type) || strings.EqualFold(strings.TrimSpace(rel.TargetMode), "External") {
			continue
		}
		target := cleanDOCXTarget(rel.Target)
		if sourceName != "" {
			target = cleanDOCXRelativeTarget(sourceName, rel.Target)
		}
		if target != "" && zipEntryByName(zr, target) != nil {
			return target
		}
	}
	return ""
}

func docxIsMainDocumentTarget(target string) bool {
	target = strings.ToLower(strings.TrimSpace(target))
	return strings.HasPrefix(target, "word/") && strings.HasSuffix(target, ".xml")
}

func docxRelationshipMap(zr *zip.Reader, sourceName string) (map[string]string, error) {
	parsed, err := docxPartRelationships(zr, sourceName)
	if err != nil {
		return nil, err
	}
	rels := make(map[string]string)
	for _, rel := range parsed {
		if rel.ID == "" || !docxIsImageRelationship(rel.Type) || strings.EqualFold(rel.TargetMode, "External") {
			continue
		}
		target := cleanDOCXRelativeTarget(sourceName, rel.Target)
		if target != "" {
			rels[rel.ID] = target
		}
	}
	return rels, nil
}

func docxIsOfficeDocumentRelationship(typ string) bool {
	return typ == docxOfficeDocumentRelationship || typ == docxStrictOfficeDocumentRelationship
}

func docxIsCorePropertiesRelationship(typ string) bool {
	return typ == docxCorePropertiesRelationship
}

func docxIsAppPropertiesRelationship(typ string) bool {
	return typ == docxAppPropertiesRelationship || typ == docxStrictAppPropertiesRelationship
}

func docxIsStylesRelationship(typ string) bool {
	return typ == docxStylesRelationship || typ == docxStrictStylesRelationship
}

func docxIsImageRelationship(typ string) bool {
	return typ == docxImageRelationship || typ == docxStrictImageRelationship
}

func cleanDOCXTarget(target string) string {
	target = strings.ReplaceAll(target, "\\", "/")
	target = strings.TrimPrefix(target, "/")
	target = path.Clean(target)
	if target == "." || strings.HasPrefix(target, "../") {
		return ""
	}
	return target
}

func cleanDOCXRelativeTarget(sourceName, target string) string {
	target = strings.TrimSpace(strings.ReplaceAll(target, "\\", "/"))
	if target == "" {
		return ""
	}
	if strings.HasPrefix(target, "/") {
		return cleanDOCXTarget(target)
	}
	return cleanDOCXTarget(path.Join(path.Dir(sourceName), target))
}

func docxDocumentImageIDs(f *zip.File) ([]string, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	decoder := xml.NewDecoder(io.LimitReader(rc, maxDOCXDocumentScanBytes))
	var ids []string
	seen := map[string]bool{}
	skippedChoiceDepth := 0
	for {
		token, err := decoder.Token()
		if err != nil {
			return ids, nil
		}
		switch value := token.(type) {
		case xml.StartElement:
			if skippedChoiceDepth > 0 {
				skippedChoiceDepth++
				continue
			}
			// Polka does not advertise support for producer-specific markup
			// extensions. Within AlternateContent, the compatibility fallback is
			// therefore the only honest cover candidate.
			if value.Name.Space == docxMarkupCompatibilityNamespace && value.Name.Local == "Choice" {
				skippedChoiceDepth = 1
				continue
			}
			var id string
			switch value.Name.Local {
			case "blip":
				id = attrValue(value.Attr, "embed")
			case "imagedata":
				id = attrValue(value.Attr, "id")
			}
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			ids = append(ids, id)
		case xml.EndElement:
			if skippedChoiceDepth > 0 {
				skippedChoiceDepth--
			}
		}
	}
}

func docxCoverImageInfo(raw []byte) (string, int, int, bool) {
	cfg, formatName, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return "", 0, 0, false
	}
	ext, ok := coverImageExtensionFromFormatName(formatName)
	if !ok {
		return "", 0, 0, false
	}
	return ext, cfg.Width, cfg.Height, true
}

func docxLooksLikeCover(width, height int) bool {
	if width <= 0 || height <= 0 {
		return false
	}
	ratio := float64(height) / float64(width)
	return ratio >= 0.8 && ratio <= 1.8 && width*height >= 160000
}

func docxMergeCoreProperties(meta *Metadata, raw []byte) {
	var props docxCoreProperties
	if err := xml.NewDecoder(bytes.NewReader(raw)).Decode(&props); err != nil {
		return
	}
	if title := strings.TrimSpace(props.Title); title != "" {
		meta.Title = title
	}
	for _, creator := range props.Creators {
		for _, author := range bookmeta.ParseAuthorList(creator) {
			if author != "" {
				meta.Authors = append(meta.Authors, bookmeta.AuthorMeta{Name: author})
			}
		}
	}
	if description := strings.TrimSpace(props.Description); description != "" {
		meta.Description = strings.ReplaceAll(description, "_x000d_", "")
	}
	if language := strings.TrimSpace(props.Language); language != "" {
		meta.Language = bookmeta.NormalizeLanguage(language)
	}
	if date := bookmeta.NormalizeMetadataDate(strings.TrimSpace(props.Created)); date != "" {
		meta.Date = date
	} else if date := bookmeta.NormalizeMetadataDate(strings.TrimSpace(props.Modified)); date != "" {
		meta.Date = date
	}
	meta.Tags = docxTags(props.Subject, props.Keywords)
}

func docxMergeAppProperties(meta *Metadata, raw []byte) {
	var props docxAppProperties
	if err := xml.NewDecoder(bytes.NewReader(raw)).Decode(&props); err != nil {
		return
	}
	if company := strings.TrimSpace(props.Company); company != "" {
		meta.Publisher = company
	}
}

func docxDefaultLanguage(raw []byte) string {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	for {
		token, err := decoder.Token()
		if err != nil {
			return ""
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "lang" {
			continue
		}
		for _, attr := range start.Attr {
			if attr.Name.Local == "val" {
				return bookmeta.NormalizeLanguage(attr.Value)
			}
		}
	}
}

func docxTags(values ...string) []string {
	return uniqueTagList(values, commaSemicolonNewlineTabSeparator, strings.TrimSpace)
}

func zipEntryByName(zr *zip.Reader, name string) *zip.File {
	want := strings.ToLower(name)
	for _, f := range zr.File {
		if strings.ToLower(NormalizeZipName(f.Name)) == want {
			return f
		}
	}
	return nil
}
