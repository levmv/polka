package format

import (
	"encoding/hex"
	"testing"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
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

func TestDecodeLegacyHexWrappedTextCharsets(t *testing.T) {
	for _, tt := range []struct {
		name     string
		text     string
		encoding encoding.Encoding
	}{
		{name: "Windows-1251", text: "Старое название книги", encoding: charmap.Windows1251},
		{name: "KOI8-R", text: "Старое название книги", encoding: charmap.KOI8R},
		{name: "Windows-1253", text: "Παλιός τίτλος βιβλίου", encoding: charmap.Windows1253},
		{name: "Windows-1255", text: "שם ישן של הספר", encoding: charmap.Windows1255},
		{name: "Shift JIS", text: "これは古い本のタイトルです。図書館で本を読むことが好きです。", encoding: japanese.ShiftJIS},
		{name: "EUC-JP", text: "これは古い本のタイトルです。図書館で本を読むことが好きです。", encoding: japanese.EUCJP},
		{name: "GB18030", text: "这是一本旧书的标题，我们在图书馆里阅读这本书。", encoding: simplifiedchinese.GB18030},
		{name: "Big5", text: "這是一本舊書的標題，我們在圖書館裡閱讀這本書。", encoding: traditionalchinese.Big5},
		{name: "EUC-KR", text: "이것은 오래된 책의 제목이며 도서관에서 읽는 책입니다.", encoding: korean.EUCKR},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := tt.encoding.NewEncoder().Bytes([]byte(tt.text))
			if err != nil {
				t.Fatalf("encode source: %v", err)
			}
			wrapped := "<" + hex.EncodeToString(raw) + ">"
			if got := decodeLegacyHexWrappedText(wrapped); got != tt.text {
				t.Fatalf("decodeLegacyHexWrappedText = %q; want %q", got, tt.text)
			}
		})
	}
}

func TestDecodeLegacyHexWrappedTextPreservesAmbiguousText(t *testing.T) {
	raw, err := charmap.Windows1251.NewEncoder().Bytes([]byte("Legacy Книга"))
	if err != nil {
		t.Fatal(err)
	}
	wrapped := "<" + hex.EncodeToString(raw) + ">"
	if got := decodeLegacyHexWrappedText(wrapped); got != wrapped {
		t.Fatalf("decodeLegacyHexWrappedText = %q; want original %q", got, wrapped)
	}
}
