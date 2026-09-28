package mobi

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/testfixture"
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
			if err := tt.budget.add(tt.second); !errors.Is(err, ErrResourceLimit) {
				t.Fatalf("second resource error = %v; want ErrResourceLimit", err)
			}
		})
	}
}

func TestExtractMOBIMetadataFromHeaderAndEXTH(t *testing.T) {
	data := testfixture.MOBIWithMetadata(65001, "Short PDB Title", []testfixture.MOBIEXTHRecord{
		{Type: 503, Value: []byte("Long Kindle Title")},
		{Type: 100, Value: []byte("Doe, Jane")},
		{Type: 101, Value: []byte("MOBI Press")},
		{Type: 103, Value: []byte("A short description.")},
		{Type: 104, Value: []byte("9780306406157")},
		{Type: 105, Value: []byte("Sci-Fi, Classics;\nSpace Adventure; Sci-Fi, Classics")},
		{Type: 106, Value: []byte("2020-05-03T00:00:00Z")},
		{Type: 113, Value: []byte("B000TESTID")},
		{Type: 524, Value: []byte("eng")},
	})
	r := bytes.NewReader(data)

	meta, err := ExtractMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractMetadata: %v", err)
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
	if len(meta.Genres) != 2 || meta.Genres[0] != "Sci-Fi, Classics" || meta.Genres[1] != "Space Adventure" || len(meta.Tags) != 0 {
		t.Fatalf("Genres / Tags = %v / %v", meta.Genres, meta.Tags)
	}
}

func TestExtractMOBIMetadataHeaderFallbackAndCP1252(t *testing.T) {
	data := testfixture.MOBIWithMetadata(1252, "Caf\xe9 MOBI", nil)
	r := bytes.NewReader(data)

	meta, err := ExtractMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractMetadata: %v", err)
	}
	if meta.Title != "Café MOBI" {
		t.Fatalf("Title = %q; want CP1252 decoded title", meta.Title)
	}
	if meta.Language != "en" {
		t.Fatalf("Language = %q; want header language fallback", meta.Language)
	}
}

func TestExtractMOBIMetadataCleansEXTHText(t *testing.T) {
	data := testfixture.MOBIWithMetadata(65001, "Short PDB Title", []testfixture.MOBIEXTHRecord{
		{Type: 503, Value: []byte("Tom &amp; Jerry &#x2019; Caf&#xE9;\x01")},
		{Type: 100, Value: []byte("O&#39;Brien, Anne\x02")},
		{Type: 101, Value: []byte("Unknown")},
		{Type: 103, Value: []byte("A &lt;b&gt;short&lt;/b&gt; description.")},
		{Type: 105, Value: []byte("Drama &amp; Comedy; Old&#x20;Books")},
	})
	r := bytes.NewReader(data)

	meta, err := ExtractMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractMetadata: %v", err)
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
	wantGenres := []string{"Drama & Comedy", "Old Books"}
	if !slices.Equal(meta.Genres, wantGenres) {
		t.Fatalf("Genres = %+v; want %+v", meta.Genres, wantGenres)
	}
}

func TestExtractMOBIMetadataPalmDBTitleFallback(t *testing.T) {
	for _, tt := range []struct {
		name  string
		Title string
		want  string
	}{
		{name: "useful name", Title: "Palm DB Title", want: "Palm DB Title"},
		{name: "generic name", Title: "Unknown"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := testfixture.BuildMOBI(testfixture.MOBIOptions{
				Codepage:   1252,
				PalmDBName: tt.Title,
			})
			r := bytes.NewReader(data)
			meta, err := ExtractMetadata(r, r.Size())
			if err != nil {
				t.Fatalf("ExtractMetadata: %v", err)
			}
			if meta.Title != tt.want {
				t.Fatalf("Title = %q; want %q", meta.Title, tt.want)
			}
		})
	}
}

func TestExtractMOBICoverSelection(t *testing.T) {
	for _, tt := range []struct {
		name      string
		options   testfixture.MOBIOptions
		wantCover bool
	}{
		{
			name: "EXTH cover offset",
			options: testfixture.MOBIOptions{
				FirstImageIndex: 2,
				EXTH:            []testfixture.MOBIEXTHRecord{{Type: 201, Value: testfixture.MOBIUint32(1)}},
				ExtraRecords:    [][]byte{[]byte("not the cover"), tinyPNG},
			},
			wantCover: true,
		},
		{
			name: "first image fallback",
			options: testfixture.MOBIOptions{
				FirstImageIndex: 2,
				ExtraRecords:    [][]byte{tinyPNG},
			},
			wantCover: true,
		},
		{
			name: "invalid image",
			options: testfixture.MOBIOptions{
				FirstImageIndex: 2,
				ExtraRecords:    [][]byte{[]byte("not an image")},
			},
		},
		{
			name: "fake cover marker",
			options: testfixture.MOBIOptions{
				FirstImageIndex: 2,
				EXTH: []testfixture.MOBIEXTHRecord{
					{Type: 201, Value: testfixture.MOBIUint32(0)},
					{Type: 203, Value: testfixture.MOBIUint32(1)},
				},
				ExtraRecords: [][]byte{tinyPNG},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.options.Codepage = 65001
			tt.options.Title = "MOBI cover test"
			data := testfixture.BuildMOBI(tt.options)
			r := bytes.NewReader(data)

			got, ext, err := ExtractCover(r, r.Size())
			if err != nil {
				t.Fatalf("ExtractCover: %v", err)
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

func TestInspectMOBIKind(t *testing.T) {
	for _, tt := range []struct {
		name string
		data []byte
		want Kind
	}{
		{
			name: "mobi6",
			data: testfixture.MOBIWithMetadata(65001, "MOBI6 Book", nil),
			want: KindMOBI6,
		},
		{
			name: "kf8 standalone",
			data: testfixture.SetMOBIRecord0Uint32(t,
				testfixture.BuildMOBI(testfixture.MOBIOptions{Title: "KF8 Book", MOBIVersion: 8}),
				0xf8,
				2,
			),
			want: KindKF8,
		},
		{
			name: "combo",
			data: testfixture.BuildMOBI(testfixture.MOBIOptions{
				Codepage: 65001,
				Title:    "Combo Book",
				EXTH: []testfixture.MOBIEXTHRecord{
					{Type: 121, Value: testfixture.MOBIUint32(3)},
				},
				ExtraRecords: [][]byte{
					[]byte("BOUNDARY"),
					[]byte("kf8 placeholder"),
				},
			}),
			want: KindCombo,
		},
		{
			name: "palmdoc",
			data: testfixture.PalmDOC("PalmDOC Book"),
			want: KindPalmDOC,
		},
		{
			name: "unknown",
			data: []byte("not mobi"),
			want: KindUnknown,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := bytes.NewReader(tt.data)
			info, err := Inspect(r, r.Size())
			if err != nil {
				t.Fatal(err)
			}
			got := KindUnknown
			if info != nil {
				got = info.Kind
			}
			if got != tt.want {
				t.Fatalf("Kind = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestInspectKindleMOBIHeaderSignals(t *testing.T) {
	data := testfixture.BuildMOBI(testfixture.MOBIOptions{
		Codepage:        65001,
		Title:           "Combo Book",
		HeaderLength:    0x108,
		FirstImageIndex: 2,
		EXTH: []testfixture.MOBIEXTHRecord{
			{Type: 121, Value: testfixture.MOBIUint32(3)},
			{Type: 501, Value: []byte("EBOK")},
			{Type: 525, Value: []byte("horizontal-lr")},
			{Type: 527, Value: []byte("ltr")},
		},
		ExtraRecords: [][]byte{
			[]byte("BOUNDARY"),
			[]byte("OTTO font"),
			tinyPNG,
			[]byte("INDX index"),
		},
	})
	data = testfixture.SetMOBIRecord0Uint32(t, data, 0xf4, 4)
	data = testfixture.SetMOBIRecord0Uint32(t, data, 0xc0, 5)
	data = testfixture.SetMOBIRecord0Uint32(t, data, 0xc4, 2)
	data = testfixture.SetMOBIRecord0Uint32(t, data, 0xf8, 6)
	data = testfixture.SetMOBIRecord0Uint32(t, data, 0xfc, 7)
	data = testfixture.SetMOBIRecord0Uint32(t, data, 0x104, 8)
	r := bytes.NewReader(data)

	info, err := Inspect(r, r.Size())
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if info == nil {
		t.Fatal("Inspect returned nil")
	}
	if info.SourceClass != "mobi6+kf8-combo" || info.Kind != KindCombo {
		t.Fatalf("class = %q, kind = %q; want combo", info.SourceClass, info.Kind)
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
	data := testfixture.BuildMOBI(testfixture.MOBIOptions{
		Codepage:    65001,
		Title:       "FDST Book",
		MOBIVersion: 8,
		ExtraRecords: [][]byte{
			testKindleFDSTRecord([2]uint32{0, 10}, [2]uint32{10, 25}),
		},
	})
	data = testfixture.SetMOBIRecord0Uint32(t, data, 0xc0, 2)
	data = testfixture.SetMOBIRecord0Uint32(t, data, 0xc4, 2)
	r := bytes.NewReader(data)

	info, err := Inspect(r, r.Size())
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if got, want := info.FDSTSections, []FDSTSection{{Start: 0, End: 10}, {Start: 10, End: 25}}; !equalFDSTSections(got, want) {
		t.Fatalf("FDSTSections = %+v; want %+v", got, want)
	}
}

func TestInspectKindleReadsKF8SkeletonAndFragmentTables(t *testing.T) {
	skelRecords := testKindleSKELRecords()
	fragRecords := testKindleFragmentRecords()
	data := testfixture.BuildMOBI(testfixture.MOBIOptions{
		Codepage:     65001,
		Title:        "KF8 Structure",
		HeaderLength: 0x108,
		MOBIVersion:  8,
		ExtraRecords: append(skelRecords, fragRecords...),
	})
	data = testfixture.SetMOBIRecord0Uint32(t, data, 0xfc, 2)
	data = testfixture.SetMOBIRecord0Uint32(t, data, 0xf8, 4)
	r := bytes.NewReader(data)

	info, err := Inspect(r, r.Size())
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if got, want := info.KF8Skeletons, []KF8Skeleton{{
		Index:         0,
		Name:          "SKEL0000000000",
		FragmentCount: 1,
		Start:         0,
		Length:        100,
	}}; !equalKF8Skeletons(got, want) {
		t.Fatalf("KF8Skeletons = %+v; want %+v", got, want)
	}
	if got, want := info.KF8Fragments, []KF8Fragment{{
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
		{name: "bad offset", data: append(append([]byte("FDST"), testfixture.MOBIUint32(16)...), testfixture.MOBIUint32(0)...)},
		{name: "truncated table", data: append(append([]byte("FDST"), testfixture.MOBIUint32(12)...), testfixture.MOBIUint32(1)...)},
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
		azw4        bool
		wantClass   string
		wantFeature string
	}{
		{
			name: "huff cdic",
			data: testfixture.BuildMOBI(testfixture.MOBIOptions{
				Codepage:    65001,
				Title:       "Compressed",
				Compression: mobiCompressionHUFFCDIC,
			}),
			wantClass:   "mobi6",
			wantFeature: "huff-cdic-compression",
		},
		{
			name: "dictionary",
			data: testfixture.BuildMOBI(testfixture.MOBIOptions{
				Codepage:        65001,
				Title:           "Dictionary",
				FirstImageIndex: 2,
				ExtraRecords:    [][]byte{[]byte("INFL index")},
			}),
			wantClass:   "dictionary",
			wantFeature: "dictionary-indexes",
		},
		{
			name: "sample book",
			data: testfixture.BuildMOBI(testfixture.MOBIOptions{
				Codepage: 65001,
				Title:    "Sample",
				EXTH:     []testfixture.MOBIEXTHRecord{{Type: 501, Value: []byte("EBSP")}},
			}),
			wantClass: "sample-book",
		},
		{
			name:        "azw4 pdf",
			data:        append(testfixture.MOBIWithMetadata(65001, "Print Replica", nil), []byte("%PDF-1.7\nbody\n%%EOF")...),
			azw4:        true,
			wantClass:   "azw4-pdf-wrapper",
			wantFeature: "azw4-print-replica",
		},
		{
			name: "encrypted",
			data: testfixture.BuildMOBI(testfixture.MOBIOptions{
				Codepage:   65001,
				Title:      "DRM",
				Encryption: 1,
			}),
			wantClass:   "encrypted",
			wantFeature: "encrypted",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := bytes.NewReader(tt.data)
			inspect := Inspect
			if tt.azw4 {
				inspect = InspectAZW4
			}
			info, err := inspect(r, r.Size())
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			if info == nil {
				t.Fatal("Inspect returned nil")
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
	exthSubject := testfixture.BuildMOBI(testfixture.MOBIOptions{
		Codepage: 65001,
		Title:    "Novel About Dictionaries",
		EXTH: []testfixture.MOBIEXTHRecord{
			{Type: 105, Value: []byte("Dictionaries")},
		},
	})
	mobiType := testfixture.SetMOBIRecord0Uint32(t, testfixture.MOBIWithMetadata(65001, "Old Dictionary", nil), 24, mobiTypeDictionary)

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
			info, err := Inspect(r, r.Size())
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			if info == nil {
				t.Fatal("Inspect returned nil")
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
	record0 := testfixture.PalmDOCHeader(2)
	binary.BigEndian.PutUint16(record0[12:14], 1)
	data := testfixture.PalmDB("DRM PalmDOC", "TEXtREAd", record0)
	r := bytes.NewReader(data)

	info, err := Inspect(r, r.Size())
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if info == nil {
		t.Fatal("Inspect returned nil")
	}
	if info.SourceClass != "encrypted-palmdoc" || info.Container != "palmdoc" || info.Kind != KindPalmDOC {
		t.Fatalf("unexpected PalmDOC info: %+v", info)
	}
	if !containsString(info.UnsupportedFeatures, "encrypted") {
		t.Fatalf("UnsupportedFeatures = %+v; want encrypted", info.UnsupportedFeatures)
	}
}

func TestInspectKindleUnknownContainer(t *testing.T) {
	r := bytes.NewReader([]byte("not a PalmDB file"))
	info, err := Inspect(r, r.Size())
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if info != nil {
		t.Fatalf("Inspect = %+v; want nil", info)
	}
}

func TestExtractKindleDocumentMOBI6PalmDOC(t *testing.T) {
	for _, label := range []string{"EBOK", "PDOC"} {
		t.Run(label, func(t *testing.T) {
			text := []byte(`<html><body><p>Hello Kindle</p><img recindex="00001"></body></html>`)
			data := testfixture.BuildMOBI(testfixture.MOBIOptions{
				Codepage:        65001,
				Title:           "Readable MOBI",
				Compression:     mobiCompressionPalmDOC,
				HeaderLength:    0xe4,
				TextRecords:     [][]byte{text},
				TextLength:      uint32(len(text)),
				MOBIVersion:     6,
				FirstImageIndex: 2,
				EXTH: []testfixture.MOBIEXTHRecord{
					{Type: 100, Value: []byte("Doe, Jane")},
					{Type: 201, Value: testfixture.MOBIUint32(0)},
					{Type: 501, Value: []byte(label)},
				},
				ExtraRecords: [][]byte{tinyPNG, []byte("FLIS control")},
			})
			r := bytes.NewReader(data)

			doc, err := ExtractDocument(r, r.Size())
			if err != nil {
				t.Fatalf("ExtractDocument: %v", err)
			}
			if doc.Metadata == nil || doc.Metadata.Title != "Readable MOBI" || len(doc.Metadata.Authors) != 1 || doc.Metadata.Authors[0].Name != "Jane Doe" {
				t.Fatalf("metadata = %+v; want title and EXTH author", doc.Metadata)
			}
			if len(doc.Flows) != 1 {
				t.Fatalf("flows = %+v; want one flow", doc.Flows)
			}
			if flow := doc.Flows[0]; flow.MediaType != "text/html" || !bytes.Equal(flow.Data, text) {
				t.Fatalf("flow = %+v; want extracted HTML text", flow)
			}
			if len(doc.Resources) != 1 {
				t.Fatalf("resources = %+v; want one image resource", doc.Resources)
			}
			res := doc.Resources[0]
			if res.EmbedIndex != 1 || res.MediaType != "image/png" || !bytes.Equal(res.Data, tinyPNG) {
				t.Fatalf("resource = %+v; want the PNG referenced by recindex 1", res)
			}
			if !res.Cover || res.ID == "" {
				t.Fatalf("resource = %+v; want the extracted cover", res)
			}
		})
	}
}

func TestExtractKindleDocumentMOBI6Uncompressed(t *testing.T) {
	raw := []byte("<html><body><p>Caf\xe9 Kindle</p></body></html>")
	data := testfixture.BuildMOBI(testfixture.MOBIOptions{
		Codepage:    1252,
		Title:       "Uncompressed MOBI",
		Compression: mobiCompressionNone,
		TextRecords: [][]byte{raw},
		TextLength:  uint32(len(raw)),
		MOBIVersion: 6,
	})
	r := bytes.NewReader(data)

	doc, err := ExtractDocument(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractDocument: %v", err)
	}
	if got, want := string(doc.Flows[0].Data), "<html><body><p>Café Kindle</p></body></html>"; got != want {
		t.Fatalf("flow text = %q; want %q", got, want)
	}
}

func TestExtractKindleDocumentMOBI6MediaResources(t *testing.T) {
	text := []byte(`<html><body><img recindex="00001"><video mediarecindex="00002">Video fallback</video><audio mediarecindex="00003">Audio fallback</audio></body></html>`)
	video := []byte{0, 0, 0, 20, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}
	audio := []byte("ID3\x04\x00\x00tiny mp3")
	data := testfixture.BuildMOBI(testfixture.MOBIOptions{
		Codepage:        65001,
		Title:           "MOBI Media",
		MOBIVersion:     6,
		TextRecords:     [][]byte{text},
		TextLength:      uint32(len(text)),
		FirstImageIndex: 2,
		ExtraRecords: [][]byte{
			tinyPNG,
			testKindleMediaRecord("VIDE", video),
			testKindleMediaRecord("AUDI", audio),
		},
	})
	r := bytes.NewReader(data)

	doc, err := ExtractDocument(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractDocument: %v", err)
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
	data := testfixture.BuildMOBI(testfixture.MOBIOptions{
		Codepage:        65001,
		Title:           "Broken MOBI Media",
		MOBIVersion:     6,
		TextRecords:     [][]byte{text},
		TextLength:      uint32(len(text)),
		FirstImageIndex: 2,
		ExtraRecords:    [][]byte{testKindleMediaRecord("AUDI", []byte("not an MP3"))},
	})
	r := bytes.NewReader(data)

	_, err := ExtractDocument(r, r.Size())
	if !errors.Is(err, ErrUnsupportedSource) {
		t.Fatalf("ExtractDocument error = %v; want ErrUnsupportedSource", err)
	}
}

func TestExtractKindleDocumentPalmDOC(t *testing.T) {
	for _, tt := range []struct {
		name        string
		Compression uint16
		text        []byte
		want        string
	}{
		{
			name:        "uncompressed",
			Compression: mobiCompressionNone,
			text:        []byte("Palm text\nCaf\xe9"),
			want:        "Palm text\nCafé",
		},
		{
			name:        "palmdoc compression",
			Compression: mobiCompressionPalmDOC,
			text:        []byte("Compressed Palm text"),
			want:        "Compressed Palm text",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := testfixture.PalmDOCWithText("Palm Export", tt.Compression, [][]byte{tt.text}, uint32(len(tt.text)))
			r := bytes.NewReader(data)

			doc, err := ExtractDocument(r, r.Size())
			if err != nil {
				t.Fatalf("ExtractDocument: %v", err)
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
	data := testfixture.PalmDOCWithResources(
		"Database Fallback",
		mobiCompressionPalmDOC,
		[][]byte{text},
		uint32(len(text)),
		[][]byte{testPalmDOCBMP(t)},
	)
	r := bytes.NewReader(data)

	doc, err := ExtractDocument(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractDocument: %v", err)
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
	for _, label := range []string{"EBOK", "PDOC"} {
		t.Run(label, func(t *testing.T) {
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
			data := testfixture.BuildMOBI(testfixture.MOBIOptions{
				Codepage:     65001,
				Title:        "KF8 Export",
				HeaderLength: 0x108,
				MOBIVersion:  8,
				TextRecords:  [][]byte{text},
				TextLength:   uint32(len(text)),
				ExtraRecords: extraRecords,
				EXTH:         []testfixture.MOBIEXTHRecord{{Type: 501, Value: []byte(label)}},
			})
			data = testfixture.SetMOBIRecord0Uint32(t, data, 0xfc, 2)
			data = testfixture.SetMOBIRecord0Uint32(t, data, 0xf8, 4)
			data = testfixture.SetMOBIRecord0Uint32(t, data, 0xf4, navIndex)
			r := bytes.NewReader(data)

			doc, err := ExtractDocument(r, r.Size())
			if err != nil {
				t.Fatalf("ExtractDocument: %v", err)
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
			if root.Label != "Part One" || root.Href != doc.Flows[0].Href+"#filepos12" {
				t.Fatalf("root = %+v; want Part One at filepos12", root)
			}
			if len(root.Children) != 1 {
				t.Fatalf("root.Children = %+v; want one child", root.Children)
			}
			child := root.Children[0]
			if child.Label != "Chapter One" || child.Href != doc.Flows[0].Href+"#filepos15" {
				t.Fatalf("child = %+v; want Chapter One at filepos15", child)
			}
		})
	}
}

func TestExtractKindleDocumentComboKF8(t *testing.T) {
	for _, tc := range []struct {
		label            string
		compression      uint16
		badSecondaryEXTH bool
	}{{"EBOK", 1, false}, {"PDOC", 1, false}, {"EBOK", 99, false}, {"EBOK", 1, true}} {
		t.Run(fmt.Sprintf("%s/primary-compression-%d/bad-secondary-EXTH-%t", tc.label, tc.compression, tc.badSecondaryEXTH), func(t *testing.T) {
			data := testComboKF8(t, tc.label)
			start := int(binary.BigEndian.Uint32(data[78:82]))
			binary.BigEndian.PutUint16(data[start:start+2], tc.compression)
			if tc.badSecondaryEXTH {
				// Optional EXTH damage must not hide usable KF8 text and indexes.
				secondary := int(binary.BigEndian.Uint32(data[78+6*8:]))
				binary.BigEndian.PutUint32(data[secondary+20:], 0xffff)
			}
			r := bytes.NewReader(data)

			doc, err := ExtractDocument(r, r.Size())
			if err != nil {
				t.Fatalf("ExtractDocument: %v", err)
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
			if len(doc.Resources) != 3 {
				t.Fatalf("shared combo resources = %+v; want image, video, and audio before boundary", doc.Resources)
			}
			for i, want := range []struct {
				mediaType string
				data      []byte
			}{{"image/png", tinyPNG}, {"video/mp4", []byte{0, 0, 0, 20, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}}, {"audio/mpeg", []byte("ID3\x04\x00\x00tiny mp3")}} {
				index := slices.IndexFunc(doc.Resources, func(res Resource) bool { return res.EmbedIndex == i+1 })
				if index < 0 {
					t.Fatalf("missing shared resource for kindle:embed:%04d", i+1)
				}
				res := doc.Resources[index]
				if res.MediaType != want.mediaType || !bytes.Equal(res.Data, want.data) {
					t.Fatalf("resource %d = %+v; want original %s payload", i+1, res, want.mediaType)
				}
				if i == 0 && (!res.Cover || res.ID == "") {
					t.Fatalf("resource = %+v; want the shared cover image", res)
				}
			}
			if len(doc.Navigation) != 1 {
				t.Fatalf("Navigation = %+v; want one root", doc.Navigation)
			}
			root := doc.Navigation[0]
			if root.Label != "Part One" || root.Href != doc.Flows[0].Href+"#filepos12" {
				t.Fatalf("root = %+v; want Part One at filepos12", root)
			}
			if len(root.Children) != 1 {
				t.Fatalf("root.Children = %+v; want one child", root.Children)
			}
			child := root.Children[0]
			if child.Label != "Chapter One" || child.Href != doc.Flows[0].Href+"#filepos18" {
				t.Fatalf("child = %+v; want Chapter One at filepos18", child)
			}
		})
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
	data := testfixture.BuildMOBI(testfixture.MOBIOptions{
		Codepage:     65001,
		Title:        "KF8 CSS",
		HeaderLength: 0x108,
		MOBIVersion:  8,
		TextRecords:  [][]byte{raw},
		TextLength:   uint32(len(raw)),
		ExtraRecords: append(append(skelRecords, testKindleFragmentRecordsWith(0, "body", 0, 0, 0, 0)...), fdstRecord),
	})
	data = testfixture.SetMOBIRecord0Uint32(t, data, 0xfc, 2)
	data = testfixture.SetMOBIRecord0Uint32(t, data, 0xf8, 4)
	data = testfixture.SetMOBIRecord0Uint32(t, data, 0xc0, 7)
	data = testfixture.SetMOBIRecord0Uint32(t, data, 0xc4, 2)
	r := bytes.NewReader(data)

	doc, err := ExtractDocument(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractDocument: %v", err)
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
	data := testfixture.BuildMOBI(testfixture.MOBIOptions{
		Codepage:     65001,
		Title:        "KF8 SVG",
		HeaderLength: 0x108,
		MOBIVersion:  8,
		TextRecords:  [][]byte{raw},
		TextLength:   uint32(len(raw)),
		ExtraRecords: append(append(skelRecords, testKindleFragmentRecordsWith(0, "body", 0, 0, 0, 0)...), fdstRecord),
	})
	data = testfixture.SetMOBIRecord0Uint32(t, data, 0xfc, 2)
	data = testfixture.SetMOBIRecord0Uint32(t, data, 0xf8, 4)
	data = testfixture.SetMOBIRecord0Uint32(t, data, 0xc0, 7)
	data = testfixture.SetMOBIRecord0Uint32(t, data, 0xc4, 2)
	r := bytes.NewReader(data)

	doc, err := ExtractDocument(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractDocument: %v", err)
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
	data := testfixture.BuildMOBI(testfixture.MOBIOptions{
		Codepage:        65001,
		Title:           "KF8 Font",
		HeaderLength:    0x108,
		MOBIVersion:     8,
		TextRecords:     [][]byte{text},
		TextLength:      uint32(len(text)),
		FirstImageIndex: fontRecordIndex,
		ExtraRecords:    extraRecords,
	})
	data = testfixture.SetMOBIRecord0Uint32(t, data, 0xfc, 2)
	data = testfixture.SetMOBIRecord0Uint32(t, data, 0xf8, 4)
	r := bytes.NewReader(data)

	doc, err := ExtractDocument(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractDocument: %v", err)
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
	data := testfixture.BuildMOBI(testfixture.MOBIOptions{
		Codepage:    65001,
		Title:       "Guided MOBI",
		Compression: mobiCompressionPalmDOC,
		TextRecords: [][]byte{text},
		TextLength:  uint32(len(text)),
		MOBIVersion: 6,
		EXTH: []testfixture.MOBIEXTHRecord{
			{Type: 501, Value: []byte("EBOK")},
		},
	})
	r := bytes.NewReader(data)

	doc, err := ExtractDocument(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractDocument: %v", err)
	}
	if len(doc.Guide) != 3 {
		t.Fatalf("Guide = %+v; want three unique references", doc.Guide)
	}
	want := []GuideReference{
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
	data := testfixture.BuildMOBI(testfixture.MOBIOptions{
		Codepage:    65001,
		Title:       "NCX MOBI",
		Compression: mobiCompressionPalmDOC,
		TextRecords: [][]byte{text},
		TextLength:  uint32(len(text)),
		MOBIVersion: 6,
		ExtraRecords: [][]byte{
			navRecords[0],
			navRecords[1],
			navRecords[2],
		},
	})
	data = testfixture.SetMOBIRecord0Uint32(t, data, 0xf4, 2)
	r := bytes.NewReader(data)

	doc, err := ExtractDocument(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractDocument: %v", err)
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
	data := testfixture.BuildMOBI(testfixture.MOBIOptions{
		Codepage:    1252,
		Title:       "Compressed Text",
		Compression: mobiCompressionPalmDOC,
		TextRecords: [][]byte{
			compressed,
		},
		TextLength:  uint32(len([]byte("Caf\xe9 iabcabc"))),
		MOBIVersion: 6,
	})
	r := bytes.NewReader(data)

	doc, err := ExtractDocument(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractDocument: %v", err)
	}
	if got := string(doc.Flows[0].Data); got != text {
		t.Fatalf("text = %q; want %q", got, text)
	}
}

func TestExtractKindleDocumentTrimsTrailingEntries(t *testing.T) {
	data := testfixture.BuildMOBI(testfixture.MOBIOptions{
		Codepage:      65001,
		Title:         "Trailing Text",
		Compression:   mobiCompressionPalmDOC,
		TextRecords:   [][]byte{[]byte("plain\x00\x81")},
		TextLength:    5,
		MOBIVersion:   6,
		TrailingFlags: 3,
	})
	r := bytes.NewReader(data)

	doc, err := ExtractDocument(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractDocument: %v", err)
	}
	if got := string(doc.Flows[0].Data); got != "plain" {
		t.Fatalf("text = %q; want trailing bytes removed", got)
	}
}

func TestExtractKindleDocumentRejectsUnsupportedSources(t *testing.T) {
	for _, tt := range []struct {
		name   string
		opts   testfixture.MOBIOptions
		reason string
	}{
		{
			name: "encrypted personal document",
			opts: testfixture.MOBIOptions{
				Encryption: 1,
				EXTH:       []testfixture.MOBIEXTHRecord{{Type: 501, Value: []byte("PDOC")}},
			},
			reason: "encrypted",
		},
		{
			name: "dictionary personal document",
			opts: testfixture.MOBIOptions{
				FirstImageIndex: 2,
				EXTH:            []testfixture.MOBIEXTHRecord{{Type: 501, Value: []byte("PDOC")}},
				ExtraRecords:    [][]byte{[]byte("INFL index")},
			},
			reason: "dictionary",
		},
		{
			name:   "sample book",
			opts:   testfixture.MOBIOptions{EXTH: []testfixture.MOBIEXTHRecord{{Type: 501, Value: []byte("EBSP")}}},
			reason: "cdetype EBSP",
		},
		{
			name:   "missing HUFF CDIC tables",
			opts:   testfixture.MOBIOptions{Compression: mobiCompressionHUFFCDIC},
			reason: "HUFF/CDIC",
		},
		{
			name: "malformed combo KF8 header",
			opts: testfixture.MOBIOptions{
				EXTH:         []testfixture.MOBIEXTHRecord{{Type: 121, Value: testfixture.MOBIUint32(3)}},
				ExtraRecords: [][]byte{[]byte("BOUNDARY"), []byte("kf8 placeholder")},
			},
			reason: "invalid combo KF8 header",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.opts.Codepage = 65001
			tt.opts.MOBIVersion = 6
			tt.opts.TextRecords = [][]byte{[]byte("<html><body>Readable fallback</body></html>")}
			r := bytes.NewReader(testfixture.BuildMOBI(tt.opts))
			doc, err := ExtractDocument(r, r.Size())
			if !errors.Is(err, ErrUnsupportedSource) || !strings.Contains(err.Error(), tt.reason) {
				t.Fatalf("ExtractDocument error = %v; want unsupported source because of %q", err, tt.reason)
			}
			if doc != nil {
				t.Fatal("rejected source returned a partial document")
			}
		})
	}
}

func TestExtractKindleDocumentRejectsMalformedPalmDOCCompression(t *testing.T) {
	data := testfixture.BuildMOBI(testfixture.MOBIOptions{
		Codepage:    65001,
		Title:       "Broken",
		Compression: mobiCompressionPalmDOC,
		TextRecords: [][]byte{
			{0x80},
		},
		TextLength:  1,
		MOBIVersion: 6,
	})
	r := bytes.NewReader(data)

	_, err := ExtractDocument(r, r.Size())
	if !errors.Is(err, ErrUnsupportedSource) || !strings.Contains(err.Error(), "back-reference overruns record") {
		t.Fatalf("ExtractDocument error = %v; want malformed PalmDOC error", err)
	}
}

func testKindleFDSTRecord(sections ...[2]uint32) []byte {
	out := []byte("FDST")
	out = append(out, testfixture.MOBIUint32(12)...)
	out = append(out, testfixture.MOBIUint32(uint32(len(sections)))...)
	for _, section := range sections {
		out = append(out, testfixture.MOBIUint32(section[0])...)
		out = append(out, testfixture.MOBIUint32(section[1])...)
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
	out = append(out, testfixture.MOBIUint32(uint32(len(payload)))...)
	out = append(out, testfixture.MOBIUint32(0x0003)...)
	out = append(out, testfixture.MOBIUint32(uint32(24+len(key)))...)
	out = append(out, testfixture.MOBIUint32(uint32(len(key)))...)
	out = append(out, testfixture.MOBIUint32(24)...)
	out = append(out, key...)
	out = append(out, fontData...)
	return out
}

func testKindleMediaRecord(magic string, payload []byte) []byte {
	out := []byte(magic)
	out = append(out, testfixture.MOBIUint32(12)...)
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

func equalFDSTSections(a, b []FDSTSection) bool {
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

func equalKF8Skeletons(a, b []KF8Skeleton) bool {
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

func equalKF8Fragments(a, b []KF8Fragment) bool {
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

var tinyPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
	0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41,
	0x54, 0x08, 0xd7, 0x63, 0xf8, 0xff, 0xff, 0x3f,
	0x00, 0x05, 0xfe, 0x02, 0xfe, 0xdc, 0xcc, 0x59,
	0xe7, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e,
	0x44, 0xae, 0x42, 0x60, 0x82,
}

func testComboKF8(t *testing.T, label string) []byte {
	t.Helper()
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
	kf8 := testfixture.BuildMOBI(testfixture.MOBIOptions{
		Codepage:     65001,
		Title:        "KF8 Part",
		HeaderLength: 0x108,
		MOBIVersion:  8,
		TextRecords:  [][]byte{text},
		TextLength:   uint32(len(text)),
		ExtraRecords: kf8ExtraRecords,
		EXTH:         []testfixture.MOBIEXTHRecord{{Type: 501, Value: []byte(label)}},
	})
	kf8 = testfixture.SetMOBIRecord0Uint32(t, kf8, 0xfc, 2)
	kf8 = testfixture.SetMOBIRecord0Uint32(t, kf8, 0xf8, 4)
	kf8 = testfixture.SetMOBIRecord0Uint32(t, kf8, 0xf4, kf8NavIndex)
	kf8Records := testfixture.PalmDBRecordBodies(t, kf8)
	video := []byte{0, 0, 0, 20, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}
	audio := []byte("ID3\x04\x00\x00tiny mp3")
	extraRecords := [][]byte{
		tinyPNG,
		testKindleMediaRecord("VIDE", video),
		testKindleMediaRecord("AUDI", audio),
		[]byte("BOUNDARY"),
	}
	extraRecords = append(extraRecords, kf8Records...)
	data := testfixture.BuildMOBI(testfixture.MOBIOptions{
		Codepage:        65001,
		Title:           "Combo Export",
		MOBIVersion:     6,
		TextRecords:     [][]byte{[]byte("<html><body>Legacy MOBI6</body></html>")},
		FirstImageIndex: 2,
		EXTH: []testfixture.MOBIEXTHRecord{
			{Type: 121, Value: testfixture.MOBIUint32(6)},
			{Type: 501, Value: []byte(label)},
		},
		ExtraRecords: extraRecords,
	})
	return data
}
