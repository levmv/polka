package format

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
)

type containerDoc struct {
	Rootfiles []containerRootfile `xml:"rootfiles>rootfile"`
}

type containerRootfile struct {
	FullPath  string `xml:"full-path,attr"`
	MediaType string `xml:"media-type,attr"`
}

const (
	epubMimetype               = "application/epub+zip"
	epubOPFMediaType           = "application/oebps-package+xml"
	maxEPUBCoverBytes          = 32 << 20
	maxEPUBMimetypeBytes int64 = int64(len(epubMimetype) + 1024)
	maxEPUBFallbackOPFs        = 16
)

type epubOPFRead struct {
	path              string
	requestedPath     string
	containerPath     string
	relaxedMediaType  bool
	skippedCandidates int
	discoveredPackage bool
	doc               opfDoc
}

type epubOPFFile struct {
	path             string
	requestedPath    string
	relaxedMediaType bool
	file             *zip.File
}

type epubOPFParseError struct {
	err error
}

func (e epubOPFParseError) Error() string {
	return e.err.Error()
}

func (e epubOPFParseError) Unwrap() error {
	return e.err
}

// ExtractEPUBMetadata extracts metadata from an EPUB file.
func ExtractEPUBMetadata(r io.ReaderAt, size int64) (*Metadata, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, err
	}

	opf, ok, err := readEPUBOPF(zr)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return metadataFromOPF(opf.doc), nil
}

// ExtractEPUBMetadataAndCover extracts metadata and cover data from one parsed
// EPUB container. It is cheaper than calling ExtractEPUBMetadata and
// ExtractEPUBCover separately when both are needed.
func ExtractEPUBMetadataAndCover(r io.ReaderAt, size int64) (*Metadata, []byte, string, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, nil, "", err
	}

	opf, ok, err := readEPUBOPF(zr)
	if err != nil {
		return nil, nil, "", err
	}
	if !ok {
		return nil, nil, "", nil
	}

	coverBytes, coverExt, err := epubCoverFromOPF(zr, opf.path, opf.doc)
	if err != nil {
		return nil, nil, "", err
	}
	return metadataFromOPF(opf.doc), coverBytes, coverExt, nil
}

// ExtractEPUBCover extracts the cover image from an EPUB file.
// Returns the image bytes, a normalized extension (".jpg" or ".png"), or an error.
// If no cover is found, returns (nil, "", nil).
func ExtractEPUBCover(r io.ReaderAt, size int64) ([]byte, string, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, "", err
	}

	opf, ok, err := readEPUBOPF(zr)
	if err != nil {
		if _, ok := errors.AsType[epubOPFParseError](err); ok {
			return nil, "", nil
		}
		return nil, "", err
	}
	if !ok {
		return nil, "", nil
	}
	return epubCoverFromOPF(zr, opf.path, opf.doc)
}

func readEPUBOPF(zr *zip.Reader) (epubOPFRead, bool, error) {
	var firstErr error
	container, ambiguous := epubZipFileMatch(zr, "META-INF/container.xml")
	if ambiguous {
		return epubOPFRead{}, false, nil
	}
	if container != nil {
		rc, err := container.Open()
		if err != nil {
			firstErr = err
		} else {
			var cDoc containerDoc
			err = xml.NewDecoder(rc).Decode(&cDoc)
			closeErr := rc.Close()
			if err == nil {
				err = closeErr
			}
			if err == nil && len(cDoc.Rootfiles) > 0 {
				for i, candidate := range epubOPFFiles(zr, cDoc) {
					opf, err := readEPUBOPFFile(candidate.file)
					if err != nil {
						if firstErr == nil {
							firstErr = err
						}
						continue
					}
					return epubOPFRead{
						path:              candidate.path,
						requestedPath:     candidate.requestedPath,
						containerPath:     container.Name,
						relaxedMediaType:  candidate.relaxedMediaType,
						skippedCandidates: i,
						doc:               opf,
					}, true, nil
				}
			}
		}
	}

	if fallback, ok := readSoleCoherentEPUBOPF(zr); ok {
		return fallback, true, nil
	}
	return epubOPFRead{}, false, firstErr
}

// readSoleCoherentEPUBOPF is the bounded fallback for packages whose producer
// omitted container.xml or left it pointing at a missing rootfile. It accepts
// only one parseable OPF whose spine resolves through its manifest to an
// existing archive member; ambiguity remains a hard stop.
func readSoleCoherentEPUBOPF(zr *zip.Reader) (epubOPFRead, bool) {
	var found epubOPFRead
	candidateCount := 0
	for _, file := range zr.File {
		if strings.HasSuffix(file.Name, "/") || !strings.EqualFold(path.Ext(file.Name), ".opf") {
			continue
		}
		candidateCount++
		if candidateCount > maxEPUBFallbackOPFs {
			return epubOPFRead{}, false
		}
		opf, err := readEPUBOPFFile(file)
		if err != nil || opf.rootName.Local != "package" || !epubOPFSpineResolves(zr, file.Name, opf) {
			continue
		}
		if found.path != "" {
			return epubOPFRead{}, false
		}
		found = epubOPFRead{
			path:              file.Name,
			discoveredPackage: true,
			doc:               opf,
		}
	}
	return found, found.path != ""
}

func epubOPFSpineResolves(zr *zip.Reader, opfPath string, opf opfDoc) bool {
	if len(opf.Manifest.Items) == 0 || len(opf.Spine.Itemrefs) == 0 {
		return false
	}
	manifestPaths := make(map[string]string, len(opf.Manifest.Items))
	for _, item := range opf.Manifest.Items {
		id := strings.TrimSpace(item.ID)
		href := cleanOPFHref(item.Href)
		if id == "" || href == "" {
			continue
		}
		href = epubZipPath(opfPath, href)
		if existing, duplicate := manifestPaths[id]; duplicate && existing != href {
			manifestPaths[id] = ""
			continue
		}
		manifestPaths[id] = href
	}
	for _, itemref := range opf.Spine.Itemrefs {
		name := manifestPaths[strings.TrimSpace(itemref.IDRef)]
		if name == "" {
			continue
		}
		if file, ambiguous := epubZipFileMatch(zr, name); file != nil && !ambiguous {
			return true
		}
	}
	return false
}

func readEPUBOPFFile(opfFile *zip.File) (opfDoc, error) {
	rcOPF, err := opfFile.Open()
	if err != nil {
		return opfDoc{}, err
	}
	rawOPF, err := io.ReadAll(io.LimitReader(rcOPF, maxOPFDocumentBytes+1))
	closeErr := rcOPF.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return opfDoc{}, err
	}
	if len(rawOPF) > maxOPFDocumentBytes {
		return opfDoc{}, fmt.Errorf("%s exceeds %d bytes", opfFile.Name, maxOPFDocumentBytes)
	}
	var opf opfDoc
	err = decodeOPFBytes(rawOPF, &opf)
	if err != nil {
		return opfDoc{}, epubOPFParseError{
			err: fmt.Errorf("parse EPUB OPF %s: %w", opfFile.Name, err),
		}
	}
	return opf, nil
}

func epubCoverFromOPF(zr *zip.Reader, opfPath string, opf opfDoc) ([]byte, string, error) {
	for _, coverHref := range coverHrefCandidates(opf) {
		imgBytes, ext, err := readEPUBImageByHref(zr, opfPath, coverHref)
		if err != nil {
			return nil, "", err
		}
		if len(imgBytes) > 0 {
			return imgBytes, ext, nil
		}

		imgBytes, ext, err = readEPUBCoverPageImage(zr, opfPath, coverHref)
		if err != nil {
			return nil, "", err
		}
		if len(imgBytes) > 0 {
			return imgBytes, ext, nil
		}
	}

	return nil, "", nil
}

func epubOPFFiles(zr *zip.Reader, cDoc containerDoc) []epubOPFFile {
	// Some EPUBs keep stale or malformed rootfile entries before the real OPF.
	var candidates []epubOPFFile
	seen := make(map[string]bool)
	for _, requireOPFMediaType := range []bool{true, false} {
		for _, rootfile := range cDoc.Rootfiles {
			if requireOPFMediaType && !isEPUBOPFRootfile(rootfile) {
				continue
			}
			opfPath := cleanOPFHref(rootfile.FullPath)
			if opfPath == "" {
				continue
			}
			if seen[opfPath] {
				continue
			}
			if f := epubZipFile(zr, opfPath); f != nil {
				seen[opfPath] = true
				candidates = append(candidates, epubOPFFile{
					path:             f.Name,
					requestedPath:    opfPath,
					relaxedMediaType: !requireOPFMediaType && !isEPUBOPFRootfile(rootfile),
					file:             f,
				})
			}
		}
	}
	return candidates
}

func isEPUBOPFRootfile(rootfile containerRootfile) bool {
	mediaType := strings.ToLower(strings.TrimSpace(rootfile.MediaType))
	return mediaType == "" || mediaType == epubOPFMediaType
}

func coverHrefCandidates(opf opfDoc) []string {
	var candidates []string
	seen := make(map[string]bool)
	add := func(href string) {
		href = cleanOPFHref(href)
		if href == "" || seen[href] {
			return
		}
		seen[href] = true
		candidates = append(candidates, href)
	}

	// EPUBs in the wild do not agree on whether meta name="cover" points to a
	// manifest id or directly to an href, so try both before weaker guesses.
	var coverRef string
	for _, m := range opf.Metadata.Meta {
		if strings.EqualFold(strings.TrimSpace(m.Name), "cover") {
			coverRef = strings.TrimSpace(m.Content)
			break
		}
	}
	if coverRef != "" {
		for _, item := range opf.Manifest.Items {
			if item.ID == coverRef {
				add(item.Href)
				break
			}
		}
		for _, item := range opf.Manifest.Items {
			if cleanOPFHref(item.Href) == cleanOPFHref(coverRef) {
				add(item.Href)
				break
			}
		}
		add(coverRef)
	}

	// Some producer tools omit the standard cover metadata but still use
	// conventional manifest ids. Only accept these when the item is an image.
	for _, item := range opf.Manifest.Items {
		if hasOPFProperty(item.Properties, "cover-image") {
			add(item.Href)
		}
	}

	for _, item := range opf.Manifest.Items {
		id := strings.TrimSpace(item.ID)
		if (strings.EqualFold(id, "cover-image") || strings.EqualFold(id, "cover")) && isImageMediaType(item.MediaType) {
			add(item.Href)
		}
	}

	for _, ref := range opf.Guide.References {
		if isOPFGuideCoverType(ref.Type) {
			add(ref.Href)
		}
	}

	// For image-only EPUBs, the first spine item is often the intended cover.
	// Keep this image-only to avoid treating the first text chapter as a cover.
	for _, itemref := range opf.Spine.Itemrefs {
		if item := opfManifestItemByID(opf.Manifest.Items, itemref.IDRef); item != nil && isImageMediaType(item.MediaType) {
			add(item.Href)
			break
		}
	}

	for _, item := range opf.Manifest.Items {
		if isConventionalCoverHref(item.Href) {
			add(item.Href)
		}
	}

	return candidates
}

func isOPFGuideCoverType(typ string) bool {
	switch strings.ToLower(strings.TrimSpace(typ)) {
	case "cover", "other.ms-coverimage-standard", "other.ms-coverimage":
		return true
	}
	return false
}

func readEPUBImageByHref(zr *zip.Reader, baseFilePath, href string) ([]byte, string, error) {
	imgFile := epubZipFile(zr, epubZipPath(baseFilePath, href))
	if imgFile == nil {
		return nil, "", nil
	}

	imgBytes, err := readZipFileLimited(imgFile, maxEPUBCoverBytes)
	if err != nil {
		return nil, "", err
	}

	if ext, ok := coverImageExtensionFromBytes(imgBytes); ok {
		return imgBytes, ext, nil
	}
	return nil, "", nil
}

// Legacy guide cover references often point to a tiny XHTML wrapper rather than
// to the image itself. Since the guide already said "cover", reading the first
// image from that wrapper is a bounded fallback, not general page rendering.
func readEPUBCoverPageImage(zr *zip.Reader, opfPath, pageHref string) ([]byte, string, error) {
	pagePath := epubZipPath(opfPath, pageHref)
	pageFile := epubZipFile(zr, pagePath)
	if pageFile == nil {
		return nil, "", nil
	}

	rcPage, err := pageFile.Open()
	if err != nil {
		return nil, "", err
	}
	defer rcPage.Close()

	imageHref := firstImageHref(rcPage)
	if imageHref == "" {
		return nil, "", nil
	}
	return readEPUBImageByHref(zr, pagePath, imageHref)
}

func firstImageHref(r io.Reader) string {
	dec := xml.NewDecoder(r)
	dec.Strict = false
	dec.AutoClose = xml.HTMLAutoClose
	dec.Entity = xml.HTMLEntity
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "img":
			if src := attrValue(start.Attr, "src"); src != "" {
				return src
			}
		case "image":
			if href := attrValue(start.Attr, "href"); href != "" {
				return href
			}
		}
	}
}

func attrValue(attrs []xml.Attr, localName string) string {
	for _, attr := range attrs {
		if attr.Name.Local == localName {
			return strings.TrimSpace(attr.Value)
		}
	}
	return ""
}

func epubZipPath(baseFilePath, href string) string {
	return path.Join(path.Dir(baseFilePath), cleanOPFHref(href))
}

// OPF hrefs are URL-ish, while ZIP member names are plain paths. Normalize only
// the parts that affect lookup: separators, fragment/query suffixes, and escapes.
func cleanOPFHref(href string) string {
	href = strings.TrimSpace(strings.ReplaceAll(href, "\\", "/"))
	if idx := strings.IndexAny(href, "?#"); idx >= 0 {
		href = href[:idx]
	}
	if decoded, err := url.PathUnescape(href); err == nil {
		href = decoded
	}
	return href
}

func hasOPFProperty(properties, want string) bool {
	for property := range strings.FieldsSeq(properties) {
		if strings.EqualFold(property, want) {
			return true
		}
	}
	return false
}

func isImageMediaType(mediaType string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(strings.Split(mediaType, ";")[0])), "image/")
}

func opfManifestItemByID(items []opfItem, id string) *opfItem {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	for i := range items {
		if items[i].ID == id {
			return &items[i]
		}
	}
	return nil
}

func epubZipFile(zr *zip.Reader, name string) *zip.File {
	file, _ := epubZipFileMatch(zr, name)
	return file
}

// epubZipFileMatch prefers an exact package path. When there is no exact
// match, one normalized match is a compatibility fallback; several are
// ambiguous and deliberately produce no candidate.
func epubZipFileMatch(zr *zip.Reader, name string) (*zip.File, bool) {
	return ResolveZIPEntry(zr, name)
}
