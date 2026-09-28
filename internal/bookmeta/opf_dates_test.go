package bookmeta

import (
	"bytes"
	"testing"
)

func opfDateTestPackage(version, records string) []byte {
	return []byte(`<package xmlns="http://www.idpf.org/2007/opf" version="` + version + `">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <dc:title>Book</dc:title>
    ` + records + `
  </metadata>
</package>`)
}

func TestOPFPublicationDate(t *testing.T) {
	for _, tc := range []struct {
		name, records, want string
	}{
		{
			name: "publication outranks chronology and unqualified dates",
			records: `<dc:date opf:event="creation">1967-05-08</dc:date>
<dc:date opf:event="original-publication">1878</dc:date>
<dc:date>1990</dc:date>
<dc:date opf:event="publication">1995-03-23</dc:date>`,
			want: "1995-03-23",
		},
		{
			name: "first recognizable date within priority",
			records: `<dc:date opf:event="publication">unknown</dc:date>
<dc:date opf:event="publication">2022-03</dc:date>
<dc:date opf:event="publication">1998</dc:date>`,
			want: "2022-03",
		},
		{
			name: "unqualified fallback and document order",
			records: `<dc:date opf:event="publication">0101-01-01T00:00:00Z</dc:date>
<dc:date opf:event="original-publication">1878</dc:date>
<dc:date opf:event=" ">June 2001</dc:date>
<dc:date>1998</dc:date>`,
			want: "2001-06",
		},
		{
			name: "original publication before electronic publication",
			records: `<dc:date opf:event="ops-publication">2008-09-18</dc:date>
<dc:date opf:event="original-publication">1597</dc:date>`,
			want: "1597",
		},
		{
			name: "electronic publication fallback",
			records: `<dc:date opf:event="original-publication">unknown</dc:date>
<dc:date opf:event="ops-publication">2008-09-18</dc:date>`,
			want: "2008-09-18",
		},
		{
			name: "unrelated events and meta are not fallbacks",
			records: `<dc:date opf:event="creation">1967</dc:date>
<dc:date opf:event="modification">2026</dc:date>
<dc:date opf:event="conversion">2020</dc:date>
<dc:date opf:event="pre-publication">1999</dc:date>
<meta property="dcterms:issued">1998</meta>
<meta property="dcterms:modified">2026-07-29T00:00:00Z</meta>
<meta name="calibre:timestamp" content="2010-01-01T00:00:00Z"/>`,
		},
		{
			name: "namespaced event precedes unqualified fallback",
			records: `<dc:date opf:event="creation" event="publication">1967</dc:date>
<dc:date xmlns:p="http://www.idpf.org/2007/opf" event="creation" p:event=" Publication ">1995</dc:date>`,
			want: "1995",
		},
		{
			name: "plain event fallback ignores foreign attributes",
			records: `<dc:date xmlns:x="urn:example" event="creation" x:event="publication">1967</dc:date>
<dc:date event=" PUBLICATION ">1995</dc:date>`,
			want: "1995",
		},
		{
			name: "legacy containers preserve document order",
			records: `<dc-metadata><dc:date>2001</dc:date></dc-metadata>
<dc:date>1998</dc:date>
<x-metadata><dc:date>1990</dc:date></x-metadata>`,
			want: "2001",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opf := opfDateTestPackage("2.0", tc.records)
			meta, err := ParseOPF(bytes.NewReader(opf))
			if err != nil || meta == nil || meta.Date != tc.want {
				t.Fatalf("OPF metadata = %+v, %v; want date %q", meta, err, tc.want)
			}
		})
	}
}
