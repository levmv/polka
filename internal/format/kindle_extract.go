package format

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

const (
	maxKindleExtractTextBytes     int64 = 128 << 20
	maxKindleExtractResourceBytes int64 = 256 << 20
	maxKindleExtractResources           = 4096
	maxKindleGuideReferences            = 128
)

var kindleFlowReferenceRE = regexp.MustCompile(`(?i)kindle:flow:([0-9A-V]+)\?mime=([a-z0-9.+-]+/[a-z0-9.+-]+)`)

// ErrUnsupportedKindleSource marks a Kindle-family source that the native
// extraction layer intentionally refuses instead of returning partial content.
var ErrUnsupportedKindleSource = errors.New("unsupported Kindle source")

// ErrKindleResourceLimit marks a structurally valid Kindle container whose
// combined retained resources exceed the native extraction boundary.
var ErrKindleResourceLimit = errors.New("Kindle resources exceed extraction limit")

type kindleResourceBudget struct {
	maxBytes     int64
	maxResources int
	bytes        int64
	resources    int
}

func newKindleResourceBudget() *kindleResourceBudget {
	return &kindleResourceBudget{
		maxBytes:     maxKindleExtractResourceBytes,
		maxResources: maxKindleExtractResources,
	}
}

func (b *kindleResourceBudget) add(data []byte) error {
	if b == nil || len(data) == 0 {
		return nil
	}
	if b.resources >= b.maxResources {
		return fmt.Errorf("Kindle resource count exceeds limit (%d): %w", b.maxResources, ErrKindleResourceLimit)
	}
	if int64(len(data)) > b.maxBytes-b.bytes {
		return fmt.Errorf("Kindle resource data exceeds limit (%d bytes): %w", b.maxBytes, ErrKindleResourceLimit)
	}
	b.resources++
	b.bytes += int64(len(data))
	return nil
}

// KindleDocument is the internal read model produced by native Kindle-family
// extraction. It is intentionally independent of EPUB writing so parser and
// writer support can mature in small slices.
type KindleDocument struct {
	SourceClass         string
	MOBIKind            MOBIKind
	Metadata            *Metadata
	Flows               []KindleTextFlow
	Resources           []KindleResource
	CoverResourceID     string
	Navigation          []KindleNavItem
	Guide               []KindleGuideReference
	UnsupportedFeatures []string
}

type KindleTextFlow struct {
	ID        string
	Href      string
	MediaType string
	Data      []byte
}

type KindleResource struct {
	ID          string
	Href        string
	MediaType   string
	Data        []byte
	EmbedIndex  int
	FlowIndex   int
	RecordIndex int
	Cover       bool
}

type KindleNavItem struct {
	Label    string
	Href     string
	Children []KindleNavItem
}

type KindleGuideReference struct {
	Type  string
	Title string
	Href  string
}

// ExtractKindleDocument extracts the currently supported native Kindle read
// model. Supported slices stay narrow and fail closed when a source needs
// structure Polka does not yet parse.
func ExtractKindleDocument(r io.ReaderAt, size int64, kind Format) (*KindleDocument, error) {
	info, err := InspectKindle(r, size, kind)
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, fmt.Errorf("%w: unrecognized Kindle container", ErrUnsupportedKindleSource)
	}
	if err := kindleExtractionSupportError(info); err != nil {
		return nil, err
	}
	if info.Container == kindlePalmDBContainerPalmDOC {
		return extractPalmDOCDocument(r, size, info)
	}
	if info.SourceClass == string(MOBIKindCombo) {
		return extractComboKF8Document(r, size, info)
	}
	if info.SourceClass == string(MOBIKindKF8Standalone) {
		return extractKF8StandaloneDocument(r, size, info)
	}

	ranges, ok := mobiRecordRanges(r, size)
	if !ok || len(ranges) == 0 {
		return nil, fmt.Errorf("invalid MOBI record table")
	}
	record0, ok := mobiReadRecord(r, ranges[0], maxMOBIRecord0Bytes)
	if !ok {
		return nil, fmt.Errorf("invalid MOBI record 0")
	}

	meta, err := ExtractMOBIMetadata(r, size)
	if err != nil {
		return nil, err
	}
	text, err := extractPalmTextFlow(r, ranges, info, "flow-0001", "text/flow-0001.html", "text/html")
	if err != nil {
		return nil, err
	}
	resources, coverID, err := extractMOBIResources(r, ranges, record0, info, 0, newKindleResourceBudget())
	if err != nil {
		return nil, err
	}
	nav, err := extractKindleNCXNavigation(ranges, r, info, text.Href)
	if err != nil {
		return nil, fmt.Errorf("extract MOBI NCX navigation: %w", err)
	}

	return &KindleDocument{
		SourceClass:         info.SourceClass,
		MOBIKind:            info.MOBIKind,
		Metadata:            meta,
		Flows:               []KindleTextFlow{text},
		Resources:           resources,
		CoverResourceID:     coverID,
		Navigation:          nav,
		Guide:               extractMOBI6GuideReferences(text),
		UnsupportedFeatures: append([]string(nil), info.UnsupportedFeatures...),
	}, nil
}

func kindleExtractionSupportError(info *KindleInspection) error {
	if info.Container != kindlePalmDBContainerMOBI && info.Container != kindlePalmDBContainerPalmDOC {
		return fmt.Errorf("%w: container %s", ErrUnsupportedKindleSource, info.Container)
	}
	switch info.SourceClass {
	case string(MOBIKindMOBI6), string(MOBIKindPalmDOC):
	case string(MOBIKindKF8Standalone):
		return kindleKF8ExtractionSupportError(info)
	case string(MOBIKindCombo):
		return kindleComboExtractionSupportError(info)
	default:
		return fmt.Errorf("%w: source class %s", ErrUnsupportedKindleSource, info.SourceClass)
	}
	if err := kindleCommonSupportError(info); err != nil {
		return err
	}
	if info.TextRecords == 0 {
		return fmt.Errorf("%w: no MOBI text records", ErrUnsupportedKindleSource)
	}
	if info.TextLength > uint32(maxKindleExtractTextBytes) {
		return fmt.Errorf("MOBI text exceeds limit (%d bytes): %w", maxKindleExtractTextBytes, ErrTextTooLarge)
	}
	if info.ResourceCounts.Fonts > 0 {
		return fmt.Errorf("%w: font resources", ErrUnsupportedKindleSource)
	}
	return nil
}

func kindleKF8ExtractionSupportError(info *KindleInspection) error {
	if err := kindleCommonSupportError(info); err != nil {
		return err
	}
	if info.TextRecords == 0 {
		return fmt.Errorf("%w: no KF8 text records", ErrUnsupportedKindleSource)
	}
	if info.TextLength > uint32(maxKindleExtractTextBytes) {
		return fmt.Errorf("KF8 text exceeds limit (%d bytes): %w", maxKindleExtractTextBytes, ErrTextTooLarge)
	}
	if len(info.KF8Skeletons) == 0 {
		return fmt.Errorf("%w: missing KF8 skeleton table", ErrUnsupportedKindleSource)
	}
	return nil
}

func kindleComboExtractionSupportError(info *KindleInspection) error {
	if err := kindleCommonSupportError(info); err != nil {
		return err
	}
	if info.BoundaryIndex == 0 {
		return fmt.Errorf("%w: missing combo KF8 boundary", ErrUnsupportedKindleSource)
	}
	return nil
}

func kindleUnsupportedFeatureError(info *KindleInspection) error {
	if len(info.UnsupportedFeatures) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrUnsupportedKindleSource, strings.Join(info.UnsupportedFeatures, ", "))
}

// kindleCommonSupportError applies the fail-closed checks shared by every
// supported Kindle source class: unsupported feature flags, text compression,
// and cdetype.
func kindleCommonSupportError(info *KindleInspection) error {
	if err := kindleUnsupportedFeatureError(info); err != nil {
		return err
	}
	if err := kindleTextCompressionSupportError(info); err != nil {
		return err
	}
	if err := kindleCDETypeSupportError(info); err != nil {
		return err
	}
	return nil
}

func kindleTextCompressionSupportError(info *KindleInspection) error {
	switch info.Compression {
	case mobiCompressionNone, mobiCompressionPalmDOC, mobiCompressionHUFFCDIC:
		return nil
	default:
		compressionName := info.CompressionName
		if compressionName == "" {
			compressionName = fmt.Sprintf("%d", info.Compression)
		}
		return fmt.Errorf("%w: compression %s", ErrUnsupportedKindleSource, compressionName)
	}
}

func kindleCDETypeSupportError(info *KindleInspection) error {
	if info.CDEType != "" && info.CDEType != "EBOK" {
		return fmt.Errorf("%w: cdetype %s", ErrUnsupportedKindleSource, info.CDEType)
	}
	return nil
}

func extractPalmDOCDocument(r io.ReaderAt, size int64, info *KindleInspection) (*KindleDocument, error) {
	ranges, ok := palmDBRecordRanges(r, size, palmDOCHeader)
	if !ok || len(ranges) == 0 {
		return nil, fmt.Errorf("invalid PalmDOC record table")
	}
	meta, err := ExtractPDBMetadata(r, size)
	if err != nil {
		return nil, err
	}
	text, err := extractPalmTextFlow(r, ranges, info, "flow-0001", "text/flow-0001.txt", "text/plain")
	if err != nil {
		return nil, err
	}
	var resources []KindleResource
	var guide []KindleGuideReference
	if palmDOCFlowIsHTML(text.Data) {
		text.Href = "text/flow-0001.html"
		text.MediaType = "text/html"
		guide = extractMOBI6GuideReferences(text)
		text.Data = palmDOCContentHTML(text.Data)
		resources, err = extractPalmDOCImageResources(r, ranges, info, newKindleResourceBudget())
		if err != nil {
			return nil, err
		}
	}
	return &KindleDocument{
		SourceClass:         info.SourceClass,
		MOBIKind:            info.MOBIKind,
		Metadata:            meta,
		Flows:               []KindleTextFlow{text},
		Resources:           resources,
		Guide:               guide,
		UnsupportedFeatures: append([]string(nil), info.UnsupportedFeatures...),
	}, nil
}

func extractPalmDOCImageResources(r io.ReaderAt, ranges []mobiRecordRange, info *KindleInspection, budget *kindleResourceBudget) ([]KindleResource, error) {
	start := 1 + int(info.TextRecords)
	if start >= len(ranges) {
		return nil, nil
	}
	var resources []KindleResource
	for i := start; i < len(ranges); i++ {
		raw, ok := mobiReadRecord(r, ranges[i], maxMOBICoverBytes)
		if !ok {
			continue
		}
		data, ext, mediaType, ok := palmDOCImageResource(raw)
		if !ok {
			continue
		}
		if err := budget.add(data); err != nil {
			return nil, err
		}
		resources = append(resources, KindleResource{
			ID:          fmt.Sprintf("res-%05d", i),
			Href:        kindleResourceHref(i, ext, mediaType),
			MediaType:   mediaType,
			Data:        data,
			EmbedIndex:  i - start + 1,
			RecordIndex: i,
		})
	}
	return resources, nil
}

func extractKF8StandaloneDocument(r io.ReaderAt, size int64, info *KindleInspection) (*KindleDocument, error) {
	ranges, ok := mobiRecordRanges(r, size)
	if !ok || len(ranges) == 0 {
		return nil, fmt.Errorf("invalid MOBI record table")
	}
	record0, ok := mobiReadRecord(r, ranges[0], maxMOBIRecord0Bytes)
	if !ok {
		return nil, fmt.Errorf("invalid MOBI record 0")
	}
	meta, err := ExtractMOBIMetadata(r, size)
	if err != nil {
		return nil, err
	}
	return extractKF8DocumentFromRanges(r, ranges, record0, info, meta, info.SourceClass, info.MOBIKind, 0, newKindleResourceBudget())
}

func extractComboKF8Document(r io.ReaderAt, size int64, info *KindleInspection) (*KindleDocument, error) {
	ranges, ok := mobiRecordRanges(r, size)
	if !ok || len(ranges) == 0 {
		return nil, fmt.Errorf("invalid MOBI record table")
	}
	start := int(info.BoundaryIndex)
	if start <= 0 || start >= len(ranges) {
		return nil, fmt.Errorf("%w: invalid combo KF8 boundary", ErrUnsupportedKindleSource)
	}
	boundary, ok := mobiReadRecord(r, ranges[start-1], 64)
	if !ok || !bytes.Equal(boundary, []byte("BOUNDARY")) {
		return nil, fmt.Errorf("%w: invalid combo KF8 boundary", ErrUnsupportedKindleSource)
	}
	kf8Ranges := ranges[start:]
	kf8Record0, ok := mobiReadRecord(r, kf8Ranges[0], maxMOBIRecord0Bytes)
	if !ok || len(kf8Record0) < 20 || !bytes.Equal(kf8Record0[16:20], []byte("MOBI")) {
		return nil, fmt.Errorf("%w: invalid combo KF8 header", ErrUnsupportedKindleSource)
	}
	kf8Info := inspectKF8RecordSet(r, kf8Ranges, kf8Record0, info.TypeCreator)
	if err := kindleKF8ExtractionSupportError(kf8Info); err != nil {
		return nil, err
	}
	meta, err := ExtractMOBIMetadata(r, size)
	if err != nil {
		return nil, err
	}
	resourceBudget := newKindleResourceBudget()
	primaryRecord0, ok := mobiReadRecord(r, ranges[0], maxMOBIRecord0Bytes)
	if !ok {
		return nil, fmt.Errorf("invalid combo MOBI record 0")
	}
	// Combo files may keep the resource table in the primary record set while
	// KF8 markup after BOUNDARY still addresses it by embed index.
	sharedResources, sharedCoverID, err := extractMOBIResources(r, ranges[:start-1], primaryRecord0, info, 0, resourceBudget)
	if err != nil {
		return nil, err
	}
	doc, err := extractKF8DocumentFromRanges(r, kf8Ranges, kf8Record0, kf8Info, meta, info.SourceClass, info.MOBIKind, start, resourceBudget)
	if err != nil {
		return nil, err
	}
	doc.Resources = append(sharedResources, doc.Resources...)
	if sharedCoverID != "" {
		doc.CoverResourceID = sharedCoverID
	}
	return doc, nil
}

func inspectKF8RecordSet(r io.ReaderAt, ranges []mobiRecordRange, record0 []byte, typeCreator string) *KindleInspection {
	info := &KindleInspection{
		Container:   kindlePalmDBContainerMOBI,
		TypeCreator: typeCreator,
		MOBIKind:    MOBIKindKF8Standalone,
		RecordCount: len(ranges),
		SourceClass: string(MOBIKindKF8Standalone),
	}
	applyPalmDOCHeader(info, record0)
	applyMOBIHeader(info, record0)
	applyKindleRecordSetDiagnostics(info, r, ranges)
	info.UnsupportedFeatures = kindleUnsupportedFeatures(info)
	return info
}

func extractKF8DocumentFromRanges(r io.ReaderAt, ranges []mobiRecordRange, record0 []byte, info *KindleInspection, meta *Metadata, sourceClass string, mobiKind MOBIKind, recordIndexBase int, resourceBudget *kindleResourceBudget) (*KindleDocument, error) {
	raw, err := extractPalmTextData(r, ranges, info)
	if err != nil {
		return nil, err
	}
	text, err := assembleKF8Text(raw, info)
	if err != nil {
		return nil, err
	}
	resources, coverID, err := extractMOBIResources(r, ranges, record0, info, recordIndexBase, resourceBudget)
	if err != nil {
		return nil, err
	}
	flowResources, err := extractKF8FlowResources(raw, info, text, resourceBudget)
	if err != nil {
		return nil, err
	}
	resources = append(resources, flowResources...)
	nav, err := extractKindleNCXNavigation(ranges, r, info, text.Href)
	if err != nil {
		return nil, fmt.Errorf("extract KF8 NCX navigation: %w", err)
	}
	return &KindleDocument{
		SourceClass:         sourceClass,
		MOBIKind:            mobiKind,
		Metadata:            meta,
		Flows:               []KindleTextFlow{text},
		Resources:           resources,
		CoverResourceID:     coverID,
		Navigation:          nav,
		UnsupportedFeatures: append([]string(nil), info.UnsupportedFeatures...),
	}, nil
}

func extractKF8FlowResources(raw []byte, info *KindleInspection, text KindleTextFlow, budget *kindleResourceBudget) ([]KindleResource, error) {
	if len(raw) == 0 || len(info.FDSTSections) < 2 || len(text.Data) == 0 {
		return nil, nil
	}
	referenced := kindleFlowReferences(text.Data, info)
	processed := make(map[int]bool)
	var resources []KindleResource
	for {
		added := false
		for flow := 1; flow < len(info.FDSTSections); flow++ {
			mediaType, ok := referenced[flow]
			if !ok || processed[flow] {
				continue
			}
			processed[flow] = true
			section := info.FDSTSections[flow]
			if section.Start > section.End || section.End > uint32(len(raw)) {
				continue
			}
			data := []byte(mobiDecode(raw[section.Start:section.End], info.Codepage))
			resource, ok := kindleFDSTFlowResource(flow, mediaType, data)
			if !ok {
				continue
			}
			if err := budget.add(resource.Data); err != nil {
				return nil, err
			}
			resources = append(resources, resource)
			added = true
			if strings.EqualFold(mediaType, "text/css") {
				for nestedFlow, nestedMediaType := range kindleFlowReferences(resource.Data, info) {
					if _, exists := referenced[nestedFlow]; !exists {
						referenced[nestedFlow] = nestedMediaType
					}
				}
			}
		}
		if !added {
			break
		}
	}
	return resources, nil
}

func kindleFlowReferences(data []byte, info *KindleInspection) map[int]string {
	referenced := make(map[int]string)
	for _, match := range kindleFlowReferenceRE.FindAllSubmatch(data, -1) {
		if len(match) != 3 {
			continue
		}
		flow, err := strconv.ParseUint(strings.ToUpper(string(match[1])), 32, 32)
		mediaType := strings.ToLower(string(match[2]))
		if err != nil || flow == 0 || int(flow) >= len(info.FDSTSections) || !kindleSupportedFDSTFlowMediaType(mediaType) {
			continue
		}
		referenced[int(flow)] = mediaType
	}
	return referenced
}

func kindleSupportedFDSTFlowMediaType(mediaType string) bool {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "text/css", "image/svg+xml":
		return true
	default:
		return false
	}
}

func kindleFDSTFlowResource(flow int, mediaType string, data []byte) (KindleResource, bool) {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "text/css":
		return KindleResource{
			ID:        fmt.Sprintf("style-%04d", flow),
			Href:      fmt.Sprintf("styles/flow-%04d.css", flow),
			MediaType: "text/css",
			Data:      data,
			FlowIndex: flow,
		}, true
	case "image/svg+xml":
		href := fmt.Sprintf("images/flow-%04d.svg", flow)
		svg, mediaType, _, ok := EPUBImageResource(data, href)
		if !ok || mediaType != "image/svg+xml" {
			return KindleResource{}, false
		}
		return KindleResource{
			ID:        fmt.Sprintf("svg-%04d", flow),
			Href:      href,
			MediaType: mediaType,
			Data:      svg,
			FlowIndex: flow,
		}, true
	default:
		return KindleResource{}, false
	}
}

func extractPalmTextFlow(r io.ReaderAt, ranges []mobiRecordRange, info *KindleInspection, id, href, mediaType string) (KindleTextFlow, error) {
	data, err := extractPalmTextData(r, ranges, info)
	if err != nil {
		return KindleTextFlow{}, err
	}
	text := mobiDecode(data, info.Codepage)
	return KindleTextFlow{
		ID:        id,
		Href:      href,
		MediaType: mediaType,
		Data:      []byte(text),
	}, nil
}

func extractPalmTextData(r io.ReaderAt, ranges []mobiRecordRange, info *KindleInspection) ([]byte, error) {
	end := 1 + int(info.TextRecords)
	if end > len(ranges) {
		return nil, fmt.Errorf("Palm text records exceed record table")
	}
	var huff *kindleHUFFCDICDecoder
	if info.Compression == mobiCompressionHUFFCDIC {
		var err error
		huff, err = newKindleHUFFCDICDecoder(r, ranges, info)
		if err != nil {
			return nil, err
		}
	}
	var out bytes.Buffer
	for i := 1; i < end; i++ {
		raw, ok := mobiReadRecord(r, ranges[i], maxKindleExtractTextBytes)
		if !ok {
			return nil, fmt.Errorf("read MOBI text record %d", i)
		}
		var err error
		raw, err = mobiTrimTrailingEntries(raw, info.TrailingFlags)
		if err != nil {
			return nil, fmt.Errorf("trim MOBI text record %d: %w", i, err)
		}
		remaining := maxKindleExtractTextBytes - int64(out.Len())
		if remaining < 0 {
			return nil, fmt.Errorf("MOBI text exceeds limit (%d bytes): %w", maxKindleExtractTextBytes, ErrTextTooLarge)
		}
		var decoded []byte
		switch info.Compression {
		case mobiCompressionNone:
			if int64(len(raw)) > remaining {
				return nil, fmt.Errorf("MOBI text exceeds limit (%d bytes): %w", maxKindleExtractTextBytes, ErrTextTooLarge)
			}
			decoded = raw
		case mobiCompressionPalmDOC:
			decoded, err = palmDOCDecompress(raw, remaining)
			if err != nil {
				return nil, fmt.Errorf("decompress MOBI text record %d: %w", i, err)
			}
		case mobiCompressionHUFFCDIC:
			decoded, err = huff.decompress(raw, remaining)
			if err != nil {
				return nil, fmt.Errorf("decompress HUFF/CDIC text record %d: %w", i, err)
			}
		default:
			return nil, fmt.Errorf("unsupported MOBI compression %d", info.Compression)
		}
		out.Write(decoded)
	}

	data := out.Bytes()
	if info.TextLength > 0 {
		want := int(info.TextLength)
		if len(data) < want {
			return nil, fmt.Errorf("MOBI text truncated: got %d bytes, want %d", len(data), want)
		}
		data = data[:want]
	}
	return data, nil
}

func assembleKF8Text(raw []byte, info *KindleInspection) (KindleTextFlow, error) {
	var out bytes.Buffer
	for _, skeleton := range info.KF8Skeletons {
		end64 := uint64(skeleton.Start) + uint64(skeleton.Length)
		if end64 > uint64(len(raw)) {
			return KindleTextFlow{}, fmt.Errorf("KF8 skeleton %d outside text bounds", skeleton.Index)
		}
		start := int(skeleton.Start)
		end := int(end64)
		body := append([]byte(nil), raw[start:end]...)
		fragments := kindleFragmentsForSkeleton(info.KF8Fragments, uint32(skeleton.Index), skeleton.FragmentCount)
		for _, fragment := range fragments {
			fragmentStart64 := uint64(skeleton.Start) + uint64(skeleton.Length) + uint64(fragment.Start)
			fragmentEnd64 := fragmentStart64 + uint64(fragment.Length)
			if fragmentEnd64 > uint64(len(raw)) {
				return KindleTextFlow{}, fmt.Errorf("KF8 fragment %d outside text bounds", fragment.Sequence)
			}
			if fragment.InsertOffset < skeleton.Start {
				return KindleTextFlow{}, fmt.Errorf("KF8 fragment %d insert offset outside skeleton", fragment.Sequence)
			}
			fragmentStart := int(fragmentStart64)
			fragmentEnd := int(fragmentEnd64)
			insertOffset := int(uint64(fragment.InsertOffset) - uint64(skeleton.Start))
			if insertOffset < 0 || insertOffset > len(body) {
				return KindleTextFlow{}, fmt.Errorf("KF8 fragment %d insert offset outside skeleton", fragment.Sequence)
			}
			part := raw[fragmentStart:fragmentEnd]
			next := make([]byte, 0, len(body)+len(part))
			next = append(next, body[:insertOffset]...)
			next = append(next, part...)
			next = append(next, body[insertOffset:]...)
			body = next
		}
		out.Write(body)
	}
	text := mobiDecode(out.Bytes(), info.Codepage)
	return KindleTextFlow{
		ID:        "flow-0001",
		Href:      "text/flow-0001.html",
		MediaType: "text/html",
		Data:      []byte(text),
	}, nil
}

func kindleFragmentsForSkeleton(fragments []KindleKF8Fragment, fileNumber uint32, limit uint32) []KindleKF8Fragment {
	if limit == 0 {
		return nil
	}
	var out []KindleKF8Fragment
	for _, fragment := range fragments {
		if fragment.FileNumber != fileNumber {
			continue
		}
		out = append(out, fragment)
		if uint32(len(out)) >= limit {
			break
		}
	}
	return out
}

func extractMOBI6GuideReferences(flow KindleTextFlow) []KindleGuideReference {
	if len(flow.Data) == 0 {
		return nil
	}
	z := html.NewTokenizer(bytes.NewReader(flow.Data))
	guideDepth := 0
	seen := make(map[string]bool)
	var refs []KindleGuideReference

	for len(refs) < maxKindleGuideReferences {
		typ := z.Next()
		switch typ {
		case html.ErrorToken:
			return refs
		case html.StartTagToken, html.SelfClosingTagToken:
			token := z.Token()
			if strings.EqualFold(token.Data, "guide") {
				if typ != html.SelfClosingTagToken {
					guideDepth++
				}
				continue
			}
			if guideDepth == 0 || !strings.EqualFold(token.Data, "reference") {
				continue
			}
			ref, ok := kindleGuideReferenceFromAttrs(token.Attr, flow.Href)
			if !ok {
				continue
			}
			key := strings.ToLower(ref.Type) + "\x00" + ref.Href
			if seen[key] {
				continue
			}
			seen[key] = true
			refs = append(refs, ref)
		case html.EndTagToken:
			token := z.Token()
			if strings.EqualFold(token.Data, "guide") && guideDepth > 0 {
				guideDepth--
			}
		}
	}
	return refs
}

func kindleGuideReferenceFromAttrs(attrs []html.Attribute, flowHref string) (KindleGuideReference, bool) {
	typ := strings.ToLower(mobiCleanString(kindleHTMLAttr(attrs, "type")))
	title := mobiCleanString(kindleHTMLAttr(attrs, "title"))
	href := kindleGuideHref(flowHref, kindleHTMLAttr(attrs, "filepos"), kindleHTMLAttr(attrs, "href"))
	if href == "" || (typ == "" && title == "") {
		return KindleGuideReference{}, false
	}
	if title == "" {
		title = typ
	}
	return KindleGuideReference{
		Type:  typ,
		Title: title,
		Href:  href,
	}, true
}

func kindleGuideHref(flowHref, filepos, href string) string {
	filepos = strings.TrimSpace(filepos)
	if filepos != "" {
		pos, err := strconv.ParseUint(filepos, 10, 32)
		if err == nil {
			return fmt.Sprintf("%s#filepos%d", flowHref, pos)
		}
	}

	href = strings.TrimSpace(href)
	if href == "" || strings.ContainsAny(href, "\x00\r\n") {
		return ""
	}
	if strings.HasPrefix(href, "#") {
		return flowHref + href
	}
	return href
}

func kindleHTMLAttr(attrs []html.Attribute, name string) string {
	for _, attr := range attrs {
		if strings.EqualFold(attr.Key, name) {
			return attr.Val
		}
	}
	return ""
}

func mobiTrimTrailingEntries(data []byte, flags uint16) ([]byte, error) {
	trailers := mobiTrailingEntryCount(flags)
	for range trailers {
		length := mobiTrailingEntryLength(data)
		if length <= 0 || length > len(data) {
			return nil, fmt.Errorf("invalid trailing entry length %d", length)
		}
		data = data[:len(data)-length]
	}
	if flags&1 != 0 {
		if len(data) == 0 {
			return nil, fmt.Errorf("missing multibyte trailing data")
		}
		length := int(data[len(data)-1]&0x03) + 1
		if length > len(data) {
			return nil, fmt.Errorf("invalid multibyte trailing length %d", length)
		}
		data = data[:len(data)-length]
	}
	return data, nil
}

func mobiTrailingEntryCount(flags uint16) int {
	count := 0
	for flags >>= 1; flags > 0; flags >>= 1 {
		if flags&1 != 0 {
			count++
		}
	}
	return count
}

func mobiTrailingEntryLength(data []byte) int {
	start := max(len(data)-4, 0)
	value := 0
	for _, b := range data[start:] {
		if b&0x80 != 0 {
			value = 0
		}
		value = (value << 7) | int(b&0x7f)
	}
	return value
}

func palmDOCDecompress(data []byte, maxBytes int64) ([]byte, error) {
	var out []byte
	appendByte := func(b byte) error {
		if maxBytes >= 0 && int64(len(out)+1) > maxBytes {
			return fmt.Errorf("decompressed text exceeds limit (%d bytes): %w", maxBytes, ErrTextTooLarge)
		}
		out = append(out, b)
		return nil
	}
	appendBytes := func(raw []byte) error {
		if maxBytes >= 0 && int64(len(out)+len(raw)) > maxBytes {
			return fmt.Errorf("decompressed text exceeds limit (%d bytes): %w", maxBytes, ErrTextTooLarge)
		}
		out = append(out, raw...)
		return nil
	}

	for i := 0; i < len(data); {
		c := data[i]
		i++
		switch {
		case c == 0:
			if err := appendByte(0); err != nil {
				return nil, err
			}
		case c <= 8:
			if i+int(c) > len(data) {
				return nil, fmt.Errorf("literal run overruns record")
			}
			if err := appendBytes(data[i : i+int(c)]); err != nil {
				return nil, err
			}
			i += int(c)
		case c <= 0x7f:
			if err := appendByte(c); err != nil {
				return nil, err
			}
		case c <= 0xbf:
			if i >= len(data) {
				return nil, fmt.Errorf("back-reference overruns record")
			}
			token := uint16(c)<<8 | uint16(data[i])
			i++
			distance := int((token >> 3) & 0x07ff)
			length := int(token&0x0007) + 3
			if distance == 0 || distance > len(out) {
				return nil, fmt.Errorf("invalid back-reference distance %d", distance)
			}
			for range length {
				if err := appendByte(out[len(out)-distance]); err != nil {
					return nil, err
				}
			}
		default:
			if err := appendByte(' '); err != nil {
				return nil, err
			}
			if err := appendByte(c ^ 0x80); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func extractMOBIResources(r io.ReaderAt, ranges []mobiRecordRange, record0 []byte, info *KindleInspection, recordIndexBase int, budget *kindleResourceBudget) ([]KindleResource, string, error) {
	start := kindleFirstResourceRecord(info)
	if start < 1 || start >= len(ranges) {
		return nil, "", nil
	}
	var resources []KindleResource
	byRecord := make(map[uint32]int)
	for i := start; i < len(ranges); i++ {
		record, ok := mobiReadRecord(r, ranges[i], maxMOBICoverBytes)
		if !ok {
			continue
		}
		data := record
		ext, mediaType, ok := kindleImageResourceType(record)
		if !ok {
			var err error
			data, ext, mediaType, ok, err = kindleFontResource(record)
			if err != nil {
				return nil, "", fmt.Errorf("extract Kindle font record %d: %w", i, err)
			}
		}
		if !ok {
			data, ext, mediaType, ok = kindleMediaResource(record)
			if !ok && kindleMediaEnvelope(record) {
				return nil, "", fmt.Errorf("%w: invalid or unsupported media record %d", ErrUnsupportedKindleSource, i)
			}
		}
		if len(data) == 0 {
			continue
		}
		if !ok {
			continue
		}
		if err := budget.add(data); err != nil {
			return nil, "", err
		}
		recordIndex := recordIndexBase + i
		id := fmt.Sprintf("res-%05d", recordIndex)
		switch {
		case strings.HasPrefix(mediaType, "font/"):
			id = fmt.Sprintf("font-%05d", recordIndex)
		case strings.HasPrefix(mediaType, "audio/"):
			id = fmt.Sprintf("audio-%05d", recordIndex)
		case strings.HasPrefix(mediaType, "video/"):
			id = fmt.Sprintf("video-%05d", recordIndex)
		}
		if strings.HasPrefix(mediaType, "image/") {
			byRecord[uint32(i)] = len(resources)
		}
		resources = append(resources, KindleResource{
			ID:          id,
			Href:        kindleResourceHref(recordIndex, ext, mediaType),
			MediaType:   mediaType,
			Data:        data,
			EmbedIndex:  i - start + 1,
			RecordIndex: recordIndex,
		})
	}

	for _, candidate := range mobiCoverRecordCandidates(record0, info.FirstResourceIndex) {
		if pos, ok := byRecord[candidate]; ok {
			resources[pos].Cover = true
			return resources, resources[pos].ID, nil
		}
	}
	return resources, "", nil
}

func kindleResourceHref(recordIndex int, ext, mediaType string) string {
	switch {
	case strings.HasPrefix(mediaType, "font/"):
		return fmt.Sprintf("fonts/%05d%s", recordIndex, ext)
	case strings.HasPrefix(mediaType, "audio/"), strings.HasPrefix(mediaType, "video/"):
		return fmt.Sprintf("media/%05d%s", recordIndex, ext)
	default:
		return fmt.Sprintf("images/%05d%s", recordIndex, ext)
	}
}

func kindleImageResourceType(data []byte) (string, string, bool) {
	ext, ok := mobiImageExtension(data)
	if !ok {
		return "", "", false
	}
	mediaType, ok := kindleImageMediaTypeForExtension(ext)
	return ext, mediaType, ok
}

func kindleMediaEnvelope(data []byte) bool {
	return bytes.HasPrefix(data, []byte("AUDI")) || bytes.HasPrefix(data, []byte("VIDE"))
}

func kindleMediaResource(data []byte) ([]byte, string, string, bool) {
	if len(data) < 12 || !kindleMediaEnvelope(data) {
		return nil, "", "", false
	}
	offset := int(binary.BigEndian.Uint32(data[4:8]))
	if offset < 12 || offset >= len(data) {
		return nil, "", "", false
	}
	payload := data[offset:]
	switch {
	case bytes.HasPrefix(data, []byte("AUDI")) && kindleMP3Resource(payload):
		return payload, ".mp3", "audio/mpeg", true
	case bytes.HasPrefix(data, []byte("VIDE")) && kindleMP4Resource(payload):
		return payload, ".mp4", "video/mp4", true
	case bytes.HasPrefix(data, []byte("VIDE")) && kindleMPEGVideoResource(payload):
		return payload, ".mpg", "video/mpeg", true
	default:
		return nil, "", "", false
	}
}

func kindleMP3Resource(data []byte) bool {
	return bytes.HasPrefix(data, []byte("ID3")) || len(data) >= 2 && data[0] == 0xff && data[1]&0xe0 == 0xe0
}

func kindleMP4Resource(data []byte) bool {
	return len(data) >= 12 && bytes.Equal(data[4:8], []byte("ftyp"))
}

func kindleMPEGVideoResource(data []byte) bool {
	return len(data) >= 4 && data[0] == 0 && data[1] == 0 && data[2] == 1 && (data[3] == 0xba || data[3] == 0xb3)
}

func kindleFontResource(data []byte) ([]byte, string, string, bool, error) {
	if len(data) == 0 {
		return nil, "", "", false, nil
	}
	if bytes.HasPrefix(data, []byte("FONT")) {
		return kindleWrappedFontResource(data)
	}
	ext, mediaType, ok := kindleFontMediaType(data)
	return data, ext, mediaType, ok, nil
}

func kindleWrappedFontResource(data []byte) ([]byte, string, string, bool, error) {
	if len(data) < 24 {
		return nil, "", "", false, fmt.Errorf("short FONT header")
	}
	uncompressedSize := kindleUint32(data, 4)
	flags := kindleUint32(data, 8)
	dataStart := int(kindleUint32(data, 12))
	xorLength := int(kindleUint32(data, 16))
	xorStart := int(kindleUint32(data, 20))
	if dataStart < 24 || dataStart > len(data) {
		return nil, "", "", false, fmt.Errorf("invalid FONT data offset %d", dataStart)
	}
	fontData := append([]byte(nil), data[dataStart:]...)
	if flags&0x0002 != 0 {
		if xorLength <= 0 || xorStart < 0 || xorStart+xorLength > len(data) {
			return nil, "", "", false, fmt.Errorf("invalid FONT xor range %d..%d", xorStart, xorStart+xorLength)
		}
		key := data[xorStart : xorStart+xorLength]
		for i, end := 0, min(len(fontData), 1040); i < end; i++ {
			fontData[i] ^= key[i%xorLength]
		}
	}
	if flags&0x0001 != 0 {
		if uncompressedSize > uint32(maxMOBICoverBytes) {
			return nil, "", "", false, fmt.Errorf("FONT payload exceeds limit (%d bytes): %w", maxMOBICoverBytes, ErrTextTooLarge)
		}
		zr, err := zlib.NewReader(bytes.NewReader(fontData))
		if err != nil {
			return nil, "", "", false, err
		}
		defer zr.Close()
		fontData, err = io.ReadAll(io.LimitReader(zr, maxMOBICoverBytes+1))
		if err != nil {
			return nil, "", "", false, err
		}
		if int64(len(fontData)) > maxMOBICoverBytes {
			return nil, "", "", false, fmt.Errorf("FONT payload exceeds limit (%d bytes): %w", maxMOBICoverBytes, ErrTextTooLarge)
		}
	}
	ext, mediaType, ok := kindleFontMediaType(fontData)
	return fontData, ext, mediaType, ok, nil
}

func mobiCoverRecordCandidates(record0 []byte, firstImageIndex uint32) []uint32 {
	if firstImageIndex == 0 || firstImageIndex == mobiNoImageIndex {
		return nil
	}
	if fakeCover, ok := mobiEXTHUint32(record0, 203); ok && fakeCover != 0 {
		return nil
	}
	var out []uint32
	if coverOffset, ok := mobiEXTHUint32(record0, 201); ok {
		if coverOffset != mobiNoImageIndex && coverOffset <= mobiNoImageIndex-firstImageIndex {
			out = append(out, firstImageIndex+coverOffset)
		}
	}
	out = append(out, firstImageIndex)
	return uniqUint32s(out)
}

func uniqUint32s(values []uint32) []uint32 {
	seen := make(map[uint32]bool, len(values))
	out := values[:0]
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func kindleImageMediaTypeForExtension(ext string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(ext)) {
	case ".jpg", ".jpeg":
		return "image/jpeg", true
	case ".png":
		return "image/png", true
	case ".gif":
		return "image/gif", true
	case ".webp":
		return "image/webp", true
	default:
		return "", false
	}
}

func kindleFontMediaType(data []byte) (string, string, bool) {
	switch {
	case bytes.HasPrefix(data, []byte{0x00, 0x01, 0x00, 0x00}),
		bytes.HasPrefix(data, []byte("true")):
		return ".ttf", "font/ttf", true
	case bytes.HasPrefix(data, []byte("OTTO")):
		return ".otf", "font/otf", true
	case bytes.HasPrefix(data, []byte("ttcf")):
		return ".ttc", "font/collection", true
	case bytes.HasPrefix(data, []byte("wOFF")):
		return ".woff", "font/woff", true
	case bytes.HasPrefix(data, []byte("wOF2")):
		return ".woff2", "font/woff2", true
	default:
		return "", "", false
	}
}
