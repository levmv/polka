package db

import (
	"crypto/rand"
	"strings"
)

// Lowercase Crockford base32 keeps device credentials easy to enter. Twelve
// independently random symbols provide 60 bits of entropy.
const deviceTokenAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"
const deviceTokenLength = 12

func newDeviceToken() string {
	var token [deviceTokenLength]byte
	rand.Read(token[:])
	for i, b := range token {
		token[i] = deviceTokenAlphabet[b&31]
	}
	return string(token[:])
}

// normalizeDeviceToken ignores letter case and accepts lookalike letters at
// the credential lookup boundary. Tokens always contain exactly twelve symbols.
func normalizeDeviceToken(raw string) string {
	if len(raw) != deviceTokenLength {
		return ""
	}
	var token [deviceTokenLength]byte
	for i := range token {
		b := raw[i]
		if b >= 'A' && b <= 'Z' {
			b += 'a' - 'A'
		}
		switch b {
		case 'o':
			b = '0'
		case 'i', 'l':
			b = '1'
		}
		if strings.IndexByte(deviceTokenAlphabet, b) == -1 {
			return ""
		}
		token[i] = b
	}
	return string(token[:])
}
