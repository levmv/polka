package format

import (
	"bytes"
	"encoding/binary"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
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
