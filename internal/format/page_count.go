package format

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

const maxPageCountBytes = 256 << 20

// IsPageCountApproximate describes the display policy for a detected format.
// Reflowable counts remain estimates, including values read from metadata.
func IsPageCountApproximate(kind Format) bool {
	switch kind {
	case FormatPDF, FormatDJVU, FormatCBZ, FormatCBR, FormatCB7:
		return false
	default:
		return true
	}
}

// ReadyPageCount reads declared or cheaply available native counts without
// estimating text layout. FB2 supplies counts through its combined import
// extractor. EPUB page maps need CountPages' content estimate;
// unsupported PDF structures need PDFium at the caller's IO boundary.
func ReadyPageCount(r io.ReaderAt, size int64, kind Format) (int, error) {
	switch kind {
	case FormatPDF:
		if structure := openPDFStructure(r, size); structure != nil {
			return structure.pageCount(), nil
		}
		return 0, nil
	case FormatCBZ:
		zr, err := zip.NewReader(r, size)
		if err != nil {
			return 0, err
		}
		// Candidate selection retains cheap signature detection for misnamed
		// images. Counting does not need to decode every page's dimensions.
		images, _, err := scanCBZ(zr)
		return len(images), err
	case FormatCBR:
		index, err := readCBRIndex(r, size)
		if err != nil {
			return 0, err
		}
		return len(index.pages), nil
	case FormatCB7:
		index, err := readCB7Index(r, size)
		if err != nil {
			return 0, err
		}
		return len(index.pages), nil
	case FormatDJVU:
		return djvuPageCount(r, size)
	case FormatEPUB, FormatKEPUB, FormatDOCX, FormatDOCM, FormatODT, FormatRTF:
		meta, err := ExtractMetadata(r, size, kind)
		if meta != nil {
			return meta.PageCount, err
		}
		return 0, err
	}
	return 0, nil
}

// CountPages returns the file's declared/native count, or a reference-layout
// estimate. Zero means no count could be obtained.
func CountPages(ctx context.Context, r io.ReaderAt, size int64, kind Format) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	switch kind {
	case FormatEPUB, FormatKEPUB:
		return estimateEPUBPages(ctx, r, size)
	case FormatFB2:
		source, err := OpenFB2Source(r, size, "")
		if err != nil {
			return 0, err
		}
		defer source.Reader.Close()
		doc, err := decodeFB2ReaderContext(ctx, source.Reader)
		if err != nil {
			return 0, err
		}
		return fb2PageCount(doc), nil
	case FormatPDF, FormatCBZ, FormatCBR, FormatCB7, FormatDJVU:
		return ReadyPageCount(r, size, kind)
	case FormatDOCX, FormatDOCM, FormatODT:
		if n, err := ReadyPageCount(r, size, kind); n > 0 || err != nil {
			return n, err
		}
		return officePageCount(ctx, r, size, kind)
	case FormatMOBI, FormatAZW, FormatAZW3, FormatAZW4, FormatPRC, FormatPDB:
		doc, err := ExtractKindleDocument(r, size, kind)
		if err != nil {
			return 0, err
		}
		return kindlePageCount(ctx, doc)
	case FormatTXT, FormatMarkdown, FormatTextile, FormatHTML, FormatXHTML, FormatTXTZ:
		var raw []byte
		var err error
		if kind == FormatTXTZ {
			raw, kind, err = ReadTXTZTextContextLimited(ctx, r, size, maxPageCountBytes)
		} else {
			raw, err = readAllLimited(io.NewSectionReader(r, 0, size), "page count", maxPageCountBytes)
		}
		if err != nil {
			return 0, err
		}
		var extent pageExtent
		if kind == FormatHTML || kind == FormatXHTML {
			if err := htmlPageExtent(ctx, raw, &extent, nil); err != nil {
				return 0, err
			}
		} else {
			if err := plainPageExtent(ctx, DecodeTextToUTF8(raw), &extent); err != nil {
				return 0, err
			}
		}
		return extent.Pages(), nil
	case FormatHTMLZ:
		zr, err := zip.NewReader(r, size)
		if err != nil {
			return 0, err
		}
		entry := HTMLZIndexEntry(zr)
		if entry == nil {
			return 0, nil
		}
		raw, err := readZipFileLimited(entry, maxPageCountBytes)
		if err != nil {
			return 0, err
		}
		var extent pageExtent
		if err := htmlPageExtent(ctx, raw, &extent, nil); err != nil {
			return 0, err
		}
		return extent.Pages(), nil
	case FormatRTF:
		if n, err := ReadyPageCount(r, size, kind); n > 0 || err != nil {
			return n, err
		}
		return rtfPageCount(ctx, r, size)
	}
	return 0, nil
}

func plainPageExtent(ctx context.Context, text string, extent *pageExtent) error {
	for line := range strings.SplitSeq(text, "\n") {
		if err := ctx.Err(); err != nil {
			return err
		}
		if strings.TrimSpace(line) == "" {
			extent.Break()
		} else {
			extent.Text(line)
			extent.Text(" ")
		}
	}
	return nil
}

func djvuPageCount(r io.ReaderAt, size int64) (int, error) {
	if !isDJVU(r, size) {
		return 0, fmt.Errorf("not a DjVu document")
	}
	var header [16]byte
	if _, err := r.ReadAt(header[:], 0); err != nil {
		return 0, err
	}
	end := int64(12) + int64(binary.BigEndian.Uint32(header[8:12]))
	if end > size || end < 16 {
		return 0, fmt.Errorf("truncated DjVu document")
	}
	if string(header[12:]) == "DJVU" {
		return 1, nil
	}
	pages := 0
	for off := int64(16); off+8 <= end; {
		if _, err := r.ReadAt(header[:8], off); err != nil {
			return 0, err
		}
		n := int64(binary.BigEndian.Uint32(header[4:8]))
		if n > end-off-8 {
			return 0, fmt.Errorf("truncated DjVu chunk")
		}
		if string(header[:4]) == "FORM" && n >= 4 {
			if _, err := r.ReadAt(header[8:12], off+8); err != nil {
				return 0, err
			}
			if string(header[8:12]) == "DJVU" {
				pages++
			}
		}
		off += 8 + n + n%2
	}
	// An indirect DJVM has no embedded page FORMs. DIRM also lists shared
	// resources, so its component count must never be presented as page count.
	return pages, nil
}

func officePageCount(ctx context.Context, r io.ReaderAt, size int64, kind Format) (int, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return 0, err
	}
	name := "content.xml"
	if kind != FormatODT {
		name = docxMainDocumentName(zr)
	}
	entry := zipEntryByName(zr, name)
	if entry == nil {
		return 0, nil
	}
	raw, err := readZipFileLimited(entry, maxPageCountBytes)
	if err != nil {
		return 0, err
	}
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var extent pageExtent
	textDepth, depth := 0, 0
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		token, err := dec.Token()
		if err == io.EOF {
			return extent.Pages(), nil
		}
		if err != nil {
			return 0, err
		}
		switch t := token.(type) {
		case xml.StartElement:
			depth++
			if depth > 256 {
				return 0, fmt.Errorf("page count: Office nesting exceeds 256")
			}
			if kind == FormatODT && t.Name.Local == "binary-data" {
				if err := dec.Skip(); err != nil {
					return 0, err
				}
				depth--
				continue
			}
			if t.Name.Local == "p" || kind == FormatODT && t.Name.Local == "h" {
				extent.Break()
			}
			if kind != FormatODT && (t.Name.Local == "br" || t.Name.Local == "cr") || kind == FormatODT && t.Name.Local == "line-break" {
				extent.Break()
			}
			if t.Name.Local == "tab" || kind == FormatODT && t.Name.Local == "s" {
				extent.Text(" ")
			}
			if kind != FormatODT && t.Name.Local == "t" || kind == FormatODT && (t.Name.Local == "p" || t.Name.Local == "h") {
				if textDepth == 0 {
					textDepth = depth
				}
			}
		case xml.EndElement:
			if t.Name.Local == "p" || kind == FormatODT && t.Name.Local == "h" {
				extent.Break()
			}
			if depth == textDepth {
				textDepth = 0
			}
			depth--
		case xml.CharData:
			if textDepth > 0 {
				extent.Text(string(t))
			}
		}
	}
}
