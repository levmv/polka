package converter

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/format"
)

type Target string

const (
	TargetPDF   Target = "pdf"
	TargetEPUB  Target = "epub"
	TargetKEPUB Target = "kepub"
	TargetCBZ   Target = "cbz"
)

type TargetSpec struct {
	Target    Target
	Label     string
	Extension string
	MediaType string
}

var supportedTargetSpecs = []TargetSpec{
	{Target: TargetPDF, Label: "PDF", Extension: ".pdf", MediaType: "application/pdf"},
	{Target: TargetEPUB, Label: "EPUB", Extension: ".epub", MediaType: "application/epub+zip"},
	{Target: TargetKEPUB, Label: "KEPUB", Extension: ".kepub.epub", MediaType: "application/epub+zip"},
	{Target: TargetCBZ, Label: "CBZ", Extension: ".cbz", MediaType: "application/vnd.comicbook+zip"},
}

var repairedEPUBTargetSpec = TargetSpec{
	Target:    TargetEPUB,
	Label:     "Repaired EPUB",
	Extension: ".epub",
	MediaType: "application/epub+zip",
}

var targetSpecsBySourceFormat = map[format.Format][]TargetSpec{
	format.FormatAZW4:     {targetSpec(TargetPDF)},
	format.FormatEPUB:     {repairedEPUBTargetSpec, targetSpec(TargetKEPUB)},
	format.FormatFB2:      {targetSpec(TargetEPUB), targetSpec(TargetKEPUB)},
	format.FormatMOBI:     {targetSpec(TargetEPUB), targetSpec(TargetKEPUB)},
	format.FormatAZW:      {targetSpec(TargetEPUB), targetSpec(TargetKEPUB)},
	format.FormatAZW3:     {targetSpec(TargetEPUB), targetSpec(TargetKEPUB)},
	format.FormatPRC:      {targetSpec(TargetEPUB), targetSpec(TargetKEPUB)},
	format.FormatPDB:      {targetSpec(TargetEPUB)},
	format.FormatTXT:      {targetSpec(TargetEPUB)},
	format.FormatTXTZ:     {targetSpec(TargetEPUB)},
	format.FormatMarkdown: {targetSpec(TargetEPUB)},
	format.FormatHTML:     {targetSpec(TargetEPUB)},
	format.FormatHTMLZ:    {targetSpec(TargetEPUB)},
	format.FormatXHTML:    {targetSpec(TargetEPUB)},
	format.FormatCBR:      {targetSpec(TargetCBZ)},
	format.FormatCB7:      {targetSpec(TargetCBZ)},
}

type ConversionOptions struct {
	// Metadata is fallback metadata for generated outputs. Source-embedded
	// metadata wins; these fields only fill gaps in weak formats such as TXT.
	Metadata *bookmeta.Metadata
	// SourceName is a final fallback for generated output titles.
	SourceName string
}

func NormalizeTarget(target string) Target {
	return Target(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(target)), "."))
}

func CanConvert(from format.Format, target Target) bool {
	target = NormalizeTarget(string(target))
	for _, supported := range TargetSpecsForFormat(from) {
		if supported.Target == target {
			return true
		}
	}
	return false
}

func TargetSpecsForFormat(from format.Format) []TargetSpec {
	targets := targetSpecsBySourceFormat[from]
	return append([]TargetSpec(nil), targets...)
}

func SupportedTargetSpecs() []TargetSpec {
	return append([]TargetSpec(nil), supportedTargetSpecs...)
}

func TargetExtension(target Target) string {
	return targetSpec(NormalizeTarget(string(target))).Extension
}

func TargetMediaType(target Target) string {
	return targetSpec(NormalizeTarget(string(target))).MediaType
}

func targetSpec(target Target) TargetSpec {
	target = NormalizeTarget(string(target))
	for _, spec := range supportedTargetSpecs {
		if spec.Target == target {
			return spec
		}
	}
	return TargetSpec{}
}

func ConvertContext(ctx context.Context, w io.Writer, src io.ReaderAt, from format.Format, size int64, target Target) error {
	return ConvertContextWithOptions(ctx, w, src, from, size, target, ConversionOptions{})
}

func ConvertContextWithOptions(ctx context.Context, w io.Writer, src io.ReaderAt, from format.Format, size int64, target Target, opts ConversionOptions) error {
	return convertContextWithLimits(ctx, w, src, from, size, target, opts, defaultConversionLimits)
}

func convertContextWithLimits(ctx context.Context, w io.Writer, src io.ReaderAt, from format.Format, size int64, target Target, opts ConversionOptions, limits conversionLimits) error {
	target = NormalizeTarget(string(target))
	if w == nil {
		return fmt.Errorf("output writer is required")
	}
	if target == "" {
		return fmt.Errorf("target format is required")
	}
	if !CanConvert(from, target) {
		return fmt.Errorf("unsupported conversion from %s to %s", format.FormatLabel(from), target)
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	budget := &conversionBudget{limits: limits}
	ctx = withConversionBudget(ctx, budget)
	src = contextReaderAt{ctx: ctx, r: src}
	w = contextWriter{ctx: ctx, w: &conversionOutputWriter{w: w, maxBytes: limits.outputBytes}}

	switch target {
	case TargetPDF:
		return format.ExtractAZW4PDFContext(ctx, w, src, size)
	case TargetEPUB:
		if from == format.FormatEPUB {
			return rebuildEPUB(ctx, w, src, size)
		}
		return convertSourceToEPUB(ctx, w, src, from, size, opts)
	case TargetKEPUB:
		if from == format.FormatEPUB {
			return convertEPUBToKEPUB(ctx, w, src, size)
		}
		return convertSourceViaEPUBToKEPUB(ctx, w, src, from, size, opts, limits.outputBytes)
	case TargetCBZ:
		if from == format.FormatCBR {
			return convertCBRToCBZ(ctx, w, src, size)
		}
		return convertCB7ToCBZ(ctx, w, src, size)
	default:
		return fmt.Errorf("unsupported target format %s", target)
	}
}

func convertSourceToEPUB(ctx context.Context, w io.Writer, src io.ReaderAt, from format.Format, size int64, opts ConversionOptions) error {
	if from == format.FormatFB2 {
		return convertFB2SourceToEPUB(ctx, w, src, size, opts)
	}
	if from == format.FormatMOBI || from == format.FormatAZW || from == format.FormatAZW3 || from == format.FormatPRC || from == format.FormatPDB {
		return convertKindleSourceToEPUB(ctx, w, src, from, size, opts)
	}
	if from == format.FormatHTML || from == format.FormatXHTML {
		return convertHTMLSourceToEPUB(ctx, w, src, size, opts)
	}
	if from == format.FormatHTMLZ {
		return convertHTMLZSourceToEPUB(ctx, w, src, size, opts)
	}
	return convertTextSourceToEPUB(ctx, w, src, from, size, opts)
}

func convertSourceViaEPUBToKEPUB(ctx context.Context, w io.Writer, src io.ReaderAt, from format.Format, size int64, opts ConversionOptions, maxIntermediateBytes int64) error {
	tmp, err := os.CreateTemp("", "polka-conversion-*.epub")
	if err != nil {
		return fmt.Errorf("create intermediate EPUB: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	tmpClosed := false
	defer func() {
		if !tmpClosed {
			_ = tmp.Close()
		}
	}()

	// Reuse the same context so both stages draw from one aggregate decoded-data
	// and resource-count budget. The intermediate also gets the normal output cap.
	stageWriter := contextWriter{ctx: ctx, w: &conversionOutputWriter{w: tmp, maxBytes: maxIntermediateBytes}}
	if err := convertSourceToEPUB(ctx, stageWriter, src, from, size, opts); err != nil {
		return fmt.Errorf("create intermediate EPUB: %w", err)
	}
	closeErr := tmp.Close()
	tmpClosed = true
	if closeErr != nil {
		return fmt.Errorf("close intermediate EPUB: %w", closeErr)
	}

	intermediate, err := os.Open(tmpPath)
	if err != nil {
		return fmt.Errorf("open intermediate EPUB: %w", err)
	}
	defer intermediate.Close()
	stat, err := intermediate.Stat()
	if err != nil {
		return fmt.Errorf("stat intermediate EPUB: %w", err)
	}
	if err := convertEPUBToKEPUB(ctx, w, intermediate, stat.Size()); err != nil {
		return fmt.Errorf("convert intermediate EPUB to KEPUB: %w", err)
	}
	return nil
}

func ConvertFile(ctx context.Context, srcPath, dstPath string, target Target, overwrite bool) error {
	if srcPath == "" {
		return fmt.Errorf("source path is required")
	}
	if dstPath == "" {
		return fmt.Errorf("output path is required")
	}
	if target == "" {
		return fmt.Errorf("target format is required")
	}

	src, kind, size, err := openSource(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	convertOpts := ConversionOptions{SourceName: filepath.Base(srcPath)}
	return writeOutput(dstPath, overwrite, func(w io.Writer) error {
		return ConvertContextWithOptions(ctx, w, src, kind, size, target, convertOpts)
	})
}

func openSource(srcPath string) (*os.File, format.Format, int64, error) {
	src, err := os.Open(srcPath)
	if err != nil {
		return nil, format.FormatUnknown, 0, fmt.Errorf("open source: %w", err)
	}
	stat, err := src.Stat()
	if err != nil {
		src.Close()
		return nil, format.FormatUnknown, 0, fmt.Errorf("stat source: %w", err)
	}
	kind := format.DetectFormat(srcPath, src, stat.Size())
	return src, kind, stat.Size(), nil
}

func writeOutput(dstPath string, overwrite bool, write func(io.Writer) error) error {
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

	if err := write(tmp); err != nil {
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
