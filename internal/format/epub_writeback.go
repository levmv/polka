package format

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"hash/crc32"
	"html"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/levmv/polka/internal/bookmeta"
)

var (
	opfPackageTagRe  = regexp.MustCompile(`(?is)<\s*(?:[A-Za-z_][A-Za-z0-9_.-]*:)?package\b[^>]*>`)
	opfMetadataTagRe = regexp.MustCompile(`(?is)<\s*(?:[A-Za-z_][A-Za-z0-9_.-]*:)?metadata\b[^>]*>`)
	opfManifestTagRe = regexp.MustCompile(`(?is)<\s*(?:[A-Za-z_][A-Za-z0-9_.-]*:)?manifest\b[^>]*>`)
	opfAttrRe        = regexp.MustCompile(`(?is)([A-Za-z_:][-A-Za-z0-9_:.]*)\s*=\s*("([^"]*)"|'([^']*)')`)
	opfMediaTypeRe   = regexp.MustCompile(`(?is)(\s(?:[A-Za-z_:][-A-Za-z0-9_:.]*:)?media-type\s*=\s*)("[^"]*"|'[^']*')`)
	opfStripTagsRe   = regexp.MustCompile(`(?is)<[^>]+>`)
)

// RewriteEPUBMetadata renders EPUB bytes with the supplied metadata written
// into the package OPF. It performs no filesystem or database work.
func RewriteEPUBMetadata(src []byte, meta Metadata, modified time.Time) ([]byte, error) {
	var out bytes.Buffer
	if err := RewriteEPUBMetadataTo(&out, bytes.NewReader(src), int64(len(src)), meta, modified); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// RewriteEPUBMetadataTo streams an EPUB with the supplied metadata written into
// the package OPF. Only the OPF member is held in memory; unchanged ZIP members
// are copied as raw compressed bytes.
func RewriteEPUBMetadataTo(w io.Writer, src io.ReaderAt, size int64, meta Metadata, modified time.Time) error {
	return RewriteEPUBMetadataAndCoverTo(w, src, size, meta, modified, nil)
}

// RewriteEPUBMetadataAndCoverTo streams an EPUB with the supplied metadata and,
// when coverBytes is non-empty, the supplied cover original written into the
// package OPF's cover slot. A missing Polka cover is represented by nil
// coverBytes and never strips an existing embedded EPUB cover.
func RewriteEPUBMetadataAndCoverTo(w io.Writer, src io.ReaderAt, size int64, meta Metadata, modified time.Time, coverBytes []byte) error {
	zr, err := zip.NewReader(src, size)
	if err != nil {
		return fmt.Errorf("open EPUB: %w", err)
	}
	if _, ambiguous := epubZipFileMatch(zr, "META-INF/container.xml"); ambiguous {
		return fmt.Errorf("rewrite EPUB: container.xml resolves to multiple entries")
	}

	opf, ok, err := readEPUBOPF(zr)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("EPUB OPF rootfile not found")
	}

	opfFile, ambiguous := epubZipFileMatch(zr, opf.path)
	if ambiguous {
		return fmt.Errorf("rewrite EPUB: OPF %s resolves to multiple entries", opf.path)
	}
	if opfFile == nil {
		return fmt.Errorf("EPUB OPF %s not found", opf.path)
	}
	rawOPF, err := readZipFileLimited(opfFile, maxOPFDocumentBytes)
	if err != nil {
		return fmt.Errorf("read EPUB OPF %s: %w", opf.path, err)
	}
	normalizedOPF, err := NormalizeOPFXML(rawOPF)
	if err != nil {
		return fmt.Errorf("normalize EPUB OPF %s: %w", opf.path, err)
	}
	nextOPF, err := rewriteOPFMetadata(normalizedOPF, meta, modified)
	if err != nil {
		return fmt.Errorf("rewrite EPUB OPF %s: %w", opf.path, err)
	}
	if err := ValidateEPUBRewriteSafety(zr, normalizedOPF, nextOPF); err != nil {
		return err
	}
	patches := map[string]epubZipPatch{
		opf.path: {data: nextOPF},
	}
	if len(coverBytes) > 0 {
		coverPatch, err := planEPUBCoverPatch(zr, opf.path, opf.doc, nextOPF, coverBytes)
		if err != nil {
			return fmt.Errorf("rewrite EPUB cover: %w", err)
		}
		patches[opf.path] = epubZipPatch{data: coverPatch.opfBytes}
		if coverPatch.coverPath != "" {
			patches[coverPatch.coverPath] = epubZipPatch{data: coverPatch.coverBytes, newEntry: coverPatch.newEntry}
		}
	}
	return repackEPUBWithPatches(w, zr, patches)
}

const (
	epubIDPFFontObfuscation  = "http://www.idpf.org/2008/embedding"
	epubAdobeFontObfuscation = "http://ns.adobe.com/pdf/enc#RC"
)

// ValidateEPUBRewriteSafety rejects package state that cannot survive an EPUB
// rewrite safely. Rebuilders may share this with metadata write-back as long as
// they preserve the key-bearing package identifier passed in rawOPF/nextOPF.
func ValidateEPUBRewriteSafety(zr *zip.Reader, rawOPF, nextOPF []byte) error {
	if epubZipFile(zr, "META-INF/signatures.xml") != nil {
		return fmt.Errorf("EPUB contains signatures.xml; refusing rewrite because package signatures would become stale")
	}

	encryptionFile := epubZipFile(zr, "META-INF/encryption.xml")
	if encryptionFile == nil {
		return nil
	}
	encryptionXML, err := readZipFileLimited(encryptionFile, maxOPFDocumentBytes)
	if err != nil {
		return fmt.Errorf("read EPUB encryption.xml: %w", err)
	}
	obfuscated, err := validateEPUBEncryptionXML(encryptionXML)
	if err != nil {
		return fmt.Errorf("unsafe EPUB encryption.xml: %w", err)
	}
	if !obfuscated {
		return nil
	}

	oldID, err := opfUniqueIdentifierValue(rawOPF)
	if err != nil {
		return fmt.Errorf("resolve obfuscated-font key before rewrite: %w", err)
	}
	newID, err := opfUniqueIdentifierValue(nextOPF)
	if err != nil {
		return fmt.Errorf("resolve obfuscated-font key after rewrite: %w", err)
	}
	if oldID != newID {
		return fmt.Errorf("EPUB metadata rewrite would change the key-bearing package unique identifier used by obfuscated fonts")
	}
	return nil
}

func validateEPUBEncryptionXML(raw []byte) (bool, error) {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	var encryptedDataMethods []bool
	foundObfuscation := false

	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return false, fmt.Errorf("parse: %w", err)
		}

		switch element := token.(type) {
		case xml.StartElement:
			switch element.Name.Local {
			case "EncryptedData":
				encryptedDataMethods = append(encryptedDataMethods, false)
			case "EncryptionMethod":
				if len(encryptedDataMethods) == 0 {
					return false, fmt.Errorf("encryption method outside EncryptedData")
				}
				algorithm := attrValue(element.Attr, "Algorithm")
				switch algorithm {
				case epubIDPFFontObfuscation, epubAdobeFontObfuscation:
				default:
					if algorithm == "" {
						return false, fmt.Errorf("EncryptedData has no encryption algorithm")
					}
					return false, fmt.Errorf("unsupported encryption algorithm %q", algorithm)
				}
				method := len(encryptedDataMethods) - 1
				if encryptedDataMethods[method] {
					return false, fmt.Errorf("EncryptedData has multiple encryption methods")
				}
				encryptedDataMethods[method] = true
				foundObfuscation = true
			}
		case xml.EndElement:
			if element.Name.Local != "EncryptedData" {
				continue
			}
			if len(encryptedDataMethods) == 0 {
				return false, fmt.Errorf("unexpected EncryptedData closing element")
			}
			method := len(encryptedDataMethods) - 1
			if !encryptedDataMethods[method] {
				return false, fmt.Errorf("EncryptedData has no encryption method")
			}
			encryptedDataMethods = encryptedDataMethods[:method]
		}
	}
	if len(encryptedDataMethods) != 0 {
		return false, fmt.Errorf("unclosed EncryptedData element")
	}
	return foundObfuscation, nil
}

func opfUniqueIdentifierValue(raw []byte) (string, error) {
	packageTag, err := opfFirstTag(raw, opfPackageTagRe)
	if err != nil {
		return "", err
	}
	uniqueID := strings.TrimSpace(opfAttrs(string(packageTag.raw))["unique-identifier"])
	if uniqueID == "" {
		return "", fmt.Errorf("OPF package has no unique-identifier reference")
	}

	metadataTag, err := opfFirstTag(raw, opfMetadataTagRe)
	if err != nil {
		return "", err
	}
	endStart, _, err := opfFindMetadataEnd(raw, metadataTag.end)
	if err != nil {
		return "", err
	}
	children, err := opfMetadataChildren(raw[metadataTag.end:endStart])
	if err != nil {
		return "", err
	}
	for _, child := range children {
		if child.local == "identifier" && strings.TrimSpace(child.attrs["id"]) == uniqueID {
			return child.text, nil
		}
	}
	return "", fmt.Errorf("OPF package unique-identifier target %q not found", uniqueID)
}

type epubZipPatch struct {
	data     []byte
	newEntry bool
}

func repackEPUBWithPatches(w io.Writer, zr *zip.Reader, patches map[string]epubZipPatch) error {
	zw := zip.NewWriter(w)
	if zr.Comment != "" {
		if err := zw.SetComment(zr.Comment); err != nil {
			zw.Close()
			return fmt.Errorf("copy EPUB zip comment: %w", err)
		}
	}

	mimetype, ambiguousMimetype := epubZipFileMatch(zr, "mimetype")
	if ambiguousMimetype {
		mimetype = nil
	}
	if mimetype != nil {
		if err := writeEPUBMimetype(zw, mimetype); err != nil {
			zw.Close()
			return err
		}
	}

	pending := make(map[string]epubZipPatch, len(patches))
	maps.Copy(pending, patches)

	for _, f := range zr.File {
		if f == mimetype {
			continue
		}
		if patch, ok := pending[f.Name]; ok {
			if err := writeZipEntryData(zw, f, patch.data); err != nil {
				zw.Close()
				return err
			}
			delete(pending, f.Name)
			continue
		}
		if err := copyZipEntryRaw(zw, f); err != nil {
			zw.Close()
			return err
		}
	}

	var newNames []string
	for name, patch := range pending {
		if !patch.newEntry {
			zw.Close()
			return fmt.Errorf("EPUB patch target %s not found", name)
		}
		newNames = append(newNames, name)
	}
	slices.Sort(newNames)
	for _, name := range newNames {
		if err := writeEPUBNewEntry(zw, name, pending[name].data); err != nil {
			zw.Close()
			return err
		}
	}

	if err := zw.Close(); err != nil {
		return fmt.Errorf("close rewritten EPUB: %w", err)
	}
	return nil
}

func writeEPUBNewEntry(zw *zip.Writer, name string, data []byte) error {
	header := &zip.FileHeader{Name: name, Method: zip.Deflate}
	w, err := zw.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("create EPUB entry %s: %w", name, err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write EPUB entry %s: %w", name, err)
	}
	return nil
}

type epubCoverPatch struct {
	opfBytes   []byte
	coverPath  string
	coverBytes []byte
	newEntry   bool
}

type epubCoverImageTarget struct {
	zipPath   string
	bytes     []byte
	itemID    string
	mediaType string
}

func planEPUBCoverPatch(zr *zip.Reader, opfPath string, opf opfDoc, opfBytes []byte, coverBytes []byte) (epubCoverPatch, error) {
	if len(coverBytes) > maxEPUBCoverBytes {
		return epubCoverPatch{}, fmt.Errorf("cover exceeds %d bytes", maxEPUBCoverBytes)
	}
	mediaType, ext, ok := coverImageMediaTypeFromBytes(coverBytes)
	if !ok {
		return epubCoverPatch{}, fmt.Errorf("unsupported cover image type")
	}

	target, ok, err := epubCoverImageTargetFromOPF(zr, opfPath, opf)
	if err != nil {
		return epubCoverPatch{}, err
	}
	if ok {
		targetMediaType, _, targetTypeKnown := coverImageMediaTypeFromBytes(target.bytes)
		if mediaTypeFromPath, pathTypeKnown := EPUBImageMediaTypeForExtension(path.Ext(target.zipPath)); pathTypeKnown {
			targetMediaType = mediaTypeFromPath
			targetTypeKnown = true
		}
		if targetTypeKnown && (!sameMediaType(mediaType, targetMediaType) || targetMediaType == "image/jpeg" && isProgressiveJPEG(coverBytes)) {
			coverBytes, err = transcodeEPUBCover(coverBytes, targetMediaType)
			if err != nil {
				return epubCoverPatch{}, fmt.Errorf("encode replacement as %s for existing cover path %s: %w", targetMediaType, target.zipPath, err)
			}
			mediaType = targetMediaType
		}
		if len(coverBytes) > maxEPUBCoverBytes {
			return epubCoverPatch{}, fmt.Errorf("encoded cover exceeds %d bytes", maxEPUBCoverBytes)
		}
		nextOPF := opfBytes
		if target.itemID != "" && !sameMediaType(target.mediaType, mediaType) {
			nextOPF, err = opfSetManifestItemMediaType(nextOPF, target.itemID, mediaType)
			if err != nil {
				return epubCoverPatch{}, err
			}
		}
		if bytes.Equal(target.bytes, coverBytes) {
			return epubCoverPatch{opfBytes: nextOPF}, nil
		}
		return epubCoverPatch{opfBytes: nextOPF, coverPath: target.zipPath, coverBytes: coverBytes}, nil
	}

	if mediaType == "image/jpeg" && isProgressiveJPEG(coverBytes) {
		coverBytes, err = transcodeEPUBCover(coverBytes, mediaType)
		if err != nil {
			return epubCoverPatch{}, fmt.Errorf("encode progressive JPEG cover as baseline: %w", err)
		}
		if len(coverBytes) > maxEPUBCoverBytes {
			return epubCoverPatch{}, fmt.Errorf("encoded cover exceeds %d bytes", maxEPUBCoverBytes)
		}
	}

	coverID := uniqueOPFCoverID(opf)
	coverHref := uniqueEPUBCoverHref(zr, opfPath, ext)
	nextOPF, err := opfAppendMetadataChild(opfBytes, fmt.Sprintf(`<meta name="cover" content="%s"/>`, opfEscapeAttr(coverID)))
	if err != nil {
		return epubCoverPatch{}, err
	}
	epub3, err := opfDocumentVersionAtLeast3(nextOPF)
	if err != nil {
		return epubCoverPatch{}, err
	}
	nextOPF, err = opfInsertManifestItem(nextOPF, coverID, coverHref, mediaType, epub3)
	if err != nil {
		return epubCoverPatch{}, err
	}
	return epubCoverPatch{
		opfBytes:   nextOPF,
		coverPath:  epubZipPath(opfPath, coverHref),
		coverBytes: coverBytes,
		newEntry:   true,
	}, nil
}

func transcodeEPUBCover(data []byte, mediaType string) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}

	var out bytes.Buffer
	switch mediaType {
	case "image/jpeg":
		bounds := src.Bounds()
		flat := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
		draw.Draw(flat, flat.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
		draw.Draw(flat, flat.Bounds(), src, bounds.Min, draw.Over)
		if err := jpeg.Encode(&out, flat, &jpeg.Options{Quality: 90}); err != nil {
			return nil, err
		}
	case "image/png":
		if err := png.Encode(&out, src); err != nil {
			return nil, err
		}
	case "image/gif":
		if err := gif.Encode(&out, src, &gif.Options{NumColors: 256}); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported target image type")
	}
	return out.Bytes(), nil
}

func isProgressiveJPEG(data []byte) bool {
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return false
	}
	for i := 2; i < len(data); {
		for i < len(data) && data[i] != 0xff {
			i++
		}
		for i < len(data) && data[i] == 0xff {
			i++
		}
		if i >= len(data) {
			return false
		}
		marker := data[i]
		i++
		switch marker {
		case 0xc2, 0xc6, 0xca, 0xce:
			return true
		case 0xd9, 0xda:
			return false
		case 0x01, 0xd0, 0xd1, 0xd2, 0xd3, 0xd4, 0xd5, 0xd6, 0xd7, 0xd8:
			continue
		}
		if i+2 > len(data) {
			return false
		}
		length := int(data[i])<<8 | int(data[i+1])
		if length < 2 || i+length > len(data) {
			return false
		}
		i += length
	}
	return false
}

func epubCoverImageTargetFromOPF(zr *zip.Reader, opfPath string, opf opfDoc) (epubCoverImageTarget, bool, error) {
	for _, coverHref := range coverHrefCandidates(opf) {
		target, ok, err := readEPUBImageTargetByHref(zr, opfPath, coverHref)
		if err != nil {
			return epubCoverImageTarget{}, false, err
		}
		if ok {
			target = attachManifestItem(target, opfPath, opf)
			return target, true, nil
		}

		target, ok, err = readEPUBCoverPageImageTarget(zr, opfPath, coverHref)
		if err != nil {
			return epubCoverImageTarget{}, false, err
		}
		if ok {
			target = attachManifestItem(target, opfPath, opf)
			return target, true, nil
		}
	}
	return epubCoverImageTarget{}, false, nil
}

func readEPUBImageTargetByHref(zr *zip.Reader, baseFilePath, href string) (epubCoverImageTarget, bool, error) {
	imgPath := epubZipPath(baseFilePath, href)
	imgFile, ambiguous := epubZipFileMatch(zr, imgPath)
	if ambiguous {
		return epubCoverImageTarget{}, false, fmt.Errorf("EPUB cover image %s resolves to multiple entries", imgPath)
	}
	if imgFile == nil {
		return epubCoverImageTarget{}, false, nil
	}

	imgBytes, err := readZipFileLimited(imgFile, maxEPUBCoverBytes)
	if err != nil {
		return epubCoverImageTarget{}, false, err
	}
	if _, ok := coverImageExtensionFromBytes(imgBytes); !ok {
		return epubCoverImageTarget{}, false, nil
	}
	return epubCoverImageTarget{zipPath: imgPath, bytes: imgBytes}, true, nil
}

func readEPUBCoverPageImageTarget(zr *zip.Reader, opfPath, pageHref string) (epubCoverImageTarget, bool, error) {
	pagePath := epubZipPath(opfPath, pageHref)
	pageFile, ambiguous := epubZipFileMatch(zr, pagePath)
	if ambiguous {
		return epubCoverImageTarget{}, false, fmt.Errorf("EPUB cover page %s resolves to multiple entries", pagePath)
	}
	if pageFile == nil {
		return epubCoverImageTarget{}, false, nil
	}

	rcPage, err := pageFile.Open()
	if err != nil {
		return epubCoverImageTarget{}, false, err
	}
	defer rcPage.Close()

	imageHref := firstImageHref(rcPage)
	if imageHref == "" {
		return epubCoverImageTarget{}, false, nil
	}
	return readEPUBImageTargetByHref(zr, pagePath, imageHref)
}

func attachManifestItem(target epubCoverImageTarget, opfPath string, opf opfDoc) epubCoverImageTarget {
	for _, item := range opf.Manifest.Items {
		if epubZipPath(opfPath, item.Href) == target.zipPath {
			target.itemID = strings.TrimSpace(item.ID)
			target.mediaType = strings.TrimSpace(item.MediaType)
			return target
		}
	}
	return target
}

func sameMediaType(a, b string) bool {
	clean := func(s string) string {
		return strings.ToLower(strings.TrimSpace(strings.Split(s, ";")[0]))
	}
	return clean(a) == clean(b)
}

func uniqueOPFCoverID(opf opfDoc) string {
	used := make(map[string]bool)
	for _, item := range opf.Manifest.Items {
		if id := strings.TrimSpace(item.ID); id != "" {
			used[id] = true
		}
	}
	for _, id := range []string{"cover-image", "polka-cover-image"} {
		if !used[id] {
			return id
		}
	}
	for i := 2; ; i++ {
		id := fmt.Sprintf("polka-cover-image-%d", i)
		if !used[id] {
			return id
		}
	}
}

func uniqueEPUBCoverHref(zr *zip.Reader, opfPath, ext string) string {
	usedNames := make(map[string]bool, len(zr.File))
	for _, file := range zr.File {
		usedNames[zipEntryNameCollisionKey(file.Name)] = true
	}
	for _, href := range []string{
		path.Join("images", "polka-cover"+ext),
		"polka-cover" + ext,
		"cover" + ext,
	} {
		if !usedNames[zipEntryNameCollisionKey(epubZipPath(opfPath, href))] {
			return href
		}
	}
	for i := 2; ; i++ {
		href := path.Join("images", fmt.Sprintf("polka-cover-%d%s", i, ext))
		if !usedNames[zipEntryNameCollisionKey(epubZipPath(opfPath, href))] {
			return href
		}
	}
}

func opfDocumentVersionAtLeast3(raw []byte) (bool, error) {
	packageTag, err := opfFirstTag(raw, opfPackageTagRe)
	if err != nil {
		return false, err
	}
	return opfVersionAtLeast3(strings.TrimSpace(opfAttrs(string(packageTag.raw))["version"])), nil
}

func opfAppendMetadataChild(raw []byte, child string) ([]byte, error) {
	metadataTag, err := opfFirstTag(raw, opfMetadataTagRe)
	if err != nil {
		return nil, err
	}
	return opfAppendElementChild(raw, metadataTag, "metadata", child)
}

func opfInsertManifestItem(raw []byte, id, href, mediaType string, epub3 bool) ([]byte, error) {
	manifestTag, err := opfFirstTag(raw, opfManifestTagRe)
	if err != nil {
		return nil, err
	}
	prefix := opfTagNamePrefix(manifestTag.raw)
	attrs := fmt.Sprintf(` id="%s" href="%s" media-type="%s"`, opfEscapeAttr(id), opfEscapeAttr(href), opfEscapeAttr(mediaType))
	if epub3 {
		attrs += ` properties="cover-image"`
	}
	child := fmt.Sprintf("<%sitem%s/>", prefix, attrs)
	return opfAppendElementChild(raw, manifestTag, "manifest", child)
}

func opfSetManifestItemMediaType(raw []byte, id, mediaType string) ([]byte, error) {
	manifestTag, err := opfFirstTag(raw, opfManifestTagRe)
	if err != nil {
		return nil, err
	}
	endStart, _, err := opfFindElementEnd(raw, manifestTag.end, "manifest")
	if err != nil {
		return nil, err
	}

	for pos := manifestTag.end; pos < endStart; {
		tagStart := bytes.IndexByte(raw[pos:endStart], '<')
		if tagStart < 0 {
			break
		}
		tagStart += pos
		tagEnd := opfTagEnd(raw, tagStart)
		if tagEnd < 0 {
			return nil, fmt.Errorf("unterminated OPF manifest item tag")
		}
		info := opfParseTag(raw[tagStart:tagEnd])
		if !info.end && info.local == "item" && strings.TrimSpace(info.attrs["id"]) == id {
			nextTag := opfSetTagAttr(raw[tagStart:tagEnd], "media-type", mediaType)
			out := make([]byte, 0, len(raw)-tagEnd+tagStart+len(nextTag))
			out = append(out, raw[:tagStart]...)
			out = append(out, nextTag...)
			out = append(out, raw[tagEnd:]...)
			return out, nil
		}
		pos = tagEnd
	}
	return nil, fmt.Errorf("OPF manifest item %s not found", id)
}

func opfSetTagAttr(tag []byte, attr, value string) []byte {
	escaped := opfEscapeAttr(value)
	if loc := opfMediaTypeRe.FindSubmatchIndex(tag); loc != nil {
		out := make([]byte, 0, len(tag)+len(escaped))
		out = append(out, tag[:loc[4]]...)
		out = append(out, []byte(`"`+escaped+`"`)...)
		out = append(out, tag[loc[5]:]...)
		return out
	}
	insertAt := len(tag) - 1
	if insertAt > 0 && tag[insertAt-1] == '/' {
		insertAt--
	}
	out := make([]byte, 0, len(tag)+len(attr)+len(escaped)+4)
	out = append(out, tag[:insertAt]...)
	out = append(out, []byte(fmt.Sprintf(` %s="%s"`, attr, escaped))...)
	out = append(out, tag[insertAt:]...)
	return out
}

func opfAppendElementChild(raw []byte, tag opfTagRange, local, child string) ([]byte, error) {
	info := opfParseTag(tag.raw)
	if info.local != local {
		return nil, fmt.Errorf("OPF %s tag not found", local)
	}
	if info.selfClosing {
		return opfExpandSelfClosingElement(raw, tag, local, child), nil
	}
	endStart, _, err := opfFindElementEnd(raw, tag.end, local)
	if err != nil {
		return nil, err
	}

	insertAt := endStart
	for insertAt > tag.end && isXMLSpace(raw[insertAt-1]) {
		insertAt--
	}
	tail := raw[insertAt:endStart]
	inner := raw[tag.end:endStart]
	newline := opfLineEnding(raw)
	childIndent := opfChildIndent(inner)
	if strings.TrimSpace(string(inner)) == "" {
		childIndent = opfLineIndent(raw, tag.start) + "  "
	}
	insert := newline + childIndent + strings.TrimSpace(child)
	if len(tail) > 0 && bytes.ContainsAny(tail, "\n\r") {
		insert += string(tail)
	} else {
		insert += newline + opfEndIndent(inner)
	}

	out := make([]byte, 0, len(raw)+len(insert))
	out = append(out, raw[:insertAt]...)
	out = append(out, insert...)
	out = append(out, raw[endStart:]...)
	return out, nil
}

func opfExpandSelfClosingElement(raw []byte, tag opfTagRange, local, child string) []byte {
	newline := opfLineEnding(raw)
	parentIndent := opfLineIndent(raw, tag.start)
	childIndent := parentIndent + "  "
	openTag := opfOpenTagFromSelfClosing(tag.raw)
	closeTag := fmt.Sprintf("</%s%s>", opfTagNamePrefix(tag.raw), local)
	replacement := string(openTag) + newline + childIndent + strings.TrimSpace(child) + newline + parentIndent + closeTag

	out := make([]byte, 0, len(raw)-len(tag.raw)+len(replacement))
	out = append(out, raw[:tag.start]...)
	out = append(out, replacement...)
	out = append(out, raw[tag.end:]...)
	return out
}

func opfOpenTagFromSelfClosing(tag []byte) []byte {
	s := strings.TrimRight(string(tag), " \t\r\n")
	if strings.HasSuffix(s, "/>") {
		s = strings.TrimRight(s[:len(s)-2], " \t\r\n") + ">"
	}
	return []byte(s)
}

func opfTagNamePrefix(tag []byte) string {
	s := strings.TrimSpace(string(tag))
	s = strings.TrimSpace(strings.TrimPrefix(s, "<"))
	s = strings.TrimSpace(strings.TrimPrefix(s, "/"))
	nameEnd := strings.IndexFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '/' || r == '>'
	})
	if nameEnd >= 0 {
		s = s[:nameEnd]
	}
	if idx := strings.LastIndexByte(s, ':'); idx >= 0 {
		return s[:idx+1]
	}
	return ""
}

func opfLineIndent(raw []byte, pos int) string {
	if pos > len(raw) {
		pos = len(raw)
	}
	lineStart := bytes.LastIndexByte(raw[:pos], '\n') + 1
	indent := raw[lineStart:pos]
	for _, b := range indent {
		if b != ' ' && b != '\t' {
			return ""
		}
	}
	return string(indent)
}

func writeEPUBMimetype(zw *zip.Writer, f *zip.File) error {
	data, err := readZipFileLimited(f, maxEPUBMimetypeBytes)
	if err != nil {
		return fmt.Errorf("read EPUB mimetype: %w", err)
	}
	header := &zip.FileHeader{Name: "mimetype", Method: zip.Store}
	header.CRC32 = crc32.ChecksumIEEE(data)
	header.CompressedSize64 = uint64(len(data))
	header.UncompressedSize64 = uint64(len(data))
	w, err := zw.CreateRaw(header)
	if err != nil {
		return fmt.Errorf("create EPUB mimetype: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write EPUB mimetype: %w", err)
	}
	return nil
}

// writeZipEntryData recompresses a single ZIP member with new bytes, clearing
// size/CRC/timestamp fields so repeated writes of the same content stay
// byte-identical. Shared by the EPUB and FB2 write-back repackers.
func writeZipEntryData(zw *zip.Writer, f *zip.File, data []byte) error {
	if f.Flags&0x1 != 0 {
		return fmt.Errorf("zip entry %s is encrypted", f.Name)
	}
	header := f.FileHeader
	header.Name = f.Name
	header.CRC32 = 0
	header.CompressedSize = 0
	header.CompressedSize64 = 0
	header.UncompressedSize = 0
	header.UncompressedSize64 = 0
	// The rewritten member is regenerated anyway; drop timestamp extras so
	// repeated writes of the same metadata snapshot stay byte-identical.
	header.Modified = time.Time{}
	header.ModifiedTime = 0
	header.ModifiedDate = 0
	header.Extra = nil
	w, err := zw.CreateHeader(&header)
	if err != nil {
		return fmt.Errorf("create zip entry %s: %w", f.Name, err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write zip entry %s: %w", f.Name, err)
	}
	return nil
}

// copyZipEntryRaw copies an unchanged member's already-compressed bytes through
// CreateRaw/OpenRaw so they stay byte-identical.
func copyZipEntryRaw(zw *zip.Writer, f *zip.File) error {
	header := f.FileHeader
	if strings.HasSuffix(f.Name, "/") && f.CompressedSize64 != 0 {
		return copyZipDirectoryEntry(zw, f)
	}
	w, err := zw.CreateRaw(&header)
	if err != nil {
		return fmt.Errorf("create raw zip entry %s: %w", f.Name, err)
	}
	rc, err := f.OpenRaw()
	if err != nil {
		return fmt.Errorf("open raw zip entry %s: %w", f.Name, err)
	}
	if _, err := io.Copy(w, rc); err != nil {
		return fmt.Errorf("copy raw zip entry %s: %w", f.Name, err)
	}
	return nil
}

func copyZipDirectoryEntry(zw *zip.Writer, f *zip.File) error {
	header := f.FileHeader
	header.Name = f.Name
	if !strings.HasSuffix(header.Name, "/") {
		header.Name += "/"
	}
	header.CRC32 = 0
	header.CompressedSize = 0
	header.CompressedSize64 = 0
	header.UncompressedSize = 0
	header.UncompressedSize64 = 0
	// Keep any existing extra fields, but clear Modified so archive/zip does
	// not append another extended timestamp block on every write-back pass.
	header.Modified = time.Time{}
	if _, err := zw.CreateHeader(&header); err != nil {
		return fmt.Errorf("create zip directory entry %s: %w", f.Name, err)
	}
	return nil
}

func rewriteOPFMetadata(raw []byte, meta Metadata, modified time.Time) ([]byte, error) {
	packageTag, err := opfFirstTag(raw, opfPackageTagRe)
	if err != nil {
		return nil, err
	}
	metadataTag, err := opfFirstTag(raw, opfMetadataTagRe)
	if err != nil {
		return nil, err
	}
	endStart, _, err := opfFindMetadataEnd(raw, metadataTag.end)
	if err != nil {
		return nil, err
	}

	packageAttrs := opfAttrs(string(packageTag.raw))
	version := strings.TrimSpace(packageAttrs["version"])
	uniqueID := strings.TrimSpace(packageAttrs["unique-identifier"])
	epub3 := opfVersionAtLeast3(version)

	inner := raw[metadataTag.end:endStart]
	children, err := opfMetadataChildren(inner)
	if err != nil {
		return nil, err
	}

	preserved := opfPreservedMetadataChildren(children, uniqueID, bookmeta.ParseIdentifiers(meta.Identifier))
	generatedUsesOPFAttrs := opfGeneratedUsesOPFAttrs(meta, epub3)
	generated := renderOPFMetadataChildren(raw, string(metadataTag.raw), meta, preserved.Identifiers, modified, epub3, generatedUsesOPFAttrs, preserved.GeneratedIdentifierIDs)
	nextInner := assembleOPFMetadataInner(inner, generated, preserved.Children)

	out := make([]byte, 0, len(raw)-len(inner)+len(nextInner))
	out = append(out, raw[:metadataTag.end]...)
	out = append(out, nextInner...)
	out = append(out, raw[endStart:]...)
	if generatedUsesOPFAttrs && !opfHasNamespacePrefix(raw, string(metadataTag.raw), "opf") {
		out, err = opfEnsurePackageNamespace(out, "opf", "http://www.idpf.org/2007/opf")
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

type opfTagRange struct {
	raw   []byte
	start int
	end   int
}

func opfFirstTag(raw []byte, re *regexp.Regexp) (opfTagRange, error) {
	loc := re.FindIndex(raw)
	if loc == nil {
		return opfTagRange{}, fmt.Errorf("OPF package metadata not found")
	}
	return opfTagRange{raw: raw[loc[0]:loc[1]], start: loc[0], end: loc[1]}, nil
}

func opfFindMetadataEnd(raw []byte, after int) (int, int, error) {
	return opfFindElementEnd(raw, after, "metadata")
}

func opfFindElementEnd(raw []byte, after int, local string) (int, int, error) {
	depth := 0
	for pos := after; pos < len(raw); {
		tagStart := bytes.IndexByte(raw[pos:], '<')
		if tagStart < 0 {
			break
		}
		tagStart += pos
		tagEnd := opfTagEnd(raw, tagStart)
		if tagEnd < 0 {
			return 0, 0, fmt.Errorf("unterminated OPF %s tag", local)
		}
		tag := raw[tagStart:tagEnd]
		info := opfParseTag(tag)
		if info.local == local {
			switch {
			case info.end:
				if depth == 0 {
					return tagStart, tagEnd, nil
				}
				depth--
			case !info.selfClosing:
				depth++
			}
		}
		pos = tagEnd
	}
	return 0, 0, fmt.Errorf("OPF %s closing tag not found", local)
}

type opfParsedTag struct {
	local       string
	end         bool
	selfClosing bool
	attrs       map[string]string
}

func opfParseTag(tag []byte) opfParsedTag {
	s := strings.TrimSpace(string(tag))
	if len(s) < 3 || s[0] != '<' {
		return opfParsedTag{}
	}
	if strings.HasPrefix(s, "<!--") || strings.HasPrefix(s, "<?") || strings.HasPrefix(s, "<!") {
		return opfParsedTag{}
	}
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(s, "<"), ">"))
	out := opfParsedTag{}
	if strings.HasPrefix(s, "/") {
		out.end = true
		s = strings.TrimSpace(strings.TrimPrefix(s, "/"))
	}
	if strings.HasSuffix(s, "/") {
		out.selfClosing = true
		s = strings.TrimSpace(strings.TrimSuffix(s, "/"))
	}
	nameEnd := strings.IndexFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	name := s
	if nameEnd >= 0 {
		name = s[:nameEnd]
	}
	out.local = opfXMLLocalName(name)
	if !out.end {
		out.attrs = opfAttrs(string(tag))
	}
	return out
}

func opfTagEnd(raw []byte, start int) int {
	if bytes.HasPrefix(raw[start:], []byte("<!--")) {
		end := bytes.Index(raw[start+4:], []byte("-->"))
		if end < 0 {
			return -1
		}
		return start + 4 + end + len("-->")
	}
	if bytes.HasPrefix(raw[start:], []byte("<![CDATA[")) {
		end := bytes.Index(raw[start+9:], []byte("]]>"))
		if end < 0 {
			return -1
		}
		return start + 9 + end + len("]]>")
	}

	var quote byte
	for i := start + 1; i < len(raw); i++ {
		switch raw[i] {
		case '\'', '"':
			if quote == 0 {
				quote = raw[i]
			} else if quote == raw[i] {
				quote = 0
			}
		case '>':
			if quote == 0 {
				return i + 1
			}
		}
	}
	return -1
}

type opfMetadataChild struct {
	raw   []byte
	local string
	attrs map[string]string
	text  string
}

func opfMetadataChildren(raw []byte) ([]opfMetadataChild, error) {
	// We only need direct <metadata> children. Keeping their raw bytes lets us
	// preserve unknown records without an encoding/xml namespace round-trip.
	var children []opfMetadataChild
	depth := 0
	childStart := -1
	childLocal := ""
	childAttrs := map[string]string(nil)

	for pos := 0; pos < len(raw); {
		tagStart := bytes.IndexByte(raw[pos:], '<')
		if tagStart < 0 {
			break
		}
		tagStart += pos
		tagEnd := opfTagEnd(raw, tagStart)
		if tagEnd < 0 {
			return nil, fmt.Errorf("unterminated metadata child tag")
		}
		info := opfParseTag(raw[tagStart:tagEnd])
		if info.local == "" {
			pos = tagEnd
			continue
		}

		switch {
		case info.end:
			if depth > 0 {
				depth--
				if depth == 0 && childStart >= 0 {
					fragment := raw[childStart:tagEnd]
					children = append(children, opfMetadataChild{
						raw:   bytes.Clone(fragment),
						local: childLocal,
						attrs: childAttrs,
						text:  opfElementText(fragment),
					})
					childStart = -1
					childLocal = ""
					childAttrs = nil
				}
			}
		case info.selfClosing:
			if depth == 0 {
				fragment := raw[tagStart:tagEnd]
				children = append(children, opfMetadataChild{
					raw:   bytes.Clone(fragment),
					local: info.local,
					attrs: info.attrs,
				})
			}
		default:
			if depth == 0 {
				childStart = tagStart
				childLocal = info.local
				childAttrs = info.attrs
			}
			depth++
		}

		pos = tagEnd
	}
	if depth != 0 {
		return nil, fmt.Errorf("unclosed metadata child tag")
	}
	return children, nil
}

type opfPreservedMetadata struct {
	Children               [][]byte
	GeneratedIdentifierIDs map[string]string
	Identifiers            []opfOutputIdentifier
}

type opfOutputIdentifier struct {
	Identifier bookmeta.Identifier
	Raw        []byte
}

func opfPreservedMetadataChildren(children []opfMetadataChild, uniqueID string, polkaIDs []bookmeta.Identifier) opfPreservedMetadata {
	// Generated Polka records replace the fields in bookmeta.Metadata; unrelated
	// metadata, cover hints, manifest refinements, and internal package ids survive.
	var desired []bookmeta.Identifier
	for _, id := range polkaIDs {
		if strings.TrimSpace(id.Type) == "" || strings.TrimSpace(id.Value) == "" || bookmeta.IsInternalIdentifier(id) {
			continue
		}
		desired = append(desired, id)
	}
	matchedDesired := make([]bool, len(desired))
	matchedChildren := make(map[int]bool)
	matchedChildByDesired := make([]int, len(desired))
	for i := range matchedChildByDesired {
		matchedChildByDesired[i] = -1
	}
	for childIndex, child := range children {
		if child.local != "identifier" {
			continue
		}
		id := bookmeta.IdentifierFromOPF(child.attrs["scheme"], child.text)
		if strings.TrimSpace(id.Value) == "" || bookmeta.IsInternalIdentifier(id) {
			continue
		}
		for desiredIndex, want := range desired {
			if matchedDesired[desiredIndex] || !opfIdentifiersEqual(id, want) {
				continue
			}
			matchedDesired[desiredIndex] = true
			matchedChildren[childIndex] = true
			matchedChildByDesired[desiredIndex] = childIndex
			break
		}
	}

	polkaTypes := make(map[string]bool)
	for _, id := range desired {
		typ := strings.ToLower(strings.TrimSpace(id.Type))
		polkaTypes[typ] = true
	}

	uniqueIDPolkaTypes := make(map[string]bool)
	generatedIdentifierIDs := make(map[string]string)
	for childIndex, child := range children {
		if child.local != "identifier" || uniqueID == "" || strings.TrimSpace(child.attrs["id"]) != uniqueID {
			continue
		}
		if matchedChildren[childIndex] {
			continue
		}
		id := bookmeta.IdentifierFromOPF(child.attrs["scheme"], child.text)
		typ := strings.ToLower(strings.TrimSpace(id.Type))
		if typ != "" && polkaTypes[typ] && !bookmeta.IsInternalIdentifier(id) {
			uniqueIDPolkaTypes[typ] = true
			generatedIdentifierIDs[typ] = uniqueID
		}
	}

	seriesCollectionIDs := make(map[string]bool)
	for _, child := range children {
		if child.local != "meta" {
			continue
		}
		if strings.ToLower(strings.TrimSpace(child.attrs["property"])) != "collection-type" || strings.ToLower(strings.TrimSpace(child.text)) != "series" {
			continue
		}
		target := strings.TrimPrefix(strings.TrimSpace(child.attrs["refines"]), "#")
		if target != "" {
			seriesCollectionIDs[target] = true
		}
	}
	for _, child := range children {
		if child.local != "meta" || strings.ToLower(strings.TrimSpace(child.attrs["property"])) != "group-position" {
			continue
		}
		target := strings.TrimPrefix(strings.TrimSpace(child.attrs["refines"]), "#")
		if target != "" {
			seriesCollectionIDs[target] = true
		}
	}

	removedIDs := make(map[string]bool)
	for childIndex, child := range children {
		if matchedChildren[childIndex] {
			continue
		}
		if opfChildOwnedByPolka(child, uniqueID, polkaTypes, uniqueIDPolkaTypes, seriesCollectionIDs) {
			if id := strings.TrimSpace(child.attrs["id"]); id != "" {
				removedIDs[id] = true
			}
		}
	}

	var preserved [][]byte
	for childIndex, child := range children {
		if matchedChildren[childIndex] {
			continue
		}
		if opfChildOwnedByPolka(child, uniqueID, polkaTypes, uniqueIDPolkaTypes, seriesCollectionIDs) {
			continue
		}
		target := strings.TrimPrefix(strings.TrimSpace(child.attrs["refines"]), "#")
		if target != "" && removedIDs[target] {
			continue
		}
		preserved = append(preserved, bytes.TrimSpace(child.raw))
	}
	identifiers := make([]opfOutputIdentifier, 0, len(desired))
	for i, id := range desired {
		identifier := opfOutputIdentifier{Identifier: id}
		if childIndex := matchedChildByDesired[i]; childIndex >= 0 {
			identifier.Raw = bytes.TrimSpace(children[childIndex].raw)
		}
		identifiers = append(identifiers, identifier)
	}
	return opfPreservedMetadata{
		Children:               preserved,
		GeneratedIdentifierIDs: generatedIdentifierIDs,
		Identifiers:            identifiers,
	}
}

func opfIdentifiersEqual(a, b bookmeta.Identifier) bool {
	return strings.EqualFold(strings.TrimSpace(a.Type), strings.TrimSpace(b.Type)) &&
		strings.TrimSpace(a.Value) == strings.TrimSpace(b.Value)
}

func opfChildOwnedByPolka(child opfMetadataChild, uniqueID string, polkaTypes map[string]bool, uniqueIDPolkaTypes map[string]bool, seriesCollectionIDs map[string]bool) bool {
	switch child.local {
	case "title", "creator", "language", "description", "publisher", "date", "subject":
		return true
	case "identifier":
		id := bookmeta.IdentifierFromOPF(child.attrs["scheme"], child.text)
		typ := strings.ToLower(strings.TrimSpace(id.Type))
		if uniqueID != "" && strings.TrimSpace(child.attrs["id"]) == uniqueID {
			return uniqueIDPolkaTypes[typ]
		}
		return polkaTypes[typ]
	case "meta":
		name := opfMetaName(child.attrs["name"])
		switch name {
		case "calibre:title_sort", "calibre:series", "calibre:series_index":
			return true
		}
		property := strings.ToLower(strings.TrimSpace(child.attrs["property"]))
		if property == "dcterms:modified" {
			return true
		}
		if property == "belongs-to-collection" && seriesCollectionIDs[strings.TrimSpace(child.attrs["id"])] {
			return true
		}
	}
	return false
}

func opfGeneratedUsesOPFAttrs(meta Metadata, epub3 bool) bool {
	if epub3 {
		return false
	}
	for _, author := range meta.Authors {
		if strings.TrimSpace(author.Name) != "" {
			return true
		}
	}
	for _, id := range bookmeta.ParseIdentifiers(meta.Identifier) {
		scheme, value := opfIdentifierParts(id)
		if scheme != "" && value != "" && !bookmeta.IsInternalIdentifier(id) {
			return true
		}
	}
	return false
}

func renderOPFMetadataChildren(raw []byte, metadataTag string, meta Metadata, identifiers []opfOutputIdentifier, modified time.Time, epub3 bool, generatedUsesOPFAttrs bool, generatedIdentifierIDs map[string]string) []string {
	dc := "dc:"
	if !opfHasNamespacePrefix(raw, metadataTag, "dc") {
		dc = ""
	}
	opfAttrPrefix := ""
	if generatedUsesOPFAttrs {
		opfAttrPrefix = "opf:"
	}

	var out []string
	titleWritten := false
	if title := strings.TrimSpace(meta.Title); title != "" {
		attrs := ` id="polka-title"`
		out = append(out, fmt.Sprintf("<%stitle%s>%s</%stitle>", dc, attrs, opfEscapeText(title), dc))
		titleWritten = true
	}
	if epub3 && titleWritten {
		out = append(out, `<meta refines="#polka-title" property="title-type">main</meta>`)
		if sortTitle := strings.TrimSpace(meta.SortTitle); sortTitle != "" {
			out = append(out, fmt.Sprintf(`<meta refines="#polka-title" property="file-as">%s</meta>`, opfEscapeText(sortTitle)))
		}
	}
	if sortTitle := strings.TrimSpace(meta.SortTitle); sortTitle != "" {
		out = append(out, fmt.Sprintf(`<meta name="calibre:title_sort" content="%s"/>`, opfEscapeAttr(sortTitle)))
	}

	authorSeq := 0
	for _, author := range meta.Authors {
		name := strings.TrimSpace(author.Name)
		if name == "" {
			continue
		}
		authorSeq++
		id := fmt.Sprintf("polka-creator-%d", authorSeq)
		role := strings.TrimSpace(author.Role)
		if role == "" {
			role = "aut"
		}
		attrs := fmt.Sprintf(` id="%s"`, id)
		if !epub3 {
			if sortName := strings.TrimSpace(author.SortName); sortName != "" {
				attrs += fmt.Sprintf(` %sfile-as="%s"`, opfAttrPrefix, opfEscapeAttr(sortName))
			}
			attrs += fmt.Sprintf(` %srole="%s"`, opfAttrPrefix, opfEscapeAttr(role))
		}
		out = append(out, fmt.Sprintf("<%screator%s>%s</%screator>", dc, attrs, opfEscapeText(name), dc))
		if epub3 {
			if sortName := strings.TrimSpace(author.SortName); sortName != "" {
				out = append(out, fmt.Sprintf(`<meta refines="#%s" property="file-as">%s</meta>`, id, opfEscapeText(sortName)))
			}
			out = append(out, fmt.Sprintf(`<meta refines="#%s" property="role" scheme="marc:relators">%s</meta>`, id, opfEscapeText(role)))
			out = append(out, fmt.Sprintf(`<meta refines="#%s" property="display-seq">%d</meta>`, id, authorSeq))
		}
	}

	if language := strings.TrimSpace(meta.Language); language != "" {
		out = append(out, fmt.Sprintf("<%slanguage>%s</%slanguage>", dc, opfEscapeText(language), dc))
	}
	if publisher := strings.TrimSpace(meta.Publisher); publisher != "" {
		out = append(out, fmt.Sprintf("<%spublisher>%s</%spublisher>", dc, opfEscapeText(publisher), dc))
	}
	if date := strings.TrimSpace(meta.Date); date != "" {
		out = append(out, fmt.Sprintf("<%sdate>%s</%sdate>", dc, opfEscapeText(date), dc))
	}
	if description := strings.TrimSpace(meta.Description); description != "" {
		out = append(out, fmt.Sprintf("<%sdescription>%s</%sdescription>", dc, opfEscapeText(description), dc))
	}
	for _, tag := range meta.Tags {
		if tag = strings.TrimSpace(tag); tag != "" {
			out = append(out, fmt.Sprintf("<%ssubject>%s</%ssubject>", dc, opfEscapeText(tag), dc))
		}
	}
	identifierSeq := 0
	for _, output := range identifiers {
		id := output.Identifier
		scheme, value := opfIdentifierParts(id)
		if value == "" || bookmeta.IsInternalIdentifier(id) {
			continue
		}
		identifierSeq++
		if len(output.Raw) > 0 {
			out = append(out, string(output.Raw))
			continue
		}
		elementID := strings.TrimSpace(generatedIdentifierIDs[scheme])
		if elementID != "" {
			delete(generatedIdentifierIDs, scheme)
		} else {
			elementID = fmt.Sprintf("polka-identifier-%d", identifierSeq)
		}
		attrs := fmt.Sprintf(` id="%s"`, opfEscapeAttr(elementID))
		if scheme != "" && !epub3 {
			attrs += fmt.Sprintf(` %sscheme="%s"`, opfAttrPrefix, opfEscapeAttr(scheme))
		}
		out = append(out, fmt.Sprintf(`<%sidentifier%s>%s</%sidentifier>`, dc, attrs, opfEscapeText(opfIdentifierText(scheme, value, epub3)), dc))
	}
	if series := strings.TrimSpace(meta.Series); series != "" {
		out = append(out, fmt.Sprintf(`<meta name="calibre:series" content="%s"/>`, opfEscapeAttr(series)))
		if meta.SeriesIndex != 0 {
			out = append(out, fmt.Sprintf(`<meta name="calibre:series_index" content="%s"/>`, opfEscapeAttr(strconv.FormatFloat(meta.SeriesIndex, 'f', -1, 64))))
		}
		if epub3 {
			out = append(out, fmt.Sprintf(`<meta property="belongs-to-collection" id="polka-series">%s</meta>`, opfEscapeText(series)))
			out = append(out, `<meta refines="#polka-series" property="collection-type">series</meta>`)
			if meta.SeriesIndex != 0 {
				out = append(out, fmt.Sprintf(`<meta refines="#polka-series" property="group-position">%s</meta>`, opfEscapeText(strconv.FormatFloat(meta.SeriesIndex, 'f', -1, 64))))
			}
		}
	}
	if epub3 && !modified.IsZero() {
		out = append(out, fmt.Sprintf(`<meta property="dcterms:modified">%s</meta>`, opfEscapeText(modified.UTC().Format("2006-01-02T15:04:05Z"))))
	}
	return out
}

func assembleOPFMetadataInner(previous []byte, generated []string, preserved [][]byte) []byte {
	newline := opfLineEnding(previous)
	childIndent := opfChildIndent(previous)
	endIndent := opfEndIndent(previous)
	var out strings.Builder
	out.WriteString(newline)
	for _, child := range generated {
		child = strings.TrimSpace(child)
		if child == "" {
			continue
		}
		out.WriteString(childIndent)
		out.WriteString(child)
		out.WriteString(newline)
	}
	for _, child := range preserved {
		child = bytes.TrimSpace(child)
		if len(child) == 0 {
			continue
		}
		out.WriteString(childIndent)
		out.Write(child)
		out.WriteString(newline)
	}
	out.WriteString(endIndent)
	return []byte(out.String())
}

func opfLineEnding(raw []byte) string {
	if bytes.Contains(raw, []byte("\r\n")) {
		return "\r\n"
	}
	return "\n"
}

func opfChildIndent(raw []byte) string {
	lines := strings.SplitSeq(string(raw), "\n")
	for line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "<") {
			return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		}
	}
	return "    "
}

func opfEndIndent(raw []byte) string {
	idx := bytes.LastIndexByte(raw, '\n')
	if idx < 0 {
		return ""
	}
	trailing := string(raw[idx+1:])
	if strings.TrimSpace(trailing) == "" {
		return trailing
	}
	return ""
}

func opfAttrs(tag string) map[string]string {
	attrs := make(map[string]string)
	for _, match := range opfAttrRe.FindAllStringSubmatch(tag, -1) {
		if len(match) < 5 {
			continue
		}
		name := opfXMLLocalName(match[1])
		value := match[3]
		if value == "" {
			value = match[4]
		}
		attrs[name] = html.UnescapeString(value)
	}
	return attrs
}

func opfXMLLocalName(name string) string {
	name = strings.TrimSpace(name)
	if _, after, ok := strings.CutLast(name, ":"); ok {
		name = after
	}
	return strings.ToLower(name)
}

func opfElementText(raw []byte) string {
	text := opfStripTagsRe.ReplaceAllString(string(raw), "")
	return strings.TrimSpace(html.UnescapeString(text))
}

func opfVersionAtLeast3(version string) bool {
	version = strings.TrimSpace(version)
	if version == "" {
		return false
	}
	major := version
	if idx := strings.IndexByte(version, '.'); idx >= 0 {
		major = version[:idx]
	}
	n, err := strconv.Atoi(major)
	return err == nil && n >= 3
}

func opfHasNamespacePrefix(raw []byte, metadataTag, prefix string) bool {
	if opfTagDeclaresNamespace([]byte(metadataTag), prefix) {
		return true
	}
	if loc := opfPackageTagRe.Find(raw); loc != nil && opfTagDeclaresNamespace(loc, prefix) {
		return true
	}
	return false
}

func opfTagDeclaresNamespace(tag []byte, prefix string) bool {
	want := "xmlns:" + strings.ToLower(strings.TrimSpace(prefix))
	for _, match := range opfAttrRe.FindAllStringSubmatch(string(tag), -1) {
		if len(match) >= 2 && strings.ToLower(strings.TrimSpace(match[1])) == want {
			return true
		}
	}
	return false
}

func opfEnsurePackageNamespace(raw []byte, prefix, uri string) ([]byte, error) {
	if opfHasNamespacePrefix(raw, "", prefix) {
		return raw, nil
	}
	loc := opfPackageTagRe.FindIndex(raw)
	if loc == nil {
		return nil, fmt.Errorf("OPF package tag not found")
	}
	end := opfTagEnd(raw, loc[0])
	if end < 0 {
		return nil, fmt.Errorf("unterminated OPF package tag")
	}
	insertAt := end - 1
	for insertAt > loc[0] && isXMLSpace(raw[insertAt-1]) {
		insertAt--
	}
	if insertAt > loc[0] && raw[insertAt-1] == '/' {
		insertAt--
	}
	decl := []byte(fmt.Sprintf(` xmlns:%s="%s"`, prefix, uri))
	out := make([]byte, 0, len(raw)+len(decl))
	out = append(out, raw[:insertAt]...)
	out = append(out, decl...)
	out = append(out, raw[insertAt:]...)
	return out, nil
}

func opfIdentifierParts(id bookmeta.Identifier) (string, string) {
	scheme := strings.ToLower(strings.TrimSpace(id.Type))
	value := strings.TrimSpace(id.Value)
	if value == "" {
		return "", ""
	}
	return scheme, value
}

func opfIdentifierText(scheme, value string, epub3 bool) string {
	if !epub3 || scheme == "" || scheme == "url" {
		return value
	}
	return scheme + ":" + value
}

func opfEscapeText(s string) string {
	return html.EscapeString(strings.TrimSpace(s))
}

func opfEscapeAttr(s string) string {
	return html.EscapeString(strings.TrimSpace(s))
}
