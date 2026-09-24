package format

import (
	"bytes"
	"encoding/xml"
	"strings"
	"testing"
	"time"
)

func TestRewriteOPFMetadataPreservesDates(t *testing.T) {
	for _, tc := range []struct {
		name, version, records, date string
		dateRecords                  []string
	}{
		{
			name: "legacy events and partial date", version: "2.0", date: "2001-06",
			dateRecords: []string{
				`<dc:date opf:event="publication">June 2001</dc:date>`,
				`<dc:date opf:event="modification">2014-05-08</dc:date>`,
			},
		},
		{
			name: "timestamp and refinements", version: "3.0", date: "2001-06-15",
			dateRecords: []string{
				`<dc:date id="polka-title">2001-06-15T23:00:00-04:00</dc:date>`,
				`<meta refines="#date-note" property="alternate-script" xml:lang="fr">Parution</meta>`,
				`<meta id="date-note" refines="#polka-title" property="source-of">Publication</meta>`,
			},
		},
		{
			name: "multiple EPUB3 dates", version: "3.0", date: "2022-03-28",
			dateRecords: []string{
				`<dc:date>2022-03-28</dc:date>`,
				`<dc:date><![CDATA[1998]]></dc:date>`,
				`<dc:date>unknown</dc:date>`,
			},
		},
		{
			name: "unrecognized date", version: "2.0",
			dateRecords: []string{`<dc:date opf:event="publication">unknown</dc:date>`},
		},
		{
			name: "only unrelated events", version: "2.0",
			dateRecords: []string{
				`<dc:date opf:event="creation">1967</dc:date>`,
				`<dc:date opf:event="unknown">1998</dc:date>`,
			},
		},
		{
			name: "nested legacy dates", version: "1.0", date: "1998",
			records: `<dc-metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
  <dc:date id="publication">1998</dc:date>
  <dc:rights>Keep rights</dc:rights>
</dc-metadata>
<x-metadata><meta refines="#publication" property="source-of">Publication</meta></x-metadata>
<dc:date>1990</dc:date>`,
			dateRecords: []string{
				`<dc:date id="publication">1998</dc:date>`,
				`<meta refines="#publication" property="source-of">Publication</meta>`,
				`<dc:date>1990</dc:date>`,
				`<dc:rights>Keep rights</dc:rights>`,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			records := tc.records
			if records == "" {
				records = strings.Join(tc.dateRecords, "\n    ")
			}
			const independentDate = `<meta property="dcterms:created">2020-01-02T03:04:05Z</meta>`
			src := opfDateTestPackage(tc.version, records+"\n"+independentDate)
			current, err := ParseOPF(bytes.NewReader(src))
			if err != nil || current == nil || current.Date != tc.date {
				t.Fatalf("source metadata = %+v, %v; want date %q", current, err, tc.date)
			}
			meta := *current
			meta.Tags = []string{"New tag"}
			modified := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
			out, err := RewriteOPFMetadata(src, meta, modified)
			if err != nil {
				t.Fatal(err)
			}
			opf := string(out)
			if got, want := strings.Count(opf, "<dc:date"), strings.Count(records, "<dc:date"); got != want {
				t.Errorf("date records = %d; want %d:\n%s", got, want, opf)
			}
			for _, record := range append(tc.dateRecords, independentDate) {
				if !strings.Contains(opf, record) {
					t.Errorf("source record lost: %s\n%s", record, opf)
				}
			}
			assertOPFReferences(t, opf)
			got, err := ParseOPF(bytes.NewReader(out))
			if err != nil || got == nil || got.Date != meta.Date {
				t.Fatalf("metadata after tag edit = %+v, %v; want date %q", got, err, meta.Date)
			}
			again, err := RewriteOPFMetadata(out, meta, modified)
			if err != nil || !bytes.Equal(out, again) {
				t.Fatalf("repeated write changed the OPF: %v", err)
			}
		})
	}
}

func TestRewriteOPFPublicationDateChanges(t *testing.T) {
	primary := []string{
		`<dc:date id="publication" opf:event="publication">June 2001</dc:date>`,
		`<dc:date opf:event="publication">unknown</dc:date>`,
		`<dc:date>1990</dc:date>`,
		`<dc:date event=" ">unknown</dc:date>`,
		`<meta refines="#date-note" property="alternate-script">Publication note</meta>`,
		`<meta id="date-note" refines="#publication" property="source-of">Publication</meta>`,
	}
	fallbacks := []string{
		`<dc:date id="original" opf:event="original-publication">1998</dc:date>`,
		`<dc:date opf:event="ops-publication">2008-09-18</dc:date>`,
		`<dc:date opf:event="original-publication">unknown</dc:date>`,
		`<meta refines="#original" property="source-of">Original edition</meta>`,
	}
	other := []string{
		`<dc:date id="creation" opf:event="creation" event="publication">1967-05-08</dc:date>`,
		`<meta refines="#creation" property="source-of">Written</meta>`,
		`<dc:date opf:event="modification">2014-05-08</dc:date>`,
		`<dc:date event=" CONVERSION ">2015</dc:date>`,
		`<dc:date opf:event="pre-publication">1999</dc:date>`,
		`<meta property="dcterms:created">2020-01-02T03:04:05Z</meta>`,
		`<meta property="dcterms:issued">2010</meta>`,
		`<dc:rights>Keep rights</dc:rights>`,
	}
	for _, tc := range []struct {
		name, version   string
		primary, nested bool
	}{
		{name: "EPUB2 dates", version: "2.0", primary: true},
		{name: "EPUB3 dates", version: "3.0", primary: true},
		{name: "only legacy publication", version: "2.0"},
		{name: "nested legacy containers", version: "1.0", primary: true, nested: true},
	} {
		version := tc.version
		for _, date := range []string{"2024-03", ""} {
			t.Run(tc.name+"/date="+date, func(t *testing.T) {
				var publications string
				if tc.primary {
					publications = strings.Join(primary, "\n") + "\n"
				}
				publications += strings.Join(fallbacks, "\n")
				rest := strings.Join(other, "\n")
				if tc.nested {
					publications = `<dc-metadata xmlns:opf="http://www.idpf.org/2007/opf">` + publications + `</dc-metadata>`
					rest = `<x-metadata xmlns:opf="http://www.idpf.org/2007/opf">` + rest + `</x-metadata>`
				}
				src := opfDateTestPackage(version, publications+"\n"+rest)
				if tc.nested {
					// The replacement must also work when only legacy containers
					// declare the OPF prefix.
					src = bytes.Replace(src, []byte(` xmlns:opf="http://www.idpf.org/2007/opf"`), nil, 1)
				}
				meta, err := ParseOPF(bytes.NewReader(src))
				if err != nil {
					t.Fatal(err)
				}
				meta.Date = date
				meta.Tags = []string{"Edited"}
				modified := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
				out, err := RewriteOPFMetadata(src, *meta, modified)
				if err != nil {
					t.Fatal(err)
				}
				for _, record := range primary {
					if bytes.Contains(out, []byte(record)) {
						t.Errorf("old primary date or refinement retained: %s\n%s", record, out)
					}
				}
				for _, record := range fallbacks {
					if kept := bytes.Contains(out, []byte(record)); kept != (date != "") {
						t.Errorf("legacy publication retained = %v; want %v: %s\n%s", kept, date != "", record, out)
					}
				}
				for _, record := range other {
					if !bytes.Contains(out, []byte(record)) {
						t.Errorf("unrelated record lost: %s\n%s", record, out)
					}
				}
				assertOPFReferences(t, string(out))
				got, err := ParseOPF(bytes.NewReader(out))
				if err != nil || got == nil || got.Date != date {
					t.Fatalf("metadata after rewrite = %+v, %v; want date %q", got, err, date)
				}
				if date != "" {
					assertFirstOPFDate(t, out, date, version)
				}
				again, err := RewriteOPFMetadata(out, *meta, modified)
				if err != nil || !bytes.Equal(out, again) {
					t.Fatalf("repeated write changed the OPF: %v\n%s\n%s", err, out, again)
				}
			})
		}
	}
}

func assertFirstOPFDate(t *testing.T, raw []byte, date, version string) {
	t.Helper()
	// Check the actual XML namespaces independently of Polka's tolerant reader.
	dec := xml.NewDecoder(bytes.NewReader(raw))
	for {
		token, err := dec.Token()
		if err != nil {
			t.Fatalf("find publication date: %v", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "date" {
			continue
		}
		if start.Name.Space != "http://purl.org/dc/elements/1.1/" {
			t.Fatalf("date namespace = %q; want Dublin Core", start.Name.Space)
		}
		var record struct {
			Text  string `xml:",chardata"`
			Event string `xml:"http://www.idpf.org/2007/opf event,attr"`
		}
		if err := dec.DecodeElement(&record, &start); err != nil {
			t.Fatal(err)
		}
		wantEvent := ""
		if version != "3.0" {
			wantEvent = "publication"
		}
		if record.Text != date || record.Event != wantEvent {
			t.Fatalf("first date = %+v; want %q with event %q", record, date, wantEvent)
		}
		return
	}
}
