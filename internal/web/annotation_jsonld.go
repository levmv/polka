package web

import (
	"encoding/json/v2"
	"fmt"
	"io"
	"time"

	"github.com/levmv/polka/internal/db"
)

var annotationContext = []any{
	"http://www.w3.org/ns/anno.jsonld",
	map[string]any{
		"polka":         "urn:polka:terms:",
		"polka:locator": map[string]string{"@id": "polka:locator", "@type": "@json"},
	},
}

func webAnnotation(libraryID string, ann db.Annotation) map[string]any {
	publication := db.PublicationURI(libraryID, ann.AssetID)
	source := map[string]any{"id": db.ResourceURI(publication, ann.Locator.Path)}
	// The text selector addresses the chapter resource when its path is known.
	// Keep the reader CFI as an explicit extension, without presenting
	// a generated FB2/MOBI CFI as an EPUB package fragment.
	var selector any = map[string]any{"type": "TextQuoteSelector", "exact": ann.Quote, "prefix": ann.ContextBefore, "suffix": ann.ContextAfter}
	if ann.Locator.Page > 0 {
		selector = map[string]any{"type": "FragmentSelector", "value": fmt.Sprintf("page=%d", ann.Locator.Page),
			"conformsTo": "http://tools.ietf.org/rfc/rfc3778", "refinedBy": selector}
	}
	target := map[string]any{"type": "SpecificResource", "source": source, "selector": selector}
	document := map[string]any{
		"id": db.AnnotationURI(libraryID, ann.ID), "type": "Annotation", "motivation": "highlighting",
		"created":  time.Unix(ann.CreatedAt, 0).UTC().Format(time.RFC3339),
		"modified": time.Unix(ann.UpdatedAt, 0).UTC().Format(time.RFC3339),
		"target":   target, "polka:publication": map[string]any{"id": publication},
		"polka:locator": ann.Locator,
		"polka:color":   ann.Color, "polka:revision": ann.Revision,
	}
	if ann.Note != "" {
		document["body"] = map[string]any{"type": "TextualBody", "value": ann.Note, "format": "text/plain", "purpose": "commenting"}
	}
	return document
}

// An empty collection has no page: the W3C model requires at least one item
// in an AnnotationPage.
func writeAnnotationCollection(w io.Writer, libraryID, collectionID string, rows []db.Annotation) error {
	document := map[string]any{
		"@context": annotationContext,
		"id":       collectionID,
		"type":     "AnnotationCollection",
		"total":    len(rows),
	}
	if len(rows) > 0 {
		items := make([]map[string]any, len(rows))
		for i, ann := range rows {
			items[i] = webAnnotation(libraryID, ann)
		}
		document["first"] = map[string]any{
			"id": collectionID + ":page:1", "type": "AnnotationPage",
			"startIndex": 0, "items": items,
		}
	}
	return json.MarshalWrite(w, document, json.Deterministic(true))
}
