package format

import (
	"archive/zip"
	"fmt"
	"io"
	"strings"
)

// Diagnostic describes a recoverable format anomaly. InspectDiagnostics is a
// standalone-tool surface: diagnostics never change import, reading, or
// conversion success, and callers should not turn them into blocking warnings.
type Diagnostic struct {
	Code    string
	Message string
}

// InspectDiagnostics reports high-signal compatibility fallbacks already used
// by the normal parser. It deliberately stays bounded and best-effort: inability
// to prove a fallback produces no diagnostic rather than a new failure mode.
func InspectDiagnostics(r io.ReaderAt, size int64, kind Format, filename string) []Diagnostic {
	switch {
	case IsEPUBContainerFormat(kind):
		return inspectEPUBDiagnostics(r, size)
	case kind == FormatFB2:
		return inspectFB2Diagnostics(r, size, filename)
	case isKindleDiagnosticFormat(kind):
		return inspectKindleDiagnostics(r, size, kind)
	default:
		return nil
	}
}

func inspectEPUBDiagnostics(r io.ReaderAt, size int64) []Diagnostic {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil
	}
	var out []Diagnostic
	if !hasStrictEPUBMimetype(zr) {
		out = append(out, Diagnostic{
			Code:    "epub.nonstandard_mimetype",
			Message: "EPUB was recognized from its package document because the mimetype entry is missing, reordered, compressed, or non-canonical.",
		})
	}
	opf, ok, err := readEPUBOPF(zr)
	if err != nil || !ok {
		return out
	}
	if opf.discoveredPackage {
		out = append(out, Diagnostic{
			Code:    "epub.discovered_package",
			Message: fmt.Sprintf("Discovered the sole coherent package document %q because container.xml was missing or unusable.", opf.path),
		})
	} else if opf.containerPath != "META-INF/container.xml" {
		out = append(out, Diagnostic{
			Code:    "epub.normalized_container_path",
			Message: fmt.Sprintf("Resolved container.xml through the unique normalized archive path %q.", opf.containerPath),
		})
	}
	if opf.requestedPath != "" && opf.path != opf.requestedPath {
		out = append(out, Diagnostic{
			Code:    "epub.normalized_package_path",
			Message: fmt.Sprintf("Resolved package path %q through the unique normalized archive path %q.", opf.requestedPath, opf.path),
		})
	}
	if opf.relaxedMediaType {
		out = append(out, Diagnostic{
			Code:    "epub.rootfile_media_type_fallback",
			Message: "Used a declared rootfile whose media-type is not the standard OPF media type.",
		})
	}
	if opf.skippedCandidates > 0 {
		out = append(out, Diagnostic{
			Code:    "epub.package_candidate_fallback",
			Message: fmt.Sprintf("Used a later package document after %d earlier candidate(s) could not be parsed.", opf.skippedCandidates),
		})
	}
	return out
}

func inspectFB2Diagnostics(r io.ReaderAt, size int64, filename string) []Diagnostic {
	source, err := OpenFB2Source(r, size, "")
	if err != nil {
		return nil
	}
	defer source.Reader.Close()
	raw, err := readAllLimited(source.Reader, "FB2 document", maxFB2DocumentBytes)
	if err != nil {
		return nil
	}
	_, normalization, err := normalizeFB2XMLBytes(raw)
	if err != nil {
		return nil
	}

	var out []Diagnostic
	declared := FB2ContainerForExtension(BookExtension(filename))
	if source.Container != FB2ContainerNone && source.Container != declared {
		ext := strings.ToLower(BookExtension(filename))
		filenameShape := "a filename without a container suffix"
		if ext != "" {
			filenameShape = fmt.Sprintf("filename extension %q", ext)
		}
		out = append(out, Diagnostic{
			Code:    "fb2.content_detected_container",
			Message: fmt.Sprintf("Detected a %s-compressed FB2 from content despite %s.", source.Container, filenameShape),
		})
	}
	if normalization.removedInvalidControls {
		out = append(out, Diagnostic{
			Code:    "fb2.removed_invalid_xml_controls",
			Message: "Removed XML 1.0-forbidden control bytes before parsing FB2 XML.",
		})
	}
	if normalization.legacyEncoding {
		out = append(out, Diagnostic{
			Code:    "fb2.legacy_encoding_fallback",
			Message: "Decoded FB2 through a bounded legacy single-byte encoding fallback.",
		})
	}
	if normalization.repairedAmpersand {
		out = append(out, Diagnostic{
			Code:    "fb2.unescaped_ampersand_repaired",
			Message: "Escaped an unencoded prose ampersand before parsing FB2 XML.",
		})
	}
	return out
}

func inspectKindleDiagnostics(r io.ReaderAt, size int64, kind Format) []Diagnostic {
	info, err := InspectKindle(r, size, kind)
	if err != nil || info == nil || info.Container != kindlePalmDBContainerMOBI {
		return nil
	}
	if kindleCodepageHasNativeDecoder(info.Codepage) {
		return nil
	}
	return []Diagnostic{{
		Code:    "kindle.codepage_fallback",
		Message: fmt.Sprintf("Decoded unsupported MOBI codepage %d with the Windows-1252 fallback.", info.Codepage),
	}}
}

func isKindleDiagnosticFormat(kind Format) bool {
	switch kind {
	case FormatMOBI, FormatAZW, FormatAZW3, FormatAZW4, FormatPRC, FormatPDB:
		return true
	default:
		return false
	}
}

func kindleCodepageHasNativeDecoder(codepage uint32) bool {
	return codepage == 65001 || codepage >= 1250 && codepage <= 1258
}
