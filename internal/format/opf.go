package format

import (
	"encoding/xml"
	"strings"

	"github.com/levmv/polka/internal/bookmeta"
)

// EPUB package structure stays with container handling; metadata is shared with
// sidecars and PalmDOC through bookmeta.
type opfDoc struct {
	Metadata bookmeta.OPFMetadata `xml:"metadata"`
	Manifest opfManifest          `xml:"manifest"`
	Spine    opfSpine             `xml:"spine"`
	Guide    opfGuide             `xml:"guide"`
	rootName xml.Name
}

func (d *opfDoc) UnmarshalXML(dec *xml.Decoder, start xml.StartElement) error {
	d.rootName = start.Name
	if strings.EqualFold(start.Name.Local, "metadata") || strings.EqualFold(start.Name.Local, "dc-metadata") {
		var metadata bookmeta.OPFMetadata
		if err := dec.DecodeElement(&metadata, &start); err != nil {
			return err
		}
		d.Metadata = metadata
		return nil
	}

	var doc struct {
		Metadata       bookmeta.OPFMetadata `xml:"metadata"`
		LegacyMetadata bookmeta.OPFMetadata `xml:"dc-metadata"`
		Manifest       opfManifest          `xml:"manifest"`
		Spine          opfSpine             `xml:"spine"`
		Guide          opfGuide             `xml:"guide"`
	}
	if err := dec.DecodeElement(&doc, &start); err != nil {
		return err
	}
	d.Metadata = doc.Metadata
	if d.Metadata.Empty() {
		d.Metadata = doc.LegacyMetadata
	}
	d.Manifest = doc.Manifest
	d.Spine = doc.Spine
	d.Guide = doc.Guide
	return nil
}

type opfManifest struct {
	Items []opfItem `xml:"item"`
}

type opfSpine struct {
	Itemrefs []opfItemref `xml:"itemref"`
	TOC      string       `xml:"toc,attr"`
	PageMap  string       `xml:"page-map,attr"`
}

type opfItemref struct {
	IDRef      string `xml:"idref,attr"`
	Linear     string `xml:"linear,attr"`
	Properties string `xml:"properties,attr"`
}

type opfGuide struct {
	References []opfGuideReference `xml:"reference"`
}

type opfGuideReference struct {
	Type string `xml:"type,attr"`
	Href string `xml:"href,attr"`
}

type opfItem struct {
	ID         string `xml:"id,attr"`
	Href       string `xml:"href,attr"`
	MediaType  string `xml:"media-type,attr"`
	Properties string `xml:"properties,attr"`
}
