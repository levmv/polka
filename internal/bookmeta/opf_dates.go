package bookmeta

import (
	"encoding/xml"
	"strings"
)

type OPFDateRecord struct {
	Text  string
	Event string
}

const (
	OPFDateOther = iota
	OPFDatePublication
	OPFDateUnqualified
	OPFDateOriginalPublication
	OPFDateOPSPublication
)

func opfDateEvent(attrs []xml.Attr) string {
	// Prefer OPF, then its OEB predecessor, then the unqualified legacy spelling.
	for _, namespace := range []string{
		"http://www.idpf.org/2007/opf",
		"http://openebook.org/namespaces/oeb-package/1.0/",
		"",
	} {
		for _, attr := range attrs {
			if attr.Name == (xml.Name{Space: namespace, Local: "event"}) {
				return attr.Value
			}
		}
	}
	return ""
}

// The same publication categories govern import and explicit date edits.
// Unknown events and technical dates never stand in for a publication date.
func OPFDatePriority(event string) int {
	switch strings.ToLower(strings.TrimSpace(event)) {
	case "publication":
		return OPFDatePublication
	case "":
		return OPFDateUnqualified
	case "original-publication":
		return OPFDateOriginalPublication
	case "ops-publication":
		return OPFDateOPSPublication
	default:
		return OPFDateOther
	}
}

func OPFPublicationDate(dates []OPFDateRecord) string {
	var best string
	var bestPriority int
	for _, record := range dates {
		priority := OPFDatePriority(record.Event)
		if priority == OPFDateOther || best != "" && priority >= bestPriority {
			continue
		}
		if date := NormalizeMetadataDate(record.Text); date != "" {
			best, bestPriority = date, priority
		}
	}
	return best
}
