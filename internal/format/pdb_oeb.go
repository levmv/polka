package format

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"io"
	"strings"

	"golang.org/x/image/bmp"
	"golang.org/x/net/html"
)

const (
	maxPalmDOCMetadataText    = 64 << 10
	maxPalmDOCTranscodePixels = 80_000_000
)

func palmDOCEmbeddedMetadata(r io.ReaderAt, size int64, pdb palmDB) *Metadata {
	ranges, ok := pdb.recordRanges(r, size, palmDOCHeader)
	if !ok || len(ranges) < 2 {
		return nil
	}
	record0 := make([]byte, palmDOCHeader)
	if _, err := r.ReadAt(record0, ranges[0].start); err != nil || binary.BigEndian.Uint16(record0[8:10]) == 0 || binary.BigEndian.Uint16(record0[12:14]) != 0 {
		return nil
	}
	raw, ok := mobiReadRecord(r, ranges[1], maxPalmDOCMetadataText)
	if !ok {
		return nil
	}
	var err error
	switch binary.BigEndian.Uint16(record0[0:2]) {
	case mobiCompressionNone:
	case mobiCompressionPalmDOC:
		raw, err = palmDOCDecompress(raw, maxPalmDOCMetadataText)
		if err != nil {
			return nil
		}
	default:
		return nil
	}
	fragment, ok := palmDOCMetadataFragment([]byte(mobiDecode(raw, 1252)))
	if !ok {
		return nil
	}
	meta, err := ParseOPF(bytes.NewReader(fragment))
	if err != nil {
		return nil
	}
	return meta
}

func palmDOCMetadataFragment(raw []byte) ([]byte, bool) {
	start, end, ok := palmDOCMetadataRange(raw)
	if !ok {
		return nil, false
	}
	return raw[start:end], true
}

func palmDOCMetadataRange(raw []byte) (int, int, bool) {
	const open = "<metadata"
	for search := 0; search < len(raw); {
		start := palmDOCIndexASCIIFold(raw, search, open)
		if start < 0 {
			return 0, 0, false
		}
		afterName := start + len(open)
		if afterName < len(raw) && (isXMLSpace(raw[afterName]) || raw[afterName] == '>') {
			closeStart := palmDOCIndexASCIIFold(raw, afterName, "</metadata>")
			if closeStart < 0 {
				return 0, 0, false
			}
			end := closeStart + len("</metadata>")
			return start, end, true
		}
		search = afterName
	}
	return 0, 0, false
}

func palmDOCContentHTML(raw []byte) []byte {
	start, end, ok := palmDOCMetadataRange(raw)
	if !ok {
		return raw
	}
	// PalmDOC filepos references are byte offsets into this exact flow. Blank the
	// metadata markup instead of deleting it so every later target keeps its
	// original position while the HTML parser sees no user-visible metadata.
	out := bytes.Clone(raw)
	for i := start; i < end; i++ {
		if out[i] != '\r' && out[i] != '\n' && out[i] != '\t' {
			out[i] = ' '
		}
	}
	return out
}

func palmDOCIndexASCIIFold(raw []byte, start int, needle string) int {
	if start < 0 {
		start = 0
	}
	for i := start; i+len(needle) <= len(raw); i++ {
		matched := true
		for j := range len(needle) {
			left := raw[i+j]
			right := needle[j]
			if left >= 'A' && left <= 'Z' {
				left += 'a' - 'A'
			}
			if right >= 'A' && right <= 'Z' {
				right += 'a' - 'A'
			}
			if left != right {
				matched = false
				break
			}
		}
		if matched {
			return i
		}
	}
	return -1
}

func palmDOCFlowIsHTML(raw []byte) bool {
	z := html.NewTokenizer(bytes.NewReader(raw))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return false
		case html.TextToken:
			if strings.TrimSpace(string(z.Text())) != "" {
				return false
			}
		case html.CommentToken, html.DoctypeToken:
			continue
		case html.StartTagToken, html.SelfClosingTagToken:
			name, _ := z.TagName()
			return strings.EqualFold(string(name), "html")
		default:
			return false
		}
	}
}

func palmDOCImageResource(raw []byte) ([]byte, string, string, bool) {
	if ext, mediaType, ok := kindleImageResourceType(raw); ok {
		return raw, ext, mediaType, true
	}
	if !bytes.HasPrefix(raw, []byte("BM")) {
		return nil, "", "", false
	}
	config, err := bmp.DecodeConfig(bytes.NewReader(raw))
	if err != nil || config.Width <= 0 || config.Height <= 0 ||
		uint64(config.Width) > uint64(maxPalmDOCTranscodePixels)/uint64(config.Height) {
		return nil, "", "", false
	}
	img, err := bmp.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, "", "", false
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, "", "", false
	}
	return out.Bytes(), ".png", "image/png", true
}
