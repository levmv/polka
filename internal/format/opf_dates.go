package format

import (
	"encoding/xml"
	"strings"

	"github.com/levmv/polka/internal/bookmeta"
)

type opfDateRecord struct {
	Text  string
	Event string
}

const (
	opfDateOther = iota
	opfDatePublication
	opfDateUnqualified
	opfDateOriginalPublication
	opfDateOPSPublication
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
func opfDatePriority(event string) int {
	switch strings.ToLower(strings.TrimSpace(event)) {
	case "publication":
		return opfDatePublication
	case "":
		return opfDateUnqualified
	case "original-publication":
		return opfDateOriginalPublication
	case "ops-publication":
		return opfDateOPSPublication
	default:
		return opfDateOther
	}
}

func opfDate(dates []opfDateRecord) string {
	var best string
	var bestPriority int
	for _, record := range dates {
		priority := opfDatePriority(record.Event)
		if priority == opfDateOther || best != "" && priority >= bestPriority {
			continue
		}
		if date := bookmeta.NormalizeMetadataDate(record.Text); date != "" {
			best, bestPriority = date, priority
		}
	}
	return best
}
