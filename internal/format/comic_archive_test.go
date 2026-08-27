package format

import (
	"bytes"
	"testing"

	"github.com/levmv/polka/internal/testfixture"
)

func TestDetectFormatComicArchives(t *testing.T) {
	for _, tt := range []struct {
		name string
		data []byte
		want Format
	}{
		{name: "comic.cbr", data: testfixture.CBR3(), want: FormatCBR},
		{name: "comic.cbr", data: testfixture.CBR5(), want: FormatCBR},
		{name: "comic.cb7", data: testfixture.CB7(), want: FormatCB7},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := bytes.NewReader(tt.data)
			if got := DetectFormat(tt.name, r, r.Size()); got != tt.want {
				t.Fatalf("DetectFormat = %v; want %v", got, tt.want)
			}
		})
	}
}

func TestDetectFormatComicArchivesRejectExtensionOnlyFiles(t *testing.T) {
	for _, tt := range []struct {
		name string
		data []byte
	}{
		{name: "comic.cbr", data: []byte("not rar")},
		{name: "comic.cb7", data: []byte("not 7z")},
		{name: "comic.cb7", data: testfixture.CBR3()},
		{name: "comic.cbr", data: testfixture.CB7()},
		{name: "comic.rar", data: testfixture.CBR3()},
		{name: "comic.7z", data: testfixture.CB7()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := bytes.NewReader(tt.data)
			if got := DetectFormat(tt.name, r, r.Size()); got != FormatUnknown {
				t.Fatalf("DetectFormat = %v; want FormatUnknown", got)
			}
		})
	}
}
