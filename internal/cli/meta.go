package cli

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/converter"
	"github.com/levmv/polka/internal/format"
)

var errMetaFileErrors = errors.New("one or more files could not be inspected")

type metaFileReport struct {
	Path              string                 `json:"path"`
	Format            string                 `json:"format"`
	FormatLabel       string                 `json:"format_label"`
	MediaType         string                 `json:"media_type"`
	Metadata          *metaMetadata          `json:"metadata,omitzero"`
	Details           *metaFormatDetail      `json:"details,omitzero"`
	Warnings          []metaFormatWarning    `json:"warnings,omitempty"`
	ConversionTargets []metaConversionTarget `json:"conversion_targets,omitempty"`
	Cover             *metaCoverReport       `json:"cover,omitzero"`
	CoverError        string                 `json:"cover_error,omitempty"`
	Error             string                 `json:"error,omitempty"`
}

type metaMetadata struct {
	Title       string       `json:"title,omitempty"`
	SortTitle   string       `json:"sort_title,omitempty"`
	Authors     []metaAuthor `json:"authors,omitempty"`
	Language    string       `json:"language,omitempty"`
	Description string       `json:"description,omitempty"`
	Publisher   string       `json:"publisher,omitempty"`
	Date        string       `json:"date,omitempty"`
	Identifier  string       `json:"identifier,omitempty"`
	Series      string       `json:"series,omitempty"`
	SeriesIndex float64      `json:"series_index,omitzero"`
	Tags        []string     `json:"tags,omitempty"`
}

type metaAuthor struct {
	Name     string `json:"name,omitempty"`
	SortName string `json:"sort_name,omitempty"`
	Role     string `json:"role,omitempty"`
}

type metaConversionTarget struct {
	Target    string `json:"target"`
	Label     string `json:"label"`
	Extension string `json:"extension"`
	MediaType string `json:"media_type"`
}

type metaFormatWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type metaCoverReport struct {
	Path      string `json:"path"`
	Extension string `json:"extension,omitempty"`
	Bytes     int    `json:"bytes,omitzero"`
}

type metaFormatDetail struct {
	MOBIKind     string            `json:"mobi_kind,omitempty"`
	AZW4PDF      string            `json:"azw4_pdf,omitempty"`
	FB2Container string            `json:"fb2_container,omitempty"`
	CBZPageCount int               `json:"cbz_page_count,omitzero"`
	CBRPageCount int               `json:"cbr_page_count,omitzero"`
	CB7PageCount int               `json:"cb7_page_count,omitzero"`
	Kindle       *metaKindleDetail `json:"kindle,omitzero"`
}

type metaKindleDetail struct {
	SourceClass              string                    `json:"source_class,omitempty"`
	Container                string                    `json:"container,omitempty"`
	TypeCreator              string                    `json:"type_creator,omitempty"`
	RecordCount              int                       `json:"record_count,omitzero"`
	Compression              string                    `json:"compression,omitempty"`
	CompressionCode          uint16                    `json:"compression_code,omitzero"`
	TextLength               uint32                    `json:"text_length,omitzero"`
	TextRecords              uint16                    `json:"text_records,omitzero"`
	RecordSize               uint16                    `json:"record_size,omitzero"`
	Encrypted                bool                      `json:"encrypted,omitzero"`
	Encryption               uint16                    `json:"encryption,omitzero"`
	HeaderLength             uint32                    `json:"header_length,omitzero"`
	Codepage                 uint32                    `json:"codepage,omitzero"`
	MOBIType                 uint32                    `json:"mobi_type,omitzero"`
	MOBIVersion              uint32                    `json:"mobi_version,omitzero"`
	TrailingFlags            uint16                    `json:"trailing_flags,omitzero"`
	HasEXTH                  bool                      `json:"has_exth,omitzero"`
	EXTHTypes                []uint32                  `json:"exth_types,omitempty"`
	CDEType                  string                    `json:"cdetype,omitempty"`
	PrimaryWritingMode       string                    `json:"primary_writing_mode,omitempty"`
	PageProgressionDirection string                    `json:"page_progression_direction,omitempty"`
	Dictionary               bool                      `json:"dictionary,omitzero"`
	FirstResourceIndex       uint32                    `json:"first_resource_index,omitzero"`
	HUFFCDICIndex            uint32                    `json:"huff_cdic_index,omitzero"`
	HUFFCDICRecordCount      uint32                    `json:"huff_cdic_record_count,omitzero"`
	NCXIndex                 uint32                    `json:"ncx_index,omitzero"`
	FDSTIndex                uint32                    `json:"fdst_index,omitzero"`
	FDSTCount                uint32                    `json:"fdst_count,omitzero"`
	FragmentIndex            uint32                    `json:"fragment_index,omitzero"`
	SkeletonIndex            uint32                    `json:"skeleton_index,omitzero"`
	GuideIndex               uint32                    `json:"guide_index,omitzero"`
	BoundaryIndex            uint32                    `json:"boundary_index,omitzero"`
	Resources                *metaKindleResourceCounts `json:"resources,omitzero"`
	AZW4PDF                  bool                      `json:"azw4_pdf,omitzero"`
	UnsupportedFeatures      []string                  `json:"unsupported_features,omitempty"`
}

type metaKindleResourceCounts struct {
	Images   int `json:"images,omitzero"`
	Fonts    int `json:"fonts,omitzero"`
	HUFF     int `json:"huff,omitzero"`
	CDIC     int `json:"cdic,omitzero"`
	INDX     int `json:"indx,omitzero"`
	RESC     int `json:"resc,omitzero"`
	FDST     int `json:"fdst,omitzero"`
	FLIS     int `json:"flis,omitzero"`
	FCIS     int `json:"fcis,omitzero"`
	DATP     int `json:"datp,omitzero"`
	SRCS     int `json:"srcs,omitzero"`
	Audios   int `json:"audios,omitzero"`
	Videos   int `json:"videos,omitzero"`
	INFL     int `json:"infl,omitzero"`
	ORTH     int `json:"orth,omitzero"`
	Boundary int `json:"boundary,omitzero"`
	Other    int `json:"other,omitzero"`
}

func runMeta(_ string, args []string) error {
	if len(args) > 0 && args[0] == "set" {
		return runMetaSet(args[1:])
	}

	fs := commandFlagSet("meta", "polka meta [--json] [--cover <path> [--force]] <file>...\n       polka meta set <file> [field flags]")
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	coverPath := fs.String("cover", "", "write extracted cover image to path (single input only)")
	force := fs.Bool("force", false, "overwrite the cover output file when used with --cover")
	if help, err := parseCommandFlags(fs, args); help || err != nil {
		return err
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return reportedErrorf("usage: polka meta [--json] [--cover <path> [--force]] <file>...")
	}
	if *coverPath != "" && fs.NArg() != 1 {
		fs.Usage()
		return reportedErrorf("usage: polka meta --cover <path> accepts exactly one input file")
	}

	reports := make([]metaFileReport, 0, fs.NArg())
	hadErrors := false
	for _, path := range fs.Args() {
		report := inspectMetaFile(path, *coverPath, *force)
		if report.Error != "" || report.CoverError != "" {
			hadErrors = true
		}
		reports = append(reports, report)
	}

	if *jsonOutput {
		if err := json.MarshalWrite(os.Stdout, reports, jsontext.WithIndent("  ")); err != nil {
			return err
		}
		fmt.Println()
	} else {
		printMetaReports(reports)
	}

	if hadErrors {
		return errMetaFileErrors
	}
	return nil
}

func inspectMetaFile(path, coverPath string, force bool) metaFileReport {
	report := metaFileReport{Path: path}
	f, err := os.Open(path)
	if err != nil {
		report.Error = fmt.Sprintf("open source: %v", err)
		return report
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		report.Error = fmt.Sprintf("stat source: %v", err)
		return report
	}

	kind := format.DetectFormat(path, f, stat.Size())
	report.Format = format.FormatKey(kind)
	report.FormatLabel = format.FormatLabel(kind)
	report.MediaType = metaMediaType(path, kind)
	report.ConversionTargets = metaConversionTargets(kind)

	meta, err := format.ExtractMetadata(f, stat.Size(), kind)
	if err != nil {
		report.Error = fmt.Sprintf("extract metadata: %v", err)
		return report
	}
	report.Metadata = metadataForReport(meta)
	report.Details = metaFormatDetails(path, f, stat.Size(), kind)
	diagnosticKind := kind
	if kindleKind := metaKindleInspectFormat(path, kind); kindleKind != format.FormatUnknown {
		diagnosticKind = kindleKind
	}
	for _, diagnostic := range format.InspectDiagnostics(f, stat.Size(), diagnosticKind, path) {
		report.Warnings = append(report.Warnings, metaFormatWarning{
			Code:    diagnostic.Code,
			Message: diagnostic.Message,
		})
	}
	if coverPath != "" {
		cover, err := extractMetaCover(f, stat.Size(), kind, coverPath, force)
		if err != nil {
			report.CoverError = err.Error()
		} else {
			report.Cover = cover
		}
	}
	return report
}

func metaMediaType(path string, kind format.Format) string {
	ext := format.BookExtension(path)
	if format.FormatFromExt(ext) == kind {
		if mediaType := format.MediaTypeForExtension(ext); mediaType != "application/octet-stream" {
			return mediaType
		}
	}
	if ext := format.DefaultExtensionForFormat(kind); ext != "" {
		return format.MediaTypeForExtension(ext)
	}
	return "application/octet-stream"
}

func extractMetaCover(r io.ReaderAt, size int64, kind format.Format, dstPath string, force bool) (*metaCoverReport, error) {
	coverBytes, coverExt, err := format.ExtractCover(r, size, kind)
	if err != nil {
		return nil, fmt.Errorf("extract cover: %w", err)
	}
	if len(coverBytes) == 0 {
		return nil, errors.New("extract cover: no cover found")
	}
	if err := writeMetaCoverFile(dstPath, coverBytes, force); err != nil {
		return nil, fmt.Errorf("write cover: %w", err)
	}
	return &metaCoverReport{
		Path:      dstPath,
		Extension: normalizeMetaCoverExtension(coverExt),
		Bytes:     len(coverBytes),
	}, nil
}

func normalizeMetaCoverExtension(ext string) string {
	ext = strings.TrimSpace(strings.ToLower(ext))
	if ext == "" || strings.HasPrefix(ext, ".") {
		return ext
	}
	return "." + ext
}

func writeMetaCoverFile(dstPath string, data []byte, overwrite bool) error {
	if dstPath == "" {
		return errors.New("output path is required")
	}
	if !overwrite {
		if _, err := os.Stat(dstPath); err == nil {
			return fmt.Errorf("output file already exists: %s", dstPath)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("stat output: %w", err)
		}
	}

	dir := filepath.Dir(dstPath)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(dstPath)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp output: %w", err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp output: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp output: %w", err)
	}
	if err := os.Rename(tmpPath, dstPath); err != nil {
		return fmt.Errorf("move temp output: %w", err)
	}
	committed = true
	return nil
}

func metaConversionTargets(kind format.Format) []metaConversionTarget {
	specs := converter.TargetSpecsForFormat(kind)
	if len(specs) == 0 {
		return nil
	}
	targets := make([]metaConversionTarget, 0, len(specs))
	for _, spec := range specs {
		targets = append(targets, metaConversionTarget{
			Target:    string(spec.Target),
			Label:     spec.Label,
			Extension: spec.Extension,
			MediaType: spec.MediaType,
		})
	}
	return targets
}

func metadataForReport(meta *bookmeta.Metadata) *metaMetadata {
	if meta == nil {
		return &metaMetadata{}
	}
	out := metaMetadata{
		Title:       strings.TrimSpace(meta.Title),
		SortTitle:   strings.TrimSpace(meta.SortTitle),
		Language:    strings.TrimSpace(meta.Language),
		Description: strings.TrimSpace(meta.Description),
		Publisher:   strings.TrimSpace(meta.Publisher),
		Date:        strings.TrimSpace(meta.Date),
		Identifier:  strings.TrimSpace(meta.Identifier),
		Series:      strings.TrimSpace(meta.Series),
		SeriesIndex: meta.SeriesIndex,
	}
	for _, author := range meta.Authors {
		name := strings.TrimSpace(author.Name)
		sortName := strings.TrimSpace(author.SortName)
		role := strings.TrimSpace(author.Role)
		if name == "" && sortName == "" && role == "" {
			continue
		}
		out.Authors = append(out.Authors, metaAuthor{Name: name, SortName: sortName, Role: role})
	}
	for _, tag := range meta.Tags {
		if tag = strings.TrimSpace(tag); tag != "" {
			out.Tags = append(out.Tags, tag)
		}
	}
	return &out
}

func metaFormatDetails(path string, f *os.File, size int64, kind format.Format) *metaFormatDetail {
	details := &metaFormatDetail{}
	if kindleKind := metaKindleInspectFormat(path, kind); kindleKind != format.FormatUnknown {
		kindle, err := format.InspectKindle(f, size, kindleKind)
		if err == nil && kindle != nil {
			if kindle.MOBIKind != format.MOBIKindUnknown {
				details.MOBIKind = string(kindle.MOBIKind)
			}
			details.Kindle = kindleDetailForReport(kindle)
		}
		if kindleKind == format.FormatAZW4 {
			if (details.Kindle != nil && details.Kindle.AZW4PDF) || (details.Kindle == nil && format.HasAZW4PDF(f, size)) {
				details.AZW4PDF = "present"
			} else {
				details.AZW4PDF = "missing"
			}
		}
	}
	switch kind {
	case format.FormatCBZ:
		if pages, err := format.ListCBZPages(f, size); err == nil {
			details.CBZPageCount = len(pages)
		}
	case format.FormatCBR:
		if pages, err := format.ListCBRPages(f, size); err == nil {
			details.CBRPageCount = len(pages)
		}
	case format.FormatCB7:
		if pages, err := format.ListCB7Pages(f, size); err == nil {
			details.CB7PageCount = len(pages)
		}
	case format.FormatFB2:
		if container := format.FB2ContainerForExtension(format.BookExtension(path)); container != format.FB2ContainerNone {
			details.FB2Container = string(container)
		} else {
			details.FB2Container = "plain"
		}
	}
	if details.MOBIKind == "" && details.AZW4PDF == "" && details.FB2Container == "" && details.CBZPageCount == 0 && details.CBRPageCount == 0 && details.CB7PageCount == 0 && details.Kindle == nil {
		return nil
	}
	return details
}

func metaKindleInspectFormat(path string, kind format.Format) format.Format {
	if metaIsKindleFamilyFormat(kind) {
		return kind
	}
	extKind := format.FormatFromExt(format.BookExtension(path))
	if metaIsKindleFamilyFormat(extKind) {
		return extKind
	}
	return format.FormatUnknown
}

func metaIsKindleFamilyFormat(kind format.Format) bool {
	switch kind {
	case format.FormatMOBI, format.FormatAZW, format.FormatAZW3, format.FormatAZW4, format.FormatPRC, format.FormatPDB:
		return true
	default:
		return false
	}
}

func kindleDetailForReport(info *format.KindleInspection) *metaKindleDetail {
	if info == nil {
		return nil
	}
	return &metaKindleDetail{
		SourceClass:              info.SourceClass,
		Container:                info.Container,
		TypeCreator:              info.TypeCreator,
		RecordCount:              info.RecordCount,
		Compression:              info.CompressionName,
		CompressionCode:          info.Compression,
		TextLength:               info.TextLength,
		TextRecords:              info.TextRecords,
		RecordSize:               info.RecordSize,
		Encrypted:                info.Encrypted,
		Encryption:               info.Encryption,
		HeaderLength:             info.HeaderLength,
		Codepage:                 info.Codepage,
		MOBIType:                 info.MOBIType,
		MOBIVersion:              info.MOBIVersion,
		TrailingFlags:            info.TrailingFlags,
		HasEXTH:                  info.HasEXTH,
		EXTHTypes:                append([]uint32(nil), info.EXTHTypes...),
		CDEType:                  info.CDEType,
		PrimaryWritingMode:       info.PrimaryWritingMode,
		PageProgressionDirection: info.PageProgressionDirection,
		Dictionary:               info.Dictionary,
		FirstResourceIndex:       info.FirstResourceIndex,
		HUFFCDICIndex:            info.HUFFCDICIndex,
		HUFFCDICRecordCount:      info.HUFFCDICRecordCount,
		NCXIndex:                 info.NCXIndex,
		FDSTIndex:                info.FDSTIndex,
		FDSTCount:                info.FDSTCount,
		FragmentIndex:            info.FragmentIndex,
		SkeletonIndex:            info.SkeletonIndex,
		GuideIndex:               info.GuideIndex,
		BoundaryIndex:            info.BoundaryIndex,
		Resources:                kindleResourceCountsForReport(info.ResourceCounts),
		AZW4PDF:                  info.AZW4PDF,
		UnsupportedFeatures:      append([]string(nil), info.UnsupportedFeatures...),
	}
}

func kindleResourceCountsForReport(counts format.KindleResourceCounts) *metaKindleResourceCounts {
	out := metaKindleResourceCounts{
		Images:   counts.Images,
		Fonts:    counts.Fonts,
		HUFF:     counts.HUFF,
		CDIC:     counts.CDIC,
		INDX:     counts.INDX,
		RESC:     counts.RESC,
		FDST:     counts.FDST,
		FLIS:     counts.FLIS,
		FCIS:     counts.FCIS,
		DATP:     counts.DATP,
		SRCS:     counts.SRCS,
		Audios:   counts.Audios,
		Videos:   counts.Videos,
		INFL:     counts.INFL,
		ORTH:     counts.ORTH,
		Boundary: counts.Boundary,
		Other:    counts.Other,
	}
	if out == (metaKindleResourceCounts{}) {
		return nil
	}
	return &out
}

func printMetaReports(reports []metaFileReport) {
	for i, report := range reports {
		if i > 0 {
			fmt.Println()
		}
		fmt.Println(report.Path)
		if report.Error != "" {
			fmt.Printf("  error: %s\n", report.Error)
			continue
		}
		fmt.Printf("  format: %s (%s)\n", report.Format, report.FormatLabel)
		fmt.Printf("  media_type: %s\n", report.MediaType)
		if report.Metadata != nil {
			printMetaMetadata(*report.Metadata)
		}
		if report.Details != nil {
			printMetaDetails(report.Details)
		}
		if len(report.Warnings) > 0 {
			fmt.Println("  warnings:")
			for _, warning := range report.Warnings {
				fmt.Printf("    - %s: %s\n", warning.Code, warning.Message)
			}
		}
		if len(report.ConversionTargets) > 0 {
			targets := make([]string, 0, len(report.ConversionTargets))
			for _, target := range report.ConversionTargets {
				targets = append(targets, target.Target)
			}
			fmt.Printf("  conversion_targets: %s\n", strings.Join(targets, ", "))
		}
		if report.Cover != nil {
			fmt.Printf("  cover: %s", report.Cover.Path)
			var details []string
			if report.Cover.Extension != "" {
				details = append(details, report.Cover.Extension)
			}
			if report.Cover.Bytes != 0 {
				details = append(details, fmt.Sprintf("%d bytes", report.Cover.Bytes))
			}
			if len(details) > 0 {
				fmt.Printf(" (%s)", strings.Join(details, ", "))
			}
			fmt.Println()
		}
		if report.CoverError != "" {
			fmt.Printf("  cover_error: %s\n", report.CoverError)
		}
	}
}

func printMetaMetadata(meta metaMetadata) {
	if meta.Title != "" {
		fmt.Printf("  title: %s\n", meta.Title)
	}
	if meta.SortTitle != "" {
		fmt.Printf("  sort_title: %s\n", meta.SortTitle)
	}
	if len(meta.Authors) > 0 {
		fmt.Println("  authors:")
		for _, author := range meta.Authors {
			parts := []string{author.Name}
			var qualifiers []string
			if author.SortName != "" {
				qualifiers = append(qualifiers, "sort: "+author.SortName)
			}
			if author.Role != "" {
				qualifiers = append(qualifiers, "role: "+author.Role)
			}
			if len(qualifiers) > 0 {
				parts = append(parts, "("+strings.Join(qualifiers, ", ")+")")
			}
			fmt.Printf("    - %s\n", strings.Join(parts, " "))
		}
	}
	printMetaField("language", meta.Language)
	printMetaField("publisher", meta.Publisher)
	printMetaField("date", meta.Date)
	printMetaField("identifier", meta.Identifier)
	if meta.Series != "" {
		if meta.SeriesIndex != 0 {
			fmt.Printf("  series: %s #%v\n", meta.Series, meta.SeriesIndex)
		} else {
			fmt.Printf("  series: %s\n", meta.Series)
		}
	}
	if len(meta.Tags) > 0 {
		fmt.Printf("  tags: %s\n", strings.Join(meta.Tags, ", "))
	}
	if meta.Description != "" {
		fmt.Printf("  description: %s\n", meta.Description)
	}
}

func printMetaField(label, value string) {
	if value != "" {
		fmt.Printf("  %s: %s\n", label, value)
	}
}

func printMetaDetails(details *metaFormatDetail) {
	if details.MOBIKind != "" {
		fmt.Printf("  mobi_kind: %s\n", details.MOBIKind)
	}
	if details.Kindle != nil {
		printMetaKindleDetails(details.Kindle)
	}
	if details.AZW4PDF != "" {
		fmt.Printf("  azw4_pdf: %s\n", details.AZW4PDF)
	}
	if details.FB2Container != "" {
		fmt.Printf("  fb2_container: %s\n", details.FB2Container)
	}
	if details.CBZPageCount != 0 {
		fmt.Printf("  cbz_page_count: %d\n", details.CBZPageCount)
	}
	if details.CBRPageCount != 0 {
		fmt.Printf("  cbr_page_count: %d\n", details.CBRPageCount)
	}
	if details.CB7PageCount != 0 {
		fmt.Printf("  cb7_page_count: %d\n", details.CB7PageCount)
	}
}

func printMetaKindleDetails(k *metaKindleDetail) {
	printMetaField("kindle_class", k.SourceClass)
	printMetaField("kindle_container", k.Container)
	if k.Compression != "" {
		fmt.Printf("  kindle_compression: %s", k.Compression)
		if k.CompressionCode != 0 {
			fmt.Printf(" (%d)", k.CompressionCode)
		}
		fmt.Println()
	}
	if k.Encrypted {
		fmt.Printf("  kindle_encrypted: yes (%d)\n", k.Encryption)
	}
	if k.RecordCount != 0 {
		fmt.Printf("  kindle_records: %d\n", k.RecordCount)
	}
	if k.TextRecords != 0 {
		fmt.Printf("  kindle_text_records: %d\n", k.TextRecords)
	}
	if k.MOBIVersion != 0 {
		fmt.Printf("  kindle_mobi_version: %d\n", k.MOBIVersion)
	}
	if k.Codepage != 0 {
		fmt.Printf("  kindle_codepage: %d\n", k.Codepage)
	}
	if k.TrailingFlags != 0 {
		fmt.Printf("  kindle_trailing_flags: 0x%04x\n", k.TrailingFlags)
	}
	if len(k.EXTHTypes) > 0 {
		fmt.Printf("  kindle_exth_types: %s\n", joinMetaUint32s(k.EXTHTypes))
	}
	printMetaField("kindle_cdetype", k.CDEType)
	printMetaField("kindle_writing_mode", k.PrimaryWritingMode)
	printMetaField("kindle_page_progression", k.PageProgressionDirection)
	if k.Dictionary {
		fmt.Println("  kindle_dictionary: yes")
	}
	var indexes []string
	addIndex := func(label string, value uint32) {
		if value != 0 {
			indexes = append(indexes, fmt.Sprintf("%s=%d", label, value))
		}
	}
	addIndex("first_resource", k.FirstResourceIndex)
	addIndex("huff_cdic", k.HUFFCDICIndex)
	addIndex("ncx", k.NCXIndex)
	addIndex("fdst", k.FDSTIndex)
	addIndex("fragment", k.FragmentIndex)
	addIndex("skeleton", k.SkeletonIndex)
	addIndex("guide", k.GuideIndex)
	addIndex("boundary", k.BoundaryIndex)
	if len(indexes) > 0 {
		fmt.Printf("  kindle_indexes: %s\n", strings.Join(indexes, ", "))
	}
	if k.Resources != nil {
		if resources := metaKindleResourceSummary(k.Resources); resources != "" {
			fmt.Printf("  kindle_resources: %s\n", resources)
		}
	}
	if len(k.UnsupportedFeatures) > 0 {
		fmt.Printf("  kindle_unsupported: %s\n", strings.Join(k.UnsupportedFeatures, ", "))
	}
}

func joinMetaUint32s(values []uint32) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, fmt.Sprintf("%d", value))
	}
	return strings.Join(parts, ", ")
}

func metaKindleResourceSummary(resources *metaKindleResourceCounts) string {
	if resources == nil {
		return ""
	}
	var parts []string
	add := func(label string, value int) {
		if value != 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", label, value))
		}
	}
	add("images", resources.Images)
	add("fonts", resources.Fonts)
	add("huff", resources.HUFF)
	add("cdic", resources.CDIC)
	add("indx", resources.INDX)
	add("resc", resources.RESC)
	add("fdst", resources.FDST)
	add("flis", resources.FLIS)
	add("fcis", resources.FCIS)
	add("datp", resources.DATP)
	add("srcs", resources.SRCS)
	add("audio", resources.Audios)
	add("video", resources.Videos)
	add("infl", resources.INFL)
	add("orth", resources.ORTH)
	add("boundary", resources.Boundary)
	add("other", resources.Other)
	return strings.Join(parts, ", ")
}
