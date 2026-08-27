package converter

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html/charset"

	"github.com/levmv/polka/internal/format"
)

const rebuildEPUBMimetype = "application/epub+zip"

var (
	rebuildXMLEncodingRE  = regexp.MustCompile(`(?is)^(?:\xef\xbb\xbf)?\s*<\?xml\b[^?]*?\bencoding\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	rebuildXMLVersion11RE = regexp.MustCompile(`(?is)^(?:\xef\xbb\xbf)?\s*<\?xml\b[^?]*?\bversion\s*=\s*(?:"(1\.1)"|'(1\.1)')`)
)

type rebuildPackage struct {
	opfPath               string
	opfFile               *zip.File
	opfBytes              []byte
	containerFile         *zip.File
	containerData         []byte
	repairXHTMLMetaValues bool
}

type rebuildOPFDoc struct {
	XMLName          xml.Name `xml:"package"`
	Version          string   `xml:"version,attr"`
	UniqueIdentifier string   `xml:"unique-identifier,attr"`
	Metadata         struct {
		Identifiers []struct {
			ID    string `xml:"id,attr"`
			Value string `xml:",chardata"`
		} `xml:"identifier"`
		Meta []struct {
			Name    string `xml:"name,attr"`
			Content string `xml:"content,attr"`
		} `xml:"meta"`
	} `xml:"metadata"`
	Manifest struct {
		Items []kepubManifestItem `xml:"item"`
	} `xml:"manifest"`
	Spine struct {
		TOC     string `xml:"toc,attr"`
		PageMap string `xml:"page-map,attr"`
		Items   []struct {
			IDRef string `xml:"idref,attr"`
		} `xml:"itemref"`
	} `xml:"spine"`
	Guide struct {
		References []struct {
			Type string `xml:"type,attr"`
			Href string `xml:"href,attr"`
		} `xml:"reference"`
	} `xml:"guide"`
}

type rebuildCandidate struct {
	path            string
	file            *zip.File
	raw             []byte
	normalizedXML11 bool
}

func rebuildEPUB(ctx context.Context, w io.Writer, src io.ReaderAt, size int64) error {
	if size < 0 {
		return fmt.Errorf("source size is invalid")
	}
	zr, err := zip.NewReader(src, size)
	if err != nil {
		return fmt.Errorf("open EPUB: %w", err)
	}
	if err := claimConversionResources(ctx, len(zr.File), "EPUB archive"); err != nil {
		return err
	}
	for _, file := range zr.File {
		if err := validateRebuildEntryName(file.Name); err != nil {
			return err
		}
		if err := kepubRejectEncryptedEntry(file); err != nil {
			return err
		}
	}

	signatures, ambiguous := format.ResolveZIPEntry(zr, "META-INF/signatures.xml")
	if ambiguous {
		return fmt.Errorf("EPUB signatures.xml resolves to multiple archive entries")
	}
	if signatures != nil {
		return fmt.Errorf("EPUB contains signatures.xml; refusing rebuild because package signatures would become stale")
	}
	if _, ambiguous := format.ResolveZIPEntry(zr, "META-INF/encryption.xml"); ambiguous {
		return fmt.Errorf("EPUB encryption.xml resolves to multiple archive entries")
	}

	pkg, err := readRebuildPackage(ctx, zr)
	if err != nil {
		return err
	}
	sourceOPFBytes := pkg.opfBytes
	var missingStylesheets map[string]bool
	pkg.opfBytes, missingStylesheets, err = removeMissingPresentationReferences(zr, pkg.opfPath, pkg.opfBytes)
	if err != nil {
		return err
	}
	pkg.opfBytes = normalizeVendorImageGuide(pkg.opfPath, pkg.opfBytes)
	pkg.opfBytes, err = removeLegacyPageMapPointer(ctx, zr, pkg.opfPath, pkg.opfBytes)
	if err != nil {
		return err
	}
	entryRepairs, inlineSVGDocuments, err := rebuildXHTMLRepairs(ctx, zr, pkg, rebuildContentRepairPlan{
		xml11MetaValues:    pkg.repairXHTMLMetaValues,
		missingStylesheets: missingStylesheets,
	})
	if err != nil {
		return err
	}
	pkg.opfBytes = addMissingSVGProperties(pkg.opfPath, pkg.opfBytes, inlineSVGDocuments)
	if err := format.ValidateEPUBRewriteSafety(zr, sourceOPFBytes, pkg.opfBytes); err != nil {
		return err
	}
	manifestPaths, err := kepubManifestPaths(pkg.opfPath, pkg.opfBytes)
	if err != nil {
		return err
	}
	manifestEntries := make(map[*zip.File]bool, len(manifestPaths))
	for manifestPath := range manifestPaths {
		file, err := kepubZipFile(zr, manifestPath)
		if err != nil {
			return fmt.Errorf("resolve EPUB manifest resource %s: %w", manifestPath, err)
		}
		if file != nil {
			manifestEntries[file] = true
		}
	}
	ncxFile, ncxData, err := rebuildNCXRepairs(ctx, zr, pkg)
	if err != nil {
		return err
	}
	if ncxFile != nil {
		if entryRepairs == nil {
			entryRepairs = make(map[*zip.File][]byte)
		}
		entryRepairs[ncxFile] = ncxData
	}

	zw := zip.NewWriter(w)
	closeWith := func(err error) error {
		_ = zw.Close()
		return err
	}
	if zr.Comment != "" {
		if err := zw.SetComment(zr.Comment); err != nil {
			return closeWith(fmt.Errorf("copy EPUB zip comment: %w", err))
		}
	}
	if err := addEPUBFile(zw, "mimetype", zip.Store, rebuildEPUBMimetype); err != nil {
		return closeWith(fmt.Errorf("write EPUB mimetype: %w", err))
	}
	if err := writeRebuildEntry(zw, "META-INF/container.xml", pkg.containerData, zip.Deflate); err != nil {
		return closeWith(err)
	}

	for _, file := range zr.File {
		if err := checkContext(ctx); err != nil {
			return closeWith(err)
		}
		if rebuildIsRootMimetype(file.Name) || file == pkg.containerFile {
			continue
		}
		if kepubFilterFile(file.Name) && !manifestEntries[file] {
			continue
		}
		if file == pkg.opfFile {
			if err := writeRebuildEntry(zw, pkg.opfPath, pkg.opfBytes, zip.Deflate); err != nil {
				return closeWith(err)
			}
			continue
		}
		if repaired, ok := entryRepairs[file]; ok {
			if err := writeRebuildEntry(zw, file.Name, repaired, file.Method); err != nil {
				return closeWith(err)
			}
			continue
		}
		if err := writeRebuildSourceEntry(ctx, zw, file, manifestEntries[file]); err != nil {
			return closeWith(err)
		}
	}

	if err := zw.Close(); err != nil {
		return fmt.Errorf("close rebuilt EPUB: %w", err)
	}
	return nil
}

func validateRebuildEntryName(name string) error {
	if name == "" || strings.ContainsRune(name, '\x00') || strings.HasPrefix(name, "/") || strings.Contains(name, `\`) {
		return fmt.Errorf("EPUB contains unsafe ZIP entry path %q", name)
	}
	segments := strings.Split(name, "/")
	if len(segments[0]) >= 2 && segments[0][1] == ':' && ((segments[0][0] >= 'A' && segments[0][0] <= 'Z') || (segments[0][0] >= 'a' && segments[0][0] <= 'z')) {
		return fmt.Errorf("EPUB contains unsafe ZIP entry path %q", name)
	}
	if slices.Contains(segments, "..") {
		return fmt.Errorf("EPUB contains unsafe ZIP entry path %q", name)
	}
	return nil
}

func readRebuildPackage(ctx context.Context, zr *zip.Reader) (rebuildPackage, error) {
	container, ambiguous := format.ResolveZIPEntry(zr, "META-INF/container.xml")
	if ambiguous {
		return rebuildPackage{}, fmt.Errorf("EPUB container.xml resolves to multiple archive entries")
	}

	var containerRaw []byte
	var declaredErr error
	if container != nil {
		var err error
		containerRaw, err = kepubReadZipFile(ctx, container, maxConverterPackageBytes)
		if err != nil {
			return rebuildPackage{}, fmt.Errorf("read EPUB container.xml: %w", err)
		}
		var doc kepubContainerDoc
		decoder := xml.NewDecoder(bytes.NewReader(containerRaw))
		decoder.CharsetReader = charset.NewReaderLabel
		if err := decoder.Decode(&doc); err != nil {
			declaredErr = fmt.Errorf("parse EPUB container.xml: %w", err)
		} else {
			seen := make(map[string]bool)
			for _, requireStandardMediaType := range []bool{true, false} {
				for _, rootfile := range doc.Rootfiles {
					standard := isKEPUBOPFMediaType(rootfile.MediaType)
					if requireStandardMediaType && !standard {
						continue
					}
					requested := cleanKEPUBHref("", rootfile.FullPath)
					if requested == "" || seen[requested] {
						continue
					}
					seen[requested] = true
					file, err := kepubZipFile(zr, requested)
					if err != nil {
						if declaredErr == nil {
							declaredErr = fmt.Errorf("resolve EPUB OPF %s: %w", requested, err)
						}
						continue
					}
					if file == nil {
						continue
					}
					candidate, err := readRebuildCandidate(ctx, zr, file)
					if err != nil {
						if declaredErr == nil {
							declaredErr = err
						}
						continue
					}
					containerData := containerRaw
					if container.Name != "META-INF/container.xml" || requested != file.Name || !standard {
						containerData = canonicalRebuildContainer(file.Name)
					}
					return rebuildPackage{
						opfPath:               candidate.path,
						opfFile:               candidate.file,
						opfBytes:              candidate.raw,
						containerFile:         container,
						containerData:         containerData,
						repairXHTMLMetaValues: candidate.normalizedXML11,
					}, nil
				}
			}
		}
	}

	candidates, err := scanRebuildOPFs(ctx, zr)
	if err != nil {
		return rebuildPackage{}, err
	}
	if len(candidates) == 0 {
		if declaredErr != nil {
			return rebuildPackage{}, declaredErr
		}
		return rebuildPackage{}, fmt.Errorf("EPUB has no coherent OPF package")
	}
	if len(candidates) != 1 {
		paths := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			paths = append(paths, candidate.path)
		}
		return rebuildPackage{}, fmt.Errorf("EPUB package is ambiguous; coherent OPF candidates: %s", strings.Join(paths, ", "))
	}
	candidate := candidates[0]
	return rebuildPackage{
		opfPath:               candidate.path,
		opfFile:               candidate.file,
		opfBytes:              candidate.raw,
		containerFile:         container,
		containerData:         canonicalRebuildContainer(candidate.path),
		repairXHTMLMetaValues: candidate.normalizedXML11,
	}, nil
}

func scanRebuildOPFs(ctx context.Context, zr *zip.Reader) ([]rebuildCandidate, error) {
	var candidates []rebuildCandidate
	for _, file := range zr.File {
		if err := checkContext(ctx); err != nil {
			return nil, err
		}
		if strings.HasSuffix(file.Name, "/") || !strings.EqualFold(path.Ext(file.Name), ".opf") {
			continue
		}
		candidate, err := readRebuildCandidate(ctx, zr, file)
		if err == nil {
			candidates = append(candidates, candidate)
		}
	}
	return candidates, nil
}

func readRebuildCandidate(ctx context.Context, zr *zip.Reader, file *zip.File) (rebuildCandidate, error) {
	raw, err := kepubReadZipFile(ctx, file, maxConverterPackageBytes)
	if err != nil {
		return rebuildCandidate{}, fmt.Errorf("read EPUB OPF %s: %w", file.Name, err)
	}
	raw, normalizedXML11, err := normalizeRebuildOPF(raw)
	if err != nil {
		return rebuildCandidate{}, fmt.Errorf("repair EPUB OPF %s: %w", file.Name, err)
	}
	var doc rebuildOPFDoc
	if err := format.DecodeOPFXML(raw, &doc); err != nil {
		return rebuildCandidate{}, fmt.Errorf("parse EPUB OPF %s: %w", file.Name, err)
	}
	if doc.XMLName.Local != "package" || len(doc.Manifest.Items) == 0 || len(doc.Spine.Items) == 0 {
		return rebuildCandidate{}, fmt.Errorf("EPUB OPF %s has no coherent manifest and spine", file.Name)
	}
	manifestPaths := make(map[string]string, len(doc.Manifest.Items))
	for _, item := range doc.Manifest.Items {
		if id := strings.TrimSpace(item.ID); id != "" {
			if itemPath := cleanKEPUBHref(file.Name, item.Href); itemPath != "" {
				manifestPaths[id] = itemPath
			}
		}
	}
	matchedSpine := false
	for _, item := range doc.Spine.Items {
		itemPath := manifestPaths[strings.TrimSpace(item.IDRef)]
		if itemPath == "" {
			continue
		}
		entry, err := kepubZipFile(zr, itemPath)
		if err != nil {
			return rebuildCandidate{}, fmt.Errorf("resolve EPUB spine item %s: %w", itemPath, err)
		}
		if entry != nil {
			matchedSpine = true
			break
		}
	}
	if !matchedSpine {
		return rebuildCandidate{}, fmt.Errorf("EPUB OPF %s spine does not resolve to its manifest", file.Name)
	}
	return rebuildCandidate{path: file.Name, file: file, raw: raw, normalizedXML11: normalizedXML11}, nil
}

func normalizeRebuildOPF(raw []byte) ([]byte, bool, error) {
	normalizedXML11 := rebuildXMLVersion11RE.Match(raw)
	decoded, err := format.NormalizeOPFXML(raw)
	if err != nil {
		return nil, false, err
	}
	if int64(len(decoded)) > maxConverterPackageBytes {
		return nil, false, fmt.Errorf("decoded OPF exceeds %d bytes: %w", maxConverterPackageBytes, ErrInputTooLarge)
	}
	decoded = removeInvalidDateSentinels(decoded)
	decoded = removeEmptyTours(decoded)
	decoder := xml.NewDecoder(bytes.NewReader(decoded))
	decoder.CharsetReader = charset.NewReaderLabel
	depth := 0
	rootSeen := false
	rootEnd := -1
	trailingText := false
	trailingOther := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, false, err
		}
		switch token := token.(type) {
		case xml.StartElement:
			if rootEnd >= 0 {
				return nil, false, fmt.Errorf("XML markup follows the closed package root")
			}
			if !rootSeen {
				if token.Name.Local != "package" {
					return nil, false, fmt.Errorf("root element is %s, not package", token.Name.Local)
				}
				rootSeen = true
			}
			depth++
		case xml.EndElement:
			if !rootSeen {
				continue
			}
			depth--
			if depth == 0 {
				rootEnd = int(decoder.InputOffset())
			}
		case xml.CharData:
			if rootEnd >= 0 && len(bytes.TrimSpace(token)) != 0 {
				trailingText = true
			}
		case xml.Comment, xml.Directive, xml.ProcInst:
			if rootEnd >= 0 {
				trailingOther = true
			}
		}
	}
	if !rootSeen {
		return nil, false, fmt.Errorf("package root element not found")
	}
	if rootEnd < 0 {
		return nil, false, fmt.Errorf("package root element is not closed")
	}
	if !trailingText {
		return decoded, normalizedXML11, nil
	}
	if trailingOther {
		return nil, false, fmt.Errorf("mixed text and XML markup follow the closed package root")
	}
	return append(bytes.Clone(decoded[:rootEnd]), '\n'), normalizedXML11, nil
}

func rebuildXMLDeclaredEncoding(raw []byte) string {
	match := rebuildXMLEncodingRE.FindSubmatchIndex(raw)
	if len(match) == 0 {
		return ""
	}
	for _, pair := range [][2]int{{match[2], match[3]}, {match[4], match[5]}} {
		if pair[0] >= 0 {
			return string(raw[pair[0]:pair[1]])
		}
	}
	return ""
}

func rebuildEncodingIsUTF8(label string) bool {
	label = strings.TrimSpace(label)
	return strings.EqualFold(label, "utf-8") || strings.EqualFold(label, "utf8")
}

func canonicalRebuildContainer(opfPath string) []byte {
	var escaped bytes.Buffer
	_ = xml.EscapeText(&escaped, []byte(opfPath))
	return []byte("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n" +
		"<container version=\"1.0\" xmlns=\"urn:oasis:names:tc:opendocument:xmlns:container\">\n" +
		"  <rootfiles>\n" +
		"    <rootfile full-path=\"" + escaped.String() + "\" media-type=\"application/oebps-package+xml\"/>\n" +
		"  </rootfiles>\n" +
		"</container>\n")
}

func rebuildIsRootMimetype(name string) bool {
	return !strings.Contains(name, "/") && strings.EqualFold(strings.TrimSpace(name), "mimetype")
}

func writeRebuildEntry(zw *zip.Writer, name string, data []byte, method uint16) error {
	header := &zip.FileHeader{Name: name, Method: method}
	w, err := zw.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("create rebuilt EPUB entry %s: %w", name, err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write rebuilt EPUB entry %s: %w", name, err)
	}
	return nil
}

func writeRebuildSourceEntry(ctx context.Context, zw *zip.Writer, file *zip.File, packageEntry bool) error {
	header := file.FileHeader
	header.Name = file.Name
	if packageEntry && utf8.ValidString(header.Name) && utf8.ValidString(header.Comment) {
		header.NonUTF8 = false
	}
	header.CRC32 = 0
	header.CompressedSize = 0
	header.CompressedSize64 = 0
	header.UncompressedSize = 0
	header.UncompressedSize64 = 0
	// archive/zip appends an extended timestamp whenever Modified is non-zero.
	// Keep the producer's existing extra fields, but do not append another copy
	// on every rebuild pass.
	header.Modified = time.Time{}

	dst, err := zw.CreateHeader(&header)
	if err != nil {
		return fmt.Errorf("create rebuilt EPUB entry %s: %w", file.Name, err)
	}
	src, err := file.Open()
	if err != nil {
		return fmt.Errorf("open EPUB entry %s: %w", file.Name, err)
	}
	defer src.Close()
	if err := copyContext(ctx, dst, src); err != nil {
		return fmt.Errorf("copy EPUB entry %s: %w", file.Name, err)
	}
	return nil
}
