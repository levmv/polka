package format

import (
	"testing"

	"golang.org/x/text/encoding/charmap"
)

func TestDecodeTextToUTF8(t *testing.T) {
	for _, tt := range []struct {
		name    string
		src     string
		encoder *charmap.Charmap
	}{
		{name: "UTF-8", src: "Café\nПривет\n"},
		{name: "Windows-1251", src: "Привет, мир.\n", encoder: charmap.Windows1251},
		{name: "Windows-1252", src: "Curly “quotes”, café, résumé, and naïve prose.\n", encoder: charmap.Windows1252},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := []byte(tt.src)
			if tt.encoder != nil {
				encoded, err := tt.encoder.NewEncoder().String(tt.src)
				if err != nil {
					t.Fatalf("encode source: %v", err)
				}
				input = []byte(encoded)
			}
			if got := DecodeTextToUTF8(input); got != tt.src {
				t.Fatalf("DecodeTextToUTF8 = %q; want %q", got, tt.src)
			}
		})
	}
}
