package db

import (
	"strings"
	"testing"
)

func TestDeviceTokenGenerationAndTranscription(t *testing.T) {
	token := newDeviceToken()
	if len(token) != 12 || strings.Trim(token, deviceTokenAlphabet) != "" {
		t.Fatalf("noncanonical generated token: %q", token)
	}

	const canonical = "01abcdefghjk"
	for _, tt := range []struct {
		name string
		raw  string
		want string
	}{
		{"canonical", canonical, canonical},
		{"uppercase", "01ABCDEFGHJK", canonical},
		{"similar letters", "OIabcdefghjk", canonical},
		{"lowercase l", "olabcdefghjk", canonical},
		{"empty", "", ""},
		{"too short", canonical[:11], ""},
		{"too long", canonical + "0", ""},
		{"grouped", "01ab-cdef-ghjk", ""},
		{"space", "01abcd fghjk", ""},
		{"hyphen", "01abcd-fghjk", ""},
		{"invalid letter", "01abcdefgujk", ""},
		{"punctuation", "01ab.cdefghjk", ""},
		{"non ASCII", "01abcdéfghjk", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeDeviceToken(tt.raw); got != tt.want {
				t.Fatalf("normalizeDeviceToken(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
