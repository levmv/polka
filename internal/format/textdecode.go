package format

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/levmv/chardet"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/ianaindex"
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

// DecodeTextToUTF8 decodes plain prose text without trying to become a broad
// charset detector. BOMs and valid UTF-8 win. Only invalid UTF-8 falls back to a
// tiny single-byte set that covers the common legacy text cases.
func DecodeTextToUTF8(raw []byte) string {
	switch {
	case bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}):
		return stringsToValidUTF8(raw[3:])
	case bytes.HasPrefix(raw, []byte{0xff, 0xfe}):
		return decodeUTF16(raw[2:], binary.LittleEndian)
	case bytes.HasPrefix(raw, []byte{0xfe, 0xff}):
		return decodeUTF16(raw[2:], binary.BigEndian)
	case utf8.Valid(raw):
		return string(raw)
	}
	return decodeLegacySingleByteText(raw)
}

// decodeLegacyHexWrappedText recovers text from broken metadata produced by
// some document-to-PDF pipelines. They serialize the source bytes as a PDF hex
// string, then store that serialized value (including < and >) as ordinary
// literal or XML text. Keep this fallback deliberately narrow: the wrapper
// must cover the whole value, the hex must be bounded and complete, and the
// decoded value must look like prose rather than an identifier or binary data.
func decodeLegacyHexWrappedText(text string) string {
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

	decoded, ok := decodeUndeclaredPDFText(raw)
	if !ok {
		return text
	}
	if !plausibleDecodedMetadataText(decoded) {
		return text
	}
	return decoded
}

// decodeUndeclaredPDFText accepts exact Unicode encodings and legacy encodings
// for which the detector has strong, unambiguous evidence. This is intentionally
// separate from DecodeTextToUTF8: broad detection is appropriate for the
// known-broken wrapped metadata case, not for every text file Polka reads.
func decodeUndeclaredPDFText(raw []byte) (string, bool) {
	switch {
	case bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}):
		return DecodeTextToUTF8(raw), true
	case bytes.HasPrefix(raw, []byte{0xff, 0xfe, 0x00, 0x00}),
		bytes.HasPrefix(raw, []byte{0x00, 0x00, 0xfe, 0xff}):
		decoded, err := unicodeutf32.UTF32(unicodeutf32.BigEndian, unicodeutf32.ExpectBOM).
			NewDecoder().Bytes(raw)
		if err == nil {
			return string(decoded), true
		}
	case bytes.HasPrefix(raw, []byte{0xff, 0xfe}), bytes.HasPrefix(raw, []byte{0xfe, 0xff}):
		return DecodeTextToUTF8(raw), true
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

func decodeUTF16(raw []byte, order binary.ByteOrder) string {
	units := make([]uint16, 0, len(raw)/2)
	for len(raw) >= 2 {
		units = append(units, order.Uint16(raw[:2]))
		raw = raw[2:]
	}
	if len(raw) > 0 {
		return string(append(utf16.Decode(units), '\uFFFD'))
	}
	return string(utf16.Decode(units))
}

func decodeLegacySingleByteText(raw []byte) string {
	latin := decodeCharmap(raw, charmap.Windows1252)
	cyrillic := decodeCharmap(raw, charmap.Windows1251)
	if legacyTextScore(cyrillic) > legacyTextScore(latin) {
		return cyrillic
	}
	return latin
}

func decodeCharmap(raw []byte, enc *charmap.Charmap) string {
	decoded, _ := enc.NewDecoder().Bytes(raw) // Charmap decoders map every byte.
	return string(decoded)
}

func legacyTextScore(text string) int {
	score := 0
	cyrillic := 0
	cyrillicRun := 0
	longestCyrillicRun := 0
	for _, r := range text {
		if r == '\uFFFD' {
			score -= 20
			cyrillicRun = 0
			continue
		}
		if unicode.IsControl(r) {
			score -= 10
			cyrillicRun = 0
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			score += 3
		}
		if unicode.Is(unicode.Cyrillic, r) {
			cyrillic++
			cyrillicRun++
			if cyrillicRun > longestCyrillicRun {
				longestCyrillicRun = cyrillicRun
			}
		} else {
			cyrillicRun = 0
		}
		if unicode.IsSpace(r) {
			score += 2
		}
		if unicode.IsPunct(r) {
			score++
		}
	}
	if cyrillic > 0 {
		if longestCyrillicRun >= 3 {
			score += cyrillic * 4
		} else {
			score -= cyrillic * 6
		}
	}
	return score
}

func stringsToValidUTF8(raw []byte) string {
	return string(bytes.ToValidUTF8(raw, []byte("\uFFFD")))
}
