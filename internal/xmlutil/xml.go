package xmlutil

import "bytes"

// ValidXML10Char implements XML 1.0's Char production. Callers still choose
// whether an invalid code point is best deleted or replaced for their surface.
func ValidXML10Char(r rune) bool {
	return r == '\t' || r == '\n' || r == '\r' ||
		(r >= 0x20 && r <= 0xd7ff) ||
		(r >= 0xe000 && r <= 0xfffd) ||
		(r >= 0x10000 && r <= 0x10ffff)
}

// RemoveInvalidXML10Chars removes code points that XML 1.0 cannot represent.
// raw must already be valid UTF-8.
func RemoveInvalidXML10Chars(raw []byte) []byte {
	return bytes.Map(func(r rune) rune {
		if ValidXML10Char(r) {
			return r
		}
		return -1
	}, raw)
}

// RemoveInvalidXML10ControlBytes removes the forbidden ASCII control bytes
// while preserving every non-ASCII byte. It is safe to run before an XML
// document's declared single-byte encoding has been decoded.
func RemoveInvalidXML10ControlBytes(raw []byte) ([]byte, bool) {
	removed := false
	out := make([]byte, 0, len(raw))
	for _, b := range raw {
		if b < 0x20 && b != '\t' && b != '\n' && b != '\r' {
			removed = true
			continue
		}
		out = append(out, b)
	}
	if !removed {
		return raw, false
	}
	return out, true
}
