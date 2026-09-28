package converter

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/format"
	"github.com/levmv/polka/internal/format/mobi"
)

type Target string

const (
	TargetPDF   Target = "pdf"
	TargetEPUB  Target = "epub"
	TargetKEPUB Target = "kepub"
	TargetMOBI6 Target = "mobi6"
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
	{Target: TargetMOBI6, Label: "MOBI6", Extension: ".mobi", MediaType: "application/x-mobipocket-ebook"},
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
	format.FormatEPUB:     {repairedEPUBTargetSpec, targetSpec(TargetKEPUB), targetSpec(TargetMOBI6)},
	format.FormatFB2:      {targetSpec(TargetEPUB), targetSpec(TargetKEPUB)},
	format.FormatMOBI:     {targetSpec(TargetEPUB), targetSpec(TargetKEPUB)},
	format.FormatAZW:      {targetSpec(TargetEPUB), targetSpec(TargetKEPUB)},
	format.FormatAZW3:     {targetSpec(TargetEPUB), targetSpec(TargetKEPUB), targetSpec(TargetMOBI6)},
	format.FormatPRC:      {targetSpec(TargetEPUB), targetSpec(TargetKEPUB)},
	format.FormatPDB:      {targetSpec(TargetEPUB)},
	format.FormatTXT:      {targetSpec(TargetEPUB), targetSpec(TargetKEPUB)},
	format.FormatTXTZ:     {targetSpec(TargetEPUB), targetSpec(TargetKEPUB)},
	format.FormatMarkdown: {targetSpec(TargetEPUB), targetSpec(TargetKEPUB)},
	format.FormatHTML:     {targetSpec(TargetEPUB), targetSpec(TargetKEPUB)},
	format.FormatHTMLZ:    {targetSpec(TargetEPUB), targetSpec(TargetKEPUB)},
	format.FormatXHTML:    {targetSpec(TargetEPUB), targetSpec(TargetKEPUB)},
	format.FormatCBR:      {targetSpec(TargetCBZ)},
	format.FormatCB7:      {targetSpec(TargetCBZ)},
}

type ConversionOptions struct {
	// Metadata is a complete catalog snapshot for EPUB, KEPUB and MOBI6 output.
	// Its supported book fields replace source values, including empty fields.
	// Existing packages use write-back's per-field preservation rules.
	// Nil keeps the source metadata. Page counts remain specific to each asset;
	// PDF extraction and comic archive repacking retain their embedded metadata.
	Metadata *bookmeta.Metadata
	// Modified is the snapshot's modification time. Zero uses a fixed epoch so
	// identical inputs never acquire a new download identity from the wall clock.
	Modified time.Time
	// SourceName is a final fallback for generated output titles.
	SourceName string
	// OnWarning is called synchronously for each recoverable problem. The
	// converter does not accumulate warnings, so callers can stream details or
	// keep only a count even for a very damaged book.
	// Ordinary markup repairs do not produce warnings.
	OnWarning func(message string)
}

func (opts ConversionOptions) warn(message string, args ...any) {
	if opts.OnWarning != nil {
		opts.OnWarning(fmt.Sprintf(message, args...))
	}
}

func (opts ConversionOptions) modifiedTime() time.Time {
	if opts.Modified.IsZero() {
		return time.Unix(0, 0).UTC()
	}
	return opts.Modified.UTC()
}

func (opts ConversionOptions) rewriteOPF(raw []byte) []byte {
	if opts.Metadata == nil {
		return raw
	}
	meta := *opts.Metadata
	// Container conversions keep the package's own page-count policy.
	meta.PageCount = 0
	// EPUB requires a language; use the same unknown value as generated books.
	if meta.Language == "" {
		meta.Language = "und"
	}
	next, err := format.RewriteOPFMetadata(raw, meta, opts.modifiedTime())
	if err != nil {
		opts.warn("Could not update EPUB metadata; kept source metadata: %v", err)
		return raw
	}
	return next
}

func NormalizeTarget(target string) Target {
	return Target(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(target)), "."))
}

func CanConvert(from format.Format, target Target) bool {
	target = NormalizeTarget(string(target))
	for _, supported := range targetSpecsBySourceFormat[from] {
		if supported.Target == target {
			return true
		}
	}
	return false
}

func TargetSpecsForFormat(from format.Format) []TargetSpec {
	return slices.Clone(targetSpecsBySourceFormat[from])
}

func SupportedTargetSpecs() []TargetSpec {
	return slices.Clone(supportedTargetSpecs)
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
		return mobiConversionError(mobi.ExtractPDF(ctx, w, src, size))
	case TargetEPUB:
		if from == format.FormatEPUB {
			return rebuildEPUB(ctx, w, src, size, opts)
		}
		return convertSourceToEPUB(ctx, w, src, from, size, opts)
	case TargetKEPUB:
		if from == format.FormatEPUB {
			return convertEPUBToKEPUB(ctx, w, src, size, opts)
		}
		return convertSourceViaEPUB(ctx, w, src, from, size, opts, limits.outputBytes, func(ctx context.Context, w io.Writer, src io.ReaderAt, size int64) error {
			// The generated intermediate already carries the catalog snapshot.
			return convertEPUBToKEPUB(ctx, w, src, size, ConversionOptions{OnWarning: opts.OnWarning})
		})
	case TargetMOBI6:
		convert := func(ctx context.Context, w io.Writer, src io.ReaderAt, size int64) error {
			return convertEPUBToMOBI6(ctx, w, src, size, opts)
		}
		if from == format.FormatEPUB {
			return convert(ctx, w, src, size)
		}
		return convertSourceViaEPUB(ctx, w, src, from, size, opts, limits.outputBytes, convert)
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
		return convertMOBISourceToEPUB(ctx, w, src, size, opts)
	}
	if from == format.FormatHTML || from == format.FormatXHTML {
		return convertHTMLSourceToEPUB(ctx, w, src, from, size, opts)
	}
	if from == format.FormatHTMLZ {
		return convertHTMLZSourceToEPUB(ctx, w, src, size, opts)
	}
	return convertTextSourceToEPUB(ctx, w, src, from, size, opts)
}

func convertSourceViaEPUB(ctx context.Context, w io.Writer, src io.ReaderAt, from format.Format, size int64, opts ConversionOptions, maxIntermediateBytes int64, convert func(context.Context, io.Writer, io.ReaderAt, int64) error) error {
	tmp, err := os.CreateTemp("", "polka-conversion-*.epub")
	if err != nil {
		return fmt.Errorf("create intermediate EPUB: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	defer tmp.Close()

	// Reuse the same context so both stages draw from one aggregate decoded-data
	// and resource-count budget. The intermediate also gets the normal output cap.
	stageWriter := contextWriter{ctx: ctx, w: &conversionOutputWriter{w: tmp, maxBytes: maxIntermediateBytes}}
	if err := convertSourceToEPUB(ctx, stageWriter, src, from, size, opts); err != nil {
		return fmt.Errorf("create intermediate EPUB: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close intermediate EPUB: %w", err)
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
	if err := convert(ctx, w, intermediate, stat.Size()); err != nil {
		return fmt.Errorf("convert intermediate EPUB: %w", err)
	}
	return nil
}

func ConvertFile(ctx context.Context, srcPath, dstPath string, target Target, overwrite bool) error {
	return ConvertFileWithOptions(ctx, srcPath, dstPath, target, overwrite, ConversionOptions{})
}

func ConvertFileWithOptions(ctx context.Context, srcPath, dstPath string, target Target, overwrite bool, opts ConversionOptions) error {
	if srcPath == "" {
		return fmt.Errorf("source path is required")
	}
	if dstPath == "" {
		return fmt.Errorf("output path is required")
	}
	if target == "" {
		return fmt.Errorf("target format is required")
	}

	src, kind, size, err := openSource(ctx, srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	if opts.SourceName == "" {
		opts.SourceName = filepath.Base(srcPath)
	}
	return writeOutput(dstPath, overwrite, func(w io.Writer) error {
		return ConvertContextWithOptions(ctx, w, src, kind, size, target, opts)
	})
}

func openSource(ctx context.Context, srcPath string) (*os.File, format.Format, int64, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, format.FormatUnknown, 0, err
	}
	src, err := os.Open(srcPath)
	if err != nil {
		return nil, format.FormatUnknown, 0, fmt.Errorf("open source: %w", err)
	}
	stat, err := src.Stat()
	if err != nil {
		src.Close()
		return nil, format.FormatUnknown, 0, fmt.Errorf("stat source: %w", err)
	}
	kind := format.DetectFormat(srcPath, contextReaderAt{ctx: ctx, r: src}, stat.Size())
	if err := context.Cause(ctx); err != nil {
		src.Close()
		return nil, format.FormatUnknown, 0, err
	}
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
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := write(tmp); err != nil {
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
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
