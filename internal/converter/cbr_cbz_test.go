package converter

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/levmv/polka/internal/format"
	"github.com/levmv/polka/internal/testfixture"
)

func TestConvertCBR3AndCBR5ToCBZ(t *testing.T) {
	for _, tt := range []struct {
		name string
		data []byte
	}{
		{name: "RAR3", data: testfixture.CBR3()},
		{name: "RAR5", data: testfixture.CBR5()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := ConvertContext(context.Background(), &out, bytes.NewReader(tt.data), format.FormatCBR, int64(len(tt.data)), TargetCBZ); err != nil {
				t.Fatalf("ConvertContext: %v", err)
			}
			zr, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
			if err != nil {
				t.Fatalf("open converted CBZ: %v", err)
			}
			if len(zr.File) != 2 || zr.File[0].Name != "testfile.jpg" || zr.File[1].Name != "testfile.png" {
				t.Fatalf("converted entries = %#v; want JPEG and PNG", zipEntryNames(zr.File))
			}
			pages, err := format.ListCBZPages(bytes.NewReader(out.Bytes()), int64(out.Len()))
			if err != nil || len(pages) != 2 {
				t.Fatalf("ListCBZPages = %#v, %v; want two pages", pages, err)
			}

			_, sourceCover, sourceExt, err := format.ExtractCBRMetadataAndCover(bytes.NewReader(tt.data), int64(len(tt.data)))
			if err != nil {
				t.Fatalf("ExtractCBRMetadataAndCover: %v", err)
			}
			convertedCover, convertedExt, err := format.ExtractCBZCover(bytes.NewReader(out.Bytes()), int64(out.Len()))
			if err != nil || sourceExt != convertedExt || !bytes.Equal(sourceCover, convertedCover) {
				t.Fatalf("converted cover ext=%q bytes=%d err=%v; want preserved %q/%d", convertedExt, len(convertedCover), err, sourceExt, len(sourceCover))
			}
		})
	}
}

func TestConvertCBRToCBZUsesSharedResourceBudget(t *testing.T) {
	src := testfixture.CBR3()
	limits := defaultConversionLimits
	limits.decodedBytes = 8
	var out bytes.Buffer
	err := convertContextWithLimits(context.Background(), &out, bytes.NewReader(src), format.FormatCBR, int64(len(src)), TargetCBZ, ConversionOptions{}, limits)
	if !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("conversion error = %v; want ErrResourceLimit", err)
	}
}

func TestComicPageOutputNameUsesDetectedBytes(t *testing.T) {
	for _, tt := range []struct {
		name string
		ext  string
		want string
	}{
		{name: "page.png", ext: ".webp", want: "page.webp"},
		{name: "page.bin", ext: ".png", want: "page.png"},
		{name: "page.jpeg", ext: ".jpg", want: "page.jpeg"},
	} {
		if got := comicPageOutputName(tt.name, tt.ext); got != tt.want {
			t.Fatalf("comicPageOutputName(%q, %q) = %q; want %q", tt.name, tt.ext, got, tt.want)
		}
	}
}

func zipEntryNames(files []*zip.File) []string {
	names := make([]string, len(files))
	for i, file := range files {
		names[i] = file.Name
	}
	return names
}
