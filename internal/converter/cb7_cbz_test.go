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

func TestConvertCB7ToCBZ(t *testing.T) {
	src := testfixture.CB7()
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatCB7, int64(len(src)), TargetCBZ); err != nil {
		t.Fatalf("ConvertContext: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatalf("open converted CBZ: %v", err)
	}
	if got := zipEntryNames(zr.File); len(got) != 3 || got[0] != "1.png" || got[1] != "2.png" || got[2] != "ComicInfo.xml" {
		t.Fatalf("converted entries = %#v; want normalized pages and ComicInfo", got)
	}
	pages, err := format.ListCBZPages(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil || len(pages) != 2 {
		t.Fatalf("ListCBZPages = %#v, %v; want two pages", pages, err)
	}
	meta, err := format.ExtractCBZMetadata(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil || meta.Title != "CB7 Fixture" {
		t.Fatalf("converted metadata = %#v, %v; want preserved ComicInfo", meta, err)
	}
}

func TestConvertCB7ToCBZUsesSharedResourceBudget(t *testing.T) {
	src := testfixture.CB7()
	limits := defaultConversionLimits
	limits.decodedBytes = 8
	var out bytes.Buffer
	err := convertContextWithLimits(context.Background(), &out, bytes.NewReader(src), format.FormatCB7, int64(len(src)), TargetCBZ, ConversionOptions{}, limits)
	if !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("conversion error = %v; want ErrResourceLimit", err)
	}
}
