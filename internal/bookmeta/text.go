package bookmeta

import (
	"bytes"
	"encoding/hex"
	"unicode"
	"unicode/utf8"

	"github.com/levmv/chardet"
	"golang.org/x/text/encoding/ianaindex"
	unicodeencoding "golang.org/x/text/encoding/unicode"
	unicodeutf32 "golang.org/x/text/encoding/unicode/utf32"
)

const (
	maxLegacyHexWrappedTextBytes = 8 << 10
	// The detector uses confidence 10 for valid but too-short multibyte input;
	// that establishes possibility, not enough evidence to choose an encoding.
	minDetectedCharsetConfidence = 11
	// Confidence is heuristic rather than probabilistic. A small absolute gap
	// still leaves near-ties unchanged.
	minDetectedCharsetLead = 5
)

// DecodeLegacyHexWrappedText recovers text from broken metadata produced by
// some document-to-PDF pipelines. They serialize the source bytes as a PDF hex
// string, then store that serialized value (including < and >) as ordinary
// literal or XML text. Keep this fallback deliberately narrow: the wrapper
// must cover the whole value, the hex must be bounded and complete, and the
// decoded value must look like prose rather than an identifier or binary data.
func DecodeLegacyHexWrappedText(text string) string {
	if len(text) < 18 || text[0] != '<' || text[len(text)-1] != '>' {
		return text
	}

	encoded := text[1 : len(text)-1]
	if len(encoded)%2 != 0 || len(encoded) > maxLegacyHexWrappedTextBytes*2 {
		return text
	}

	raw := make([]byte, len(encoded)/2)
	if _, err := hex.Decode(raw, []byte(encoded)); err != nil {
		return text
	}

	decoded, ok := decodeMetadataText(raw)
	if !ok {
		return text
	}
	if !plausibleDecodedMetadataText(decoded) {
		return text
	}
	return decoded
}

// decodeMetadataText accepts exact Unicode encodings and legacy encodings
// for which the detector has strong, unambiguous evidence. This is intentionally
// restricted to known-broken wrapped metadata, not ordinary prose files.
func decodeMetadataText(raw []byte) (string, bool) {
	switch {
	case bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}):
		return string(raw[3:]), utf8.Valid(raw[3:])
	case bytes.HasPrefix(raw, []byte{0xff, 0xfe, 0x00, 0x00}),
		bytes.HasPrefix(raw, []byte{0x00, 0x00, 0xfe, 0xff}):
		decoded, err := unicodeutf32.UTF32(unicodeutf32.BigEndian, unicodeutf32.ExpectBOM).
			NewDecoder().Bytes(raw)
		if err == nil {
			return string(decoded), true
		}
	case bytes.HasPrefix(raw, []byte{0xff, 0xfe}), bytes.HasPrefix(raw, []byte{0xfe, 0xff}):
		decoded, err := unicodeencoding.UTF16(unicodeencoding.BigEndian, unicodeencoding.ExpectBOM).NewDecoder().Bytes(raw)
		return string(decoded), err == nil
	case utf8.Valid(raw):
		return string(raw), true
	}

	results, err := chardet.NewTextDetector().DetectAll(raw)
	if err != nil {
		return "", false
	}

	type candidate struct {
		text       string
		confidence int
	}
	candidates := make([]candidate, 0, len(results))
	for _, result := range results {
		if result.Confidence < minDetectedCharsetConfidence {
			break
		}
		encoding, err := ianaindex.IANA.Encoding(result.Charset)
		if err != nil || encoding == nil {
			continue
		}
		decoded, err := encoding.NewDecoder().Bytes(raw)
		decodedText := string(decoded)
		if err != nil || !utf8.Valid(decoded) || !plausibleDecodedMetadataText(decodedText) {
			continue
		}
		encoded, err := encoding.NewEncoder().Bytes(decoded)
		if err != nil || !bytes.Equal(encoded, raw) {
			continue
		}

		duplicate := false
		for _, existing := range candidates {
			if existing.text == decodedText {
				duplicate = true
				break
			}
		}
		if !duplicate {
			candidates = append(candidates, candidate{text: decodedText, confidence: result.Confidence})
		}
	}
	if len(candidates) == 0 {
		return "", false
	}
	if len(candidates) > 1 && candidates[0].confidence-candidates[1].confidence < minDetectedCharsetLead {
		return "", false
	}
	return candidates[0].text, true
}

func plausibleDecodedMetadataText(decoded string) bool {
	hasText := false
	hasSpace := false
	hasNonASCIIText := false
	for _, r := range decoded {
		if r == utf8.RuneError || !unicode.IsPrint(r) && !unicode.IsSpace(r) {
			return false
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			hasText = true
			if r > unicode.MaxASCII {
				hasNonASCIIText = true
			}
		}
		if unicode.IsSpace(r) {
			hasSpace = true
		}
	}
	return hasText && (hasSpace || hasNonASCIIText)
}
