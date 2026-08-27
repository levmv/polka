package format

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestKindleResourceBudgetBoundsAggregateDataAndCount(t *testing.T) {
	tests := []struct {
		name   string
		budget kindleResourceBudget
		first  []byte
		second []byte
	}{
		{
			name:   "resource count",
			budget: kindleResourceBudget{maxBytes: 10, maxResources: 1},
			first:  []byte("a"),
			second: []byte("b"),
		},
		{
			name:   "decoded bytes",
			budget: kindleResourceBudget{maxBytes: 3, maxResources: 2},
			first:  []byte("ab"),
			second: []byte("cd"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.budget.add(tt.first); err != nil {
				t.Fatalf("first resource: %v", err)
			}
			if err := tt.budget.add(tt.second); !errors.Is(err, ErrKindleResourceLimit) {
				t.Fatalf("second resource error = %v; want ErrKindleResourceLimit", err)
			}
		})
	}
}

func TestExtractMOBIMetadataFromHeaderAndEXTH(t *testing.T) {
	data := testMOBIFile(65001, "Short PDB Title", []testEXTHRecord{
		{typ: 503, value: []byte("Long Kindle Title")},
		{typ: 100, value: []byte("Doe, Jane")},
		{typ: 101, value: []byte("MOBI Press")},
		{typ: 103, value: []byte("A short description.")},
		{typ: 104, value: []byte("9780306406157")},
		{typ: 105, value: []byte("Sci-Fi, Classics;\nSpace Adventure; Sci-Fi, Classics")},
		{typ: 106, value: []byte("2020-05-03T00:00:00Z")},
		{typ: 113, value: []byte("B000TESTID")},
		{typ: 524, value: []byte("eng")},
	})
	r := bytes.NewReader(data)

	meta, err := ExtractMOBIMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractMOBIMetadata: %v", err)
	}
	if meta.Title != "Long Kindle Title" {
		t.Fatalf("Title = %q; want EXTH long title", meta.Title)
	}
	if len(meta.Authors) != 1 || meta.Authors[0].Name != "Jane Doe" || meta.Authors[0].SortName != "Doe, Jane" {
		t.Fatalf("Authors = %+v; want Jane Doe with sort", meta.Authors)
	}
	if meta.Publisher != "MOBI Press" {
		t.Fatalf("Publisher = %q", meta.Publisher)
	}
	if meta.Description != "A short description." {
		t.Fatalf("Description = %q", meta.Description)
	}
	if meta.Date != "2020-05-03" {
		t.Fatalf("Date = %q", meta.Date)
	}
	if meta.Language != "en" {
		t.Fatalf("Language = %q", meta.Language)
	}
	if meta.Identifier != "isbn:9780306406157, amazon:B000TESTID" {
		t.Fatalf("Identifier = %q", meta.Identifier)
	}
	if len(meta.Tags) != 2 || meta.Tags[0] != "Sci-Fi, Classics" || meta.Tags[1] != "Space Adventure" {
		t.Fatalf("Tags = %+v", meta.Tags)
	}
}

func TestExtractMOBIMetadataHeaderFallbackAndCP1252(t *testing.T) {
	data := testMOBIFile(1252, "Caf\xe9 MOBI", nil)
	r := bytes.NewReader(data)

	meta, err := ExtractMOBIMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractMOBIMetadata: %v", err)
	}
	if meta.Title != "Café MOBI" {
		t.Fatalf("Title = %q; want CP1252 decoded title", meta.Title)
	}
	if meta.Language != "en" {
		t.Fatalf("Language = %q; want header language fallback", meta.Language)
	}
}

func TestExtractMOBIMetadataCleansEXTHText(t *testing.T) {
	data := testMOBIFile(65001, "Short PDB Title", []testEXTHRecord{
		{typ: 503, value: []byte("Tom &amp; Jerry &#x2019; Caf&#xE9;\x01")},
		{typ: 100, value: []byte("O&#39;Brien, Anne\x02")},
		{typ: 101, value: []byte("Unknown")},
		{typ: 103, value: []byte("A &lt;b&gt;short&lt;/b&gt; description.")},
		{typ: 105, value: []byte("Drama &amp; Comedy; Old&#x20;Books")},
	})
	r := bytes.NewReader(data)

	meta, err := ExtractMOBIMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractMOBIMetadata: %v", err)
	}
	if meta.Title != "Tom & Jerry ’ Café" {
		t.Fatalf("Title = %q; want decoded entities and stripped controls", meta.Title)
	}
	if len(meta.Authors) != 1 || meta.Authors[0].Name != "Anne O'Brien" || meta.Authors[0].SortName != "O'Brien, Anne" {
		t.Fatalf("Authors = %+v; want decoded EXTH author", meta.Authors)
	}
	if meta.Publisher != "" {
		t.Fatalf("Publisher = %q; want Unknown publisher dropped", meta.Publisher)
	}
	if meta.Description != "A <b>short</b> description." {
		t.Fatalf("Description = %q; want decoded EXTH description", meta.Description)
	}
	wantTags := []string{"Drama & Comedy", "Old Books"}
	if !equalStrings(meta.Tags, wantTags) {
		t.Fatalf("Tags = %+v; want %+v", meta.Tags, wantTags)
	}
}

func TestExtractMOBIMetadataPalmDBTitleFallback(t *testing.T) {
	for _, tt := range []struct {
		name  string
		title string
		want  string
	}{
		{name: "useful name", title: "Palm DB Title", want: "Palm DB Title"},
		{name: "generic name", title: "Unknown"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := testMOBIFileWithOptions(testMOBIOptions{
				codepage:   1252,
				palmDBName: tt.title,
			})
			r := bytes.NewReader(data)
			meta, err := ExtractMOBIMetadata(r, r.Size())
			if err != nil {
				t.Fatalf("ExtractMOBIMetadata: %v", err)
			}
			if meta.Title != tt.want {
				t.Fatalf("Title = %q; want %q", meta.Title, tt.want)
			}
		})
	}
}

func TestExtractMOBIMetadataFallsBackToLegacyUnknownTitlePage(t *testing.T) {
	text := []byte(`<html xmlns="http://www.w3.org/1999/xhtml">
  <head><title>Unknown</title></head>
  <body>
    <div><h1>Unknown</h1><h3>by Unknown</h3></div>
    <div>
      <p>GRANTCHESTER GRIND</p>
      <p>By</p>
      <p>Tom Sharpe</p>
      <p>Copyright &copy; 1995</p>
      <p>A dedication follows.</p>
    </div>
  </body>
</html>`)
	data := testMOBIFileWithOptions(testMOBIOptions{
		codepage:    65001,
		palmDBName:  "Unknown",
		textRecords: [][]byte{text},
		textLength:  uint32(len(text)),
	})
	r := bytes.NewReader(data)

	meta, err := ExtractMOBIMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractMOBIMetadata: %v", err)
	}
	if meta.Title != "GRANTCHESTER GRIND" {
		t.Fatalf("Title = %q; want legacy title-page title", meta.Title)
	}
	if len(meta.Authors) != 1 || meta.Authors[0].Name != "Tom Sharpe" || meta.Authors[0].Role != "aut" {
		t.Fatalf("Authors = %+v; want legacy title-page author", meta.Authors)
	}
}

func TestLegacyUnknownTitlePageMetadataRequiresCompleteProducerShape(t *testing.T) {
	valid := `<html><head><title>Unknown</title></head><body>
<div><h1>Unknown</h1><h3>by Unknown</h3></div>
<div><p>Book Title</p><p>By</p><p>Book Author</p><p>Copyright 2001</p></div>
</body></html>`
	for _, tt := range []struct {
		name string
		src  string
	}{
		{name: "useful document title", src: strings.Replace(valid, "<title>Unknown</title>", "<title>Chapter One</title>", 1)},
		{name: "no placeholder block", src: strings.Replace(valid, "<h1>Unknown</h1>", "<h1>Preface</h1>", 1)},
		{name: "no exact by separator", src: strings.Replace(valid, "<p>By</p>", "<p>Written by</p>", 1)},
		{name: "no copyright witness", src: strings.Replace(valid, "<p>Copyright 2001</p>", "<p>First chapter</p>", 1)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if title, author, ok := mobiLegacyTitlePageMetadata(tt.src); ok {
				t.Fatalf("metadata = %q / %+v; want narrow fallback rejected", title, author)
			}
		})
	}
}

func TestExtractMOBICoverSelection(t *testing.T) {
	for _, tt := range []struct {
		name      string
		options   testMOBIOptions
		wantCover bool
	}{
		{
			name: "EXTH cover offset",
			options: testMOBIOptions{
				firstImageIndex: 2,
				records:         []testEXTHRecord{{typ: 201, value: testMOBIUint32(1)}},
				extraRecords:    [][]byte{[]byte("not the cover"), tinyPNG},
			},
			wantCover: true,
		},
		{
			name: "first image fallback",
			options: testMOBIOptions{
				firstImageIndex: 2,
				extraRecords:    [][]byte{tinyPNG},
			},
			wantCover: true,
		},
		{
			name: "invalid image",
			options: testMOBIOptions{
				firstImageIndex: 2,
				extraRecords:    [][]byte{[]byte("not an image")},
			},
		},
		{
			name: "fake cover marker",
			options: testMOBIOptions{
				firstImageIndex: 2,
				records: []testEXTHRecord{
					{typ: 201, value: testMOBIUint32(0)},
					{typ: 203, value: testMOBIUint32(1)},
				},
				extraRecords: [][]byte{tinyPNG},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.options.codepage = 65001
			tt.options.title = "MOBI cover test"
			data := testMOBIFileWithOptions(tt.options)
			r := bytes.NewReader(data)

			got, ext, err := ExtractMOBICover(r, r.Size())
			if err != nil {
				t.Fatalf("ExtractMOBICover: %v", err)
			}
			if tt.wantCover {
				if !bytes.Equal(got, tinyPNG) || ext != ".png" {
					t.Fatalf("cover = %d bytes, ext = %q; want tiny PNG", len(got), ext)
				}
			} else if got != nil || ext != "" {
				t.Fatalf("cover = %d bytes, ext = %q; want no cover", len(got), ext)
			}
		})
	}
}

func TestDetectMOBIKind(t *testing.T) {
	for _, tt := range []struct {
		name string
		data []byte
		want MOBIKind
	}{
		{
			name: "mobi6",
			data: testMOBIFile(65001, "MOBI6 Book", nil),
			want: MOBIKindMOBI6,
		},
		{
			name: "kf8 standalone",
			data: testMOBIRecord0Uint32(t,
				testMOBIRecord0Uint32(t, testMOBIFile(65001, "KF8 Book", nil), 20, 0x108),
				0xf8,
				2,
			),
			want: MOBIKindKF8Standalone,
		},
		{
			name: "combo",
			data: testMOBIFileWithOptions(testMOBIOptions{
				codepage: 65001,
				title:    "Combo Book",
				records: []testEXTHRecord{
					{typ: 121, value: testMOBIUint32(3)},
				},
				extraRecords: [][]byte{
					[]byte("BOUNDARY"),
					[]byte("kf8 placeholder"),
				},
			}),
			want: MOBIKindCombo,
		},
		{
			name: "palmdoc",
			data: testPalmDOCFile("PalmDOC Book"),
			want: MOBIKindPalmDOC,
		},
		{
			name: "unknown",
			data: []byte("not mobi"),
			want: MOBIKindUnknown,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := bytes.NewReader(tt.data)
			if got := DetectMOBIKind(r, r.Size()); got != tt.want {
				t.Fatalf("DetectMOBIKind = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestInspectKindleMOBIHeaderSignals(t *testing.T) {
	data := testMOBIFileWithOptions(testMOBIOptions{
		codepage:        65001,
		title:           "Combo Book",
		headerLength:    0x108,
		firstImageIndex: 2,
		records: []testEXTHRecord{
			{typ: 121, value: testMOBIUint32(3)},
			{typ: 501, value: []byte("EBOK")},
			{typ: 525, value: []byte("horizontal-lr")},
			{typ: 527, value: []byte("ltr")},
		},
		extraRecords: [][]byte{
			[]byte("BOUNDARY"),
			[]byte("OTTO font"),
			tinyPNG,
			[]byte("INDX index"),
		},
	})
	data = testMOBIRecord0Uint32(t, data, 0xf4, 4)
	data = testMOBIRecord0Uint32(t, data, 0xc0, 5)
	data = testMOBIRecord0Uint32(t, data, 0xc4, 2)
	data = testMOBIRecord0Uint32(t, data, 0xf8, 6)
	data = testMOBIRecord0Uint32(t, data, 0xfc, 7)
	data = testMOBIRecord0Uint32(t, data, 0x104, 8)
	r := bytes.NewReader(data)

	info, err := InspectKindle(r, r.Size(), FormatAZW3)
	if err != nil {
		t.Fatalf("InspectKindle: %v", err)
	}
	if info == nil {
		t.Fatal("InspectKindle returned nil")
	}
	if info.SourceClass != "mobi6+kf8-combo" || info.MOBIKind != MOBIKindCombo {
		t.Fatalf("class = %q, kind = %q; want combo", info.SourceClass, info.MOBIKind)
	}
	if info.Container != "bookmobi" || info.TypeCreator != "BOOKMOBI" {
		t.Fatalf("container = %q, type/creator = %q", info.Container, info.TypeCreator)
	}
	if info.CompressionName != "none" || info.Compression != 1 || info.TextRecords != 1 || info.RecordSize != 4096 {
		t.Fatalf("PalmDOC header facts = compression %s/%d text_records %d record_size %d", info.CompressionName, info.Compression, info.TextRecords, info.RecordSize)
	}
	if !info.HasEXTH || !equalUint32s(info.EXTHTypes, []uint32{121, 501, 525, 527}) {
		t.Fatalf("EXTH types = %+v", info.EXTHTypes)
	}
	if info.CDEType != "EBOK" || info.PrimaryWritingMode != "horizontal-lr" || info.PageProgressionDirection != "ltr" {
		t.Fatalf("EXTH signals: cdetype=%q writing=%q progression=%q", info.CDEType, info.PrimaryWritingMode, info.PageProgressionDirection)
	}
	if info.BoundaryIndex != 3 || info.NCXIndex != 4 || info.FDSTIndex != 5 || info.FDSTCount != 2 || info.FragmentIndex != 6 || info.SkeletonIndex != 7 || info.GuideIndex != 8 {
		t.Fatalf("indexes = boundary %d ncx %d fdst %d/%d frag %d skel %d guide %d", info.BoundaryIndex, info.NCXIndex, info.FDSTIndex, info.FDSTCount, info.FragmentIndex, info.SkeletonIndex, info.GuideIndex)
	}
	if info.ResourceCounts.Boundary != 1 || info.ResourceCounts.Fonts != 1 || info.ResourceCounts.Images != 1 || info.ResourceCounts.INDX != 1 || info.ResourceCounts.Other != 0 {
		t.Fatalf("resource counts = %+v", info.ResourceCounts)
	}
	if len(info.UnsupportedFeatures) != 0 {
		t.Fatalf("unsupported features = %+v; want none", info.UnsupportedFeatures)
	}
}

func TestInspectKindleReadsFDSTSections(t *testing.T) {
	data := testMOBIFileWithOptions(testMOBIOptions{
		codepage:    65001,
		title:       "FDST Book",
		mobiVersion: 8,
		extraRecords: [][]byte{
			testKindleFDSTRecord([2]uint32{0, 10}, [2]uint32{10, 25}),
		},
	})
	data = testMOBIRecord0Uint32(t, data, 0xc0, 2)
	data = testMOBIRecord0Uint32(t, data, 0xc4, 2)
	r := bytes.NewReader(data)

	info, err := InspectKindle(r, r.Size(), FormatAZW3)
	if err != nil {
		t.Fatalf("InspectKindle: %v", err)
	}
	if got, want := info.FDSTSections, []KindleFDSTSection{{Start: 0, End: 10}, {Start: 10, End: 25}}; !equalFDSTSections(got, want) {
		t.Fatalf("FDSTSections = %+v; want %+v", got, want)
	}
}

func TestInspectKindleReadsKF8SkeletonAndFragmentTables(t *testing.T) {
	skelRecords := testKindleSKELRecords()
	fragRecords := testKindleFragmentRecords()
	data := testMOBIFileWithOptions(testMOBIOptions{
		codepage:     65001,
		title:        "KF8 Structure",
		headerLength: 0x108,
		mobiVersion:  8,
		extraRecords: append(skelRecords, fragRecords...),
	})
	data = testMOBIRecord0Uint32(t, data, 0xfc, 2)
	data = testMOBIRecord0Uint32(t, data, 0xf8, 4)
	r := bytes.NewReader(data)

	info, err := InspectKindle(r, r.Size(), FormatAZW3)
	if err != nil {
		t.Fatalf("InspectKindle: %v", err)
	}
	if got, want := info.KF8Skeletons, []KindleKF8Skeleton{{
		Index:         0,
		Name:          "SKEL0000000000",
		FragmentCount: 1,
		Start:         0,
		Length:        100,
	}}; !equalKF8Skeletons(got, want) {
		t.Fatalf("KF8Skeletons = %+v; want %+v", got, want)
	}
	if got, want := info.KF8Fragments, []KindleKF8Fragment{{
		InsertOffset: 42,
		Selector:     "body > p:nth-of-type(1)",
		FileNumber:   0,
		Sequence:     7,
		Start:        0,
		Length:       25,
	}}; !equalKF8Fragments(got, want) {
		t.Fatalf("KF8Fragments = %+v; want %+v", got, want)
	}
	if len(info.UnsupportedFeatures) != 0 {
		t.Fatalf("UnsupportedFeatures = %+v; want none", info.UnsupportedFeatures)
	}
}

func TestParseKindleFDSTRecordRejectsMalformedData(t *testing.T) {
	for _, tt := range []struct {
		name string
		data []byte
	}{
		{name: "missing magic", data: []byte("NOPE")},
		{name: "bad offset", data: append(append([]byte("FDST"), testMOBIUint32(16)...), testMOBIUint32(0)...)},
		{name: "truncated table", data: append(append([]byte("FDST"), testMOBIUint32(12)...), testMOBIUint32(1)...)},
		{name: "inverted section", data: testKindleFDSTRecord([2]uint32{10, 2})},
		{name: "overlap", data: testKindleFDSTRecord([2]uint32{0, 10}, [2]uint32{9, 20})},
		{name: "trailing data", data: append(testKindleFDSTRecord([2]uint32{0, 10}), 1)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseKindleFDSTRecord(tt.data, 0); err == nil {
				t.Fatalf("parseKindleFDSTRecord succeeded; want error")
			}
		})
	}
}

func TestInspectKindleClassifiesSpecialCases(t *testing.T) {
	for _, tt := range []struct {
		name        string
		data        []byte
		kind        Format
		wantClass   string
		wantFeature string
	}{
		{
			name: "huff cdic",
			data: testMOBIFileWithOptions(testMOBIOptions{
				codepage:    65001,
				title:       "Compressed",
				compression: mobiCompressionHUFFCDIC,
			}),
			kind:        FormatMOBI,
			wantClass:   "mobi6",
			wantFeature: "huff-cdic-compression",
		},
		{
			name: "dictionary",
			data: testMOBIFileWithOptions(testMOBIOptions{
				codepage:        65001,
				title:           "Dictionary",
				firstImageIndex: 2,
				extraRecords:    [][]byte{[]byte("INFL index")},
			}),
			kind:        FormatMOBI,
			wantClass:   "dictionary",
			wantFeature: "dictionary-indexes",
		},
		{
			name: "sample book",
			data: testMOBIFileWithOptions(testMOBIOptions{
				codepage: 65001,
				title:    "Sample",
				records:  []testEXTHRecord{{typ: 501, value: []byte("EBSP")}},
			}),
			kind:      FormatMOBI,
			wantClass: "sample-book",
		},
		{
			name:        "azw4 pdf",
			data:        append(testMOBIFile(65001, "Print Replica", nil), []byte("%PDF-1.7\nbody\n%%EOF")...),
			kind:        FormatAZW4,
			wantClass:   "azw4-pdf-wrapper",
			wantFeature: "azw4-print-replica",
		},
		{
			name: "encrypted",
			data: testMOBIFileWithOptions(testMOBIOptions{
				codepage:   65001,
				title:      "DRM",
				encryption: 1,
			}),
			kind:        FormatMOBI,
			wantClass:   "encrypted",
			wantFeature: "encrypted",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := bytes.NewReader(tt.data)
			info, err := InspectKindle(r, r.Size(), tt.kind)
			if err != nil {
				t.Fatalf("InspectKindle: %v", err)
			}
			if info == nil {
				t.Fatal("InspectKindle returned nil")
			}
			if info.SourceClass != tt.wantClass {
				t.Fatalf("SourceClass = %q; want %q", info.SourceClass, tt.wantClass)
			}
			if tt.wantFeature != "" && !containsString(info.UnsupportedFeatures, tt.wantFeature) {
				t.Fatalf("UnsupportedFeatures = %+v; want %q", info.UnsupportedFeatures, tt.wantFeature)
			}
		})
	}
}

func TestInspectKindleDictionarySignalsAreStructural(t *testing.T) {
	exthSubject := testMOBIFileWithOptions(testMOBIOptions{
		codepage: 65001,
		title:    "Novel About Dictionaries",
		records: []testEXTHRecord{
			{typ: 105, value: []byte("Dictionaries")},
		},
	})
	mobiType := testMOBIRecord0Uint32(t, testMOBIFile(65001, "Old Dictionary", nil), 24, mobiTypeDictionary)

	for _, tt := range []struct {
		name       string
		data       []byte
		dictionary bool
	}{
		{name: "subject text only", data: exthSubject, dictionary: false},
		{name: "mobi type", data: mobiType, dictionary: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := bytes.NewReader(tt.data)
			info, err := InspectKindle(r, r.Size(), FormatMOBI)
			if err != nil {
				t.Fatalf("InspectKindle: %v", err)
			}
			if info == nil {
				t.Fatal("InspectKindle returned nil")
			}
			if info.Dictionary != tt.dictionary {
				t.Fatalf("Dictionary = %v; want %v", info.Dictionary, tt.dictionary)
			}
			if got := info.SourceClass == "dictionary"; got != tt.dictionary {
				t.Fatalf("SourceClass = %q; dictionary classification = %v, want %v", info.SourceClass, got, tt.dictionary)
			}
			if got := containsString(info.UnsupportedFeatures, "dictionary-indexes"); got != tt.dictionary {
				t.Fatalf("dictionary-indexes = %v in %+v; want %v", got, info.UnsupportedFeatures, tt.dictionary)
			}
		})
	}
}

func TestInspectKindlePalmDOCIncludesEncryptedShape(t *testing.T) {
	record0 := testPalmDOCRecord0(2)
	binary.BigEndian.PutUint16(record0[12:14], 1)
	data := testPalmDBFile("DRM PalmDOC", "TEXtREAd", record0)
	r := bytes.NewReader(data)

	info, err := InspectKindle(r, r.Size(), FormatPDB)
	if err != nil {
		t.Fatalf("InspectKindle: %v", err)
	}
	if info == nil {
		t.Fatal("InspectKindle returned nil")
	}
	if info.SourceClass != "encrypted-palmdoc" || info.Container != "palmdoc" || info.MOBIKind != MOBIKindPalmDOC {
		t.Fatalf("unexpected PalmDOC info: %+v", info)
	}
	if !containsString(info.UnsupportedFeatures, "encrypted") {
		t.Fatalf("UnsupportedFeatures = %+v; want encrypted", info.UnsupportedFeatures)
	}
}

func TestInspectKindleUnknownContainer(t *testing.T) {
	r := bytes.NewReader([]byte("not a PalmDB file"))
	info, err := InspectKindle(r, r.Size(), FormatUnknown)
	if err != nil {
		t.Fatalf("InspectKindle: %v", err)
	}
	if info != nil {
		t.Fatalf("InspectKindle = %+v; want nil", info)
	}
}

func TestExtractKindleDocumentMOBI6PalmDOC(t *testing.T) {
	text := []byte("<html><body><p>Hello Kindle</p></body></html>")
	data := testMOBIFileWithOptions(testMOBIOptions{
		codepage:        65001,
		title:           "Readable MOBI",
		compression:     mobiCompressionPalmDOC,
		headerLength:    0xe4,
		textRecords:     [][]byte{text},
		textLength:      uint32(len(text)),
		mobiVersion:     6,
		firstImageIndex: 2,
		records: []testEXTHRecord{
			{typ: 100, value: []byte("Doe, Jane")},
			{typ: 201, value: testMOBIUint32(0)},
			{typ: 501, value: []byte("EBOK")},
		},
		extraRecords: [][]byte{tinyPNG, []byte("FLIS control")},
	})
	r := bytes.NewReader(data)

	doc, err := ExtractKindleDocument(r, r.Size(), FormatMOBI)
	if err != nil {
		t.Fatalf("ExtractKindleDocument: %v", err)
	}
	if doc.SourceClass != "mobi6" || doc.MOBIKind != MOBIKindMOBI6 {
		t.Fatalf("class = %q, kind = %q; want MOBI6", doc.SourceClass, doc.MOBIKind)
	}
	if doc.Metadata == nil || doc.Metadata.Title != "Readable MOBI" || len(doc.Metadata.Authors) != 1 || doc.Metadata.Authors[0].Name != "Jane Doe" {
		t.Fatalf("metadata = %+v; want title and EXTH author", doc.Metadata)
	}
	if len(doc.Flows) != 1 {
		t.Fatalf("flows = %+v; want one flow", doc.Flows)
	}
	if flow := doc.Flows[0]; flow.ID != "flow-0001" || flow.Href != "text/flow-0001.html" || flow.MediaType != "text/html" || string(flow.Data) != string(text) {
		t.Fatalf("flow = %+v; want extracted HTML text", flow)
	}
	if len(doc.Resources) != 1 {
		t.Fatalf("resources = %+v; want one image resource", doc.Resources)
	}
	if res := doc.Resources[0]; res.ID != "res-00002" || res.Href != "images/00002.png" || res.MediaType != "image/png" || res.RecordIndex != 2 || !res.Cover || !bytes.Equal(res.Data, tinyPNG) {
		t.Fatalf("resource = %+v; want cover PNG at record 2", res)
	}
	if doc.CoverResourceID != "res-00002" {
		t.Fatalf("CoverResourceID = %q; want res-00002", doc.CoverResourceID)
	}
	if len(doc.UnsupportedFeatures) != 0 {
		t.Fatalf("UnsupportedFeatures = %+v; want none", doc.UnsupportedFeatures)
	}
}

func TestExtractKindleDocumentMOBI6Uncompressed(t *testing.T) {
	raw := []byte("<html><body><p>Caf\xe9 Kindle</p></body></html>")
	data := testMOBIFileWithOptions(testMOBIOptions{
		codepage:    1252,
		title:       "Uncompressed MOBI",
		compression: mobiCompressionNone,
		textRecords: [][]byte{raw},
		textLength:  uint32(len(raw)),
		mobiVersion: 6,
	})
	r := bytes.NewReader(data)

	doc, err := ExtractKindleDocument(r, r.Size(), FormatMOBI)
	if err != nil {
		t.Fatalf("ExtractKindleDocument: %v", err)
	}
	if got, want := string(doc.Flows[0].Data), "<html><body><p>Café Kindle</p></body></html>"; got != want {
		t.Fatalf("flow text = %q; want %q", got, want)
	}
}

func TestExtractKindleDocumentMOBI6MediaResources(t *testing.T) {
	text := []byte(`<html><body><img recindex="00001"><video mediarecindex="00002">Video fallback</video><audio mediarecindex="00003">Audio fallback</audio></body></html>`)
	video := []byte{0, 0, 0, 20, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}
	audio := []byte("ID3\x04\x00\x00tiny mp3")
	data := testMOBIFileWithOptions(testMOBIOptions{
		codepage:        65001,
		title:           "MOBI Media",
		mobiVersion:     6,
		textRecords:     [][]byte{text},
		textLength:      uint32(len(text)),
		firstImageIndex: 2,
		extraRecords: [][]byte{
			tinyPNG,
			testKindleMediaRecord("VIDE", video),
			testKindleMediaRecord("AUDI", audio),
		},
	})
	r := bytes.NewReader(data)

	doc, err := ExtractKindleDocument(r, r.Size(), FormatMOBI)
	if err != nil {
		t.Fatalf("ExtractKindleDocument: %v", err)
	}
	if len(doc.Resources) != 3 {
		t.Fatalf("resources = %+v; want image, video, and audio", doc.Resources)
	}
	for i, want := range []struct {
		id        string
		href      string
		mediaType string
		data      []byte
	}{
		{id: "res-00002", href: "images/00002.png", mediaType: "image/png", data: tinyPNG},
		{id: "video-00003", href: "media/00003.mp4", mediaType: "video/mp4", data: video},
		{id: "audio-00004", href: "media/00004.mp3", mediaType: "audio/mpeg", data: audio},
	} {
		resource := doc.Resources[i]
		if resource.ID != want.id || resource.Href != want.href || resource.MediaType != want.mediaType || resource.EmbedIndex != i+1 || !bytes.Equal(resource.Data, want.data) {
			t.Fatalf("resource %d = %+v; want %s %s", i, resource, want.href, want.mediaType)
		}
	}
}

func TestExtractKindleDocumentRejectsInvalidMediaEnvelope(t *testing.T) {
	text := []byte(`<html><body><audio mediarecindex="00001">Fallback</audio></body></html>`)
	data := testMOBIFileWithOptions(testMOBIOptions{
		codepage:        65001,
		title:           "Broken MOBI Media",
		mobiVersion:     6,
		textRecords:     [][]byte{text},
		textLength:      uint32(len(text)),
		firstImageIndex: 2,
		extraRecords:    [][]byte{testKindleMediaRecord("AUDI", []byte("not an MP3"))},
	})
	r := bytes.NewReader(data)

	_, err := ExtractKindleDocument(r, r.Size(), FormatMOBI)
	if !errors.Is(err, ErrUnsupportedKindleSource) {
		t.Fatalf("ExtractKindleDocument error = %v; want ErrUnsupportedKindleSource", err)
	}
}

func TestExtractKindleDocumentPalmDOC(t *testing.T) {
	for _, tt := range []struct {
		name        string
		compression uint16
		text        []byte
		want        string
	}{
		{
			name:        "uncompressed",
			compression: mobiCompressionNone,
			text:        []byte("Palm text\nCaf\xe9"),
			want:        "Palm text\nCafé",
		},
		{
			name:        "palmdoc compression",
			compression: mobiCompressionPalmDOC,
			text:        []byte("Compressed Palm text"),
			want:        "Compressed Palm text",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := testPalmDOCFileWithText("Palm Export", tt.compression, [][]byte{tt.text}, uint32(len(tt.text)))
			r := bytes.NewReader(data)

			doc, err := ExtractKindleDocument(r, r.Size(), FormatPDB)
			if err != nil {
				t.Fatalf("ExtractKindleDocument: %v", err)
			}
			if doc.SourceClass != "palmdoc" || doc.MOBIKind != MOBIKindPalmDOC {
				t.Fatalf("class = %q, kind = %q; want PalmDOC", doc.SourceClass, doc.MOBIKind)
			}
			if doc.Metadata == nil || doc.Metadata.Title != "Palm Export" {
				t.Fatalf("metadata = %+v; want Palm database title", doc.Metadata)
			}
			if len(doc.Flows) != 1 {
				t.Fatalf("flows = %+v; want one flow", doc.Flows)
			}
			flow := doc.Flows[0]
			if flow.MediaType != "text/plain" || flow.Href != "text/flow-0001.txt" || string(flow.Data) != tt.want {
				t.Fatalf("flow = %+v; want decoded plain-text flow %q", flow, tt.want)
			}
			if len(doc.Resources) != 0 || len(doc.Navigation) != 0 || len(doc.Guide) != 0 {
				t.Fatalf("PalmDOC extras = resources %+v nav %+v guide %+v; want none", doc.Resources, doc.Navigation, doc.Guide)
			}
		})
	}
}

func TestExtractKindleDocumentPalmDOCOEBHTML(t *testing.T) {
	text := []byte(`<HTML><HEAD><metadata><dc-metadata xmlns:dc="http://purl.org/metadata/dublin_core">
<dc:Title>Structured PalmDOC</dc:Title><dc:Creator>Example Author</dc:Creator>
<dc:Language>pt</dc:Language><dc:Publisher>Example Press</dc:Publisher>
</dc-metadata></metadata><GUIDE><REFERENCE TYPE="toc" TITLE="Contents" filepos="0000000042"></GUIDE></HEAD>
<BODY><p>Structured <b>book text</b>.</p><img src="BMP" recindex="00001"></BODY></HTML>`)
	data := testPalmDOCFileWithTextAndResources(
		"Database Fallback",
		mobiCompressionPalmDOC,
		[][]byte{text},
		uint32(len(text)),
		[][]byte{testPalmDOCBMP(t)},
	)
	r := bytes.NewReader(data)

	doc, err := ExtractKindleDocument(r, r.Size(), FormatPDB)
	if err != nil {
		t.Fatalf("ExtractKindleDocument: %v", err)
	}
	if doc.Metadata == nil || doc.Metadata.Title != "Structured PalmDOC" || len(doc.Metadata.Authors) != 1 || doc.Metadata.Authors[0].Name != "Example Author" || doc.Metadata.Publisher != "Example Press" || doc.Metadata.Language != "pt" {
		t.Fatalf("metadata = %+v; want embedded OEB metadata", doc.Metadata)
	}
	if len(doc.Flows) != 1 || doc.Flows[0].MediaType != "text/html" || doc.Flows[0].Href != "text/flow-0001.html" || len(doc.Flows[0].Data) != len(text) || bytes.Contains(doc.Flows[0].Data, []byte("dc-metadata")) || !bytes.Contains(doc.Flows[0].Data, []byte("Structured <b>book text</b>.")) {
		t.Fatalf("flows = %+v; want one content-only HTML flow", doc.Flows)
	}
	if len(doc.Resources) != 1 {
		t.Fatalf("resources = %+v; want one transcoded image", doc.Resources)
	}
	resource := doc.Resources[0]
	if resource.ID != "res-00002" || resource.Href != "images/00002.png" || resource.MediaType != "image/png" || resource.EmbedIndex != 1 || resource.RecordIndex != 2 || !bytes.HasPrefix(resource.Data, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("resource = %+v; want bounded BMP-to-PNG resource", resource)
	}
	if len(doc.Guide) != 1 || doc.Guide[0].Type != "toc" || doc.Guide[0].Title != "Contents" || doc.Guide[0].Href != "text/flow-0001.html#filepos42" {
		t.Fatalf("guide = %+v; want PalmDOC OEB guide", doc.Guide)
	}
}

func TestExtractKindleDocumentKF8Standalone(t *testing.T) {
	prefix := []byte("<html><body><p>")
	suffix := []byte("</p></body></html>")
	skeleton := append(append([]byte(nil), prefix...), suffix...)
	fragment := []byte("Hello KF8")
	text := append(append([]byte(nil), skeleton...), fragment...)
	skelRecords := testKindleSKELRecordsWith(1, 0, uint32(len(skeleton)))
	fragRecords := testKindleFragmentRecordsWith(uint32(len(prefix)), "body > p", 0, 0, 0, uint32(len(fragment)))
	extraRecords := append([][]byte{}, skelRecords...)
	extraRecords = append(extraRecords, fragRecords...)
	navIndex := uint32(2 + len(extraRecords))
	extraRecords = append(extraRecords, testMOBINCXRecordsWithPositions(12, 15)...)
	data := testMOBIFileWithOptions(testMOBIOptions{
		codepage:     65001,
		title:        "KF8 Export",
		headerLength: 0x108,
		mobiVersion:  8,
		textRecords:  [][]byte{text},
		textLength:   uint32(len(text)),
		extraRecords: extraRecords,
	})
	data = testMOBIRecord0Uint32(t, data, 0xfc, 2)
	data = testMOBIRecord0Uint32(t, data, 0xf8, 4)
	data = testMOBIRecord0Uint32(t, data, 0xf4, navIndex)
	r := bytes.NewReader(data)

	doc, err := ExtractKindleDocument(r, r.Size(), FormatAZW3)
	if err != nil {
		t.Fatalf("ExtractKindleDocument: %v", err)
	}
	if doc.SourceClass != "kf8-standalone" || doc.MOBIKind != MOBIKindKF8Standalone {
		t.Fatalf("class = %q, kind = %q; want KF8 standalone", doc.SourceClass, doc.MOBIKind)
	}
	if doc.Metadata == nil || doc.Metadata.Title != "KF8 Export" {
		t.Fatalf("metadata = %+v; want KF8 title", doc.Metadata)
	}
	if len(doc.Flows) != 1 {
		t.Fatalf("flows = %+v; want one flow", doc.Flows)
	}
	if got, want := string(doc.Flows[0].Data), "<html><body><p>Hello KF8</p></body></html>"; got != want {
		t.Fatalf("flow = %q; want %q", got, want)
	}
	if len(doc.Navigation) != 1 {
		t.Fatalf("Navigation = %+v; want one root", doc.Navigation)
	}
	root := doc.Navigation[0]
	if root.Label != "Part One" || root.Href != "text/flow-0001.html#filepos12" {
		t.Fatalf("root = %+v; want Part One at filepos12", root)
	}
	if len(root.Children) != 1 {
		t.Fatalf("root.Children = %+v; want one child", root.Children)
	}
	child := root.Children[0]
	if child.Label != "Chapter One" || child.Href != "text/flow-0001.html#filepos15" {
		t.Fatalf("child = %+v; want Chapter One at filepos15", child)
	}
	if len(doc.UnsupportedFeatures) != 0 {
		t.Fatalf("UnsupportedFeatures = %+v; want none", doc.UnsupportedFeatures)
	}
}

func TestExtractKindleDocumentComboKF8(t *testing.T) {
	prefix := []byte("<html><body><p>")
	suffix := []byte(`</p><img src="kindle:embed:0001?mime=image/png"><video src="kindle:embed:0002?mime=video/mp4"></video><audio src="kindle:embed:0003?mime=audio/mpeg"></audio></body></html>`)
	skeleton := append(append([]byte(nil), prefix...), suffix...)
	fragment := []byte("Hello Combo KF8")
	text := append(append([]byte(nil), skeleton...), fragment...)
	skelRecords := testKindleSKELRecordsWith(1, 0, uint32(len(skeleton)))
	fragRecords := testKindleFragmentRecordsWith(uint32(len(prefix)), "body > p", 0, 0, 0, uint32(len(fragment)))
	kf8ExtraRecords := append([][]byte{}, skelRecords...)
	kf8ExtraRecords = append(kf8ExtraRecords, fragRecords...)
	kf8NavIndex := uint32(2 + len(kf8ExtraRecords))
	kf8ExtraRecords = append(kf8ExtraRecords, testMOBINCXRecordsWithPositions(12, 18)...)
	kf8 := testMOBIFileWithOptions(testMOBIOptions{
		codepage:     65001,
		title:        "KF8 Part",
		headerLength: 0x108,
		mobiVersion:  8,
		textRecords:  [][]byte{text},
		textLength:   uint32(len(text)),
		extraRecords: kf8ExtraRecords,
	})
	kf8 = testMOBIRecord0Uint32(t, kf8, 0xfc, 2)
	kf8 = testMOBIRecord0Uint32(t, kf8, 0xf8, 4)
	kf8 = testMOBIRecord0Uint32(t, kf8, 0xf4, kf8NavIndex)
	kf8Records := testPalmDBRecordBodies(t, kf8)
	video := []byte{0, 0, 0, 20, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}
	audio := []byte("ID3\x04\x00\x00tiny mp3")
	extraRecords := [][]byte{
		tinyPNG,
		testKindleMediaRecord("VIDE", video),
		testKindleMediaRecord("AUDI", audio),
		[]byte("BOUNDARY"),
	}
	extraRecords = append(extraRecords, kf8Records...)
	data := testMOBIFileWithOptions(testMOBIOptions{
		codepage:        65001,
		title:           "Combo Export",
		mobiVersion:     6,
		textRecords:     [][]byte{[]byte("<html><body>Legacy MOBI6</body></html>")},
		firstImageIndex: 2,
		records: []testEXTHRecord{
			{typ: 121, value: testMOBIUint32(6)},
			{typ: 501, value: []byte("EBOK")},
		},
		extraRecords: extraRecords,
	})
	r := bytes.NewReader(data)

	doc, err := ExtractKindleDocument(r, r.Size(), FormatMOBI)
	if err != nil {
		t.Fatalf("ExtractKindleDocument: %v", err)
	}
	if doc.SourceClass != "mobi6+kf8-combo" || doc.MOBIKind != MOBIKindCombo {
		t.Fatalf("class = %q, kind = %q; want combo", doc.SourceClass, doc.MOBIKind)
	}
	if doc.Metadata == nil || doc.Metadata.Title != "Combo Export" {
		t.Fatalf("metadata = %+v; want primary MOBI title", doc.Metadata)
	}
	if len(doc.Flows) != 1 {
		t.Fatalf("flows = %+v; want one flow", doc.Flows)
	}
	if got, want := string(doc.Flows[0].Data), `<html><body><p>Hello Combo KF8</p><img src="kindle:embed:0001?mime=image/png"><video src="kindle:embed:0002?mime=video/mp4"></video><audio src="kindle:embed:0003?mime=audio/mpeg"></audio></body></html>`; got != want {
		t.Fatalf("flow = %q; want %q", got, want)
	}
	if len(doc.Resources) != 3 || doc.Resources[0].RecordIndex != 2 || doc.Resources[1].MediaType != "video/mp4" || doc.Resources[2].MediaType != "audio/mpeg" {
		t.Fatalf("shared combo resources = %+v; want image, video, and audio before boundary", doc.Resources)
	}
	if doc.CoverResourceID != "res-00002" {
		t.Fatalf("CoverResourceID = %q; want shared primary cover", doc.CoverResourceID)
	}
	if len(doc.Navigation) != 1 {
		t.Fatalf("Navigation = %+v; want one root", doc.Navigation)
	}
	root := doc.Navigation[0]
	if root.Label != "Part One" || root.Href != "text/flow-0001.html#filepos12" {
		t.Fatalf("root = %+v; want Part One at filepos12", root)
	}
	if len(root.Children) != 1 {
		t.Fatalf("root.Children = %+v; want one child", root.Children)
	}
	child := root.Children[0]
	if child.Label != "Chapter One" || child.Href != "text/flow-0001.html#filepos18" {
		t.Fatalf("child = %+v; want Chapter One at filepos18", child)
	}
	if len(doc.UnsupportedFeatures) != 0 {
		t.Fatalf("UnsupportedFeatures = %+v; want none", doc.UnsupportedFeatures)
	}
}

func TestExtractKindleDocumentKF8CSSFlowResources(t *testing.T) {
	skeleton := []byte(`<html><head><link rel="stylesheet" href="kindle:flow:0001?mime=text/css"/></head><body><p>Styled</p></body></html>`)
	css := []byte("p { color: red; }\n")
	raw := append(append([]byte(nil), skeleton...), css...)
	skelRecords := testKindleSKELRecordsWith(0, 0, uint32(len(skeleton)))
	fdstRecord := testKindleFDSTRecord(
		[2]uint32{0, uint32(len(skeleton))},
		[2]uint32{uint32(len(skeleton)), uint32(len(raw))},
	)
	data := testMOBIFileWithOptions(testMOBIOptions{
		codepage:     65001,
		title:        "KF8 CSS",
		headerLength: 0x108,
		mobiVersion:  8,
		textRecords:  [][]byte{raw},
		textLength:   uint32(len(raw)),
		extraRecords: append(append(skelRecords, testKindleFragmentRecordsWith(0, "body", 0, 0, 0, 0)...), fdstRecord),
	})
	data = testMOBIRecord0Uint32(t, data, 0xfc, 2)
	data = testMOBIRecord0Uint32(t, data, 0xf8, 4)
	data = testMOBIRecord0Uint32(t, data, 0xc0, 7)
	data = testMOBIRecord0Uint32(t, data, 0xc4, 2)
	r := bytes.NewReader(data)

	doc, err := ExtractKindleDocument(r, r.Size(), FormatAZW3)
	if err != nil {
		t.Fatalf("ExtractKindleDocument: %v", err)
	}
	if len(doc.Resources) != 1 {
		t.Fatalf("Resources = %+v; want one CSS resource", doc.Resources)
	}
	resource := doc.Resources[0]
	if resource.ID != "style-0001" || resource.Href != "styles/flow-0001.css" || resource.MediaType != "text/css" || string(resource.Data) != string(css) {
		t.Fatalf("CSS resource = %+v data=%q; want extracted FDST CSS", resource, resource.Data)
	}
}

func TestExtractKindleDocumentKF8SVGFlowResource(t *testing.T) {
	skeleton := []byte(`<html><body><img src="kindle:flow:0001?mime=image/svg+xml"/></body></html>`)
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"></svg>`)
	raw := append(append([]byte(nil), skeleton...), svg...)
	skelRecords := testKindleSKELRecordsWith(0, 0, uint32(len(skeleton)))
	fdstRecord := testKindleFDSTRecord(
		[2]uint32{0, uint32(len(skeleton))},
		[2]uint32{uint32(len(skeleton)), uint32(len(raw))},
	)
	data := testMOBIFileWithOptions(testMOBIOptions{
		codepage:     65001,
		title:        "KF8 SVG",
		headerLength: 0x108,
		mobiVersion:  8,
		textRecords:  [][]byte{raw},
		textLength:   uint32(len(raw)),
		extraRecords: append(append(skelRecords, testKindleFragmentRecordsWith(0, "body", 0, 0, 0, 0)...), fdstRecord),
	})
	data = testMOBIRecord0Uint32(t, data, 0xfc, 2)
	data = testMOBIRecord0Uint32(t, data, 0xf8, 4)
	data = testMOBIRecord0Uint32(t, data, 0xc0, 7)
	data = testMOBIRecord0Uint32(t, data, 0xc4, 2)
	r := bytes.NewReader(data)

	doc, err := ExtractKindleDocument(r, r.Size(), FormatAZW3)
	if err != nil {
		t.Fatalf("ExtractKindleDocument: %v", err)
	}
	if len(doc.Resources) != 1 {
		t.Fatalf("Resources = %+v; want one SVG resource", doc.Resources)
	}
	resource := doc.Resources[0]
	if resource.ID != "svg-0001" || resource.Href != "images/flow-0001.svg" || resource.MediaType != "image/svg+xml" || resource.FlowIndex != 1 {
		t.Fatalf("SVG resource = %+v; want extracted SVG flow resource", resource)
	}
	if !bytes.Equal(resource.Data, svg) {
		t.Fatalf("SVG data = %q; want %q", resource.Data, svg)
	}
}

func TestExtractKindleDocumentKF8FontResource(t *testing.T) {
	prefix := []byte("<html><body><p>")
	suffix := []byte("</p></body></html>")
	skeleton := append(append([]byte(nil), prefix...), suffix...)
	fragment := []byte("Font body")
	text := append(append([]byte(nil), skeleton...), fragment...)
	skelRecords := testKindleSKELRecordsWith(1, 0, uint32(len(skeleton)))
	fragRecords := testKindleFragmentRecordsWith(uint32(len(prefix)), "body > p", 0, 0, 0, uint32(len(fragment)))
	extraRecords := append(append([][]byte{}, skelRecords...), fragRecords...)
	fontRecordIndex := uint32(2 + len(extraRecords))
	fontData := []byte("\x00\x01\x00\x00tiny ttf")
	extraRecords = append(extraRecords, testKindleFONTRecord(t, fontData))
	data := testMOBIFileWithOptions(testMOBIOptions{
		codepage:        65001,
		title:           "KF8 Font",
		headerLength:    0x108,
		mobiVersion:     8,
		textRecords:     [][]byte{text},
		textLength:      uint32(len(text)),
		firstImageIndex: fontRecordIndex,
		extraRecords:    extraRecords,
	})
	data = testMOBIRecord0Uint32(t, data, 0xfc, 2)
	data = testMOBIRecord0Uint32(t, data, 0xf8, 4)
	r := bytes.NewReader(data)

	doc, err := ExtractKindleDocument(r, r.Size(), FormatAZW3)
	if err != nil {
		t.Fatalf("ExtractKindleDocument: %v", err)
	}
	if len(doc.Resources) != 1 {
		t.Fatalf("Resources = %+v; want one font resource", doc.Resources)
	}
	resource := doc.Resources[0]
	if resource.ID != "font-00007" || resource.Href != "fonts/00007.ttf" || resource.MediaType != "font/ttf" || resource.EmbedIndex != 1 || resource.RecordIndex != 7 {
		t.Fatalf("font resource = %+v; want extracted TTF resource", resource)
	}
	if !bytes.Equal(resource.Data, fontData) {
		t.Fatalf("font data = %x; want %x", resource.Data, fontData)
	}
}

func TestExtractKindleDocumentMOBI6InlineGuide(t *testing.T) {
	text := []byte(`<html><head><guide>
<reference type="toc" title="Contents" filepos=0000000010 />
<reference type="text" filepos=0000000020 />
<reference type="cover" title="Cover &amp; Start" href="#cover" />
<reference type="toc" title="Duplicate Contents" filepos=0000000010 />
<reference type="ignored" />
</guide></head><body id="cover"><p>Hello Kindle</p></body></html>`)
	data := testMOBIFileWithOptions(testMOBIOptions{
		codepage:    65001,
		title:       "Guided MOBI",
		compression: mobiCompressionPalmDOC,
		textRecords: [][]byte{text},
		textLength:  uint32(len(text)),
		mobiVersion: 6,
		records: []testEXTHRecord{
			{typ: 501, value: []byte("EBOK")},
		},
	})
	r := bytes.NewReader(data)

	doc, err := ExtractKindleDocument(r, r.Size(), FormatMOBI)
	if err != nil {
		t.Fatalf("ExtractKindleDocument: %v", err)
	}
	if len(doc.Guide) != 3 {
		t.Fatalf("Guide = %+v; want three unique references", doc.Guide)
	}
	want := []KindleGuideReference{
		{Type: "toc", Title: "Contents", Href: "text/flow-0001.html#filepos10"},
		{Type: "text", Title: "text", Href: "text/flow-0001.html#filepos20"},
		{Type: "cover", Title: "Cover & Start", Href: "text/flow-0001.html#cover"},
	}
	for i := range want {
		if doc.Guide[i] != want[i] {
			t.Fatalf("Guide[%d] = %+v; want %+v", i, doc.Guide[i], want[i])
		}
	}
	if len(doc.Navigation) != 0 {
		t.Fatalf("Navigation = %+v; want no synthesized NCX navigation yet", doc.Navigation)
	}
}

func TestExtractKindleDocumentMOBI6NCXNavigation(t *testing.T) {
	text := []byte("<html><body><p>Hello Kindle</p></body></html>")
	navRecords := testMOBI6NCXRecords()
	data := testMOBIFileWithOptions(testMOBIOptions{
		codepage:    65001,
		title:       "NCX MOBI",
		compression: mobiCompressionPalmDOC,
		textRecords: [][]byte{text},
		textLength:  uint32(len(text)),
		mobiVersion: 6,
		extraRecords: [][]byte{
			navRecords[0],
			navRecords[1],
			navRecords[2],
		},
	})
	data = testMOBIRecord0Uint32(t, data, 0xf4, 2)
	r := bytes.NewReader(data)

	doc, err := ExtractKindleDocument(r, r.Size(), FormatMOBI)
	if err != nil {
		t.Fatalf("ExtractKindleDocument: %v", err)
	}
	if len(doc.Navigation) != 1 {
		t.Fatalf("Navigation = %+v; want one root", doc.Navigation)
	}
	root := doc.Navigation[0]
	if root.Label != "Part One" || root.Href != "text/flow-0001.html#filepos42" {
		t.Fatalf("root = %+v; want Part One at filepos42", root)
	}
	if len(root.Children) != 1 {
		t.Fatalf("root.Children = %+v; want one child", root.Children)
	}
	child := root.Children[0]
	if child.Label != "Chapter One" || child.Href != "text/flow-0001.html#filepos84" {
		t.Fatalf("child = %+v; want Chapter One at filepos84", child)
	}
}

func TestExtractKindleDocumentPalmDOCDecompression(t *testing.T) {
	// "Café" as a literal run, then a space-compressed high byte and a
	// back-reference that repeats "abc".
	compressed := []byte{4, 'C', 'a', 'f', 0xe9, 0xe9, 'a', 'b', 'c', 0x80, 0x1b}
	text := "Café iabcabc"
	data := testMOBIFileWithOptions(testMOBIOptions{
		codepage:    1252,
		title:       "Compressed Text",
		compression: mobiCompressionPalmDOC,
		textRecords: [][]byte{
			compressed,
		},
		textLength:  uint32(len([]byte("Caf\xe9 iabcabc"))),
		mobiVersion: 6,
	})
	r := bytes.NewReader(data)

	doc, err := ExtractKindleDocument(r, r.Size(), FormatMOBI)
	if err != nil {
		t.Fatalf("ExtractKindleDocument: %v", err)
	}
	if got := string(doc.Flows[0].Data); got != text {
		t.Fatalf("text = %q; want %q", got, text)
	}
}

func TestExtractKindleDocumentTrimsTrailingEntries(t *testing.T) {
	data := testMOBIFileWithOptions(testMOBIOptions{
		codepage:      65001,
		title:         "Trailing Text",
		compression:   mobiCompressionPalmDOC,
		textRecords:   [][]byte{[]byte("plain\x00\x81")},
		textLength:    5,
		mobiVersion:   6,
		trailingFlags: 3,
	})
	r := bytes.NewReader(data)

	doc, err := ExtractKindleDocument(r, r.Size(), FormatMOBI)
	if err != nil {
		t.Fatalf("ExtractKindleDocument: %v", err)
	}
	if got := string(doc.Flows[0].Data); got != "plain" {
		t.Fatalf("text = %q; want trailing bytes removed", got)
	}
}

func TestExtractKindleDocumentRejectsUnsupportedSources(t *testing.T) {
	for _, tt := range []struct {
		name string
		data []byte
		kind Format
	}{
		{
			name: "huff cdic",
			data: testMOBIFileWithOptions(testMOBIOptions{
				codepage:    65001,
				title:       "HUFF",
				compression: mobiCompressionHUFFCDIC,
				textRecords: [][]byte{[]byte("plain")},
				textLength:  5,
				mobiVersion: 6,
			}),
			kind: FormatMOBI,
		},
		{
			name: "combo",
			data: testMOBIFileWithOptions(testMOBIOptions{
				codepage:    65001,
				title:       "Combo",
				compression: mobiCompressionPalmDOC,
				textRecords: [][]byte{[]byte("plain")},
				textLength:  5,
				mobiVersion: 6,
				records: []testEXTHRecord{
					{typ: 121, value: testMOBIUint32(3)},
				},
				extraRecords: [][]byte{[]byte("BOUNDARY"), []byte("kf8 placeholder")},
			}),
			kind: FormatMOBI,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := bytes.NewReader(tt.data)
			_, err := ExtractKindleDocument(r, r.Size(), tt.kind)
			if !errors.Is(err, ErrUnsupportedKindleSource) {
				t.Fatalf("ExtractKindleDocument error = %v; want ErrUnsupportedKindleSource", err)
			}
		})
	}
}

func TestExtractKindleDocumentRejectsMalformedPalmDOCCompression(t *testing.T) {
	data := testMOBIFileWithOptions(testMOBIOptions{
		codepage:    65001,
		title:       "Broken",
		compression: mobiCompressionPalmDOC,
		textRecords: [][]byte{
			{0x80},
		},
		textLength:  1,
		mobiVersion: 6,
	})
	r := bytes.NewReader(data)

	_, err := ExtractKindleDocument(r, r.Size(), FormatMOBI)
	if err == nil || !strings.Contains(err.Error(), "back-reference overruns record") {
		t.Fatalf("ExtractKindleDocument error = %v; want malformed PalmDOC error", err)
	}
}

type testEXTHRecord struct {
	typ   uint32
	value []byte
}

func testMOBIFile(codepage uint32, title string, records []testEXTHRecord) []byte {
	return testMOBIFileWithOptions(testMOBIOptions{
		codepage: codepage,
		title:    title,
		records:  records,
	})
}

type testMOBIOptions struct {
	codepage        uint32
	palmDBName      string
	title           string
	compression     uint16
	encryption      uint16
	headerLength    uint32
	mobiVersion     uint32
	trailingFlags   uint16
	textLength      uint32
	textRecords     [][]byte
	records         []testEXTHRecord
	firstImageIndex uint32
	extraRecords    [][]byte
}

func testMOBIFileWithOptions(opts testMOBIOptions) []byte {
	mobiHeaderLength := opts.headerLength
	if mobiHeaderLength == 0 {
		mobiHeaderLength = 0xe8
	}
	exth := testEXTH(opts.records)
	titleOffset := 16 + int(mobiHeaderLength) + len(exth)
	titleBytes := []byte(opts.title)
	firstImageIndex := opts.firstImageIndex
	if firstImageIndex == 0 {
		firstImageIndex = mobiNoImageIndex
	}
	compression := opts.compression
	if compression == 0 {
		compression = mobiCompressionNone
	}
	textRecords := opts.textRecords
	if len(textRecords) == 0 {
		textRecords = [][]byte{[]byte("dummy text record")}
	}
	textLength := opts.textLength
	if textLength == 0 {
		for _, record := range textRecords {
			textLength += uint32(len(record))
		}
	}
	mobiVersion := opts.mobiVersion
	if mobiVersion == 0 {
		mobiVersion = 8
	}

	record0 := make([]byte, titleOffset+len(titleBytes))
	binary.BigEndian.PutUint16(record0[0:2], compression)
	binary.BigEndian.PutUint32(record0[4:8], textLength)
	binary.BigEndian.PutUint16(record0[8:10], uint16(len(textRecords)))
	binary.BigEndian.PutUint16(record0[10:12], 4096)
	binary.BigEndian.PutUint16(record0[12:14], opts.encryption)
	copy(record0[16:20], "MOBI")
	binary.BigEndian.PutUint32(record0[20:24], mobiHeaderLength)
	binary.BigEndian.PutUint32(record0[28:32], opts.codepage)
	binary.BigEndian.PutUint32(record0[0x54:0x58], uint32(titleOffset))
	binary.BigEndian.PutUint32(record0[0x58:0x5c], uint32(len(titleBytes)))
	binary.BigEndian.PutUint32(record0[0x5c:0x60], 0x09) // English primary language id.
	binary.BigEndian.PutUint32(record0[0x68:0x6c], mobiVersion)
	binary.BigEndian.PutUint32(record0[0x6c:0x70], firstImageIndex)
	binary.BigEndian.PutUint16(record0[0xf2:0xf4], opts.trailingFlags)
	if len(opts.records) > 0 {
		binary.BigEndian.PutUint32(record0[0x80:0x84], 0x40)
		copy(record0[16+mobiHeaderLength:], exth)
	}
	copy(record0[titleOffset:], titleBytes)

	header := make([]byte, palmDBHeaderSize)
	copy(header[:palmDBNameBytes], []byte(opts.palmDBName))
	copy(header[60:68], "BOOKMOBI")
	recordBodies := append([][]byte{record0}, textRecords...)
	recordBodies = append(recordBodies, opts.extraRecords...)
	binary.BigEndian.PutUint16(header[76:78], uint16(len(recordBodies)))

	offset := palmDBHeaderSize + len(recordBodies)*palmDBRecordSize
	table := make([]byte, len(recordBodies)*palmDBRecordSize)
	for i, body := range recordBodies {
		binary.BigEndian.PutUint32(table[i*palmDBRecordSize:i*palmDBRecordSize+4], uint32(offset))
		offset += len(body)
	}

	out := append(header, table...)
	for _, body := range recordBodies {
		out = append(out, body...)
	}
	return out
}

func testPalmDBRecordBodies(t *testing.T, data []byte) [][]byte {
	t.Helper()
	if len(data) < palmDBHeaderSize {
		t.Fatalf("PalmDB fixture too short")
	}
	count := int(binary.BigEndian.Uint16(data[76:78]))
	if count < 1 || len(data) < palmDBHeaderSize+count*palmDBRecordSize {
		t.Fatalf("invalid PalmDB record table")
	}
	records := make([][]byte, 0, count)
	for i := range count {
		start := int(binary.BigEndian.Uint32(data[palmDBHeaderSize+i*palmDBRecordSize : palmDBHeaderSize+i*palmDBRecordSize+4]))
		end := len(data)
		if i+1 < count {
			end = int(binary.BigEndian.Uint32(data[palmDBHeaderSize+(i+1)*palmDBRecordSize : palmDBHeaderSize+(i+1)*palmDBRecordSize+4]))
		}
		if start < palmDBHeaderSize+count*palmDBRecordSize || start > end || end > len(data) {
			t.Fatalf("invalid PalmDB record %d bounds %d..%d", i, start, end)
		}
		records = append(records, append([]byte(nil), data[start:end]...))
	}
	return records
}

func testMOBIUint32(value uint32) []byte {
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, value)
	return buf
}

func testKindleFDSTRecord(sections ...[2]uint32) []byte {
	out := []byte("FDST")
	out = append(out, testMOBIUint32(12)...)
	out = append(out, testMOBIUint32(uint32(len(sections)))...)
	for _, section := range sections {
		out = append(out, testMOBIUint32(section[0])...)
		out = append(out, testMOBIUint32(section[1])...)
	}
	return out
}

func testKindleFONTRecord(t *testing.T, payload []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	if _, err := zw.Write(payload); err != nil {
		t.Fatalf("compress font fixture: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close font fixture compressor: %v", err)
	}
	key := []byte("test-key")
	fontData := append([]byte(nil), compressed.Bytes()...)
	for i, end := 0, min(len(fontData), 1040); i < end; i++ {
		fontData[i] ^= key[i%len(key)]
	}
	out := []byte("FONT")
	out = append(out, testMOBIUint32(uint32(len(payload)))...)
	out = append(out, testMOBIUint32(0x0003)...)
	out = append(out, testMOBIUint32(uint32(24+len(key)))...)
	out = append(out, testMOBIUint32(uint32(len(key)))...)
	out = append(out, testMOBIUint32(24)...)
	out = append(out, key...)
	out = append(out, fontData...)
	return out
}

func testKindleMediaRecord(magic string, payload []byte) []byte {
	out := []byte(magic)
	out = append(out, testMOBIUint32(12)...)
	out = append(out, 0, 0, 0, 0)
	out = append(out, payload...)
	return out
}

func testKindleSKELRecords() [][]byte {
	return testKindleSKELRecordsWith(1, 0, 100)
}

func testKindleSKELRecordsWith(fragmentCount, start, length uint32) [][]byte {
	tagx := testKindleTAGX(1, [][4]byte{
		{1, 1, 0x01, 0},
		{6, 2, 0x02, 0},
		{0, 0, 0, 1},
	})
	master := testKindleINDXMaster(tagx, 1, 0)
	entryRecord := testKindleINDXEntryRecord([][]byte{
		testKindleINDXEntry("SKEL0000000000", 0x03, fragmentCount, start, length),
	})
	return [][]byte{master, entryRecord}
}

func testKindleFragmentRecords() [][]byte {
	return testKindleFragmentRecordsWith(42, "body > p:nth-of-type(1)", 0, 7, 0, 25)
}

func testKindleFragmentRecordsWith(insertOffset uint32, selector string, fileNumber, sequence, start, length uint32) [][]byte {
	cncx, selectorOffsets := testKindleCNCXRecord(selector)
	tagx := testKindleTAGX(1, [][4]byte{
		{2, 1, 0x01, 0},
		{3, 1, 0x02, 0},
		{4, 1, 0x04, 0},
		{6, 2, 0x08, 0},
		{0, 0, 0, 1},
	})
	master := testKindleINDXMaster(tagx, 1, 1)
	entryRecord := testKindleINDXEntryRecord([][]byte{
		testKindleINDXEntry(fmt.Sprintf("%d", insertOffset), 0x0f, selectorOffsets[0], fileNumber, sequence, start, length),
	})
	return [][]byte{master, entryRecord, cncx}
}

func equalFDSTSections(a, b []KindleFDSTSection) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalKF8Skeletons(a, b []KindleKF8Skeleton) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalKF8Fragments(a, b []KindleKF8Fragment) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func testMOBIRecord0Uint32(t *testing.T, data []byte, offset int, value uint32) []byte {
	t.Helper()
	out := append([]byte(nil), data...)
	if len(out) < palmDBHeaderSize+palmDBRecordSize {
		t.Fatalf("MOBI fixture too short")
	}
	record0Offset := int(binary.BigEndian.Uint32(out[palmDBHeaderSize : palmDBHeaderSize+4]))
	if record0Offset+offset+4 > len(out) {
		t.Fatalf("record0 offset %x outside fixture length %d", offset, len(out))
	}
	binary.BigEndian.PutUint32(out[record0Offset+offset:record0Offset+offset+4], value)
	return out
}

func testMOBI6NCXRecords() [][]byte {
	return testMOBINCXRecordsWithPositions(42, 84)
}

func testMOBINCXRecordsWithPositions(rootPos, childPos uint32) [][]byte {
	cncx, labelOffsets := testKindleCNCXRecord("Part One", "Chapter One")
	tagx := testKindleTAGX(1, [][4]byte{
		{1, 1, 0x01, 0},
		{3, 1, 0x02, 0},
		{4, 1, 0x04, 0},
		{21, 1, 0x08, 0},
		{22, 1, 0x10, 0},
		{23, 1, 0x20, 0},
		{0, 0, 0, 1},
	})
	master := testKindleINDXMaster(tagx, 1, 1)
	entryRecord := testKindleINDXEntryRecord([][]byte{
		testKindleINDXEntry("part", 0x37, rootPos, labelOffsets[0], 0, 1, 1),
		testKindleINDXEntry("chapter", 0x0f, childPos, labelOffsets[1], 1, 0),
	})
	return [][]byte{master, entryRecord, cncx}
}

func testKindleINDXMaster(tagx []byte, indexRecords, cncxRecords uint32) []byte {
	record := make([]byte, 56+len(tagx))
	copy(record[:4], "INDX")
	binary.BigEndian.PutUint32(record[4:8], 56)
	binary.BigEndian.PutUint32(record[24:28], indexRecords)
	binary.BigEndian.PutUint32(record[28:32], 65001)
	binary.BigEndian.PutUint32(record[52:56], cncxRecords)
	copy(record[56:], tagx)
	return record
}

func testKindleTAGX(controlBytes uint32, entries [][4]byte) []byte {
	length := 12 + len(entries)*4
	tagx := make([]byte, length)
	copy(tagx[:4], "TAGX")
	binary.BigEndian.PutUint32(tagx[4:8], uint32(length))
	binary.BigEndian.PutUint32(tagx[8:12], controlBytes)
	for i, entry := range entries {
		offset := 12 + i*4
		copy(tagx[offset:offset+4], entry[:])
	}
	return tagx
}

func testKindleINDXEntryRecord(entries [][]byte) []byte {
	idxt := 56
	for _, entry := range entries {
		idxt += len(entry)
	}
	record := make([]byte, idxt+4+len(entries)*2)
	copy(record[:4], "INDX")
	binary.BigEndian.PutUint32(record[4:8], 56)
	binary.BigEndian.PutUint32(record[20:24], uint32(idxt))
	binary.BigEndian.PutUint32(record[24:28], uint32(len(entries)))
	binary.BigEndian.PutUint32(record[28:32], 65001)

	offset := 56
	for i, entry := range entries {
		binary.BigEndian.PutUint16(record[idxt+4+i*2:idxt+6+i*2], uint16(offset))
		copy(record[offset:], entry)
		offset += len(entry)
	}
	copy(record[idxt:idxt+4], "IDXT")
	return record
}

func testKindleINDXEntry(name string, control byte, values ...uint32) []byte {
	out := []byte{byte(len(name))}
	out = append(out, []byte(name)...)
	out = append(out, control)
	for _, value := range values {
		out = append(out, testKindleVarLen(value)...)
	}
	return out
}

func testKindleCNCXRecord(labels ...string) ([]byte, []uint32) {
	var out []byte
	offsets := make([]uint32, 0, len(labels))
	for _, label := range labels {
		offsets = append(offsets, uint32(len(out)))
		raw := []byte(label)
		out = append(out, testKindleVarLen(uint32(len(raw)))...)
		out = append(out, raw...)
	}
	return out, offsets
}

func testKindleVarLen(value uint32) []byte {
	if value == 0 {
		return []byte{0x80}
	}
	var out []byte
	for value > 0 {
		out = append([]byte{byte(value & 0x7f)}, out...)
		value >>= 7
	}
	out[len(out)-1] |= 0x80
	return out
}

func testEXTH(records []testEXTHRecord) []byte {
	if len(records) == 0 {
		return nil
	}
	var body []byte
	for _, rec := range records {
		buf := make([]byte, 8+len(rec.value))
		binary.BigEndian.PutUint32(buf[0:4], rec.typ)
		binary.BigEndian.PutUint32(buf[4:8], uint32(len(buf)))
		copy(buf[8:], rec.value)
		body = append(body, buf...)
	}
	length := 12 + len(body)
	exth := make([]byte, length)
	copy(exth[0:4], "EXTH")
	binary.BigEndian.PutUint32(exth[4:8], uint32(length))
	binary.BigEndian.PutUint32(exth[8:12], uint32(len(records)))
	copy(exth[12:], body)
	for len(exth)%4 != 0 {
		exth = append(exth, 0)
	}
	return exth
}

func equalUint32s(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsString(values []string, want string) bool {
	return slices.Contains(values, want)
}
